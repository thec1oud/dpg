// Package graph implements pipeline.DependencyResolver. It performs
// topological sort using Kahn's algorithm and resolves circular FK dependencies
// via DEFERRABLE constraints.
package graph

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/pipeline"
)

func init() {
	pipeline.Default.Register(pipeline.KeyDependencyResolver, New())
}

// Resolver implements pipeline.DependencyResolver.
type Resolver struct{}

// New returns a Resolver.
func New() *Resolver { return &Resolver{} }

// Sort performs a topological sort of the IR objects, respecting the dependency
// edges described in RFC Phase 7. Circular FK dependencies that are all
// DEFERRABLE are resolved by emitting the tables without the circular FK and
// appending the FK as a deferred ALTER TABLE statement.
func (r *Resolver) Sort(objects []pipeline.IRObject) ([]pipeline.IRObject, error) {
	n := len(objects)
	if n == 0 {
		return nil, nil
	}

	edges, idx, diags := buildEdges(objects)

	// Circular FK edges that can be deferred.
	type deferredFK struct {
		table *ir.Table
		fk    *ir.Constraint
	}
	var deferred []deferredFK

	if diags.HasErrors() {
		return nil, diags
	}

	// Kahn's algorithm.
	// inDegree[i] = number of objects that must come before i.
	inDegree := make([]int, n)
	for i := range edges {
		for j := range edges[i] {
			inDegree[j]++
		}
	}

	var queue []int
	for i, d := range inDegree {
		if d == 0 {
			queue = append(queue, i)
		}
	}

	var sorted []pipeline.IRObject
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		sorted = append(sorted, objects[cur])
		var newlyReady []int
		for j := range edges[cur] {
			inDegree[j]--
			if inDegree[j] == 0 {
				newlyReady = append(newlyReady, j)
			}
		}
		// Sort by original position to make the output deterministic and stable
		// (respects source file order as tiebreaker between independent objects).
		sort.Ints(newlyReady)
		queue = append(queue, newlyReady...)
	}

	if len(sorted) != n {
		// There is a cycle. Detect DEFERRABLE FKs that could break the cycle.
		cycle := findCycle(edges, n)
		if canDefer(objects, cycle, idx) {
			cycleSet := make(map[int]bool, len(cycle))
			for _, i := range cycle {
				cycleSet[i] = true
			}

			// Remove circular FKs from tables in the cycle, collecting them as deferred.
			modified := make([]pipeline.IRObject, len(objects))
			copy(modified, objects)

			removedAny := false
			for i, obj := range modified {
				if !cycleSet[i] {
					continue
				}
				tbl, ok := obj.(*ir.Table)
				if !ok {
					continue
				}
				var keepConstraints []*ir.Constraint
				for _, cst := range tbl.Constraints {
					if cst.Type == "FOREIGN KEY" && cst.Deferrable {
						ref := extractFKRef(cst.Expr)
						if ref != "" {
							if j, ok := idx[ref]; ok && cycleSet[j] {
								deferred = append(deferred, deferredFK{table: tbl, fk: cst})
								removedAny = true
								continue
							}
						}
					}
					keepConstraints = append(keepConstraints, cst)
				}
				tblCopy := *tbl
				tblCopy.Constraints = keepConstraints
				modified[i] = &tblCopy
			}

			// Defense in depth, independent of canDefer's own guarantee: if
			// nothing was actually removed, recursing would hit this exact
			// cycle again and recurse forever (see canDefer's doc comment,
			// RFC audit item C.1's infinite-recursion crash). Fall through to
			// the ordinary "no DEFERRABLE FK" error instead of retrying an
			// unmodified object set.
			if !removedAny {
				members := make([]string, 0, len(cycle))
				for _, i := range cycle {
					members = append(members, objects[i].QualifiedName())
				}
				return nil, pipeline.ErrorfCode(pipeline.SourcePos{}, "DPG-E017", "circular dependency cycle with no DEFERRABLE FK: %s",
					strings.Join(members, " → "))
			}

			reResolved, err := (&Resolver{}).Sort(modified)
			if err != nil {
				return objects, nil
			}

			for _, df := range deferred {
				for _, obj := range reResolved {
					if t, ok := obj.(*ir.Table); ok && t.Schema == df.table.Schema && t.Name == df.table.Name {
						t.Constraints = append(t.Constraints, df.fk)
						break
					}
				}
			}
			return reResolved, nil
		}
		members := make([]string, 0, len(cycle))
		for _, i := range cycle {
			members = append(members, objects[i].QualifiedName())
		}
		return nil, pipeline.ErrorfCode(pipeline.SourcePos{}, "DPG-E017", "circular dependency cycle with no DEFERRABLE FK: %s",
			strings.Join(members, " → "))
	}

	return sorted, nil
}

// extractFKRef extracts the referenced table's qualified name from a FK constraint
// Expr. The Expr looks like `FOREIGN KEY ("col") REFERENCES "schema"."table" ("col2")`.
// Returns the name in the unquoted form used as index keys (e.g. "schema.table" or "table").
// Dependencies returns, for each object, the qualified names of the
// objects among objects it depends on (the edges Sort orders by).
// Unresolved references are not reported.
func Dependencies(objects []pipeline.IRObject) map[string][]string {
	edges, _, _ := buildEdges(objects)
	out := map[string][]string{}
	for i := range edges {
		for j := range edges[i] {
			// i must come before j: j depends on i.
			name := objects[j].QualifiedName()
			out[name] = append(out[name], objects[i].QualifiedName())
		}
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}

// buildEdges computes the dependency edges between objects (edges[i] holds
// every j that depends on i) and the index of each object by name.
func buildEdges(objects []pipeline.IRObject) ([]map[int]bool, map[string]int, pipeline.Diagnostics) {
	n := len(objects)
	// Build index: qualifiedName → position. Also record the set of schemas
	// declared in source — references whose schema is in this set must resolve;
	// references into schemas not in source (e.g. extension-managed `extensions.geometry`)
	// are out of DPG's scope and silently allowed.
	idx := make(map[string]int, n)
	schemaSet := make(map[string]bool)
	// vtypeCaseIdx maps a declared VIRTUAL TYPE's lowercased qualified name to
	// its correctly-cased qualified name — used solely to detect a column or
	// composite-attribute type reference that case-insensitively matches a
	// declared virtual type but isn't an exact match (see
	// checkVirtualTypeCase's doc comment for why this is safe to flag
	// unconditionally, unlike an ordinary unresolved custom-type reference).
	vtypeCaseIdx := make(map[string]string)
	for i, obj := range objects {
		idx[obj.QualifiedName()] = i
		if s, ok := obj.(*ir.Schema); ok {
			schemaSet[s.Name] = true
		}
		if vt, ok := obj.(*ir.VirtualType); ok {
			vtypeCaseIdx[strings.ToLower(vt.QualifiedName())] = vt.QualifiedName()
		}
	}

	var diags pipeline.Diagnostics

	// edges[i] = set of j where i must come BEFORE j (i → j).
	// Equivalently: j depends on i.
	edges := make([]map[int]bool, n)
	for i := range edges {
		edges[i] = make(map[int]bool)
	}

	// mustPrecede(before, after) records that `before` must be emitted before `after`.
	mustPrecede := func(before, after int) {
		if before != after {
			edges[before][after] = true
		}
	}

	// dependsOn(obj, dep) records that obj depends on dep (dep must come first).
	dependsOn := func(obj, dep int) {
		mustPrecede(dep, obj)
	}

	// schemaEdge adds a dependency from a schema-scoped object to its schema.
	schemaEdge := func(objIdx int, schema string) {
		if schema == "" {
			return
		}
		if schemaIdx, ok := idx[schema]; ok {
			dependsOn(objIdx, schemaIdx)
		}
	}

	// refEdge adds a dependency from objIdx to whatever object is keyed by
	// qualifiedKey, if one exists in the object set. Like the unqualified
	// custom-type case in the *ir.Table branch below, this never errors when
	// the reference isn't found in source — the referenced object (an
	// operator family, TS parser, or TS template) is very commonly a
	// pg_catalog built-in that legitimately isn't part of the managed set,
	// unlike an FK target, which DOES error when unresolved.
	refEdge := func(objIdx int, qualifiedKey string) {
		if refIdx, ok := idx[qualifiedKey]; ok {
			dependsOn(objIdx, refIdx)
		}
	}

	// defaultSchema returns schema if non-empty, else fallback — mirroring how
	// an unqualified reference (FAMILY name, PARSER name, TEMPLATE name)
	// resolves to the referencing object's own schema, the same convention
	// already used for unqualified column type references below.
	defaultSchema := func(schema, fallback string) string {
		if schema != "" {
			return schema
		}
		return fallback
	}

	// funcRefKey builds the desired-object index key for a trigger or event
	// trigger's EXECUTE FUNCTION reference, which arrives as a single,
	// possibly schema-qualified plain string (Trigger.Function is parsed by
	// the DPG blockparser, EventTrigger.Function by pg_query's Funcname list
	// — neither is a structured Schema+Name pair the way a column type or FK
	// target is) — mirroring how Function.QualifiedName() always includes
	// an arg-type suffix, empty for the zero-argument functions a trigger or
	// event trigger can call.
	funcRefKey := func(ref, fallbackSchema string) string {
		if ref == "" {
			return ""
		}
		schema, name := fallbackSchema, ref
		if i := strings.LastIndex(ref, "."); i >= 0 {
			schema, name = ref[:i], ref[i+1:]
		}
		if schema == "" {
			return name + "()"
		}
		return schema + "." + name + "()"
	}

	// funcPrefixEdge adds a dependency from objIdx to every Function object
	// whose "schema.name(" prefix matches ref — used for CAST's WITH
	// FUNCTION reference, which (unlike a trigger's always-zero-argument
	// function) takes the cast's source type as a real argument, so an
	// exact funcRefKey "()" match would never hit. Matching by prefix
	// rather than resolving the exact overload avoids needing to convert
	// CreateCastStmt's argument TypeName into the identical ArgsKey format
	// Function.QualifiedName() uses; over-matching a same-named overload
	// only adds a harmless extra ordering constraint, never a wrong result.
	funcPrefixEdge := func(objIdx int, ref, fallbackSchema string) {
		if ref == "" {
			return
		}
		schema, name := fallbackSchema, ref
		if i := strings.LastIndex(ref, "."); i >= 0 {
			schema, name = ref[:i], ref[i+1:]
		}
		prefix := name + "("
		if schema != "" {
			prefix = schema + "." + name + "("
		}
		for key, j := range idx {
			if strings.HasPrefix(key, prefix) {
				dependsOn(objIdx, j)
			}
		}
	}

	// checkVirtualTypeCase reports a diagnostic when t's qualified name is a
	// case-insensitive match for a declared VIRTUAL TYPE but not an exact
	// match — e.g. a column declared `col MyJson` when the source actually
	// declares `VIRTUAL TYPE myjson`. Unlike an ordinary custom TYPE/ENUM/
	// DOMAIN reference (whose absence from idx might legitimately mean "a
	// live-catalog PostgreSQL builtin, unknowable offline" — see
	// typeRefEdge's own doc comment on why that case stays silent), VIRTUAL
	// TYPE is DPG-native with zero catalog footprint (ir.VirtualType's own
	// doc comment: "no backing PostgreSQL DDL is ever emitted"), so the full
	// universe of valid virtual-type names is 100% known and closed at
	// compile time — there is no ambiguity to preserve by staying silent.
	// Before this check, a case-mismatched reference silently rendered as a
	// bogus literal type name in the generated SQL and only failed much
	// later as a confusing raw PostgreSQL error at apply time ("type ...
	// does not exist") instead of a clear compile-time diagnostic citing the
	// correct spelling.
	//
	// Only a near-miss is flagged, never an outright unrecognized name — an
	// unrecognized name might still be a genuine PostgreSQL builtin or a
	// forward reference to some other custom type, and this check has
	// nothing useful to say about either.
	checkVirtualTypeCase := func(t ir.TypeRef, fallbackSchema string, pos pipeline.SourcePos, context string) {
		if t.Name == "" || t.Schema == "pg_catalog" {
			return
		}
		schema := defaultSchema(t.Schema, fallbackSchema)
		key := t.Name
		if schema != "" {
			key = schema + "." + t.Name
		}
		correct, ok := vtypeCaseIdx[strings.ToLower(key)]
		if !ok || correct == key {
			return
		}
		diags = append(diags, pipeline.Errorf(pos,
			"%s references %q, which case-insensitively matches VIRTUAL TYPE %q but is not an exact match — VIRTUAL TYPE names are case-sensitive; did you mean %q?",
			context, key, correct, correct))
	}

	// typeRefEdge adds a dependency from objIdx to the custom TYPE referenced
	// by t — same ordering hazard as the *ir.Table column-type case below,
	// but for a Function/Procedure parameter or return type (a function
	// referencing a not-yet-created custom type fails at apply time,
	// confirmed live). Unlike the Table case this never errors when
	// unresolved: t.Schema=="" is ambiguous here between "built-in" and
	// "same schema as the function" the way it isn't for a table column
	// (which always has a definite owning schema to fall back to), so
	// silence on a miss is the safer default — same reasoning as refEdge.
	typeRefEdge := func(objIdx int, t ir.TypeRef, fallbackSchema string) {
		if t.Schema == "pg_catalog" || t.Name == "" {
			return
		}
		schema := defaultSchema(t.Schema, fallbackSchema)
		if schema == "" {
			refEdge(objIdx, t.Name)
			return
		}
		refEdge(objIdx, schema+"."+t.Name)
	}

	// isBaseTypeTarget reports whether t resolves to a BASE- or RANGE-variant
	// Type in the object set — used to skip the usual Function/Procedure→Type
	// ordering edge for a LANGUAGE internal/c function specifically.
	// Confirmed live: CREATE FUNCTION ... RETURNS/  (arg) not_yet_existing_type
	// AS '...' LANGUAGE internal auto-creates a shell type ("type ... is not
	// yet defined / Creating a shell type definition") — PostgreSQL's own
	// documented bootstrapping trick for a base type's I/O functions, which
	// legitimately forward-reference the type before it exists. The usual
	// "function depends on its param/return type" edge would be exactly
	// backwards here, and combined with the Type→Function edge added below
	// (the type's full definition needs those functions first) would form an
	// unresolvable cycle with no DEFERRABLE escape hatch.
	//
	// RANGE needs the identical exemption for a different but analogous
	// reason: CREATE TYPE ... AS RANGE auto-generates its own eponymous
	// constructor function(s) (LANGUAGE internal, e.g. `range_constructor2`),
	// introspected as an ordinary managed ir.Function whose ReturnType is the
	// range type itself. Without this exemption that function gets a
	// Function→Type edge, and the range type's own Body text (which contains
	// the type's — and thus the constructor's — own name) matches
	// bodyCallsFuncEdge's whole-word scan, adding a Type→Function edge back:
	// a genuine 2-node cycle with zero Tables in it, which canDefer used to
	// mishandle (see canDefer's doc comment) — confirmed live, reproduced by
	// dumping a real RANGE type and hitting a stack-overflowing infinite
	// Sort recursion (RFC audit item C.1).
	isBaseTypeTarget := func(t ir.TypeRef, fallbackSchema string) bool {
		if t.Schema == "pg_catalog" || t.Name == "" {
			return false
		}
		schema := defaultSchema(t.Schema, fallbackSchema)
		key := t.Name
		if schema != "" {
			key = schema + "." + t.Name
		}
		j, ok := idx[key]
		if !ok {
			return false
		}
		ty, ok := objects[j].(*ir.Type)
		return ok && (ty.Variant == "BASE" || ty.Variant == "RANGE")
	}

	// sqlBodyCallsIdent matches whether name appears as a whole-word
	// identifier (case-insensitive, PostgreSQL identifiers being
	// case-insensitive unless quoted) inside body. Not a real SQL parse —
	// a plain text scan, like funcPrefixEdge's own prefix matching — so it
	// can over-match a same-named column/variable/literal; a spurious extra
	// ordering constraint from that is harmless, never a wrong result.
	sqlBodyCallsIdent := func(body, name string) bool {
		if body == "" || name == "" {
			return false
		}
		re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(name) + `\b`)
		return re.MatchString(body)
	}

	// bodyCallsFuncEdge adds a dependency from objIdx to every Function/
	// Procedure in the object set whose name is referenced in body — used
	// for a LANGUAGE sql function/procedure body calling another function.
	// Real PostgreSQL validates a LANGUAGE sql body immediately at CREATE
	// FUNCTION/PROCEDURE time (unlike plpgsql, which is compiled lazily and
	// not checked against the catalog at creation), so a call to a
	// not-yet-created function fails at apply time — same ordering hazard,
	// found the same way, as the funcPrefixEdge cases elsewhere in this
	// file.
	bodyCallsFuncEdge := func(objIdx int, body string) {
		if body == "" {
			return
		}
		for j, dep := range objects {
			if j == objIdx {
				continue
			}
			var name string
			switch d := dep.(type) {
			case *ir.Function:
				name = d.Name
			case *ir.Procedure:
				name = d.Name
			default:
				continue
			}
			if sqlBodyCallsIdent(body, name) {
				dependsOn(objIdx, j)
			}
		}
	}

	// tableRefEdges adds a dependency from objIdx to every table statically
	// referenced in body — a real AST walk (ir.ExtractTableRefs, RFC Section 22.1
	// item 9), not a heuristic: used for a view's query and a LANGUAGE sql
	// function/procedure body (LANGUAGE plpgsql is deliberately never
	// passed here — see the *ir.Function/*ir.Procedure call sites for why).
	// An unqualified reference falls back to "public", the same convention
	// already used for Publication/EventTrigger/Cast's own unqualified
	// references elsewhere in this file — PostgreSQL itself would actually
	// resolve it via search_path at definition time, which DPG has no way
	// to know at plan time.
	tableRefEdges := func(objIdx int, language, body string) {
		if !strings.EqualFold(language, "sql") {
			return
		}
		for _, ref := range ir.ExtractTableRefs(language, body) {
			refEdge(objIdx, defaultSchema(ref.Schema, "public")+"."+ref.Name)
		}
	}

	for i, obj := range objects {
		switch o := obj.(type) {
		case *ir.Table:
			// Table depends on its schema.
			schemaEdge(i, o.Schema)

			// Table depends on any custom types used in columns. If the schema is
			// in source but the type isn't defined, that's a real bug — surface it
			// rather than silently dropping the dependency.
			for _, col := range o.Columns {
				checkVirtualTypeCase(col.Type, o.Schema, col.SrcPos,
					fmt.Sprintf("column %s.%s.%s", o.Schema, o.Name, col.Name))
				ct := col.Type
				if ct.Schema == "" && strings.Contains(ct.Name, ".") {
					// An introspected column type is format_type()'s text:
					// "cron.state" for a type outside the search path.
					ct = ir.ParseTypeText(ct.String())
				}
				if ct.Schema == "pg_catalog" {
					continue
				}
				if ct.Schema == "" {
					// Unqualified type: could be a built-in or a custom type in the
					// table's own schema (this is how introspected columns look —
					// e.g. an enum column reads as "mood", not "public.mood"). Add a
					// dependency only when a matching custom type is defined; never
					// error, since built-ins legitimately aren't in the index.
					if o.Schema != "" {
						if j, ok := idx[o.Schema+"."+ct.Name]; ok {
							dependsOn(i, j)
						}
					}
					continue
				}
				typeKey := ct.Schema + "." + ct.Name
				if j, ok := idx[typeKey]; ok {
					dependsOn(i, j)
				} else if schemaSet[ct.Schema] {
					diags = append(diags, pipeline.Errorf(col.SrcPos,
						"unresolved type reference %q used by column %s.%s.%s — no such type defined in source",
						typeKey, o.Schema, o.Name, col.Name))
				}
			}

			// Table depends on its INHERITS parent table(s) — a child created
			// before its parent exists fails at apply time ("relation ...
			// does not exist"), and nothing else in the pipeline orders these
			// relative to each other (unlike FK refs below, which have their
			// own dependency edge). Unresolved refs are reported the same way
			// as FK refs: a real error only when the target schema is itself
			// managed in source, never for an external/pre-existing parent.
			for _, ref := range o.Inherits {
				refSchema, refTable := "", ref
				if schema, table, ok := strings.Cut(ref, "."); ok {
					refSchema, refTable = schema, table
				}
				resolvedKey, ok := resolveFKTarget(idx, refSchema, refTable, o.Schema)
				if ok {
					dependsOn(i, idx[resolvedKey])
					continue
				}
				effectiveSchema := refSchema
				if effectiveSchema == "" {
					effectiveSchema = o.Schema
				}
				if effectiveSchema == "" || schemaSet[effectiveSchema] {
					displayRef := refTable
					if effectiveSchema != "" {
						displayRef = effectiveSchema + "." + refTable
					}
					diags = append(diags, pipeline.Errorf(o.SrcPos,
						"unresolved INHERITS reference %q from %s — no such table defined in source",
						displayRef, o.QualifiedName()))
				}
			}

			// Table depends on FK-referenced tables. Like type refs, if the
			// referenced schema is managed in source, an unresolved FK target is
			// reported as an error so user typos surface at plan time.
			for _, cst := range o.Constraints {
				if cst.Type != "FOREIGN KEY" {
					continue
				}
				refSchema, refTable := extractFKRefParts(cst.Expr)
				if refTable == "" {
					continue
				}
				resolvedKey, ok := resolveFKTarget(idx, refSchema, refTable, o.Schema)
				if ok {
					dependsOn(i, idx[resolvedKey])
					continue
				}
				effectiveSchema := refSchema
				if effectiveSchema == "" {
					effectiveSchema = o.Schema
				}
				if effectiveSchema == "" || schemaSet[effectiveSchema] {
					displayRef := refTable
					if effectiveSchema != "" {
						displayRef = effectiveSchema + "." + refTable
					}
					diags = append(diags, pipeline.Errorf(cst.Pos,
						"unresolved FK reference %q from %s — no such table defined in source",
						displayRef, o.QualifiedName()))
				}
			}

			// Table depends on the function(s) its own triggers call — a
			// trigger created before its function exists fails at apply
			// time (confirmed live: "function ... does not exist"). Never
			// errors when unresolved: a trigger function is very commonly a
			// pg_catalog/extension-provided one (e.g. moddatetime()) that
			// legitimately isn't part of the managed object set.
			for _, trg := range o.Triggers {
				if j, ok := idx[funcRefKey(trg.Function, o.Schema)]; ok {
					dependsOn(i, j)
				}
			}

		case *ir.EventTrigger:
			// Event trigger depends on the function it calls — same
			// ordering hazard and the same reasoning for never erroring on
			// an unresolved reference as the table-trigger case above.
			// Event triggers aren't schema-scoped, so an unqualified
			// function reference falls back to "public" (this project's
			// own default-schema convention), matching how dump.go and
			// other opaque-object rendering already assume public as the
			// default when no schema is otherwise known.
			if j, ok := idx[funcRefKey(o.Function, "public")]; ok {
				dependsOn(i, j)
			}

		case *ir.Publication:
			// Depends on every FOR TABLE target — a publication created
			// before its table exists fails at apply time (confirmed live:
			// "relation ... does not exist"), same ordering hazard as the
			// Trigger/EventTrigger/Cast cases elsewhere in this file.
			// Publications aren't schema-scoped, so an unqualified table
			// reference falls back to "public", the same convention used
			// for EventTrigger/Cast's unqualified function references.
			for _, t := range o.Tables {
				refEdge(i, defaultSchema(t.Schema, "public")+"."+t.Name)
			}

		case *ir.View:
			// View depends on its schema.
			schemaEdge(i, o.Schema)
			// Depends on every table its query statically references (RFC
			// Section 22.1 item 9) — a real AST walk of Query, not a heuristic.
			tableRefEdges(i, "sql", o.Query)

		case *ir.Cast:
			// Cast depends on its WITH FUNCTION target — same ordering
			// hazard as Trigger/EventTrigger above, found the same way
			// (live apply failure: "function ... does not exist"). Casts
			// aren't schema-scoped in PostgreSQL, so an unqualified
			// function reference falls back to "public".
			funcPrefixEdge(i, o.Function, "public")

		case *ir.Type:
			// Type/domain/enum depends on its schema.
			schemaEdge(i, o.Schema)

			// A composite type's attributes may reference a VIRTUAL TYPE
			// directly (ir.VirtualType's own doc comment) — same case-
			// mismatch hazard as a table column, see checkVirtualTypeCase.
			for _, attr := range o.CompositeAttrs {
				checkVirtualTypeCase(attr.Type, o.Schema, attr.SrcPos,
					fmt.Sprintf("composite type attribute %s.%s.%s", o.Schema, o.Name, attr.Name))
			}

			// A BASE type's INPUT/OUTPUT/RECEIVE/SEND/... functions (and a
			// RANGE type's CANONICAL/SUBTYPE_DIFF) must already exist before
			// CREATE TYPE runs — confirmed live: "type ... does not exist"
			// when the type is created first. Body is opaque free text (RFC
			// Section 5.3/Section 5.5), so this reuses bodyCallsFuncEdge's whole-word scan
			// rather than parsing it — same ordering hazard, found the same
			// way, as a LANGUAGE sql body's function calls.
			if o.Variant == "BASE" || o.Variant == "RANGE" {
				bodyCallsFuncEdge(i, o.Body)
			}

		case *ir.Function:
			schemaEdge(i, o.Schema)
			// Depends on any custom TYPE used in its parameters or return
			// type, and (for a LANGUAGE sql body) any function/procedure it
			// calls — see typeRefEdge/bodyCallsFuncEdge above. Skipped for a
			// LANGUAGE internal/c function's reference to a BASE or RANGE
			// type — see isBaseTypeTarget's doc comment; that specific
			// combination is a forward reference PostgreSQL itself resolves
			// via shell-type auto-creation (BASE) or its own auto-generated
			// constructor function (RANGE), not a real ordering requirement.
			isCLike := strings.EqualFold(o.Attrs.Language, "internal") || strings.EqualFold(o.Attrs.Language, "c")
			for _, arg := range o.Args {
				if isCLike && isBaseTypeTarget(arg.Type, o.Schema) {
					continue
				}
				typeRefEdge(i, arg.Type, o.Schema)
			}
			if !(isCLike && isBaseTypeTarget(o.ReturnType, o.Schema)) {
				typeRefEdge(i, o.ReturnType, o.Schema)
			}
			if strings.EqualFold(o.Attrs.Language, "sql") {
				bodyCallsFuncEdge(i, o.Attrs.Body)
				// Depends on every table its body statically references
				// (RFC Section 22.1 item 9) — real AST analysis; dynamic SQL
				// (EXECUTE '...') is invisible to it by design. LANGUAGE
				// plpgsql is deliberately NOT scanned here, same reasoning
				// as bodyCallsFuncEdge's own plpgsql skip just above:
				// PostgreSQL compiles plpgsql lazily and never resolves
				// embedded SQL against the catalog at CREATE FUNCTION time,
				// so a function→table edge isn't correctness-load-bearing
				// for it. Forcing one anyway can manufacture a 2-node cycle
				// Section 22.2's FK-only cycle-breaker has no mechanism to
				// resolve — e.g. an entirely ordinary validation/audit
				// trigger function that queries its own table, which
				// already has a table→function edge from the trigger
				// itself (RFC audit finding #121).
				tableRefEdges(i, "sql", o.Attrs.Body)
			}

		case *ir.Procedure:
			schemaEdge(i, o.Schema)
			// Same reasoning as *ir.Function above (a procedure has no
			// return type), including the LANGUAGE internal/c BASE/RANGE-type
			// forward-reference exemption.
			isCLike := strings.EqualFold(o.Attrs.Language, "internal") || strings.EqualFold(o.Attrs.Language, "c")
			for _, arg := range o.Args {
				if isCLike && isBaseTypeTarget(arg.Type, o.Schema) {
					continue
				}
				typeRefEdge(i, arg.Type, o.Schema)
			}
			if strings.EqualFold(o.Attrs.Language, "sql") {
				bodyCallsFuncEdge(i, o.Attrs.Body)
				// See the *ir.Function case above: plpgsql is
				// deliberately not scanned for table references, only
				// LANGUAGE sql.
				tableRefEdges(i, "sql", o.Attrs.Body)
			}

		case *ir.Aggregate:
			schemaEdge(i, o.Schema)

		case *ir.Sequence:
			schemaEdge(i, o.Schema)

		case *ir.Collation:
			schemaEdge(i, o.Schema)

		case *ir.Operator:
			schemaEdge(i, o.Schema)
			// Operator depends on its PROCEDURE target — same ordering
			// hazard, found the same way, as Trigger/EventTrigger/Cast
			// above: an operator created before its function exists fails
			// at apply time. Matched by prefix like Cast (a real argument
			// list, not the zero-arg shape a trigger function always has).
			funcPrefixEdge(i, o.Function, o.Schema)

		case *ir.OperatorClass:
			schemaEdge(i, o.Schema)
			// Class depends on its FAMILY (must be CREATEd first — PostgreSQL's
			// CREATE OPERATOR FAMILY has no IF NOT EXISTS, so ordering isn't
			// self-healing the way, say, a redundant CREATE SCHEMA IF NOT EXISTS
			// would be). o.FamilyName is empty only for hand-written source that
			// omits FAMILY (relying on PG's own same-name auto-creation) — every
			// introspected class always has one now (see introspectOperatorClasses).
			if o.FamilyName != "" {
				famSchema := defaultSchema(o.FamilySchema, o.Schema)
				refEdge(i, famSchema+"."+o.FamilyName+" USING "+o.AccessMethod+" FAMILY")
			}
			// Class depends on every support FUNCTION it declares — same
			// ordering hazard as Trigger/EventTrigger/Cast/Operator above,
			// found the same way (live apply failure). Unlike those single-
			// function cases, a class can declare several (numbered support
			// slots), so this loops over all of them.
			for _, fn := range o.Functions {
				funcPrefixEdge(i, fn, o.Schema)
			}

		case *ir.OperatorFamily:
			schemaEdge(i, o.Schema)
			// Loose members (RFC Section 14.4) reference operators/functions/types
			// the same way OperatorClass's AS-list members do above — same
			// ordering hazard (ALTER OPERATOR FAMILY ... ADD referencing a
			// not-yet-created operator/function/type fails at apply time),
			// so reuse the exact same edge helpers rather than duplicate the
			// reasoning.
			for _, m := range o.Members {
				if m.IsFunction {
					funcPrefixEdge(i, m.Name.String(), o.Schema)
				} else {
					// Matches ir.Operator.QualifiedName()'s exact shape
					// (qualName(schema, name) + "(" + OperandsKey(...) + ")")
					// — LeftType/RightType are already canonical
					// TypeRef.String() output (normalizeOpFamilyMembers ran
					// them through ir.ParseTypeText), so this only needs the
					// schema defaulted, same as funcPrefixEdge does
					// internally for the function case above.
					opSchema := defaultSchema(m.Name.Schema, o.Schema)
					refEdge(i, opSchema+"."+m.Name.Name+"("+m.LeftType+", "+m.RightType+")")
				}
				typeRefEdge(i, ir.ParseTypeText(m.LeftType), o.Schema)
				typeRefEdge(i, ir.ParseTypeText(m.RightType), o.Schema)
				for _, a := range m.FuncArgs {
					typeRefEdge(i, ir.ParseTypeText(a), o.Schema)
				}
				if m.OrderBy {
					// PostgreSQL requires a FOR ORDER BY sort family to be a
					// btree family — confirmed via the ALTER OPERATOR FAMILY
					// grammar itself (amopsortfamily must reference a btree
					// opfamily) — so the access method is never ambiguous
					// even though the source text doesn't state it.
					sortSchema := defaultSchema(m.SortFamily.Schema, o.Schema)
					refEdge(i, sortSchema+"."+m.SortFamily.Name+" USING btree FAMILY")
				}
			}

		case *ir.StatisticsObject:
			schemaEdge(i, o.Schema)

		case *ir.TSConfig:
			schemaEdge(i, o.Schema)
			// Config depends on its PARSER (see refEdge: only an edge if the
			// parser is itself part of the managed object set — most commonly
			// it's a pg_catalog built-in like "default", which correctly gets no
			// edge at all).
			if o.ParserName != "" {
				refEdge(i, defaultSchema(o.ParserSchema, o.Schema)+"."+o.ParserName)
			}

		case *ir.TSDict:
			schemaEdge(i, o.Schema)
			// Dict depends on its TEMPLATE, same reasoning as TSConfig's PARSER.
			if o.TemplateName != "" {
				refEdge(i, defaultSchema(o.TemplateSchema, o.Schema)+"."+o.TemplateName)
			}

		case *ir.TSParser:
			schemaEdge(i, o.Schema)
			// Parser depends on its START/GETTOKEN/END/LEXTYPES/HEADLINE
			// support functions — same ordering hazard as
			// Trigger/EventTrigger/Cast/Operator/OperatorClass above. In
			// practice these almost always name a pg_catalog built-in
			// (custom ones require C), so funcPrefixEdge's silent no-op on
			// an unresolved reference is the common case, not the
			// exception.
			for _, fn := range o.Functions {
				funcPrefixEdge(i, fn, o.Schema)
			}

		case *ir.TSTemplate:
			schemaEdge(i, o.Schema)
			// Template depends on its INIT/LEXIZE support functions — same
			// reasoning as TSParser above.
			for _, fn := range o.Functions {
				funcPrefixEdge(i, fn, o.Schema)
			}
		}
	}

	return edges, idx, diags
}

func extractFKRef(expr string) string {
	schema, table := extractFKRefParts(expr)
	if table == "" {
		return ""
	}
	if schema != "" {
		return schema + "." + table
	}
	return table
}

// extractFKRefParts splits the FK target into (schema, table). schema is "" when
// the source text wrote an unqualified reference. Quotes around either component
// are stripped.
func extractFKRefParts(expr string) (schema, table string) {
	upper := strings.ToUpper(expr)
	i := strings.Index(upper, "REFERENCES")
	if i < 0 {
		return "", ""
	}
	rest := strings.TrimSpace(expr[i+len("REFERENCES"):])
	ref := unquoteIdent(extractFirstIdent(rest))
	if ref == "" {
		return "", ""
	}
	if dot := strings.Index(ref, "."); dot >= 0 {
		return ref[:dot], ref[dot+1:]
	}
	return "", ref
}

// resolveFKTarget looks up the FK target in idx, falling back to the referencing
// table's own schema when the source wrote an unqualified reference. Returns the
// resolved index key and whether a hit was found.
func resolveFKTarget(idx map[string]int, refSchema, refTable, ownSchema string) (string, bool) {
	if refSchema != "" {
		key := refSchema + "." + refTable
		if _, ok := idx[key]; ok {
			return key, true
		}
		return "", false
	}
	if _, ok := idx[refTable]; ok {
		return refTable, true
	}
	if ownSchema != "" {
		key := ownSchema + "." + refTable
		if _, ok := idx[key]; ok {
			return key, true
		}
	}
	return "", false
}

// extractFirstIdent reads the leading identifier (possibly schema."name" or "schema"."name")
// stopping before the first space or '('.
func extractFirstIdent(s string) string {
	end := strings.IndexAny(s, " \t\n(")
	if end < 0 {
		return s
	}
	return s[:end]
}

// unquoteIdent removes double-quotes from a (possibly schema-qualified) identifier
// and returns the canonical "schema.name" or "name" form used in the dependency index.
func unquoteIdent(s string) string {
	s = strings.ReplaceAll(s, `""`, `"`) // unescape embedded double-quotes
	s = strings.ReplaceAll(s, `"`, "")   // strip delimiter quotes
	return s
}

// findCycle finds every node on a cycle using DFS, reconstructing the full
// cycle path via parent pointers (the current DFS stack) rather than just
// the two endpoints of the back-edge that closed it. For a cycle of 3+
// nodes, a gray node w reached from v is not necessarily v's direct
// predecessor — it can be any ancestor still on the DFS stack — so the full
// cycle is the stack slice from w's position through v, not just {w, v}.
// Returning only the back-edge endpoints let canDefer examine a strict
// subset of the real cycle-closing FKs, silently missing a non-deferrable
// FK on any node in between (RFC audit item #23).
func findCycle(edges []map[int]bool, n int) []int {
	color := make([]int, n) // 0=white, 1=gray, 2=black
	var stack []int
	stackPos := make(map[int]int, n)
	var cycle []int
	var dfs func(v int) bool
	dfs = func(v int) bool {
		color[v] = 1
		stack = append(stack, v)
		stackPos[v] = len(stack) - 1
		for w := range edges[v] {
			if color[w] == 1 {
				cycle = append(cycle, stack[stackPos[w]:]...)
				return true
			}
			if color[w] == 0 && dfs(w) {
				return true
			}
		}
		color[v] = 2
		stack = stack[:len(stack)-1]
		delete(stackPos, v)
		return false
	}
	for i := 0; i < n; i++ {
		if color[i] == 0 {
			if dfs(i) {
				return cycle
			}
		}
	}
	return nil
}

// canDefer returns true only when the cycle is actually closed by at least
// one DEFERRABLE FOREIGN KEY constraint — i.e. a constraint on a Table
// member of the cycle whose REFERENCES target (via extractFKRef/idx) is
// itself another member of the cycle — and every such cycle-closing FK is
// DEFERRABLE. Mirrors Sort's own constraint-stripping loop's target check so
// the two stay in agreement about which constraints actually break the
// cycle.
//
// Previously this returned true whenever it found no *non-deferrable* FK
// among cycle members, which silently defaulted to true for a cycle with no
// FK-bearing Tables in it at all (e.g. one closed purely by Function/Type
// ordering edges — see isBaseTypeTarget's RANGE-type doc comment, RFC audit
// item C.1). Sort's stripping loop then found nothing to remove, recursed
// with an unchanged object set, hit the identical cycle again, and repeated
// until the goroutine stack overflowed. Requiring at least one genuine
// deferrable cycle-closing FK closes that hole; the no-progress guard in
// Sort's caller is a second, independent line of defense against the same
// failure mode for any future cause.
func canDefer(objects []pipeline.IRObject, cycle []int, idx map[string]int) bool {
	if len(cycle) == 0 {
		return false
	}
	cycleSet := make(map[int]bool, len(cycle))
	for _, i := range cycle {
		cycleSet[i] = true
	}
	foundDeferrableFK := false
	for _, i := range cycle {
		tbl, ok := objects[i].(*ir.Table)
		if !ok {
			continue
		}
		for _, cst := range tbl.Constraints {
			if cst.Type != "FOREIGN KEY" {
				continue
			}
			ref := extractFKRef(cst.Expr)
			if ref == "" {
				continue
			}
			j, ok := idx[ref]
			if !ok || !cycleSet[j] {
				continue
			}
			if !cst.Deferrable {
				return false
			}
			foundDeferrableFK = true
		}
	}
	return foundDeferrableFK
}

// Ensure Resolver implements pipeline.DependencyResolver.
var _ pipeline.DependencyResolver = (*Resolver)(nil)
