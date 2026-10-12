// Package diff implements pipeline.Differ. It compares a slice of desired
// IRObjects against a pipeline.Snapshot and produces an ordered list of DiffOps
// representing the minimal set of DDL changes needed.
package diff

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	pg_query "github.com/pganalyze/pg_query_go/v6"

	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/snapshot"
)

func init() {
	pipeline.Default.Register(pipeline.KeyDiffer, New())
}

// op implements pipeline.DiffOp.
type op struct {
	sql    string
	safety pipeline.Safety
	pos    pipeline.SourcePos
	mode   pipeline.ExecMode
}

func (o *op) SQL() string             { return o.sql }
func (o *op) Safety() pipeline.Safety { return o.safety }
func (o *op) Pos() pipeline.SourcePos { return o.pos }
func (o *op) Mode() pipeline.ExecMode { return o.mode }

func safeOp(sql string, pos pipeline.SourcePos) *op {
	return &op{sql: sql, safety: pipeline.Safe, pos: pos, mode: pipeline.ModeTransactional}
}
func cautionOp(sql string, pos pipeline.SourcePos) *op {
	return &op{sql: sql, safety: pipeline.Caution, pos: pos, mode: pipeline.ModeTransactional}
}
func destructiveOp(sql string, pos pipeline.SourcePos) *op {
	return &op{sql: sql, safety: pipeline.Destructive, pos: pos, mode: pipeline.ModeTransactional}
}

// nonTxnOp is an op PostgreSQL refuses to run inside a transaction block
// (CREATE INDEX CONCURRENTLY, CREATE TABLESPACE, CREATE SUBSCRIPTION, ...).
// It runs after COMMIT, with its own safety class.
func nonTxnOp(safety pipeline.Safety, sql string, pos pipeline.SourcePos) *op {
	return &op{sql: sql, safety: safety, pos: pos, mode: pipeline.ModeNonTransactional}
}

// instructionOp is an SQL comment describing a step DPG does not perform.
// It is shown in the plan and never executed. A Destructive instruction
// (a partition strategy rebuild) must be acknowledged before apply; a Safe
// one is an informational note.
func instructionOp(safety pipeline.Safety, comment string, pos pipeline.SourcePos) *op {
	return &op{sql: comment, safety: safety, pos: pos, mode: pipeline.ModeInstructionOnly}
}

// wrapCreateWithOwner wraps createOp in SET ROLE/RESET ROLE when owner is
// declared (RFC Section 11.5, audit item #28). PostgreSQL attributes default-
// privilege eligibility (Section 11.4) to whichever role actually executed CREATE,
// not to final ownership — creating directly as the declared owner (rather
// than creating as the connecting role and reassigning afterward via a
// trailing ALTER ... OWNER TO) matches real PostgreSQL creator semantics and
// is what makes a matching DEFAULT PRIVILEGES FOR ROLE block actually fire.
// Preserves createOp's own Safety()/Mode() for the bookend ops, so
// this works identically for an object whose CREATE must run outside a
// transaction (e.g. TABLESPACE). Once an object exists, reassigning its
// owner keeps using plain ALTER ... OWNER TO — see each diff*'s existing
// "owner changed" branches, untouched by this helper.
func wrapCreateWithOwner(createOp pipeline.DiffOp, owner *string, pos pipeline.SourcePos) []pipeline.DiffOp {
	if owner == nil {
		return []pipeline.DiffOp{createOp}
	}
	bookend := func(sql string) *op {
		return &op{sql: sql, safety: createOp.Safety(), pos: pos, mode: createOp.Mode()}
	}
	return []pipeline.DiffOp{
		bookend(fmt.Sprintf("SET ROLE %s;", quoteIdent(*owner))),
		createOp,
		bookend("RESET ROLE;"),
	}
}

// Differ implements pipeline.Differ.
type Differ struct{}

// New returns a Differ.
func New() *Differ { return &Differ{} }

// Diff compares desired IR state against snap and returns ordered DiffOps.
func (d *Differ) Diff(desired []pipeline.IRObject, snap *pipeline.Snapshot) ([]pipeline.DiffOp, error) {
	var ops []pipeline.DiffOp

	desiredByName := make(map[string]pipeline.IRObject, len(desired))
	for _, obj := range desired {
		desiredByName[obj.QualifiedName()] = obj
	}

	// consumed tracks snapshot keys claimed by a rename, so they are not dropped.
	consumed := make(map[string]bool)

	// A table referenced by ATTACHED FROM (RFC Section 7.13) anywhere in
	// desired is claimed the same way a rename target is: it genuinely
	// disappears from desired as an independent top-level object (it's now
	// only reachable nested inside its new parent's Partitions), so without
	// this Pass 2 below would see it "absent from desired" and drop it —
	// racing the ALTER TABLE ... ATTACH PARTITION diffPartitionList emits
	// for it later in the same migration, which would then fail live
	// ("relation does not exist") since the table it's trying to attach
	// was just dropped out from under it.
	for _, obj := range desired {
		tbl, ok := obj.(*ir.Table)
		if !ok {
			continue
		}
		markAttachedFromTargets(tbl.Schema, tbl.Partitions, consumed)
	}

	env := newDiffEnv(desired)

	// Pass 0: create brand-new schemas before anything else. Pass 1's
	// cross-schema SET SCHEMA (RFC Section 7.6) can target a schema that is
	// declared in this same apply but doesn't exist in the live database
	// yet — Pass 1 runs before Pass 3's own "create new objects" handling,
	// so without this, moving an object into a brand-new schema emits
	// ALTER ... SET SCHEMA before the CREATE SCHEMA that must precede it
	// (confirmed live: "schema ... does not exist"). A schema itself being
	// renamed is excluded here and left to Pass 1, which matches it by its
	// OLD key — Pass 0 only ever sees the new key, which by definition isn't
	// in the snapshot yet for either a genuinely new schema or a renamed
	// one, so it can't tell those apart without this check.
	schemasCreatedEarly := make(map[string]bool)
	for _, obj := range desired {
		s, ok := obj.(*ir.Schema)
		if !ok || renamedFromKey(obj) != "" {
			continue
		}
		var so snapshot.SnapObject
		found, err := snap.GetObject(s.QualifiedName(), &so)
		if err != nil {
			return nil, fmt.Errorf("diff: decoding snapshot for %q: %w", s.QualifiedName(), err)
		}
		if found {
			continue
		}
		createOps, err := createObject(obj)
		if err != nil {
			return nil, err
		}
		ops = append(ops, createOps...)
		schemasCreatedEarly[s.QualifiedName()] = true
	}

	// Pass 1: handle object renames.
	//
	// A rename is detected when desired has RenamedFrom, the snapshot has the
	// OLD key, and the snapshot does NOT yet have the NEW key. After a rename
	// is applied, the snapshot is rewritten to use the new key — so on every
	// subsequent run the new key IS present and the old key is gone. RFC Section 7.4
	// step 5 says a stale RENAMED FROM is a compiler error; the trick is to
	// distinguish "stale because user typo'd" from "stale because the rename
	// already happened." We use the new key's presence as the discriminator:
	//
	//   • new in snap                → rename already landed (or no-op); skip
	//                                   directive validation, fall through to
	//                                   the normal alter pipeline.
	//   • new not in snap, old in    → State A, fresh rename; emit it.
	//   • new not in snap, old not   → State C, stale/typo'd directive on a
	//                                   brand-new object; error.
	//
	// State D (both in snap, e.g. a hand-edited snapshot or a partial apply)
	// is intentionally NOT an error: the new key already exists, so we treat
	// it as a post-apply state and let Pass 2 drop the orphaned old key.
	for _, obj := range desired {
		oldKey := renamedFromKey(obj)
		if oldKey == "" {
			continue
		}
		newKey := obj.QualifiedName()

		var oldSnap snapshot.SnapObject
		oldFound, _ := snap.GetObject(oldKey, &oldSnap)
		var newSnap snapshot.SnapObject
		newFound, _ := snap.GetObject(newKey, &newSnap)

		if newFound {
			// Post-apply (or State D): nothing to rename. Don't consume the
			// old key — if it still exists in snap, Pass 2 will drop it.
			continue
		}
		if !oldFound {
			return nil, pipeline.ErrorfCode(obj.Pos(), "DPG-E021",
				"RENAMED FROM %q on %s %q does not match the snapshot — neither the old nor the new name exists there. Remove RENAMED FROM if this is a genuinely new object.",
				oldKey, describeKind(obj), newKey)
		}
		consumed[oldKey] = true
		// Route to diffObject; individual diff functions emit RENAME when
		// the snap name differs from the desired name. nil here (not
		// Pass 2.5's skipDetachedByParent, computed later): a table being
		// both renamed and a DETACHED FROM parent in the same migration is
		// an unhandled combination, the same class of narrow edge case the
		// RFC itself declines to support for RENAMED FROM combined with
		// ATTACHED FROM/DETACHED FROM on one entry.
		alterOps, err := diffObject(obj, &oldSnap, snap, env, nil)
		if err != nil {
			return nil, err
		}
		ops = append(ops, alterOps...)
	}

	// Pass 2: drop objects in snapshot that are absent from desired and not consumed.
	for key, raw := range snap.Objects {
		if consumed[key] {
			continue
		}
		if _, ok := desiredByName[key]; ok {
			continue
		}
		var so snapshot.SnapObject
		if err := json.Unmarshal(raw, &so); err != nil {
			return nil, fmt.Errorf("diff: corrupted snapshot entry %q: %w", key, err)
		}
		// RFC Section 7.11/Section 15.10 Phase 9 Pass 3: a table marked PROTECTED in the
		// snapshot must never be silently dropped just because it's absent
		// from desired — that's error DPG-E022, not a DROP TABLE. Found via
		// this session's live-testing sweep: Protected was parsed all the way
		// into SnapTable but nothing ever read it back out again, so a
		// declared PROTECTED table had zero actual protection.
		if so.Kind == "table" && so.Table != nil && so.Table.Protected {
			return nil, pipeline.ErrorfCode(pipeline.SourcePos{}, "DPG-E022",
				"table %q is PROTECTED and cannot be dropped; remove the PROTECTED directive first",
				key,
			)
		}
		// "public" always exists in PostgreSQL and is never dropped — the
		// same reasoning compiler.go documents for skipping it from
		// directory-based synthetic schema declarations. Needed now that
		// introspectSchemas includes "public" (RFC audit item C.2): without
		// this guard, any project with a schemas/public/ directory but no
		// explicit `SCHEMA public { }` declaration would get a spurious
		// DESTRUCTIVE DROP SCHEMA IF EXISTS "public" on every plan --live,
		// since desired never carries an object for it but snap now does.
		if so.Kind == "schema" && so.Schema != nil && so.Schema.Name == "public" {
			continue
		}
		// Roles are shared by every database and application on the
		// cluster, so a live role DPG doesn't declare is left alone; only a
		// role removed from the stored snapshot is dropped.
		if snap.Live && so.Kind == "role" {
			continue
		}
		ops = append(ops, dropObject(&so)...)
	}

	// Pass 2.5: DETACHED FROM (RFC Section 7.13) — a standalone Table
	// declaring DetachedFrom is matched against a NESTED entry in its
	// parent's own snapshot (SnapTable.Partitions), a different key space
	// from every top-level-to-top-level mechanism above (Pass 1's rename
	// handling included): a partition has no snap.Objects entry of its
	// own. Without this pass, the child would independently look "new"
	// to Pass 3 (CREATE TABLE) while the parent's own partition-list diff
	// (diffPartitionList, inside Pass 3's per-object diffTable call) would
	// independently see it "missing" (DROP TABLE) — emitting a destructive
	// drop+recreate pair instead of the single metadata-only detach this
	// directive exists to express. detachedChildren short-circuits the
	// child's own Pass 3 handling entirely (there is no richer prior state
	// to diff against beyond bounds/name — see the doc comment on
	// ir.Table.DetachedFrom); skipDetachedByParent, keyed by the PARENT's
	// qualified name, is threaded through diffObject → diffTable →
	// diffPartitions → diffPartitionList so the parent's own removal-
	// detection loop skips this specific child name.
	detachedChildren := make(map[string]bool)
	skipDetachedByParent := make(map[string]map[string]bool)
	for _, obj := range desired {
		tbl, ok := obj.(*ir.Table)
		if !ok || tbl.DetachedFrom == nil {
			continue
		}
		childKey := tbl.QualifiedName()
		var childSnap snapshot.SnapObject
		childFound, err := snap.GetObject(childKey, &childSnap)
		if err != nil {
			return nil, fmt.Errorf("diff: decoding snapshot for %q: %w", childKey, err)
		}
		parentKey := normalizeInheritRef(tbl.Schema, tbl.DetachedFrom.ParentTable)
		var parentSnap snapshot.SnapObject
		found, err := snap.GetObject(parentKey, &parentSnap)
		if err != nil {
			return nil, fmt.Errorf("diff: decoding snapshot for %q: %w", parentKey, err)
		}
		if !found || parentSnap.Table == nil {
			return nil, pipeline.Errorf(tbl.SrcPos,
				"DETACHED FROM %q on table %q does not match the snapshot — %q is not a known table",
				tbl.DetachedFrom.ParentTable, tbl.QualifiedName(), parentKey)
		}
		matchedIdx := -1
		for i := range parentSnap.Table.Partitions {
			if parentSnap.Table.Partitions[i].Name == tbl.Name {
				matchedIdx = i
				break
			}
		}
		if matchedIdx < 0 {
			if childFound {
				// Post-apply / no-op state: the detach already landed (the
				// child now exists as its own top-level table, no longer
				// listed among the parent's partitions) — same three-state
				// resolution every other RENAMED-FROM-style directive in
				// this codebase uses (Section 7.6): "new state already
				// present" is not an error, just nothing left to do here.
				// Falls through to Pass 3's normal handling, which diffs it
				// like any other ordinary existing table.
				continue
			}
			return nil, pipeline.Errorf(tbl.SrcPos,
				"DETACHED FROM %q on table %q does not match the snapshot — %q is not currently a partition of %q, and no existing table %q was found either",
				tbl.DetachedFrom.ParentTable, tbl.QualifiedName(), tbl.Name, parentKey, tbl.QualifiedName())
		}
		matched := parentSnap.Table.Partitions[matchedIdx]
		stmt := fmt.Sprintf("ALTER TABLE %s DETACH PARTITION %s", quoteQualIdent(parentKey), qualIdent(matched.Schema, matched.Name))
		if tbl.DetachedFrom.Concurrently {
			ops = append(ops, nonTxnOp(pipeline.Caution, stmt+" CONCURRENTLY;", tbl.SrcPos))
		} else {
			ops = append(ops, cautionOp(stmt+";", tbl.SrcPos))
		}
		detachedChildren[tbl.QualifiedName()] = true
		if skipDetachedByParent[parentKey] == nil {
			skipDetachedByParent[parentKey] = make(map[string]bool)
		}
		skipDetachedByParent[parentKey][tbl.Name] = true
	}

	// Pass 3: create new or alter existing objects.
	for _, obj := range desired {
		// Skip objects already handled in pass 1.
		if oldKey := renamedFromKey(obj); oldKey != "" && consumed[oldKey] {
			continue
		}
		// Skip schemas already created early by Pass 0.
		if s, ok := obj.(*ir.Schema); ok && schemasCreatedEarly[s.QualifiedName()] {
			continue
		}
		key := obj.QualifiedName()
		// Skip tables just detached by Pass 2.5 — already fully handled
		// there (see its own doc comment for why neither CREATE nor ALTER
		// applies here).
		if detachedChildren[key] {
			continue
		}
		var so snapshot.SnapObject
		found, err := snap.GetObject(key, &so)
		if err != nil {
			return nil, fmt.Errorf("diff: decoding snapshot for %q: %w", key, err)
		}
		if !found {
			createOps, err := createObject(obj)
			if err != nil {
				return nil, err
			}
			ops = append(ops, createOps...)
		} else {
			alterOps, err := diffObject(obj, &so, snap, env, skipDetachedByParent[key])
			if err != nil {
				return nil, err
			}
			ops = append(ops, alterOps...)
		}
	}

	return placeEnumAdditions(ops), nil
}

// addValueOp is an ALTER TYPE ... ADD VALUE op, remembering which enum and
// value it adds so placeEnumAdditions can move it before the transaction.
type addValueOp struct {
	*op
	typeIdent string
	value     string
}

// placeEnumAdditions implements RFC Section 5.1.1: PostgreSQL rejects using
// an enum value inside the transaction that added it ("unsafe use of new
// value"). When any other op in the migration mentions a value being added
// (as the literal 'value'), every ADD VALUE for that enum is moved before
// the transaction, as ADD VALUE IF NOT EXISTS so a retry after a later
// failure is harmless. Each enum's ADD VALUEs move together because later
// ones may be positioned relative to earlier ones.
func placeEnumAdditions(ops []pipeline.DiffOp) []pipeline.DiffOp {
	used := map[string]bool{} // typeIdent of an enum whose new value is used
	for _, o := range ops {
		av, ok := o.(*addValueOp)
		if !ok || used[av.typeIdent] {
			continue
		}
		lit := quoteLit(av.value)
		for _, other := range ops {
			if _, isAdd := other.(*addValueOp); isAdd || other.Mode() == pipeline.ModeInstructionOnly {
				continue
			}
			if strings.Contains(other.SQL(), lit) {
				used[av.typeIdent] = true
				break
			}
		}
	}
	if len(used) == 0 {
		return ops
	}
	for _, o := range ops {
		if av, ok := o.(*addValueOp); ok && used[av.typeIdent] {
			av.sql = strings.Replace(av.sql, " ADD VALUE ", " ADD VALUE IF NOT EXISTS ", 1)
			av.mode = pipeline.ModePreTransactional
		}
	}
	return ops
}

// isLegacySerialTypeName reports whether typeName is the literal,
// un-normalized SERIAL-family type name a pre-upgrade snapshot may have
// stored (before Column.Serial existed, typeNameToRef passed "serial"/
// "bigserial"/"smallserial"/their serialN spellings straight through
// unchanged). Used solely as a one-time, self-clearing stale-snapshot guard
// in diffColumns — see the call site for the full explanation.
func isLegacySerialTypeName(typeName string) bool {
	switch strings.ToLower(typeName) {
	case "smallserial", "serial2", "serial", "serial4", "bigserial", "serial8":
		return true
	default:
		return false
	}
}

// legacyVirtualTypeSnapshot reports whether sc is a snapshot column that
// stored col's virtual type reference as its type, unresolved, rather than
// the json/jsonb type PostgreSQL holds.
func legacyVirtualTypeSnapshot(col *ir.Column, sc *snapshot.SnapColumn) bool {
	if col.VirtualType == nil || sc.VirtualType != "" {
		return false
	}
	bare := ir.TypeRef{Name: col.VirtualType.Name, ArrayDims: col.VirtualType.ArrayDims}
	return sc.Type == col.VirtualType.String() || sc.Type == bare.String()
}

// legacyTypeNameBeforeFix reconstructs what TypeRef.String()
// would have rendered for resolvedType before 2026-08-17's PGCatalogName fix
// added "time"->"time without time zone", "timestamp"->"timestamp without
// time zone", and "varbit"->"bit varying" mappings (all three previously
// passed through PGCatalogName's default case unchanged, silently
// mismatching format_type()'s own canonical spelling — a real, live-verified
// bug that caused permanent spurious DESTRUCTIVE drift for every plain bare
// TIMESTAMP/TIME column, and dropped the typmod entirely for BIT/BIT
// VARYING). Used only to recognize a pre-upgrade snapshot's stale Type
// string as "no real drift, only DPG's own rendering changed" so it
// self-heals on save — the same pattern as isLegacySerialTypeName just
// above.
func legacyTypeNameBeforeFix(resolvedType string) string {
	s := resolvedType
	arraySuffix := ""
	for strings.HasSuffix(s, "[]") {
		arraySuffix += "[]"
		s = strings.TrimSuffix(s, "[]")
	}
	switch {
	case strings.HasSuffix(s, " without time zone"):
		s = strings.TrimSuffix(s, " without time zone")
	case s == "bit varying" || strings.HasPrefix(s, "bit varying("):
		s = "varbit" + strings.TrimPrefix(s, "bit varying")
	}
	return s + arraySuffix
}

// renamedFromKey returns the snapshot key under the OLD name for objects that
// carry a RENAMED FROM directive. Returns "" if the object has no such field.
func renamedFromKey(obj pipeline.IRObject) string {
	switch o := obj.(type) {
	case *ir.Table:
		if o.RenamedFrom != nil {
			return qualKey(oldSchema(o.Schema, o.RenamedFromSchema), *o.RenamedFrom)
		}
	case *ir.Schema:
		if o.RenamedFrom != nil {
			return *o.RenamedFrom
		}
	case *ir.View:
		if o.RenamedFrom != nil {
			return qualKey(oldSchema(o.Schema, o.RenamedFromSchema), *o.RenamedFrom)
		}
	case *ir.Function:
		if o.RenamedFrom != nil {
			// Includes the arg-types signature, like ir.Procedure/ir.Aggregate
			// below: ir.Function.QualifiedName() (the actual snapshot key)
			// always does, including for a zero-arg function ("schema.name()"),
			// so omitting it here never matched the stored key at all — the
			// same shape bug RFC audit item #10 already fixed for Procedure.
			return fmt.Sprintf("%s(%s)", qualKey(oldSchema(o.Schema, o.RenamedFromSchema), *o.RenamedFrom), ir.ArgsKey(o.Args))
		}
	case *ir.Procedure:
		if o.RenamedFrom != nil {
			// Unlike Function above, this includes the arg-types signature —
			// ir.Procedure.QualifiedName() (the actual snapshot key) always
			// does, including for a zero-arg procedure ("schema.name()"), so
			// omitting it here would never match the stored key at all (RFC
			// audit item #10, confirmed live: RENAMED FROM was rejected as
			// stale on every real procedure rename attempt).
			return fmt.Sprintf("%s(%s)", qualKey(oldSchema(o.Schema, o.RenamedFromSchema), *o.RenamedFrom), ir.ArgsKey(o.Args))
		}
	case *ir.Aggregate:
		if o.RenamedFrom != nil {
			// See ir.Procedure's identical reasoning just above (RFC audit
			// item #11).
			return fmt.Sprintf("%s(%s)", qualKey(oldSchema(o.Schema, o.RenamedFromSchema), *o.RenamedFrom), ir.ArgsKey(o.Args))
		}
	case *ir.Type:
		if o.RenamedFrom != nil {
			return qualKey(oldSchema(o.Schema, o.RenamedFromSchema), *o.RenamedFrom)
		}
	case *ir.Collation:
		if o.RenamedFrom != nil {
			return qualKey(oldSchema(o.Schema, o.RenamedFromSchema), *o.RenamedFrom)
		}
	case *ir.Role:
		// Bare, like *ir.Schema above — cluster-level, no schema component.
		if o.RenamedFrom != nil {
			return *o.RenamedFrom
		}
	case *ir.Publication:
		// Bare, like *ir.Role above (RFC audit item #78).
		if o.RenamedFrom != nil {
			return *o.RenamedFrom
		}
	case *ir.ForeignServer:
		// Bare, like *ir.Role above (RFC audit item #79).
		if o.RenamedFrom != nil {
			return *o.RenamedFrom
		}
	case *ir.Tablespace:
		// Bare, like *ir.Role above (RFC audit item #80).
		if o.RenamedFrom != nil {
			return *o.RenamedFrom
		}
	case *ir.Sequence:
		if o.RenamedFrom != nil {
			return qualKey(oldSchema(o.Schema, o.RenamedFromSchema), *o.RenamedFrom)
		}
	case *ir.TSConfig:
		if o.RenamedFrom != nil {
			return qualKey(oldSchema(o.Schema, o.RenamedFromSchema), *o.RenamedFrom)
		}
	case *ir.TSDict:
		if o.RenamedFrom != nil {
			return qualKey(oldSchema(o.Schema, o.RenamedFromSchema), *o.RenamedFrom)
		}
	case *ir.TSParser:
		if o.RenamedFrom != nil {
			return qualKey(oldSchema(o.Schema, o.RenamedFromSchema), *o.RenamedFrom)
		}
	case *ir.TSTemplate:
		if o.RenamedFrom != nil {
			return qualKey(oldSchema(o.Schema, o.RenamedFromSchema), *o.RenamedFrom)
		}
	case *ir.EventTrigger:
		// Bare, like *ir.Schema/*ir.Role above — database-level, no schema
		// component.
		if o.RenamedFrom != nil {
			return *o.RenamedFrom
		}
	case *ir.ForeignDataWrapper:
		// Bare, like *ir.Role/*ir.EventTrigger above — database-level, no
		// schema component.
		if o.RenamedFrom != nil {
			return *o.RenamedFrom
		}
	case *ir.Subscription:
		// Bare, same reasoning as *ir.ForeignDataWrapper above.
		if o.RenamedFrom != nil {
			return *o.RenamedFrom
		}
	case *ir.OperatorClass:
		if o.RenamedFrom != nil {
			// Includes the " USING accessmethod" suffix, like Function/
			// Procedure/Aggregate above include their arg-types signature:
			// ir.OperatorClass.QualifiedName() (the actual snapshot key)
			// always does — a class's name is only unique per access
			// method, so omitting it here would either never match the
			// stored key at all, or (worse) match the wrong class under a
			// same-named-different-method collision.
			return qualKey(oldSchema(o.Schema, o.RenamedFromSchema), *o.RenamedFrom) + " USING " + o.AccessMethod
		}
	case *ir.OperatorFamily:
		if o.RenamedFrom != nil {
			// See *ir.OperatorClass's identical reasoning just above, plus
			// the trailing " FAMILY" ir.OperatorFamily.QualifiedName() also
			// always appends, to avoid colliding with PostgreSQL's own
			// same-named auto-created family for a class.
			return qualKey(oldSchema(o.Schema, o.RenamedFromSchema), *o.RenamedFrom) + " USING " + o.AccessMethod + " FAMILY"
		}
	case *ir.StatisticsObject:
		if o.RenamedFrom != nil {
			return qualKey(oldSchema(o.Schema, o.RenamedFromSchema), *o.RenamedFrom)
		}
	}
	return ""
}

// markAttachedFromTargets recurses through parts (a table's or partition's
// own Partitions list, RFC Section 7.13) and marks every ATTACHED FROM
// target's resolved qualified name in consumed — see Diff's own call site
// for why an ATTACHED FROM target must never be dropped by Pass 2 despite
// disappearing from desired as an independent top-level object.
func markAttachedFromTargets(schema string, parts []*ir.Partition, consumed map[string]bool) {
	for _, p := range parts {
		if p.AttachedFrom != nil {
			consumed[normalizeInheritRef(schema, *p.AttachedFrom)] = true
		}
		markAttachedFromTargets(schema, p.Partitions, consumed)
	}
}

// oldSchema resolves the schema a RENAMED FROM name lived in: the explicit
// RenamedFromSchema when the directive is schema-qualified (a rename
// combined with a cross-schema move), or the object's own (new) schema
// otherwise — the pre-existing, same-schema-only assumption every RENAMED
// FROM directive relied on before RenamedFromSchema existed.
func oldSchema(currentSchema string, renamedFromSchema *string) string {
	if renamedFromSchema != nil {
		return *renamedFromSchema
	}
	return currentSchema
}

func qualKey(schema, name string) string {
	if schema == "" {
		return name
	}
	return schema + "." + name
}

// describeKind returns a lowercase noun for an IRObject — used in user-facing
// error messages so "RENAMED FROM ..." failures name the kind concretely
// ("table", "column", ...) instead of a generic "object".
func describeKind(obj pipeline.IRObject) string {
	switch obj.(type) {
	case *ir.Table:
		return "table"
	case *ir.Schema:
		return "schema"
	case *ir.View:
		return "view"
	case *ir.Function:
		return "function"
	case *ir.Procedure:
		return "procedure"
	case *ir.Aggregate:
		return "aggregate"
	case *ir.Type:
		return "type"
	case *ir.Collation:
		return "collation"
	case *ir.Role:
		return "role"
	case *ir.Publication:
		return "publication"
	case *ir.ForeignServer:
		return "foreign server"
	case *ir.Tablespace:
		return "tablespace"
	case *ir.TSDict:
		return "text search dictionary"
	case *ir.TSParser:
		return "text search parser"
	case *ir.TSTemplate:
		return "text search template"
	case *ir.EventTrigger:
		return "event trigger"
	case *ir.ForeignDataWrapper:
		return "foreign data wrapper"
	case *ir.Subscription:
		return "subscription"
	case *ir.OperatorClass:
		return "operator class"
	case *ir.OperatorFamily:
		return "operator family"
	case *ir.StatisticsObject:
		return "statistics object"
	}
	return "object"
}

// ── SQL identifier helpers ─────────────────────────────────────────────────────

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func qualIdent(schema, name string) string {
	if schema == "" {
		return quoteIdent(name)
	}
	return quoteIdent(schema) + "." + quoteIdent(name)
}

// quoteQualIdent quotes a possibly schema-qualified "schema.name" string
// (as stored in ir.Table.Inherits) part by part, rather than wrapping the
// whole dotted string in one identifier, which would produce a single
// malformed quoted identifier instead of a schema-qualified one.
func quoteQualIdent(s string) string {
	schema, name, ok := strings.Cut(s, ".")
	if !ok {
		return quoteIdent(s)
	}
	return qualIdent(schema, name)
}

// qualOperatorIdent qualifies an operator symbol with its schema, like
// qualIdent, except the symbol itself (e.g. "===", "@>") is never quoted —
// unlike a regular identifier, it's a lexical operator token, and quoting it
// (as qualIdent would) is a PostgreSQL syntax error. Mirrors introspection's
// operatorRef (internal/introspect/opaque.go), which renders the same way
// for the same reason.
func qualOperatorIdent(schema, name string) string {
	if schema == "" {
		return name
	}
	return quoteIdent(schema) + "." + name
}

// quoteLit single-quotes a SQL string literal.
func quoteLit(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// hashText returns a SHA-256 hex digest of s (trimmed) — used wherever a
// declared-but-never-persisted-verbatim value (a secret reference or
// literal) needs offline drift detection without ever storing the value
// itself in the snapshot. Mirrors snapshot.hashBodyStr's exact formula,
// duplicated here since that one is unexported in a different package.
func hashText(s string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(s)))
	return fmt.Sprintf("%x", sum)
}

func ptrEq(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// stringSetEqual compares two string slices as sets (order-independent,
// duplicates collapsed) — used for EVENT TRIGGER's WHEN TAG IN (...) list,
// which PostgreSQL stores as an unordered evttags array.
func stringSetEqual(a, b []string) bool {
	if len(a) != len(b) {
		// A dedup-aware length check would be more correct for duplicate
		// entries, but WHEN TAG IN (...) listing the same tag twice is not
		// a real, meaningful shape (RFC Section 14.1's tag-list is just an
		// enumeration of distinct DDL command tags) — a plain length
		// mismatch is sufficient signal here without building a multiset.
		return false
	}
	set := make(map[string]bool, len(a))
	for _, s := range a {
		set[s] = true
	}
	for _, s := range b {
		if !set[s] {
			return false
		}
	}
	return true
}

// effectiveComment resolves an object's live COMMENT text — used for
// columns, tables, views, and functions (the RFC's own stated scope for
// DEPRECATED, Section 19.1: "Applied to tables, columns, views, functions").
// DEPRECATED (when set) always wins, rendered with the RFC's own
// "[DEPRECATED] msg" prefix — real PostgreSQL only ever stores one comment
// per object, so an explicit Comment is shadowed whenever Deprecated is
// also set, rather than inventing an undocumented combined format. Found
// live-testing a demo project: DEPRECATED was captured by the parser and
// snapshot and used by the linter (hence its warning appearing in `dpg
// plan` output), but the differ never referenced it at all for ANY of the
// four kinds — it had zero effect on the actual generated SQL, despite the
// RFC documenting `COMMENT ON COLUMN t.c IS '[DEPRECATED] msg'` (and the
// table/view/function equivalents) as the expected output.
func effectiveComment(comment, deprecated *string) *string {
	if deprecated != nil {
		s := "[DEPRECATED] " + *deprecated
		return &s
	}
	return comment
}

// diffCompositeAttrs diffs a composite type's attribute list against the
// snapshot, emitting the RFC Section 5.2-documented granular ops (ADD ATTRIBUTE =
// SAFE, DROP ATTRIBUTE = DESTRUCTIVE, ALTER ATTRIBUTE TYPE = DESTRUCTIVE,
// RENAME ATTRIBUTE via a COLUMN-equivalent RENAMED FROM sub-block) instead
// of the previous behavior, which treated ANY attribute change — including
// a pure, RFC-promised-safe addition — as a bare DROP TYPE + recreate, and
// couldn't detect a rename at all (an attribute rename looked like an
// unrelated drop+add). Deliberately not a call into diffColumns: composite
// type attributes support none of a table column's extra machinery
// (defaults, identity, generated, NOT NULL, grants, comments) — real
// PostgreSQL's ALTER TYPE ... ATTRIBUTE grammar only has ADD/DROP/ALTER
// TYPE/RENAME.
func diffCompositeAttrs(typeIdent string, attrs []*ir.Column, snap []snapshot.SnapColumn) ([]pipeline.DiffOp, error) {
	var ops []pipeline.DiffOp

	snapByName := make(map[string]*snapshot.SnapColumn, len(snap))
	for i := range snap {
		snapByName[snap[i].Name] = &snap[i]
	}
	desiredHasName := make(map[string]bool, len(attrs))
	for _, a := range attrs {
		desiredHasName[a.Name] = true
	}

	// Attributes renamed in desired: map old→new name. Same validation shape
	// as diffColumns' table-column rename handling (see its own doc comment),
	// scaled down to composite attrs' much smaller feature surface.
	renamedFrom := make(map[string]string) // snapName → desiredName
	for _, a := range attrs {
		if a.RenamedFrom == nil {
			continue
		}
		if desiredHasName[*a.RenamedFrom] {
			return nil, pipeline.Errorf(a.SrcPos,
				"RENAMED FROM %q on composite attribute %q in %s collides with another attribute of the same name in the desired declaration. Remove the stale attribute.",
				*a.RenamedFrom, a.Name, typeIdent)
		}
		_, oldInSnap := snapByName[*a.RenamedFrom]
		_, newInSnap := snapByName[a.Name]
		if newInSnap {
			// Post-apply / no-op state: the snapshot already has the new name.
			continue
		}
		if !oldInSnap {
			return nil, pipeline.ErrorfCode(a.SrcPos, "DPG-E021",
				"RENAMED FROM %q on composite attribute %q in %s does not match the snapshot — neither the old nor the new name exists there. Remove RENAMED FROM if this is a genuinely new attribute.",
				*a.RenamedFrom, a.Name, typeIdent)
		}
		renamedFrom[*a.RenamedFrom] = a.Name
		ops = append(ops, safeOp(
			fmt.Sprintf("ALTER TYPE %s RENAME ATTRIBUTE %s TO %s;", typeIdent, quoteIdent(*a.RenamedFrom), quoteIdent(a.Name)),
			a.SrcPos,
		))
	}

	// Drop attributes absent from desired (and not just renamed away).
	for _, sc := range snap {
		if _, ok := renamedFrom[sc.Name]; ok {
			continue
		}
		if !desiredHasName[sc.Name] {
			ops = append(ops, destructiveOp(
				fmt.Sprintf("ALTER TYPE %s DROP ATTRIBUTE %s;", typeIdent, quoteIdent(sc.Name)),
				pipeline.SourcePos{},
			))
		}
	}

	// Add new attributes, or alter the type of a changed one.
	for _, a := range attrs {
		snapAttrName := a.Name
		if a.RenamedFrom != nil {
			if _, ok := snapByName[*a.RenamedFrom]; ok {
				snapAttrName = *a.RenamedFrom
			}
		}
		sc, exists := snapByName[snapAttrName]
		resolvedType := a.Type.String()
		collateClause := ""
		if a.Collation != "" {
			collateClause = fmt.Sprintf(" COLLATE %s", quoteQualifiedIdent(a.Collation))
		}
		if !exists {
			ops = append(ops, safeOp(
				fmt.Sprintf("ALTER TYPE %s ADD ATTRIBUTE %s %s%s;", typeIdent, quoteIdent(a.Name), resolvedType, collateClause),
				a.SrcPos,
			))
			ops = append(ops, compositeAttrCommentOps(typeIdent, a, nil)...)
			continue
		}
		// RFC Section 5.2: a type or COLLATE change are both DESTRUCTIVE, via the
		// same ALTER ATTRIBUTE TYPE statement (COLLATE is an optional
		// trailing clause on it, not a separate ALTER).
		if (resolvedType != sc.Type && !legacyVirtualTypeSnapshot(a, sc)) || a.Collation != sc.Collation {
			ops = append(ops, destructiveOp(
				fmt.Sprintf("ALTER TYPE %s ALTER ATTRIBUTE %s TYPE %s%s;", typeIdent, quoteIdent(a.Name), resolvedType, collateClause),
				a.SrcPos,
			))
		}
		ops = append(ops, compositeAttrCommentOps(typeIdent, a, sc.Comment)...)
	}

	return ops, nil
}

func ptrStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func normalizeWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ── Grant helpers ─────────────────────────────────────────────────────────────

// grantKey returns a canonical string key for a grant entry, allowing grant
// sets to be compared regardless of ordering in the source file.
func grantKey(privs []string, roles []string, withGrant bool) string {
	p := append([]string(nil), privs...)
	sort.Strings(p)
	r := append([]string(nil), roles...)
	sort.Strings(r)
	if len(p) == 0 {
		p = []string{"ALL"}
	}
	wg := ""
	if withGrant {
		wg = "+wg"
	}
	return strings.Join(p, ",") + "|" + strings.Join(r, ",") + wg
}

// privStr returns the SQL privilege list, or ALL for an empty list.
func privStr(privs []string) string {
	if len(privs) == 0 {
		return "ALL"
	}
	return strings.Join(privs, ", ")
}

// roleList quotes and joins a list of role names. PUBLIC is PostgreSQL's
// pseudo-role keyword, not an actual role — GRANT/REVOKE ... TO/FROM PUBLIC
// must stay bare. Quoting it (as every other role name is) makes PG look up
// an actual role literally named "PUBLIC", which doesn't exist and errors
// with "role \"PUBLIC\" does not exist". Confirmed live: introspection's
// grant queries emit the literal string "PUBLIC" as the grantee for PG's
// own implicit default EXECUTE-to-PUBLIC grant (grantee = 0), across every
// grant-bearing object kind (tables, columns, views, functions, sequences,
// aggregates, ...) — any REVOKE reconciling that implicit grant away hit
// this.
func roleList(roles []string) string {
	quoted := make([]string, len(roles))
	for i, r := range roles {
		if strings.EqualFold(r, "PUBLIC") {
			quoted[i] = "PUBLIC"
		} else {
			quoted[i] = quoteIdent(r)
		}
	}
	return strings.Join(quoted, ", ")
}

// roleSpecSQL renders a role-spec (Appendix A's shared production): the
// three fixed keywords CURRENT_ROLE/CURRENT_USER/SESSION_USER render bare
// (uppercase, matching parseRoleSpec's own normalisation), any other value
// is a plain role name and gets quoted like any other identifier.
func roleSpecSQL(s string) string {
	switch strings.ToUpper(s) {
	case "CURRENT_ROLE", "CURRENT_USER", "SESSION_USER":
		return strings.ToUpper(s)
	default:
		return quoteIdent(s)
	}
}

// grantOptSuffix renders a Grant's optional trailing clauses, shared by
// every GRANT statement this codebase emits: WITH GRANT OPTION (RFC
// Section 11.2) and GRANTED BY role-spec (RFC Section 7.10/11.3, audit item
// #90).
func grantOptSuffix(g ir.Grant) string {
	s := ""
	if g.WithGrant {
		s += " WITH GRANT OPTION"
	}
	if g.GrantedBy != nil {
		s += " GRANTED BY " + roleSpecSQL(*g.GrantedBy)
	}
	return s
}

// revokeOptSuffix is grantOptSuffix's REVOKE counterpart: GRANTED BY
// role-spec then CASCADE, matching RFC's revoke-entry clause order.
func revokeOptSuffix(r ir.Revocation) string {
	s := ""
	if r.GrantedBy != nil {
		s += " GRANTED BY " + roleSpecSQL(*r.GrantedBy)
	}
	if r.Cascade {
		s += " CASCADE"
	}
	return s
}

// grantedByMatches reports whether a declared GrantedBy role-spec (desired,
// nil when not declared) is satisfied by an already-applied grant's known
// GrantedBy (live, nil when introspection couldn't determine one — e.g. a
// pre-existing snapshot from before grantor capture, or a Revocation, which
// is never introspected from live state at all).
//
// desired == nil is the "declared, so managed" skip: never compared, see
// diffGrantSet's own comment. Otherwise, the three fixed role-spec keywords
// (roleSpecSQL's identical list) never mismatch against any live grantor:
// confirmed live via direct psql that PostgreSQL unconditionally restricts a
// GRANT's effective grantor to whichever role executes the statement
// ("grantor must be current user" naming anyone else, even for a
// superuser), so once introspection reports ANY concrete grantor for an
// already-applied grant, a declared symbolic role-spec is proven satisfied
// — there is no other value it could have resolved to. A literal role-name
// declaration has no such guarantee, so it's compared for real equality.
func grantedByMatches(desired, live *string) bool {
	if desired == nil {
		return true
	}
	switch strings.ToUpper(*desired) {
	case "CURRENT_ROLE", "CURRENT_USER", "SESSION_USER":
		return live != nil
	default:
		return ptrEq(desired, live)
	}
}

// diffGrantSet diffs two grant lists and emits GRANT/REVOKE ops.
// onClause is the SQL object specifier after ON, e.g. "TABLE \"public\".\"users\"".
func diffGrantSet(
	snapGrants []snapshot.SnapGrant,
	desiredGrants []ir.Grant,
	onClause string,
	pos pipeline.SourcePos,
) []pipeline.DiffOp {
	var ops []pipeline.DiffOp

	snapByKey := make(map[string]snapshot.SnapGrant, len(snapGrants))
	for _, g := range snapGrants {
		snapByKey[grantKey(g.Privileges, g.Roles, g.WithGrant)] = g
	}
	desiredByKey := make(map[string]ir.Grant, len(desiredGrants))
	for _, g := range desiredGrants {
		desiredByKey[grantKey(g.Privileges, g.Roles, g.WithGrant)] = g
	}

	for k, sg := range snapByKey {
		if _, ok := desiredByKey[k]; !ok {
			// Implicit revoke (a GRANT simply removed from source) can break
			// another role's dependent access exactly like an explicit
			// REVOKE does — classified cautionOp to match DEFAULT
			// PRIVILEGES's already-correct handling of the identical
			// real-world event (RFC audit item #25).
			ops = append(ops, cautionOp(
				fmt.Sprintf("REVOKE %s ON %s FROM %s;", privStr(sg.Privileges), onClause, roleList(sg.Roles)),
				pos,
			))
		}
	}
	for k, g := range desiredByKey {
		sg, matched := snapByKey[k]
		if !matched {
			sql := fmt.Sprintf("GRANT %s ON %s TO %s", privStr(g.Privileges), onClause, roleList(g.Roles))
			sql += grantOptSuffix(g)
			ops = append(ops, safeOp(sql+";", pos))
			continue
		}
		// Matched: GrantedBy isn't part of grantKey (see grantKey's own doc
		// comment) — only re-GRANT when the desired side explicitly
		// declares a GrantedBy that isn't satisfied by the snapshot's own
		// value. An undeclared GrantedBy (nil) is never compared, the same
		// "declared, so managed" rule already used for Function Cost/Rows —
		// a live-introspected concrete grantor must never cause spurious
		// drift here (RFC audit item #90). See grantedByMatches for why a
		// symbolic role-spec (CURRENT_USER etc.) is never itself drift
		// against a live grantor.
		if !grantedByMatches(g.GrantedBy, sg.GrantedBy) {
			sql := fmt.Sprintf("GRANT %s ON %s TO %s", privStr(g.Privileges), onClause, roleList(g.Roles))
			sql += grantOptSuffix(g)
			ops = append(ops, safeOp(sql+";", pos))
		}
	}
	return ops
}

// diffRevocationSet diffs an explicit REVOKES list (RFC Section 11.3) against its
// snapshot. Unlike grants (the additive model in diffGrantSet — removing a
// GRANT declaration emits nothing), a revocation is itself tracked as a
// persistent declaration: once applied it's remembered in the snapshot so a
// later unchanged run doesn't re-issue REVOKE — not because re-running it
// would error (REVOKE on an already-absent privilege is a harmless no-op in
// PG), but to keep migration output free of redundant statements, matching
// the same idempotency style already used for grants. Removing a REVOKE
// entry re-GRANTs the privilege to restore it. Mirrors diffDefaultPrivileges's
// existing revocation-diffing logic, generalized so table/view/column
// REVOKE can share it.
func diffRevocationSet(
	snapRevs []snapshot.SnapGrant,
	desiredRevs []ir.Revocation,
	onClause string,
	pos pipeline.SourcePos,
) []pipeline.DiffOp {
	var ops []pipeline.DiffOp

	snapByKey := make(map[string]snapshot.SnapGrant, len(snapRevs))
	for _, r := range snapRevs {
		snapByKey[grantKey(r.Privileges, r.Roles, false)] = r
	}
	desiredByKey := make(map[string]ir.Revocation, len(desiredRevs))
	for _, r := range desiredRevs {
		desiredByKey[grantKey(r.Privileges, r.Roles, false)] = r
	}

	for k, sr := range snapByKey {
		if _, ok := desiredByKey[k]; !ok {
			// Revocation removed from desired: re-grant to restore.
			ops = append(ops, safeOp(
				fmt.Sprintf("GRANT %s ON %s TO %s;", privStr(sr.Privileges), onClause, roleList(sr.Roles)),
				pos,
			))
		}
	}
	for k, r := range desiredByKey {
		sr, matched := snapByKey[k]
		if !matched {
			ops = append(ops, cautionOp(
				fmt.Sprintf("REVOKE %s ON %s FROM %s%s;", privStr(r.Privileges), onClause, roleList(r.Roles), revokeOptSuffix(r)),
				pos,
			))
			continue
		}
		// Matched: same "declared, so managed" GrantedBy re-check as
		// diffGrantSet's identical branch (RFC audit item #90).
		if !grantedByMatches(r.GrantedBy, sr.GrantedBy) {
			ops = append(ops, cautionOp(
				fmt.Sprintf("REVOKE %s ON %s FROM %s%s;", privStr(r.Privileges), onClause, roleList(r.Roles), revokeOptSuffix(r)),
				pos,
			))
		}
	}
	return ops
}

// securityLabelSQL renders a single "SECURITY LABEL [FOR provider] ON
// <onClause> IS ..." statement (RFC Section 14.11). label == nil renders IS NULL
// (removes the label for that provider); provider == "" omits the FOR
// clause, letting PostgreSQL resolve it to the sole loaded provider.
func securityLabelSQL(provider, onClause string, label *string) string {
	var b strings.Builder
	b.WriteString("SECURITY LABEL ")
	if provider != "" {
		b.WriteString("FOR ")
		b.WriteString(quoteIdent(provider))
		b.WriteString(" ")
	}
	b.WriteString("ON ")
	b.WriteString(onClause)
	b.WriteString(" IS ")
	if label == nil {
		b.WriteString("NULL")
	} else {
		b.WriteString(quoteLit(*label))
	}
	b.WriteString(";")
	return b.String()
}

// diffSecurityLabelSet diffs a SECURITY LABEL list (RFC Section 14.11) against its
// snapshot, keyed by provider — PostgreSQL lets several independent label
// providers label the same object simultaneously, and each provider's own
// label is an independent catalog row (pg_seclabel's primary key includes
// provider), so two entries for different providers are never in conflict
// the way two GRANT entries for the same (privilege, role) pair would be.
// Unlike diffGrantSet's additive model, a removed provider entry emits an
// explicit "IS NULL" (SECURITY LABEL has no separate REVOKE-shaped
// statement — NULL is real PostgreSQL's own documented way to clear a
// label). All ops are Safe: SECURITY LABEL never touches data, only catalog
// metadata, same classification COMMENT ON already gets throughout this file.
func diffSecurityLabelSet(
	snapLabels []snapshot.SnapSecurityLabel,
	desiredLabels []pipeline.SecurityLabel,
	onClause string,
	pos pipeline.SourcePos,
) []pipeline.DiffOp {
	var ops []pipeline.DiffOp

	snapByKey := make(map[string]snapshot.SnapSecurityLabel, len(snapLabels))
	for _, l := range snapLabels {
		snapByKey[l.Provider] = l
	}
	desiredByKey := make(map[string]pipeline.SecurityLabel, len(desiredLabels))
	for _, l := range desiredLabels {
		desiredByKey[l.Provider] = l
	}

	for k, sl := range snapByKey {
		if _, ok := desiredByKey[k]; !ok {
			ops = append(ops, safeOp(securityLabelSQL(sl.Provider, onClause, nil), pos))
		}
	}
	for k, dl := range desiredByKey {
		if sl, ok := snapByKey[k]; !ok || sl.Label != dl.Label {
			label := dl.Label
			ops = append(ops, safeOp(securityLabelSQL(dl.Provider, onClause, &label), pos))
		}
	}
	return ops
}

// createSecurityLabelOps renders one SAFE op per declared SecurityLabel
// entry, for use at object-creation time (mirroring how createTable/
// createView/etc. emit one op per Grant — see diffSecurityLabelSet's doc
// comment for why this can never be a REVOKE-shaped removal at create time).
func createSecurityLabelOps(labels []pipeline.SecurityLabel, onClause string, pos pipeline.SourcePos) []pipeline.DiffOp {
	ops := make([]pipeline.DiffOp, len(labels))
	for i, l := range labels {
		label := l.Label
		ops[i] = safeOp(securityLabelSQL(l.Provider, onClause, &label), pos)
	}
	return ops
}

// ── DROP operations ───────────────────────────────────────────────────────────

func dropObject(so *snapshot.SnapObject) []pipeline.DiffOp {
	zero := pipeline.SourcePos{}
	switch so.Kind {
	case "schema":
		if so.Schema != nil {
			return []pipeline.DiffOp{
				destructiveOp(fmt.Sprintf("DROP SCHEMA IF EXISTS %s;", quoteIdent(so.Schema.Name)), zero),
			}
		}
	case "extension":
		if so.Extension != nil {
			return []pipeline.DiffOp{
				destructiveOp(fmt.Sprintf("DROP EXTENSION IF EXISTS %s;", quoteIdent(so.Extension.Name)), zero),
			}
		}
	case "table":
		if so.Table != nil {
			t := so.Table
			suffix := ""
			if t.DropCascade {
				suffix = " CASCADE"
			}
			return []pipeline.DiffOp{
				destructiveOp(fmt.Sprintf("DROP TABLE IF EXISTS %s%s;", qualIdent(t.Schema, t.Name), suffix), zero),
			}
		}
	case "view":
		if so.View != nil {
			v := so.View
			return []pipeline.DiffOp{
				destructiveOp(fmt.Sprintf("DROP VIEW IF EXISTS %s;", qualIdent(v.Schema, v.Name)), zero),
			}
		}
	case "function":
		if so.Function != nil {
			f := so.Function
			return []pipeline.DiffOp{
				destructiveOp(fmt.Sprintf("DROP FUNCTION IF EXISTS %s(%s);", qualIdent(f.Schema, f.Name), f.Args), zero),
			}
		}
	case "type":
		if so.Type != nil {
			t := so.Type
			return []pipeline.DiffOp{
				destructiveOp(fmt.Sprintf("DROP TYPE IF EXISTS %s;", qualIdent(t.Schema, t.Name)), zero),
			}
		}
	case "sequence":
		if so.Sequence != nil {
			s := so.Sequence
			return []pipeline.DiffOp{
				destructiveOp(fmt.Sprintf("DROP SEQUENCE IF EXISTS %s;", qualIdent(s.Schema, s.Name)), zero),
			}
		}
	case "role":
		if so.Role != nil {
			return []pipeline.DiffOp{
				destructiveOp(fmt.Sprintf("DROP ROLE IF EXISTS %s;", quoteIdent(so.Role.Name)), zero),
			}
		}
	case "procedure":
		if so.Opaque != nil {
			return []pipeline.DiffOp{destructiveOp(
				fmt.Sprintf("DROP PROCEDURE IF EXISTS %s(%s);", qualIdent(so.Opaque.Schema, so.Opaque.Name), so.Opaque.Args),
				zero,
			)}
		}
	case "aggregate":
		if so.Opaque != nil {
			return []pipeline.DiffOp{destructiveOp(
				fmt.Sprintf("DROP AGGREGATE IF EXISTS %s(%s);", qualIdent(so.Opaque.Schema, so.Opaque.Name), so.Opaque.Args),
				zero,
			)}
		}
	case "tablespace":
		if so.Opaque != nil {
			// DROP TABLESPACE cannot run inside a transaction block
			// (confirmed live) — same restriction as CREATE TABLESPACE.
			return []pipeline.DiffOp{nonTxnOp(pipeline.Destructive, fmt.Sprintf("DROP TABLESPACE IF EXISTS %s;", quoteIdent(so.Opaque.Name)), zero)}
		}
	case "fdw":
		if so.Opaque != nil {
			return []pipeline.DiffOp{destructiveOp(fmt.Sprintf("DROP FOREIGN DATA WRAPPER IF EXISTS %s;", quoteIdent(so.Opaque.Name)), zero)}
		}
	case "server":
		if so.Opaque != nil {
			return []pipeline.DiffOp{destructiveOp(fmt.Sprintf("DROP SERVER IF EXISTS %s;", quoteIdent(so.Opaque.Name)), zero)}
		}
	case "user_mapping":
		if so.Opaque != nil {
			parts := strings.SplitN(so.Opaque.Name, "@", 2)
			if len(parts) == 2 {
				// An empty user is a FOR PUBLIC mapping; quoting "" would emit a
				// zero-length identifier and abort the migration.
				forClause := "PUBLIC"
				if parts[0] != "" {
					forClause = quoteIdent(parts[0])
				}
				return []pipeline.DiffOp{destructiveOp(fmt.Sprintf("DROP USER MAPPING IF EXISTS FOR %s SERVER %s;", forClause, quoteIdent(parts[1])), zero)}
			}
		}
	case "publication":
		if so.Opaque != nil {
			return []pipeline.DiffOp{destructiveOp(fmt.Sprintf("DROP PUBLICATION IF EXISTS %s;", quoteIdent(so.Opaque.Name)), zero)}
		}
	case "subscription":
		if so.Opaque != nil {
			// Non-transactional, not destructiveOp: DROP SUBSCRIPTION
			// errors "cannot run inside a transaction block" (confirmed
			// live) — the same non-transactional constraint CREATE
			// SUBSCRIPTION has, see createSubscription's doc comment.
			return []pipeline.DiffOp{nonTxnOp(pipeline.Destructive, fmt.Sprintf("DROP SUBSCRIPTION IF EXISTS %s;", quoteIdent(so.Opaque.Name)), zero)}
		}
	case "event_trigger":
		if so.Opaque != nil {
			// Unlike its opaque-tier siblings (Collation, Cast, Operator, ...),
			// an event trigger holds no data and nothing else in the catalog
			// depends on it — RFC Section 14.1 explicitly classifies its DROP+CREATE
			// cycle as SAFE ("no data involved"), not DESTRUCTIVE like the
			// rest of dropObject's cases.
			return []pipeline.DiffOp{safeOp(fmt.Sprintf("DROP EVENT TRIGGER IF EXISTS %s;", quoteIdent(so.Opaque.Name)), zero)}
		}
	case "collation":
		if so.Opaque != nil {
			return []pipeline.DiffOp{destructiveOp(fmt.Sprintf("DROP COLLATION IF EXISTS %s;", qualIdent(so.Opaque.Schema, so.Opaque.Name)), zero)}
		}
	case "operator":
		if so.Opaque != nil {
			return []pipeline.DiffOp{destructiveOp(fmt.Sprintf("DROP OPERATOR IF EXISTS %s(%s);", qualOperatorIdent(so.Opaque.Schema, so.Opaque.Name), so.Opaque.Args), zero)}
		}
	case "operator_class":
		if so.Opaque != nil {
			return []pipeline.DiffOp{destructiveOp(fmt.Sprintf("DROP OPERATOR CLASS IF EXISTS %s USING %s;", qualIdent(so.Opaque.Schema, so.Opaque.Name), accessMethodOrDefault(so.Opaque.Using)), zero)}
		}
	case "operator_family":
		if so.Opaque != nil {
			return []pipeline.DiffOp{destructiveOp(fmt.Sprintf("DROP OPERATOR FAMILY IF EXISTS %s USING %s;", qualIdent(so.Opaque.Schema, so.Opaque.Name), accessMethodOrDefault(so.Opaque.Using)), zero)}
		}
	case "cast":
		if so.Opaque != nil {
			parts := strings.SplitN(so.Opaque.Name, "->", 2)
			if len(parts) == 2 {
				return []pipeline.DiffOp{destructiveOp(fmt.Sprintf("DROP CAST IF EXISTS (%s AS %s);", parts[0], parts[1]), zero)}
			}
		}
	case "statistics":
		if so.Opaque != nil {
			return []pipeline.DiffOp{destructiveOp(fmt.Sprintf("DROP STATISTICS IF EXISTS %s;", qualIdent(so.Opaque.Schema, so.Opaque.Name)), zero)}
		}
	case "ts_config":
		if so.Opaque != nil {
			return []pipeline.DiffOp{destructiveOp(fmt.Sprintf("DROP TEXT SEARCH CONFIGURATION IF EXISTS %s;", qualIdent(so.Opaque.Schema, so.Opaque.Name)), zero)}
		}
	case "ts_dict":
		if so.Opaque != nil {
			return []pipeline.DiffOp{destructiveOp(fmt.Sprintf("DROP TEXT SEARCH DICTIONARY IF EXISTS %s;", qualIdent(so.Opaque.Schema, so.Opaque.Name)), zero)}
		}
	case "ts_parser":
		if so.Opaque != nil {
			return []pipeline.DiffOp{destructiveOp(fmt.Sprintf("DROP TEXT SEARCH PARSER IF EXISTS %s;", qualIdent(so.Opaque.Schema, so.Opaque.Name)), zero)}
		}
	case "ts_template":
		if so.Opaque != nil {
			return []pipeline.DiffOp{destructiveOp(fmt.Sprintf("DROP TEXT SEARCH TEMPLATE IF EXISTS %s;", qualIdent(so.Opaque.Schema, so.Opaque.Name)), zero)}
		}
	case "default_privileges":
		if so.DefaultPrivileges != nil {
			// Revoking a DEFAULT PRIVILEGES declaration means revoking all its grants.
			dp := so.DefaultPrivileges
			prefix := buildDefaultPrivPrefix(dp.ForRole, dp.InSchema)
			var ops []pipeline.DiffOp
			for _, g := range dp.Grants {
				ops = append(ops, cautionOp(
					fmt.Sprintf("%s REVOKE %s ON %s FROM %s;",
						prefix, privStr(g.Privileges), dp.ObjectType, roleList(g.Roles)),
					zero,
				))
			}
			return ops
		}
	case "parameter_privileges":
		// Unlike default_privileges (whose drop handler revokes every
		// grant), removing the entire PARAMETER PRIVILEGES declaration is
		// just the union of removing each of its individual grant
		// declarations — the additive model's "emits nothing" applies the
		// same way here (see diffParameterPrivileges' doc comment).
		return nil
	case "virtual_type":
		// Virtual types have no SQL backing — nothing to drop.
		return nil
	}
	return nil
}

// ── CREATE operations ─────────────────────────────────────────────────────────

func createObject(obj pipeline.IRObject) ([]pipeline.DiffOp, error) {
	switch o := obj.(type) {
	case *ir.Schema:
		return createSchema(o), nil
	case *ir.Extension:
		return createExtension(o), nil
	case *ir.Table:
		return createTable(o), nil
	case *ir.View:
		return createView(o), nil
	case *ir.Function:
		return createFunction(o), nil
	case *ir.Type:
		return createType(o), nil
	case *ir.Sequence:
		return createSequence(o), nil
	case *ir.Role:
		return createRole(o), nil
	case *ir.Procedure:
		return createProcedure(o), nil
	case *ir.Aggregate:
		return createAggregate(o)
	case *ir.Tablespace:
		ops, err := createOpaque(o.Name, o.Body, "TABLESPACE", "", o.SrcPos)
		if err == nil {
			ops = append(ops, createSimpleGrantOps("TABLESPACE", quoteIdent(o.Name), o.Grants, o.Revocations, o.SrcPos, true)...)
		}
		ops, err = appendCommentOp(ops, err, "tablespace", "", o.Name, "", "", o.Comment, o.SrcPos)
		return appendSecurityLabelOps(ops, err, "tablespace", "", o.Name, "", "", o.SecurityLabels, o.SrcPos)
	case *ir.ForeignDataWrapper:
		ops, err := createOpaque(o.Name, o.Body, "FOREIGN DATA WRAPPER", "", o.SrcPos)
		if err == nil {
			ops = append(ops, createSimpleGrantOps("FOREIGN DATA WRAPPER", quoteIdent(o.Name), o.Grants, o.Revocations, o.SrcPos, false)...)
		}
		return appendCommentOp(ops, err, "fdw", "", o.Name, "", "", o.Comment, o.SrcPos)
	case *ir.ForeignServer:
		ops, err := createOpaque(o.Name, o.Body, "SERVER", "", o.SrcPos)
		if err == nil && o.Owner != nil && len(ops) > 0 {
			ops = append(wrapCreateWithOwner(ops[0], o.Owner, o.SrcPos), ops[1:]...)
		}
		if err == nil {
			ops = append(ops, createSimpleGrantOps("FOREIGN SERVER", quoteIdent(o.Name), o.Grants, o.Revocations, o.SrcPos, false)...)
		}
		return appendCommentOp(ops, err, "server", "", o.Name, "", "", o.Comment, o.SrcPos)
	case *ir.UserMapping:
		return createUserMapping(o)
	case *ir.Publication:
		ops, err := createOpaque(o.Name, o.Body, "PUBLICATION", "", o.SrcPos)
		if err == nil && o.Owner != nil && len(ops) > 0 {
			ops = append(wrapCreateWithOwner(ops[0], o.Owner, o.SrcPos), ops[1:]...)
		}
		ops, err = appendCommentOp(ops, err, "publication", "", o.Name, "", "", o.Comment, o.SrcPos)
		return appendSecurityLabelOps(ops, err, "publication", "", o.Name, "", "", o.SecurityLabels, o.SrcPos)
	case *ir.Subscription:
		return createSubscription(o)
	case *ir.EventTrigger:
		ops, err := createOpaque(o.Name, o.Body, "EVENT TRIGGER", "", o.SrcPos)
		if err == nil && o.Owner != nil && len(ops) > 0 {
			ops = append(wrapCreateWithOwner(ops[0], o.Owner, o.SrcPos), ops[1:]...)
		}
		ops, err = appendCommentOp(ops, err, "event_trigger", "", o.Name, "", "", o.Comment, o.SrcPos)
		// trigger-enable-state (Section 14.1) has no inline CREATE EVENT
		// TRIGGER form (confirmed empirically — a new event trigger is
		// always ENABLED per real PostgreSQL's own grammar), only
		// ALTER EVENT TRIGGER, even at initial creation — same reasoning as
		// table Trigger's identical createTrigger follow-up.
		if err == nil && o.EnableState != "" {
			ops = append(ops, safeOp(eventTriggerEnableStateSQL(o.Name, o.EnableState), o.SrcPos))
		}
		return appendSecurityLabelOps(ops, err, "event_trigger", "", o.Name, "", "", o.SecurityLabels, o.SrcPos)
	case *ir.Collation:
		ops, err := createOpaque(o.QualifiedName(), o.Body, "COLLATION", o.Schema, o.SrcPos)
		if err == nil && o.Owner != nil && len(ops) > 0 {
			ops = append(wrapCreateWithOwner(ops[0], o.Owner, o.SrcPos), ops[1:]...)
		}
		ops, err = appendCommentOp(ops, err, "collation", o.Schema, o.Name, "", "", o.Comment, o.SrcPos)
		if err == nil && o.RefreshVersion {
			ops = append(ops, collationRefreshVersionOp(o))
		}
		return ops, err
	case *ir.Operator:
		ops, err := createOpaque(o.QualifiedName(), o.Body, "OPERATOR", o.Schema, o.SrcPos)
		return appendCommentOp(ops, err, "operator", o.Schema, o.Name, ir.OperandsKey(o.LeftType, o.RightType), "", o.Comment, o.SrcPos)
	case *ir.OperatorClass:
		ops, err := createOpaque(o.QualifiedName(), o.Body, "OPERATOR CLASS", o.Schema, o.SrcPos)
		return appendCommentOp(ops, err, "operator_class", o.Schema, o.Name, "", o.AccessMethod, o.Comment, o.SrcPos)
	case *ir.OperatorFamily:
		ops, err := createOpaque(o.QualifiedName(), o.Body, "OPERATOR FAMILY", o.Schema, o.SrcPos)
		ops, err = appendCommentOp(ops, err, "operator_family", o.Schema, o.Name, "", o.AccessMethod, o.Comment, o.SrcPos)
		if err != nil {
			return ops, err
		}
		famIdent := qualIdent(o.Schema, o.Name)
		for _, m := range o.Members {
			ops = append(ops, safeOp(opFamilyAddSQL(famIdent, o.AccessMethod, m), o.SrcPos))
		}
		return ops, nil
	case *ir.Cast:
		ops, err := createOpaque(o.QualifiedName(), o.Body, "CAST", "", o.SrcPos)
		return appendCommentOp(ops, err, "cast", "", o.QualifiedName(), "", "", o.Comment, o.SrcPos)
	case *ir.StatisticsObject:
		ops, err := createOpaque(o.QualifiedName(), o.Body, "STATISTICS", o.Schema, o.SrcPos)
		// CREATE STATISTICS has no target clause (RFC Section 14.6).
		if err == nil && o.StatisticsTarget != nil {
			ops = append(ops, safeOp(fmt.Sprintf("ALTER STATISTICS %s SET STATISTICS %d;",
				qualIdent(o.Schema, o.Name), *o.StatisticsTarget), o.SrcPos))
		}
		return appendCommentOp(ops, err, "statistics", o.Schema, o.Name, "", "", o.Comment, o.SrcPos)
	case *ir.TSConfig:
		ops, err := createOpaque(o.QualifiedName(), o.Body, "TEXT SEARCH CONFIGURATION", o.Schema, o.SrcPos)
		if err == nil && o.Owner != nil && len(ops) > 0 {
			ops = append(wrapCreateWithOwner(ops[0], o.Owner, o.SrcPos), ops[1:]...)
		}
		ops, err = appendCommentOp(ops, err, "ts_config", o.Schema, o.Name, "", "", o.Comment, o.SrcPos)
		if err != nil {
			return ops, err
		}
		for _, m := range o.Mappings {
			ops = append(ops, safeOp(tsMappingAlterSQL(qualIdent(o.Schema, o.Name), m), o.SrcPos))
		}
		return ops, nil
	case *ir.TSDict:
		ops, err := createOpaque(o.QualifiedName(), o.Body, "TEXT SEARCH DICTIONARY", o.Schema, o.SrcPos)
		if err == nil && o.Owner != nil && len(ops) > 0 {
			ops = append(wrapCreateWithOwner(ops[0], o.Owner, o.SrcPos), ops[1:]...)
		}
		return appendCommentOp(ops, err, "ts_dict", o.Schema, o.Name, "", "", o.Comment, o.SrcPos)
	case *ir.TSParser:
		ops, err := createOpaque(o.QualifiedName(), o.Body, "TEXT SEARCH PARSER", o.Schema, o.SrcPos)
		return appendCommentOp(ops, err, "ts_parser", o.Schema, o.Name, "", "", o.Comment, o.SrcPos)
	case *ir.TSTemplate:
		ops, err := createOpaque(o.QualifiedName(), o.Body, "TEXT SEARCH TEMPLATE", o.Schema, o.SrcPos)
		return appendCommentOp(ops, err, "ts_template", o.Schema, o.Name, "", "", o.Comment, o.SrcPos)
	case *ir.DefaultPrivileges:
		return createDefaultPrivileges(o), nil
	case *ir.ParameterPrivileges:
		return createParameterPrivileges(o), nil
	case *ir.VirtualType:
		// Virtual types exist in the snapshot for downstream consumers but
		// generate no SQL — they have no backing PostgreSQL object.
		return nil, nil
	}
	return nil, nil
}

// createOpaque emits a CREATE statement from a pre-built Body SQL string.
// Returns an error if Body is empty — the builder failed to capture the source SQL,
// which would otherwise produce a silent no-op migration.
//
// schema is the object's own declared/inferred schema for kinds that are
// genuinely schema-scoped in PostgreSQL (COLLATION, OPERATOR, OPERATOR
// CLASS/FAMILY, STATISTICS, the 4 TEXT SEARCH kinds) — pass "" for kinds
// that aren't (TABLESPACE, FOREIGN DATA WRAPPER, SERVER, PUBLICATION, EVENT
// TRIGGER are cluster/database-level; CAST has no schema concept at all,
// identified purely by its source/target type pair).
//
// Body is Part1 deparsed as written in source — it only carries a
// schema-qualified object name if the user happened to write one
// explicitly; DPG's normal schema inference (directory placement, an
// enclosing SCHEMA { } block) is never baked into this raw text the way
// createTable/createView etc. explicitly qualify their own identifiers.
// Confirmed live: a STATISTICS object declared under a non-public schema
// context landed in `public` instead — PostgreSQL resolves an unqualified
// CREATE target through search_path, and the opaque Body's own unqualified
// name says nothing about DPG's tracked Schema. Fixed the same way pg_dump
// itself handles this (SET search_path before each unqualified opaque
// statement) rather than attempting to string-rewrite 9 differently-shaped
// CREATE statements to inject a qualified name — safe because these ops
// already run inside the migration's single transaction, and SET LOCAL's
// scope ends at that transaction's COMMIT, so it can never leak into a
// later, unrelated migration. ", public" is appended as a fallback so an
// unqualified reference elsewhere in the same body (e.g. STATISTICS' FROM
// table) still resolves normally when that referent lives in public.
func createOpaque(name, body, kind, schema string, pos pipeline.SourcePos) ([]pipeline.DiffOp, error) {
	if body == "" {
		return nil, fmt.Errorf("%s %s: body not captured; define it explicitly in a .dpg source file", kind, name)
	}
	sql := body + ";"
	if schema != "" && schema != "public" {
		sql = fmt.Sprintf("SET LOCAL search_path = %s, public;\n%s", quoteIdent(schema), sql)
	}
	// CREATE TABLESPACE cannot run inside a transaction block (confirmed
	// live: "ERROR: CREATE TABLESPACE cannot run inside a transaction
	// block") — unlike every other opaque kind this function handles (FDW,
	// SERVER, EVENT TRIGGER, COLLATION, etc.), which all run fine
	// transactionally. kind arrives both uppercase (createObject's literal
	// "TABLESPACE") and lowercase snake_case (diffOpaqueIR's snap.Kind
	// "tablespace"), hence the case-insensitive compare.
	if strings.EqualFold(kind, "tablespace") {
		return []pipeline.DiffOp{nonTxnOp(pipeline.Safe, sql, pos)}, nil
	}
	return []pipeline.DiffOp{safeOp(sql, pos)}, nil
}

// commentOnOpaqueSQL renders "COMMENT ON <kind-specific identity> IS
// <lit-or-NULL>;" for the 14 opaque kinds that carry a Comment field
// (tablespace, fdw, server, event_trigger, collation, operator,
// operator_class, operator_family, cast, statistics, and all 4 TS kinds) —
// found live-testing a demo project: PostgreSQL genuinely supports
// COMMENT ON for every one of these (confirmed via \h COMMENT against a
// real server), but the blockparser's generic { COMMENT '...'; } was
// silently discarded for 9 of them (no field existed at all) and captured-
// but-never-emitted for the other 5 — dpg plan reported "-- (no changes)"
// with no error and no effect either way.
//
// kind uses the same lowercase snake_case vocabulary as dropObject/
// snapshot.SnapOpaque.Kind (not createOpaque's own uppercase display kind),
// so callers already holding a SnapOpaque (the diff/update path) can pass
// its Kind/Schema/Name/Args/Using straight through with no translation.
// Reuses the exact same per-kind identity shapes dropObject already relies
// on, so a comment always targets the identical object dropObject would —
// operator/operator class/operator family/cast all need their special
// (non-plain-identifier) COMMENT ON syntax, confirmed against \h COMMENT.
// Returns "" for an unrecognized kind.
// opaqueOnClause builds the "<OBJECT TYPE> <identity>" clause shared by
// COMMENT ON and SECURITY LABEL ON for every opaque kind — both statements
// target the exact same object identity, just with a different verb/value
// tail. Returns "" for a kind neither statement supports.
func opaqueOnClause(kind, schema, name, args, using string) string {
	switch kind {
	case "tablespace":
		return "TABLESPACE " + quoteIdent(name)
	case "fdw":
		return "FOREIGN DATA WRAPPER " + quoteIdent(name)
	case "server":
		return "SERVER " + quoteIdent(name)
	case "publication":
		return "PUBLICATION " + quoteIdent(name)
	case "event_trigger":
		return "EVENT TRIGGER " + quoteIdent(name)
	case "collation":
		return "COLLATION " + qualIdent(schema, name)
	case "operator":
		return "OPERATOR " + qualOperatorIdent(schema, name) + "(" + args + ")"
	case "operator_class":
		return "OPERATOR CLASS " + qualIdent(schema, name) + " USING " + accessMethodOrDefault(using)
	case "operator_family":
		return "OPERATOR FAMILY " + qualIdent(schema, name) + " USING " + accessMethodOrDefault(using)
	case "cast":
		parts := strings.SplitN(name, "->", 2)
		if len(parts) != 2 {
			return ""
		}
		return fmt.Sprintf("CAST (%s AS %s)", parts[0], parts[1])
	case "statistics":
		return "STATISTICS " + qualIdent(schema, name)
	case "ts_config":
		return "TEXT SEARCH CONFIGURATION " + qualIdent(schema, name)
	case "ts_dict":
		return "TEXT SEARCH DICTIONARY " + qualIdent(schema, name)
	case "ts_parser":
		return "TEXT SEARCH PARSER " + qualIdent(schema, name)
	case "ts_template":
		return "TEXT SEARCH TEMPLATE " + qualIdent(schema, name)
	default:
		return ""
	}
}

func commentOnOpaqueSQL(kind, schema, name, args, using string, comment *string) string {
	ident := opaqueOnClause(kind, schema, name, args, using)
	if ident == "" {
		return ""
	}
	val := "NULL"
	if comment != nil {
		val = quoteLit(*comment)
	}
	return fmt.Sprintf("COMMENT ON %s IS %s;", ident, val)
}

// appendCommentOp appends a COMMENT ON op to ops (the result of a preceding
// createOpaque call) when comment is set, passing err through unchanged —
// lets each createObject call site stay a single return statement:
// return appendCommentOp(createOpaque(...), kind, schema, name, args, using, comment, pos)
// wouldn't type-check (createOpaque's 2 return values can't feed more
// params in the same call), so callers destructure first; this only
// centralizes the "skip on error or nil comment" check itself.
func appendCommentOp(ops []pipeline.DiffOp, err error, kind, schema, name, args, using string, comment *string, pos pipeline.SourcePos) ([]pipeline.DiffOp, error) {
	if err != nil || comment == nil {
		return ops, err
	}
	if sql := commentOnOpaqueSQL(kind, schema, name, args, using, comment); sql != "" {
		ops = append(ops, safeOp(sql, pos))
	}
	return ops, nil
}

// appendSecurityLabelOps is appendCommentOp's SecurityLabels counterpart —
// one createSecurityLabelOps op per declared provider, appended at
// creation time (RFC Section 14.11). Skipped entirely for an opaque kind
// opaqueOnClause doesn't recognize.
func appendSecurityLabelOps(ops []pipeline.DiffOp, err error, kind, schema, name, args, using string, labels []pipeline.SecurityLabel, pos pipeline.SourcePos) ([]pipeline.DiffOp, error) {
	if err != nil || len(labels) == 0 {
		return ops, err
	}
	if ident := opaqueOnClause(kind, schema, name, args, using); ident != "" {
		ops = append(ops, createSecurityLabelOps(labels, ident, pos)...)
	}
	return ops, nil
}

// userMappingCreateOp is the DiffOp for a CREATE USER MAPPING statement
// whose OPTIONS clause may embed a secret reference (Secret resolution,
// Phase 5) — e.g. OPTIONS (user 'app', password '{{vault:secret/fdw/db#pw}}').
// Unlike SUBSCRIPTION CONNECTION (Section 13.2/items 6bb-6dd) there's no single known
// clause to isolate: FDW OPTIONS keys are provider-specific, not fixed by
// DPG, so ExecSQL runs pipeline.ResolveTemplate over the ENTIRE deparsed
// body — a no-op everywhere there's no {{...}}, and it substitutes each
// placeholder in place regardless of which OPTIONS key it's under, with no
// regex/positional parsing needed at all. Same redaction contract as
// subscriptionCreateOp/roleCreateOp: SQL() never changes meaning, only
// ExecSQL (called only inside the executor's exec loop) resolves.
type userMappingCreateOp struct {
	*op
	body string
}

func (o *userMappingCreateOp) ExecSQL(resolver pipeline.SecretResolver) (string, error) {
	resolved, err := pipeline.ResolveTemplate(o.body, resolver)
	if err != nil {
		return "", err
	}
	return resolved + ";", nil
}

var _ pipeline.SecretBearingOp = (*userMappingCreateOp)(nil)

func createUserMapping(o *ir.UserMapping) ([]pipeline.DiffOp, error) {
	if o.Body == "" {
		return nil, fmt.Errorf("USER MAPPING %s: body not captured; define it explicitly in a .dpg source file", o.QualifiedName())
	}
	return []pipeline.DiffOp{&userMappingCreateOp{op: safeOp(o.Body+";", o.SrcPos), body: o.Body}}, nil
}

// diffUserMapping mirrors diffOpaqueIR's shape (offline body-hash compare,
// structured DROP+CREATE on a real change) but can't reuse it directly: its
// CREATE side must produce a userMappingCreateOp, not a generic createOpaque
// op, so any {{...}} reference in OPTIONS is resolved only immediately
// before execution, never during diff.
// diffUserMapping implements RFC Section 14.10's structured diffing: "any change
// to the mapping is a full DROP USER MAPPING + CREATE USER MAPPING, not a
// targeted ALTER USER MAPPING" is the RFC's own explicit, deliberately-
// corrected semantics, so this is a single "did the non-sensitive OPTIONS
// change" comparison, in place of the previous offline-only body-hash
// compare (o.Reconstructed==true — always false for a desired/source-side
// object — meant the *snap* side going Reconstructed on any live path
// forced snap.BodyHash to "", so this never fired against live catalog
// drift). Password-like OPTIONS keys are excluded from the comparison
// entirely (toComparableOptions' excludeSensitive=true): the live side
// never carries the real value, only a fixed redaction placeholder (see
// ir.UserMapping.Options' doc comment), so comparing it would show
// permanent, spurious drift on every plan.
func diffUserMapping(o *ir.UserMapping, snap *snapshot.SnapOpaque) ([]pipeline.DiffOp, error) {
	if o.Body == "" {
		return nil, nil
	}
	// Offline path: snap.BodyHash is only ever populated when the snap
	// side is NOT Reconstructed (see sourceBodyHash) — i.e. both sides
	// came from source, so a real, comparable password/secret-reference
	// value exists on both. Preserve the original full-body hash compare
	// here unchanged: it's the only way to detect a declared {{secret-uri}}
	// reference change (e.g. rotating to a different vault path) at all,
	// and password-like keys must stay fully in scope for it.
	if snap.BodyHash != "" {
		newHash := hashText(o.Body)
		if newHash == snap.BodyHash {
			return nil, nil
		}
		ops := dropObject(&snapshot.SnapObject{Kind: snap.Kind, Opaque: snap})
		createOps, err := createUserMapping(o)
		if err != nil {
			return nil, err
		}
		return append(ops, createOps...), nil
	}
	// Live path (snap.BodyHash == "", i.e. Reconstructed): the live side
	// can never expose a real, comparable password value (see
	// ir.UserMapping.Options' doc comment) — RFC Section 14.10's structured
	// diffing input, per-field OPTIONS comparison excluding password-like
	// keys entirely, closes G-live for the non-sensitive subset. Detecting
	// a live-only password/secret-reference change remains genuinely
	// inherent (same PostgreSQL-imposed limit RFC Section 24 already documents
	// for Subscription's subconninfo), not a gap this can close.
	if !snap.OptionsStructured {
		// Stale snapshot predating this structured field — same
		// self-healing pattern as diffFDW/diffForeignServer. UserMapping
		// has no Comment field (real PostgreSQL has no COMMENT ON USER
		// MAPPING), so there's nothing to fall back to but the refresh op.
		return []pipeline.DiffOp{safeOp(fmt.Sprintf("-- refresh snapshot metadata for user mapping %s", o.QualifiedName()), o.SrcPos)}, nil
	}
	if optionsEqual(toComparableOptions(o.Options, true), snap.UserMappingOptions) {
		return nil, nil
	}
	ops := dropObject(&snapshot.SnapObject{Kind: snap.Kind, Opaque: snap})
	createOps, err := createUserMapping(o)
	if err != nil {
		return nil, err
	}
	return append(ops, createOps...), nil
}

// subscriptionConnectionLit matches CREATE SUBSCRIPTION's CONNECTION clause
// literal in pg_query's deparsed output — verified empirically:
// "CONNECTION '<connstr>'", connstr single-quote-escaped by doubling,
// exactly once per statement, since CONNECTION is a required, unique clause
// in this grammar (never appears elsewhere in a CREATE SUBSCRIPTION).
var subscriptionConnectionLit = regexp.MustCompile(`(?s)\bCONNECTION\s+'(?:[^']|'')*'`)

// substituteConnection replaces body's CONNECTION '...' clause with a
// freshly quoted resolved value. Returns an error if the clause can't be
// found — would mean pg_query's deparse output changed shape; safer to fail
// loudly than silently execute a statement with the wrong, unresolved
// CONNECTION value.
func substituteConnection(body, resolved string) (string, error) {
	loc := subscriptionConnectionLit.FindStringIndex(body)
	if loc == nil {
		return "", fmt.Errorf("could not locate CONNECTION clause in deparsed SUBSCRIPTION SQL")
	}
	return body[:loc[0]] + "CONNECTION " + quoteLit(resolved) + body[loc[1]:], nil
}

// subscriptionCreateOp is the DiffOp for a CREATE SUBSCRIPTION statement
// whose CONNECTION clause may hold an unresolved secret reference (RFC
// Section 13.2). SQL() always returns the placeholder/reference form (embedded
// op.sql, unchanged) — used for plan output, migration-file archival, and
// error messages, so a resolved secret is never persisted or logged.
// ExecSQL resolves the native CONNECTION literal via pipeline.ResolveTemplate
// (a no-op if it contains no {{...}} at all) and substitutes it into a fresh
// copy of the SQL, used only for the immediate execution call — never for
// anything that gets displayed, hashed, or wrapped into an error.
type subscriptionCreateOp struct {
	*op
	connInfo string
}

func (o *subscriptionCreateOp) ExecSQL(resolver pipeline.SecretResolver) (string, error) {
	resolved, err := pipeline.ResolveTemplate(o.connInfo, resolver)
	if err != nil {
		return "", err
	}
	return substituteConnection(o.sql, resolved)
}

var _ pipeline.SecretBearingOp = (*subscriptionCreateOp)(nil)

// createSubscription emits a CREATE SUBSCRIPTION statement as a
// subscriptionCreateOp rather than the generic createOpaque path — every
// other opaque kind has nothing secret-bearing in its Body, but a
// Subscription's CONNECTION clause may embed a secret reference that must
// only ever be resolved immediately before execution (see
// subscriptionCreateOp's doc comment). Also emits COMMENT ON SUBSCRIPTION
// when set — Comment isn't part of Body (it lives in the { } block), so it
// needs its own statement, same shape as every other Comment-bearing kind.
//
// Non-transactional (nonTxnOp), the same as CREATE INDEX CONCURRENTLY: PostgreSQL's CREATE SUBSCRIPTION defaults to
// WITH (create_slot = true), which errors "cannot run inside a transaction
// block" — confirmed live (this was a real, pre-existing bug predating
// secret-reference support entirely; createOpaque's generic path used
// safeOp, transactional, for every opaque kind including this one, and
// nothing had ever live-tested a fresh CREATE SUBSCRIPTION before). Always
// non-transactional regardless of whether a given declaration happens to
// set create_slot = false — Subscription's WITH-options aren't structurally
// parsed (still fully opaque beyond CONNECTION), and running outside a
// transaction is always safe even when not strictly required, unlike the
// reverse.
func createSubscription(o *ir.Subscription) ([]pipeline.DiffOp, error) {
	if o.Body == "" {
		return nil, fmt.Errorf("SUBSCRIPTION %s: body not captured; define it explicitly in a .dpg source file", o.Name)
	}
	ops := []pipeline.DiffOp{&subscriptionCreateOp{
		op:       nonTxnOp(pipeline.Safe, o.Body+";", o.SrcPos),
		connInfo: o.ConnInfo,
	}}
	if o.Owner != nil {
		ops = wrapCreateWithOwner(ops[0], o.Owner, o.SrcPos)
	}
	if o.Comment != nil {
		// Non-transactional, not safeOp: emit buckets ops purely by Mode()
		// into two separate lists, and the executor runs
		// the WHOLE transactional block before any non-transactional op —
		// confirmed live. A transactional COMMENT here would run BEFORE the
		// non-transactional CREATE above, erroring "subscription does not
		// exist" (the subscription doesn't exist yet). Non-transactional
		// keeps it in CREATE's own bucket, in the correct relative order.
		// diffSubscription's standalone comment-only-change path has no such
		// ordering hazard (the subscription already exists there) and stays
		// transactional.
		ops = append(ops, nonTxnOp(pipeline.Safe,
			fmt.Sprintf("COMMENT ON SUBSCRIPTION %s IS %s;", quoteIdent(o.Name), quoteLit(*o.Comment)),
			o.SrcPos,
		))
	}
	// Non-transactional for the same ordering reason as COMMENT above: CREATE
	// SUBSCRIPTION is itself non-transactional, so a transactional
	// SECURITY LABEL here would run before it exists.
	for _, l := range o.SecurityLabels {
		label := l.Label
		ops = append(ops, nonTxnOp(pipeline.Safe, securityLabelSQL(l.Provider, "SUBSCRIPTION "+quoteIdent(o.Name), &label), o.SrcPos))
	}
	return ops, nil
}

// diffTablespace implements RFC Section 14.7's structured diffing (Location is the
// only property that decides DROP+CREATE — LOCATION cannot be changed after
// creation in real PostgreSQL) in place of diffOpaqueIR's generic
// Reconstructed-gated body-hash compare, which went silently unset (via
// Reconstructed) on every live path — verify/plan --live never detected a
// live-catalog-only LOCATION change. Comment is still diffed independently,
// same as every other opaque-tier kind.
func diffTablespace(o *ir.Tablespace, snap *snapshot.SnapOpaque) ([]pipeline.DiffOp, error) {
	pos := o.SrcPos
	ident := quoteIdent(o.Name)
	onClause := "TABLESPACE " + ident
	// RENAME TO is bare, like Role/Publication/ForeignServer's identical
	// mechanism (RFC audit item #80) — tablespaces are cluster-level, not
	// schema-scoped. Applies regardless of which branch below handles the
	// rest of the diff, same as diffForeignServer's renameOps.
	var renameOps []pipeline.DiffOp
	if snap.Name != o.Name {
		renameOps = append(renameOps, cautionOp(
			fmt.Sprintf("ALTER TABLESPACE %s RENAME TO %s;", quoteIdent(snap.Name), ident),
			pos,
		))
	}
	// Stale snapshot predating this structured field: Go's zero value ""
	// for TablespaceLocation, even though a real tablespace always has a
	// non-empty LOCATION (required by CREATE TABLESPACE's grammar) — same
	// self-healing guard pattern as diffType's DOMAIN branch
	// (snap.DomainBaseType == ""). Skip structural comparison and fall back
	// to Comment-only, emitting a harmless refresh op if nothing else
	// changed so the snapshot self-heals on the very next apply instead of
	// staying stale forever.
	if snap.TablespaceLocation == "" {
		ops := renameOps
		if !ptrEq(o.Owner, snap.TablespaceOwner) && o.Owner != nil {
			ops = append(ops, safeOp(fmt.Sprintf("ALTER TABLESPACE %s OWNER TO %s;", ident, quoteIdent(*o.Owner)), pos))
		}
		if !ptrEq(o.Comment, snap.Comment) {
			if sql := commentOnOpaqueSQL("tablespace", "", o.Name, "", "", o.Comment); sql != "" {
				ops = append(ops, safeOp(sql, pos))
			}
		}
		ops = append(ops, diffGrantSet(snap.Grants, o.Grants, onClause, pos)...)
		ops = append(ops, diffRevocationSet(snap.Revocations, o.Revocations, onClause, pos)...)
		ops = append(ops, diffSecurityLabelSet(snap.SecurityLabels, o.SecurityLabels, onClause, pos)...)
		ops = append(ops, diffTablespaceStorageParams(ident, o.StorageParams, snap.TablespaceStorageParams, pos)...)
		if len(ops) == 0 {
			ops = append(ops, safeOp(fmt.Sprintf("-- refresh snapshot metadata for tablespace %s", quoteIdent(o.Name)), pos))
		}
		return ops, nil
	}
	if o.Location != snap.TablespaceLocation {
		ops := dropObject(&snapshot.SnapObject{Kind: snap.Kind, Opaque: snap})
		createOps, err := createOpaque(o.Name, o.Body, "TABLESPACE", "", pos)
		if err != nil {
			return nil, err
		}
		ops = append(ops, createOps...)
		ops = append(ops, createSimpleGrantOps("TABLESPACE", quoteIdent(o.Name), o.Grants, o.Revocations, pos, true)...)
		ops, err = appendCommentOp(ops, nil, "tablespace", "", o.Name, "", "", o.Comment, pos)
		if err != nil {
			return nil, err
		}
		ops = append(ops, createSecurityLabelOps(o.SecurityLabels, onClause, pos)...)
		return ops, nil
	}
	ops := renameOps
	if !ptrEq(o.Owner, snap.TablespaceOwner) && o.Owner != nil {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER TABLESPACE %s OWNER TO %s;", ident, quoteIdent(*o.Owner)), pos))
	}
	if !ptrEq(o.Comment, snap.Comment) {
		if sql := commentOnOpaqueSQL("tablespace", "", o.Name, "", "", o.Comment); sql != "" {
			ops = append(ops, safeOp(sql, pos))
		}
	}
	ops = append(ops, diffGrantSet(snap.Grants, o.Grants, onClause, pos)...)
	ops = append(ops, diffRevocationSet(snap.Revocations, o.Revocations, onClause, pos)...)
	ops = append(ops, diffSecurityLabelSet(snap.SecurityLabels, o.SecurityLabels, onClause, pos)...)
	ops = append(ops, diffTablespaceStorageParams(ident, o.StorageParams, snap.TablespaceStorageParams, pos)...)
	return ops, nil
}

// diffCast implements RFC Section 14.5's structured diffing: PostgreSQL provides no
// ALTER CAST at all, so any change to Method, Function, or Context decides
// DROP+CREATE, in place of diffOpaqueIR's generic Reconstructed-gated
// body-hash compare (silently unset on every live path — see
// diffTablespace's doc comment for the same root cause). Function is
// compared via qualifyFuncForCompare (same helper diffTriggers already
// uses): introspection always returns a schema-qualified name, while
// hand-written source commonly leaves it unqualified relying on the default
// "public" schema.
func diffCast(o *ir.Cast, snap *snapshot.SnapOpaque) ([]pipeline.DiffOp, error) {
	pos := o.SrcPos
	name := o.QualifiedName()
	// Stale snapshot predating these structured fields: CastMethod is
	// always one of "f"/"i"/"b" for a real cast, never the Go zero value —
	// same self-healing guard pattern as diffTablespace.
	if snap.CastMethod == "" {
		var ops []pipeline.DiffOp
		if !ptrEq(o.Comment, snap.Comment) {
			if sql := commentOnOpaqueSQL("cast", "", name, "", "", o.Comment); sql != "" {
				ops = append(ops, safeOp(sql, pos))
			}
		}
		if len(ops) == 0 {
			ops = append(ops, safeOp(fmt.Sprintf("-- refresh snapshot metadata for cast %s", name), pos))
		}
		return ops, nil
	}
	if o.Method != snap.CastMethod || o.Context != snap.CastContext ||
		qualifyFuncForCompare(o.Function) != qualifyFuncForCompare(snap.CastFunction) {
		ops := dropObject(&snapshot.SnapObject{Kind: snap.Kind, Opaque: snap})
		createOps, err := createOpaque(name, o.Body, "CAST", "", pos)
		if err != nil {
			return nil, err
		}
		ops = append(ops, createOps...)
		return appendCommentOp(ops, nil, "cast", "", name, "", "", o.Comment, pos)
	}
	if !ptrEq(o.Comment, snap.Comment) {
		if sql := commentOnOpaqueSQL("cast", "", name, "", "", o.Comment); sql != "" {
			return []pipeline.DiffOp{safeOp(sql, pos)}, nil
		}
	}
	return nil, nil
}

// operatorHintRef renders a Commutator/Negator dotted schema.symbol back
// into ALTER/CREATE OPERATOR's own reference syntax: OPERATOR(schema.symbol)
// when schema-qualified (confirmed live that this wrapper is required in
// both CREATE and ALTER OPERATOR SET contexts — an operator symbol is not a
// valid identifier, so a bare "schema.symbol" would parse wrong), or the
// bare symbol when unqualified.
func operatorHintRef(ref string) string {
	if idx := strings.LastIndex(ref, "."); idx >= 0 {
		return fmt.Sprintf("OPERATOR(%s.%s)", quoteIdent(ref[:idx]), ref[idx+1:])
	}
	return ref
}

// diffOperator implements RFC Section 14.3's optimizer-hint diffing: a
// change to RESTRICT/JOIN/COMMUTATOR/NEGATOR/HASHES/MERGES previously always
// forced a DROP+CREATE via the generic opaque body-hash compare (Operator
// was the one opaque kind without dedicated structured fields — every
// sibling, FDW/Foreign Server/Collation/Cast/Statistics Object/Event
// Trigger, already got them for exactly this reason).
//
// Real PostgreSQL's ALTER OPERATOR ... SET is asymmetric (confirmed live
// against a PostgreSQL 17 server): RESTRICT/JOIN are freely re-settable and
// clearable in place (SET (RESTRICT = NONE)); COMMUTATOR/NEGATOR/HASHES/
// MERGES can only move from unset to set — PostgreSQL flatly rejects
// changing (or clearing) an already-set value on any of those four
// ("operator attribute ... cannot be changed if it has already been set"),
// so that specific transition still routes through DROP+CREATE below.
func diffOperator(o *ir.Operator, snap *snapshot.SnapOpaque) ([]pipeline.DiffOp, error) {
	pos := o.SrcPos
	name := o.QualifiedName()

	commentOps := func() []pipeline.DiffOp {
		var ops []pipeline.DiffOp
		if !ptrEq(o.Comment, snap.Comment) {
			if sql := commentOnOpaqueSQL("operator", o.Schema, o.Name, snap.Args, "", o.Comment); sql != "" {
				ops = append(ops, safeOp(sql, pos))
			}
		}
		return ops
	}

	// Stale snapshot predating these structured fields — same self-healing
	// guard pattern as diffCast/diffCollation's identical checks.
	if !snap.OperatorStructured {
		ops := commentOps()
		if len(ops) == 0 {
			ops = append(ops, safeOp(fmt.Sprintf("-- refresh snapshot metadata for operator %s", name), pos))
		}
		return ops, nil
	}

	// Core-definition change (Function, operand types, or anything else in
	// Body besides the 6 hint properties) still needs DROP+CREATE — detected
	// via OperatorCoreHash (Body with the hint properties' text stripped
	// first, see OperatorCoreBodyHashInput), not the plain whole-Body hash,
	// so a hint-only change doesn't also trip this branch. Same technique,
	// same reasoning as diffType's BASE-type BaseImmutableHash check.
	coreHash := ""
	if o.Body != "" && !o.Reconstructed {
		sum := sha256.Sum256([]byte(strings.TrimSpace(snapshot.OperatorCoreBodyHashInput(o.Body))))
		coreHash = fmt.Sprintf("%x", sum)
	}
	coreChanged := coreHash != "" && snap.OperatorCoreHash != "" && coreHash != snap.OperatorCoreHash

	// Commutator/Negator/Hashes/Merges are one-directional (see doc comment
	// above): only unset->set can go through ALTER OPERATOR SET, so a
	// change to an already-set value on any of these four also forces
	// DROP+CREATE even though it wouldn't otherwise trip the core-hash
	// check (they're part of the hint-stripped text, not the core).
	commutatorBreaking := snap.OperatorCommutator != nil && !ptrEq(o.Commutator, snap.OperatorCommutator)
	negatorBreaking := snap.OperatorNegator != nil && !ptrEq(o.Negator, snap.OperatorNegator)
	hashesBreaking := snap.OperatorHashes && !o.Hashes
	mergesBreaking := snap.OperatorMerges && !o.Merges

	if coreChanged || commutatorBreaking || negatorBreaking || hashesBreaking || mergesBreaking {
		ops := dropObject(&snapshot.SnapObject{Kind: snap.Kind, Opaque: snap})
		createOps, err := createOpaque(name, o.Body, "OPERATOR", o.Schema, pos)
		if err != nil {
			return nil, err
		}
		ops = append(ops, createOps...)
		return appendCommentOp(ops, nil, "operator", o.Schema, o.Name, snap.Args, "", o.Comment, pos)
	}

	var setClauses []string
	if !ptrEq(o.Restrict, snap.OperatorRestrict) {
		if o.Restrict != nil {
			setClauses = append(setClauses, "RESTRICT = "+*o.Restrict)
		} else {
			setClauses = append(setClauses, "RESTRICT = NONE")
		}
	}
	if !ptrEq(o.Join, snap.OperatorJoin) {
		if o.Join != nil {
			setClauses = append(setClauses, "JOIN = "+*o.Join)
		} else {
			setClauses = append(setClauses, "JOIN = NONE")
		}
	}
	if o.Commutator != nil && snap.OperatorCommutator == nil {
		setClauses = append(setClauses, "COMMUTATOR = "+operatorHintRef(*o.Commutator))
	}
	if o.Negator != nil && snap.OperatorNegator == nil {
		setClauses = append(setClauses, "NEGATOR = "+operatorHintRef(*o.Negator))
	}
	if o.Hashes && !snap.OperatorHashes {
		setClauses = append(setClauses, "HASHES")
	}
	if o.Merges && !snap.OperatorMerges {
		setClauses = append(setClauses, "MERGES")
	}

	var ops []pipeline.DiffOp
	if len(setClauses) > 0 {
		ops = append(ops, safeOp(
			fmt.Sprintf("ALTER OPERATOR %s (%s) SET (%s);", qualOperatorIdent(o.Schema, o.Name), snap.Args, strings.Join(setClauses, ", ")),
			pos,
		))
	}
	ops = append(ops, commentOps()...)
	return ops, nil
}

// diffEventTrigger implements RFC Section 14.1's structured diffing: PostgreSQL has
// no ALTER EVENT TRIGGER for Event/Tags/Function (only ENABLE/DISABLE/
// OWNER TO/RENAME TO, none modeled here), so any change to those three
// decides DROP+CREATE — RFC Section 14.1 classifies this SAFE ("no data
// involved"), unlike every other opaque-tier DROP+CREATE, which is why
// dropObject's "event_trigger" case already uses safeOp instead of
// destructiveOp. Replaces diffOpaqueIR's generic Reconstructed-gated
// body-hash compare (silently unset on every live path — see
// diffTablespace's doc comment for the same root cause). Tags are compared
// as a set (stringSetEqual): PostgreSQL stores evttags as an unordered
// array, and WHEN TAG IN (...)'s list order carries no meaning.
func diffEventTrigger(o *ir.EventTrigger, snap *snapshot.SnapOpaque) ([]pipeline.DiffOp, error) {
	pos := o.SrcPos
	onClause := "EVENT TRIGGER " + quoteIdent(o.Name)

	// RENAME TO (Section 14.1) applies regardless of whether the structured
	// Event/Tags/Function comparison below is trustworthy — Name is a basic
	// identity field, not one of those three. Uses snap's matched (old)
	// name for the FROM side, same reasoning as diffType/diffCollation's
	// identical rename handling. Not merged into the drop+create branch
	// below — a simultaneous rename needs no separate ALTER there, since
	// createOpaque already creates the trigger under its final (new) name.
	var renameOps []pipeline.DiffOp
	if snap.Name != o.Name {
		renameOps = append(renameOps, cautionOp(
			fmt.Sprintf("ALTER EVENT TRIGGER %s RENAME TO %s;", quoteIdent(snap.Name), quoteIdent(o.Name)),
			pos,
		))
	}

	// Stale snapshot predating these structured fields: EventTriggerEvent
	// is always non-empty for a real event trigger (required by CREATE
	// EVENT TRIGGER's grammar), never the Go zero value — same
	// self-healing guard pattern as diffTablespace/diffCast.
	if snap.EventTriggerEvent == "" {
		ops := renameOps
		if !ptrEq(o.Comment, snap.Comment) {
			if sql := commentOnOpaqueSQL("event_trigger", "", o.Name, "", "", o.Comment); sql != "" {
				ops = append(ops, safeOp(sql, pos))
			}
		}
		if !ptrEq(o.Owner, snap.EventTriggerOwner) && o.Owner != nil {
			ops = append(ops, safeOp(fmt.Sprintf("ALTER EVENT TRIGGER %s OWNER TO %s;", quoteIdent(o.Name), quoteIdent(*o.Owner)), pos))
		}
		if o.EnableState != snap.EventTriggerEnableState {
			ops = append(ops, safeOp(eventTriggerEnableStateSQL(o.Name, o.EnableState), pos))
		}
		ops = append(ops, diffSecurityLabelSet(snap.SecurityLabels, o.SecurityLabels, onClause, pos)...)
		if len(ops) == 0 {
			ops = append(ops, safeOp(fmt.Sprintf("-- refresh snapshot metadata for event trigger %s", quoteIdent(o.Name)), pos))
		}
		return ops, nil
	}
	if o.Event != snap.EventTriggerEvent ||
		qualifyFuncForCompare(o.Function) != qualifyFuncForCompare(snap.EventTriggerFunction) ||
		!stringSetEqual(o.Tags, snap.EventTriggerTags) {
		ops := dropObject(&snapshot.SnapObject{Kind: snap.Kind, Opaque: snap})
		createOps, err := createOpaque(o.Name, o.Body, "EVENT TRIGGER", "", pos)
		if err != nil {
			return nil, err
		}
		if o.Owner != nil && len(createOps) > 0 {
			createOps = append(wrapCreateWithOwner(createOps[0], o.Owner, pos), createOps[1:]...)
		}
		ops = append(ops, createOps...)
		ops, err = appendCommentOp(ops, nil, "event_trigger", "", o.Name, "", "", o.Comment, pos)
		if err != nil {
			return nil, err
		}
		if o.EnableState != "" {
			ops = append(ops, safeOp(eventTriggerEnableStateSQL(o.Name, o.EnableState), pos))
		}
		ops = append(ops, createSecurityLabelOps(o.SecurityLabels, onClause, pos)...)
		return ops, nil
	}
	ops := renameOps
	if !ptrEq(o.Comment, snap.Comment) {
		if sql := commentOnOpaqueSQL("event_trigger", "", o.Name, "", "", o.Comment); sql != "" {
			ops = append(ops, safeOp(sql, pos))
		}
	}
	if !ptrEq(o.Owner, snap.EventTriggerOwner) && o.Owner != nil {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER EVENT TRIGGER %s OWNER TO %s;", quoteIdent(o.Name), quoteIdent(*o.Owner)), pos))
	}
	if o.EnableState != snap.EventTriggerEnableState {
		ops = append(ops, safeOp(eventTriggerEnableStateSQL(o.Name, o.EnableState), pos))
	}
	ops = append(ops, diffSecurityLabelSet(snap.SecurityLabels, o.SecurityLabels, onClause, pos)...)
	return ops, nil
}

// userMappingPasswordKeys mirrors internal/introspect's and
// internal/snapshot's own copies of the same list (which itself mirrors
// internal/linter's passwordColNames) — kept as a local duplicate rather
// than a cross-package import, following this codebase's established
// pattern for this exact recurring 5-string need.
var userMappingPasswordKeys = []string{"password", "passwd", "pwd", "secret", "passphrase"}

func isUserMappingPasswordKey(key string) bool {
	lower := strings.ToLower(key)
	for _, kw := range userMappingPasswordKeys {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// toComparableOptions converts a desired-side Options list to the snapshot
// comparison shape, optionally excluding password-like keys — used for
// UserMapping (excludeSensitive=true, since the live side never carries a
// real password value to compare against, only a fixed redaction
// placeholder) and reused as a plain passthrough (excludeSensitive=false)
// for FDW/ForeignServer, which have no sensitive-key concept.
func toComparableOptions(opts []pipeline.StorageParam, excludeSensitive bool) []snapshot.SnapOptionKV {
	var out []snapshot.SnapOptionKV
	for _, o := range opts {
		if excludeSensitive && isUserMappingPasswordKey(o.Key) {
			continue
		}
		out = append(out, snapshot.SnapOptionKV{Key: o.Key, Value: o.Value})
	}
	return out
}

// optionsEqual compares two OPTIONS lists as sets (order-independent, since
// PostgreSQL's own OPTIONS have no meaningful order).
func optionsEqual(a, b []snapshot.SnapOptionKV) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]string, len(a))
	for _, kv := range a {
		m[kv.Key] = kv.Value
	}
	for _, kv := range b {
		v, ok := m[kv.Key]
		if !ok || v != kv.Value {
			return false
		}
	}
	return true
}

// alterOptionsSQL builds an ALTER ... OPTIONS (ADD/SET/DROP ...) clause's
// entry list comparing desired against live — confirmed live against a
// real PostgreSQL 17 server: OPTIONS keys need no identifier quoting (even
// reserved words like "user" parse fine unquoted; formatOptions elsewhere
// in this codebase already relies on the same). Returns "" when nothing
// changed.
func alterOptionsSQL(desired, live []snapshot.SnapOptionKV) string {
	liveByKey := make(map[string]string, len(live))
	for _, kv := range live {
		liveByKey[kv.Key] = kv.Value
	}
	desiredByKey := make(map[string]string, len(desired))
	for _, kv := range desired {
		desiredByKey[kv.Key] = kv.Value
	}
	var parts []string
	for _, kv := range desired {
		if lv, ok := liveByKey[kv.Key]; !ok {
			parts = append(parts, fmt.Sprintf("ADD %s %s", kv.Key, quoteLit(kv.Value)))
		} else if lv != kv.Value {
			parts = append(parts, fmt.Sprintf("SET %s %s", kv.Key, quoteLit(kv.Value)))
		}
	}
	for _, kv := range live {
		if _, ok := desiredByKey[kv.Key]; !ok {
			parts = append(parts, fmt.Sprintf("DROP %s", kv.Key))
		}
	}
	return strings.Join(parts, ", ")
}

// diffCollation implements RFC Section 14.2's structured diffing: "any property
// change requires DROP COLLATION + CREATE COLLATION" (real PostgreSQL's
// ALTER COLLATION supports only REFRESH VERSION/OWNER TO/RENAME TO/SET
// SCHEMA, none of Provider/Collate/Ctype/ICULocale/Deterministic), so this
// is a single "did anything change" comparison, same shape as diffFDW.
// Comparing the resolved Collate/Ctype/ICULocale fields directly (rather
// than the LOCALE-vs-LC_COLLATE/LC_CTYPE shorthand text) is what actually
// closes the gap — see ir.Collation's doc comment. Replaces diffOpaqueIR's
// generic Reconstructed-gated body-hash compare (silently unset on every
// live path).
// diffStatisticsObject implements RFC Section 14.6's structured diffing table:
// real PostgreSQL's ALTER STATISTICS supports only OWNER TO/RENAME TO/
// SET SCHEMA/SET STATISTICS (confirmed live via `\h ALTER STATISTICS`
// against a real PostgreSQL 17 server), so a Table, Kinds, or Columns
// change decides DROP+CREATE (RFC's "Column list or kinds changed" row,
// DESTRUCTIVE), while a StatisticsTarget-only change gets a real, targeted
// ALTER STATISTICS ... SET STATISTICS (RFC: SAFE). Table is compared via
// qualifyTableForCompare (same helper Publication's Tables use): a
// statistics object's FROM table may be left unqualified in source,
// relying on the default "public" schema, while introspection always
// returns a fully schema-qualified name. Replaces diffOpaqueIR's generic
// Reconstructed-gated body-hash compare (silently unset on every live
// path).
func diffStatisticsObject(o *ir.StatisticsObject, snap *snapshot.SnapOpaque) ([]pipeline.DiffOp, error) {
	pos := o.SrcPos
	ident := qualIdent(o.Schema, o.Name)

	// SET SCHEMA / RENAME TO apply regardless of whether the structured
	// Table/Kinds/Columns comparison below is trustworthy — Name/Schema
	// are basic identity fields, not part of that set. Same ordering as
	// diffTable's identical mechanism: SET SCHEMA first (old_schema.old_name
	// -> new schema), then RENAME TO (new_schema.old_name -> new_name). Not
	// merged into the drop+create branch below — a simultaneous rename/move
	// needs no separate ALTER there, since createOpaque already creates the
	// object under its final (new) name/schema.
	var renameOps []pipeline.DiffOp
	if snap.Schema != o.Schema {
		renameOps = append(renameOps, safeOp(
			fmt.Sprintf("ALTER STATISTICS %s SET SCHEMA %s;", qualIdent(snap.Schema, snap.Name), quoteIdent(o.Schema)),
			pos,
		))
	}
	if snap.Name != o.Name {
		renameOps = append(renameOps, cautionOp(
			fmt.Sprintf("ALTER STATISTICS %s RENAME TO %s;", qualIdent(o.Schema, snap.Name), quoteIdent(o.Name)),
			pos,
		))
	}

	if !snap.StatisticsStructured {
		// Stale snapshot predating these structured fields — same
		// self-healing pattern as diffCollation/diffFDW. An empty
		// Kinds/Columns list is not itself a reliable "unpopulated"
		// signal (a freshly-populated object could legitimately have
		// neither yet, e.g. mid-edit), hence the explicit sentinel.
		ops := renameOps
		if !ptrEq(o.Comment, snap.Comment) {
			if sql := commentOnOpaqueSQL("statistics", o.Schema, o.Name, "", "", o.Comment); sql != "" {
				ops = append(ops, safeOp(sql, pos))
			}
		}
		if len(ops) == 0 {
			ops = append(ops, safeOp(fmt.Sprintf("-- refresh snapshot metadata for statistics %s", ident), pos))
		}
		return ops, nil
	}
	tableChanged := qualifyTableForCompare(o.Table) != qualifyTableForCompare(snap.StatisticsTable)
	kindsChanged := !stringSetEqual(o.Kinds, snap.StatisticsKinds)
	columnsChanged := !stringSetEqual(o.Columns, snap.StatisticsColumns)
	if tableChanged || kindsChanged || columnsChanged {
		ops := dropObject(&snapshot.SnapObject{Kind: snap.Kind, Opaque: snap})
		createOps, err := createOpaque(o.QualifiedName(), o.Body, "STATISTICS", o.Schema, pos)
		if err != nil {
			return nil, err
		}
		ops = append(ops, createOps...)
		if o.StatisticsTarget != nil {
			ops = append(ops, safeOp(fmt.Sprintf("ALTER STATISTICS %s SET STATISTICS %d;", ident, *o.StatisticsTarget), pos))
		}
		return appendCommentOp(ops, nil, "statistics", o.Schema, o.Name, "", "", o.Comment, pos)
	}
	ops := renameOps
	if !intPtrEq(o.StatisticsTarget, snap.StatisticsTarget) {
		if o.StatisticsTarget != nil {
			ops = append(ops, safeOp(fmt.Sprintf("ALTER STATISTICS %s SET STATISTICS %d;", ident, *o.StatisticsTarget), pos))
		} else {
			// -1 resets to the default on every supported version; the
			// DEFAULT keyword needs PostgreSQL 17.
			ops = append(ops, safeOp(fmt.Sprintf("ALTER STATISTICS %s SET STATISTICS -1;", ident), pos))
		}
	}
	if !ptrEq(o.Comment, snap.Comment) {
		if sql := commentOnOpaqueSQL("statistics", o.Schema, o.Name, "", "", o.Comment); sql != "" {
			ops = append(ops, safeOp(sql, pos))
		}
	}
	return ops, nil
}

// collationRefreshVersionOp builds the imperative, non-persisted REFRESH
// VERSION op — see ir.Collation.RefreshVersion's doc comment. Caution and
// transactional, same reasoning as sequenceRestartOp: real PostgreSQL's
// ALTER COLLATION ... REFRESH VERSION has no restriction against running
// inside a transaction.
func collationRefreshVersionOp(o *ir.Collation) pipeline.DiffOp {
	return cautionOp(
		fmt.Sprintf("ALTER COLLATION %s REFRESH VERSION;", qualIdent(o.Schema, o.Name)),
		o.SrcPos,
	)
}

func diffCollation(o *ir.Collation, snap *snapshot.SnapOpaque) ([]pipeline.DiffOp, error) {
	pos := o.SrcPos
	ident := qualIdent(o.Schema, o.Name)

	// SET SCHEMA / RENAME TO apply regardless of whether the structured
	// property comparison below is trustworthy (CollationStructured) —
	// Name/Schema are basic identity fields, not part of the LOCALE/
	// PROVIDER/etc. set that sentinel gates. Same ordering as diffType's
	// identical mechanism: SET SCHEMA first (old_schema.old_name -> new
	// schema), then RENAME TO (new_schema.old_name -> new_name). Not
	// merged into the property-changed branch below — a simultaneous
	// rename/move needs no separate ALTER there, since createOpaque already
	// creates the object under its final (new) name/schema directly.
	var renameOps []pipeline.DiffOp
	if snap.Schema != o.Schema {
		renameOps = append(renameOps, safeOp(
			fmt.Sprintf("ALTER COLLATION %s SET SCHEMA %s;", qualIdent(snap.Schema, snap.Name), quoteIdent(o.Schema)),
			pos,
		))
	}
	if snap.Name != o.Name {
		renameOps = append(renameOps, cautionOp(
			fmt.Sprintf("ALTER COLLATION %s RENAME TO %s;", qualIdent(o.Schema, snap.Name), quoteIdent(o.Name)),
			pos,
		))
	}

	// ownerCommentRefreshOps builds the owner/comment/REFRESH VERSION ops
	// shared by both branches below that skip the structured property
	// comparison (a FROM-declared collation, permanently, and a stale
	// pre-CollationStructured snapshot, transiently — see each call site).
	ownerCommentRefreshOps := func() []pipeline.DiffOp {
		ops := renameOps
		if !ptrEq(o.Owner, snap.CollationOwner) && o.Owner != nil {
			ops = append(ops, safeOp(fmt.Sprintf("ALTER COLLATION %s OWNER TO %s;", ident, quoteIdent(*o.Owner)), pos))
		}
		if !ptrEq(o.Comment, snap.Comment) {
			if sql := commentOnOpaqueSQL("collation", o.Schema, o.Name, "", "", o.Comment); sql != "" {
				ops = append(ops, safeOp(sql, pos))
			}
		}
		if o.RefreshVersion {
			ops = append(ops, collationRefreshVersionOp(o))
		}
		return ops
	}

	// A FROM-declared collation (RFC Section 14.2) skips the structured
	// property comparison below entirely, permanently — see
	// ir.Collation.CopyFrom's doc comment for why (this struct's Provider/
	// Collate/Ctype/ICULocale/Deterministic/Rules are just hardcoded Go
	// zero-value defaults for a FROM declaration, never DPG's real
	// understanding of what the collation resolves to; comparing them
	// against introspection's actual resolved values would be actively
	// wrong, not just unhelpful). Deliberately no "nothing else to say"
	// placeholder op here, unlike the stale-snapshot branch below: that
	// placeholder exists to self-heal a snapshot ONE TIME as it upgrades to
	// CollationStructured — CopyFrom's opt-out is permanent (every future
	// plan still declares FROM), so the same placeholder here would show up
	// as a harmless but permanently-visible no-op change in every single
	// `dpg plan` output, forever.
	if o.CopyFrom != nil {
		return ownerCommentRefreshOps(), nil
	}
	if !snap.CollationStructured {
		ops := ownerCommentRefreshOps()
		if len(ops) == 0 {
			ops = append(ops, safeOp(fmt.Sprintf("-- refresh snapshot metadata for collation %s", ident), pos))
		}
		return ops, nil
	}
	if o.Provider != snap.CollationProvider ||
		!ptrEq(o.Collate, snap.CollationCollate) || !ptrEq(o.Ctype, snap.CollationCtype) || !ptrEq(o.ICULocale, snap.CollationICULocale) ||
		o.Deterministic != snap.CollationDeterministic ||
		(o.Rules != nil && !ptrEq(o.Rules, snap.CollationRules)) {
		ops := dropObject(&snapshot.SnapObject{Kind: snap.Kind, Opaque: snap})
		createOps, err := createOpaque(o.QualifiedName(), o.Body, "COLLATION", o.Schema, pos)
		if err != nil {
			return nil, err
		}
		if o.Owner != nil && len(createOps) > 0 {
			createOps = append(wrapCreateWithOwner(createOps[0], o.Owner, pos), createOps[1:]...)
		}
		ops = append(ops, createOps...)
		ops, err = appendCommentOp(ops, nil, "collation", o.Schema, o.Name, "", "", o.Comment, pos)
		if err == nil && o.RefreshVersion {
			ops = append(ops, collationRefreshVersionOp(o))
		}
		return ops, err
	}
	ops := renameOps
	if !ptrEq(o.Owner, snap.CollationOwner) && o.Owner != nil {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER COLLATION %s OWNER TO %s;", ident, quoteIdent(*o.Owner)), pos))
	}
	if !ptrEq(o.Comment, snap.Comment) {
		if sql := commentOnOpaqueSQL("collation", o.Schema, o.Name, "", "", o.Comment); sql != "" {
			ops = append(ops, safeOp(sql, pos))
		}
	}
	if o.RefreshVersion {
		ops = append(ops, collationRefreshVersionOp(o))
	}
	return ops, nil
}

// diffFDW implements RFC Section 14.8's structured diffing: "any change to a FDW
// requires drop + recreate" is the RFC's own explicit semantics (no
// ALTER FOREIGN DATA WRAPPER path modeled, even though real PostgreSQL has
// one — a deliberate DPG simplification), so this is a single "did
// anything change" comparison across Handler/Validator/Options, in place
// of diffOpaqueIR's generic Reconstructed-gated body-hash compare (silently
// unset on every live path — see diffTablespace's doc comment for the same
// root cause).
func diffFDW(o *ir.ForeignDataWrapper, snap *snapshot.SnapOpaque) ([]pipeline.DiffOp, error) {
	pos := o.SrcPos
	onClause := "FOREIGN DATA WRAPPER " + quoteIdent(o.Name)

	// RENAME TO applies regardless of whether the structured Handler/
	// Validator/Options comparison below is trustworthy — Name is a basic
	// identity field, not part of that set. Uses snap's matched (old) name
	// for the FROM side, same reasoning as diffType/diffCollation's
	// identical rename handling. Not merged into the drop+create branch
	// below — a simultaneous rename needs no separate ALTER there, since
	// createOpaque already creates the object under its final (new) name.
	// Bare (no schema qualifier) — FDWs are database-level.
	var renameOps []pipeline.DiffOp
	if snap.Name != o.Name {
		renameOps = append(renameOps, cautionOp(
			fmt.Sprintf("ALTER FOREIGN DATA WRAPPER %s RENAME TO %s;", quoteIdent(snap.Name), quoteIdent(o.Name)),
			pos,
		))
	}

	// Stale snapshot predating these structured fields: unlike Tablespace/
	// Cast/EventTrigger, no single field is guaranteed non-empty on a real
	// FDW (a bare FOREIGN DATA WRAPPER with no HANDLER/VALIDATOR/OPTIONS
	// is valid), so OptionsStructured is an explicit sentinel instead.
	if !snap.OptionsStructured {
		ops := renameOps
		if !ptrEq(o.Owner, snap.FDWOwner) && o.Owner != nil {
			ops = append(ops, safeOp(fmt.Sprintf("ALTER FOREIGN DATA WRAPPER %s OWNER TO %s;", quoteIdent(o.Name), quoteIdent(*o.Owner)), pos))
		}
		if !ptrEq(o.Comment, snap.Comment) {
			if sql := commentOnOpaqueSQL("fdw", "", o.Name, "", "", o.Comment); sql != "" {
				ops = append(ops, safeOp(sql, pos))
			}
		}
		ops = append(ops, diffGrantSet(snap.Grants, o.Grants, onClause, pos)...)
		ops = append(ops, diffRevocationSet(snap.Revocations, o.Revocations, onClause, pos)...)
		if len(ops) == 0 {
			ops = append(ops, safeOp(fmt.Sprintf("-- refresh snapshot metadata for foreign data wrapper %s", quoteIdent(o.Name)), pos))
		}
		return ops, nil
	}
	if qualifyFuncForCompare(o.Handler) != qualifyFuncForCompare(snap.FDWHandler) ||
		qualifyFuncForCompare(o.Validator) != qualifyFuncForCompare(snap.FDWValidator) ||
		!optionsEqual(toComparableOptions(o.Options, false), snap.FDWOptions) {
		ops := dropObject(&snapshot.SnapObject{Kind: snap.Kind, Opaque: snap})
		createOps, err := createOpaque(o.Name, o.Body, "FOREIGN DATA WRAPPER", "", pos)
		if err != nil {
			return nil, err
		}
		if o.Owner != nil && len(createOps) > 0 {
			createOps = append(wrapCreateWithOwner(createOps[0], o.Owner, pos), createOps[1:]...)
		}
		ops = append(ops, createOps...)
		ops = append(ops, createSimpleGrantOps("FOREIGN DATA WRAPPER", quoteIdent(o.Name), o.Grants, o.Revocations, pos, false)...)
		return appendCommentOp(ops, nil, "fdw", "", o.Name, "", "", o.Comment, pos)
	}
	ops := renameOps
	if !ptrEq(o.Owner, snap.FDWOwner) && o.Owner != nil {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER FOREIGN DATA WRAPPER %s OWNER TO %s;", quoteIdent(o.Name), quoteIdent(*o.Owner)), pos))
	}
	if !ptrEq(o.Comment, snap.Comment) {
		if sql := commentOnOpaqueSQL("fdw", "", o.Name, "", "", o.Comment); sql != "" {
			ops = append(ops, safeOp(sql, pos))
		}
	}
	ops = append(ops, diffGrantSet(snap.Grants, o.Grants, onClause, pos)...)
	ops = append(ops, diffRevocationSet(snap.Revocations, o.Revocations, onClause, pos)...)
	return ops, nil
}

// diffForeignServer implements RFC Section 14.9's structured diffing table: a
// FDW-wrapper change decides DROP+CREATE (DESTRUCTIVE) — same for a TYPE
// change, since real PostgreSQL's ALTER SERVER has no TYPE clause
// (confirmed via `\h ALTER SERVER` against a live PostgreSQL 17 server:
// only VERSION and OPTIONS are alterable in place) — while a VERSION or
// OPTIONS change gets a real, targeted ALTER SERVER (SAFE, per the RFC's
// own "OPTIONS changed" row; VERSION follows the identical reasoning since
// PostgreSQL supports altering it the same way). Replaces diffOpaqueIR's
// generic Reconstructed-gated body-hash compare (silently unset on every
// live path).
func diffForeignServer(o *ir.ForeignServer, snap *snapshot.SnapOpaque) ([]pipeline.DiffOp, error) {
	pos := o.SrcPos
	ident := quoteIdent(o.Name)
	onClause := "FOREIGN SERVER " + ident
	// RENAME TO is bare, like Role/Publication's identical mechanism (RFC
	// audit item #79) — foreign servers are cluster-level, not
	// schema-scoped. Applies regardless of which branch below handles the
	// rest of the diff, same as diffPublication's renameOps.
	var renameOps []pipeline.DiffOp
	if snap.Name != o.Name {
		renameOps = append(renameOps, cautionOp(
			fmt.Sprintf("ALTER SERVER %s RENAME TO %s;", quoteIdent(snap.Name), ident),
			pos,
		))
	}
	if !snap.OptionsStructured {
		ops := renameOps
		if !ptrEq(o.Owner, snap.ServerOwner) && o.Owner != nil {
			ops = append(ops, safeOp(fmt.Sprintf("ALTER SERVER %s OWNER TO %s;", ident, quoteIdent(*o.Owner)), pos))
		}
		if !ptrEq(o.Comment, snap.Comment) {
			if sql := commentOnOpaqueSQL("server", "", o.Name, "", "", o.Comment); sql != "" {
				ops = append(ops, safeOp(sql, pos))
			}
		}
		ops = append(ops, diffGrantSet(snap.Grants, o.Grants, onClause, pos)...)
		ops = append(ops, diffRevocationSet(snap.Revocations, o.Revocations, onClause, pos)...)
		if len(ops) == 0 {
			ops = append(ops, safeOp(fmt.Sprintf("-- refresh snapshot metadata for server %s", ident), pos))
		}
		return ops, nil
	}
	if o.FDWName != snap.ServerFDWName || !ptrEq(o.Type, snap.ServerType) {
		ops := dropObject(&snapshot.SnapObject{Kind: snap.Kind, Opaque: snap})
		createOps, err := createOpaque(o.Name, o.Body, "SERVER", "", pos)
		if err != nil {
			return nil, err
		}
		if o.Owner != nil && len(createOps) > 0 {
			createOps = append(wrapCreateWithOwner(createOps[0], o.Owner, pos), createOps[1:]...)
		}
		ops = append(ops, createOps...)
		ops = append(ops, createSimpleGrantOps("FOREIGN SERVER", ident, o.Grants, o.Revocations, pos, false)...)
		return appendCommentOp(ops, nil, "server", "", o.Name, "", "", o.Comment, pos)
	}
	ops := renameOps
	if !ptrEq(o.Owner, snap.ServerOwner) && o.Owner != nil {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER SERVER %s OWNER TO %s;", ident, quoteIdent(*o.Owner)), pos))
	}
	desiredOptions := toComparableOptions(o.Options, false)
	versionChanged := !ptrEq(o.Version, snap.ServerVersion)
	optionsChanged := !optionsEqual(desiredOptions, snap.ServerOptions)
	if versionChanged || optionsChanged {
		var sb strings.Builder
		fmt.Fprintf(&sb, "ALTER SERVER %s", ident)
		if versionChanged && o.Version != nil {
			fmt.Fprintf(&sb, " VERSION %s", quoteLit(*o.Version))
		}
		if optionsChanged {
			fmt.Fprintf(&sb, " OPTIONS (%s)", alterOptionsSQL(desiredOptions, snap.ServerOptions))
		}
		sb.WriteString(";")
		ops = append(ops, safeOp(sb.String(), pos))
	}
	if !ptrEq(o.Comment, snap.Comment) {
		if sql := commentOnOpaqueSQL("server", "", o.Name, "", "", o.Comment); sql != "" {
			ops = append(ops, safeOp(sql, pos))
		}
	}
	ops = append(ops, diffGrantSet(snap.Grants, o.Grants, onClause, pos)...)
	ops = append(ops, diffRevocationSet(snap.Revocations, o.Revocations, onClause, pos)...)
	return ops, nil
}

// qualifyTableForCompare mirrors qualifyFuncForCompare (used for Cast/
// EventTrigger's Function fields): a Publication's FOR TABLE entry may be
// left unqualified in source, relying on the default "public" schema,
// while introspection always returns a fully schema-qualified name.
func qualifyTableForCompare(s string) string {
	if strings.Contains(s, ".") {
		return s
	}
	return "public." + s
}

// publicationTableKeys converts Tables to "schema.name"/"name" strings
// matching toSnapObject's own PublicationTables format (see convert.go) —
// raw, un-defaulted, for storage-shape consistency; qualifyTableForCompare
// normalizes at comparison time instead, same layering as
// qualifyFuncForCompare.
func publicationTableKeys(tables []ir.PublicationTableRef) []string {
	out := make([]string, len(tables))
	for i, t := range tables {
		if t.Schema != "" {
			out[i] = t.Schema + "." + t.Name
		} else {
			out[i] = t.Name
		}
	}
	return out
}

// publishClause renders a Publication's WITH (publish = ...) value —
// matches introspect.publishOption's own "insert, update"-style format for
// consistency, though byte-identical output isn't required here (this
// feeds a freshly-built ALTER statement, not a reconstruction compare).
func publishClause(o *ir.Publication) string {
	var ops []string
	if o.Insert {
		ops = append(ops, "insert")
	}
	if o.Update {
		ops = append(ops, "update")
	}
	if o.Delete {
		ops = append(ops, "delete")
	}
	if o.Truncate {
		ops = append(ops, "truncate")
	}
	return strings.Join(ops, ", ")
}

// diffPublication implements RFC Section 13.1's structured diffing table: a
// FOR ALL TABLES publication can never be converted to/from an explicit
// table list via ALTER (confirmed live against a real PostgreSQL 17
// server: "Tables cannot be added to or dropped from FOR ALL TABLES
// publications") — an AllTables change decides DROP+CREATE (DESTRUCTIVE,
// matching dropObject's existing "publication" case and RFC Section 13.1's
// "Publication removed" row). A Tables or WITH (publish = ...) change
// each get their own real, targeted ALTER PUBLICATION (RFC Section 13.1: both
// rows SAFE) — a genuine new capability, not just detection. Replaces
// diffOpaqueIR's generic Reconstructed-gated body-hash compare (silently
// unset on every live path). FOR TABLES IN SCHEMA targets are a
// pre-existing, separate gap not modeled here (see ir.Publication.
// AllTables' doc comment) — unaffected by this change either way.
func diffPublication(o *ir.Publication, snap *snapshot.SnapOpaque) ([]pipeline.DiffOp, error) {
	pos := o.SrcPos
	ident := quoteIdent(o.Name)
	// RENAMED FROM on a publication is bare, like Role's identical
	// mechanism (RFC audit item #78) — publications are cluster-level, not
	// schema-scoped. Applies regardless of which branch below handles the
	// rest of the diff, same as diffCollation's renameOps.
	var renameOps []pipeline.DiffOp
	if snap.Name != o.Name {
		renameOps = append(renameOps, cautionOp(
			fmt.Sprintf("ALTER PUBLICATION %s RENAME TO %s;", quoteIdent(snap.Name), ident),
			pos,
		))
	}
	if !snap.PublicationStructured {
		// Stale snapshot predating these structured fields — same
		// self-healing pattern as diffFDW/diffForeignServer.
		ops := renameOps
		if !ptrEq(o.Owner, snap.PublicationOwner) && o.Owner != nil {
			ops = append(ops, safeOp(fmt.Sprintf("ALTER PUBLICATION %s OWNER TO %s;", ident, quoteIdent(*o.Owner)), pos))
		}
		if !ptrEq(o.Comment, snap.Comment) {
			if sql := commentOnOpaqueSQL("publication", "", o.Name, "", "", o.Comment); sql != "" {
				ops = append(ops, safeOp(sql, pos))
			}
		}
		ops = append(ops, diffSecurityLabelSet(snap.SecurityLabels, o.SecurityLabels, "PUBLICATION "+ident, pos)...)
		if len(ops) == 0 {
			ops = append(ops, safeOp(fmt.Sprintf("-- refresh snapshot metadata for publication %s", ident), pos))
		}
		return ops, nil
	}
	if o.AllTables != snap.PublicationAllTables {
		ops := dropObject(&snapshot.SnapObject{Kind: snap.Kind, Opaque: snap})
		createOps, err := createOpaque(o.Name, o.Body, "PUBLICATION", "", pos)
		if err != nil {
			return nil, err
		}
		ops = append(ops, createOps...)
		if o.Owner != nil {
			ops = append(ops, safeOp(fmt.Sprintf("ALTER PUBLICATION %s OWNER TO %s;", ident, quoteIdent(*o.Owner)), pos))
		}
		ops, err = appendCommentOp(ops, nil, "publication", "", o.Name, "", "", o.Comment, pos)
		if err != nil {
			return nil, err
		}
		ops = append(ops, createSecurityLabelOps(o.SecurityLabels, "PUBLICATION "+ident, pos)...)
		return ops, nil
	}
	ops := renameOps
	if !o.AllTables {
		desiredKeys := publicationTableKeys(o.Tables)
		normDesired := make([]string, len(desiredKeys))
		for i, k := range desiredKeys {
			normDesired[i] = qualifyTableForCompare(k)
		}
		normLive := make([]string, len(snap.PublicationTables))
		for i, k := range snap.PublicationTables {
			normLive[i] = qualifyTableForCompare(k)
		}
		if !stringSetEqual(normDesired, normLive) {
			if o.HasFilteredTables || snap.PublicationHasFilteredTables {
				// A column-list/WHERE filter exists on at least one table
				// entry, on at least one side — PublicationTableRef can't
				// represent either, so a targeted SET TABLE here would
				// silently rebuild the table list without it (see
				// ir.Publication.HasFilteredTables' doc comment). Fall back
				// to a full DROP+CREATE, which always regenerates from the
				// complete, correct Body.
				ops = dropObject(&snapshot.SnapObject{Kind: snap.Kind, Opaque: snap})
				createOps, err := createOpaque(o.Name, o.Body, "PUBLICATION", "", pos)
				if err != nil {
					return nil, err
				}
				ops = append(ops, createOps...)
				ops, err = appendCommentOp(ops, nil, "publication", "", o.Name, "", "", o.Comment, pos)
				if err != nil {
					return nil, err
				}
				ops = append(ops, createSecurityLabelOps(o.SecurityLabels, "PUBLICATION "+ident, pos)...)
				return ops, nil
			}
			idents := make([]string, len(o.Tables))
			for i, t := range o.Tables {
				idents[i] = qualIdent(t.Schema, t.Name)
			}
			ops = append(ops, safeOp(fmt.Sprintf("ALTER PUBLICATION %s SET TABLE %s;", ident, strings.Join(idents, ", ")), pos))
		}
	}
	if o.Insert != snap.PublicationInsert || o.Update != snap.PublicationUpdate ||
		o.Delete != snap.PublicationDelete || o.Truncate != snap.PublicationTruncate {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER PUBLICATION %s SET (publish = %s);", ident, quoteLit(publishClause(o))), pos))
	}
	if !ptrEq(o.Owner, snap.PublicationOwner) && o.Owner != nil {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER PUBLICATION %s OWNER TO %s;", ident, quoteIdent(*o.Owner)), pos))
	}
	if !ptrEq(o.Comment, snap.Comment) {
		if sql := commentOnOpaqueSQL("publication", "", o.Name, "", "", o.Comment); sql != "" {
			ops = append(ops, safeOp(sql, pos))
		}
	}
	ops = append(ops, diffSecurityLabelSet(snap.SecurityLabels, o.SecurityLabels, "PUBLICATION "+ident, pos)...)
	return ops, nil
}

// diffSubscription mirrors diffOpaqueIR's shape (offline body-hash compare,
// structured DROP+CREATE on a real change) but can't reuse it directly: its
// CREATE side must produce a subscriptionCreateOp, not a generic createOpaque
// op, and Comment is diffed at the field level (like every other
// Comment-bearing kind) rather than folded into the body hash — a
// comment-only edit doesn't need the subscription dropped and recreated.
func diffSubscription(o *ir.Subscription, snap *snapshot.SnapOpaque) ([]pipeline.DiffOp, error) {
	pos := o.SrcPos

	// RENAME TO applies regardless of the body-hash comparison below — Name
	// is a basic identity field, not part of the hashed body. Uses snap's
	// matched (old) name for the FROM side, same reasoning as every other
	// kind's identical rename handling. Not merged into the drop+create
	// branch below — a simultaneous rename needs no separate ALTER there,
	// since createSubscription already creates under the final (new) name.
	// Bare (no schema qualifier) — subscriptions are database-level.
	var renameOps []pipeline.DiffOp
	if snap.Name != o.Name {
		renameOps = append(renameOps, cautionOp(
			fmt.Sprintf("ALTER SUBSCRIPTION %s RENAME TO %s;", quoteIdent(snap.Name), quoteIdent(o.Name)),
			pos,
		))
	}

	if o.Body != "" {
		// hashBody normalizes the subscription's own (new) name back to its
		// old (matched) name before hashing — Body embeds the subscription's
		// own name ("CREATE SUBSCRIPTION name ..."), so hashing it unmodified
		// against a snapshot hash computed under the old name would always
		// misdetect a pure rename as a body change: confirmed live, this
		// previously emitted a real DROP SUBSCRIPTION + CREATE SUBSCRIPTION
		// for a bare rename with no other change, which can even error
		// outright (DROP SUBSCRIPTION also tries to drop the associated
		// replication slot on the publisher, which may not exist under the
		// dropped name). Same reasoning and pattern as diffOpaqueIRHash's
		// callers (diffTSDict et al.), applied inline since Subscription's
		// body-hash comparison is bespoke, not routed through diffOpaqueIR.
		hashBody := opaqueBodyForHash(o.Body, "", o.Name, "", snap.Name)
		newHash := hashText(hashBody)
		if snap.BodyHash != "" && newHash != snap.BodyHash {
			ops := dropObject(&snapshot.SnapObject{Kind: snap.Kind, Opaque: snap})
			createOps, err := createSubscription(o)
			if err != nil {
				return nil, err
			}
			return append(ops, createOps...), nil
		}
	}
	ops := renameOps
	if !ptrEq(o.Owner, snap.SubscriptionOwner) && o.Owner != nil {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER SUBSCRIPTION %s OWNER TO %s;", quoteIdent(o.Name), quoteIdent(*o.Owner)), pos))
	}
	if !ptrEq(o.Comment, snap.Comment) {
		if o.Comment != nil {
			ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON SUBSCRIPTION %s IS %s;", quoteIdent(o.Name), quoteLit(*o.Comment)), pos))
		} else {
			ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON SUBSCRIPTION %s IS NULL;", quoteIdent(o.Name)), pos))
		}
	}
	ops = append(ops, diffSecurityLabelSet(snap.SecurityLabels, o.SecurityLabels, "SUBSCRIPTION "+quoteIdent(o.Name), pos)...)
	return ops, nil
}

func buildProcedureSignature(o *ir.Procedure) string {
	return fmt.Sprintf("%s(%s)", qualIdent(o.Schema, o.Name), ir.ArgsKey(o.Args))
}

func createProcedure(o *ir.Procedure) []pipeline.DiffOp {
	ops := []pipeline.DiffOp{safeOp(ir.RenderCreateProcedureSQL(o), o.SrcPos)}
	sig := buildProcedureSignature(o)
	if txt := effectiveComment(o.Comment, o.Deprecated); txt != nil {
		ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON PROCEDURE %s IS %s;", sig, quoteLit(*txt)), o.SrcPos))
	}
	for _, g := range o.Grants {
		sql := fmt.Sprintf("GRANT %s ON PROCEDURE %s TO %s", privStr(g.Privileges), sig, roleList(g.Roles))
		sql += grantOptSuffix(g)
		ops = append(ops, safeOp(sql+";", o.SrcPos))
	}
	for _, r := range o.Revocations {
		ops = append(ops, explicitRevokeOp(r, "PROCEDURE "+sig, o.SrcPos))
	}
	ops = append(ops, createSecurityLabelOps(o.SecurityLabels, "PROCEDURE "+sig, o.SrcPos)...)
	// DEPENDS ON EXTENSION (Section 9.1) — see createFunction's identical
	// note; no inline CREATE PROCEDURE form exists either.
	for _, ext := range o.DependsOnExtensions {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER PROCEDURE %s DEPENDS ON EXTENSION %s;", sig, quoteIdent(ext)), o.SrcPos))
	}
	return ops
}

func buildAggregateSignature(o *ir.Aggregate) string {
	return fmt.Sprintf("%s(%s)", qualIdent(o.Schema, o.Name), ir.ArgsKey(o.Args))
}

func createAggregate(o *ir.Aggregate) ([]pipeline.DiffOp, error) {
	if o.Body == "" {
		return nil, fmt.Errorf("aggregate %s: body not captured; define it explicitly in a .dpg source file", o.QualifiedName())
	}
	sig := buildAggregateSignature(o)
	var ops []pipeline.DiffOp
	if o.Owner != nil {
		ops = wrapCreateWithOwner(safeOp(o.Body+";", o.SrcPos), o.Owner, o.SrcPos)
	} else {
		ops = []pipeline.DiffOp{safeOp(o.Body+";", o.SrcPos)}
	}
	if txt := effectiveComment(o.Comment, o.Deprecated); txt != nil {
		ops = append(ops, safeOp(
			fmt.Sprintf("COMMENT ON AGGREGATE %s IS %s;", sig, quoteLit(*txt)),
			o.SrcPos,
		))
	}
	for _, g := range o.Grants {
		sql := fmt.Sprintf("GRANT %s ON FUNCTION %s TO %s", privStr(g.Privileges), sig, roleList(g.Roles))
		sql += grantOptSuffix(g)
		ops = append(ops, safeOp(sql+";", o.SrcPos))
	}
	for _, r := range o.Revocations {
		ops = append(ops, explicitRevokeOp(r, "FUNCTION "+sig, o.SrcPos))
	}
	ops = append(ops, createSecurityLabelOps(o.SecurityLabels, "FUNCTION "+sig, o.SrcPos)...)
	return ops, nil
}

// diffAggregate diffs a CREATE AGGREGATE declaration against its snapshot.
// PostgreSQL has no incremental ALTER AGGREGATE, so any genuine change to
// SFUNC/STYPE/INITCOND/... still must resolve to DROP+CREATE — but whether
// that DROP+CREATE is warranted is decided by comparing the already-
// structured Options list (see SnapOpaque.AggregateOptionsStructured's doc
// comment), not by hashing the raw Body text. Raw-hash comparison false-
// positives on cosmetic differences (keyword case, option order) between
// hand-written source and an introspected reconstruction — the same class
// of bug already fixed for OperatorClass (see diffOperatorClass). A stale
// pre-feature snapshot (AggregateOptionsStructured false) falls back to the
// old raw-BodyHash path.
func diffAggregate(o *ir.Aggregate, snap *snapshot.SnapOpaque) ([]pipeline.DiffOp, error) {
	sig := buildAggregateSignature(o)
	pos := o.SrcPos

	var bodyChanged bool
	if snap.AggregateOptionsStructured {
		bodyChanged = !optionsEqual(toComparableOptions(o.Options, false), snap.AggregateOptions)
	} else {
		var newHash string
		if o.Body != "" {
			sum := sha256.Sum256([]byte(strings.TrimSpace(o.Body)))
			newHash = fmt.Sprintf("%x", sum)
		}
		// Skip body comparison when either side has no hash: the live snapshot
		// (introspected) cannot reconstruct the aggregate body, so we only diff
		// body when both sides have a hash (offline plan against committed snapshot).
		bodyChanged = newHash != "" && snap.BodyHash != "" && newHash != snap.BodyHash
	}

	if bodyChanged {
		createOp := safeOp(o.Body+";", pos)
		ops := []pipeline.DiffOp{destructiveOp(fmt.Sprintf("DROP AGGREGATE IF EXISTS %s;", sig), pos)}
		if o.Owner != nil {
			ops = append(ops, wrapCreateWithOwner(createOp, o.Owner, pos)...)
		} else {
			ops = append(ops, createOp)
		}
		if txt := effectiveComment(o.Comment, o.Deprecated); txt != nil {
			ops = append(ops, safeOp(
				fmt.Sprintf("COMMENT ON AGGREGATE %s IS %s;", sig, quoteLit(*txt)),
				pos,
			))
		}
		ops = append(ops, diffGrantSet(nil, o.Grants, "FUNCTION "+sig, pos)...)
		ops = append(ops, diffRevocationSet(nil, o.Revocations, "FUNCTION "+sig, pos)...)
		ops = append(ops, createSecurityLabelOps(o.SecurityLabels, "FUNCTION "+sig, pos)...)
		return ops, nil
	}

	var ops []pipeline.DiffOp
	// SET SCHEMA / RENAME TO (RFC audit item #11) — see diffProcedure's
	// identical reasoning, including the snap.Schema (not o.Schema) old-
	// signature fix and the SET-SCHEMA-first ordering.
	if snap.Schema != o.Schema {
		oldSig := fmt.Sprintf("%s(%s)", qualIdent(snap.Schema, snap.Name), ir.ArgsKey(o.Args))
		ops = append(ops, safeOp(fmt.Sprintf("ALTER AGGREGATE %s SET SCHEMA %s;", oldSig, quoteIdent(o.Schema)), pos))
	}
	if snap.Name != o.Name {
		oldSig := fmt.Sprintf("%s(%s)", qualIdent(o.Schema, snap.Name), ir.ArgsKey(o.Args))
		ops = append(ops, cautionOp(fmt.Sprintf("ALTER AGGREGATE %s RENAME TO %s;", oldSig, quoteIdent(o.Name)), pos))
	}
	if desiredTxt, snapTxt := effectiveComment(o.Comment, o.Deprecated), effectiveComment(snap.Comment, snap.Deprecated); !ptrEq(desiredTxt, snapTxt) {
		if desiredTxt != nil {
			ops = append(ops, safeOp(
				fmt.Sprintf("COMMENT ON AGGREGATE %s IS %s;", sig, quoteLit(*desiredTxt)),
				pos,
			))
		} else {
			ops = append(ops, safeOp(
				fmt.Sprintf("COMMENT ON AGGREGATE %s IS NULL;", sig),
				pos,
			))
		}
	}
	if !ptrEq(o.Owner, snap.AggregateOwner) && o.Owner != nil {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER AGGREGATE %s OWNER TO %s;", sig, quoteIdent(*o.Owner)), pos))
	}
	ops = append(ops, diffGrantSet(snap.Grants, o.Grants, "FUNCTION "+sig, pos)...)
	ops = append(ops, diffRevocationSet(snap.Revocations, o.Revocations, "FUNCTION "+sig, pos)...)
	ops = append(ops, diffSecurityLabelSet(snap.SecurityLabels, o.SecurityLabels, "FUNCTION "+sig, pos)...)
	return ops, nil
}

func createDefaultPrivileges(o *ir.DefaultPrivileges) []pipeline.DiffOp {
	var ops []pipeline.DiffOp
	pos := o.SrcPos
	prefix := buildDefaultPrivPrefix(o.ForRole, o.InSchema)
	for _, g := range o.Grants {
		sql := fmt.Sprintf("%s GRANT %s ON %s TO %s",
			prefix, privStr(g.Privileges), o.ObjectType, roleList(g.Roles))
		sql += grantOptSuffix(g)
		ops = append(ops, safeOp(sql+";", pos))
	}
	for _, r := range o.Revocations {
		sql := fmt.Sprintf("%s REVOKE %s ON %s FROM %s%s",
			prefix, privStr(r.Privileges), o.ObjectType, roleList(r.Roles), revokeOptSuffix(r))
		ops = append(ops, cautionOp(sql+";", pos))
	}
	return ops
}

// diffDefaultPrivileges diffs a DEFAULT PRIVILEGES declaration against its snapshot.
// Grants are diffed structurally; revocations are re-emitted whenever their set changes.
func diffDefaultPrivileges(o *ir.DefaultPrivileges, snap *snapshot.SnapDefaultPrivileges) []pipeline.DiffOp {
	var ops []pipeline.DiffOp
	pos := o.SrcPos

	prefix := buildDefaultPrivPrefix(o.ForRole, o.InSchema)

	// Diff grants.
	snapGrantsByKey := make(map[string]snapshot.SnapGrant, len(snap.Grants))
	for _, g := range snap.Grants {
		snapGrantsByKey[grantKey(g.Privileges, g.Roles, g.WithGrant)] = g
	}
	desiredGrantsByKey := make(map[string]ir.Grant, len(o.Grants))
	for _, g := range o.Grants {
		desiredGrantsByKey[grantKey(g.Privileges, g.Roles, g.WithGrant)] = g
	}

	for k, sg := range snapGrantsByKey {
		if _, ok := desiredGrantsByKey[k]; !ok {
			ops = append(ops, cautionOp(
				fmt.Sprintf("%s REVOKE %s ON %s FROM %s;",
					prefix, privStr(sg.Privileges), o.ObjectType, roleList(sg.Roles)),
				pos,
			))
		}
	}
	for k, g := range desiredGrantsByKey {
		if _, ok := snapGrantsByKey[k]; !ok {
			sql := fmt.Sprintf("%s GRANT %s ON %s TO %s",
				prefix, privStr(g.Privileges), o.ObjectType, roleList(g.Roles))
			sql += grantOptSuffix(g)
			ops = append(ops, safeOp(sql+";", pos))
		}
	}

	// Diff explicit revocations.
	snapRevsByKey := make(map[string]snapshot.SnapGrant, len(snap.Revocations))
	for _, r := range snap.Revocations {
		snapRevsByKey[grantKey(r.Privileges, r.Roles, false)] = r
	}
	desiredRevsByKey := make(map[string]ir.Revocation, len(o.Revocations))
	for _, r := range o.Revocations {
		desiredRevsByKey[grantKey(r.Privileges, r.Roles, false)] = r
	}

	for k, sr := range snapRevsByKey {
		if _, ok := desiredRevsByKey[k]; !ok {
			// Revocation removed from desired: re-grant to restore.
			ops = append(ops, safeOp(
				fmt.Sprintf("%s GRANT %s ON %s TO %s;",
					prefix, privStr(sr.Privileges), o.ObjectType, roleList(sr.Roles)),
				pos,
			))
		}
	}
	for k, r := range desiredRevsByKey {
		if _, ok := snapRevsByKey[k]; !ok {
			ops = append(ops, cautionOp(
				fmt.Sprintf("%s REVOKE %s ON %s FROM %s%s;",
					prefix, privStr(r.Privileges), o.ObjectType, roleList(r.Roles), revokeOptSuffix(r)),
				pos,
			))
		}
	}

	return ops
}

// paramList quotes/joins a PARAMETER PRIVILEGES parameter-name list. Unlike
// roleList, parameter names are GUC names (not SQL identifiers to be
// escaped) — real PostgreSQL's GRANT ... ON PARAMETER grammar takes them as
// plain name tokens, dotted namespaced GUCs (e.g.
// "pg_stat_statements.max") included.
func paramList(params []string) string {
	return strings.Join(params, ", ")
}

// paramGrantKey extends grantKey with a sorted parameter list. Every other
// use of grantKey compares grants scoped to one implicit "on" object (a
// single table, or one DefaultPrivileges' fixed ObjectType); PARAMETER
// PRIVILEGES has no such implicit scope — each grant entry carries its own
// parameter list — so that list is part of the entry's identity here.
func paramGrantKey(privs, params, roles []string, withGrant bool) string {
	p := append([]string(nil), params...)
	sort.Strings(p)
	return grantKey(privs, roles, withGrant) + "|" + strings.Join(p, ",")
}

// createParameterPrivileges emits GRANT/REVOKE ops for a brand-new PARAMETER
// PRIVILEGES declaration (RFC Section 11.6, PG15+). Safety SAFE for both
// directions per the RFC — this is metadata governing who may run a command,
// not an object with data of its own.
func createParameterPrivileges(o *ir.ParameterPrivileges) []pipeline.DiffOp {
	var ops []pipeline.DiffOp
	pos := o.SrcPos
	for _, g := range o.Grants {
		sql := fmt.Sprintf("GRANT %s ON PARAMETER %s TO %s",
			privStr(g.Privileges), paramList(g.Parameters), roleList(g.Roles))
		sql += paramGrantOptSuffix(g)
		ops = append(ops, safeOp(sql+";", pos))
	}
	for _, r := range o.Revocations {
		sql := fmt.Sprintf("REVOKE %s ON PARAMETER %s FROM %s",
			privStr(r.Privileges), paramList(r.Parameters), roleList(r.Roles))
		sql += paramRevokeOptSuffix(r)
		ops = append(ops, safeOp(sql+";", pos))
	}
	return ops
}

// paramGrantOptSuffix is grantOptSuffix's ParameterGrant counterpart (that
// helper takes ir.Grant specifically; ParameterGrant is a distinct type).
func paramGrantOptSuffix(g ir.ParameterGrant) string {
	s := ""
	if g.WithGrant {
		s += " WITH GRANT OPTION"
	}
	if g.GrantedBy != nil {
		s += " GRANTED BY " + roleSpecSQL(*g.GrantedBy)
	}
	return s
}

// paramRevokeOptSuffix is revokeOptSuffix's ParameterRevocation counterpart.
func paramRevokeOptSuffix(r ir.ParameterRevocation) string {
	s := ""
	if r.GrantedBy != nil {
		s += " GRANTED BY " + roleSpecSQL(*r.GrantedBy)
	}
	if r.Cascade {
		s += " CASCADE"
	}
	return s
}

// diffParameterPrivileges diffs a PARAMETER PRIVILEGES declaration against
// its snapshot. Unlike diffGrantSet/diffDefaultPrivileges (which, per RFC
// audit item #25, emit an implicit REVOKE — cautionOp — when a grant is
// removed from desired), PARAMETER PRIVILEGES follows RFC Section 11.6's
// literal text: "removing the declaration emits nothing (an explicit
// REVOKES { } entry is required to actually REVOKE)". This is a
// deliberate departure from the other grant kinds' current behavior, not an
// oversight — Section 11.6 restates Section 11.2's original "emits nothing"
// model verbatim for this brand-new kind, so it is implemented as written
// here rather than silently inheriting #25's since-diverged precedent.
// Explicit revocations are still tracked as persistent declarations exactly
// like diffDefaultPrivileges' revocation-diffing (RFC Section 11.3).
func diffParameterPrivileges(o *ir.ParameterPrivileges, snap *snapshot.SnapParameterPrivileges) []pipeline.DiffOp {
	var ops []pipeline.DiffOp
	pos := o.SrcPos

	snapGrantsByKey := make(map[string]snapshot.SnapParamGrant, len(snap.Grants))
	for _, g := range snap.Grants {
		snapGrantsByKey[paramGrantKey(g.Privileges, g.Parameters, g.Roles, g.WithGrant)] = g
	}
	desiredGrantsByKey := make(map[string]ir.ParameterGrant, len(o.Grants))
	for _, g := range o.Grants {
		desiredGrantsByKey[paramGrantKey(g.Privileges, g.Parameters, g.Roles, g.WithGrant)] = g
	}

	// No pass over snapGrantsByKey for "removed from desired" here — the
	// additive model's deliberate no-op (RFC Section 11.6/11.2): DPG only
	// ensures declared grants are present, it never revokes one just because
	// its declaration was deleted.
	for k, g := range desiredGrantsByKey {
		sg, matched := snapGrantsByKey[k]
		if !matched {
			sql := fmt.Sprintf("GRANT %s ON PARAMETER %s TO %s",
				privStr(g.Privileges), paramList(g.Parameters), roleList(g.Roles))
			sql += paramGrantOptSuffix(g)
			ops = append(ops, safeOp(sql+";", pos))
			continue
		}
		// Matched: GrantedBy isn't part of paramGrantKey — same "declared, so
		// managed" comparison rule as diffGrantSet (RFC audit item #90,
		// generalized to Parameter Privileges).
		if !grantedByMatches(g.GrantedBy, sg.GrantedBy) {
			sql := fmt.Sprintf("GRANT %s ON PARAMETER %s TO %s",
				privStr(g.Privileges), paramList(g.Parameters), roleList(g.Roles))
			sql += paramGrantOptSuffix(g)
			ops = append(ops, safeOp(sql+";", pos))
		}
	}

	// Diff explicit revocations.
	snapRevsByKey := make(map[string]snapshot.SnapParamGrant, len(snap.Revocations))
	for _, r := range snap.Revocations {
		snapRevsByKey[paramGrantKey(r.Privileges, r.Parameters, r.Roles, false)] = r
	}
	desiredRevsByKey := make(map[string]ir.ParameterRevocation, len(o.Revocations))
	for _, r := range o.Revocations {
		desiredRevsByKey[paramGrantKey(r.Privileges, r.Parameters, r.Roles, false)] = r
	}

	for k, sr := range snapRevsByKey {
		if _, ok := desiredRevsByKey[k]; !ok {
			// Revocation removed from desired: re-grant to restore.
			ops = append(ops, safeOp(
				fmt.Sprintf("GRANT %s ON PARAMETER %s TO %s;",
					privStr(sr.Privileges), paramList(sr.Parameters), roleList(sr.Roles)),
				pos,
			))
		}
	}
	for k, r := range desiredRevsByKey {
		sr, matched := snapRevsByKey[k]
		if !matched {
			sql := fmt.Sprintf("REVOKE %s ON PARAMETER %s FROM %s",
				privStr(r.Privileges), paramList(r.Parameters), roleList(r.Roles))
			sql += paramRevokeOptSuffix(r)
			ops = append(ops, cautionOp(sql+";", pos))
			continue
		}
		// Matched: same "declared, so managed" GrantedBy re-check as
		// diffRevocationSet's identical branch (RFC audit item #90).
		if !grantedByMatches(r.GrantedBy, sr.GrantedBy) {
			sql := fmt.Sprintf("REVOKE %s ON PARAMETER %s FROM %s",
				privStr(r.Privileges), paramList(r.Parameters), roleList(r.Roles))
			sql += paramRevokeOptSuffix(r)
			ops = append(ops, cautionOp(sql+";", pos))
		}
	}

	return ops
}

// buildDefaultPrivPrefix builds the "ALTER DEFAULT PRIVILEGES [FOR ROLE x] [IN SCHEMA y]" prefix.
func buildDefaultPrivPrefix(forRole, inSchema *string) string {
	var b strings.Builder
	b.WriteString("ALTER DEFAULT PRIVILEGES")
	if forRole != nil {
		b.WriteString(" FOR ROLE ")
		b.WriteString(quoteIdent(*forRole))
	}
	if inSchema != nil {
		b.WriteString(" IN SCHEMA ")
		b.WriteString(quoteIdent(*inSchema))
	}
	return b.String()
}

func createSchema(o *ir.Schema) []pipeline.DiffOp {
	var b strings.Builder
	b.WriteString("CREATE SCHEMA IF NOT EXISTS ")
	b.WriteString(quoteIdent(o.Name))
	if o.Owner != nil {
		b.WriteString(" AUTHORIZATION ")
		b.WriteString(quoteIdent(*o.Owner))
	}
	b.WriteString(";")
	ops := wrapCreateWithOwner(safeOp(b.String(), o.SrcPos), o.Owner, o.SrcPos)
	if o.Comment != nil {
		ops = append(ops, safeOp(
			fmt.Sprintf("COMMENT ON SCHEMA %s IS %s;", quoteIdent(o.Name), quoteLit(*o.Comment)),
			o.SrcPos,
		))
	}
	schemaIdent := quoteIdent(o.Name)
	for _, g := range o.Grants {
		sql := fmt.Sprintf("GRANT %s ON SCHEMA %s TO %s", privStr(g.Privileges), schemaIdent, roleList(g.Roles))
		sql += grantOptSuffix(g)
		ops = append(ops, safeOp(sql+";", o.SrcPos))
	}
	for _, r := range o.Revocations {
		ops = append(ops, explicitRevokeOp(r, "SCHEMA "+schemaIdent, o.SrcPos))
	}
	ops = append(ops, createSecurityLabelOps(o.SecurityLabels, "SCHEMA "+schemaIdent, o.SrcPos)...)
	return ops
}

func createExtension(o *ir.Extension) []pipeline.DiffOp {
	var b strings.Builder
	b.WriteString("CREATE EXTENSION IF NOT EXISTS ")
	b.WriteString(quoteIdent(o.Name))
	if o.Schema != nil {
		b.WriteString(" SCHEMA ")
		b.WriteString(quoteIdent(*o.Schema))
	}
	if o.Version != nil {
		b.WriteString(" VERSION '")
		b.WriteString(*o.Version)
		b.WriteString("'")
	}
	if o.Cascade {
		b.WriteString(" CASCADE")
	}
	b.WriteString(";")
	ops := []pipeline.DiffOp{safeOp(b.String(), o.SrcPos)}
	if o.Comment != nil {
		ops = append(ops, safeOp(
			fmt.Sprintf("COMMENT ON EXTENSION %s IS %s;", quoteIdent(o.Name), quoteLit(*o.Comment)),
			o.SrcPos,
		))
	}
	return ops
}

func createTable(o *ir.Table) []pipeline.DiffOp {
	var b strings.Builder
	switch {
	case o.Unlogged:
		b.WriteString("CREATE UNLOGGED TABLE ")
	case o.Foreign:
		b.WriteString("CREATE FOREIGN TABLE ")
	default:
		b.WriteString("CREATE TABLE ")
	}
	b.WriteString(qualIdent(o.Schema, o.Name))

	if o.OfType.Name != "" {
		// RFC Section 7.1's "Form 2" typed table: buildTable already
		// rejects any per-attribute WITH OPTIONS narrowing column (see
		// ir.Table.OfType's doc comment), so o.Columns is guaranteed empty
		// here — the only possible parenthesized content is table-level
		// constraints (a plain CHECK etc., which parse identically to a
		// regular table's). Real PostgreSQL rejects empty parens outright
		// ("CREATE TABLE t OF ty ()" is a syntax error), so they're omitted
		// entirely when there are none to declare.
		b.WriteString(" OF ")
		b.WriteString(o.OfType.String())
		if len(o.Constraints) > 0 {
			b.WriteString(" (")
			for i, cst := range o.Constraints {
				if i > 0 {
					b.WriteString(",")
				}
				b.WriteString("\n    ")
				if cst.Name != "" {
					b.WriteString("CONSTRAINT ")
					b.WriteString(quoteIdent(cst.Name))
					b.WriteString(" ")
				}
				b.WriteString(cst.Expr)
				if cst.NotValid {
					b.WriteString(" NOT VALID")
				}
				if cst.NotEnforced {
					b.WriteString(" NOT ENFORCED")
				}
			}
			b.WriteString("\n)")
		}
		return finishCreateTable(&b, o)
	}

	b.WriteString(" (")

	// Classify constraints: single-column PK/UNIQUE/FK and column-promoted CHECK
	// are inlined into their column; everything else stays table-level.
	// A column may accumulate multiple inline clauses (e.g. UNIQUE + REFERENCES
	// + CHECK), so inlineFor holds a slice ordered by declaration.
	// pkColSet tracks all PK columns so we can suppress redundant NOT NULL
	// (PRIMARY KEY already implies NOT NULL in PostgreSQL).
	type inlineKW struct {
		name    string // CONSTRAINT name, may be ""
		keyword string // e.g. "PRIMARY KEY", "UNIQUE", "REFERENCES ...", "CHECK (...)"
	}
	inlineFor := make(map[string][]inlineKW) // colName → ordered inline clauses
	pkColSet := make(map[string]bool)        // any PK column (single- or multi-)
	// notNullColSet tracks columns whose NOT NULL is represented by a
	// promoted table-level Constraint (explicit name and/or NO INHERIT)
	// rather than the plain Column.NotNull bool, so the per-column render
	// loop below doesn't ALSO emit a redundant bare " NOT NULL" alongside
	// the inlined "CONSTRAINT name NOT NULL ... [NO INHERIT]" clause.
	notNullColSet := make(map[string]bool)
	skipIdx := make(map[int]bool) // constraint index to omit at table level

	for i, cst := range o.Constraints {
		cols := localConstraintCols(cst.Expr)
		switch cst.Type {
		case "PRIMARY KEY":
			for _, c := range cols {
				pkColSet[c] = true
			}
			if len(cols) == 1 {
				inlineFor[cols[0]] = append(inlineFor[cols[0]], inlineKW{name: cst.Name, keyword: "PRIMARY KEY"})
				skipIdx[i] = true
			}
		case "UNIQUE":
			if len(cols) == 1 {
				// Skip if column already carries PRIMARY KEY — UNIQUE is redundant.
				hasPK := false
				for _, kw := range inlineFor[cols[0]] {
					if kw.keyword == "PRIMARY KEY" {
						hasPK = true
						break
					}
				}
				if !hasPK {
					kw := "UNIQUE"
					if strings.Contains(strings.ToUpper(cst.Expr), "NULLS NOT DISTINCT") {
						kw = "UNIQUE NULLS NOT DISTINCT"
					}
					inlineFor[cols[0]] = append(inlineFor[cols[0]], inlineKW{name: cst.Name, keyword: kw})
					skipIdx[i] = true
				}
			}
		case "FOREIGN KEY":
			// Single-column FK: strip "FOREIGN KEY ("col") " prefix, keep "REFERENCES ...".
			// The REFERENCES suffix includes ON DELETE/UPDATE actions and DEFERRABLE.
			// NotValid constraints are never inlined (see the CHECK case's identical
			// guard below for why) — they always stay in the table-level loop, the
			// only place NOT VALID is actually appended. NotEnforced constraints are
			// excluded the same way, even though real PostgreSQL's inline grammar
			// does support ENFORCED/NOT ENFORCED there (unlike NOT VALID) — a
			// deliberate simplification, not a limitation: NOT ENFORCED is rare
			// enough that routing it through the one table-level rendering path
			// this codebase already appends it at (below) is simpler than adding a
			// second, inline-specific rendering path for it.
			if len(cols) == 1 && !cst.NotValid && !cst.NotEnforced {
				upper := strings.ToUpper(cst.Expr)
				if refIdx := strings.Index(upper, "REFERENCES"); refIdx > 0 {
					inlineFor[cols[0]] = append(inlineFor[cols[0]], inlineKW{name: cst.Name, keyword: cst.Expr[refIdx:]})
					skipIdx[i] = true
				}
			}
		case "CHECK":
			// Inline CHECK when Columns is populated — that means the constraint was
			// promoted from a column definition (buildColumn sets Columns=[colname]).
			// Table-level CHECK constraints (Columns empty) stay table-level because
			// we cannot safely infer which column they belong to from the expression.
			// NotValid constraints are excluded from inlining regardless of column
			// count: whether PostgreSQL's inline column_constraint grammar even
			// accepts a trailing NOT VALID isn't verified here, so routing them to
			// the table-level loop (which does append it, confirmed live) avoids
			// silently dropping NOT VALID again the way the un-guarded version of
			// this loop did before this fix. NotEnforced constraints are excluded
			// the same way — see the FOREIGN KEY case's identical guard above for
			// why (this one's real PostgreSQL support for inline ENFORCED is
			// confirmed, not just assumed, but the simplification is the same).
			if len(cst.Columns) == 1 && !cst.NotValid && !cst.NotEnforced {
				inlineFor[cst.Columns[0]] = append(inlineFor[cst.Columns[0]], inlineKW{name: cst.Name, keyword: cst.Expr})
				skipIdx[i] = true
			}
		case "NOT NULL":
			// A promoted NOT NULL constraint (explicit name and/or NO
			// INHERIT — see buildColumn/buildConstraint's promotion rule)
			// always has exactly one column, matching real PostgreSQL's
			// own single-column-only grammar for this constraint kind.
			// NotValid stays table-level, same reasoning as CHECK above.
			if len(cst.Columns) == 1 && !cst.NotValid {
				notNullColSet[cst.Columns[0]] = true
				inlineFor[cst.Columns[0]] = append(inlineFor[cst.Columns[0]], inlineKW{name: cst.Name, keyword: cst.Expr})
				skipIdx[i] = true
			}
		}
	}

	for i, col := range o.Columns {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n    ")
		b.WriteString(quoteIdent(col.Name))
		b.WriteString(" ")
		if col.Serial != nil {
			// Emit the literal SERIAL/BIGSERIAL/SMALLSERIAL keyword and let
			// PostgreSQL's own server-side macro-expansion create the
			// backing sequence, default, ownership, and implicit NOT NULL —
			// rather than DPG hand-reconstructing that expansion itself.
			b.WriteString(*col.Serial)
		} else {
			b.WriteString(col.Type.String())
		}
		// Suppress NOT NULL for PK columns — PRIMARY KEY already implies it.
		// Also suppress for SERIAL — PG's own macro-expansion adds it. Also
		// suppress when a promoted NOT NULL constraint (explicit name
		// and/or NO INHERIT) is about to be inlined for this column below —
		// otherwise both the bare keyword and the CONSTRAINT clause would
		// render, redundantly (and, for NO INHERIT specifically, wrongly:
		// the bare form has no way to carry it at all).
		if col.NotNull && !pkColSet[col.Name] && col.Serial == nil && !notNullColSet[col.Name] {
			b.WriteString(" NOT NULL")
		}
		if col.Default != nil && col.Serial == nil {
			b.WriteString(" DEFAULT ")
			b.WriteString(*col.Default)
		}
		if col.Identity != nil {
			if col.Identity.Always {
				b.WriteString(" GENERATED ALWAYS AS IDENTITY")
			} else {
				b.WriteString(" GENERATED BY DEFAULT AS IDENTITY")
			}
			writeIdentityOpts(&b, col.Identity)
		}
		if col.Generated != nil {
			b.WriteString(" GENERATED ALWAYS AS (")
			b.WriteString(col.Generated.Expr)
			b.WriteString(") STORED")
		}
		for _, spec := range inlineFor[col.Name] {
			if spec.name != "" {
				b.WriteString(" CONSTRAINT ")
				b.WriteString(quoteIdent(spec.name))
			}
			b.WriteString(" ")
			b.WriteString(spec.keyword)
		}
	}
	for i, cst := range o.Constraints {
		if skipIdx[i] {
			continue
		}
		b.WriteString(",\n    ")
		if cst.Name != "" {
			b.WriteString("CONSTRAINT ")
			b.WriteString(quoteIdent(cst.Name))
			b.WriteString(" ")
		}
		b.WriteString(cst.Expr)
		if cst.NotValid {
			b.WriteString(" NOT VALID")
		}
		if cst.NotEnforced {
			b.WriteString(" NOT ENFORCED")
		}
	}
	b.WriteString("\n)")
	return finishCreateTable(&b, o)
}

// finishCreateTable renders the CREATE TABLE clauses/ops shared by both
// createTable's regular-column path and its OF type_name (typed-table)
// path (Section 7.1): everything after the column-list/OF-type-and-
// constraints portion, which the two paths build differently but otherwise
// finish identically (INHERITS, PARTITION BY, SERVER/TABLESPACE, then every
// op that follows the CREATE TABLE statement itself).
func finishCreateTable(b *strings.Builder, o *ir.Table) []pipeline.DiffOp {
	if len(o.Inherits) > 0 && !o.Foreign {
		b.WriteString(" INHERITS (")
		for i, p := range o.Inherits {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(quoteQualIdent(p))
		}
		b.WriteString(")")
	}
	if o.PartitionBy != nil && !o.Foreign {
		b.WriteString(" PARTITION BY ")
		b.WriteString(o.PartitionBy.Strategy)
		b.WriteString(" (")
		b.WriteString(strings.Join(o.PartitionBy.Columns, ", "))
		b.WriteString(")")
	}
	// USING method then WITH (...) storage params — real PostgreSQL's own
	// fixed CREATE TABLE clause order, confirmed live via pg_query.Parse:
	// INHERITS/PARTITION BY must precede USING (moved here from each
	// caller's own tail, which rendered USING BEFORE calling this function
	// — a real bug found while adding WITH's own ordering, live-verified as
	// producing an outright syntax error whenever a table declared both
	// USING and INHERITS/PARTITION BY together: "CREATE TABLE t (...)
	// USING heap INHERITS (p)" is rejected, "... INHERITS (p) ... USING
	// heap" is not). Meaningless for a foreign table (no local storage),
	// matching Tablespace's identical guard below.
	if o.AccessMethod != "" && !o.Foreign {
		b.WriteString(" USING ")
		b.WriteString(o.AccessMethod)
	}
	if len(o.StorageParams) > 0 && !o.Foreign {
		b.WriteString(" WITH (")
		for i, p := range o.StorageParams {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(p.Key)
			b.WriteString("=")
			b.WriteString(p.Value)
		}
		b.WriteString(")")
	}
	if o.Foreign {
		// Real PostgreSQL grammar: CREATE FOREIGN TABLE name (cols)
		// SERVER server_name [OPTIONS (...)]. Without this, the emitted
		// statement was missing SERVER entirely — not just incomplete, an
		// outright syntax error, since SERVER is mandatory for a foreign table.
		if o.ForeignServer != nil {
			b.WriteString(" SERVER ")
			b.WriteString(quoteIdent(*o.ForeignServer))
		}
		if len(o.ForeignOptions) > 0 {
			b.WriteString(" OPTIONS (")
			for i, p := range o.ForeignOptions {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(p.Key)
				b.WriteString(" ")
				b.WriteString(quoteLit(p.Value))
			}
			b.WriteString(")")
		}
	} else if o.Tablespace != nil {
		// TABLESPACE is real PostgreSQL's final CREATE TABLE clause, and
		// meaningless for a foreign table (it stores no local data).
		b.WriteString(" TABLESPACE ")
		b.WriteString(quoteIdent(*o.Tablespace))
	}
	b.WriteString(";")

	var ops []pipeline.DiffOp
	ops = append(ops, wrapCreateWithOwner(safeOp(b.String(), o.SrcPos), o.Owner, o.SrcPos)...)
	for _, p := range o.Partitions {
		ops = append(ops, createPartitionOps(o.Schema, qualIdent(o.Schema, o.Name), p)...)
	}

	if txt := effectiveComment(o.Comment, o.Deprecated); txt != nil {
		ops = append(ops, safeOp(
			fmt.Sprintf("COMMENT ON TABLE %s IS %s;", qualIdent(o.Schema, o.Name), quoteLit(*txt)),
			o.SrcPos,
		))
	}
	for _, col := range o.Columns {
		if col.Comment != nil {
			ops = append(ops, safeOp(
				fmt.Sprintf("COMMENT ON COLUMN %s.%s IS %s;",
					qualIdent(o.Schema, o.Name), quoteIdent(col.Name), quoteLit(*col.Comment)),
				col.SrcPos,
			))
		}
	}
	for _, cst := range o.Constraints {
		if cst.Comment != nil && cst.Name != "" {
			ops = append(ops, safeOp(
				fmt.Sprintf("COMMENT ON CONSTRAINT %s ON %s IS %s;",
					quoteIdent(cst.Name), qualIdent(o.Schema, o.Name), quoteLit(*cst.Comment)),
				cst.Pos,
			))
		}
	}
	if o.RLSEnabled {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER TABLE %s ENABLE ROW LEVEL SECURITY;", qualIdent(o.Schema, o.Name)), o.SrcPos))
	}
	if o.RLSForced {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER TABLE %s FORCE ROW LEVEL SECURITY;", qualIdent(o.Schema, o.Name)), o.SrcPos))
	}
	for _, idx := range o.Indexes {
		// Always non-concurrent, regardless of idx.Concurrently or the
		// project's concurrent_indexes default: this table doesn't exist yet,
		// so its indexes are created in the SAME transactional migration as
		// the CREATE TABLE itself — and PostgreSQL rejects CREATE INDEX
		// CONCURRENTLY inside a transaction block. See createIndex's doc
		// comment; diffIndexes is the path where the default/explicit
		// CONCURRENTLY actually applies (adding an index to a table that
		// already exists).
		ops = append(ops, createIndex(o.Schema, o.Name, idx, indexWithNewRelation)...)
	}
	// REPLICA IDENTITY/CLUSTER ON (Section 7.11): real PostgreSQL has no
	// CREATE TABLE-native clause for either, only ALTER TABLE — even at
	// initial creation. Emitted after the Indexes loop above so a
	// CLUSTER ON (or REPLICA IDENTITY USING INDEX) referencing one of this
	// table's own just-declared indexes finds it already created.
	if replicaIdentityMode(o.ReplicaIdentity.Mode) != "DEFAULT" {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER TABLE %s REPLICA IDENTITY %s;", qualIdent(o.Schema, o.Name), replicaIdentityClause(o.ReplicaIdentity)), o.SrcPos))
	}
	if o.ClusterOn != nil {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER TABLE %s CLUSTER ON %s;", qualIdent(o.Schema, o.Name), quoteIdent(*o.ClusterOn)), o.SrcPos))
	}
	for _, pol := range o.Policies {
		ops = append(ops, createPolicy(o.Schema, o.Name, pol)...)
	}
	for _, trg := range o.Triggers {
		ops = append(ops, createTrigger(o.Schema, o.Name, trg)...)
	}
	tblIdent := qualIdent(o.Schema, o.Name)
	for _, g := range o.Grants {
		ops = append(ops, tableGrantOp(g, tblIdent, o.SrcPos))
	}
	for _, r := range o.Revocations {
		ops = append(ops, explicitRevokeOp(r, "TABLE "+tblIdent, o.SrcPos))
	}
	tableObjType := "TABLE"
	if o.Foreign {
		tableObjType = "FOREIGN TABLE"
	}
	ops = append(ops, createSecurityLabelOps(o.SecurityLabels, tableObjType+" "+tblIdent, o.SrcPos)...)
	for _, col := range o.Columns {
		// SET STATISTICS has no inline CREATE TABLE column-definition form
		// (unlike STORAGE/COMPRESSION, which render inline elsewhere) —
		// real PostgreSQL only offers it via ALTER COLUMN, even at initial
		// creation. Previously missing entirely: a column declaring a
		// custom target on a brand-new table silently never got it live,
		// while the snapshot (populated from the desired IR, not a live
		// re-introspection) recorded the value anyway — permanently
		// masking the drift on every subsequent plan.
		if col.Statistics != nil {
			ops = append(ops, safeOp(
				fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET STATISTICS %d;", tblIdent, quoteIdent(col.Name), *col.Statistics),
				col.SrcPos,
			))
		}
		for _, g := range col.Grants {
			ops = append(ops, colGrantOp(g, tblIdent, col.Name, col.SrcPos))
		}
		for _, r := range col.Revocations {
			ops = append(ops, colExplicitRevokeOp(r, tblIdent, col.Name, col.SrcPos))
		}
		ops = append(ops, createSecurityLabelOps(col.SecurityLabels, "COLUMN "+tblIdent+"."+quoteIdent(col.Name), col.SrcPos)...)
	}
	return ops
}

// indexCreate is how createIndex runs a CREATE INDEX (RFC Section 17.2).
type indexCreate int

const (
	// indexWithNewRelation: in the same transaction as the table's or
	// materialized view's own CREATE, so never concurrent, and SAFE since
	// the relation is empty. PostgreSQL rejects CREATE INDEX CONCURRENTLY
	// inside a transaction block.
	indexWithNewRelation indexCreate = iota
	// indexOnExisting: plain CREATE INDEX on an existing relation, CAUTION
	// (blocks writes while it builds).
	indexOnExisting
	// indexConcurrent: CREATE INDEX CONCURRENTLY on an existing relation,
	// CAUTION and non-transactional.
	indexConcurrent
)

// existingIndexCreate is the indexCreate for an index added to an existing
// relation: concurrent only when the source wrote CONCURRENTLY.
func existingIndexCreate(concurrent bool) indexCreate {
	if concurrent {
		return indexConcurrent
	}
	return indexOnExisting
}

// createIndex builds the CREATE INDEX statement for idx.
func createIndex(schema, table string, idx *ir.Index, how indexCreate) []pipeline.DiffOp {
	var b strings.Builder
	b.WriteString("CREATE ")
	if idx.Unique {
		b.WriteString("UNIQUE ")
	}
	b.WriteString("INDEX ")
	if how == indexConcurrent {
		b.WriteString("CONCURRENTLY ")
	}
	b.WriteString(quoteIdent(idx.Name))
	b.WriteString(" ON ")
	if idx.Only {
		b.WriteString("ONLY ")
	}
	b.WriteString(qualIdent(schema, table))
	if idx.Method != "" && idx.Method != "btree" {
		b.WriteString(" USING ")
		b.WriteString(idx.Method)
	}
	b.WriteString(" (")
	for i, col := range idx.Columns {
		if i > 0 {
			b.WriteString(", ")
		}
		if col.Name != "" {
			b.WriteString(quoteIdent(col.Name))
		} else if col.Expr != nil {
			b.WriteString("(")
			b.WriteString(col.Expr.Text)
			b.WriteString(")")
		}
		// COLLATE and opclass[(params)] come immediately after the column/
		// expression, before ASC/DESC/NULLS — real PostgreSQL's own fixed
		// index_elem clause order (confirmed live: "a DESC COLLATE x" is a
		// syntax error, "a COLLATE x DESC" is not).
		if col.Collation != nil {
			b.WriteString(" COLLATE ")
			b.WriteString(quoteIdent(col.Collation.Name))
		}
		if col.OpClass != nil {
			b.WriteString(" ")
			b.WriteString(col.OpClass.Name)
			if len(col.OpClassParams) > 0 {
				b.WriteString("(")
				for i, p := range col.OpClassParams {
					if i > 0 {
						b.WriteString(", ")
					}
					b.WriteString(p.Key)
					b.WriteString(" = ")
					b.WriteString(p.Value)
				}
				b.WriteString(")")
			}
		}
		if col.SortOrder != "" {
			b.WriteString(" ")
			b.WriteString(col.SortOrder)
		}
		if col.Nulls != "" {
			b.WriteString(" NULLS ")
			b.WriteString(col.Nulls)
		}
	}
	b.WriteString(")")
	// Covering (INCLUDE) columns and the partial-index predicate must be emitted
	// or the created index silently differs from the declared one.
	if len(idx.Include) > 0 {
		b.WriteString(" INCLUDE (")
		for i, c := range idx.Include {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(quoteIdent(c))
		}
		b.WriteString(")")
	}
	if idx.NullsNotDistinct {
		b.WriteString(" NULLS NOT DISTINCT")
	}
	if len(idx.With) > 0 {
		b.WriteString(" WITH (")
		for i, p := range idx.With {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(p.Key)
			b.WriteString("=")
			b.WriteString(p.Value)
		}
		b.WriteString(")")
	}
	if idx.Tablespace != nil {
		b.WriteString(" TABLESPACE ")
		b.WriteString(quoteIdent(*idx.Tablespace))
	}
	if idx.Where != nil && *idx.Where != "" {
		b.WriteString(" WHERE ")
		b.WriteString(*idx.Where)
	}
	b.WriteString(";")

	var ops []pipeline.DiffOp
	switch how {
	case indexConcurrent:
		ops = append(ops, nonTxnOp(pipeline.Caution, b.String(), idx.Pos))
	case indexOnExisting:
		ops = append(ops, cautionOp(b.String(), idx.Pos))
	default:
		ops = append(ops, safeOp(b.String(), idx.Pos))
	}
	if idx.Comment != nil {
		ops = append(ops, safeOp(
			fmt.Sprintf("COMMENT ON INDEX %s IS %s;", qualIdent(schema, idx.Name), quoteLit(*idx.Comment)),
			idx.Pos,
		))
	}
	return ops
}

func createPolicy(schema, table string, pol *ir.Policy) []pipeline.DiffOp {
	tbl := qualIdent(schema, table)
	var b strings.Builder
	b.WriteString("CREATE POLICY ")
	b.WriteString(quoteIdent(pol.Name))
	b.WriteString(" ON ")
	b.WriteString(tbl)
	if !pol.Permissive {
		b.WriteString(" AS RESTRICTIVE")
	}
	if pol.Command != "" && pol.Command != "ALL" {
		b.WriteString(" FOR ")
		b.WriteString(pol.Command)
	}
	if len(pol.Roles) > 0 {
		b.WriteString(" TO ")
		b.WriteString(roleList(pol.Roles))
	}
	if pol.Using != nil {
		b.WriteString(" USING (")
		b.WriteString(*pol.Using)
		b.WriteString(")")
	}
	if pol.WithCheck != nil {
		b.WriteString(" WITH CHECK (")
		b.WriteString(*pol.WithCheck)
		b.WriteString(")")
	}
	b.WriteString(";")
	ops := []pipeline.DiffOp{safeOp(b.String(), pol.Pos)}
	if pol.Comment != nil {
		ops = append(ops, safeOp(
			fmt.Sprintf("COMMENT ON POLICY %s ON %s IS %s;", quoteIdent(pol.Name), tbl, quoteLit(*pol.Comment)),
			pol.Pos,
		))
	}
	return ops
}

func createTrigger(schema, table string, trg *ir.Trigger) []pipeline.DiffOp {
	var b strings.Builder
	b.WriteString("CREATE ")
	if trg.Constraint {
		b.WriteString("CONSTRAINT ")
	}
	b.WriteString("TRIGGER ")
	b.WriteString(quoteIdent(trg.Name))
	b.WriteString(" ")
	b.WriteString(trg.When)
	b.WriteString(" ")
	b.WriteString(strings.Join(triggerEventClauses(trg), " OR "))
	b.WriteString(" ON ")
	b.WriteString(qualIdent(schema, table))
	if trg.ConstraintFrom != "" {
		b.WriteString(" FROM ")
		b.WriteString(quoteQualifiedIdent(trg.ConstraintFrom))
	}
	if trg.Deferrable {
		b.WriteString(" DEFERRABLE")
		if trg.InitiallyDeferred {
			b.WriteString(" INITIALLY DEFERRED")
		}
	}
	if trg.OldTransitionName != nil || trg.NewTransitionName != nil {
		b.WriteString(" REFERENCING")
		if trg.OldTransitionName != nil {
			b.WriteString(" OLD TABLE AS ")
			b.WriteString(quoteIdent(*trg.OldTransitionName))
		}
		if trg.NewTransitionName != nil {
			b.WriteString(" NEW TABLE AS ")
			b.WriteString(quoteIdent(*trg.NewTransitionName))
		}
	}
	b.WriteString(" FOR EACH ")
	b.WriteString(trg.ForEach)
	if trg.Condition != nil {
		b.WriteString(" WHEN (")
		b.WriteString(*trg.Condition)
		b.WriteString(")")
	}
	b.WriteString(" EXECUTE FUNCTION ")
	b.WriteString(trg.Function)
	b.WriteString("(")
	b.WriteString(strings.Join(trg.Args, ", "))
	b.WriteString(");")
	ops := []pipeline.DiffOp{safeOp(b.String(), trg.Pos)}
	if trg.Comment != nil {
		ops = append(ops, safeOp(
			fmt.Sprintf("COMMENT ON TRIGGER %s ON %s IS %s;", quoteIdent(trg.Name), qualIdent(schema, table), quoteLit(*trg.Comment)),
			trg.Pos,
		))
	}
	// trigger-enable-state (Section 7.9, audit item #56) has no inline
	// CREATE TRIGGER form either (confirmed empirically — a new trigger
	// is always ENABLED per real PostgreSQL's own grammar), only
	// ALTER TABLE, even at initial creation.
	if trg.EnableState != "" {
		ops = append(ops, safeOp(triggerEnableStateSQL(qualIdent(schema, table), trg.Name, trg.EnableState), trg.Pos))
	}
	// DEPENDS ON EXTENSION (Section 9.1, reused for triggers — Section
	// 7.9, audit item #75) has no inline CREATE TRIGGER form (confirmed
	// empirically — real PostgreSQL rejects it there outright, "syntax
	// error at or near DEPENDS"), only ALTER TRIGGER, even at initial
	// creation.
	for _, ext := range trg.DependsOnExtensions {
		ops = append(ops, safeOp(
			fmt.Sprintf("ALTER TRIGGER %s ON %s DEPENDS ON EXTENSION %s;", quoteIdent(trg.Name), qualIdent(schema, table), quoteIdent(ext)),
			trg.Pos,
		))
	}
	return ops
}

// qualifyRelForCompare qualifies an unqualified relation name with schema,
// so a name written unqualified in source compares equal to the
// schema-qualified name read from the catalog.
func qualifyRelForCompare(name, schema string) string {
	if name == "" || strings.Contains(name, ".") {
		return name
	}
	return schema + "." + name
}

// triggerEventClauses renders each of a trigger's events, attaching
// "OF col1, col2" to the UPDATE event when UpdateOfColumns is set (RFC
// audit item #1) — real PostgreSQL attaches the OF clause to the UPDATE
// keyword specifically, even within a combined "INSERT OR UPDATE OF col"
// event list, not to the trigger as a whole.
func triggerEventClauses(trg *ir.Trigger) []string {
	events := make([]string, len(trg.Events))
	copy(events, trg.Events)
	if len(trg.UpdateOfColumns) > 0 {
		cols := make([]string, len(trg.UpdateOfColumns))
		for i, c := range trg.UpdateOfColumns {
			cols[i] = quoteIdent(c)
		}
		for i, e := range events {
			if e == "UPDATE" {
				events[i] = "UPDATE OF " + strings.Join(cols, ", ")
			}
		}
	}
	return events
}

func tableGrantOp(g ir.Grant, tblIdent string, pos pipeline.SourcePos) *op {
	var privs string
	if len(g.Privileges) == 0 {
		privs = "ALL"
	} else {
		privs = strings.Join(g.Privileges, ", ")
	}
	sql := fmt.Sprintf("GRANT %s ON TABLE %s TO %s", privs, tblIdent, roleList(g.Roles))
	sql += grantOptSuffix(g)
	sql += ";"
	return safeOp(sql, pos)
}

func colGrantOp(g ir.Grant, tbl, col string, pos pipeline.SourcePos) pipeline.DiffOp {
	colIdent := quoteIdent(col)
	var privParts []string
	if len(g.Privileges) == 0 {
		privParts = []string{"ALL (" + colIdent + ")"}
	} else {
		for _, p := range g.Privileges {
			privParts = append(privParts, p+" ("+colIdent+")")
		}
	}
	sql := "GRANT " + strings.Join(privParts, ", ") + " ON TABLE " + tbl + " TO " + roleList(g.Roles)
	sql += grantOptSuffix(g)
	return safeOp(sql+";", pos)
}

// explicitRevokeOp emits the REVOKE for an explicit REVOKE directive
// (RFC Section 11.3) — distinct from the additive-model's implicit revoke (see
// colRevokeOp) that fires when a GRANT declaration is simply removed. Explicit
// revocations are CAUTION (not SAFE): they can break access that something
// else depends on. PG's REVOKE grammar has no IF EXISTS clause, so there's
// no guard to add regardless. onClause is the SQL object specifier after ON,
// e.g. "TABLE \"public\".\"users\"" or "FUNCTION \"public\".\"f\"(integer)".
func explicitRevokeOp(r ir.Revocation, onClause string, pos pipeline.SourcePos) *op {
	var privs string
	if len(r.Privileges) == 0 {
		privs = "ALL"
	} else {
		privs = strings.Join(r.Privileges, ", ")
	}
	sql := fmt.Sprintf("REVOKE %s ON %s FROM %s", privs, onClause, roleList(r.Roles))
	sql += revokeOptSuffix(r)
	sql += ";"
	return cautionOp(sql, pos)
}

// colExplicitRevokeOp is explicitRevokeOp's column-level counterpart,
// mirroring colGrantOp's column-parenthesized privilege syntax.
func colExplicitRevokeOp(r ir.Revocation, tbl, col string, pos pipeline.SourcePos) pipeline.DiffOp {
	colIdent := quoteIdent(col)
	var privParts []string
	if len(r.Privileges) == 0 {
		privParts = []string{"ALL (" + colIdent + ")"}
	} else {
		for _, p := range r.Privileges {
			privParts = append(privParts, p+" ("+colIdent+")")
		}
	}
	sql := "REVOKE " + strings.Join(privParts, ", ") + " ON TABLE " + tbl + " FROM " + roleList(r.Roles)
	sql += revokeOptSuffix(r)
	return cautionOp(sql+";", pos)
}

func colRevokeOp(sg snapshot.SnapGrant, tbl, col string, pos pipeline.SourcePos) pipeline.DiffOp {
	colIdent := quoteIdent(col)
	var privParts []string
	if len(sg.Privileges) == 0 {
		privParts = []string{"ALL (" + colIdent + ")"}
	} else {
		for _, p := range sg.Privileges {
			privParts = append(privParts, p+" ("+colIdent+")")
		}
	}
	// Implicit revoke — see the identical cautionOp reasoning in diffGrantSet
	// (RFC audit item #25).
	return cautionOp(
		"REVOKE "+strings.Join(privParts, ", ")+" ON TABLE "+tbl+" FROM "+roleList(sg.Roles)+";",
		pos,
	)
}

func diffColGrantSet(tbl, col string, snapGrants []snapshot.SnapGrant, desiredGrants []ir.Grant, pos pipeline.SourcePos) []pipeline.DiffOp {
	snapKeys := make(map[string]snapshot.SnapGrant, len(snapGrants))
	for _, g := range snapGrants {
		snapKeys[grantKey(g.Privileges, g.Roles, g.WithGrant)] = g
	}
	desiredKeys := make(map[string]ir.Grant, len(desiredGrants))
	for _, g := range desiredGrants {
		desiredKeys[grantKey(g.Privileges, g.Roles, g.WithGrant)] = g
	}
	var ops []pipeline.DiffOp
	for k, sg := range snapKeys {
		if _, ok := desiredKeys[k]; !ok {
			ops = append(ops, colRevokeOp(sg, tbl, col, pos))
		}
	}
	for k, g := range desiredKeys {
		if _, ok := snapKeys[k]; !ok {
			ops = append(ops, colGrantOp(g, tbl, col, pos))
		}
	}
	return ops
}

// diffColRevocationSet is diffRevocationSet's column-level counterpart,
// mirroring diffColGrantSet's structure for explicit REVOKES on a column.
func diffColRevocationSet(tbl, col string, snapRevs []snapshot.SnapGrant, desiredRevs []ir.Revocation, pos pipeline.SourcePos) []pipeline.DiffOp {
	snapKeys := make(map[string]snapshot.SnapGrant, len(snapRevs))
	for _, r := range snapRevs {
		snapKeys[grantKey(r.Privileges, r.Roles, false)] = r
	}
	desiredKeys := make(map[string]ir.Revocation, len(desiredRevs))
	for _, r := range desiredRevs {
		desiredKeys[grantKey(r.Privileges, r.Roles, false)] = r
	}
	var ops []pipeline.DiffOp
	for k, sr := range snapKeys {
		if _, ok := desiredKeys[k]; !ok {
			// Revocation removed from desired: re-grant to restore.
			ops = append(ops, colGrantOp(ir.Grant{Privileges: sr.Privileges, Roles: sr.Roles}, tbl, col, pos))
		}
	}
	for k, r := range desiredKeys {
		if _, ok := snapKeys[k]; !ok {
			ops = append(ops, colExplicitRevokeOp(r, tbl, col, pos))
		}
	}
	return ops
}

// viewHead renders a view's name, column-name list and WITH (...) options,
// the part of CREATE [OR REPLACE] VIEW between VIEW and AS.
func viewHead(o *ir.View) string {
	var b strings.Builder
	b.WriteString(qualIdent(o.Schema, o.Name))
	if len(o.Columns) > 0 {
		b.WriteString(" (")
		for i, c := range o.Columns {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(quoteIdent(c))
		}
		b.WriteString(")")
	}
	if len(o.StorageParams) > 0 {
		b.WriteString(" WITH (")
		for i, p := range o.StorageParams {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(p.Key)
			b.WriteString("=")
			b.WriteString(p.Value)
		}
		b.WriteString(")")
	}
	return b.String()
}

func createView(o *ir.View) []pipeline.DiffOp {
	var b strings.Builder
	b.WriteString("CREATE ")
	if o.Materialized {
		b.WriteString("MATERIALIZED ")
	}
	// o.Recursive deliberately does NOT get its own "RECURSIVE VIEW" DDL
	// keyword here: pg_query's deparse of a RECURSIVE VIEW's query already
	// returns a self-contained "WITH RECURSIVE ..." CTE (confirmed live —
	// PostgreSQL desugars CREATE RECURSIVE VIEW into exactly this form
	// internally), which is valid, self-sufficient recursion under a plain
	// CREATE VIEW/CREATE MATERIALIZED VIEW — same reasoning already used by
	// dump's identical View-rendering case. Emitting "CREATE RECURSIVE
	// VIEW ... AS WITH RECURSIVE ..." is a real PostgreSQL syntax error
	// (SQLSTATE 42601): CREATE RECURSIVE VIEW's own grammar expects a bare
	// query, not one already wrapped in WITH RECURSIVE, and additionally
	// requires an explicit column list DPG doesn't track separately from Query.
	b.WriteString("VIEW ")
	b.WriteString(viewHead(o))
	if o.Materialized && o.Tablespace != nil {
		b.WriteString(" TABLESPACE ")
		b.WriteString(quoteIdent(*o.Tablespace))
	}
	b.WriteString(" AS ")
	// Strip trailing semicolons from the query — we control the final delimiter.
	b.WriteString(strings.TrimRight(strings.TrimSpace(o.Query), ";"))
	if o.Materialized && o.WithNoData {
		b.WriteString(" WITH NO DATA")
	}
	b.WriteString(";")
	ops := wrapCreateWithOwner(safeOp(b.String(), o.SrcPos), o.Owner, o.SrcPos)
	viewKind := "VIEW"
	if o.Materialized {
		viewKind = "MATERIALIZED VIEW"
	}
	if txt := effectiveComment(o.Comment, o.Deprecated); txt != nil {
		ops = append(ops, safeOp(
			fmt.Sprintf("COMMENT ON %s %s IS %s;", viewKind, qualIdent(o.Schema, o.Name), quoteLit(*txt)),
			o.SrcPos,
		))
	}
	viewIdent := qualIdent(o.Schema, o.Name)
	for _, g := range o.Grants {
		sql := fmt.Sprintf("GRANT %s ON TABLE %s TO %s", privStr(g.Privileges), viewIdent, roleList(g.Roles))
		sql += grantOptSuffix(g)
		ops = append(ops, safeOp(sql+";", o.SrcPos))
	}
	for _, r := range o.Revocations {
		ops = append(ops, explicitRevokeOp(r, "TABLE "+viewIdent, o.SrcPos))
	}
	ops = append(ops, createSecurityLabelOps(o.SecurityLabels, viewKind+" "+viewIdent, o.SrcPos)...)
	for _, idx := range o.Indexes {
		// Always non-concurrent — this view doesn't exist yet, so its
		// indexes are created in the same transactional migration as the
		// CREATE MATERIALIZED VIEW itself, matching createTable's identical
		// reasoning for a brand-new table's indexes.
		ops = append(ops, createIndex(o.Schema, o.Name, idx, indexWithNewRelation)...)
	}
	return ops
}

func createFunction(o *ir.Function) []pipeline.DiffOp {
	ops := []pipeline.DiffOp{safeOp(buildFunctionSQL(o), o.SrcPos)}
	if o.Owner != nil {
		ops = wrapCreateWithOwner(ops[0], o.Owner, o.SrcPos)
	}
	sig := buildFuncSignature(o)
	if txt := effectiveComment(o.Comment, o.Deprecated); txt != nil {
		ops = append(ops, safeOp(
			fmt.Sprintf("COMMENT ON FUNCTION %s IS %s;", sig, quoteLit(*txt)),
			o.SrcPos,
		))
	}
	for _, g := range o.Grants {
		sql := fmt.Sprintf("GRANT %s ON FUNCTION %s TO %s", privStr(g.Privileges), sig, roleList(g.Roles))
		sql += grantOptSuffix(g)
		ops = append(ops, safeOp(sql+";", o.SrcPos))
	}
	for _, r := range o.Revocations {
		ops = append(ops, explicitRevokeOp(r, "FUNCTION "+sig, o.SrcPos))
	}
	ops = append(ops, createSecurityLabelOps(o.SecurityLabels, "FUNCTION "+sig, o.SrcPos)...)
	// DEPENDS ON EXTENSION (Section 9.1) has no inline CREATE FUNCTION form
	// (confirmed empirically — real PostgreSQL rejects it there outright,
	// "syntax error at or near DEPENDS"), only ALTER FUNCTION, even at
	// initial creation.
	for _, ext := range o.DependsOnExtensions {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER FUNCTION %s DEPENDS ON EXTENSION %s;", sig, quoteIdent(ext)), o.SrcPos))
	}
	return ops
}

func buildFuncSignature(o *ir.Function) string {
	return fmt.Sprintf("%s(%s)", qualIdent(o.Schema, o.Name), ir.ArgsKey(o.Args))
}

func buildFunctionSQL(o *ir.Function) string {
	return ir.RenderCreateFunctionSQL(o)
}

func createType(o *ir.Type) []pipeline.DiffOp {
	var ops []pipeline.DiffOp
	switch o.Variant {
	case "ENUM":
		var b strings.Builder
		b.WriteString("CREATE TYPE ")
		b.WriteString(qualIdent(o.Schema, o.Name))
		b.WriteString(" AS ENUM (")
		for i, v := range o.EnumValues {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(quoteLit(v))
		}
		b.WriteString(");")
		ops = append(ops, safeOp(b.String(), o.SrcPos))
	case "COMPOSITE":
		ops = append(ops, safeOp(buildCompositeTypeSQL(o), o.SrcPos))
	case "DOMAIN":
		ops = append(ops, wrapCreateWithOwner(safeOp(buildDomainSQL(o), o.SrcPos), o.Owner, o.SrcPos)...)
		if o.Comment != nil {
			ops = append(ops, safeOp(
				fmt.Sprintf("COMMENT ON DOMAIN %s IS %s;", qualIdent(o.Schema, o.Name), quoteLit(*o.Comment)),
				o.SrcPos,
			))
		}
		ops = append(ops, createTypeGrantOps(o)...)
		ops = append(ops, createSecurityLabelOps(o.SecurityLabels, "DOMAIN "+qualIdent(o.Schema, o.Name), o.SrcPos)...)
		return ops
	default:
		if o.Body != "" {
			ops = append(ops, safeOp(o.Body+";", o.SrcPos))
		}
	}
	if o.Owner != nil && len(ops) > 0 {
		ops = append(wrapCreateWithOwner(ops[0], o.Owner, o.SrcPos), ops[1:]...)
	}
	if o.Comment != nil {
		ops = append(ops, safeOp(
			fmt.Sprintf("COMMENT ON TYPE %s IS %s;", qualIdent(o.Schema, o.Name), quoteLit(*o.Comment)),
			o.SrcPos,
		))
	}
	if o.Variant == "COMPOSITE" {
		typeIdent := qualIdent(o.Schema, o.Name)
		for _, attr := range o.CompositeAttrs {
			ops = append(ops, compositeAttrCommentOps(typeIdent, attr, nil)...)
		}
	}
	ops = append(ops, createTypeGrantOps(o)...)
	ops = append(ops, createSecurityLabelOps(o.SecurityLabels, "TYPE "+qualIdent(o.Schema, o.Name), o.SrcPos)...)
	return ops
}

// diffTypeGrantOps diffs a type's Grants/Revocations against its snapshot
// (RFC audit item #3, uniform across all 5 variants — see createTypeGrantOps'
// identical reasoning).
func diffTypeGrantOps(o *ir.Type, snap *snapshot.SnapType, typeIdent string, pos pipeline.SourcePos) []pipeline.DiffOp {
	var ops []pipeline.DiffOp
	ops = append(ops, diffGrantSet(snap.Grants, o.Grants, "TYPE "+typeIdent, pos)...)
	ops = append(ops, diffRevocationSet(snap.Revocations, o.Revocations, "TYPE "+typeIdent, pos)...)
	return ops
}

// createTypeGrantOps emits GRANT/REVOKE ops for a newly-created type (RFC
// audit item #3, uniform across all 5 variants — real PostgreSQL's GRANT/
// REVOKE has no separate "ON DOMAIN" target, a domain is granted exactly
// like any other type via "... ON TYPE domain_name ..." — confirmed live).
func createTypeGrantOps(o *ir.Type) []pipeline.DiffOp {
	var ops []pipeline.DiffOp
	typeIdent := qualIdent(o.Schema, o.Name)
	for _, g := range o.Grants {
		sql := fmt.Sprintf("GRANT %s ON TYPE %s TO %s", privStr(g.Privileges), typeIdent, roleList(g.Roles))
		sql += grantOptSuffix(g)
		ops = append(ops, safeOp(sql+";", o.SrcPos))
	}
	for _, r := range o.Revocations {
		ops = append(ops, explicitRevokeOp(r, "TYPE "+typeIdent, o.SrcPos))
	}
	return ops
}

// createSimpleGrantOps emits GRANT/REVOKE ops for a newly-created object
// whose GRANT object-kind keyword is fixed and whose identifier is already
// a bare quoted name (no schema qualification) — Tablespace/FDW/
// ForeignServer (RFC audit items #4/#5/#6). manual must be true for
// Tablespace specifically: CREATE/DROP TABLESPACE cannot run inside a
// transaction block (see createOpaque's identical reasoning), and Emit
// splits ops into a transactional batch that runs BEFORE the non-
// transactional batch — a transactional (safeOp) GRANT ON TABLESPACE would
// therefore execute before the manual CREATE TABLESPACE it depends on, live-
// failing with "tablespace ... does not exist". FDW/ForeignServer have no
// such restriction and pass false.
func createSimpleGrantOps(grantKind, ident string, grants []ir.Grant, revocations []ir.Revocation, pos pipeline.SourcePos, manual bool) []pipeline.DiffOp {
	var ops []pipeline.DiffOp
	for _, g := range grants {
		sql := fmt.Sprintf("GRANT %s ON %s %s TO %s", privStr(g.Privileges), grantKind, ident, roleList(g.Roles))
		sql += grantOptSuffix(g)
		if manual {
			ops = append(ops, nonTxnOp(pipeline.Safe, sql+";", pos))
		} else {
			ops = append(ops, safeOp(sql+";", pos))
		}
	}
	for _, r := range revocations {
		ro := explicitRevokeOp(r, grantKind+" "+ident, pos)
		if manual {
			ro.mode = pipeline.ModeNonTransactional
		}
		ops = append(ops, ro)
	}
	return ops
}

// compositeAttrCommentOps emits COMMENT ON COLUMN for a composite type
// attribute (RFC Section 5.2's ATTRIBUTE { COMMENT '...'; }) when the
// desired comment differs from snapComment. PostgreSQL comments on a
// composite type attribute through COMMENT ON COLUMN type.attr.
func compositeAttrCommentOps(typeIdent string, a *ir.Column, snapComment *string) []pipeline.DiffOp {
	if ptrEq(a.Comment, snapComment) {
		return nil
	}
	if a.Comment != nil {
		return []pipeline.DiffOp{safeOp(
			fmt.Sprintf("COMMENT ON COLUMN %s.%s IS %s;", typeIdent, quoteIdent(a.Name), quoteLit(*a.Comment)),
			a.SrcPos,
		)}
	}
	return []pipeline.DiffOp{safeOp(
		fmt.Sprintf("COMMENT ON COLUMN %s.%s IS NULL;", typeIdent, quoteIdent(a.Name)),
		a.SrcPos,
	)}
}

func buildCompositeTypeSQL(o *ir.Type) string {
	var b strings.Builder
	b.WriteString("CREATE TYPE ")
	b.WriteString(qualIdent(o.Schema, o.Name))
	b.WriteString(" AS (")
	for i, attr := range o.CompositeAttrs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(quoteIdent(attr.Name))
		b.WriteString(" ")
		b.WriteString(attr.Type.String())
		if attr.Collation != "" {
			b.WriteString(" COLLATE ")
			b.WriteString(quoteQualifiedIdent(attr.Collation))
		}
	}
	b.WriteString(");")
	return b.String()
}

// buildDomainSQL builds a full CREATE DOMAIN statement from ir.Type's
// structured domain fields — never from o.Body, which (for a domain
// declared via RFC Section 5.4's block syntax) may only ever have captured the
// bare "name AS basetype" from Part 1, missing any DEFAULT/CONSTRAINT/NOT
// NULL the user put in the { } block instead. Building from the structured
// fields is correct regardless of which syntax (real PG's own inline form,
// the RFC's block form, or a mix) the user wrote.
func buildDomainSQL(o *ir.Type) string {
	var b strings.Builder
	b.WriteString("CREATE DOMAIN ")
	b.WriteString(qualIdent(o.Schema, o.Name))
	b.WriteString(" AS ")
	b.WriteString(o.DomainBaseType.String())
	if o.DomainCollation != "" {
		b.WriteString(" COLLATE ")
		b.WriteString(quoteQualifiedIdent(o.DomainCollation))
	}
	if o.DomainDefault != nil {
		b.WriteString(" DEFAULT ")
		b.WriteString(*o.DomainDefault)
	}
	if o.DomainNotNull {
		b.WriteString(" NOT NULL")
	}
	for _, cst := range o.DomainConstraints {
		b.WriteString(" ")
		if cst.Name != "" {
			b.WriteString("CONSTRAINT ")
			b.WriteString(quoteIdent(cst.Name))
			b.WriteString(" ")
		}
		// NOT VALID is deliberately never appended here: confirmed
		// empirically (direct pg_query parse) that real PostgreSQL's
		// CREATE DOMAIN grammar rejects an inline NOT VALID on its
		// constraint clause outright ("syntax error at or near VALID") —
		// unlike CREATE TABLE, which does accept it inline. A domain
		// constraint can only ever be added NOT VALID via a follow-up
		// ALTER DOMAIN ... ADD CONSTRAINT (see the diffType DOMAIN
		// branch), never at creation time.
		b.WriteString(cst.Expr)
	}
	b.WriteString(";")
	return b.String()
}

func createSequence(o *ir.Sequence) []pipeline.DiffOp {
	ident := qualIdent(o.Schema, o.Name)
	var b strings.Builder
	b.WriteString("CREATE ")
	if o.Unlogged {
		b.WriteString("UNLOGGED ")
	}
	b.WriteString("SEQUENCE IF NOT EXISTS ")
	b.WriteString(ident)
	if o.AsType != nil {
		// AS type must come first, right after the sequence name, per real
		// PostgreSQL's CREATE SEQUENCE grammar — kept out of writeSeqParams
		// (shared with ALTER SEQUENCE) since a changed AS type is never
		// altered in place (RFC audit item #14: DROP + CREATE, DESTRUCTIVE),
		// only ever emitted here at creation time.
		fmt.Fprintf(&b, " AS %s", o.AsType.String())
	}
	writeSeqParams(&b, o)
	b.WriteString(";")
	ops := wrapCreateWithOwner(safeOp(b.String(), o.SrcPos), o.Owner, o.SrcPos)
	if o.Comment != nil {
		ops = append(ops, safeOp(
			fmt.Sprintf("COMMENT ON SEQUENCE %s IS %s;", ident, quoteLit(*o.Comment)),
			o.SrcPos,
		))
	}
	for _, g := range o.Grants {
		sql := fmt.Sprintf("GRANT %s ON SEQUENCE %s TO %s", privStr(g.Privileges), ident, roleList(g.Roles))
		sql += grantOptSuffix(g)
		ops = append(ops, safeOp(sql+";", o.SrcPos))
	}
	for _, r := range o.Revocations {
		ops = append(ops, explicitRevokeOp(r, "SEQUENCE "+ident, o.SrcPos))
	}
	ops = append(ops, createSecurityLabelOps(o.SecurityLabels, "SEQUENCE "+ident, o.SrcPos)...)
	if o.Restart {
		ops = append(ops, sequenceRestartOp(ident, o))
	}
	return ops
}

// sequenceRestartOp builds the imperative, non-persisted RESTART op — see
// Sequence.Restart's doc comment. Caution since it's a one-shot action
// PostgreSQL itself recommends removing from source after applying, not a
// steady-state property like every other sequence option.
func sequenceRestartOp(ident string, o *ir.Sequence) pipeline.DiffOp {
	sql := fmt.Sprintf("ALTER SEQUENCE %s RESTART", ident)
	if o.RestartWith != nil {
		sql += fmt.Sprintf(" WITH %d", *o.RestartWith)
	}
	return cautionOp(sql+";", o.SrcPos)
}

// writeSeqParams appends explicit sequence parameters to b for any non-nil fields.
func writeSeqParams(b *strings.Builder, o *ir.Sequence) {
	if o.IncrementBy != nil {
		fmt.Fprintf(b, " INCREMENT BY %d", *o.IncrementBy)
	}
	if o.NoMinValue {
		// Explicit "NO MINVALUE" must be emitted, not silenced, the same
		// reasoning as NO CYCLE below — ALTER SEQUENCE never resets an
		// omitted option to a default (RFC audit item #20).
		b.WriteString(" NO MINVALUE")
	} else if o.MinValue != nil {
		fmt.Fprintf(b, " MINVALUE %d", *o.MinValue)
	}
	if o.NoMaxValue {
		b.WriteString(" NO MAXVALUE")
	} else if o.MaxValue != nil {
		fmt.Fprintf(b, " MAXVALUE %d", *o.MaxValue)
	}
	if o.StartValue != nil {
		fmt.Fprintf(b, " START WITH %d", *o.StartValue)
	}
	if o.Cache != nil {
		fmt.Fprintf(b, " CACHE %d", *o.Cache)
	}
	if o.Cycle != nil {
		// Unlike CREATE SEQUENCE (where an omitted option resets to the
		// PostgreSQL default), ALTER SEQUENCE leaves any option not named in
		// the statement untouched — so toggling CYCLE off must emit an
		// explicit "NO CYCLE", not silence, or the live sequence keeps
		// cycling forever with the drift invisible to future diffs (RFC
		// audit item #21).
		if *o.Cycle {
			b.WriteString(" CYCLE")
		} else {
			b.WriteString(" NO CYCLE")
		}
	}
	if o.OwnedBy != nil {
		// RFC audit item #14: OWNED BY, SAFE per the RFC's diffing table —
		// unlike AS type, this is a normal ALTER SEQUENCE clause, valid in
		// both CREATE and ALTER SEQUENCE grammar, so it belongs in the
		// shared param writer.
		if *o.OwnedBy == "NONE" {
			b.WriteString(" OWNED BY NONE")
		} else {
			fmt.Fprintf(b, " OWNED BY %s", quoteQualifiedIdent(*o.OwnedBy))
		}
	}
}

// writeIdentityOpts appends a parenthesized identity-opts clause (RFC
// Section 7.2) for any non-nil option on id — "" (nothing written) when no
// option was declared. Order follows the RFC's own identity-opts ABNF
// (START WITH, INCREMENT BY, MINVALUE/NO MINVALUE, MAXVALUE/NO MAXVALUE,
// CACHE, CYCLE/NO CYCLE); live-confirmed real PostgreSQL accepts identity
// sequence options in any order, so this ordering is a rendering choice,
// not a grammar requirement.
func writeIdentityOpts(b *strings.Builder, id *ir.Identity) {
	var opts strings.Builder
	if id.StartValue != nil {
		fmt.Fprintf(&opts, " START WITH %d", *id.StartValue)
	}
	if id.IncrementBy != nil {
		fmt.Fprintf(&opts, " INCREMENT BY %d", *id.IncrementBy)
	}
	if id.NoMinValue {
		opts.WriteString(" NO MINVALUE")
	} else if id.MinValue != nil {
		fmt.Fprintf(&opts, " MINVALUE %d", *id.MinValue)
	}
	if id.NoMaxValue {
		opts.WriteString(" NO MAXVALUE")
	} else if id.MaxValue != nil {
		fmt.Fprintf(&opts, " MAXVALUE %d", *id.MaxValue)
	}
	if id.Cache != nil {
		fmt.Fprintf(&opts, " CACHE %d", *id.Cache)
	}
	if id.Cycle != nil {
		if *id.Cycle {
			opts.WriteString(" CYCLE")
		} else {
			opts.WriteString(" NO CYCLE")
		}
	}
	if opts.Len() == 0 {
		return
	}
	b.WriteString(" (")
	b.WriteString(strings.TrimSpace(opts.String()))
	b.WriteString(")")
}

// quoteQualifiedIdent quotes each dot-separated part of a qualified
// identifier independently (e.g. "orders.order_number" ->
// "\"orders\".\"order_number\"") — used for Sequence.OwnedBy's table.column
// reference (RFC audit item #14), which the builder stores as plain,
// unquoted dotted text.
func quoteQualifiedIdent(s string) string {
	parts := strings.Split(s, ".")
	for i, p := range parts {
		parts[i] = quoteIdent(p)
	}
	return strings.Join(parts, ".")
}

// writeRoleBoolOpt writes " ON" or " OFF" (PostgreSQL's toggle-pair keywords)
// to b if v is non-nil, nothing otherwise — mirrors RFC Section 11.1's "any option
// a declaration omits is simply not managed by DPG" rule at the SQL-building
// level.
func writeRoleBoolOpt(b *strings.Builder, v *bool, on, off string) {
	if v == nil {
		return
	}
	if *v {
		b.WriteString(" " + on)
	} else {
		b.WriteString(" " + off)
	}
}

// roleAttrClause builds the "<opt1> <opt2> ..." portion of a CREATE
// ROLE/ALTER ROLE statement for every non-PASSWORD, non-membership
// attribute the declaration sets (RFC Section 11.1) — valid in both statement
// forms, unlike IN ROLE/ROLE/ADMIN (create-time only) and unlike PASSWORD
// (kept separate so callers can pass either the raw declared text or an
// apply-time-resolved value; nil password omits the clause entirely).
func roleAttrClause(o *ir.Role, password *string) string {
	var b strings.Builder
	writeRoleBoolOpt(&b, o.CanLogin, "LOGIN", "NOLOGIN")
	writeRoleBoolOpt(&b, o.Superuser, "SUPERUSER", "NOSUPERUSER")
	writeRoleBoolOpt(&b, o.CreateDB, "CREATEDB", "NOCREATEDB")
	writeRoleBoolOpt(&b, o.CreateRole, "CREATEROLE", "NOCREATEROLE")
	writeRoleBoolOpt(&b, o.Inherit, "INHERIT", "NOINHERIT")
	writeRoleBoolOpt(&b, o.IsReplication, "REPLICATION", "NOREPLICATION")
	writeRoleBoolOpt(&b, o.BypassRLS, "BYPASSRLS", "NOBYPASSRLS")
	if o.ConnectionLimit != nil {
		fmt.Fprintf(&b, " CONNECTION LIMIT %d", *o.ConnectionLimit)
	}
	if password != nil {
		b.WriteString(" PASSWORD " + quoteLit(*password))
	}
	if o.ValidUntil != nil {
		b.WriteString(" VALID UNTIL " + quoteLit(*o.ValidUntil))
	}
	return b.String()
}

// buildCreateRoleSQL builds a full CREATE ROLE statement, including the
// create-time-only IN ROLE/ROLE/ADMIN membership clauses (RFC Section 11.1) —
// PostgreSQL's ALTER ROLE has no membership clause at all, so this shape
// (attrs + membership in one CREATE) is only ever used at creation time;
// later membership changes are diffed as GRANT/REVOKE (see
// diffRoleMembership). password is passed separately from o.Password so
// callers can build either the display form (raw declared text, may
// contain {{...}}) or the apply-time exec form (resolved value).
func buildCreateRoleSQL(o *ir.Role, password *string) string {
	var b strings.Builder
	b.WriteString("CREATE ROLE ")
	b.WriteString(quoteIdent(o.Name))
	b.WriteString(roleAttrClause(o, password))
	// Only bare entries (no WITH INHERIT/SET — real CREATE ROLE's inline
	// IN ROLE/ROLE/ADMIN lists accept no WITH modifiers at all, confirmed
	// live) render inline here; anything needing INHERIT/SET goes through
	// its own follow-up GRANT in createRole, same treatment as RoleConfigs.
	var inRole, roleMembers, adminRoles []string
	for _, m := range o.Memberships {
		if m.Inherit != nil || m.Set != nil {
			continue
		}
		switch {
		case m.Direction == "IN_ROLE":
			inRole = append(inRole, m.Role)
		case m.Admin:
			adminRoles = append(adminRoles, m.Role)
		default:
			roleMembers = append(roleMembers, m.Role)
		}
	}
	if len(inRole) > 0 {
		b.WriteString(" IN ROLE " + roleList(inRole))
	}
	if len(roleMembers) > 0 {
		b.WriteString(" ROLE " + roleList(roleMembers))
	}
	if len(adminRoles) > 0 {
		b.WriteString(" ADMIN " + roleList(adminRoles))
	}
	b.WriteString(";")
	return b.String()
}

// boolSQL renders a Go bool as the bare TRUE/FALSE keyword real PostgreSQL's
// GrantRoleStmt WITH-option grammar expects (confirmed live) — not a quoted
// string literal.
func boolSQL(v bool) string {
	if v {
		return "TRUE"
	}
	return "FALSE"
}

// roleMembershipGrantSQL renders one RoleMembership entry as a GRANT ...
// TO ... [WITH ...]; statement (RFC audit item #32, Section 11.1).
// Direction decides which of roleName/m.Role is the granted role and which
// is the member: "IN_ROLE" grants m.Role to roleName (this role becomes a
// member of m.Role); "ROLE" grants roleName to m.Role (m.Role becomes a
// member of this role) — the same real PostgreSQL GRANT/REVOKE ROLE
// mechanism underlies both directions, only the argument order differs.
func roleMembershipGrantSQL(roleName string, m ir.RoleMembership) string {
	grantedRole, memberRole := roleName, m.Role
	if m.Direction == "IN_ROLE" {
		grantedRole, memberRole = m.Role, roleName
	}
	var b strings.Builder
	b.WriteString("GRANT ")
	b.WriteString(quoteIdent(grantedRole))
	b.WriteString(" TO ")
	b.WriteString(quoteIdent(memberRole))
	// Real PostgreSQL's GrantRoleStmt WITH clause is a single "WITH" keyword
	// followed by a comma-separated option list — confirmed live via
	// pg_query.Parse that repeated "WITH x WITH y" clauses are a syntax
	// error, unlike this same codebase's table/column-level GRANT (whose
	// only WITH option, WITH GRANT OPTION, never had a second option to
	// combine with, so this shape never came up there).
	var opts []string
	if m.Admin {
		opts = append(opts, "ADMIN OPTION")
	}
	if m.Inherit != nil {
		opts = append(opts, "INHERIT "+boolSQL(*m.Inherit))
	}
	if m.Set != nil {
		opts = append(opts, "SET "+boolSQL(*m.Set))
	}
	if len(opts) > 0 {
		b.WriteString(" WITH ")
		b.WriteString(strings.Join(opts, ", "))
	}
	b.WriteString(";")
	return b.String()
}

// roleMembershipRevokeSQL is roleMembershipGrantSQL's REVOKE counterpart —
// RFC's diffing table documents only a full-membership REVOKE for a removed
// entry, no partial "REVOKE ADMIN OPTION FOR ... FROM ..." support (matches
// this codebase's pre-unification behavior, which never supported it
// either).
func roleMembershipRevokeSQL(roleName string, m ir.RoleMembership) string {
	grantedRole, memberRole := roleName, m.Role
	if m.Direction == "IN_ROLE" {
		grantedRole, memberRole = m.Role, roleName
	}
	return fmt.Sprintf("REVOKE %s FROM %s;", quoteIdent(grantedRole), quoteIdent(memberRole))
}

// roleCreateOp is the DiffOp for a CREATE ROLE statement whose PASSWORD
// option may hold an unresolved secret reference (RFC Section 11.1). SQL() always
// returns the placeholder/declared form — used for plan output,
// migration-file archival, and error messages, so a resolved password is
// never persisted or logged. ExecSQL rebuilds the statement with PASSWORD's
// resolved value; unlike Subscription's subscriptionCreateOp, this doesn't
// need regex substitution into deparsed text — Role is built from
// structured fields on both the display and exec paths, so ExecSQL just
// calls the same builder with a different password string.
type roleCreateOp struct {
	*op
	role *ir.Role
}

func (o *roleCreateOp) ExecSQL(resolver pipeline.SecretResolver) (string, error) {
	resolved, err := pipeline.ResolveTemplate(*o.role.Password, resolver)
	if err != nil {
		return "", err
	}
	return buildCreateRoleSQL(o.role, &resolved), nil
}

var _ pipeline.SecretBearingOp = (*roleCreateOp)(nil)

func createRole(o *ir.Role) []pipeline.DiffOp {
	var ops []pipeline.DiffOp
	displaySQL := buildCreateRoleSQL(o, o.Password)
	if o.Password != nil {
		ops = append(ops, &roleCreateOp{op: safeOp(displaySQL, o.SrcPos), role: o})
	} else {
		ops = append(ops, safeOp(displaySQL, o.SrcPos))
	}
	if o.Comment != nil {
		ops = append(ops, safeOp(
			fmt.Sprintf("COMMENT ON ROLE %s IS %s;", quoteIdent(o.Name), quoteLit(*o.Comment)),
			o.SrcPos,
		))
	}
	ident := quoteIdent(o.Name)
	for _, c := range o.Configs {
		// RFC audit item #74: real CREATE ROLE has no inline SET/RESET
		// clause at all (confirmed live) — every entry, even for a
		// brand-new role, needs its own follow-up ALTER ROLE.
		ops = append(ops, safeOp(roleConfigSQL(ident, c), o.SrcPos))
	}
	for _, m := range o.Memberships {
		// RFC audit item #32: only bare entries (no WITH INHERIT/SET) were
		// already rendered inline by buildCreateRoleSQL above — anything
		// needing those modifiers needs its own follow-up GRANT, the same
		// treatment as RoleConfigs.
		if m.Inherit != nil || m.Set != nil {
			ops = append(ops, safeOp(roleMembershipGrantSQL(o.Name, m), o.SrcPos))
		}
	}
	ops = append(ops, createSecurityLabelOps(o.SecurityLabels, "ROLE "+quoteIdent(o.Name), o.SrcPos)...)
	return ops
}

// ── DIFF / ALTER operations ───────────────────────────────────────────────────

// skipDetachedChildren, only meaningful for the *ir.Table case, is Pass
// 2.5's per-parent set of child partition names being detached this run
// (see Diff's own doc comment) — nil everywhere else.
func diffObject(desired pipeline.IRObject, snap *snapshot.SnapObject, fullSnap *pipeline.Snapshot, env *diffEnv, skipDetachedChildren map[string]bool) ([]pipeline.DiffOp, error) {
	switch o := desired.(type) {
	case *ir.Extension:
		if snap.Extension == nil {
			return nil, nil
		}
		return diffExtension(o, snap.Extension), nil
	case *ir.Sequence:
		if snap.Sequence == nil {
			return nil, nil
		}
		return diffSequence(o, snap.Sequence), nil
	case *ir.Role:
		if snap.Role == nil {
			return nil, nil
		}
		return diffRole(o, snap.Role, fullSnap.Live), nil
	case *ir.Schema:
		if snap.Schema == nil {
			return nil, nil
		}
		return diffSchema(o, snap.Schema), nil
	case *ir.Table:
		if snap.Table == nil {
			return nil, nil
		}
		return diffTable(o, snap.Table, fullSnap, env, skipDetachedChildren)
	case *ir.View:
		if snap.View == nil {
			return nil, nil
		}
		return diffView(o, snap.View)
	case *ir.Function:
		if snap.Function == nil {
			return nil, nil
		}
		return diffFunction(o, snap.Function), nil
	case *ir.Type:
		if snap.Type == nil {
			return nil, nil
		}
		return diffType(o, snap.Type, fullSnap)
	case *ir.Procedure:
		if snap.Opaque == nil {
			return nil, nil
		}
		return diffProcedure(o, snap.Opaque)
	case *ir.Aggregate:
		if snap.Opaque == nil {
			return nil, nil
		}
		return diffAggregate(o, snap.Opaque)
	case *ir.Tablespace:
		if snap.Opaque == nil {
			return nil, nil
		}
		return diffTablespace(o, snap.Opaque)
	case *ir.ForeignDataWrapper:
		if snap.Opaque == nil {
			return nil, nil
		}
		return diffFDW(o, snap.Opaque)
	case *ir.ForeignServer:
		if snap.Opaque == nil {
			return nil, nil
		}
		return diffForeignServer(o, snap.Opaque)
	case *ir.UserMapping:
		if snap.Opaque == nil {
			return nil, nil
		}
		return diffUserMapping(o, snap.Opaque)
	case *ir.Publication:
		if snap.Opaque == nil {
			return nil, nil
		}
		return diffPublication(o, snap.Opaque)
	case *ir.Subscription:
		if snap.Opaque == nil {
			return nil, nil
		}
		return diffSubscription(o, snap.Opaque)
	case *ir.EventTrigger:
		if snap.Opaque == nil {
			return nil, nil
		}
		return diffEventTrigger(o, snap.Opaque)
	case *ir.Collation:
		if snap.Opaque == nil {
			return nil, nil
		}
		return diffCollation(o, snap.Opaque)
	case *ir.Operator:
		if snap.Opaque == nil {
			return nil, nil
		}
		return diffOperator(o, snap.Opaque)
	case *ir.OperatorClass:
		if snap.Opaque == nil {
			return nil, nil
		}
		return diffOperatorClass(o, snap.Opaque)
	case *ir.OperatorFamily:
		if snap.Opaque == nil {
			return nil, nil
		}
		return diffOperatorFamily(o, snap.Opaque)
	case *ir.Cast:
		if snap.Opaque == nil {
			return nil, nil
		}
		return diffCast(o, snap.Opaque)
	case *ir.StatisticsObject:
		if snap.Opaque == nil {
			return nil, nil
		}
		return diffStatisticsObject(o, snap.Opaque)
	case *ir.TSConfig:
		if snap.Opaque == nil {
			return nil, nil
		}
		return diffTSConfig(o, snap.Opaque)
	case *ir.TSDict:
		if snap.Opaque == nil {
			return nil, nil
		}
		return diffTSDict(o, snap.Opaque)
	case *ir.TSParser:
		if snap.Opaque == nil {
			return nil, nil
		}
		return diffTSParser(o, snap.Opaque)
	case *ir.TSTemplate:
		if snap.Opaque == nil {
			return nil, nil
		}
		return diffTSTemplate(o, snap.Opaque)
	case *ir.DefaultPrivileges:
		if snap.DefaultPrivileges == nil {
			return nil, nil
		}
		return diffDefaultPrivileges(o, snap.DefaultPrivileges), nil
	case *ir.ParameterPrivileges:
		if snap.ParameterPrivileges == nil {
			return nil, nil
		}
		return diffParameterPrivileges(o, snap.ParameterPrivileges), nil
	case *ir.VirtualType:
		// Virtual types are DPG-only annotations; no SQL is generated on change.
		return nil, nil
	}
	return nil, nil
}

// accessMethodOrDefault returns the recorded index access method for an
// operator class/family, falling back to "btree" for snapshots written before
// the access method was tracked (the field is absent in legacy snapshot JSON).
func accessMethodOrDefault(am string) string {
	if am == "" {
		return "btree"
	}
	return am
}

// tsMappingAlterSQL builds "ALTER TEXT SEARCH CONFIGURATION cfg ALTER MAPPING
// FOR tok1, tok2 WITH dict1, dict2;" — confirmed live against postgres:17
// that ALTER MAPPING FOR is an upsert (adds the mapping if none exists yet
// for that token type, replaces it if one does), so it's always the right
// verb regardless of whether the config was COPY'd from an existing one
// (which pre-populates default mappings) or created from scratch — no need
// to distinguish ADD vs ALTER, matching the RFC's own diffing-semantics
// table (Section 12.1), which only ever uses ALTER MAPPING FOR.
func tsMappingAlterSQL(configIdent string, m pipeline.TSMappingDef) string {
	dicts := make([]string, len(m.Dictionaries))
	for i, d := range m.Dictionaries {
		dicts[i] = d.String()
	}
	return tsMappingAlterSQLStrings(configIdent, m.TokenTypes, dicts)
}

func tsMappingAlterSQLStrings(configIdent string, tokenTypes, dictionaries []string) string {
	return fmt.Sprintf("ALTER TEXT SEARCH CONFIGURATION %s ALTER MAPPING FOR %s WITH %s;",
		configIdent, strings.Join(tokenTypes, ", "), strings.Join(dictionaries, ", "))
}

// tsMappingDropSQL builds "ALTER TEXT SEARCH CONFIGURATION cfg DROP MAPPING
// FOR tok;" for a token type whose mapping was removed from source.
func tsMappingDropSQL(configIdent, tokenType string) string {
	return fmt.Sprintf("ALTER TEXT SEARCH CONFIGURATION %s DROP MAPPING FOR %s;", configIdent, tokenType)
}

// opFamilyAddSQL/opFamilyDropSQL render a single-member "ALTER OPERATOR
// FAMILY ... ADD/DROP ..." statement (RFC Section 14.4) — one statement per
// member, not batched, so each DiffOp carries its own safety classification
// and can be individually reviewed or skipped, matching how every other
// per-element diff in this file (constraints, columns, grants) is emitted.
func opFamilyAddSQL(famIdent, am string, m pipeline.OpFamilyMember) string {
	return fmt.Sprintf("ALTER OPERATOR FAMILY %s USING %s ADD %s;", famIdent, am, m.AddClause())
}

func opFamilyDropSQL(famIdent, am string, m snapshot.SnapOpFamilyMember) string {
	kind := "OPERATOR"
	if m.IsFunction {
		kind = "FUNCTION"
	}
	return fmt.Sprintf("ALTER OPERATOR FAMILY %s USING %s DROP %s %d (%s, %s);",
		famIdent, am, kind, m.Number, m.LeftType, m.RightType)
}

// opFamilyMemberSnapKey is snapshot.SnapOpFamilyMember's counterpart to
// pipeline.OpFamilyMember.Key() — must stay byte-identical to it (same
// kind/number/op_type components) so a desired-side and snapshot-side
// member at the same catalog slot compare as the same map key.
func opFamilyMemberSnapKey(m snapshot.SnapOpFamilyMember) string {
	kind := "OPERATOR"
	if m.IsFunction {
		kind = "FUNCTION"
	}
	return kind + "|" + strconv.Itoa(m.Number) + "|" + m.LeftType + "|" + m.RightType
}

// qualifyOpFamilyOperandForCompare normalizes a loose member's operand
// identity (RFC Section 14.4) for comparison purposes only — mirroring
// qualifyFuncForCompare's own reasoning (introspection always returns a
// fully schema-qualified name; hand-written source commonly doesn't) and,
// critically, its symmetric application: called on BOTH the desired and
// snapshot side before comparing, so a pure-offline diff (both sides
// unqualified) still matches, not just the live-catalog path. Function
// members and sort-family targets default like every other DPG object
// reference (the family's own declaring schema, matching graph.go's
// defaultSchema for the identical reference); operator members default to
// pg_catalog instead — an unqualified operator symbol overwhelmingly means
// a built-in, unlike a function or family reference.
func qualifyOpFamilyOperandForCompare(isOperator bool, famSchema, schema, name string) string {
	if schema != "" {
		return schema + "." + name
	}
	if isOperator {
		return "pg_catalog." + name
	}
	return famSchema + "." + name
}

// opMemberNameMatches compares an operator/function member's identity,
// tolerating an unqualified FUNCTION reference that resolves to either the
// family's own schema (qualifyOpFamilyOperandForCompare's existing default
// for a genuinely custom support function) or pg_catalog (a built-in reused
// unqualified — routine for standard access-method support functions like
// btint4cmp, which PostgreSQL's own documentation examples write exactly
// this way). Both are legitimate resolutions for the identical unqualified
// text without a live catalog to disambiguate, so treating only one as a
// match produces a false structural mismatch — confirmed live: RFC audit
// item C.5's fix started trusting this comparison's "false" result to drive
// DROP+CREATE directly (previously masked by diffOperatorClass falling
// through to diffOpaqueIR's live-BodyHash blind spot regardless), which
// surfaced this as a real live-drift false positive on an entirely
// unmodified operator class using an unqualified builtin FUNCTION item. An
// unqualified OPERATOR reference keeps its existing pg_catalog-only
// default: unlike a function, a genuinely custom operator symbol left
// unqualified in source is rare enough, and ambiguous enough at apply time
// too, not to warrant the same widening.
func opMemberNameMatches(isFunction bool, famSchema, dSchema, dName, sSchema, sName string) bool {
	if dName != sName {
		return false
	}
	if dSchema != "" {
		return dSchema == sSchema
	}
	// sSchema == "" is a second, genuine legitimate resolution alongside
	// famSchema/pg_catalog below, found live-testing operator class rename:
	// this function's existing branches assume the snapshot side is always
	// resolved/qualified (true for a live-introspected snapshot, per this
	// function's own doc comment), but an OFFLINE (non-introspected)
	// snapshot populated directly from a prior compile — snapshot.Populate,
	// not introspection — stores the identical unqualified text the
	// desired side has, on BOTH sides equally. Without this branch, a
	// same-declaration offline apply/plan compared "" (snap) against
	// famSchema/"pg_catalog" and always mismatched, permanently breaking
	// idempotency for any operator class member with an unqualified
	// FUNCTION/OPERATOR reference — confirmed live against a real
	// postgres:17 server via the exact offline (compile-vs-committed-
	// snapshot) path apply/plan actually uses between two applies.
	if isFunction {
		return sSchema == "" || sSchema == famSchema || sSchema == "pg_catalog"
	}
	return sSchema == "" || sSchema == "pg_catalog"
}

// opFamilyMemberEqual compares the "payload" of a same-slot member pair —
// the parts Key() deliberately excludes (see its doc comment): operator/
// function identity, FOR ORDER BY, and (for FUNCTION) the argument list.
func opFamilyMemberEqual(famSchema string, d pipeline.OpFamilyMember, s snapshot.SnapOpFamilyMember) bool {
	if !opMemberNameMatches(d.IsFunction, famSchema, d.Name.Schema, d.Name.Name, s.NameSchema, s.Name) {
		return false
	}
	if d.OrderBy != s.OrderBy {
		return false
	}
	if d.OrderBy {
		dSort := qualifyOpFamilyOperandForCompare(false, famSchema, d.SortFamily.Schema, d.SortFamily.Name)
		sSort := qualifyOpFamilyOperandForCompare(false, famSchema, s.SortFamilySchema, s.SortFamilyName)
		if dSort != sSort {
			return false
		}
	}
	return slices.Equal(d.FuncArgs, s.FuncArgs)
}

// opClassMembersEqual is diffOperatorClass's structural-equality check: true
// only when desired and snap carry the exact same set of members (by Key()
// and opFamilyMemberEqual's payload comparison), with none added or removed.
// Unlike diffOpFamilyMembers, this never produces per-member ADD/DROP ops —
// PostgreSQL has no incremental ALTER OPERATOR CLASS at all (RFC Section 14.4), so
// a real difference here can only ever mean DROP+CREATE, same as before this
// fix. Its only purpose is telling "same members, cosmetically different
// Body text" apart from "actually different members".
func opClassMembersEqual(famSchema string, desired []pipeline.OpFamilyMember, snap []snapshot.SnapOpFamilyMember) bool {
	if len(desired) != len(snap) {
		return false
	}
	snapByKey := make(map[string]snapshot.SnapOpFamilyMember, len(snap))
	for _, m := range snap {
		snapByKey[opFamilyMemberSnapKey(m)] = m
	}
	for _, d := range desired {
		s, ok := snapByKey[d.Key()]
		if !ok || !opFamilyMemberEqual(famSchema, d, s) {
			return false
		}
	}
	return true
}

// diffOperatorClass is diffOpaqueIR's OperatorClass-specific wrapper. Unlike
// diffOperatorFamily/diffTSConfig, it never adds incremental ops of its own —
// PostgreSQL's AS-list has no incremental ALTER OPERATOR CLASS, so any real
// member-list change still must resolve to a DROP+CREATE. Its job is
// deciding *whether* that DROP+CREATE is warranted, using the structural
// Members/StorageType/FAMILY comparison instead of diffOpaqueIR's raw
// BodyHash — which false-positives on a hand-written AS-list that's
// structurally identical to the snapshot's but spelled differently
// (whitespace, operator/type qualification, item order), and, more
// severely, silently under-reports for the live-comparison case: a live-
// introspected snap always has Reconstructed == true (see
// introspectOperatorClasses), which makes its BodyHash permanently "" (see
// sourceBodyHash's doc comment) — so diffOpaqueIR's `snap.BodyHash != ""`
// guard would skip the comparison entirely no matter how real the change
// is. Confirmed live: a genuine AS-list edit was correctly detected by
// offline `plan` (source vs. committed snapshot, real BodyHash on both
// sides) but produced zero ops from `plan --live` (RFC audit item C.5).
//
// So both directions of this structural comparison drive the decision
// directly, without ever falling through to diffOpaqueIR's hash check for a
// members-changed case:
//   - equal → body passed through as "" to diffOpaqueIR, so its hash branch
//     is skipped and only Comment gets diffed — exactly as if nothing about
//     the AS-list had changed, because nothing catalog-relevant has;
//   - unequal → DROP+CREATE emitted directly via dropCreateOpaque, since the
//     structural comparison alone already proves a real difference exists,
//     independent of whether either side's BodyHash happens to be usable.
//
// A stale pre-feature snapshot (OperatorClassMembersStructured false) falls
// back to today's unstructured BodyHash path, same as before this fix.
// renameOperatorIfUnchanged is renameOpaqueIfUnchanged's OperatorClass/
// OperatorFamily-specific counterpart: the FROM side needs an extra
// "USING access_method" qualifier neither Table-shaped kinds nor the other
// opaque wrapper kinds need — real PostgreSQL's ALTER OPERATOR CLASS/FAMILY
// ... USING method RENAME TO new_name, since a class/family name is unique
// only per access method, which itself cannot change via a rename. alterKW
// is "OPERATOR CLASS" or "OPERATOR FAMILY" (the "FAMILY" keyword belongs
// there, not after RENAME TO).
func renameOperatorIfUnchanged(ops []pipeline.DiffOp, alterKW, accessMethod string, snap *snapshot.SnapOpaque, desiredSchema, desiredName string, pos pipeline.SourcePos) []pipeline.DiffOp {
	if snap.Schema == desiredSchema && snap.Name == desiredName {
		return ops
	}
	for _, op := range ops {
		if op.Safety() == pipeline.Destructive {
			return ops
		}
	}
	// SET SCHEMA / RENAME TO, in that order — see diffTable's identical
	// mechanism/reasoning.
	if snap.Schema != desiredSchema {
		ops = append(ops, safeOp(
			fmt.Sprintf("ALTER %s %s USING %s SET SCHEMA %s;", alterKW, qualIdent(snap.Schema, snap.Name), accessMethod, quoteIdent(desiredSchema)),
			pos,
		))
	}
	if snap.Name != desiredName {
		ops = append(ops, cautionOp(
			fmt.Sprintf("ALTER %s %s USING %s RENAME TO %s;", alterKW, qualIdent(desiredSchema, snap.Name), accessMethod, quoteIdent(desiredName)),
			pos,
		))
	}
	return ops
}

func diffOperatorClass(o *ir.OperatorClass, snap *snapshot.SnapOpaque) ([]pipeline.DiffOp, error) {
	// An omitted/unqualified FAMILY schema does NOT mean "wherever the class
	// currently lives" — confirmed live via a real postgres:17 server
	// (ALTER OPERATOR CLASS ... SET SCHEMA moves the class but leaves its
	// same-name auto-created family exactly where it was created). The
	// snapshot's own last-known family schema is authoritative for an
	// already-existing class; o.Schema is only the right fallback for a
	// class with no prior recorded family schema at all (a brand-new class,
	// or a stale snapshot predating OperatorClassFamilySchema — same
	// self-healing spirit as the stale-snapshot guards elsewhere in this
	// file). Without this, a class relying on the implicit auto-family
	// spuriously misdiffed as "family changed" on every cross-schema SET
	// SCHEMA, forcing an unnecessary DESTRUCTIVE drop+recreate instead of
	// the real SAFE ALTER ... SET SCHEMA the class-only move actually is.
	famSchema := o.FamilySchema
	if famSchema == "" {
		if snap.OperatorClassFamilySchema != "" {
			famSchema = snap.OperatorClassFamilySchema
		} else {
			famSchema = o.Schema
		}
	}
	// famName mirrors famSchema's fallback: ir.OperatorClass.FamilyName's own
	// doc comment documents empty FamilyName as meaning "hand-written source
	// omitted FAMILY, relying on PostgreSQL's own same-name auto-creation" —
	// i.e. it resolves to the class's own Name. Introspection, by contrast,
	// always names the family explicitly (see introspectOperatorClasses),
	// so comparing o.FamilyName/o.FamilySchema against snap's without this
	// same resolution made every unqualified-or-omitted FAMILY declaration
	// misdiff as "changed" the moment a real snap value was available to
	// compare against — live-reproduced as a false-positive DROP+CREATE on
	// an entirely unmodified operator class (RFC audit item C.5).
	famName := o.FamilyName
	if famName == "" {
		famName = o.Name
	}
	// The snapshot side needs the identical fallback resolution applied to
	// the desired side just above — a genuine, pre-existing bug found while
	// live-testing operator class rename (unrelated to renaming itself):
	// toSnapObject stores o.FamilySchema/FamilyName raw, so an unqualified
	// FAMILY clause (the common case — same-schema, or omitted entirely)
	// left snap's value "" while the desired side's fallback resolved it to
	// a real schema/name, permanently miscomparing "public" != "" on every
	// single plan/apply and spuriously DROP+CREATE-ing an entirely
	// unmodified operator class every time — the offline analog of RFC
	// audit item C.5, which only ever fixed the live-introspection side
	// (introspectOperatorClasses always names the family explicitly, so it
	// never hit this path) — confirmed live against a real postgres:17
	// server, not merely suspected from reading the code.
	snapFamSchema := snap.OperatorClassFamilySchema
	if snapFamSchema == "" {
		snapFamSchema = snap.Schema
	}
	snapFamName := snap.OperatorClassFamilyName
	if snapFamName == "" {
		snapFamName = snap.Name
	}
	var ops []pipeline.DiffOp
	var err error
	if snap.OperatorClassMembersStructured {
		if o.StorageType == snap.OperatorClassStorageType &&
			famSchema == snapFamSchema &&
			famName == snapFamName &&
			opClassMembersEqual(famSchema, o.Members, snap.OperatorClassMembers) {
			ops, err = diffOpaqueIR(o.QualifiedName(), "", o.Reconstructed, o.Comment, snap, o.SrcPos)
		} else {
			ops, err = dropCreateOpaque(o.QualifiedName(), o.Body, o.Comment, snap, o.SrcPos)
		}
	} else {
		// hashBody normalizes the class's own (new) name back to its old
		// (matched) name before hashing — same reasoning as diffTSDict's
		// identical fix: Body embeds the class's own name, so hashing it
		// unmodified against a snapshot hash computed under the old name
		// would always misdetect a pure rename as a body change.
		hashBody := opaqueBodyForHash(o.Body, o.Schema, o.Name, snap.Schema, snap.Name)
		ops, err = diffOpaqueIRHash(o.QualifiedName(), o.Body, hashBody, o.Reconstructed, o.Comment, snap, o.SrcPos)
	}
	if err != nil {
		return nil, err
	}
	return renameOperatorIfUnchanged(ops, "OPERATOR CLASS", o.AccessMethod, snap, o.Schema, o.Name, o.SrcPos), nil
}

// diffOpFamilyMembers diffs an operator family's loose members (RFC Section 14.4)
// at the per-slot level (Key(): kind+number+op_types — see
// pipeline.OpFamilyMember.Key's doc comment: two members at the same slot
// are the same catalog row changing shape, not independent members).
// structured is the same stale-snapshot guard pattern as
// OptionsStructured/StatisticsStructured elsewhere in this file: a snapshot
// written before this feature existed has OpFamilyMembersStructured ==
// false, so this returns nil rather than proposing an ADD for a member
// PostgreSQL may already have — that ADD would genuinely error live (there
// is no "ADD ... IF NOT EXISTS"), so silence is the only safe default until
// the next apply/verify populates the sentinel. Keys are iterated in sorted
// order (unlike diffTSConfigMappings's plain map iteration) so plan output
// is stable across repeated runs.
func diffOpFamilyMembers(famSchema, famIdent, am string, desired []pipeline.OpFamilyMember, snap []snapshot.SnapOpFamilyMember, structured bool, pos pipeline.SourcePos) []pipeline.DiffOp {
	if !structured {
		return nil
	}
	desiredByKey := make(map[string]pipeline.OpFamilyMember, len(desired))
	for _, m := range desired {
		desiredByKey[m.Key()] = m
	}
	snapByKey := make(map[string]snapshot.SnapOpFamilyMember, len(snap))
	for _, m := range snap {
		snapByKey[opFamilyMemberSnapKey(m)] = m
	}

	keys := make(map[string]bool, len(desiredByKey)+len(snapByKey))
	for k := range desiredByKey {
		keys[k] = true
	}
	for k := range snapByKey {
		keys[k] = true
	}
	sortedKeys := make([]string, 0, len(keys))
	for k := range keys {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)

	var ops []pipeline.DiffOp
	for _, key := range sortedKeys {
		d, inDesired := desiredByKey[key]
		s, inSnap := snapByKey[key]
		switch {
		case inDesired && !inSnap:
			ops = append(ops, safeOp(opFamilyAddSQL(famIdent, am, d), pos))
		case !inDesired && inSnap:
			ops = append(ops, destructiveOp(opFamilyDropSQL(famIdent, am, s), pos))
		case inDesired && inSnap && !opFamilyMemberEqual(famSchema, d, s):
			ops = append(ops, destructiveOp(opFamilyDropSQL(famIdent, am, s), pos))
			ops = append(ops, safeOp(opFamilyAddSQL(famIdent, am, d), pos))
		}
	}
	return ops
}

// diffTSConfigMappings diffs MAPPING FOR entries at the per-token-type level
// (not per declared entry): a real PG mapping is keyed by individual token
// type, and DPG's "MAPPING FOR word, hword WITH ..." grouping is just a
// source-side convenience for declaring several token types at once — two
// entries covering the same token types with different groupings must NOT
// show as spurious drift, so both sides are flattened to tokenType->
// dictionaries before comparing (mirrors how introspection naturally reads
// pg_ts_config_map, one row per token type).
func diffTSConfigMappings(configIdent string, desired []pipeline.TSMappingDef, snap []snapshot.SnapTSMapping, pos pipeline.SourcePos) []pipeline.DiffOp {
	desiredByToken := make(map[string][]string)
	for _, m := range desired {
		dicts := make([]string, len(m.Dictionaries))
		for i, d := range m.Dictionaries {
			dicts[i] = d.String()
		}
		for _, tt := range m.TokenTypes {
			desiredByToken[tt] = dicts
		}
	}
	snapByToken := make(map[string][]string)
	for _, m := range snap {
		for _, tt := range m.TokenTypes {
			snapByToken[tt] = m.Dictionaries
		}
	}

	var ops []pipeline.DiffOp
	for tt, dicts := range desiredByToken {
		if !slices.Equal(snapByToken[tt], dicts) {
			ops = append(ops, safeOp(tsMappingAlterSQLStrings(configIdent, []string{tt}, dicts), pos))
		}
	}
	for tt := range snapByToken {
		if _, ok := desiredByToken[tt]; !ok {
			ops = append(ops, safeOp(tsMappingDropSQL(configIdent, tt), pos))
		}
	}
	return ops
}

// diffOpaqueIR checks whether an opaque object's body hash has changed and, if
// so, emits a structured DROP (via dropObject, reusing the exact statement
// already used when the object is removed outright) followed by a CREATE from
// the new body. The body comparison is only meaningful when BOTH sides are
// source-derived deparses: it is skipped when the desired object's body is a
// catalog reconstruction (reconstructed == true, e.g. verify introspects the
// desired side) or when the snapshot side carries no hash (a reconstructed
// baseline, e.g. plan --live). Offline plan/apply (source vs source) compare
// normally and detect genuine edits.
//
// comment is diffed independently of Body — a comment-only change never
// needs the DROP+CREATE a body change requires, so it's checked even when
// the body branch above doesn't fire (unchanged body, reconstructed, or no
// baseline hash yet). Uses snap's own Schema/Name/Args/Using (already the
// exact per-object identity dropObject/commentOnOpaqueSQL need) rather than
// requiring a second set of identity params from every call site. Runs on
// the live/reconstructed path too, unlike Body: an exact comment string has
// no canonical-form-vs-hand-written ambiguity the way a reconstructed body
// does, so comparing it is always reliable.
func diffOpaqueIR(name, body string, reconstructed bool, comment *string, snap *snapshot.SnapOpaque, pos pipeline.SourcePos) ([]pipeline.DiffOp, error) {
	return diffOpaqueIRHash(name, body, body, reconstructed, comment, snap, pos)
}

// diffOpaqueIRHash is diffOpaqueIR generalized with a separate hashBody:
// the text actually hashed for the body-changed comparison, which may
// differ from body (the text used to emit CREATE on a genuine change).
// diffOpaqueIR itself passes body for both — every caller with no rename
// concept (Operator, TSConfig, and OperatorClass/Family's structured-equal
// path, which passes "" for body and so never reaches this branch at all)
// goes through it unchanged.
//
// Rename-aware wrappers (diffTSDict/diffTSParser/diffTSTemplate/
// diffOperatorFamily/diffOperatorClass's unstructured fallback) call this
// directly with hashBody normalized via opaqueBodyForHash: their Body text
// embeds the object's own name (e.g. "CREATE TEXT SEARCH DICTIONARY
// name (...)"), so hashing the real (new-name) body against a snapshot
// hash computed under the OLD name would always misdetect a pure rename
// as a body/definition change — confirmed live: a bare RENAMED FROM with
// no other change was silently emitting DROP + CREATE for these kinds
// (and, for Subscription specifically — handled separately, not through
// this function, but the identical bug — erroring outright on DROP
// SUBSCRIPTION's own replication-slot cleanup) before this fix. body
// itself is left untouched, so a genuine change still emits CREATE under
// the correct (new) name via dropCreateOpaque below.
func diffOpaqueIRHash(name, body, hashBody string, reconstructed bool, comment *string, snap *snapshot.SnapOpaque, pos pipeline.SourcePos) ([]pipeline.DiffOp, error) {
	if hashBody != "" && !reconstructed {
		sum := sha256.Sum256([]byte(strings.TrimSpace(hashBody)))
		newHash := fmt.Sprintf("%x", sum)
		if snap.BodyHash != "" && newHash != snap.BodyHash {
			return dropCreateOpaque(name, body, comment, snap, pos)
		}
	}
	if !ptrEq(comment, snap.Comment) {
		if sql := commentOnOpaqueSQL(snap.Kind, snap.Schema, snap.Name, snap.Args, snap.Using, comment); sql != "" {
			return []pipeline.DiffOp{safeOp(sql, pos)}, nil
		}
	}
	return nil, nil
}

// opaqueBodyForHash returns body with its own (new) desiredName normalized
// back to oldName before hashing, when a rename is in effect (oldName set
// and different from desiredName) — the same "normalize the renamed
// identity out before comparing" principle translateConstraintExpr/
// translateIndexColumnList already use for column renames (Section 7.6),
// applied here to an opaque object's raw body text instead of a
// constraint/index expression. Only the bare (unqualified) name is
// substituted (first occurrence), not a schema-qualified form — correct
// for the common same-schema rename case this mechanism actually supports;
// a simultaneous cross-schema move (not itself implemented for these
// opaque kinds) may still misdetect, the same known, accepted limitation
// already documented for Table/View's own cross-schema RENAMED FROM
// (renames in place, doesn't move schema).
func opaqueBodyForHash(body, desiredSchema, desiredName, oldSchema, oldName string) string {
	// Body commonly embeds the object's own schema-qualified name (e.g.
	// "CREATE TEXT SEARCH DICTIONARY new_schema.ispell (...)", or quoted
	// as `"new_schema"."ispell"` depending on the kind's own compiler
	// output style) — a cross-schema RENAMED FROM changes that schema
	// text too, even when nothing about the object's real definition
	// changed. Two independent bare-substring replacements (schema, then
	// name), not a single concatenated "schema.name" match: a plain
	// substring replacement is quoting-agnostic (it matches the token
	// wherever it appears, quoted or not), which is exactly how the
	// original name-only fix below already worked before schema moves
	// existed — reusing that same mechanism instead of inventing a
	// quote-aware qualified-name matcher.
	if desiredSchema != "" && oldSchema != "" && desiredSchema != oldSchema {
		body = strings.Replace(body, desiredSchema, oldSchema, 1)
	}
	if oldName != "" && oldName != desiredName {
		body = strings.Replace(body, desiredName, oldName, 1)
	}
	return body
}

// dropCreateOpaque emits the standard DROP+CREATE(+COMMENT) sequence for an
// opaque object whose body has genuinely changed — shared by diffOpaqueIR's
// raw-BodyHash decision and diffOperatorClass's structural one (see
// diffOperatorClass's doc comment for why OperatorClass can't rely on
// diffOpaqueIR's BodyHash check for the live-comparison case, RFC audit item
// C.5).
func dropCreateOpaque(name, body string, comment *string, snap *snapshot.SnapOpaque, pos pipeline.SourcePos) ([]pipeline.DiffOp, error) {
	ops := dropObject(&snapshot.SnapObject{Kind: snap.Kind, Opaque: snap})
	createOps, err := createOpaque(name, body, snap.Kind, snap.Schema, pos)
	if err != nil {
		return nil, err
	}
	ops = append(ops, createOps...)
	return appendCommentOp(ops, nil, snap.Kind, snap.Schema, snap.Name, snap.Args, snap.Using, comment, pos)
}

// renameOpaqueIfUnchanged appends an ALTER <alterKW> ... RENAME TO op when
// snap's matched (old) name differs from the desired name, unless ops
// already contains a Destructive op — a body/definition change already
// recreates the object under its final (new) name directly via
// createOpaque, so no separate rename statement is needed in that combined
// case. Shared by diffTSDict/diffTSParser/diffTSTemplate's thin wrappers
// (RFC Sections 12.2-12.4): kept out of diffOpaqueIR itself since several of
// its other callers (Operator, OperatorClass, OperatorFamily) have no
// rename concept in real PostgreSQL at all and shouldn't gain one by
// extending the shared function's signature.
func renameOpaqueIfUnchanged(ops []pipeline.DiffOp, alterKW string, snap *snapshot.SnapOpaque, desiredSchema, desiredName string, pos pipeline.SourcePos) []pipeline.DiffOp {
	if snap.Schema == desiredSchema && snap.Name == desiredName {
		return ops
	}
	for _, op := range ops {
		if op.Safety() == pipeline.Destructive {
			return ops
		}
	}
	// SET SCHEMA / RENAME TO, in that order — see diffTable's identical
	// mechanism/reasoning.
	if snap.Schema != desiredSchema {
		ops = append(ops, safeOp(
			fmt.Sprintf("ALTER %s %s SET SCHEMA %s;", alterKW, qualIdent(snap.Schema, snap.Name), quoteIdent(desiredSchema)),
			pos,
		))
	}
	if snap.Name != desiredName {
		ops = append(ops, cautionOp(
			fmt.Sprintf("ALTER %s %s RENAME TO %s;", alterKW, qualIdent(desiredSchema, snap.Name), quoteIdent(desiredName)),
			pos,
		))
	}
	return ops
}

// diffTSDict is diffOpaqueIR's TSDict-specific wrapper: adds RENAME TO
// detection (RFC Section 12.2), which diffOpaqueIR knows nothing about — see
// renameOpaqueIfUnchanged's doc comment for why this lives in a per-kind
// wrapper instead of the shared function.
func diffTSDict(o *ir.TSDict, snap *snapshot.SnapOpaque) ([]pipeline.DiffOp, error) {
	// Option-change diffing (Section 12.2, audit item #52): when the
	// snapshot has structured Options/Template data, TEMPLATE (which real
	// PostgreSQL has no ALTER for at all — confirmed live) decides
	// DROP+CREATE on its own, and everything else (option add/change/
	// remove) uses a targeted SAFE ALTER TEXT SEARCH DICTIONARY (...)
	// instead of the raw opaque BodyHash comparison below, which would
	// otherwise misdetect any option edit as a full body change (forcing
	// an unnecessary destructive recreate) when comparing hand-written
	// source, and — via Reconstructed always being true for a live-
	// introspected object — detect nothing at all for a live-catalog-only
	// option change (BodyHash is skipped entirely for Reconstructed
	// bodies, the same root cause diffTablespace/diffCollation's identical
	// doc comments already explain).
	if snap.TSDictOptionsStructured {
		// TEMPLATE comparison is deliberately name-only — see
		// SnapOpaque.TSDictTemplateName's doc comment for why a schema
		// fallback would misdetect the common unqualified-builtin-template
		// case as changed on every plan.
		if !strings.EqualFold(o.TemplateName, snap.TSDictTemplateName) {
			ops, err := dropCreateOpaque(o.QualifiedName(), o.Body, o.Comment, snap, o.SrcPos)
			if err != nil {
				return nil, err
			}
			return ops, nil
		}
		var ops []pipeline.DiffOp
		ident := qualIdent(snap.Schema, snap.Name)
		ops = append(ops, diffTSDictOptions(ident, o.Options, snap.TSDictOptions, o.SrcPos)...)
		if !ptrEq(o.Comment, snap.Comment) {
			if sql := commentOnOpaqueSQL("ts_dict", snap.Schema, snap.Name, "", "", o.Comment); sql != "" {
				ops = append(ops, safeOp(sql, o.SrcPos))
			}
		}
		if !ptrEq(o.Owner, snap.TSDictOwner) && o.Owner != nil {
			ops = append(ops, safeOp(fmt.Sprintf("ALTER TEXT SEARCH DICTIONARY %s OWNER TO %s;", ident, quoteIdent(*o.Owner)), o.SrcPos))
		}
		return renameOpaqueIfUnchanged(ops, "TEXT SEARCH DICTIONARY", snap, o.Schema, o.Name, o.SrcPos), nil
	}

	// Stale snapshot predating Options/Template's structured fields: fall
	// back to the original opaque BodyHash comparison entirely, same
	// self-healing spirit as every other Structured sentinel in this file.
	hashBody := opaqueBodyForHash(o.Body, o.Schema, o.Name, snap.Schema, snap.Name)
	ops, err := diffOpaqueIRHash(o.QualifiedName(), o.Body, hashBody, o.Reconstructed, o.Comment, snap, o.SrcPos)
	if err != nil {
		return nil, err
	}
	// OWNER TO (Section 12.2) — real PostgreSQL has no OWNER concept for a
	// TS parser/template (see diffTSParser's identical note), only for a
	// dictionary. Skipped when a body/definition change already recreates
	// it under the final desired Owner via createOpaque (no such op exists
	// today — createOpaque doesn't apply Owner at all — so this only
	// fires on the no-recreate path in practice, same guard style as
	// renameOpaqueIfUnchanged's Destructive scan).
	destructive := false
	for _, op := range ops {
		if op.Safety() == pipeline.Destructive {
			destructive = true
			break
		}
	}
	if !destructive && !ptrEq(o.Owner, snap.TSDictOwner) && o.Owner != nil {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER TEXT SEARCH DICTIONARY %s OWNER TO %s;", qualIdent(snap.Schema, snap.Name), quoteIdent(*o.Owner)), o.SrcPos))
	}
	return renameOpaqueIfUnchanged(ops, "TEXT SEARCH DICTIONARY", snap, o.Schema, o.Name, o.SrcPos), nil
}

// diffTSDictOptions computes a single combined ALTER TEXT SEARCH DICTIONARY
// name (key1 = val1, key2, ...) statement for a changed option set (Section
// 12.2, audit item #52) — confirmed live via \h ALTER TEXT SEARCH
// DICTIONARY that, unlike Table/Tablespace's separate SET/RESET clauses,
// this is one parenthesized list where "key = value" adds/changes an option
// and bare "key" (no "= value") removes it. snap is the snapshot's
// structured option list; desired is the live IR's ordered option list.
// SAFE: a text search dictionary has no stored data of its own to protect,
// unlike a table's storage-affecting reloptions.
func diffTSDictOptions(dictIdent string, desired []pipeline.StorageParam, snap []snapshot.SnapOptionKV, pos pipeline.SourcePos) []pipeline.DiffOp {
	snapMap := make(map[string]string, len(snap))
	for _, kv := range snap {
		snapMap[kv.Key] = kv.Value
	}
	desiredKeys := make(map[string]bool, len(desired))
	var entries []string
	for _, p := range desired {
		desiredKeys[p.Key] = true
		old, existed := snapMap[p.Key]
		if !existed || old != p.Value {
			entries = append(entries, fmt.Sprintf("%s = %s", p.Key, quoteLit(p.Value)))
		}
	}
	for _, kv := range snap {
		if !desiredKeys[kv.Key] {
			entries = append(entries, kv.Key)
		}
	}
	if len(entries) == 0 {
		return nil
	}
	return []pipeline.DiffOp{safeOp(
		fmt.Sprintf("ALTER TEXT SEARCH DICTIONARY %s (%s);", dictIdent, strings.Join(entries, ", ")),
		pos,
	)}
}

// diffTSParser is diffTSDict's TSParser-specific counterpart (RFC Section
// 12.3). Real PostgreSQL has no OWNER concept for a parser at all, unlike
// TSDict, so there is no analogous Owner gap to track here.
func diffTSParser(o *ir.TSParser, snap *snapshot.SnapOpaque) ([]pipeline.DiffOp, error) {
	hashBody := opaqueBodyForHash(o.Body, o.Schema, o.Name, snap.Schema, snap.Name)
	ops, err := diffOpaqueIRHash(o.QualifiedName(), o.Body, hashBody, o.Reconstructed, o.Comment, snap, o.SrcPos)
	if err != nil {
		return nil, err
	}
	return renameOpaqueIfUnchanged(ops, "TEXT SEARCH PARSER", snap, o.Schema, o.Name, o.SrcPos), nil
}

// diffTSTemplate is diffTSDict's TSTemplate-specific counterpart (RFC
// Section 12.4). Same no-OWNER-concept note as diffTSParser.
func diffTSTemplate(o *ir.TSTemplate, snap *snapshot.SnapOpaque) ([]pipeline.DiffOp, error) {
	hashBody := opaqueBodyForHash(o.Body, o.Schema, o.Name, snap.Schema, snap.Name)
	ops, err := diffOpaqueIRHash(o.QualifiedName(), o.Body, hashBody, o.Reconstructed, o.Comment, snap, o.SrcPos)
	if err != nil {
		return nil, err
	}
	return renameOpaqueIfUnchanged(ops, "TEXT SEARCH TEMPLATE", snap, o.Schema, o.Name, o.SrcPos), nil
}

// diffTSConfig is diffOpaqueIR's TSConfig-specific wrapper: MAPPING FOR
// entries (RFC Section 12.1) are a TSConfig-only concern diffOpaqueIR knows nothing
// about. When the base diff triggers a full DROP+CREATE (body/PARSER
// changed — diffOpaqueIR calls createOpaque directly, not createObject, so
// none of o.Mappings gets applied automatically), every declared mapping is
// unconditionally re-applied (ALTER MAPPING FOR is confirmed live to be an
// upsert, so this is always correct against a config that was just
// recreated from scratch) instead of being diffed against the old
// snapshot's mappings, which no longer describe anything real. Otherwise
// mappings are diffed at the per-token-type level (diffTSConfigMappings).
func diffTSConfig(o *ir.TSConfig, snap *snapshot.SnapOpaque) ([]pipeline.DiffOp, error) {
	hashBody := opaqueBodyForHash(o.Body, o.Schema, o.Name, snap.Schema, snap.Name)
	ops, err := diffOpaqueIRHash(o.QualifiedName(), o.Body, hashBody, o.Reconstructed, o.Comment, snap, o.SrcPos)
	if err != nil {
		return nil, err
	}
	configIdent := qualIdent(o.Schema, o.Name)
	destructive := false
	for _, op := range ops {
		if op.Safety() == pipeline.Destructive {
			destructive = true
			break
		}
	}
	if destructive {
		for _, m := range o.Mappings {
			ops = append(ops, safeOp(tsMappingAlterSQL(configIdent, m), o.SrcPos))
		}
		return ops, nil
	}
	// RENAMED FROM (Section 12.1): after the comment, which names the old
	// configuration, and before the owner and mapping changes, which name
	// the new one.
	ops = renameOpaqueIfUnchanged(ops, "TEXT SEARCH CONFIGURATION", snap, o.Schema, o.Name, o.SrcPos)
	// OWNER TO (Section 12.1) — skipped on the destructive (recreate) path
	// above, same reasoning as diffTSDict's identical guard: createOpaque
	// doesn't apply Owner at all, so this only fires on the no-recreate
	// path in practice.
	if !ptrEq(o.Owner, snap.TSConfigOwner) && o.Owner != nil {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER TEXT SEARCH CONFIGURATION %s OWNER TO %s;", configIdent, quoteIdent(*o.Owner)), o.SrcPos))
	}
	ops = append(ops, diffTSConfigMappings(configIdent, o.Mappings, snap.Mappings, o.SrcPos)...)
	return ops, nil
}

// diffOperatorFamily is diffOpaqueIR's OperatorFamily-specific wrapper,
// cloning diffTSConfig's own DROP+CREATE-vs-incremental-diff structure
// exactly: loose members (RFC Section 14.4) are a family-only concern diffOpaqueIR
// knows nothing about. When the base diff triggers a full DROP+CREATE
// (body/AccessMethod changed — diffOpaqueIR calls createOpaque directly,
// not createObject, so none of o.Members gets applied automatically), every
// declared member is unconditionally re-added: the just-recreated family
// has none of the old snapshot's members anymore, so diffing against them
// would be comparing against nothing real. Otherwise members are diffed
// incrementally (diffOpFamilyMembers) — unlike an operator class's
// AS-list, PostgreSQL genuinely supports incremental
// ALTER OPERATOR FAMILY ... ADD/DROP for family-level members, so there is
// no need to force a full drop+recreate here the way OperatorClass does.
func diffOperatorFamily(o *ir.OperatorFamily, snap *snapshot.SnapOpaque) ([]pipeline.DiffOp, error) {
	// hashBody normalizes the family's own (new) name back to its old
	// (matched) name before hashing — same reasoning as diffTSDict's
	// identical fix: Body embeds the family's own name, so hashing it
	// unmodified against a snapshot hash computed under the old name
	// would always misdetect a pure rename as a body change.
	hashBody := opaqueBodyForHash(o.Body, o.Schema, o.Name, snap.Schema, snap.Name)
	ops, err := diffOpaqueIRHash(o.QualifiedName(), o.Body, hashBody, o.Reconstructed, o.Comment, snap, o.SrcPos)
	if err != nil {
		return nil, err
	}
	famIdent := qualIdent(o.Schema, o.Name)
	destructive := false
	for _, op := range ops {
		if op.Safety() == pipeline.Destructive {
			destructive = true
			break
		}
	}
	if destructive {
		for _, m := range o.Members {
			ops = append(ops, safeOp(opFamilyAddSQL(famIdent, o.AccessMethod, m), o.SrcPos))
		}
	} else {
		ops = append(ops, diffOpFamilyMembers(o.Schema, famIdent, o.AccessMethod, o.Members, snap.OpFamilyMembers, snap.OpFamilyMembersStructured, o.SrcPos)...)
	}
	// renameOperatorIfUnchanged's own Destructive scan correctly skips the
	// rename here too — a body change already recreates the family under
	// its final (new) name via dropCreateOpaque, so no separate ALTER is
	// needed even though this function appends more (safe) ops afterward.
	return renameOperatorIfUnchanged(ops, "OPERATOR FAMILY", o.AccessMethod, snap, o.Schema, o.Name, o.SrcPos), nil
}

func diffProcedure(o *ir.Procedure, snap *snapshot.SnapOpaque) ([]pipeline.DiffOp, error) {
	sig := buildProcedureSignature(o)
	pos := o.SrcPos

	// Body changed: re-create via CREATE OR REPLACE (includes comment and
	// grants). Transforms/ObjFile/LinkSymbol/AtomicBody (RFC audit items
	// #26-#28), SecurityDef, and Config (Section 9.2/9.3) join the same
	// recreate trigger as BodyHash — none of them feed into BodyHash
	// itself (Transforms isn't body text at all; AtomicBody's
	// canonicalised text can hash identically to the same content
	// declared via a plain dollar-quoted body), so without this they
	// would never be diffed at all. SecurityDef/Config were previously
	// missing here AND in RenderCreateProcedureSQL's emission — a
	// procedure declaring either (RFC Section 9.3's own worked example
	// uses SECURITY DEFINER) silently lost it on the very first apply,
	// not just on a later drift.
	if (o.BodyHash != "" && snap.BodyHash != "" && o.BodyHash != snap.BodyHash) ||
		transformsChanged(o.Attrs.Transforms, snap.ProcedureTransforms) ||
		!ptrEq(o.Attrs.ObjFile, snap.ProcedureObjFile) || !ptrEq(o.Attrs.LinkSymbol, snap.ProcedureLinkSymbol) ||
		o.Attrs.AtomicBody != snap.ProcedureAtomicBody ||
		o.Attrs.SecurityDef != snap.ProcedureSecurityDef ||
		funcConfigsChanged(o.Attrs.Config, snap.ProcedureConfig) {
		return createProcedure(o), nil
	}

	var ops []pipeline.DiffOp
	// SET SCHEMA / RENAME TO (RFC audit item #10) — same ordering as
	// diffTable's identical mechanism: SET SCHEMA first (old_schema.old_sig
	// -> new schema), then RENAME TO (new_schema.old_sig -> new_name). The
	// old signature uses snap.Schema (where the procedure actually
	// currently lives), not o.Schema — see diffTable's identical reasoning.
	if snap.Schema != o.Schema {
		oldSig := fmt.Sprintf("%s(%s)", qualIdent(snap.Schema, snap.Name), ir.ArgsKey(o.Args))
		ops = append(ops, safeOp(fmt.Sprintf("ALTER PROCEDURE %s SET SCHEMA %s;", oldSig, quoteIdent(o.Schema)), pos))
	}
	if snap.Name != o.Name {
		oldSig := fmt.Sprintf("%s(%s)", qualIdent(o.Schema, snap.Name), ir.ArgsKey(o.Args))
		ops = append(ops, cautionOp(fmt.Sprintf("ALTER PROCEDURE %s RENAME TO %s;", oldSig, quoteIdent(o.Name)), pos))
	}
	if !ptrEq(o.Owner, snap.ProcedureOwner) && o.Owner != nil {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER PROCEDURE %s OWNER TO %s;", sig, quoteIdent(*o.Owner)), pos))
	}
	if desiredTxt, snapTxt := effectiveComment(o.Comment, o.Deprecated), effectiveComment(snap.Comment, snap.Deprecated); !ptrEq(desiredTxt, snapTxt) {
		if desiredTxt != nil {
			ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON PROCEDURE %s IS %s;", sig, quoteLit(*desiredTxt)), pos))
		} else {
			ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON PROCEDURE %s IS NULL;", sig), pos))
		}
	}
	ops = append(ops, diffGrantSet(snap.Grants, o.Grants, "PROCEDURE "+sig, pos)...)
	ops = append(ops, diffRevocationSet(snap.Revocations, o.Revocations, "PROCEDURE "+sig, pos)...)
	ops = append(ops, diffSecurityLabelSet(snap.SecurityLabels, o.SecurityLabels, "PROCEDURE "+sig, pos)...)
	ops = append(ops, diffDependsOnExtension("PROCEDURE", sig, snap.ProcedureDependsOnExtensions, o.DependsOnExtensions, pos)...)
	return ops, nil
}

func diffExtension(o *ir.Extension, snap *snapshot.SnapExtension) []pipeline.DiffOp {
	pos := o.SrcPos
	var ops []pipeline.DiffOp
	// SCHEMA change (RFC audit item #16, #50): real PostgreSQL supports
	// ALTER EXTENSION ... SET SCHEMA (confirmed via pg_query.Parse) — it
	// moves the extension and every object it owns in one step, same as
	// any other SET SCHEMA-capable kind. It errors at apply time for a
	// non-relocatable extension ("extension is not relocatable"), but
	// that's a real PostgreSQL constraint on the target, not a reason for
	// DPG to preemptively drop+recreate every relocatable extension too.
	// Previously SnapExtension.Schema existed but was never compared at
	// all, a spurious no-op on a real change.
	if !ptrEq(o.Schema, snap.Schema) && o.Schema != nil {
		ops = append(ops, safeOp(
			fmt.Sprintf("ALTER EXTENSION %s SET SCHEMA %s;", quoteIdent(o.Name), quoteIdent(*o.Schema)),
			pos,
		))
	}
	if !ptrEq(o.Version, snap.Version) && o.Version != nil {
		ops = append(ops, safeOp(
			fmt.Sprintf("ALTER EXTENSION %s UPDATE TO %s;", quoteIdent(o.Name), quoteLit(*o.Version)),
			pos,
		))
	}
	if !ptrEq(o.Comment, snap.Comment) {
		if o.Comment != nil {
			ops = append(ops, safeOp(
				fmt.Sprintf("COMMENT ON EXTENSION %s IS %s;", quoteIdent(o.Name), quoteLit(*o.Comment)),
				pos,
			))
		} else {
			ops = append(ops, safeOp(
				fmt.Sprintf("COMMENT ON EXTENSION %s IS NULL;", quoteIdent(o.Name)),
				pos,
			))
		}
	}
	return ops
}

func diffSequence(o *ir.Sequence, snap *snapshot.SnapSequence) []pipeline.DiffOp {
	pos := o.SrcPos
	ident := qualIdent(o.Schema, o.Name)

	// RENAMED FROM (RFC Section 10): SET SCHEMA then RENAME TO, as for a
	// table; everything after refers to the new name.
	var renameOps []pipeline.DiffOp
	if snap.Schema != o.Schema {
		renameOps = append(renameOps, safeOp(
			fmt.Sprintf("ALTER SEQUENCE %s SET SCHEMA %s;", qualIdent(snap.Schema, snap.Name), quoteIdent(o.Schema)), pos))
	}
	if snap.Name != o.Name {
		renameOps = append(renameOps, cautionOp(
			fmt.Sprintf("ALTER SEQUENCE %s RENAME TO %s;", qualIdent(o.Schema, snap.Name), quoteIdent(o.Name)), pos))
	}

	// AS type change (RFC audit item #14): per the RFC's own diffing
	// table, this is DROP + CREATE, DESTRUCTIVE — PostgreSQL has no
	// in-place ALTER SEQUENCE ... AS that DPG chose to expose (unlike
	// OWNED BY below, which real PostgreSQL does support altering in
	// place). Checked before paramsChanged since it takes over the whole
	// diff, same shape as diffType's DOMAIN base-type-change branch.
	if o.AsType != nil && o.AsType.String() != snap.AsType {
		ops := []pipeline.DiffOp{destructiveOp(fmt.Sprintf("DROP SEQUENCE IF EXISTS %s;", qualIdent(snap.Schema, snap.Name)), pos)}
		return append(ops, createSequence(o)...)
	}

	ops := renameOps

	// UNLOGGED toggled (Section 10) — same targeted CAUTION ALTER as an
	// unlogged table's identical toggle (Section 7.12), not a recreate.
	if o.Unlogged != snap.Unlogged {
		if o.Unlogged {
			ops = append(ops, cautionOp(fmt.Sprintf("ALTER SEQUENCE %s SET UNLOGGED;", ident), pos))
		} else {
			ops = append(ops, cautionOp(fmt.Sprintf("ALTER SEQUENCE %s SET LOGGED;", ident), pos))
		}
	}

	// Check if any explicitly-specified sequence params differ from the snapshot.
	// Only compare params that the user set (non-nil in desired IR).
	ownedByChanged := o.OwnedBy != nil && !ptrEq(o.OwnedBy, snap.OwnedBy)
	paramsChanged := (o.IncrementBy != nil && !int64PtrEq(o.IncrementBy, snap.IncrementBy)) ||
		seqBoundChanged(o.MinValue, o.NoMinValue, snap.MinValue, snap.NoMinValue) ||
		seqBoundChanged(o.MaxValue, o.NoMaxValue, snap.MaxValue, snap.NoMaxValue) ||
		(o.StartValue != nil && !int64PtrEq(o.StartValue, snap.StartValue)) ||
		(o.Cache != nil && !int64PtrEq(o.Cache, snap.Cache)) ||
		(o.Cycle != nil && *o.Cycle != snap.Cycle) ||
		ownedByChanged
	if paramsChanged {
		var b strings.Builder
		b.WriteString("ALTER SEQUENCE ")
		b.WriteString(ident)
		writeSeqParams(&b, o)
		b.WriteString(";")
		ops = append(ops, safeOp(b.String(), pos))
	}

	if !ptrEq(o.Owner, snap.Owner) && o.Owner != nil {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER SEQUENCE %s OWNER TO %s;", ident, quoteIdent(*o.Owner)), pos))
	}

	if !ptrEq(o.Comment, snap.Comment) {
		if o.Comment != nil {
			ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON SEQUENCE %s IS %s;", ident, quoteLit(*o.Comment)), pos))
		} else {
			ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON SEQUENCE %s IS NULL;", ident), pos))
		}
	}
	ops = append(ops, diffGrantSet(snap.Grants, o.Grants, "SEQUENCE "+ident, pos)...)
	ops = append(ops, diffRevocationSet(snap.Revocations, o.Revocations, "SEQUENCE "+ident, pos)...)
	ops = append(ops, diffSecurityLabelSet(snap.SecurityLabels, o.SecurityLabels, "SEQUENCE "+ident, pos)...)
	if o.Restart {
		// Unconditional, every plan/apply while declared — see
		// Sequence.Restart's doc comment. Not gated on any snapshot
		// comparison, deliberately: there is no persisted "current RESTART
		// value" to compare against. The trailing comment op mirrors real
		// PostgreSQL's own documented usage pattern for RESTART (Section 10):
		// a one-shot reset that should be removed from source once applied.
		ops = append(ops, sequenceRestartOp(ident, o))
		ops = append(ops, instructionOp(pipeline.Safe, fmt.Sprintf(
			"-- remove the RESTART directive on %s once this reset has been applied; it will be re-emitted on every future plan/apply while it remains declared",
			ident,
		), pos))
	}
	return ops
}

// seqBoundChanged compares a sequence's MINVALUE/MAXVALUE declared state
// (RFC audit item #20) — desiredVal/desiredNo and snapVal/snapNo together
// form a 3-way state: unspecified (both zero), an explicit numeric bound, or
// an explicit "NO MINVALUE"/"NO MAXVALUE". An unspecified desired side never
// counts as a change (DPG doesn't manage what source never mentions); an
// explicit NO MINVALUE/NO MAXVALUE side changed if the snapshot wasn't
// already recorded that way (regardless of what numeric bound it held,
// since PostgreSQL doesn't expose one under NO MINVALUE/NO MAXVALUE either).
func seqBoundChanged(desiredVal *int64, desiredNo bool, snapVal *int64, snapNo bool) bool {
	if !desiredNo && desiredVal == nil {
		return false
	}
	if desiredNo {
		return !snapNo
	}
	return snapNo || !int64PtrEq(desiredVal, snapVal)
}

func int64PtrEq(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// desiredFloatChanged reports whether an explicitly-declared desired value
// (e.g. Function.Attrs.Cost/Rows) differs from the snapshot's. A nil
// desired means the source doesn't mention the attribute at all — that
// must never itself trigger drift, since the snapshot side always carries
// a concrete value (PostgreSQL's own catalog default, once introspected)
// even when nothing was ever declared. Only a genuinely explicit,
// different desired value counts as a change.
func desiredFloatChanged(desired, snap *float64) bool {
	if desired == nil {
		return false
	}
	return snap == nil || *desired != *snap
}

// parallelChanged compares a function's PARALLEL setting, treating an empty
// snapshot value as "unknown" (a pre-upgrade snapshot.json that predates this
// field) rather than a real "UNSAFE": desired is always concrete (the IR
// builder defaults unspecified source to "UNSAFE"), but snap can be "" purely
// from JSON's zero-value-on-missing-key behavior, which must never itself be
// read as a genuine difference.
func parallelChanged(desired, snap string) bool {
	if snap == "" {
		return false
	}
	return desired != snap
}

// transformsChanged compares a desired TRANSFORM FOR TYPE list (RFC audit
// item #26) against its flattened snapshot form, order-sensitive (matching
// how writeTransforms/RenderCreateFunctionSQL re-emits the list).
func transformsChanged(desired []ir.TypeRef, snap []string) bool {
	if len(desired) != len(snap) {
		return true
	}
	for i, t := range desired {
		if t.String() != snap[i] {
			return true
		}
	}
	return false
}

// splitConfigValueItems splits a SET-clause value on "," and strips one
// optional layer of surrounding single quotes from each item, for
// comparing a Section 9.2 FuncConfig's declared value (quoted, comma-joined
// by extractFuncAttrs' "set" case for a multi-valued GUC like search_path)
// against PostgreSQL's own bare/resolved proconfig text (introspectFuncConfigs)
// — same "both spellings must compare equal" reasoning as
// normalizeConfigValue, just applied per comma-separated item instead of to
// the whole string.
func splitConfigValueItems(v *string) []string {
	if v == nil {
		return nil
	}
	parts := strings.Split(*v, ",")
	out := make([]string, len(parts))
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if len(p) >= 2 && p[0] == '\'' && p[len(p)-1] == '\'' {
			p = p[1 : len(p)-1]
		}
		out[i] = p
	}
	return out
}

// funcConfigsChanged compares a FUNCTION/PROCEDURE's Section 9.2 SET-clause
// list against its snapshot. A FROM CURRENT-declared entry can never be
// distinguished from its resolved value on a live comparison (introspection
// only ever sees the resolved value, not which form was used — same
// accepted limitation already documented for RoleConfig's identical case),
// so a FromCurrent entry on either side is treated as always matching once
// its param is present on both; only a genuinely different param set or
// resolved value counts as changed.
func funcConfigsChanged(desired []ir.FuncConfig, snap []snapshot.SnapFuncConfig) bool {
	if len(desired) != len(snap) {
		return true
	}
	snapByParam := make(map[string]snapshot.SnapFuncConfig, len(snap))
	for _, s := range snap {
		snapByParam[s.Param] = s
	}
	for _, c := range desired {
		s, ok := snapByParam[c.Param]
		if !ok {
			return true
		}
		if c.FromCurrent || s.FromCurrent {
			continue
		}
		if !slices.Equal(splitConfigValueItems(c.Value), splitConfigValueItems(s.Value)) {
			return true
		}
	}
	return false
}

func boolPtrEq(a, b *bool) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func intPtrEq(a, b *int) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// roleAlterPasswordOp is the DiffOp for a standalone ALTER ROLE ... PASSWORD
// statement (an existing role's password changed, RFC Section 11.1) — same
// SecretBearingOp contract as roleCreateOp/subscriptionCreateOp: SQL() is
// always the placeholder/declared form, ExecSQL resolves it fresh for the
// one execution call only.
type roleAlterPasswordOp struct {
	*op
	name     string
	password string
}

func (o *roleAlterPasswordOp) ExecSQL(resolver pipeline.SecretResolver) (string, error) {
	resolved, err := pipeline.ResolveTemplate(o.password, resolver)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("ALTER ROLE %s PASSWORD %s;", quoteIdent(o.name), quoteLit(resolved)), nil
}

var _ pipeline.SecretBearingOp = (*roleAlterPasswordOp)(nil)

// stringSetDiff returns the elements present in desired but not current
// (added) and present in current but not desired (removed).
func stringSetDiff(desired, current []string) (added, removed []string) {
	curSet := make(map[string]bool, len(current))
	for _, s := range current {
		curSet[s] = true
	}
	desSet := make(map[string]bool, len(desired))
	for _, s := range desired {
		desSet[s] = true
		if !curSet[s] {
			added = append(added, s)
		}
	}
	for _, s := range current {
		if !desSet[s] {
			removed = append(removed, s)
		}
	}
	return added, removed
}

// roleMembershipKey identifies a RoleMembership/SnapRoleMembership entry for
// diffing purposes — real PostgreSQL's pg_auth_members has exactly one row
// per (parent, member) pair, so (Direction, Role) is a genuine identity, not
// an arbitrary choice: Admin/Inherit/Set are attributes of that one row, not
// part of its identity (see ir.RoleMembership's own doc comment for why this
// replaced two independently-managed ROLE/ADMIN buckets).
func roleMembershipKey(direction, role string) string { return direction + "|" + role }

// boolPtrDiffers reports whether an explicitly-declared *bool differs from
// its snapshot counterpart — nil on the desired side means "not declared,
// don't compare" (the same "declared, so managed" convention as
// Grant.GrantedBy); nil on the snapshot side (a prior declaration never
// recorded this option) is always "differs" once the desired side does
// declare it, since there is nothing to compare against but the option is
// now explicitly wanted.
func boolPtrDiffers(desired, snap *bool) bool {
	if desired == nil {
		return false
	}
	if snap == nil {
		return true
	}
	return *desired != *snap
}

// diffRoleMembership diffs IN ROLE/ROLE/ADMIN membership (RFC Section 11.1,
// audit item #32) as GRANT/REVOKE — PostgreSQL's ALTER ROLE has no
// membership clause at all (confirmed via pg_query: addroleto/rolemembers/
// adminmembers are CreateRoleStmt-only options), so an existing role's
// membership can only ever change via GRANT/REVOKE, the same mechanism
// PostgreSQL itself uses post-creation. A Direction bucket ("IN_ROLE" or
// "ROLE") is only managed when this run's o.Memberships has at least one
// entry for it — undeclared means "not managed by DPG for this role", the
// same convention every other optional Role field already uses (matches
// the pre-unification three-bucket nil-guards exactly, now correctly scoped
// by real catalog identity rather than by which bare keyword bucket an
// entry happened to be typed under).
func diffRoleMembership(o *ir.Role, snap *snapshot.SnapRole, pos pipeline.SourcePos) []pipeline.DiffOp {
	var ops []pipeline.DiffOp

	managedDirections := make(map[string]bool, 2)
	desiredByKey := make(map[string]ir.RoleMembership, len(o.Memberships))
	for _, m := range o.Memberships {
		managedDirections[m.Direction] = true
		desiredByKey[roleMembershipKey(m.Direction, m.Role)] = m
	}
	snapByKey := make(map[string]snapshot.SnapRoleMembership, len(snap.Memberships))
	for _, m := range snap.Memberships {
		snapByKey[roleMembershipKey(m.Direction, m.Role)] = m
	}

	for k, sm := range snapByKey {
		if !managedDirections[sm.Direction] {
			continue
		}
		if _, ok := desiredByKey[k]; !ok {
			ops = append(ops, cautionOp(roleMembershipRevokeSQL(o.Name, ir.RoleMembership{Role: sm.Role, Direction: sm.Direction}), pos))
		}
	}
	for k, m := range desiredByKey {
		sm, ok := snapByKey[k]
		if !ok || m.Admin != sm.Admin || boolPtrDiffers(m.Inherit, sm.Inherit) || boolPtrDiffers(m.Set, sm.Set) {
			ops = append(ops, safeOp(roleMembershipGrantSQL(o.Name, m), pos))
		}
	}
	return ops
}

func diffRole(o *ir.Role, snap *snapshot.SnapRole, live bool) []pipeline.DiffOp {
	var ops []pipeline.DiffOp
	pos := o.SrcPos
	ident := quoteIdent(o.Name)

	// RENAMED FROM on a role is schema-agnostic — roles are cluster-level,
	// not schema-scoped, so the generic cross-schema extension other kinds
	// use never applies here; only the bare RENAME TO form is meaningful.
	// This can close a gap that would otherwise be genuinely impossible to
	// work around: PostgreSQL refuses to DROP ROLE a role that still owns
	// any object, so drop-and-recreate isn't always a viable fallback.
	// Uses snap's matched (old) name for the FROM side; every later op in
	// this function uses ident (o.Name) for the already-renamed identity.
	if snap.Name != o.Name {
		ops = append(ops, cautionOp(
			fmt.Sprintf("ALTER ROLE %s RENAME TO %s;", quoteIdent(snap.Name), ident),
			pos,
		))
	}

	// Every non-PASSWORD, non-membership attribute the declaration manages,
	// batched into one ALTER ROLE (mirrors diffSequence's identical
	// batching of its own optional params into one ALTER SEQUENCE).
	attrsChanged := (o.CanLogin != nil && !boolPtrEq(o.CanLogin, snap.CanLogin)) ||
		(o.Superuser != nil && !boolPtrEq(o.Superuser, snap.Superuser)) ||
		(o.CreateDB != nil && !boolPtrEq(o.CreateDB, snap.CreateDB)) ||
		(o.CreateRole != nil && !boolPtrEq(o.CreateRole, snap.CreateRole)) ||
		(o.Inherit != nil && !boolPtrEq(o.Inherit, snap.Inherit)) ||
		(o.IsReplication != nil && !boolPtrEq(o.IsReplication, snap.IsReplication)) ||
		(o.BypassRLS != nil && !boolPtrEq(o.BypassRLS, snap.BypassRLS)) ||
		(o.ConnectionLimit != nil && !intPtrEq(o.ConnectionLimit, snap.ConnectionLimit)) ||
		(o.ValidUntil != nil && !ptrEq(o.ValidUntil, snap.ValidUntil))
	if attrsChanged {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER ROLE %s%s;", ident, roleAttrClause(o, nil)), pos))
	}

	// PASSWORD is never compared as raw text — only its hash (of the
	// declared text, never the resolved value) is ever stored, so this is
	// the only way to detect a change offline (RFC Section 11.1's "Password drift
	// detection"). A live snapshot has no hash to compare against (the
	// catalog never shows passwords), so a password is not diffed there.
	if !live && o.Password != nil && hashText(*o.Password) != snap.PasswordHash {
		ops = append(ops, &roleAlterPasswordOp{
			op:       cautionOp(fmt.Sprintf("ALTER ROLE %s PASSWORD %s;", ident, quoteLit(*o.Password)), pos),
			name:     o.Name,
			password: *o.Password,
		})
	}

	if !ptrEq(o.Comment, snap.Comment) {
		if o.Comment != nil {
			ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON ROLE %s IS %s;", ident, quoteLit(*o.Comment)), pos))
		} else {
			ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON ROLE %s IS NULL;", ident), pos))
		}
	}

	ops = append(ops, diffRoleMembership(o, snap, pos)...)
	ops = append(ops, diffRoleConfigs(o, snap, pos)...)
	ops = append(ops, diffSecurityLabelSet(snap.SecurityLabels, o.SecurityLabels, "ROLE "+ident, pos)...)
	return ops
}

// roleConfigKey identifies a role-config-dir entry (RFC audit item #74) for
// diffing purposes: the (param-or-ALL, database) pair — real PostgreSQL
// tracks per-role session config per-database independently via
// pg_db_role_setting, so the same param SET differently IN DATABASE a vs IN
// DATABASE b vs cluster-wide are three genuinely distinct settings, not one.
func roleConfigKey(param string, resetAll bool, inDatabase *string) string {
	db := ""
	if inDatabase != nil {
		db = *inDatabase
	}
	if resetAll {
		return "\x00ALL|" + db
	}
	return strings.ToLower(param) + "|" + db
}

// roleConfigSQL renders one role-config-dir entry as ALTER ROLE ... [IN
// DATABASE db] SET/RESET ... — param is real PostgreSQL var_name grammar
// (ColId ('.' ColId)*, e.g. a namespaced GUC like pg_stat_statements.track),
// never quoted like an ordinary identifier.
func roleConfigSQL(ident string, c ir.RoleConfig) string {
	var b strings.Builder
	b.WriteString("ALTER ROLE ")
	b.WriteString(ident)
	if c.InDatabase != nil {
		b.WriteString(" IN DATABASE ")
		b.WriteString(quoteIdent(*c.InDatabase))
	}
	if c.Reset {
		b.WriteString(" RESET ")
		if c.ResetAll {
			b.WriteString("ALL")
		} else {
			b.WriteString(c.Param)
		}
	} else {
		b.WriteString(" SET ")
		b.WriteString(c.Param)
		if c.FromCurrent {
			b.WriteString(" FROM CURRENT")
		} else {
			b.WriteString(" = ")
			if c.Value != nil {
				b.WriteString(*c.Value)
			}
		}
	}
	b.WriteString(";")
	return b.String()
}

// normalizeConfigValue strips one optional layer of surrounding single
// quotes for comparison purposes only, mirroring flattenParams' identical
// reasoning for Table.StorageParams: a live-introspected value always comes
// back bare/resolved (PostgreSQL doesn't preserve original quote style, see
// introspectRoleConfigs' doc comment), while a hand-written declaration may
// or may not quote it — both spellings must compare equal.
func normalizeConfigValue(v *string) string {
	if v == nil {
		return ""
	}
	s := *v
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return s[1 : len(s)-1]
	}
	return s
}

// roleConfigEqual reports whether a desired role-config-dir entry already
// matches its snapshot counterpart — content only, not identity (the caller
// has already matched them by roleConfigKey).
func roleConfigEqual(c ir.RoleConfig, s snapshot.SnapRoleConfig) bool {
	return c.FromCurrent == s.FromCurrent && c.Reset == s.Reset && c.ResetAll == s.ResetAll &&
		normalizeConfigValue(c.Value) == normalizeConfigValue(s.Value)
}

// diffRoleConfigs diffs RFC audit item #74's role-config-dir set (Section
// 11.1). Matches diffGrantSet's additive spirit: a new or changed entry
// emits the ALTER ROLE SET/RESET; an entry simply removed from source emits
// nothing (RESET is itself an explicit, available directive — the same
// "declared, so managed" convention already used throughout, not an
// oversight).
func diffRoleConfigs(o *ir.Role, snap *snapshot.SnapRole, pos pipeline.SourcePos) []pipeline.DiffOp {
	var ops []pipeline.DiffOp
	ident := quoteIdent(o.Name)

	snapByKey := make(map[string]snapshot.SnapRoleConfig, len(snap.Configs))
	for _, s := range snap.Configs {
		snapByKey[roleConfigKey(s.Param, s.ResetAll, s.InDatabase)] = s
	}
	for _, c := range o.Configs {
		k := roleConfigKey(c.Param, c.ResetAll, c.InDatabase)
		if s, ok := snapByKey[k]; ok && roleConfigEqual(c, s) {
			continue
		}
		ops = append(ops, safeOp(roleConfigSQL(ident, c), pos))
	}
	return ops
}

func diffSchema(o *ir.Schema, snap *snapshot.SnapSchema) []pipeline.DiffOp {
	var ops []pipeline.DiffOp
	pos := o.SrcPos

	// Rename: snap stores the old name; desired has the new name.
	if snap.Name != o.Name {
		ops = append(ops, safeOp(
			fmt.Sprintf("ALTER SCHEMA %s RENAME TO %s;", quoteIdent(snap.Name), quoteIdent(o.Name)),
			pos,
		))
	}
	if !ptrEq(o.Owner, snap.Owner) && o.Owner != nil {
		ops = append(ops, safeOp(
			fmt.Sprintf("ALTER SCHEMA %s OWNER TO %s;", quoteIdent(o.Name), quoteIdent(*o.Owner)),
			pos,
		))
	}
	if !ptrEq(o.Comment, snap.Comment) {
		if o.Comment != nil {
			ops = append(ops, safeOp(
				fmt.Sprintf("COMMENT ON SCHEMA %s IS %s;", quoteIdent(o.Name), quoteLit(*o.Comment)),
				pos,
			))
		} else {
			ops = append(ops, safeOp(
				fmt.Sprintf("COMMENT ON SCHEMA %s IS NULL;", quoteIdent(o.Name)),
				pos,
			))
		}
	}
	schemaIdent := quoteIdent(o.Name)
	ops = append(ops, diffGrantSet(snap.Grants, o.Grants, "SCHEMA "+schemaIdent, pos)...)
	ops = append(ops, diffRevocationSet(snap.Revocations, o.Revocations, "SCHEMA "+schemaIdent, pos)...)
	ops = append(ops, diffSecurityLabelSet(snap.SecurityLabels, o.SecurityLabels, "SCHEMA "+schemaIdent, pos)...)
	return ops
}

// viewOutputColumnsChanged reports whether a view's output column list —
// compared by name and ordinal position, per RFC Section 8.1 — differs between the
// previous (snap) and desired query text. This is exactly what CREATE OR
// REPLACE VIEW itself enforces: PostgreSQL rejects a replacement whose
// implicit or explicit output column name would change (SQLSTATE 42P16),
// most commonly hit by an unaliased "SELECT col FROM t" whose source column
// got renamed elsewhere in the same migration. ok=false means the column
// list couldn't be confidently determined for one or both sides (e.g. a
// `SELECT *`/`t.*` expansion, whose true column count needs catalog
// access) — callers should treat that the same as "unchanged," preserving
// the prior (non-regressive) CREATE OR REPLACE behavior for those cases
// rather than risk a false DESTRUCTIVE reclassification.
// viewQueryChanged reports whether a view's query differs from the
// snapshot's. When exactly one side was read from the live catalog and
// the declared side selects `*`, the catalog side holds PostgreSQL's
// expansion of it, so the two are compared without their select lists
// (RFC Section 8.1).
func viewQueryChanged(o *ir.View, snap *snapshot.SnapView) bool {
	if normalizeWS(o.Query) == normalizeWS(snap.Query) {
		return false
	}
	if o.Reconstructed == snap.Reconstructed {
		return true
	}
	declared := o.Query
	if o.Reconstructed {
		declared = snap.Query
	}
	if !querySelectsStar(declared) {
		return true
	}
	a, ok1 := withoutSelectLists(o.Query)
	b, ok2 := withoutSelectLists(snap.Query)
	return !ok1 || !ok2 || a != b
}

// querySelectsStar reports whether a SELECT's output list, or that of any
// branch of a set operation, contains `*` or `t.*`.
func querySelectsStar(query string) bool {
	res, err := pg_query.Parse(query)
	if err != nil || len(res.Stmts) == 0 {
		return false
	}
	found := false
	forEachOutputSelect(res.Stmts[0].GetStmt().GetSelectStmt(), func(sel *pg_query.SelectStmt) {
		for _, t := range sel.TargetList {
			if rt := t.GetResTarget(); rt != nil && viewTargetIsStar(rt.Val) {
				found = true
			}
		}
	})
	return found
}

// withoutSelectLists returns query deparsed with the output lists of its
// SELECT (every branch of a set operation) removed.
func withoutSelectLists(query string) (string, bool) {
	res, err := pg_query.Parse(query)
	if err != nil || len(res.Stmts) != 1 {
		return "", false
	}
	sel := res.Stmts[0].GetStmt().GetSelectStmt()
	if sel == nil {
		return "", false
	}
	forEachOutputSelect(sel, func(s *pg_query.SelectStmt) { s.TargetList = nil })
	out, err := pg_query.Deparse(res)
	if err != nil {
		return "", false
	}
	return normalizeWS(out), true
}

// forEachOutputSelect calls fn for sel, or for each leaf SELECT of a set
// operation.
func forEachOutputSelect(sel *pg_query.SelectStmt, fn func(*pg_query.SelectStmt)) {
	if sel == nil {
		return
	}
	if sel.Op != pg_query.SetOperation_SETOP_NONE {
		forEachOutputSelect(sel.Larg, fn)
		forEachOutputSelect(sel.Rarg, fn)
		return
	}
	fn(sel)
}

func viewOutputColumnsChanged(desiredQuery, snapQuery string) (changed bool, ok bool) {
	desiredNames, ok1 := viewOutputColumnNames(desiredQuery)
	if !ok1 {
		return false, false
	}
	snapNames, ok2 := viewOutputColumnNames(snapQuery)
	if !ok2 {
		return false, false
	}
	if len(desiredNames) != len(snapNames) {
		return true, true
	}
	for i, n := range desiredNames {
		if n != snapNames[i] {
			return true, true
		}
	}
	return false, true
}

// viewOutputColumnNames extracts the implicit or explicit output column
// name for each item in query's top-level SELECT target list. An explicit
// alias always wins; otherwise ir.FigureColname computes PostgreSQL's own
// implicit name for the common expression shapes (a bare column reference,
// a function call, a cast/COLLATE wrapper, ...). Any other expression
// shape it can't name (arithmetic, CASE, a subquery, ...) is bucketed
// under PostgreSQL's own literal fallback, "?column?" — not a fully
// faithful reproduction of every FigureColnameInternal case, but safe here:
// two unaliased complex expressions at the same position only compare
// equal when they'd both fall into that same generic PostgreSQL bucket
// too, which is all this comparison needs. Returns ok=false when the query
// can't be parsed, isn't a plain SELECT (or a set-operation whose leftmost
// leaf is), or contains a `*`/`t.*` star expansion, whose true output
// column count isn't knowable without catalog access.
func viewOutputColumnNames(query string) ([]string, bool) {
	res, err := pg_query.Parse(query)
	if err != nil || len(res.Stmts) == 0 {
		return nil, false
	}
	sel := res.Stmts[0].GetStmt().GetSelectStmt()
	if sel == nil {
		return nil, false
	}
	// A set-operation (UNION/INTERSECT/EXCEPT) node has no target list of
	// its own — only its leaf statements do — and PostgreSQL takes the
	// leftmost leaf's column names for the combined result, so descend
	// through Larg until a plain SELECT with a target list is reached.
	for sel.Larg != nil {
		sel = sel.Larg
	}
	if len(sel.TargetList) == 0 {
		return nil, false
	}
	names := make([]string, 0, len(sel.TargetList))
	for _, node := range sel.TargetList {
		rt := node.GetResTarget()
		if rt == nil {
			return nil, false
		}
		if rt.Name != "" {
			names = append(names, rt.Name)
			continue
		}
		if viewTargetIsStar(rt.Val) {
			return nil, false
		}
		name, _ := ir.FigureColname(rt.Val)
		if name == "" {
			name = "?column?"
		}
		names = append(names, name)
	}
	return names, true
}

// viewTargetIsStar reports whether val is a "*" or "t.*" star expansion —
// the one shape viewOutputColumnNames can't assign a single name to at
// all, since it stands for an a-priori unknown number of real columns.
func viewTargetIsStar(val *pg_query.Node) bool {
	cr := val.GetColumnRef()
	if cr == nil {
		return false
	}
	for _, f := range cr.Fields {
		if f.GetAStar() != nil {
			return true
		}
	}
	return false
}

func diffView(o *ir.View, snap *snapshot.SnapView) ([]pipeline.DiffOp, error) {
	var ops []pipeline.DiffOp
	pos := o.SrcPos
	tbl := qualIdent(o.Schema, o.Name)
	viewKind := "VIEW"
	if o.Materialized {
		viewKind = "MATERIALIZED VIEW"
	}

	// SET SCHEMA / RENAME TO, in that order — see diffTable's identical
	// reasoning. Uses viewKind so a materialized view emits
	// ALTER MATERIALIZED VIEW, not ALTER VIEW (real PostgreSQL rejects
	// ALTER VIEW against a matview relkind).
	if snap.Schema != o.Schema {
		ops = append(ops, safeOp(
			fmt.Sprintf("ALTER %s %s SET SCHEMA %s;", viewKind, qualIdent(snap.Schema, snap.Name), quoteIdent(o.Schema)),
			pos,
		))
	}
	if snap.Name != o.Name {
		ops = append(ops, cautionOp(
			fmt.Sprintf("ALTER %s %s RENAME TO %s;", viewKind, qualIdent(o.Schema, snap.Name), quoteIdent(o.Name)),
			pos,
		))
	}

	// Recursive flag change or a materialized view query change requires DROP + CREATE
	// because PG has no in-place ALTER for these.
	// A column list is only kept for a query with `*` (ir.View.Columns).
	// The live catalog can't show one, so it is only compared when
	// neither side was read from the catalog.
	columnsChanged := !o.Reconstructed && !snap.Reconstructed && !slices.Equal(o.Columns, snap.Columns)
	queryChanged := viewQueryChanged(o, snap)
	if snap.Recursive != o.Recursive || columnsChanged || (o.Materialized && queryChanged) {
		ops = append(ops, destructiveOp(fmt.Sprintf("DROP %s IF EXISTS %s;", viewKind, tbl), pos))
		ops = append(ops, createView(o)...)
		// createView emits comments and grants; nothing more to do.
		return ops, nil
	}

	if queryChanged {
		// RFC Section 8.1: "Output column list changed (any way) → DROP VIEW
		// CASCADE; CREATE VIEW → DESTRUCTIVE." Real PostgreSQL enforces this
		// itself — CREATE OR REPLACE VIEW rejects a query whose replacement
		// would change an existing output column's implicit or explicit
		// name (SQLSTATE 42P16) — so emitting CREATE OR REPLACE
		// unconditionally on every query-text change used to abort the
		// whole migration on the common case of renaming a source column an
		// unaliased "SELECT col FROM t" view references. viewOutputColumnsChanged
		// only escalates when it can actually prove the column list
		// changed; anything it can't confidently analyze (e.g. a `SELECT *`
		// expansion) falls back to the prior, non-regressive behavior.
		if changed, ok := viewOutputColumnsChanged(o.Query, snap.Query); ok && changed {
			ops = append(ops, destructiveOp(fmt.Sprintf("DROP %s IF EXISTS %s CASCADE;", viewKind, tbl), pos))
			ops = append(ops, createView(o)...)
			return ops, nil
		}
		// CREATE OR REPLACE VIEW resets every view option it doesn't
		// restate, so it carries the desired WITH (...) list.
		ops = append(ops, safeOp(fmt.Sprintf("CREATE OR REPLACE VIEW %s AS %s;", viewHead(o), o.Query), pos))
	} else if !o.Materialized {
		ops = append(ops, diffStorageParamsSetReset("ALTER VIEW", tbl, o.StorageParams, snap.StorageParams, pos)...)
	}

	if o.Materialized && snap.WithNoData != o.WithNoData {
		ops = append(ops, instructionOp(pipeline.Safe,
			fmt.Sprintf("-- WITH NO DATA changed on %s %s; refresh manually: REFRESH MATERIALIZED VIEW %s;",
				viewKind, tbl, tbl),
			pos,
		))
	}

	// TABLESPACE/WITH (...) (Section 8.2) — materialized-view-only, targeted
	// SAFE ALTERs, confirmed live against a real PostgreSQL server, mirroring
	// Table.Tablespace/StorageParams' identical treatment.
	if o.Materialized {
		if !ptrEq(o.Tablespace, snap.Tablespace) && o.Tablespace != nil {
			ops = append(ops, safeOp(fmt.Sprintf("ALTER MATERIALIZED VIEW %s SET TABLESPACE %s;", tbl, quoteIdent(*o.Tablespace)), pos))
		}
		ops = append(ops, diffStorageParamsSetReset("ALTER MATERIALIZED VIEW", tbl, o.StorageParams, snap.StorageParams, pos)...)
	}

	if desiredTxt, snapTxt := effectiveComment(o.Comment, o.Deprecated), effectiveComment(snap.Comment, snap.Deprecated); !ptrEq(desiredTxt, snapTxt) {
		if desiredTxt != nil {
			ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON %s %s IS %s;", viewKind, tbl, quoteLit(*desiredTxt)), pos))
		} else {
			ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON %s %s IS NULL;", viewKind, tbl), pos))
		}
	}

	if !ptrEq(o.Owner, snap.Owner) && o.Owner != nil {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER %s %s OWNER TO %s;", viewKind, tbl, quoteIdent(*o.Owner)), pos))
	}

	ops = append(ops, diffGrantSet(snap.Grants, o.Grants, "TABLE "+tbl, pos)...)
	ops = append(ops, diffRevocationSet(snap.Revocations, o.Revocations, "TABLE "+tbl, pos)...)
	ops = append(ops, diffSecurityLabelSet(snap.SecurityLabels, o.SecurityLabels, viewKind+" "+tbl, pos)...)
	viewIdxOps, err := diffViewIndexes(o.Schema, o.Name, o.Indexes, snap.Indexes)
	if err != nil {
		return nil, err
	}
	ops = append(ops, viewIdxOps...)
	return ops, nil
}

func diffFunction(o *ir.Function, snap *snapshot.SnapFunction) []pipeline.DiffOp {
	var ops []pipeline.DiffOp
	pos := o.SrcPos
	sig := buildFuncSignature(o)

	// A return-type change (including toggling SETOF, or a RETURNS TABLE(...)
	// column-list edit) needs DROP + CREATE, not CREATE OR REPLACE: confirmed
	// live against postgres:17 that PG rejects an in-place return-type change
	// outright ("cannot change return type of existing function", hinting to
	// DROP FUNCTION first) — the same DROP-required class of change diffView
	// already handles for a materialized view's query. This also closes a
	// genuinely separate, pre-existing gap found while wiring this up:
	// ReturnType was never compared here at all before, so ANY return-type-
	// only change (SETOF or otherwise) silently went undetected.
	//
	// ReturnTable is compared separately from ReturnType/ReturnsSet: a RETURNS
	// TABLE(...) function's ReturnType is always "record"/SetOf=true no matter
	// what its column list is (that's how PostgreSQL's own catalog represents
	// it — prorettype is genuinely 'record' regardless), so two functions with
	// different TABLE column lists would look identical to that check alone.
	returnTable := ir.FormatTableColumns(ir.FuncTableColumns(o.Args))
	if o.ReturnType.String() != snap.ReturnType || o.ReturnType.SetOf != snap.ReturnsSet || returnTable != snap.ReturnTable {
		ops = append(ops, destructiveOp(fmt.Sprintf("DROP FUNCTION IF EXISTS %s;", sig), pos))
		ops = append(ops, createFunction(o)...)
		return ops
	}

	// SET SCHEMA / RENAME TO — mirrors diffProcedure/diffAggregate's
	// identical fix (RFC audit items #10/#11), same ordering as diffTable's
	// identical mechanism: SET SCHEMA first (old_schema.old_sig -> new
	// schema), then RENAME TO (new_schema.old_sig -> new_name).
	// ir.Function.RenamedFrom was previously used for identity matching only:
	// a rename was correctly matched (no DROP+CREATE) but the live function
	// itself was never actually renamed, a silent no-op on the rename half;
	// SET SCHEMA was never emitted at all, so a cross-schema RENAMED FROM
	// matched and renamed in place but never actually moved the function.
	if snap.Schema != o.Schema {
		oldSig := fmt.Sprintf("%s(%s)", qualIdent(snap.Schema, snap.Name), ir.ArgsKey(o.Args))
		ops = append(ops, safeOp(fmt.Sprintf("ALTER FUNCTION %s SET SCHEMA %s;", oldSig, quoteIdent(o.Schema)), pos))
	}
	if snap.Name != o.Name {
		oldSig := fmt.Sprintf("%s(%s)", qualIdent(o.Schema, snap.Name), ir.ArgsKey(o.Args))
		ops = append(ops, cautionOp(fmt.Sprintf("ALTER FUNCTION %s RENAME TO %s;", oldSig, quoteIdent(o.Name)), pos))
	}

	// Re-create if: desired has a hash and it differs from the snapshot (including
	// "" when the snapshot predates body-hash tracking), or language/volatility/
	// parallel changed, or an EXPLICITLY declared Cost/Rows differs. Cost/Rows
	// use desiredFloatChanged rather than a plain !=: an unspecified COST/ROWS
	// in source (o.Attrs.Cost == nil) must never trigger a recreate on its own,
	// the same "only act when the desired side actually declares it" rule
	// already established for column STORAGE — otherwise every function that
	// has never mentioned COST/ROWS would show permanent drift the moment
	// introspection (or a prior apply) records PostgreSQL's own concrete
	// default value for it.
	//
	// Parallel needs the same "don't trust an absent value" treatment, but from
	// the opposite side: unlike Cost/Rows (a *float64, nil on the DESIRED side
	// means "not declared"), Parallel is a plain string that the IR builder
	// ALWAYS sets to a concrete default ("UNSAFE") even when source never
	// mentions PARALLEL — so o.Attrs.Parallel is never "". snap.Parallel CAN be
	// "" though: any snapshot.json written before this field existed has no
	// "parallel" key at all, so JSON unmarshalling leaves it at the Go zero
	// value. A bare != would then compare "UNSAFE" against "" for every single
	// function in every pre-existing project and spuriously recreate all of
	// them on the first plan/apply after upgrading. parallelChanged treats an
	// empty snapshot value as "unknown, don't diff yet" — self-healing after
	// that first apply records a real value.
	if (o.BodyHash != "" && o.BodyHash != snap.BodyHash) ||
		o.Attrs.Language != snap.Language || o.Attrs.Volatility != snap.Volatility ||
		parallelChanged(o.Attrs.Parallel, snap.Parallel) ||
		desiredFloatChanged(o.Attrs.Cost, snap.Cost) || desiredFloatChanged(o.Attrs.Rows, snap.Rows) ||
		o.Attrs.Leakproof != snap.Leakproof || transformsChanged(o.Attrs.Transforms, snap.Transforms) ||
		!ptrEq(o.Attrs.ObjFile, snap.ObjFile) || !ptrEq(o.Attrs.LinkSymbol, snap.LinkSymbol) ||
		o.Attrs.AtomicBody != snap.AtomicBody ||
		o.Attrs.Strict != snap.Strict || o.Attrs.SecurityDef != snap.SecurityDef ||
		funcConfigsChanged(o.Attrs.Config, snap.Config) {
		ops = append(ops, safeOp(buildFunctionSQL(o), pos))
	}
	if !ptrEq(o.Owner, snap.Owner) && o.Owner != nil {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER FUNCTION %s OWNER TO %s;", sig, quoteIdent(*o.Owner)), pos))
	}
	if desiredTxt, snapTxt := effectiveComment(o.Comment, o.Deprecated), effectiveComment(snap.Comment, snap.Deprecated); !ptrEq(desiredTxt, snapTxt) {
		if desiredTxt != nil {
			ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON FUNCTION %s IS %s;", sig, quoteLit(*desiredTxt)), pos))
		} else {
			ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON FUNCTION %s IS NULL;", sig), pos))
		}
	}
	ops = append(ops, diffGrantSet(snap.Grants, o.Grants, "FUNCTION "+sig, pos)...)
	ops = append(ops, diffRevocationSet(snap.Revocations, o.Revocations, "FUNCTION "+sig, pos)...)
	ops = append(ops, diffSecurityLabelSet(snap.SecurityLabels, o.SecurityLabels, "FUNCTION "+sig, pos)...)
	ops = append(ops, diffDependsOnExtension("FUNCTION", sig, snap.DependsOnExtensions, o.DependsOnExtensions, pos)...)
	return ops
}

// diffDependsOnExtension diffs Section 9.1's `[NO] DEPENDS ON EXTENSION`
// set — shared by Function and Procedure (real PostgreSQL has no ALTER
// AGGREGATE equivalent). objKW is "FUNCTION"/"PROCEDURE"; sig is the
// already-built "name(args)" signature.
func diffDependsOnExtension(objKW, sig string, current, desired []string, pos pipeline.SourcePos) []pipeline.DiffOp {
	added, removed := stringSetDiff(desired, current)
	var ops []pipeline.DiffOp
	for _, ext := range added {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER %s %s DEPENDS ON EXTENSION %s;", objKW, sig, quoteIdent(ext)), pos))
	}
	for _, ext := range removed {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER %s %s NO DEPENDS ON EXTENSION %s;", objKW, sig, quoteIdent(ext)), pos))
	}
	return ops
}

func diffType(o *ir.Type, snap *snapshot.SnapType, fullSnap *pipeline.Snapshot) ([]pipeline.DiffOp, error) {
	var ops []pipeline.DiffOp
	pos := o.SrcPos
	typeIdent := qualIdent(o.Schema, o.Name)

	// OWNER TO applies identically across every variant (only DOMAIN uses a
	// distinct ALTER verb, matching the COMMENT ON DOMAIN/TYPE split already
	// used per-branch below), so it's diffed once here rather than
	// duplicated into each variant branch.
	if !ptrEq(o.Owner, snap.Owner) && o.Owner != nil {
		alterKW := "TYPE"
		if o.Variant == "DOMAIN" {
			alterKW = "DOMAIN"
		}
		ops = append(ops, safeOp(fmt.Sprintf("ALTER %s %s OWNER TO %s;", alterKW, typeIdent, quoteIdent(*o.Owner)), pos))
	}

	// SET SCHEMA / RENAME TO apply identically across every variant too,
	// same reasoning as OWNER TO above and the same ordering as diffTable's
	// identical mechanism: SET SCHEMA first (old_schema.old_name -> new
	// schema), then RENAME TO (new_schema.old_name -> new_name).
	renameAlterKW := "TYPE"
	if o.Variant == "DOMAIN" {
		renameAlterKW = "DOMAIN"
	}
	if snap.Schema != o.Schema {
		ops = append(ops, safeOp(
			fmt.Sprintf("ALTER %s %s SET SCHEMA %s;", renameAlterKW, qualIdent(snap.Schema, snap.Name), quoteIdent(o.Schema)),
			pos,
		))
	}
	if snap.Name != o.Name {
		ops = append(ops, cautionOp(
			fmt.Sprintf("ALTER %s %s RENAME TO %s;", renameAlterKW, qualIdent(o.Schema, snap.Name), quoteIdent(o.Name)),
			pos,
		))
	}

	if o.Variant == "COMPOSITE" && snap.Variant == "COMPOSITE" {
		attrOps, err := diffCompositeAttrs(typeIdent, o.CompositeAttrs, snap.CompositeAttrs)
		if err != nil {
			return nil, err
		}
		ops = append(ops, attrOps...)
		if !ptrEq(o.Comment, snap.Comment) {
			if o.Comment != nil {
				ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON TYPE %s IS %s;", typeIdent, quoteLit(*o.Comment)), pos))
			} else {
				ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON TYPE %s IS NULL;", typeIdent), pos))
			}
		}
		ops = append(ops, diffTypeGrantOps(o, snap, typeIdent, pos)...)
		ops = append(ops, diffSecurityLabelSet(snap.SecurityLabels, o.SecurityLabels, "TYPE "+typeIdent, pos)...)
		return ops, nil
	}

	if o.Variant == "DOMAIN" && snap.Variant == "DOMAIN" {
		// RFC Section 5.4: unlike RANGE/BASE (hash-diffed opaque bodies, DROP+CREATE
		// on any change), a domain's properties are each diffed and altered
		// individually — DEFAULT/NOT NULL/constraint changes never require
		// recreating the domain, only a base-type change does. Found live-
		// testing a demo project: diffType had no case for DOMAIN at all, so
		// none of this was ever diffed — only the generic COMMENT check
		// (shared by every variant, below) ever fired for an already-applied
		// domain.
		// Same "reconstructed/stale snapshot, no comparison possible yet"
		// guard as RANGE/BASE below: a snapshot written before this
		// structured-field tracking existed has snap.DomainBaseType == ""
		// (Go zero value, never populated) even though the domain is real
		// and unchanged. Found live-testing a demo project's pre-existing
		// email_address domain: without this guard, every already-applied
		// domain looked like a base-type change on the very first plan
		// after upgrading, proposing a destructive DROP DOMAIN CASCADE
		// that would have cascade-dropped any column using it. A domain
		// always has a non-empty base type, so an empty snap value can
		// only mean "not yet populated" — skip structural diffing entirely
		// and fall through to the COMMENT check, which still works since
		// Comment was already tracked before this change.
		if snap.DomainBaseType == "" {
			if !ptrEq(o.Comment, snap.Comment) {
				if o.Comment != nil {
					ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON DOMAIN %s IS %s;", typeIdent, quoteLit(*o.Comment)), pos))
				} else {
					ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON DOMAIN %s IS NULL;", typeIdent), pos))
				}
			}
			ops = append(ops, diffTypeGrantOps(o, snap, typeIdent, pos)...)
			ops = append(ops, diffSecurityLabelSet(snap.SecurityLabels, o.SecurityLabels, "DOMAIN "+typeIdent, pos)...)
			// Same PROTECTED-field precedent as diffTable: if nothing else
			// changed, ops would be empty and apply's len(ops)==0
			// short-circuit skips Populate, so snap.DomainBaseType would
			// stay "" forever and this branch would never self-heal. A
			// harmless comment op forces the snapshot refresh on the very
			// next apply.
			if len(ops) == 0 {
				ops = append(ops, safeOp(fmt.Sprintf("-- refresh snapshot metadata for domain %s", typeIdent), pos))
			}
			return ops, nil
		}
		if o.DomainBaseType.String() != snap.DomainBaseType || o.DomainCollation != snap.DomainCollation {
			// PG has no ALTER DOMAIN ... TYPE; the base type (and its
			// COLLATE, RFC Section 5.4's own diffing table) is fixed at creation.
			// RFC Section 5.4 explicitly requires DROP DOMAIN CASCADE for either.
			ops = append(ops, destructiveOp(fmt.Sprintf("DROP DOMAIN IF EXISTS %s CASCADE;", typeIdent), pos))
			ops = append(ops, createType(o)...)
			return ops, nil
		}
		if !ptrEq(o.DomainDefault, snap.DomainDefault) {
			if o.DomainDefault != nil {
				ops = append(ops, safeOp(fmt.Sprintf("ALTER DOMAIN %s SET DEFAULT %s;", typeIdent, *o.DomainDefault), pos))
			} else {
				ops = append(ops, safeOp(fmt.Sprintf("ALTER DOMAIN %s DROP DEFAULT;", typeIdent), pos))
			}
		}
		if o.DomainNotNull != snap.DomainNotNull {
			if o.DomainNotNull {
				// Existing rows may already violate it — PG validates on SET,
				// unlike DROP which can never fail.
				ops = append(ops, cautionOp(fmt.Sprintf("ALTER DOMAIN %s SET NOT NULL;", typeIdent), pos))
			} else {
				ops = append(ops, safeOp(fmt.Sprintf("ALTER DOMAIN %s DROP NOT NULL;", typeIdent), pos))
			}
		}
		// Constraints are matched by name; an unnamed CHECK (Name == "") is
		// left undiffed — same "genuinely hard, no reliable identity to
		// match on" class as unnamed table constraints before that was
		// separately solved with PG's full auto-naming algorithm, out of
		// scope here since the RFC's own worked example always names them.
		snapByName := make(map[string]snapshot.SnapConstraint, len(snap.DomainConstraints))
		for _, c := range snap.DomainConstraints {
			if c.Name != "" {
				snapByName[c.Name] = c
			}
		}
		desiredByName := make(map[string]*ir.Constraint, len(o.DomainConstraints))
		for _, c := range o.DomainConstraints {
			if c.Name != "" {
				desiredByName[c.Name] = c
			}
		}

		// Constraints renamed in desired: map old name -> new name, validated
		// the same shape as diffConstraints/diffIndexes' RENAMED FROM
		// handling (Section 7.4).
		renamedFrom := make(map[string]string) // snap name -> desired name
		for _, c := range o.DomainConstraints {
			if c.RenamedFrom == nil {
				continue
			}
			if _, collide := desiredByName[*c.RenamedFrom]; collide {
				return nil, pipeline.Errorf(c.Pos,
					"RENAMED FROM %q on domain constraint %q collides with another constraint of the same name in the desired declaration. Remove the stale constraint.",
					*c.RenamedFrom, c.Name)
			}
			_, oldInSnap := snapByName[*c.RenamedFrom]
			_, newInSnap := snapByName[c.Name]
			if newInSnap {
				continue // post-apply / no-op state
			}
			if !oldInSnap {
				return nil, pipeline.ErrorfCode(c.Pos, "DPG-E021",
					"RENAMED FROM %q on domain constraint %q does not match the snapshot — neither the old nor the new name exists there. Remove RENAMED FROM if this is a genuinely new constraint.",
					*c.RenamedFrom, c.Name)
			}
			renamedFrom[*c.RenamedFrom] = c.Name
		}

		for name := range snapByName {
			if _, ok := desiredByName[name]; ok {
				continue
			}
			if _, wasRenamed := renamedFrom[name]; wasRenamed {
				continue
			}
			ops = append(ops, safeOp(fmt.Sprintf("ALTER DOMAIN %s DROP CONSTRAINT %s;", typeIdent, quoteIdent(name)), pos))
		}
		for name, c := range desiredByName {
			sc, existed := snapByName[name]
			if !existed && c.RenamedFrom != nil {
				if oldSc, ok := snapByName[*c.RenamedFrom]; ok {
					sc, existed = oldSc, true
				}
			}
			if !existed || sc.Expr != c.Expr {
				if existed {
					// Same identity, different expression: PG has no ALTER
					// DOMAIN ... ALTER CONSTRAINT for the check expression
					// itself, so replace it via drop + add. Drops under sc's
					// CURRENT (matched-snapshot) name, not the desired name —
					// those can differ when this is also a rename.
					ops = append(ops, safeOp(fmt.Sprintf("ALTER DOMAIN %s DROP CONSTRAINT %s;", typeIdent, quoteIdent(sc.Name)), pos))
				}
				// NOT VALID lifecycle (Section 5.4, same as the table-level
				// feature Constraint.NotValid already exists for): a newly
				// added or replaced constraint can declare NOT VALID to skip
				// PostgreSQL's own existing-row validation at ADD time.
				notValid := ""
				if c.NotValid {
					notValid = " NOT VALID"
				}
				ops = append(ops, cautionOp(fmt.Sprintf("ALTER DOMAIN %s ADD CONSTRAINT %s %s%s;", typeIdent, quoteIdent(name), c.Expr, notValid), pos))
				continue
			}
			if sc.Name != name {
				// RENAMED FROM on a domain constraint (Section 5.4) — same
				// mechanism and SAFE classification as a table constraint
				// rename (Section 7.3).
				ops = append(ops, safeOp(fmt.Sprintf("ALTER DOMAIN %s RENAME CONSTRAINT %s TO %s;", typeIdent, quoteIdent(sc.Name), quoteIdent(name)), pos))
			}
			// VALIDATE CONSTRAINT: the second half of the NOT VALID
			// lifecycle — a constraint added NOT VALID that has since had
			// NOT VALID removed from source is validated in place, matching
			// the table-level feature's identical validateConstraintOp.
			// Uses the post-rename name (name, not sc.Name) as the target,
			// since a rename may have just been emitted above.
			validated := sc
			validated.Name = name
			ops = append(ops, validateConstraintOp("DOMAIN", typeIdent, &validated, c)...)
		}
		if !ptrEq(o.Comment, snap.Comment) {
			if o.Comment != nil {
				ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON DOMAIN %s IS %s;", typeIdent, quoteLit(*o.Comment)), pos))
			} else {
				ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON DOMAIN %s IS NULL;", typeIdent), pos))
			}
		}
		ops = append(ops, diffTypeGrantOps(o, snap, typeIdent, pos)...)
		ops = append(ops, diffSecurityLabelSet(snap.SecurityLabels, o.SecurityLabels, "DOMAIN "+typeIdent, pos)...)
		return ops, nil
	}

	if o.Variant == "BASE" && snap.Variant == "BASE" && snap.BaseStructured {
		// RFC Section 5.5: a BASE type's 7 in-place-alterable properties (real
		// PostgreSQL's ALTER TYPE ... SET (...) supports exactly these) get
		// a targeted, SAFE ALTER; everything else about a BASE type is
		// immutable, so a change to any of it still requires DROP+CREATE —
		// detected via BaseImmutableHash (Body with the 7 properties' text
		// stripped out first, see BaseBodyHashInput), not the plain
		// whole-Body hash, so an alterable-property-only change doesn't
		// also trip this branch. snap.BaseStructured gates this whole path:
		// a pre-existing snapshot saved before this feature existed has
		// BaseImmutableHash=="" always, which would otherwise look
		// identical to "no comparison possible yet" (Reconstructed) and
		// silently skip the immutable-property check entirely for every
		// such snapshot — falling back to the old whole-Body-hash-only
		// branch below instead keeps that case's existing, correct
		// behavior unchanged.
		immutableHash := ""
		if o.Body != "" && !o.Reconstructed {
			sum := sha256.Sum256([]byte(strings.TrimSpace(snapshot.BaseBodyHashInput(o.Body))))
			immutableHash = fmt.Sprintf("%x", sum)
		}
		if immutableHash != "" && snap.BaseImmutableHash != "" && immutableHash != snap.BaseImmutableHash {
			ops = append(ops, destructiveOp(fmt.Sprintf("DROP TYPE IF EXISTS %s;", typeIdent), pos))
			ops = append(ops, createType(o)...)
			return ops, nil
		}
		var setClauses []string
		addBaseSet := func(key string, desired, snapVal *string) {
			// Only diffed when the DESIRED side explicitly declares it —
			// same "don't reset just because it's nil" convention as
			// Owner/Cost/Rows elsewhere, needed because STORAGE always has
			// a concrete catalog value even when never declared. Applied
			// uniformly to all 7 for simplicity: this only ever fires on a
			// genuine declared change, never attempts to model "unsetting"
			// a previously-set property back to none.
			if desired != nil && !ptrEq(desired, snapVal) {
				setClauses = append(setClauses, fmt.Sprintf("%s = %s", key, *desired))
			}
		}
		addBaseSet("RECEIVE", o.BaseReceive, snap.BaseReceive)
		addBaseSet("SEND", o.BaseSend, snap.BaseSend)
		addBaseSet("TYPMOD_IN", o.BaseTypmodIn, snap.BaseTypmodIn)
		addBaseSet("TYPMOD_OUT", o.BaseTypmodOut, snap.BaseTypmodOut)
		addBaseSet("ANALYZE", o.BaseAnalyze, snap.BaseAnalyze)
		addBaseSet("SUBSCRIPT", o.BaseSubscript, snap.BaseSubscript)
		addBaseSet("STORAGE", o.BaseStorage, snap.BaseStorage)
		if len(setClauses) > 0 {
			ops = append(ops, safeOp(fmt.Sprintf("ALTER TYPE %s SET (%s);", typeIdent, strings.Join(setClauses, ", ")), pos))
		}
		if !ptrEq(o.Comment, snap.Comment) {
			if o.Comment != nil {
				ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON TYPE %s IS %s;", typeIdent, quoteLit(*o.Comment)), pos))
			} else {
				ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON TYPE %s IS NULL;", typeIdent), pos))
			}
		}
		ops = append(ops, diffTypeGrantOps(o, snap, typeIdent, pos)...)
		ops = append(ops, diffSecurityLabelSet(snap.SecurityLabels, o.SecurityLabels, "TYPE "+typeIdent, pos)...)
		return ops, nil
	}

	if (o.Variant == "RANGE" || o.Variant == "BASE") && snap.Variant == o.Variant {
		// RFC Section 5.3/Section 5.5: any change to a RANGE type's options, or to a BASE
		// type at all (pre-existing snapshot only — see the BaseStructured
		// branch above for the current behavior), requires DROP + CREATE
		// (RANGE explicitly says CASCADE; BASE's RFC text doesn't, so none
		// is added). Found live-testing a demo project: this whole branch
		// was simply missing — diffType had no case for RANGE or BASE at
		// all, so an already-applied one whose Body changed was a silent
		// no-op forever, only the COMMENT (below) was ever diffed. Same
		// body-hash-with-reconstructed-guard pattern as diffOpaqueIR
		// (Publication/Collation/...): a reconstructed (introspected) body
		// isn't byte-identical to hand-written source, so hashing it would
		// report spurious drift — snap.BodyHash is "" for a reconstructed
		// snapshot entry, and desiredHash is "" here for the same reason,
		// both treated as "no comparison possible yet."
		desiredHash := ""
		if o.Body != "" && !o.Reconstructed {
			hashInput := o.Body
			if o.Variant == "RANGE" {
				hashInput = snapshot.RangeBodyHashInput(o.Body)
			}
			sum := sha256.Sum256([]byte(strings.TrimSpace(hashInput)))
			desiredHash = fmt.Sprintf("%x", sum)
		}
		if desiredHash != "" && snap.BodyHash != "" && desiredHash != snap.BodyHash {
			cascade := ""
			if o.Variant == "RANGE" {
				cascade = " CASCADE"
			}
			ops = append(ops, destructiveOp(fmt.Sprintf("DROP TYPE IF EXISTS %s%s;", typeIdent, cascade), pos))
			ops = append(ops, createType(o)...)
			return ops, nil
		}
		// RFC Section 5.3: a change to MULTIRANGE_TYPE_NAME alone renames the
		// multirange companion type in place.
		if o.Variant == "RANGE" && o.MultirangeName != "" && snap.MultirangeName != "" && o.MultirangeName != snap.MultirangeName {
			ops = append(ops, cautionOp(fmt.Sprintf("ALTER TYPE %s RENAME TO %s;",
				qualIdent(o.Schema, snap.MultirangeName), quoteIdent(o.MultirangeName)), pos))
		}
		if !ptrEq(o.Comment, snap.Comment) {
			if o.Comment != nil {
				ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON TYPE %s IS %s;", typeIdent, quoteLit(*o.Comment)), pos))
			} else {
				ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON TYPE %s IS NULL;", typeIdent), pos))
			}
		}
		ops = append(ops, diffTypeGrantOps(o, snap, typeIdent, pos)...)
		ops = append(ops, diffSecurityLabelSet(snap.SecurityLabels, o.SecurityLabels, "TYPE "+typeIdent, pos)...)
		return ops, nil
	}

	if o.Variant == "ENUM" && snap.Variant == "ENUM" {
		snapVals := make(map[string]bool, len(snap.Values))
		for _, v := range snap.Values {
			snapVals[v] = true
		}
		desiredVals := make(map[string]bool, len(o.EnumValues))
		for _, v := range o.EnumValues {
			desiredVals[v] = true
		}

		// RENAME VALUE (Section 5.1.1, audit item #19): validated against
		// the same three-state resolution every other RENAMED-FROM-style
		// directive in this codebase uses (genuine rename / post-apply
		// no-op / stale-directive error). renamedFrom/renamedTo exclude
		// both sides from the plain add/remove detection below — a rename
		// is neither an addition nor (for MIGRATE REMOVE purposes) a
		// removal.
		renamedFrom := make(map[string]bool, len(o.EnumValueRenames))
		renamedTo := make(map[string]bool, len(o.EnumValueRenames))
		var renameOps []pipeline.DiffOp
		for _, r := range o.EnumValueRenames {
			if snapVals[r.To] {
				// Post-apply / no-op state: the snapshot already has the
				// new value.
				continue
			}
			if !snapVals[r.From] {
				return nil, pipeline.ErrorfCode(r.Pos, "DPG-E021",
					"VALUE %s RENAMED FROM %s on enum %s does not match the snapshot — neither the old nor the new value exists there. Remove the directive if this is a genuinely new value.",
					quoteLit(r.To), quoteLit(r.From), typeIdent)
			}
			renamedFrom[r.From] = true
			renamedTo[r.To] = true
			renameOps = append(renameOps, safeOp(
				fmt.Sprintf("ALTER TYPE %s RENAME VALUE %s TO %s;", typeIdent, quoteLit(r.From), quoteLit(r.To)),
				r.Pos,
			))
		}

		// Values removed from the enum require the MIGRATE REMOVE
		// procedure — a rename's old value doesn't count as "removed"
		// (handled by RENAME VALUE above instead).
		var removedCount int
		for v := range snapVals {
			if !desiredVals[v] && !renamedFrom[v] {
				removedCount++
			}
		}
		if removedCount > 0 {
			// Merge into ops (not a bare return) — ops may already carry the
			// shared OWNER TO/RENAME TO handling from the top of this
			// function, which a plain return here would silently discard.
			// Renames run first: real PostgreSQL's RENAME VALUE only
			// retargets the catalog label (existing rows keep the same
			// underlying enum OID, so col::text already reflects the new
			// name by the time diffEnumRemove's own verification runs),
			// and diffEnumRemove needs to know about them too so it
			// doesn't independently (and wrongly) treat a just-renamed
			// value as one that's actually being removed.
			removeOps, err := diffEnumRemove(o, snap, fullSnap, renamedFrom)
			if err != nil {
				return nil, err
			}
			return append(append(ops, renameOps...), removeOps...), nil
		}

		ops = append(ops, renameOps...)

		// ADD VALUE runs inside the migration's transaction unless another
		// statement in the migration uses the new value — see
		// placeEnumAdditions (RFC Section 5.1.1). Positional control (BEFORE/AFTER, audit item #18) is inferred
		// purely from where a new value sits in the desired list relative
		// to values that already exist live (including one just renamed
		// into place above), not a separate directive: walking left to
		// right, each new value anchors AFTER the nearest earlier
		// already-existing value (including one added earlier in this
		// same loop — each ALTER TYPE ADD VALUE commits its own catalog
		// change, so a later statement in the same migration can reference
		// an earlier one), or BEFORE the nearest later already-existing
		// one when there's no earlier anchor yet (e.g. inserting before
		// the enum's current first value). Falls back to a plain
		// (appended) ADD VALUE only when neither anchor exists.
		alreadyLive := func(v string) bool { return snapVals[v] || renamedTo[v] }
		lastKnown := ""
		for i, v := range o.EnumValues {
			if alreadyLive(v) {
				lastKnown = v
				continue
			}
			var stmt string
			if lastKnown != "" {
				stmt = fmt.Sprintf("ALTER TYPE %s ADD VALUE %s AFTER %s;", typeIdent, quoteLit(v), quoteLit(lastKnown))
			} else {
				anchor := ""
				for _, w := range o.EnumValues[i+1:] {
					if alreadyLive(w) {
						anchor = w
						break
					}
				}
				if anchor != "" {
					stmt = fmt.Sprintf("ALTER TYPE %s ADD VALUE %s BEFORE %s;", typeIdent, quoteLit(v), quoteLit(anchor))
				} else {
					stmt = fmt.Sprintf("ALTER TYPE %s ADD VALUE %s;", typeIdent, quoteLit(v))
				}
			}
			ops = append(ops, &addValueOp{op: safeOp(stmt, pos), typeIdent: typeIdent, value: v})
			lastKnown = v
		}
	}
	if !ptrEq(o.Comment, snap.Comment) {
		if o.Comment != nil {
			ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON TYPE %s IS %s;", typeIdent, quoteLit(*o.Comment)), pos))
		} else {
			ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON TYPE %s IS NULL;", typeIdent), pos))
		}
	}
	ops = append(ops, diffTypeGrantOps(o, snap, typeIdent, pos)...)
	ops = append(ops, diffSecurityLabelSet(snap.SecurityLabels, o.SecurityLabels, "TYPE "+typeIdent, pos)...)
	return ops, nil
}

// diffEnumRemove implements the 7-step MIGRATE REMOVE procedure for enums.
// It creates a shadow type, runs the user-supplied DML, verifies no rows
// carry removed values, alters affected columns, drops the old type, and
// renames the shadow type back to the original name. renamedFrom is the
// caller's already-validated set of RENAME VALUE old-names (audit item
// #19) — excluded from the removed-value computation here too, or a value
// simultaneously renamed and left alone elsewhere would be wrongly treated
// as an unguarded removal (real PostgreSQL's RENAME VALUE only retargets
// the catalog label; by the time this function's own verification query
// runs, an already-renamed row's col::text reflects the new name, not the
// old one it's checking for).
func diffEnumRemove(o *ir.Type, snap *snapshot.SnapType, fullSnap *pipeline.Snapshot, renamedFrom map[string]bool) ([]pipeline.DiffOp, error) {
	pos := o.SrcPos
	typeIdent := qualIdent(o.Schema, o.Name)
	shadowIdent := qualIdent(o.Schema, o.Name+"__dpg_new")

	if o.MigrateRemove == nil {
		return nil, pipeline.ErrorfCode(pos, "DPG-E014",
			"enum %s has removed values but no MIGRATE REMOVE block; add a MIGRATE REMOVE block with DML to migrate existing rows before removing",
			typeIdent)
	}

	desiredSet := make(map[string]bool, len(o.EnumValues))
	for _, v := range o.EnumValues {
		desiredSet[v] = true
	}
	var removed []string
	for _, v := range snap.Values {
		if !desiredSet[v] && !renamedFrom[v] {
			removed = append(removed, v)
		}
	}

	// Find all table columns in the snapshot that reference this enum type.
	type affectedCol struct{ tableSchema, tableName, colName string }
	var affected []affectedCol
	for _, raw := range fullSnap.Objects {
		var so snapshot.SnapObject
		if err := json.Unmarshal(raw, &so); err != nil || so.Table == nil {
			continue
		}
		for _, col := range so.Table.Columns {
			if enumTypeMatch(col.Type, o.Schema, o.Name) {
				affected = append(affected, affectedCol{so.Table.Schema, so.Table.Name, col.Name})
			}
		}
	}

	var ops []pipeline.DiffOp

	// Step 1: create shadow type with the new (reduced) value set.
	vals := make([]string, len(o.EnumValues))
	for i, v := range o.EnumValues {
		vals[i] = quoteLit(v)
	}
	ops = append(ops, safeOp(
		fmt.Sprintf("CREATE TYPE %s AS ENUM (%s);", shadowIdent, strings.Join(vals, ", ")),
		pos,
	))

	// Step 2: execute migration DML (user-supplied).
	if dml := strings.TrimSpace(o.MigrateRemove.SQL.Text); dml != "" {
		ops = append(ops, destructiveOp(dml, pos))
	}

	// Step 3: verify no rows still carry removed values.
	removedLits := make([]string, len(removed))
	for i, v := range removed {
		removedLits[i] = quoteLit(v)
	}
	for _, ac := range affected {
		tblIdent := qualIdent(ac.tableSchema, ac.tableName)
		colIdent := quoteIdent(ac.colName)
		ops = append(ops, safeOp(fmt.Sprintf(
			"DO $$ DECLARE _cnt bigint; BEGIN\n"+
				"  SELECT count(*) INTO _cnt FROM %s WHERE %s::text = ANY(ARRAY[%s]);\n"+
				"  IF _cnt > 0 THEN RAISE EXCEPTION 'DPG-E013: MIGRATE REMOVE: %% row(s) in %s.%s still carry a removed %s value', _cnt; END IF;\n"+
				"END; $$;",
			tblIdent, colIdent, strings.Join(removedLits, ", "),
			tblIdent, colIdent, typeIdent,
		), pos))
	}

	// Step 4: alter affected columns to use the shadow type.
	for _, ac := range affected {
		tblIdent := qualIdent(ac.tableSchema, ac.tableName)
		colIdent := quoteIdent(ac.colName)
		ops = append(ops, destructiveOp(fmt.Sprintf(
			"ALTER TABLE %s ALTER COLUMN %s TYPE %s USING %s::text::%s;",
			tblIdent, colIdent, shadowIdent, colIdent, shadowIdent,
		), pos))
	}

	// Step 5: drop the old type.
	ops = append(ops, destructiveOp(fmt.Sprintf("DROP TYPE %s;", typeIdent), pos))

	// Step 6: rename shadow to the original name.
	ops = append(ops, safeOp(fmt.Sprintf("ALTER TYPE %s RENAME TO %s;", shadowIdent, quoteIdent(o.Name)), pos))

	// Re-apply comment after the rename.
	if o.Comment != nil {
		ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON TYPE %s IS %s;", typeIdent, quoteLit(*o.Comment)), pos))
	}

	// Re-apply grants (RFC audit item #3): the old type (and any grants on
	// it) is fully dropped in step 5, so a MIGRATE REMOVE cycle would
	// otherwise silently lose them, the same reasoning as the comment
	// re-apply just above.
	ops = append(ops, createTypeGrantOps(o)...)

	// Step 7: cleanup instruction for manual rollback.
	ops = append(ops, instructionOp(pipeline.Safe, fmt.Sprintf("-- On failure run: DROP TYPE IF EXISTS %s;", shadowIdent), pos))

	return ops, nil
}

// enumTypeMatch reports whether colType refers to the enum identified by
// (schema, name). It handles both qualified and unqualified forms, and
// single-dimension arrays.
func enumTypeMatch(colType, schema, name string) bool {
	qn := schema + "." + name
	return colType == qn || colType == name ||
		colType == qn+"[]" || colType == name+"[]"
}

// replicaIdentityMode normalizes an empty/omitted REPLICA IDENTITY mode
// (both a fresh IR object's zero value and a pre-upgrade snapshot's zero
// value) to PostgreSQL's own "DEFAULT" — see ir.Table.ReplicaIdentity's doc
// comment for why an omitted directive is a real declared value, not
// "unmanaged".
func replicaIdentityMode(mode string) string {
	if mode == "" {
		return "DEFAULT"
	}
	return mode
}

// replicaIdentityClause renders the argument half of
// ALTER TABLE ... REPLICA IDENTITY ...
func replicaIdentityClause(ri ir.ReplicaIdentity) string {
	mode := replicaIdentityMode(ri.Mode)
	if mode == "INDEX" {
		return "USING INDEX " + quoteIdent(ri.IndexName)
	}
	return mode
}

func diffTable(o *ir.Table, snap *snapshot.SnapTable, fullSnap *pipeline.Snapshot, env *diffEnv, skipDetachedChildren map[string]bool) ([]pipeline.DiffOp, error) {
	var ops []pipeline.DiffOp
	pos := o.SrcPos
	tbl := qualIdent(o.Schema, o.Name)

	// SET SCHEMA / RENAME TO, in that order: RFC Section 7.6's cross-schema
	// RENAMED FROM extension emits SET SCHEMA first (using the table's
	// actual current identity, old_schema.old_name) so a still-in-flight
	// rename can't collide with an existing name already in the target
	// schema, then RENAME TO (new_schema.old_name -> new_name) — real
	// PostgreSQL has no single statement that does both at once. RENAME TO
	// alone is CAUTION per RFC's own diffing table (top-level object,
	// independently referenceable); SET SCHEMA is SAFE.
	if snap.Schema != o.Schema {
		ops = append(ops, safeOp(
			fmt.Sprintf("ALTER TABLE %s SET SCHEMA %s;", qualIdent(snap.Schema, snap.Name), quoteIdent(o.Schema)),
			pos,
		))
	}
	if snap.Name != o.Name {
		ops = append(ops, cautionOp(
			fmt.Sprintf("ALTER TABLE %s RENAME TO %s;", qualIdent(o.Schema, snap.Name), quoteIdent(o.Name)),
			pos,
		))
	}

	// A foreign table's SERVER cannot be changed via ALTER FOREIGN TABLE —
	// real PostgreSQL has no such clause — so it requires DROP + CREATE,
	// same as diffView's handling of a materialized view's query change.
	if o.Foreign && !ptrEq(o.ForeignServer, snap.ForeignServer) {
		ops = append(ops, destructiveOp(fmt.Sprintf("DROP FOREIGN TABLE IF EXISTS %s;", tbl), pos))
		ops = append(ops, createTable(o)...)
		return ops, nil
	}
	if o.Foreign {
		ops = append(ops, diffForeignOptions(tbl, o.ForeignOptions, snap.ForeignOptions, pos)...)
	}

	if !ptrEq(o.Owner, snap.Owner) && o.Owner != nil {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER TABLE %s OWNER TO %s;", tbl, quoteIdent(*o.Owner)), pos))
	}
	if !ptrEq(o.Tablespace, snap.Tablespace) && o.Tablespace != nil {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER TABLE %s SET TABLESPACE %s;", tbl, quoteIdent(*o.Tablespace)), pos))
	}
	// LOGGED/UNLOGGED toggled (Section 7.12) — a targeted CAUTION ALTER
	// (rewrites the table's storage), not a recreate. Meaningless for a
	// foreign table (no local storage), matching createTable's own
	// mutually-exclusive Unlogged/Foreign switch.
	if !o.Foreign && o.Unlogged != snap.Unlogged {
		if o.Unlogged {
			ops = append(ops, cautionOp(fmt.Sprintf("ALTER TABLE %s SET UNLOGGED;", tbl), pos))
		} else {
			ops = append(ops, cautionOp(fmt.Sprintf("ALTER TABLE %s SET LOGGED;", tbl), pos))
		}
	}
	// PROTECTED has no PG DDL equivalent — it's pure DPG-side bookkeeping
	// (see the Pass-2 deletion check's DPG-E022 guard) — but it must still
	// appear as an op, or a PROTECTED-only removal produces zero DiffOps and
	// apply's len(ops)==0 short-circuit means the snapshot's stale
	// Protected=true is never cleared, permanently blocking the very
	// "remove PROTECTED first" workflow RFC Section 7.11 documents.
	if o.Protected != snap.Protected {
		ops = append(ops, safeOp(fmt.Sprintf("-- PROTECTED %s on %s", map[bool]string{true: "enabled", false: "removed"}[o.Protected], tbl), pos))
	}
	if desiredTxt, snapTxt := effectiveComment(o.Comment, o.Deprecated), effectiveComment(snap.Comment, snap.Deprecated); !ptrEq(desiredTxt, snapTxt) {
		if desiredTxt != nil {
			ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON TABLE %s IS %s;", tbl, quoteLit(*desiredTxt)), pos))
		} else {
			ops = append(ops, safeOp(fmt.Sprintf("COMMENT ON TABLE %s IS NULL;", tbl), pos))
		}
	}

	// RLS changes.
	if o.RLSEnabled && !snap.RLSEnabled {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER TABLE %s ENABLE ROW LEVEL SECURITY;", tbl), pos))
	} else if !o.RLSEnabled && snap.RLSEnabled {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER TABLE %s DISABLE ROW LEVEL SECURITY;", tbl), pos))
	}
	if o.RLSForced && !snap.RLSForced {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER TABLE %s FORCE ROW LEVEL SECURITY;", tbl), pos))
	} else if !o.RLSForced && snap.RLSForced {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER TABLE %s NO FORCE ROW LEVEL SECURITY;", tbl), pos))
	}

	// REPLICA IDENTITY (Section 7.11) — always compared in full, like RLS
	// above: omitting the directive means PostgreSQL's own DEFAULT, not
	// "leave whatever's live alone". Metadata-only, no table rewrite.
	desiredReplIdentMode := replicaIdentityMode(o.ReplicaIdentity.Mode)
	snapReplIdentMode := replicaIdentityMode(snap.ReplicaIdentityMode)
	if desiredReplIdentMode != snapReplIdentMode ||
		(desiredReplIdentMode == "INDEX" && o.ReplicaIdentity.IndexName != snap.ReplicaIdentityIndex) {
		ops = append(ops, safeOp(fmt.Sprintf("ALTER TABLE %s REPLICA IDENTITY %s;", tbl, replicaIdentityClause(o.ReplicaIdentity)), pos))
	}

	// CLUSTER ON (Section 7.11).
	if !ptrEq(o.ClusterOn, snap.ClusterOn) {
		if o.ClusterOn != nil {
			ops = append(ops, safeOp(fmt.Sprintf("ALTER TABLE %s CLUSTER ON %s;", tbl, quoteIdent(*o.ClusterOn)), pos))
		} else {
			ops = append(ops, safeOp(fmt.Sprintf("ALTER TABLE %s SET WITHOUT CLUSTER;", tbl), pos))
		}
	}

	// OF type_name / NOT OF (Section 7.11) — CAUTION: PostgreSQL validates
	// every column against the type's attributes at execution time, unlike
	// a plain metadata change.
	if o.OfType.String() != snap.OfType {
		if o.OfType.Name != "" {
			ops = append(ops, cautionOp(fmt.Sprintf("ALTER TABLE %s OF %s;", tbl, o.OfType.String()), pos))
		} else {
			ops = append(ops, cautionOp(fmt.Sprintf("ALTER TABLE %s NOT OF;", tbl), pos))
		}
	}

	// SET ACCESS METHOD (Section 7.11) — CAUTION: rewrites the table's
	// storage using the new method. Only diffed when DESIRED declares a
	// method (same "omitting a directive means leave it alone" convention
	// as Owner/ClusterOn's != nil guards elsewhere), except clearing an
	// already-declared one back to the cluster default, which real
	// PostgreSQL expresses as the literal keyword DEFAULT.
	if o.AccessMethod != snap.AccessMethod {
		if o.AccessMethod != "" {
			ops = append(ops, cautionOp(fmt.Sprintf("ALTER TABLE %s SET ACCESS METHOD %s;", tbl, o.AccessMethod), pos))
		} else {
			ops = append(ops, cautionOp(fmt.Sprintf("ALTER TABLE %s SET ACCESS METHOD DEFAULT;", tbl), pos))
		}
	}

	// WITH (...) storage params (Section 7.11) — SET/RESET, no rewrite: real
	// PostgreSQL applies both in place. Meaningless for a foreign table (its
	// OPTIONS clause, diffed separately above via diffForeignOptions, is a
	// distinct grammar with its own ADD/SET/DROP verbs).
	if !o.Foreign {
		ops = append(ops, diffTableStorageParams(tbl, o.StorageParams, snap.StorageParams, pos)...)
	}

	colOps, renamedCols, droppedCols, err := diffColumns(tbl, o, snap, env)
	if err != nil {
		return nil, err
	}
	ops = append(ops, colOps...)
	if err := checkNoStaleColumnRefs(o, renamedCols); err != nil {
		return nil, err
	}
	constraintOps, droppedViaOnly, err := diffConstraints(tbl, o, snap, fullSnap, pos, renamedCols, droppedCols)
	if err != nil {
		return nil, err
	}
	ops = append(ops, constraintOps...)
	indexOps, err := diffIndexes(o.Schema, o.Name, o, snap, renamedCols, droppedCols)
	if err != nil {
		return nil, err
	}
	ops = append(ops, indexOps...)
	policyOps, err := diffPolicies(o.Schema, o.Name, o, snap)
	if err != nil {
		return nil, err
	}
	ops = append(ops, policyOps...)
	triggerOps, err := diffTriggers(o.Schema, o.Name, o, snap)
	if err != nil {
		return nil, err
	}
	ops = append(ops, triggerOps...)
	ops = append(ops, diffTableInherits(tbl, o, snap, pos)...)
	ops = append(ops, diffGrantSet(snap.Grants, o.Grants, "TABLE "+tbl, pos)...)
	ops = append(ops, diffRevocationSet(snap.Revocations, o.Revocations, "TABLE "+tbl, pos)...)
	tableObjType := "TABLE"
	if o.Foreign {
		tableObjType = "FOREIGN TABLE"
	}
	ops = append(ops, diffSecurityLabelSet(snap.SecurityLabels, o.SecurityLabels, tableObjType+" "+tbl, pos)...)
	partitionOps, err := diffPartitions(tbl, o, snap, pos, skipDetachedChildren, droppedViaOnly)
	if err != nil {
		return nil, err
	}
	ops = append(ops, partitionOps...)
	return ops, nil
}

// diffForeignOptions computes ALTER FOREIGN TABLE ... OPTIONS (ADD/SET/DROP
// ...) for a changed FOREIGN TABLE OPTIONS clause. Unlike a SERVER change,
// real PostgreSQL supports altering OPTIONS in place — no drop+recreate
// needed. snapFlat is the snapshot's flattened "key=value, key=value" form
// (see flattenParams); desired is the live IR's ordered param list.
func diffForeignOptions(tbl string, desired []pipeline.StorageParam, snapFlat string, pos pipeline.SourcePos) []pipeline.DiffOp {
	snapMap := make(map[string]string)
	for _, kv := range strings.Split(snapFlat, ", ") {
		if kv == "" {
			continue
		}
		if k, v, ok := strings.Cut(kv, "="); ok {
			snapMap[k] = v
		}
	}

	desiredKeys := make(map[string]bool, len(desired))
	var actions []string
	for _, p := range desired {
		desiredKeys[p.Key] = true
		old, existed := snapMap[p.Key]
		switch {
		case !existed:
			actions = append(actions, fmt.Sprintf("ADD %s %s", p.Key, quoteLit(p.Value)))
		case old != p.Value:
			actions = append(actions, fmt.Sprintf("SET %s %s", p.Key, quoteLit(p.Value)))
		}
	}
	// Iterate snapFlat's own order (not map order) for deterministic DROP output.
	for _, kv := range strings.Split(snapFlat, ", ") {
		if kv == "" {
			continue
		}
		k, _, _ := strings.Cut(kv, "=")
		if !desiredKeys[k] {
			actions = append(actions, fmt.Sprintf("DROP %s", k))
		}
	}
	if len(actions) == 0 {
		return nil
	}
	return []pipeline.DiffOp{safeOp(
		fmt.Sprintf("ALTER FOREIGN TABLE %s OPTIONS (%s);", tbl, strings.Join(actions, ", ")),
		pos,
	)}
}

// diffTableStorageParams computes ALTER TABLE ... SET (...)/RESET (...) for
// a changed WITH (...) storage-params clause (Section 7.11).
func diffTableStorageParams(tbl string, desired []pipeline.StorageParam, snapFlat string, pos pipeline.SourcePos) []pipeline.DiffOp {
	return diffStorageParamsSetReset("ALTER TABLE", tbl, desired, snapFlat, pos)
}

// diffTablespaceStorageParams is diffTableStorageParams' Tablespace
// counterpart (Section 14.7) — real PostgreSQL's ALTER TABLESPACE ... SET
// (...)/RESET (...) is the identical two-clause grammar as ALTER TABLE's
// (confirmed via \h ALTER TABLESPACE), just a different verb.
func diffTablespaceStorageParams(ident string, desired []pipeline.StorageParam, snapFlat string, pos pipeline.SourcePos) []pipeline.DiffOp {
	return diffStorageParamsSetReset("ALTER TABLESPACE", ident, desired, snapFlat, pos)
}

// diffStorageParamsSetReset is the shared implementation behind
// diffTableStorageParams/diffTablespaceStorageParams: alterStmt is the
// prefix ("ALTER TABLE"/"ALTER TABLESPACE"), ident the already-quoted
// target identifier. Unlike diffForeignOptions' single ADD/SET/DROP OPTIONS
// statement, real PostgreSQL splits a WITH (...) storage-params change into
// two independently-valid clauses — a changed/added key goes through SET, a
// removed one through RESET, and either may be omitted when empty
// (confirmed live: "ALTER TABLE t RESET ()" is a syntax error). snapFlat is
// the snapshot's flattened "key=value, key=value" form (see flattenParams);
// desired is the live IR's ordered param list. CAUTION, not SAFE: reloptions
// like fillfactor/autovacuum_*/tablespace cost-planner params alter
// storage/maintenance/planning behavior, not pure metadata.
func diffStorageParamsSetReset(alterStmt, ident string, desired []pipeline.StorageParam, snapFlat string, pos pipeline.SourcePos) []pipeline.DiffOp {
	snapMap := make(map[string]string)
	for _, kv := range strings.Split(snapFlat, ", ") {
		if kv == "" {
			continue
		}
		if k, v, ok := strings.Cut(kv, "="); ok {
			snapMap[k] = v
		}
	}

	desiredKeys := make(map[string]bool, len(desired))
	var sets []string
	for _, p := range desired {
		desiredKeys[p.Key] = true
		old, existed := snapMap[p.Key]
		if !existed || old != p.Value {
			sets = append(sets, fmt.Sprintf("%s=%s", p.Key, p.Value))
		}
	}
	var resets []string
	for _, kv := range strings.Split(snapFlat, ", ") {
		if kv == "" {
			continue
		}
		k, _, _ := strings.Cut(kv, "=")
		if !desiredKeys[k] {
			resets = append(resets, k)
		}
	}

	var ops []pipeline.DiffOp
	if len(sets) > 0 {
		ops = append(ops, cautionOp(fmt.Sprintf("%s %s SET (%s);", alterStmt, ident, strings.Join(sets, ", ")), pos))
	}
	if len(resets) > 0 {
		ops = append(ops, cautionOp(fmt.Sprintf("%s %s RESET (%s);", alterStmt, ident, strings.Join(resets, ", ")), pos))
	}
	return ops
}

// createPartitionOps emits "CREATE TABLE child PARTITION OF parent
// FOR VALUES ...;" for p (or "CREATE FOREIGN TABLE ... SERVER ... [OPTIONS
// (...)]" when p.Foreign — Section 7.13's FOREIGN partition form),
// recursing into any sub-partitions (a FOREIGN partition never has any:
// real PostgreSQL rejects PARTITION BY on a foreign table). A
// sub-partitioned child gets its own trailing "PARTITION BY strategy (cols)"
// clause — real PostgreSQL allows this directly on a PARTITION OF statement —
// then each of its sub-partitions is created as PARTITION OF the child.
//
// p.Bounds already carries the full clause ("FOR VALUES FROM (...) TO
// (...)"/"FOR VALUES IN (...)"/"FOR VALUES WITH (...)"/"DEFAULT") — both the
// parser (raw text after the partition name) and introspection (pg_get_expr
// on relpartbound) capture it that way, so prepending "FOR VALUES" here
// would double it up (and be outright wrong for a DEFAULT partition, which
// has no FOR VALUES at all).
func createPartitionOps(schema string, parent string, p *ir.Partition) []pipeline.DiffOp {
	childTbl := qualIdent(schema, p.Name)
	verb := "CREATE TABLE"
	if p.Foreign {
		verb = "CREATE FOREIGN TABLE"
	}
	stmt := fmt.Sprintf("%s %s PARTITION OF %s %s", verb, childTbl, parent, p.Bounds)
	if p.PartitionBy != nil {
		stmt += fmt.Sprintf(" PARTITION BY %s (%s)", p.PartitionBy.Strategy, strings.Join(p.PartitionBy.Columns, ", "))
	}
	if p.Foreign {
		// Real PostgreSQL grammar: CREATE FOREIGN TABLE ... PARTITION OF
		// parent bounds SERVER server_name [OPTIONS (...)] — SERVER is
		// mandatory (enforced at parse time), so ForeignServer is always
		// non-nil here.
		if p.ForeignServer != nil {
			stmt += " SERVER " + quoteIdent(*p.ForeignServer)
		}
		if len(p.ForeignOptions) > 0 {
			var opts []string
			for _, o := range p.ForeignOptions {
				opts = append(opts, o.Key+" "+quoteLit(o.Value))
			}
			stmt += " OPTIONS (" + strings.Join(opts, ", ") + ")"
		}
	}
	stmt += ";"

	ops := []pipeline.DiffOp{safeOp(stmt, p.SrcPos)}
	for _, cst := range p.Constraints {
		// A brand-new partition declaring its own local constraint from the
		// start (RFC Section 7.3's "DROP CONSTRAINT ... ONLY" gap) — an
		// ordinary ADD CONSTRAINT, no PostgreSQL version dependency at all
		// (unlike the ONLY-drop transition itself, see diffConstraints):
		// adding a constraint directly to an already-existing partition,
		// independent of its parent, has always been valid PostgreSQL.
		notValid := ""
		if cst.NotValid {
			notValid = " NOT VALID"
		}
		notEnforced := ""
		if cst.NotEnforced {
			notEnforced = " NOT ENFORCED"
		}
		ops = append(ops, cautionOp(
			fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s %s%s%s;", childTbl, quoteIdent(cst.Name), cst.Expr, notValid, notEnforced),
			p.SrcPos,
		))
	}
	for _, sub := range p.Partitions {
		ops = append(ops, createPartitionOps(schema, childTbl, sub)...)
	}
	return ops
}

// diffPartitionConstraints diffs one partition's own locally-declared
// constraints (ir.Partition.Constraints) against its snapshot counterpart —
// RFC Section 7.3's "DROP CONSTRAINT ... ONLY" gap, PostgreSQL 18+. Always
// named per the { }-block grammar both sides share with a table's own
// block-declared constraints (see buildBlockConstraint), so — unlike
// diffConstraints above — this needs no unnamed-constraint structural or
// PostgreSQL-auto-name matching, just a name-keyed comparison; a change to
// an already-matched constraint's body is not detected here, the same
// identity-only limitation diffConstraints' own doc comment already
// documents for the table-level case. suppressAdd names constraints
// diffConstraints already made locally present on this partition via
// "ALTER TABLE ONLY parent DROP CONSTRAINT" this same run — physically
// there already, so no separate ADD CONSTRAINT is emitted (or valid) for
// them.
func diffPartitionConstraints(partTbl string, desired []*ir.Constraint, snap []snapshot.SnapConstraint, pos pipeline.SourcePos, suppressAdd map[string]bool) []pipeline.DiffOp {
	var ops []pipeline.DiffOp

	snapByName := make(map[string]snapshot.SnapConstraint, len(snap))
	for _, sc := range snap {
		snapByName[sc.Name] = sc
	}
	desiredNames := make(map[string]bool, len(desired))
	for _, c := range desired {
		desiredNames[c.Name] = true
		if _, exists := snapByName[c.Name]; exists {
			continue
		}
		if suppressAdd[c.Name] {
			continue
		}
		notValid := ""
		if c.NotValid {
			notValid = " NOT VALID"
		}
		notEnforced := ""
		if c.NotEnforced {
			notEnforced = " NOT ENFORCED"
		}
		ops = append(ops, cautionOp(
			fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s %s%s%s;", partTbl, quoteIdent(c.Name), c.Expr, notValid, notEnforced),
			pos,
		))
	}
	for _, sc := range snap {
		if !desiredNames[sc.Name] {
			ops = append(ops, destructiveOp(
				fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;", partTbl, quoteIdent(sc.Name)),
				pos,
			))
		}
	}
	return ops
}

// dropPartitionVerb picks "DROP TABLE" or "DROP FOREIGN TABLE" for removing
// an existing partition, based on its CURRENT (snapshot) relkind — real
// PostgreSQL rejects DROP TABLE on a foreign table ("... is not a table",
// confirmed live) and vice versa.
func dropPartitionVerb(foreign bool) string {
	if foreign {
		return "DROP FOREIGN TABLE"
	}
	return "DROP TABLE"
}

func diffPartitions(tbl string, o *ir.Table, snap *snapshot.SnapTable, pos pipeline.SourcePos, skipDetachedChildren map[string]bool, droppedViaOnly map[string]bool) ([]pipeline.DiffOp, error) {
	var ops []pipeline.DiffOp

	// Partition strategy change cannot be done in-place.
	desiredPB := ""
	if o.PartitionBy != nil {
		desiredPB = o.PartitionBy.Strategy + " (" + strings.Join(o.PartitionBy.Columns, ", ") + ")"
	}
	if snap.PartitionBy != desiredPB {
		ops = append(ops, instructionOp(pipeline.Destructive,
			fmt.Sprintf("-- PARTITION BY changed on %s; table must be recreated to alter the partition strategy", tbl),
			pos,
		))
		return ops, nil
	}

	partOps, err := diffPartitionList(o.Schema, tbl, o.Partitions, snap.Partitions, pos, skipDetachedChildren, droppedViaOnly)
	if err != nil {
		return nil, err
	}
	ops = append(ops, partOps...)
	return ops, nil
}

// diffPartitionList diffs one level of partition entries (desired vs.
// snapshot), recursing into each matched pair's sub-partitions
// (RFC Section 7.13). parent is the qualified name of the owning table (or,
// one level down, the qualified name of the parent partition).
//
// RENAMED FROM matching mirrors diffCompositeAttrs' identical shape (name-
// keyed collection, matched by old name within the same parent, same stale-
// directive validation) — a partition has no independent schema, so matching
// is always within THIS parent's own list, never cross-schema.
// skipDetached, only meaningful at the top level (nil for every recursive
// sub-partition call — see Diff's Pass 2.5 doc comment: DETACHED FROM only
// matches a direct child of the named parent), names child partitions
// intentionally being detached this run — excluded from the "absent from
// desired" removal detection below, which would otherwise treat them as
// simply dropped.
// droppedViaOnly, also only meaningful at the top level (nil for every
// recursive sub-partition call, matching skipDetached's own restriction —
// see diffConstraints' doc comment on why this coordination isn't threaded
// deeper than one level), names constraints diffConstraints already dropped
// from parent via "ALTER TABLE ONLY parent DROP CONSTRAINT" this run —
// already physically local on every direct partition as a result, so the
// per-partition constraint diff below must not also emit an ADD for them.
func diffPartitionList(schema, parent string, desired []*ir.Partition, snap []snapshot.SnapPartition, pos pipeline.SourcePos, skipDetached map[string]bool, droppedViaOnly map[string]bool) ([]pipeline.DiffOp, error) {
	var ops []pipeline.DiffOp

	snapByName := make(map[string]snapshot.SnapPartition, len(snap))
	for _, sp := range snap {
		snapByName[sp.Name] = sp
	}
	desiredHasName := make(map[string]bool, len(desired))
	for _, p := range desired {
		desiredHasName[p.Name] = true
	}

	// Partitions renamed in desired: map old name -> new name, validated the
	// same way as diffCompositeAttrs/diffColumns' RENAMED FROM handling.
	renamedFrom := make(map[string]string) // snap name -> desired name
	for _, p := range desired {
		if p.RenamedFrom == nil {
			continue
		}
		if desiredHasName[*p.RenamedFrom] {
			return nil, pipeline.Errorf(p.SrcPos,
				"RENAMED FROM %q on partition %q collides with another partition of the same name in the desired declaration. Remove the stale partition.",
				*p.RenamedFrom, p.Name)
		}
		_, oldInSnap := snapByName[*p.RenamedFrom]
		_, newInSnap := snapByName[p.Name]
		if newInSnap {
			// Post-apply / no-op state: the snapshot already has the new name.
			continue
		}
		if !oldInSnap {
			return nil, pipeline.ErrorfCode(p.SrcPos, "DPG-E021",
				"RENAMED FROM %q on partition %q does not match the snapshot — neither the old nor the new name exists there. Remove RENAMED FROM if this is a genuinely new partition.",
				*p.RenamedFrom, p.Name)
		}
		renamedFrom[*p.RenamedFrom] = p.Name
	}

	for _, p := range desired {
		sp, exists := snapByName[p.Name]
		if !exists && p.RenamedFrom != nil {
			if oldSp, ok := snapByName[*p.RenamedFrom]; ok {
				sp, exists = oldSp, true
			}
		}
		partTbl := qualIdent(schema, p.Name)
		desiredPB := ""
		if p.PartitionBy != nil {
			desiredPB = p.PartitionBy.Strategy + " (" + strings.Join(p.PartitionBy.Columns, ", ") + ")"
		}
		switch {
		case !exists && p.AttachedFrom != nil:
			// RFC Section 7.13's "ATTACHED FROM existing_table" form:
			// attaches an already-existing standalone table instead of
			// creating a new one — real PostgreSQL validates the existing
			// table's structure and any CHECK constraints against the
			// target bound at attach time; DPG performs no additional
			// validation of its own (passthrough principle, as elsewhere
			// in this document). ATTACH PARTITION has no CONCURRENTLY
			// variant in real PostgreSQL, unlike DETACH.
			ops = append(ops, cautionOp(
				fmt.Sprintf("ALTER TABLE %s ATTACH PARTITION %s %s;", parent, quoteQualIdent(normalizeInheritRef(schema, *p.AttachedFrom)), p.Bounds),
				p.SrcPos,
			))
		case !exists:
			ops = append(ops, createPartitionOps(schema, parent, p)...)
		case sp.Bound != p.Bounds, sp.Foreign != p.Foreign:
			// PG cannot alter partition bounds in place, and a partition's
			// relkind (ordinary table vs. foreign table) can never be
			// changed via ALTER either — both require DROP + CREATE. Drops
			// the partition under its CURRENT (matched-snapshot) identity —
			// sp.Name, not p.Name — since a RENAMED FROM match means those
			// two can legitimately differ at this point in the diff. Uses
			// sp.Foreign (the CURRENT relkind) to pick DROP TABLE vs. DROP
			// FOREIGN TABLE, since dropping under the wrong statement is a
			// real PostgreSQL error ("... is not a table"/"... is not a
			// foreign table"), confirmed live.
			ops = append(ops, destructiveOp(fmt.Sprintf("%s %s;", dropPartitionVerb(sp.Foreign), qualIdent(sp.Schema, sp.Name)), p.SrcPos))
			ops = append(ops, createPartitionOps(schema, parent, p)...)
		case sp.PartitionBy != desiredPB:
			// A sub-partition's own PARTITION BY strategy cannot be altered
			// in place either.
			ops = append(ops, instructionOp(pipeline.Destructive,
				fmt.Sprintf("-- PARTITION BY changed on %s; table must be recreated to alter the partition strategy", partTbl),
				p.SrcPos,
			))
		default:
			if sp.Name != p.Name {
				// Same mechanism and safety class as a plain table rename
				// (Section 7.6) — a partition is an ordinary table under the
				// hood, and renaming it has no effect on its attachment.
				ops = append(ops, cautionOp(
					fmt.Sprintf("ALTER TABLE %s RENAME TO %s;", qualIdent(sp.Schema, sp.Name), quoteIdent(p.Name)),
					p.SrcPos,
				))
			}
			ops = append(ops, diffPartitionConstraints(partTbl, p.Constraints, sp.Constraints, p.SrcPos, droppedViaOnly)...)
			subOps, err := diffPartitionList(schema, partTbl, p.Partitions, sp.Partitions, p.SrcPos, nil, nil)
			if err != nil {
				return nil, err
			}
			ops = append(ops, subOps...)
		}
	}
	for _, sp := range snap {
		if _, wasRenamed := renamedFrom[sp.Name]; wasRenamed {
			continue
		}
		if skipDetached[sp.Name] {
			continue
		}
		if !desiredHasName[sp.Name] {
			ops = append(ops, destructiveOp(
				fmt.Sprintf("%s %s;", dropPartitionVerb(sp.Foreign), qualIdent(sp.Schema, sp.Name)),
				pos,
			))
		}
	}
	return ops, nil
}

// normalizeInheritRef canonicalises a possibly-bare parent-table reference
// (as written by the user, e.g. "base_logs") to the same fully-qualified
// "schema.name" form introspection always produces (e.g. "public.base_logs")
// — otherwise an unqualified same-schema reference in desired never
// string-matches its qualified snapshot counterpart, and diffTableInherits
// churns out a spurious NO INHERIT + INHERIT pair on every plan.
func normalizeInheritRef(defaultSchema, ref string) string {
	if strings.Contains(ref, ".") {
		return ref
	}
	return defaultSchema + "." + ref
}

func diffTableInherits(tbl string, o *ir.Table, snap *snapshot.SnapTable, pos pipeline.SourcePos) []pipeline.DiffOp {
	var ops []pipeline.DiffOp

	snapSet := make(map[string]bool, len(snap.Inherits))
	for _, p := range snap.Inherits {
		snapSet[normalizeInheritRef(o.Schema, p)] = true
	}
	desiredSet := make(map[string]bool, len(o.Inherits))
	for _, p := range o.Inherits {
		desiredSet[normalizeInheritRef(o.Schema, p)] = true
	}

	for _, p := range o.Inherits {
		if !snapSet[normalizeInheritRef(o.Schema, p)] {
			ops = append(ops, safeOp(fmt.Sprintf("ALTER TABLE %s INHERIT %s;", tbl, quoteQualIdent(normalizeInheritRef(o.Schema, p))), pos))
		}
	}
	for _, p := range snap.Inherits {
		if !desiredSet[normalizeInheritRef(o.Schema, p)] {
			// SAFE, not CAUTION: confirmed live (PG17) that NO INHERIT only
			// drops the catalog-level inheritance link — it takes the same
			// AccessExclusiveLock as any other structural ALTER TABLE, does
			// not rewrite or scan the child's rows, and any inherited CHECK
			// constraint is retained on the child as a local constraint
			// rather than being dropped.
			ops = append(ops, safeOp(fmt.Sprintf("ALTER TABLE %s NO INHERIT %s;", tbl, quoteQualIdent(normalizeInheritRef(o.Schema, p))), pos))
		}
	}
	return ops
}

// diffColumns returns the column DDL ops along with a snap→desired rename map
// and the set of snapshot columns being dropped. Constraint and index diffing
// use these so a column rename doesn't fabricate spurious drop/recreate pairs,
// and so PG-cascaded objects on dropped columns aren't double-emitted.
//
// RENAMED FROM is validated using the same logic as object-level renames in
// Diff(): the new column's presence in the snapshot is the discriminator
// between "stale typo" and "rename already applied". The snapshot is rewritten
// after every apply (see snapshot.Populate) so the new column appears there
// from the next plan onward — erroring on a missing OLD name without checking
// for the NEW name would make every directive a one-shot. The collision check
// (RENAMED FROM names a column ALSO present in the desired DDL) stays
// snapshot-independent because it's incoherent intent regardless of state.
// checkNoStaleColumnRefs validates that no constraint or index in o still
// references a column's pre-rename name (RFC Section 7.6, DPG-E019): once a
// fresh column rename is recognized, "all constraint and index
// declarations MUST use [the new name]." renamedCols maps old name -> new
// name for renames happening in this diff pass only (built by diffColumns,
// which excludes already-applied renames — nothing to check there since
// the old name is gone from live use).
//
// Checks structural column-list fields only: Constraint.Columns,
// Constraint.CheckColumn (populated only for a single-column CHECK
// expression), Index.Columns' plain-name entries, and Index.Include. A
// stale reference buried inside a multi-column CHECK expression or an
// expression index would need a full AST walk to detect and isn't covered
// — the same documented limitation Constraint.CheckColumn's own
// single-column restriction already carries elsewhere in this codebase.
func checkNoStaleColumnRefs(o *ir.Table, renamedCols map[string]string) error {
	if len(renamedCols) == 0 {
		return nil
	}
	for _, c := range o.Constraints {
		for _, col := range c.Columns {
			if newName, stale := renamedCols[col]; stale {
				return pipeline.ErrorfCode(c.Pos, "DPG-E019",
					"constraint %q references column %q, which was just renamed to %q — update the constraint to use the new name",
					c.Name, col, newName)
			}
		}
		if c.CheckColumn != nil {
			if newName, stale := renamedCols[*c.CheckColumn]; stale {
				return pipeline.ErrorfCode(c.Pos, "DPG-E019",
					"constraint %q references column %q, which was just renamed to %q — update the constraint to use the new name",
					c.Name, *c.CheckColumn, newName)
			}
		}
	}
	for _, idx := range o.Indexes {
		for _, col := range idx.Columns {
			if col.Name == "" {
				continue // expression index column, not a simple reference
			}
			if newName, stale := renamedCols[col.Name]; stale {
				return pipeline.ErrorfCode(idx.Pos, "DPG-E019",
					"index %q references column %q, which was just renamed to %q — update the index to use the new name",
					idx.Name, col.Name, newName)
			}
		}
		for _, col := range idx.Include {
			if newName, stale := renamedCols[col]; stale {
				return pipeline.ErrorfCode(idx.Pos, "DPG-E019",
					"index %q INCLUDEs column %q, which was just renamed to %q — update the index to use the new name",
					idx.Name, col, newName)
			}
		}
	}
	return nil
}

func diffColumns(tbl string, o *ir.Table, snap *snapshot.SnapTable, env *diffEnv) ([]pipeline.DiffOp, map[string]string, map[string]bool, error) {
	// A typed table (Section 7.1's OF type_name) never has its own
	// independently-diffed Columns in this codebase — the desired side is
	// always empty (buildTable rejects any WITH OPTIONS narrowing column,
	// see ir.Table.OfType's doc comment) while the snapshot side, once
	// populated from a live-introspected typed table, genuinely does carry
	// the type's inherited attributes (introspectColumns reads pg_attribute
	// with no reloftype awareness). Comparing the two would otherwise
	// propose dropping and re-adding every one of them on every plan —
	// they aren't independent columns PostgreSQL lets you ADD/DROP on a
	// typed table anyway.
	if o.OfType.Name != "" {
		return nil, nil, nil, nil
	}
	var ops []pipeline.DiffOp

	// notNullConstrainedCols tracks columns whose NOT NULL is governed by a
	// promoted table-level Constraint (explicit name and/or PostgreSQL 18+
	// NO INHERIT — see buildColumn/buildConstraint's promotion rule) rather
	// than the plain Column.NotNull bool. diffConstraints' own ADD
	// CONSTRAINT/ALTER CONSTRAINT path already handles the full "make not
	// null" transition for these (confirmed live: ADD CONSTRAINT name NOT
	// NULL col performs the transition itself, no separate SET NOT NULL
	// needed) — a bare ALTER COLUMN SET NOT NULL emitted here as well for
	// the same column, same run, would either be redundant or, if the
	// eventual NO INHERIT differs from what SET NOT NULL's own
	// auto-created constraint gets, a real PostgreSQL error ("cannot
	// change NO INHERIT status of NOT NULL constraint ..."), confirmed
	// live.
	notNullConstrainedCols := make(map[string]bool)
	for _, cst := range o.Constraints {
		if cst.Type == "NOT NULL" && len(cst.Columns) == 1 {
			notNullConstrainedCols[cst.Columns[0]] = true
		}
	}

	// PostgreSQL PRIMARY KEY implies NOT NULL. Snapshots written before this
	// inference was applied may have not_null=false for PK columns; normalise
	// them here so we don't emit a spurious SET NOT NULL on every plan run.
	pkCols := make(map[string]bool)
	for _, sc := range snap.Constraints {
		if sc.Type == "PRIMARY KEY" {
			for _, col := range localConstraintCols(sc.Expr) {
				pkCols[col] = true
			}
		}
	}
	for i := range snap.Columns {
		if pkCols[snap.Columns[i].Name] {
			snap.Columns[i].NotNull = true
		}
	}

	snapByName := make(map[string]*snapshot.SnapColumn, len(snap.Columns))
	for i := range snap.Columns {
		snapByName[snap.Columns[i].Name] = &snap.Columns[i]
	}

	desiredHasName := make(map[string]bool, len(o.Columns))
	for _, col := range o.Columns {
		desiredHasName[col.Name] = true
	}

	// Columns renamed in desired: map old→new name.
	renamedFrom := make(map[string]string) // snapName → desiredName
	for _, col := range o.Columns {
		if col.RenamedFrom == nil {
			continue
		}
		if desiredHasName[*col.RenamedFrom] {
			// Caught even in post-apply state: the user listed both old and
			// new in the table's ( ) section while also asserting a rename.
			// Snapshot state can't disambiguate this — it's always wrong.
			return nil, nil, nil, pipeline.Errorf(col.SrcPos,
				"RENAMED FROM %q on column %q in %s collides with another column of the same name in the desired DDL. Remove the stale column from the table's ( ) list.",
				*col.RenamedFrom, col.Name, tbl)
		}
		_, oldInSnap := snapByName[*col.RenamedFrom]
		_, newInSnap := snapByName[col.Name]
		if newInSnap {
			// Post-apply / no-op state: the snapshot already has the new
			// name. Don't add to the rename map (no SQL needed) and don't
			// validate the directive — the rename has already happened.
			continue
		}
		if !oldInSnap {
			return nil, nil, nil, pipeline.ErrorfCode(col.SrcPos, "DPG-E021",
				"RENAMED FROM %q on column %q in %s does not match the snapshot — neither the old nor the new name exists there. Remove RENAMED FROM if this is a genuinely new column.",
				*col.RenamedFrom, col.Name, tbl)
		}
		renamedFrom[*col.RenamedFrom] = col.Name
		ops = append(ops, safeOp(
			fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s;",
				tbl, quoteIdent(*col.RenamedFrom), quoteIdent(col.Name)),
			col.SrcPos,
		))
	}

	desiredByName := make(map[string]*ir.Column, len(o.Columns))
	for _, col := range o.Columns {
		desiredByName[col.Name] = col
	}

	// Drop columns absent from desired (and not just renamed).
	droppedCols := make(map[string]bool)
	for _, sc := range snap.Columns {
		if _, ok := renamedFrom[sc.Name]; ok {
			continue // renamed away
		}
		if _, ok := desiredByName[sc.Name]; !ok {
			droppedCols[sc.Name] = true
			ops = append(ops, destructiveOp(
				fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", tbl, quoteIdent(sc.Name)),
				pipeline.SourcePos{},
			))
		}
	}

	// Add new columns or alter existing ones.
	for _, col := range o.Columns {
		// After a rename, the snap col is stored under the old name.
		snapColName := col.Name
		if col.RenamedFrom != nil {
			if _, ok := snapByName[*col.RenamedFrom]; ok {
				snapColName = *col.RenamedFrom
			}
		}
		sc, exists := snapByName[snapColName]

		if !exists {
			// ADD COLUMN
			var b strings.Builder
			if col.Serial != nil {
				fmt.Fprintf(&b, "ALTER TABLE %s ADD COLUMN %s %s", tbl, quoteIdent(col.Name), *col.Serial)
			} else {
				fmt.Fprintf(&b, "ALTER TABLE %s ADD COLUMN %s %s", tbl, quoteIdent(col.Name), col.Type.String())
			}
			if col.NotNull && col.Serial == nil && !notNullConstrainedCols[col.Name] {
				b.WriteString(" NOT NULL")
			}
			if col.Default != nil && col.Serial == nil {
				b.WriteString(" DEFAULT ")
				b.WriteString(*col.Default)
			}
			if col.Identity != nil {
				if col.Identity.Always {
					b.WriteString(" GENERATED ALWAYS AS IDENTITY")
				} else {
					b.WriteString(" GENERATED BY DEFAULT AS IDENTITY")
				}
				writeIdentityOpts(&b, col.Identity)
			}
			if col.Generated != nil {
				b.WriteString(" GENERATED ALWAYS AS (")
				b.WriteString(col.Generated.Expr)
				if col.Generated.Stored {
					b.WriteString(") STORED")
				} else {
					b.WriteString(") VIRTUAL")
				}
			}
			b.WriteString(";")
			// CAUTION when existing rows make it fail (NOT NULL with nothing
			// to fill it) or make PostgreSQL rewrite the table (RFC Section
			// 17.2). SERIAL, identity and generated columns fill
			// themselves. A notNullConstrainedCols column's NOT NULL is a
			// separate ADD CONSTRAINT, classified by diffConstraints.
			safety := pipeline.Safe
			if col.NotNull && col.Default == nil && col.Identity == nil && col.Generated == nil && col.Serial == nil && !notNullConstrainedCols[col.Name] {
				safety = pipeline.Caution
			}
			if env.addColumnRewritesTable(col) {
				safety = pipeline.Caution
			}
			ops = append(ops, &op{sql: b.String(), safety: safety, pos: col.SrcPos, mode: pipeline.ModeTransactional})
			if txt := effectiveComment(col.Comment, col.Deprecated); txt != nil {
				ops = append(ops, safeOp(
					fmt.Sprintf("COMMENT ON COLUMN %s.%s IS %s;", tbl, quoteIdent(col.Name), quoteLit(*txt)),
					col.SrcPos,
				))
			}
			for _, g := range col.Grants {
				ops = append(ops, colGrantOp(g, tbl, col.Name, col.SrcPos))
			}
			for _, r := range col.Revocations {
				ops = append(ops, colExplicitRevokeOp(r, tbl, col.Name, col.SrcPos))
			}
			continue
		}

		// Alter existing column.
		resolvedType := col.Type.String()
		if col.Serial != nil && isLegacySerialTypeName(sc.Type) {
			// Pre-upgrade snapshot stored the literal, un-normalized
			// "serial"/"bigserial"/"smallserial" type name (this project's
			// own prior representation, before SERIAL got a distinct IR
			// marker) — the live column hasn't actually changed shape, only
			// DPG's own internal representation of it has. Treat as no
			// drift; the snapshot self-heals to the new normalized shape
			// ("integer"+Serial marker etc.) on this run's save, the same
			// way a plpgsql BodyHash-algorithm upgrade self-heals (see
			// internal/ir/typeutil.go's canonicalizePlpgsqlBody).
		} else if sc.Type == legacyTypeNameBeforeFix(resolvedType) {
			// Pre-2026-08-17 snapshot stored the un-normalized short
			// spelling ("timestamp"/"time"/"varbit") for a type
			// PGCatalogName now maps to its full canonical form — see
			// legacyTypeNameBeforeFix's doc comment. The live column hasn't
			// actually changed, only DPG's own rendering of it has. Treat
			// as no drift; self-heals to the new spelling on this run's
			// save, same pattern as the SERIAL case just above.
		} else if legacyVirtualTypeSnapshot(col, sc) {
			// A snapshot written before virtual type references were
			// resolved at compile time stored the reference itself
			// ("line_item[]") as the column type; the live column is
			// already json/jsonb. No drift; self-heals on save.
		} else if resolvedType != sc.Type {
			using := ""
			// RFC Section 7.2/Section 17.2 "Column type change diffing": a type change is
			// CAUTION when the user supplies an explicit USING expression
			// (their own safe conversion), OR when no USING is given but
			// PostgreSQL itself has an implicit cast (pg_cast.castcontext =
			// 'i') between the old and new base types — RFC Section 17.2's own
			// "ALTER TABLE ALTER COLUMN TYPE (implicit cast) -> CAUTION" row
			// — OR the change is a same-base-type modifier (length/
			// precision/scale) widening PostgreSQL applies with no data
			// loss, RFC Section 7.2's own primary example (VARCHAR(10) ->
			// VARCHAR(20)) — see typmod_widening.go for the full per-type-
			// family rules, each verified live, not assumed. Anything else
			// (no USING, no implicit cast, no provable widening) stays the
			// safe, conservative DESTRUCTIVE default. Found live-testing a
			// demo project: this was once hardcoded destructiveOp
			// unconditionally, so even a correctly-supplied USING clause
			// got treated as potential data loss requiring
			// --allow-destructive.
			safety := pipeline.Destructive
			switch {
			case col.Using != nil:
				using = " USING " + *col.Using
				safety = pipeline.Caution
			case hasImplicitCast(sc.Type, resolvedType):
				safety = pipeline.Caution
			case typmodWideningSafe(sc.Type, resolvedType):
				safety = pipeline.Caution
			}
			ops = append(ops, &op{
				sql:    fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s%s;", tbl, quoteIdent(col.Name), resolvedType, using),
				safety: safety,
				pos:    col.SrcPos,
				mode:   pipeline.ModeTransactional,
			})
		}
		ops = append(ops, diffGeneratedColumn(tbl, col, sc, resolvedType)...)
		// A notNullConstrainedCols column's "make not null" transition is
		// handled entirely by diffConstraints' own ADD CONSTRAINT NOT NULL
		// (which performs the transition itself — confirmed live) — see
		// notNullConstrainedCols' doc comment above for why a bare SET NOT
		// NULL here as well would be redundant at best, a real PostgreSQL
		// error at worst.
		if col.NotNull && !sc.NotNull && !notNullConstrainedCols[col.Name] {
			ops = append(ops, cautionOp(
				fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET NOT NULL;", tbl, quoteIdent(col.Name)),
				col.SrcPos,
			))
		} else if !col.NotNull && sc.NotNull {
			ops = append(ops, safeOp(
				fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP NOT NULL;", tbl, quoteIdent(col.Name)),
				col.SrcPos,
			))
		}
		ops = append(ops, diffIdentityColumn(tbl, col, sc)...)
		if !ptrEq(col.Default, sc.Default) {
			if col.Default != nil {
				ops = append(ops, safeOp(
					fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s;",
						tbl, quoteIdent(col.Name), *col.Default),
					col.SrcPos,
				))
			} else {
				ops = append(ops, safeOp(
					fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;", tbl, quoteIdent(col.Name)),
					col.SrcPos,
				))
			}
		}
		if desiredTxt, snapTxt := effectiveComment(col.Comment, col.Deprecated), effectiveComment(sc.Comment, sc.Deprecated); !ptrEq(desiredTxt, snapTxt) {
			if desiredTxt != nil {
				ops = append(ops, safeOp(
					fmt.Sprintf("COMMENT ON COLUMN %s.%s IS %s;", tbl, quoteIdent(col.Name), quoteLit(*desiredTxt)),
					col.SrcPos,
				))
			} else {
				ops = append(ops, safeOp(
					fmt.Sprintf("COMMENT ON COLUMN %s.%s IS NULL;", tbl, quoteIdent(col.Name)),
					col.SrcPos,
				))
			}
		}
		if !ptrEq(col.Storage, sc.Storage) && col.Storage != nil {
			ops = append(ops, safeOp(
				fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET STORAGE %s;",
					tbl, quoteIdent(col.Name), *col.Storage),
				col.SrcPos,
			))
		}
		if !ptrEq(col.Compression, sc.Compression) && col.Compression != nil {
			ops = append(ops, safeOp(
				fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET COMPRESSION %s;",
					tbl, quoteIdent(col.Name), *col.Compression),
				col.SrcPos,
			))
		}
		if col.Statistics != nil && (sc.Statistics == nil || *col.Statistics != *sc.Statistics) {
			ops = append(ops, safeOp(
				fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET STATISTICS %d;",
					tbl, quoteIdent(col.Name), *col.Statistics),
				col.SrcPos,
			))
		} else if col.Statistics == nil && sc.Statistics != nil {
			// Reset to server default (-1 instructs PG to use default_statistics_target).
			ops = append(ops, safeOp(
				fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET STATISTICS -1;",
					tbl, quoteIdent(col.Name)),
				col.SrcPos,
			))
		}
		ops = append(ops, diffColGrantSet(tbl, col.Name, sc.Grants, col.Grants, col.SrcPos)...)
		ops = append(ops, diffColRevocationSet(tbl, col.Name, sc.Revocations, col.Revocations, col.SrcPos)...)
		ops = append(ops, diffSecurityLabelSet(sc.SecurityLabels, col.SecurityLabels, "COLUMN "+tbl+"."+quoteIdent(col.Name), col.SrcPos)...)
	}

	return ops, renamedFrom, droppedCols, nil
}

// diffGeneratedColumn diffs col.Generated (desired) against sc.Generated/
// sc.GeneratedVirtual (snapshot) — RFC Section 7.4's generated-column
// diffing table, referenced from Section 7.2's VIRTUAL documentation but
// not previously implemented at all (diffColumns never compared Generated
// against the snapshot before this — any change to a generated column's
// expression, or a STORED/VIRTUAL switch, was silently undetected).
//
// Real PostgreSQL's actual ALTER COLUMN surface for generated columns
// (confirmed live against a PostgreSQL 18 container, not assumed from the
// RFC's own pre-existing text — see the "genuinely new" case below for why
// that mattered): SET EXPRESSION changes an existing generated column's
// expression in place without touching attgenerated (the STORED/VIRTUAL
// kind is immutable via that path); DROP EXPRESSION converts a generated
// column back to a plain one, freezing its last computed value. There is
// no ALTER COLUMN form that turns an already-existing plain column into a
// generated one, or that changes STORED<->VIRTUAL — confirmed live
// (ALTER COLUMN c ADD GENERATED ALWAYS AS (expr) STORED is not valid syntax
// at all; ADD GENERATED only ever applies to ...AS IDENTITY). Both of
// those transitions need the column dropped and re-added.
func diffGeneratedColumn(tbl string, col *ir.Column, sc *snapshot.SnapColumn, resolvedType string) []pipeline.DiffOp {
	var ops []pipeline.DiffOp

	genExpr := func(g *ir.Generated) string {
		kw := "STORED"
		if !g.Stored {
			kw = "VIRTUAL"
		}
		return fmt.Sprintf("GENERATED ALWAYS AS (%s) %s", g.Expr, kw)
	}

	dropAndReadd := func() {
		ops = append(ops, destructiveOp(
			fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", tbl, quoteIdent(col.Name)),
			col.SrcPos,
		))
		ops = append(ops, destructiveOp(
			fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s;", tbl, quoteIdent(col.Name), resolvedType),
			col.SrcPos,
		))
	}

	switch {
	case col.Generated == nil && sc.Generated == nil:
		// No generated column on either side — nothing to do.
		return nil

	case col.Generated == nil && sc.Generated != nil && sc.GeneratedVirtual:
		// "removed, column kept" for a VIRTUAL column — confirmed live
		// against a real PostgreSQL 18 server that DROP EXPRESSION is
		// flatly rejected for VIRTUAL ("ALTER TABLE / DROP EXPRESSION is
		// not supported for virtual generated columns"), unlike STORED
		// just below. A VIRTUAL column has no stored value to freeze in
		// the first place (it's computed on read), so the only path is
		// dropping and re-adding as plain — DESTRUCTIVE, since any value a
		// prior read would have computed is simply gone, not preserved.
		dropAndReadd()

	case col.Generated == nil && sc.Generated != nil:
		// "removed, column kept" for a STORED column (RFC Section 7.4) —
		// real PostgreSQL's DROP EXPRESSION freezes the column's current
		// values with no rewrite, hence SAFE.
		ops = append(ops, safeOp(
			fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP EXPRESSION IF EXISTS;", tbl, quoteIdent(col.Name)),
			col.SrcPos,
		))

	case col.Generated != nil && sc.Generated == nil:
		// "added where none existed" — genuinely no in-place ALTER path in
		// real PostgreSQL (see doc comment above), so this drops and
		// re-adds the column as generated. DESTRUCTIVE: the column's prior
		// stored values are discarded, not merely rewritten in place.
		ops = append(ops, destructiveOp(
			fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", tbl, quoteIdent(col.Name)),
			col.SrcPos,
		))
		ops = append(ops, destructiveOp(
			fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s %s;", tbl, quoteIdent(col.Name), resolvedType, genExpr(col.Generated)),
			col.SrcPos,
		))

	case col.Generated.Stored != !sc.GeneratedVirtual:
		// STORED/VIRTUAL kind changed — part of the column's identity (see
		// doc comment above), same drop-and-recreate treatment as a
		// freshly-added generated column.
		ops = append(ops, destructiveOp(
			fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", tbl, quoteIdent(col.Name)),
			col.SrcPos,
		))
		ops = append(ops, destructiveOp(
			fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s %s;", tbl, quoteIdent(col.Name), resolvedType, genExpr(col.Generated)),
			col.SrcPos,
		))

	case normalizeWS(normalizeExprForCompare(col.Generated.Expr)) != normalizeWS(normalizeExprForCompare(*sc.Generated)):
		// Same STORED/VIRTUAL kind, expression text changed — real
		// PostgreSQL's targeted SET EXPRESSION, CAUTION (rewrites the
		// column's stored values in place for STORED; recomputes on next
		// read for VIRTUAL, but still a real behavioral change).
		//
		// normalizeExprForCompare (not just normalizeWS) matters here:
		// confirmed live that pg_get_expr wraps the introspected expression
		// in an extra outer paren pair ("(amount * 1.08)" for a source
		// "amount * 1.08") — without stripping it, every plan after a
		// dump/introspect round-trip would see a spurious expression
		// change and re-run SET EXPRESSION for no real reason.
		ops = append(ops, cautionOp(
			fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET EXPRESSION AS (%s);", tbl, quoteIdent(col.Name), col.Generated.Expr),
			col.SrcPos,
		))
	}

	return ops
}

// identityDirKw renders an identity column's direction keyword phrase for
// ADD GENERATED/SET GENERATED — "ALWAYS" or "BY DEFAULT".
func identityDirKw(always bool) string {
	if always {
		return "ALWAYS"
	}
	return "BY DEFAULT"
}

// diffIdentityColumn diffs col.Identity (desired) against sc.Identity/
// sc.Identity<Option> (snapshot) — RFC Section 7.4's identity-column
// diffing table, previously entirely unimplemented: diffColumns' "alter
// existing column" branch never read col.Identity at all, so adding,
// removing, or changing identity on an already-existing column produced
// zero DiffOps — permanent silent drift between source and the live
// database. Same bug class as diffGeneratedColumn above (RFC E.21), which
// this mirrors.
//
// Real PostgreSQL's ALTER COLUMN surface for identity (confirmed live
// against a PostgreSQL 17 container, not assumed from the RFC's own
// pre-existing text): ADD GENERATED {ALWAYS|BY DEFAULT} AS IDENTITY
// [(seq-options)] requires the column already be NOT NULL — the caller
// invokes this after the NOT NULL diff, which already queues SET NOT NULL
// first when identity is newly added to a nullable column; SET GENERATED
// {ALWAYS|BY DEFAULT} toggles direction in place; each sequence option has
// its own independent, targeted SET <option> form (SET INCREMENT BY n,
// SET START WITH n, SET MINVALUE n / SET NO MINVALUE, SET MAXVALUE n /
// SET NO MAXVALUE, SET CACHE n, SET CYCLE / SET NO CYCLE) — all confirmed
// live, all SAFE, no rewrite; DROP IDENTITY [IF EXISTS] removes the clause
// and keeps the column, also SAFE.
func diffIdentityColumn(tbl string, col *ir.Column, sc *snapshot.SnapColumn) []pipeline.DiffOp {
	colIdent := quoteIdent(col.Name)

	switch {
	case col.Identity == nil && sc.Identity == nil:
		return nil

	case col.Identity == nil && sc.Identity != nil:
		// "clause removed, column kept" — SAFE, freezes the sequence at its
		// current value; the column keeps whatever values it already holds.
		return []pipeline.DiffOp{safeOp(
			fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP IDENTITY IF EXISTS;", tbl, colIdent),
			col.SrcPos,
		)}

	case col.Identity != nil && sc.Identity == nil:
		// "clause added where none existed" — CAUTION per the RFC's own
		// table.
		var b strings.Builder
		fmt.Fprintf(&b, "ALTER TABLE %s ALTER COLUMN %s ADD GENERATED %s AS IDENTITY", tbl, colIdent, identityDirKw(col.Identity.Always))
		writeIdentityOpts(&b, col.Identity)
		b.WriteString(";")
		return []pipeline.DiffOp{cautionOp(b.String(), col.SrcPos)}
	}

	// Both sides declare identity — direction and each option are
	// independent, targeted SET clauses.
	var ops []pipeline.DiffOp
	if desiredAlways, snapAlways := col.Identity.Always, *sc.Identity == "ALWAYS"; desiredAlways != snapAlways {
		ops = append(ops, safeOp(
			fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET GENERATED %s;", tbl, colIdent, identityDirKw(desiredAlways)),
			col.SrcPos,
		))
	}

	setOp := func(clause string) {
		ops = append(ops, safeOp(
			fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET %s;", tbl, colIdent, clause),
			col.SrcPos,
		))
	}
	id := col.Identity
	if id.IncrementBy != nil && !int64PtrEq(id.IncrementBy, sc.IdentityIncrementBy) {
		setOp(fmt.Sprintf("INCREMENT BY %d", *id.IncrementBy))
	}
	if id.StartValue != nil && !int64PtrEq(id.StartValue, sc.IdentityStartValue) {
		setOp(fmt.Sprintf("START WITH %d", *id.StartValue))
	}
	if seqBoundChanged(id.MinValue, id.NoMinValue, sc.IdentityMinValue, sc.IdentityNoMinValue) {
		if id.NoMinValue {
			setOp("NO MINVALUE")
		} else {
			setOp(fmt.Sprintf("MINVALUE %d", *id.MinValue))
		}
	}
	if seqBoundChanged(id.MaxValue, id.NoMaxValue, sc.IdentityMaxValue, sc.IdentityNoMaxValue) {
		if id.NoMaxValue {
			setOp("NO MAXVALUE")
		} else {
			setOp(fmt.Sprintf("MAXVALUE %d", *id.MaxValue))
		}
	}
	if id.Cache != nil && !int64PtrEq(id.Cache, sc.IdentityCache) {
		setOp(fmt.Sprintf("CACHE %d", *id.Cache))
	}
	if id.Cycle != nil && (sc.IdentityCycle == nil || *id.Cycle != *sc.IdentityCycle) {
		if *id.Cycle {
			setOp("CYCLE")
		} else {
			setOp("NO CYCLE")
		}
	}

	return ops
}

// diffConstraints returns the constraint DDL ops for tbl, along with the set
// of constraint names it dropped via "ALTER TABLE ONLY tbl DROP CONSTRAINT"
// rather than a plain cascading drop (see the partitionLocalNames branch
// below) — diffPartitions/diffPartitionList use this to suppress the
// redundant ADD CONSTRAINT that would otherwise fire for the exact same
// constraint, now already physically present (as a local, non-inherited
// row) on every direct partition.
func diffConstraints(tbl string, o *ir.Table, snap *snapshot.SnapTable, fullSnap *pipeline.Snapshot, pos pipeline.SourcePos, renamedCols map[string]string, droppedCols map[string]bool) ([]pipeline.DiffOp, map[string]bool, error) {
	var ops []pipeline.DiffOp
	droppedViaOnly := make(map[string]bool)

	// partitionLocalNames collects every constraint name a direct partition
	// declares independently of the parent (RFC Section 7.3's "DROP
	// CONSTRAINT ... ONLY" gap, PostgreSQL 18+) — used below to decide
	// whether a constraint disappearing from THIS table's own declaration
	// must be dropped with ONLY (retaining it, now local, on every existing
	// partition) rather than the ordinary cascading drop that would remove
	// it there too. Only direct partitions participate — see
	// ir.Partition.Constraints' doc comment and diffPartitionList's
	// recursive calls, which deliberately don't thread this coordination
	// any deeper than one level.
	partitionLocalNames := make(map[string]bool)
	for _, p := range o.Partitions {
		for _, c := range p.Constraints {
			partitionLocalNames[c.Name] = true
		}
	}

	// colStillNotNull records every desired column that is still NOT NULL
	// (regardless of whether that's the plain bool or a promoted
	// Constraint) — used below to tell "a NOT NULL constraint disappeared
	// because NOT NULL itself was dropped from the column" (diffColumns'
	// own DROP NOT NULL already handles removing it, so this function must
	// not also try) apart from "the column is still NOT NULL, only its
	// name/NO INHERIT changed" (a genuine identity change still needing
	// its own drop-and-recreate here).
	colStillNotNull := make(map[string]bool, len(o.Columns))
	for _, col := range o.Columns {
		if col.NotNull {
			colStillNotNull[col.Name] = true
		}
	}

	// snapNotNullCol/snapHasPromotedNotNull together identify the "ordinary
	// bare NOT NULL is being promoted to a named/NO INHERIT constraint for
	// the first time" transition — see the "promoting an ordinary NOT
	// NULL" branch below for why this needs its own handling, separate
	// from both the generic ADD CONSTRAINT path and colStillNotNull above.
	snapNotNullCol := make(map[string]bool, len(snap.Columns))
	for _, sc := range snap.Columns {
		if sc.NotNull {
			snapNotNullCol[sc.Name] = true
		}
	}
	snapHasPromotedNotNull := make(map[string]bool, len(snap.Constraints))
	for _, sc := range snap.Constraints {
		if sc.Type != "NOT NULL" {
			continue
		}
		for _, col := range localConstraintCols(sc.Expr) {
			snapHasPromotedNotNull[col] = true
		}
	}

	// Inline constraints (e.g. `id BIGINT PRIMARY KEY`) have no user-supplied
	// name, so matching by name alone would treat them as new on every run.
	// An unnamed desired constraint is matched against the snapshot side two
	// ways:
	//   1. A structural signature (type + normalized expression) — matches
	//      an unnamed snapshot-side constraint. This is the offline
	//      plan/apply path: the persisted snapshot.json preserves an
	//      original unnamed declaration's empty name verbatim, so both sides
	//      key identically.
	//   2. PostgreSQL's own auto-naming algorithm, reconstructed (see
	//      pgAutoConstraintName) — matches a REAL generated name. This is
	//      the verify/plan --live path: live introspection's
	//      pg_constraint.conname is NEVER empty, since PG always assigns a
	//      name (e.g. "t_pkey") even when the user wrote none, so without
	//      this an unnamed inline PK/UNIQUE/FK/CHECK/EXCLUDE produced a
	//      self-inconsistent DROP+ADD pair on every single run. Covers
	//      PRIMARY KEY/UNIQUE/FOREIGN KEY/CHECK/EXCLUDE, including EXCLUDE
	//      elements that are expressions rather than plain columns — see
	//      predictName's "excl" case, which reconstructs PostgreSQL's own
	//      "expr"/"expr1" literal-fallback naming for any element shape its
	//      syntax-only PredictedName rules don't otherwise cover, so this
	//      strategy applies uniformly with no narrower EXCLUDE carve-out.
	// Note both matching strategies are identity-only, never full-definition
	// equality — this is a pre-existing property of this function (a named
	// constraint whose definition changes while keeping the same name was
	// already invisible to it before this fix) that now also applies to an
	// unnamed PRIMARY KEY specifically: PG's generated PK name never encodes
	// columns (a table has only one PK), so an unnamed PK moved to a
	// different column on both sides can't be distinguished from unchanged.
	// Genuine removal, or a change of constraint TYPE, are still caught
	// correctly either way, since those change which map entry (if any) a
	// snapshot constraint matches.
	// The snapshot expression still references pre-rename column names, so
	// apply the rename map first — otherwise a plain RENAMED FROM would
	// surface as a spurious drop+recreate of every constraint touching the
	// renamed column.
	key := func(name, typ, expr string) string {
		if name != "" {
			return "n:" + name
		}
		return "s:" + typ + "|" + normalizeWS(expr)
	}

	snapByKey := make(map[string]*snapshot.SnapConstraint, len(snap.Constraints))
	for i := range snap.Constraints {
		sc := &snap.Constraints[i]
		snapByKey[key(sc.Name, sc.Type, translateConstraintExpr(sc.Expr, renamedCols))] = sc
	}

	// The collision universe pgAutoConstraintName needs: every constraint
	// name already used by another table in this schema (schemaConstraintNames),
	// PLUS — for PRIMARY KEY/UNIQUE specifically, since those are index-backed —
	// every other RELATION name in the schema (schemaRelationNames), matching
	// PostgreSQL's own ChooseRelationName, which checks pg_class, not just
	// pg_constraint. FOREIGN KEY and CHECK aren't index-backed (their default
	// names go through ChooseConstraintName, which only ever checks
	// pg_constraint — see heap.c/pg_constraint.c), so they only get the
	// narrower constraint-name set (see schemaRelationNames' doc comment).
	// The current table's own names are excluded from both — those are
	// exactly the pre-existing assignments a recomputed name is trying to
	// reproduce, not competitors to avoid. Each unnamed constraint gets an
	// independent copy so that multiple unnamed constraints on the same
	// table don't influence one another's predicted name (PG's actual
	// within-batch collision order for that pathological case can't be
	// reconstructed after the fact).
	otherTableNames := schemaConstraintNames(fullSnap, o.Schema, o.Name)
	otherRelationNames := schemaRelationNames(fullSnap, o.Schema, o.Name)
	// predictName returns PostgreSQL's predicted auto-generated name for an
	// unnamed constraint, and whether a prediction was possible at all —
	// distinct from CHECK's cols==nil case (still a real, valid PG-generated
	// name, "tab_check" with no name2). The only case predictName cannot
	// predict is a malformed EXCLUDE with zero elements (see the "excl" case
	// below) — every real element shape, including a bare uncast expression,
	// is now predictable.
	predictName := func(c *ir.Constraint, label string) (string, bool) {
		existing := maps.Clone(otherTableNames)
		if label == "pkey" || label == "key" || label == "excl" {
			// EXCLUDE is index-backed (ChooseIndexName's exclusionOpNames
			// branch calls ChooseRelationName, the same pg_class-scanning
			// path PRIMARY KEY/UNIQUE use) — confirmed live: a plain table
			// coincidentally named like an EXCLUDE's predicted name forces
			// PostgreSQL to fall back to a "1"-suffixed name, exactly the
			// PRIMARY KEY/UNIQUE relation-name-collision case already
			// guarded for those two.
			maps.Copy(existing, otherRelationNames)
		}
		var cols []string
		switch label {
		case "check":
			// CHECK's name2 is the single referenced column, not the full
			// set c.Columns carries for other purposes (createTable's
			// inline-rendering marker) — see CheckColumn's doc comment.
			if c.CheckColumn != nil {
				cols = []string{*c.CheckColumn}
			}
		case "excl":
			// EXCLUDE's name2 is built from each element's own name, in
			// order, joined by "_" (ChooseIndexNameAddition — confirmed
			// live: EXCLUDE USING gist (room WITH =, during WITH &&) on
			// table "bookings" generates "bookings_room_during_excl"). A
			// plain column contributes its own name; any OTHER element
			// shape's contribution is Exclude.Elements[i].PredictedName,
			// populated by figureIndexColname (internal/ir/builder.go) — a
			// port of PostgreSQL's real FigureColnameInternal, confirmed
			// live to run BEFORE expression analysis (no catalog/OID
			// resolution involved), covering a bare function call (its own
			// name, never descending into arguments), NULLIF(a,b) (the
			// literal "nullif"), and a type cast (recursing into its
			// argument, falling back to the cast's own target type name
			// only when the argument isn't itself a column or function
			// call). A shape none of those rules cover — a bare, uncast
			// operator expression, e.g. "a + b" — leaves PredictedName
			// empty at the IR layer (see its doc comment), but this is
			// NOT the end of PostgreSQL's own algorithm: ChooseIndexColumnNames
			// (indexcmds.c) falls back to the literal string "expr" for any
			// element whose indexcolname AND simple-column name are both
			// unset — previously misdiagnosed in this codebase as
			// "unpredictable" and left as a false/false return, corrected
			// after live-verifying against PG 17 that
			// EXCLUDE USING gist ((a + b) WITH =) really does generate
			// "<table>_expr_excl", and two such elements on one constraint
			// dedup to "expr"/"expr1" as expected (dedupIndexColNames below
			// already handles that generically). This makes EXCLUDE naming
			// fully offline-predictable for every element shape; there is
			// no remaining case that needs a live catalog connection.
			if c.Exclude == nil || len(c.Exclude.Elements) == 0 {
				return "", false
			}
			var raw []string
			for _, el := range c.Exclude.Elements {
				switch {
				case el.Column != "":
					raw = append(raw, el.Column)
				case el.PredictedName != "":
					raw = append(raw, el.PredictedName)
				default:
					raw = append(raw, "expr")
				}
			}
			// PostgreSQL also deduplicates same-derived-name elements
			// within the one index (ChooseIndexColumnNames — confirmed
			// live: two lower(...) elements on different columns produce
			// "lower" and "lower1", joined as name2 "lower_lower1", not a
			// name2 with two identical "lower" components).
			cols = dedupIndexColNames(raw)
		default:
			cols = c.Columns
		}
		return pgAutoConstraintName(o.Name, cols, label, existing), true
	}

	desiredByKey := make(map[string]*ir.Constraint, len(o.Constraints))
	for _, c := range o.Constraints {
		desiredByKey[key(c.Name, c.Type, c.Expr)] = c
		if c.Name == "" {
			if label, ok := pgConstraintNameLabel(c.Type); ok {
				if predicted, predOK := predictName(c, label); predOK {
					desiredByKey["n:"+predicted] = c
				}
			}
		}
	}

	// Constraints renamed in desired: map old key -> new name, validated the
	// same shape as diffPartitionList/diffIndexes' RENAMED FROM handling.
	// RENAMED FROM is only reachable via the RFC's grammar on a NAMED
	// constraint (an unnamed one has no prior name to reference), so this
	// only ever keys off "n:"+name, never a structural key.
	renamedFrom := make(map[string]string) // snap key -> desired name
	for _, c := range o.Constraints {
		if c.RenamedFrom == nil {
			continue
		}
		oldKey, newKey := "n:"+*c.RenamedFrom, "n:"+c.Name
		if _, collide := desiredByKey[oldKey]; collide {
			return nil, nil, pipeline.Errorf(c.Pos,
				"RENAMED FROM %q on constraint %q collides with another constraint of the same name in the desired declaration. Remove the stale constraint.",
				*c.RenamedFrom, c.Name)
		}
		_, oldInSnap := snapByKey[oldKey]
		_, newInSnap := snapByKey[newKey]
		if newInSnap {
			// Post-apply / no-op state: the snapshot already has the new name.
			continue
		}
		if !oldInSnap {
			return nil, nil, pipeline.ErrorfCode(c.Pos, "DPG-E021",
				"RENAMED FROM %q on constraint %q does not match the snapshot — neither the old nor the new name exists there. Remove RENAMED FROM if this is a genuinely new constraint.",
				*c.RenamedFrom, c.Name)
		}
		renamedFrom[oldKey] = c.Name
	}

	for i := range snap.Constraints {
		sc := &snap.Constraints[i]
		skey := key(sc.Name, sc.Type, translateConstraintExpr(sc.Expr, renamedCols))
		if _, ok := desiredByKey[skey]; ok {
			continue
		}
		if _, wasRenamed := renamedFrom[skey]; wasRenamed {
			continue
		}
		// PG cascades constraint removal when the underlying column is dropped.
		// If any local column referenced by this constraint is being dropped,
		// skip emitting anything — DROP COLUMN already handles it (confirmed
		// live it cascades the whole constraint away even when only one of
		// several referenced columns is dropped — see anyDropped's own doc
		// comment).
		cols := localConstraintCols(sc.Expr)
		if anyDropped(cols, droppedCols) {
			continue
		}
		// A NOT NULL constraint (PostgreSQL 18+) genuinely going away —
		// the column stays, but is no longer NOT NULL at all — is already
		// fully handled by diffColumns' own DROP NOT NULL (confirmed live:
		// it removes the underlying constraint regardless of name/NO
		// INHERIT). Skip here to avoid a second, redundant DROP CONSTRAINT
		// targeting a name PostgreSQL has already removed. This is
		// deliberately narrower than the cascade-skip above: it must NOT
		// fire when the column is simply losing its explicit name/NO
		// INHERIT while staying NOT NULL (a real identity change still
		// needing its own drop-and-recreate under the new auto-generated
		// name, same limitation every other constraint kind already has).
		if desiredCols := localConstraintCols(translateConstraintExpr(sc.Expr, renamedCols)); sc.Type == "NOT NULL" && len(desiredCols) == 1 && !colStillNotNull[desiredCols[0]] {
			continue
		}
		if sc.Name == "" {
			// Cannot DROP CONSTRAINT without a name; surface a manual notice.
			ops = append(ops, destructiveOp(
				fmt.Sprintf("-- WARNING: unnamed constraint on %s (%s %s) is no longer in desired; drop it manually",
					tbl, sc.Type, sc.Expr),
				pos,
			))
			continue
		}
		if o.PartitionBy != nil && partitionLocalNames[sc.Name] {
			// RFC Section 7.3's "DROP CONSTRAINT ... ONLY" (PostgreSQL 18+): a
			// direct partition independently declares a constraint of this
			// same name, so the user's intent is to detach it from the
			// parent while keeping it live on the partitions, not to remove
			// it everywhere. "ALTER TABLE ONLY tbl DROP CONSTRAINT" is real
			// PostgreSQL's own mechanism for exactly this (confirmed live,
			// PG17 vs PG18: PG17 rejects it outright — "cannot remove
			// constraint from only the partitioned table when partitions
			// exist" — while PG18 succeeds, flipping the identical row on
			// every existing partition from inherited to local rather than
			// dropping it there too). No proactive min_pg_version gate here,
			// matching every other PostgreSQL 18+ construct in this
			// codebase — a pre-18 target server rejects the statement with
			// its own clear error at apply time (passthrough principle).
			// SAFE, not DESTRUCTIVE, for the same reason NO INHERIT above
			// is SAFE rather than CAUTION: catalog-only change, no rewrite,
			// and nothing is actually destroyed — the constraint keeps
			// enforcing everywhere it currently applies; only the
			// propagate-to-future-partitions guarantee is what's being
			// given up, and that's the explicit, declared intent here.
			ops = append(ops, safeOp(
				fmt.Sprintf("ALTER TABLE ONLY %s DROP CONSTRAINT %s;", tbl, quoteIdent(sc.Name)),
				pos,
			))
			droppedViaOnly[sc.Name] = true
			continue
		}
		ops = append(ops, destructiveOp(
			fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;", tbl, quoteIdent(sc.Name)),
			pos,
		))
	}
	// commentOp emits (or clears) a COMMENT ON CONSTRAINT for an
	// already-existing constraint — unnamed constraints can't be targeted
	// (same limitation already documented above for DROP CONSTRAINT), so sc
	// carries the real catalog name the way validateConstraintOp's own
	// sc.Name argument does.
	commentOp := func(sc *snapshot.SnapConstraint, c *ir.Constraint) []pipeline.DiffOp {
		if sc.Name == "" || ptrStr(c.Comment) == sc.Comment {
			return nil
		}
		if c.Comment != nil {
			return []pipeline.DiffOp{safeOp(
				fmt.Sprintf("COMMENT ON CONSTRAINT %s ON %s IS %s;", quoteIdent(sc.Name), tbl, quoteLit(*c.Comment)),
				c.Pos,
			)}
		}
		return []pipeline.DiffOp{safeOp(
			fmt.Sprintf("COMMENT ON CONSTRAINT %s ON %s IS NULL;", quoteIdent(sc.Name), tbl),
			c.Pos,
		)}
	}

	for _, c := range o.Constraints {
		if sc, exists := snapByKey[key(c.Name, c.Type, c.Expr)]; exists {
			matched, recreate := matchedConstraintOps(tbl, sc, c)
			ops = append(ops, matched...)
			if !recreate {
				ops = append(ops, commentOp(sc, c)...)
				continue
			}
			// CHECK's ENFORCED/NOT ENFORCED changed — matched's only entry
			// is the DROP; fall through to this constraint's own ADD
			// CONSTRAINT below to recreate it.
		} else if c.RenamedFrom != nil {
			if sc, exists := snapByKey["n:"+*c.RenamedFrom]; exists {
				if sc.Type == "CHECK" && sc.Name != "" && sc.NotEnforced != c.NotEnforced {
					// Renaming AND changing ENFORCED at once — real
					// PostgreSQL has no ALTER path for the latter on CHECK
					// at all (confirmed live), so this drops the OLD name
					// outright and falls through to ADD under the NEW one,
					// rather than renaming first and then immediately
					// dropping+recreating under the name it was just given.
					ops = append(ops, destructiveOp(
						fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;", tbl, quoteIdent(sc.Name)),
						c.Pos,
					))
				} else {
					// RENAMED FROM on a constraint (like index RENAMED FROM,
					// Section 7.7) emits ALTER TABLE ... RENAME CONSTRAINT —
					// SAFE, metadata-only — instead of the drop-and-recreate a
					// name-only difference would otherwise trigger. Subsequent
					// ops reference the constraint's NEW (post-rename) name, not
					// sc's snapshot name, since the rename already executed.
					ops = append(ops, safeOp(
						fmt.Sprintf("ALTER TABLE %s RENAME CONSTRAINT %s TO %s;", tbl, quoteIdent(sc.Name), quoteIdent(c.Name)),
						c.Pos,
					))
					renamedSc := *sc
					renamedSc.Name = c.Name
					matched, _ := matchedConstraintOps(tbl, &renamedSc, c)
					ops = append(ops, matched...)
					ops = append(ops, commentOp(&renamedSc, c)...)
					continue
				}
			}
		} else if c.Name == "" {
			if label, ok := pgConstraintNameLabel(c.Type); ok {
				if predicted, predOK := predictName(c, label); predOK {
					if sc, exists := snapByKey["n:"+predicted]; exists {
						matched, recreate := matchedConstraintOps(tbl, sc, c)
						ops = append(ops, matched...)
						if !recreate {
							ops = append(ops, commentOp(sc, c)...)
							continue
						}
					}
				}
			}
		}
		// Promoting an ordinary NOT NULL (RFC Section 7.3): the desired
		// side just gained a name and/or NO INHERIT for a column that was
		// ALREADY NOT NULL — via a bare inline declaration this codebase
		// deliberately never promotes to its own Constraint (see
		// buildColumn's promotion rule) — and, critically, PostgreSQL
		// itself already auto-created a real catalogued constraint for it
		// under its own generated name the moment the column became NOT
		// NULL. A plain ADD CONSTRAINT here would collide with that
		// existing row (confirmed live: "cannot change NO INHERIT status
		// of NOT NULL constraint ... on relation ..."), so this retargets
		// PostgreSQL's own auto-generated name instead — RENAME CONSTRAINT
		// to the new name (only if one was actually given; a bare NO
		// INHERIT with no CONSTRAINT keyword keeps the auto-name) and/or
		// ALTER CONSTRAINT NO INHERIT, exactly the machinery
		// alterNotNullInheritOp already provides for the "unchanged name,
		// only NO INHERIT differs" case above — reused here via a synthetic
		// SnapConstraint standing in for the live, snapshot-untracked
		// auto-created row.
		if c.Type == "NOT NULL" && len(c.Columns) == 1 &&
			snapNotNullCol[c.Columns[0]] && !snapHasPromotedNotNull[c.Columns[0]] {
			predicted := pgAutoConstraintName(o.Name, c.Columns, "not_null", maps.Clone(otherTableNames))
			newName := c.Name
			if newName == "" {
				newName = predicted
			}
			if newName != predicted {
				ops = append(ops, safeOp(
					fmt.Sprintf("ALTER TABLE %s RENAME CONSTRAINT %s TO %s;", tbl, quoteIdent(predicted), quoteIdent(newName)),
					c.Pos,
				))
			}
			ops = append(ops, alterNotNullInheritOp(tbl, &snapshot.SnapConstraint{Type: "NOT NULL", Name: newName, NoInherit: false}, c)...)
			continue
		}
		notValid := ""
		if c.NotValid {
			notValid = " NOT VALID"
		}
		// RFC Section 7.3's table-constraint clause order: body, then
		// [NOT VALID], then [ENFORCED|NOT ENFORCED] — CHECK/FOREIGN KEY
		// only, PostgreSQL 18+ (Section 7.2/7.3).
		enforcedClause := ""
		if c.NotEnforced {
			enforcedClause = " NOT ENFORCED"
		}
		var sql string
		if c.Name != "" {
			sql = fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s %s%s%s;",
				tbl, quoteIdent(c.Name), c.Expr, notValid, enforcedClause)
		} else {
			sql = fmt.Sprintf("ALTER TABLE %s ADD %s%s%s;", tbl, c.Expr, notValid, enforcedClause)
		}
		ops = append(ops, cautionOp(sql, c.Pos))
		if c.Comment != nil && c.Name != "" {
			ops = append(ops, safeOp(
				fmt.Sprintf("COMMENT ON CONSTRAINT %s ON %s IS %s;", quoteIdent(c.Name), tbl, quoteLit(*c.Comment)),
				c.Pos,
			))
		}
	}
	return ops, droppedViaOnly, nil
}

// matchedConstraintOps handles a table constraint matched against an
// existing snapshot entry by name (or predicted auto-name): the ordinary
// in-place changes (NOT VALID lifecycle, NOT NULL NO INHERIT, FK
// deferrable/ENFORCED) that real PostgreSQL supports via a targeted ALTER.
// Returns recreate=true when the match isn't enough on its own — a CHECK
// constraint's ENFORCED/NOT ENFORCED changed, and real PostgreSQL has no
// ALTER path for that at all (confirmed live, unlike FOREIGN KEY) — in
// which case ops holds only the DROP CONSTRAINT and the caller must fall
// through to its own ADD CONSTRAINT logic instead of treating this
// constraint as already up to date.
func matchedConstraintOps(tbl string, sc *snapshot.SnapConstraint, c *ir.Constraint) (ops []pipeline.DiffOp, recreate bool) {
	if sc.Type == "CHECK" && sc.Name != "" && sc.NotEnforced != c.NotEnforced {
		return []pipeline.DiffOp{destructiveOp(
			fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;", tbl, quoteIdent(sc.Name)),
			c.Pos,
		)}, true
	}
	ops = append(ops, alterFKAttributesOp(tbl, sc, c)...)
	// A FOREIGN KEY transitioning NOT ENFORCED -> ENFORCED is already
	// validated by alterFKAttributesOp's own ALTER CONSTRAINT ENFORCED as
	// a side effect (confirmed live: convalidated flips true in lockstep
	// with conenforced) — issuing VALIDATE CONSTRAINT as well, ordered
	// BEFORE that ALTER as this function's sequence originally had it,
	// isn't merely redundant but a real PostgreSQL error ("cannot validate
	// NOT ENFORCED constraint"), confirmed live. Ordering alterFKAttributesOp
	// first (above) fixes the sequencing; skipping validateConstraintOp
	// entirely for exactly this transition avoids emitting a statement
	// that's harmless once reordered but still pure noise.
	reenforcing := sc.Type == "FOREIGN KEY" && sc.NotEnforced && !c.NotEnforced
	if !reenforcing {
		ops = append(ops, validateConstraintOp("TABLE", tbl, sc, c)...)
	}
	ops = append(ops, alterNotNullInheritOp(tbl, sc, c)...)
	return ops, false
}

// validateConstraintOp emits ALTER <alterKW> ... VALIDATE CONSTRAINT ... when
// a constraint that was previously added NOT VALID has had NOT VALID removed
// from source — the second half of the RFC Section 7.3/Section 5.4 NOT VALID lifecycle
// (ADD CONSTRAINT ... NOT VALID, then later VALIDATE CONSTRAINT once the
// author is ready to scan existing rows). alterKW is "TABLE" or "DOMAIN"
// (real PostgreSQL's VALIDATE CONSTRAINT syntax is otherwise identical for
// both). sc.Name (the snapshot's recorded name), not c.Name, is used as the
// target: every PostgreSQL constraint has a real catalog name even when
// DPG's source never spelled one out, and sc only reaches here (for the
// table case) via a key that already matched an auto-predicted name. The
// reverse transition (NotValid: false → true) has no PostgreSQL equivalent
// — an already-validated constraint can't be marked NOT VALID again — so
// it's silently a no-op, same as any other unrepresentable state.
func validateConstraintOp(alterKW, tbl string, sc *snapshot.SnapConstraint, c *ir.Constraint) []pipeline.DiffOp {
	if !sc.NotValid || c.NotValid || sc.Name == "" {
		return nil
	}
	return []pipeline.DiffOp{cautionOp(
		fmt.Sprintf("ALTER %s %s VALIDATE CONSTRAINT %s;", alterKW, tbl, quoteIdent(sc.Name)),
		c.Pos,
	)}
}

// alterNotNullInheritOp emits ALTER TABLE ... ALTER CONSTRAINT name [NO]
// INHERIT when a table-level NOT NULL constraint's NO INHERIT flag differs
// from the snapshot but everything else about it (column, validity) is
// unchanged — RFC Section 7.3's "NOT NULL constraint inheritability
// (PostgreSQL 18+)" row. This is a separate, distinctly-named ALTER
// CONSTRAINT variant from FK's own deferrability/ENFORCED one
// (alterFKAttributesOp, below) — real PostgreSQL's ALTER CONSTRAINT grammar
// is constraint-kind-specific, not one shared option set — SAFE,
// metadata-only, confirmed live against a PostgreSQL 18 server. sc.Name (the
// snapshot's recorded/catalog name), not c.Name, is used as the target,
// matching validateConstraintOp's identical reasoning just above:
// PostgreSQL always assigns NOT NULL a real catalog name, even one DPG's own
// source never spelled out.
func alterNotNullInheritOp(tbl string, sc *snapshot.SnapConstraint, c *ir.Constraint) []pipeline.DiffOp {
	if sc.Type != "NOT NULL" || sc.Name == "" || sc.NoInherit == c.NoInherit {
		return nil
	}
	kw := "INHERIT"
	if c.NoInherit {
		kw = "NO INHERIT"
	}
	return []pipeline.DiffOp{safeOp(
		fmt.Sprintf("ALTER TABLE %s ALTER CONSTRAINT %s %s;", tbl, quoteIdent(sc.Name), kw),
		c.Pos,
	)}
}

// alterFKAttributesOp emits a single combined ALTER TABLE ... ALTER
// CONSTRAINT name [[NOT] DEFERRABLE [INITIALLY ...]] [ENFORCED|NOT
// ENFORCED] when a FOREIGN KEY constraint's deferrability and/or PostgreSQL
// 18+ ENFORCED state differ from the snapshot but everything else about it
// is unchanged — RFC Section 7.3's "Deferrability-only changes" row (real
// PostgreSQL's ALTER CONSTRAINT supports only this narrow set of changes,
// and only for FOREIGN KEY constraints — confirmed live: the identical
// statement against a CHECK constraint errors "cannot alter enforceability
// of constraint ... is not a foreign key constraint"), extended to also
// cover ENFORCED/NOT ENFORCED in the same statement when both differ at
// once, matching real PostgreSQL's own combined grammar rather than two
// separate ALTER CONSTRAINT statements. SAFE, metadata-only. Toggling
// ENFORCED back on also re-validates the constraint as a side effect
// (confirmed live: convalidated flips true→false→true in lockstep with
// conenforced) — validateConstraintOp may also fire redundantly alongside
// this in that case; a VALIDATE CONSTRAINT on an already-valid constraint
// is a harmless no-op in real PostgreSQL, so this isn't specially
// suppressed.
func alterFKAttributesOp(tbl string, sc *snapshot.SnapConstraint, c *ir.Constraint) []pipeline.DiffOp {
	if sc.Type != "FOREIGN KEY" || sc.Name == "" {
		return nil
	}
	deferChanged := sc.Deferrable != c.Deferrable || (c.Deferrable && sc.InitiallyDeferred != c.InitiallyDeferred)
	enforcedChanged := sc.NotEnforced != c.NotEnforced
	if !deferChanged && !enforcedChanged {
		return nil
	}
	var parts []string
	if deferChanged {
		if c.Deferrable {
			parts = append(parts, "DEFERRABLE")
			if c.InitiallyDeferred {
				parts = append(parts, "INITIALLY DEFERRED")
			} else {
				parts = append(parts, "INITIALLY IMMEDIATE")
			}
		} else {
			parts = append(parts, "NOT DEFERRABLE")
		}
	}
	if enforcedChanged {
		if c.NotEnforced {
			parts = append(parts, "NOT ENFORCED")
		} else {
			parts = append(parts, "ENFORCED")
		}
	}
	return []pipeline.DiffOp{safeOp(
		fmt.Sprintf("ALTER TABLE %s ALTER CONSTRAINT %s %s;", tbl, quoteIdent(sc.Name), strings.Join(parts, " ")),
		c.Pos,
	)}
}

// pgConstraintNameLabel returns the label PostgreSQL's auto-naming
// algorithm uses for a constraint type, and whether pgAutoConstraintName can
// reconstruct a name for it at all. For EXCLUDE this is necessary but not
// sufficient: a per-instance check in diffConstraints' predictName further
// restricts prediction to an EXCLUDE whose every element resolves to SOME
// name (a plain column, or the result of figureIndexColname for anything
// else) — see the comment there for exactly what that covers.
func pgConstraintNameLabel(typ string) (label string, ok bool) {
	switch typ {
	case "PRIMARY KEY":
		return "pkey", true
	case "UNIQUE":
		return "key", true
	case "FOREIGN KEY":
		return "fkey", true
	case "CHECK":
		return "check", true
	case "EXCLUDE":
		return "excl", true
	case "NOT NULL":
		return "not_null", true
	default:
		return "", false
	}
}

// pgAutoConstraintName replicates PostgreSQL's algorithm for generating a
// constraint name when the user supplies none (ChooseConstraintName /
// ChooseRelationName / makeObjectName, in src/backend/catalog/
// pg_constraint.c and src/backend/commands/indexcmds.c /tablecmds.c), so
// that verify/plan --live can recognize a live catalog's auto-generated
// name as the SAME constraint an inline unnamed declaration refers to.
//
// label is "pkey"/"key"/"fkey"/"check"/"excl" (see pgConstraintNameLabel);
// cols is ignored for "pkey" (PG's primary key name never depends on
// columns — a table can only have one), and is joined with "_" for the
// others (for "fkey" this must be the LOCAL/referencing columns, matching
// ChooseForeignKeyConstraintNameAddition, not the referenced table's; for
// "check" the caller must pass either a single-element slice — when the
// expression references exactly one distinct column — or nil, matching
// heap.c's pull_var_clause-based name2 selection, not the constraint's full
// column set; for "excl" the caller must pass every element's column name
// in source order, matching ChooseIndexNameAddition — never called at all
// when any element is an expression, see predictName's "excl" case).
// existingNames is mutated: each name this function returns is added to it,
// so a second call sharing the same map correctly avoids colliding with the
// first — callers that want independent (non-batch) predictions, as
// diffConstraints does, must pass a fresh copy per call.
func pgAutoConstraintName(table string, cols []string, label string, existingNames map[string]bool) string {
	var name2 string
	if label != "pkey" {
		name2 = strings.Join(cols, "_")
	}
	for pass := 0; ; pass++ {
		modlabel := label
		if pass > 0 {
			modlabel = fmt.Sprintf("%s%d", label, pass)
		}
		name := makeObjectName(table, name2, modlabel)
		if !existingNames[name] {
			existingNames[name] = true
			return name
		}
	}
}

// makeObjectName ports PostgreSQL's makeObjectName (src/backend/commands/
// indexcmds.c): build "name1_name2_label" (name2 omitted when empty),
// truncating name1/name2 — never label, which the caller retries with
// instead on collision — to fit within NAMEDATALEN-1 (63) bytes. Truncation
// shortens whichever of name1/name2 is currently longer, one byte at a
// time, then rounds each cut down to the nearest complete UTF-8 rune
// boundary (PG's pg_mbcliplen), so a multi-byte identifier is never split
// mid-character.
func makeObjectName(name1, name2, label string) string {
	const nameDataLen = 64 // PostgreSQL's NAMEDATALEN
	overhead := len(label) + 1
	if name2 != "" {
		overhead++
	}
	avail := nameDataLen - 1 - overhead

	n1, n2 := len(name1), len(name2)
	for n1+n2 > avail {
		if n1 > n2 {
			n1--
		} else {
			n2--
		}
	}
	n1 = mbClipLen(name1, n1)
	if name2 != "" {
		n2 = mbClipLen(name2, n2)
	}

	var b strings.Builder
	b.WriteString(name1[:n1])
	if name2 != "" {
		b.WriteByte('_')
		b.WriteString(name2[:n2])
	}
	b.WriteByte('_')
	b.WriteString(label)
	return b.String()
}

// mbClipLen returns the largest n <= limit such that s[:n] does not split a
// UTF-8 rune, mirroring PostgreSQL's pg_mbcliplen.
func mbClipLen(s string, limit int) int {
	if limit >= len(s) {
		return len(s)
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return limit
}

// dedupIndexColNames replicates PostgreSQL's ChooseIndexColumnNames: when
// two index elements independently derive the SAME name (e.g. two EXCLUDE
// elements that are both a call to the same function, on different
// columns), the second (and any further) conflicting one gets a numeric
// suffix — 1, 2, ... — appended, truncating the ORIGINAL name (never the
// suffix) down to a complete UTF-8 rune boundary if needed to fit
// NAMEDATALEN-1 bytes total. Confirmed live: EXCLUDE ((lower(a)) WITH =,
// (lower(b)) WITH =) generates the per-element names "lower"/"lower1"
// (joined by pgAutoConstraintName's caller into name2 "lower_lower1"), not
// two identical "lower" components. Each name is checked against every
// PRIOR (already-deduped) name in the same call, matching PG's own
// grow-the-result-list-as-you-go comparison.
func dedupIndexColNames(names []string) []string {
	const nameDataLen = 64 // PostgreSQL's NAMEDATALEN
	result := make([]string, 0, len(names))
	for _, origname := range names {
		curname := origname
		for i := 1; slices.Contains(result, curname); i++ {
			suffix := strconv.Itoa(i)
			n := mbClipLen(origname, nameDataLen-1-len(suffix))
			curname = origname[:n] + suffix
		}
		result = append(result, curname)
	}
	return result
}

// schemaConstraintNames collects every constraint name already in use by
// any OTHER table in the given schema, per fullSnap — the collision
// universe pgAutoConstraintName needs, since PostgreSQL's auto-naming
// avoids clashing with any constraint name in the same namespace (schema),
// not just the same table. excludeTable is left out deliberately: see the
// comment at diffConstraints's call site.
func schemaConstraintNames(fullSnap *pipeline.Snapshot, schema, excludeTable string) map[string]bool {
	names := make(map[string]bool)
	if fullSnap == nil {
		return names
	}
	for _, raw := range fullSnap.Objects {
		var so snapshot.SnapObject
		if err := json.Unmarshal(raw, &so); err != nil || so.Table == nil {
			continue
		}
		if so.Table.Schema != schema || so.Table.Name == excludeTable {
			continue
		}
		for _, c := range so.Table.Constraints {
			if c.Name != "" {
				names[c.Name] = true
			}
		}
	}
	return names
}

// schemaRelationNames collects every relation name (table, view, sequence,
// and index) already in use in the given schema, per fullSnap. This is the
// BROADER collision universe PostgreSQL's ChooseRelationName actually
// checks — a pg_class scan, not just a pg_constraint one — for PRIMARY
// KEY/UNIQUE/EXCLUDE specifically: those constraint types are backed by an
// index, so the auto-generated name must be unique among ALL relations in
// the namespace (a plain table happening to be named "orders_pkey" would
// force PG to fall back to "orders_pkey1" for an unnamed PK on "orders",
// even though no OTHER constraint is named "orders_pkey" — confirmed the
// same holds for EXCLUDE live: ChooseIndexName's exclusionOpNames branch
// calls ChooseRelationName too, exactly like the PRIMARY KEY branch).
// FOREIGN KEY and CHECK are NOT index-backed (their default names go
// through ChooseConstraintName, which only ever checks pg_constraint, never
// pg_class — see ChooseConstraintName in pg_constraint.c and heap.c's CHECK
// naming), so callers must not add this set for "fkey"/"check".
// excludeTable's own index names are left out — those are exactly the
// pre-existing backing-index assignment a recomputed name is trying to
// reproduce, not a competitor to avoid.
func schemaRelationNames(fullSnap *pipeline.Snapshot, schema, excludeTable string) map[string]bool {
	names := make(map[string]bool)
	if fullSnap == nil {
		return names
	}
	for _, raw := range fullSnap.Objects {
		var so snapshot.SnapObject
		if err := json.Unmarshal(raw, &so); err != nil {
			continue
		}
		switch {
		case so.Table != nil && so.Table.Schema == schema:
			if so.Table.Name != excludeTable {
				names[so.Table.Name] = true
			}
			if so.Table.Name != excludeTable {
				for _, idx := range so.Table.Indexes {
					names[idx.Name] = true
				}
			}
		case so.View != nil && so.View.Schema == schema:
			names[so.View.Name] = true
		case so.Sequence != nil && so.Sequence.Schema == schema:
			names[so.Sequence.Name] = true
		}
	}
	return names
}

// diffViewIndexes is diffIndexes' sibling for a materialized view's INDEXES
// (RFC Section 8.2). Simpler than the table version: a view has no COLUMN block, so
// there's no RENAMED FROM / DROP COLUMN cascade to translate index
// definitions through — indexes are matched by name and compared as-is.
// diffViewIndexes mirrors diffIndexes' RENAMED FROM handling (see its own
// doc comment) minus the column-rename translation step, which has no View
// counterpart.
func diffViewIndexes(schema, view string, desired []*ir.Index, snapIdx []snapshot.SnapIndex) ([]pipeline.DiffOp, error) {
	var ops []pipeline.DiffOp

	snapByName := make(map[string]*snapshot.SnapIndex, len(snapIdx))
	for i := range snapIdx {
		snapByName[snapIdx[i].Name] = &snapIdx[i]
	}
	desiredByName := make(map[string]*ir.Index, len(desired))
	for _, idx := range desired {
		desiredByName[idx.Name] = idx
	}

	renamedFrom, err := validateIndexRenames(desired, desiredByName, snapByName)
	if err != nil {
		return nil, err
	}

	for _, si := range snapIdx {
		if _, ok := desiredByName[si.Name]; ok {
			continue
		}
		if _, wasRenamed := renamedFrom[si.Name]; wasRenamed {
			continue
		}
		ops = append(ops, cautionOp(
			fmt.Sprintf("DROP INDEX IF EXISTS %s;", quoteIdent(si.Name)),
			pipeline.SourcePos{},
		))
	}
	for _, idx := range desired {
		si, exists := snapByName[idx.Name]
		if !exists && idx.RenamedFrom != nil {
			if oldSi, ok := snapByName[*idx.RenamedFrom]; ok {
				si, exists = oldSi, true
			}
		}
		if !exists {
			ops = append(ops, createIndex(schema, view, idx, existingIndexCreate(idx.Concurrently))...)
			continue
		}
		// Comment is excluded from the recreate-triggering comparison, same
		// reasoning as diffIndexes below. Where is normalized (not excluded)
		// the same way — see stripOuterParens' doc comment — since pg_get_expr
		// always wraps a WHERE predicate in one extra pair of parens on
		// reconstruction, which would otherwise compare unequal against an
		// unchanged, hand-written predicate that never had them. Name is also
		// excluded — a pure rename is its own ALTER INDEX below, not a
		// definition change requiring DROP+CREATE.
		desiredSnap := snapshot.ToSnapIndex(idx)
		definitionDesired, definitionSnap := desiredSnap, *si
		definitionDesired.Comment, definitionSnap.Comment = "", ""
		definitionDesired.Name, definitionSnap.Name = "", ""
		definitionDesired.Where = normalizeExprForCompare(definitionDesired.Where)
		definitionSnap.Where = normalizeExprForCompare(definitionSnap.Where)
		if definitionDesired != definitionSnap {
			ops = append(ops, cautionOp(
				fmt.Sprintf("DROP INDEX IF EXISTS %s;", quoteIdent(si.Name)),
				idx.Pos,
			))
			ops = append(ops, createIndex(schema, view, idx, existingIndexCreate(idx.Concurrently))...)
			continue
		}
		if si.Name != idx.Name {
			ops = append(ops, safeOp(
				fmt.Sprintf("ALTER INDEX %s RENAME TO %s;", quoteIdent(si.Name), quoteIdent(idx.Name)),
				idx.Pos,
			))
		}
		if desiredSnap.Comment != si.Comment {
			if idx.Comment != nil {
				ops = append(ops, safeOp(
					fmt.Sprintf("COMMENT ON INDEX %s IS %s;", quoteIdent(idx.Name), quoteLit(*idx.Comment)),
					idx.Pos,
				))
			} else {
				ops = append(ops, safeOp(
					fmt.Sprintf("COMMENT ON INDEX %s IS NULL;", quoteIdent(idx.Name)),
					idx.Pos,
				))
			}
		}
	}
	return ops, nil
}

// validateIndexRenames validates every desired index's RENAMED FROM
// directive against the snapshot, the same stale-directive validation shape
// as diffCompositeAttrs/diffPartitionList (see either's doc comment), and
// returns a map of snapshot name -> desired name for every genuine rename —
// used by callers to (a) look up the OLD snapshot entry when an index isn't
// found under its new name, and (b) suppress a spurious DROP INDEX for an
// entry consumed by a rename. Shared by diffIndexes and diffViewIndexes
// since indexes are identical objects in both contexts (no schema/table
// namespacing differs between the two).
func validateIndexRenames(desired []*ir.Index, desiredByName map[string]*ir.Index, snapByName map[string]*snapshot.SnapIndex) (map[string]string, error) {
	renamedFrom := make(map[string]string) // snap name -> desired name
	for _, idx := range desired {
		if idx.RenamedFrom == nil {
			continue
		}
		if _, collide := desiredByName[*idx.RenamedFrom]; collide {
			return nil, pipeline.Errorf(idx.Pos,
				"RENAMED FROM %q on index %q collides with another index of the same name in the desired declaration. Remove the stale index.",
				*idx.RenamedFrom, idx.Name)
		}
		_, oldInSnap := snapByName[*idx.RenamedFrom]
		_, newInSnap := snapByName[idx.Name]
		if newInSnap {
			// Post-apply / no-op state: the snapshot already has the new name.
			continue
		}
		if !oldInSnap {
			return nil, pipeline.ErrorfCode(idx.Pos, "DPG-E021",
				"RENAMED FROM %q on index %q does not match the snapshot — neither the old nor the new name exists there. Remove RENAMED FROM if this is a genuinely new index.",
				*idx.RenamedFrom, idx.Name)
		}
		renamedFrom[*idx.RenamedFrom] = idx.Name
	}
	return renamedFrom, nil
}

// diffIndexes' RENAMED FROM handling mirrors diffCompositeAttrs/
// diffPartitionList's identical shape (name-keyed collection, matched by old
// name within the same table, same stale-directive validation via
// validateIndexRenames) — an index has no independent schema, so matching is
// always within THIS table's own index list, never cross-schema.
func diffIndexes(schema, table string, o *ir.Table, snap *snapshot.SnapTable, renamedCols map[string]string, droppedCols map[string]bool) ([]pipeline.DiffOp, error) {
	var ops []pipeline.DiffOp

	snapByName := make(map[string]*snapshot.SnapIndex, len(snap.Indexes))
	for i := range snap.Indexes {
		snapByName[snap.Indexes[i].Name] = &snap.Indexes[i]
	}
	desiredByName := make(map[string]*ir.Index, len(o.Indexes))
	for _, idx := range o.Indexes {
		desiredByName[idx.Name] = idx
	}

	renamedFrom, err := validateIndexRenames(o.Indexes, desiredByName, snapByName)
	if err != nil {
		return nil, err
	}

	for _, si := range snap.Indexes {
		if _, ok := desiredByName[si.Name]; ok {
			continue
		}
		if _, wasRenamed := renamedFrom[si.Name]; wasRenamed {
			continue
		}
		// Indexes are matched by name. If a column was renamed via RENAMED FROM
		// and the index name is unchanged, PG keeps the index transparently.
		// Apply the rename map before deciding whether the snap index has truly
		// disappeared from desired (i.e. its only columns were dropped).
		cols := translateIndexCols(si.Columns, renamedCols)
		if anyDropped(cols, droppedCols) {
			continue // DROP COLUMN cascade handles it.
		}
		ops = append(ops, cautionOp(
			fmt.Sprintf("DROP INDEX IF EXISTS %s;", quoteIdent(si.Name)),
			pipeline.SourcePos{},
		))
	}
	for _, idx := range o.Indexes {
		// idx.Concurrently is only ever true when the source wrote
		// CONCURRENTLY explicitly on this index (a bare presence keyword,
		// same as real PostgreSQL — there is no project-wide default and no
		// boolean form). An index added to an EXISTING table (as opposed to
		// one created alongside its own brand-new table — see createTable)
		// honors that explicit request directly.
		concurrent := idx.Concurrently

		si, exists := snapByName[idx.Name]
		if !exists && idx.RenamedFrom != nil {
			if oldSi, ok := snapByName[*idx.RenamedFrom]; ok {
				si, exists = oldSi, true
			}
		}
		if !exists {
			ops = append(ops, createIndex(schema, table, idx, existingIndexCreate(concurrent))...)
			continue
		}
		// A same-named index must still be compared: PG has no generic ALTER
		// for an index's structural definition, so any change to method,
		// uniqueness, columns (incl. sort order/NULLS placement), WHERE,
		// INCLUDE, WITH, or NULLS NOT DISTINCT requires DROP + recreate.
		// Without this, an edited index that keeps its name was a silent
		// no-op on plan/apply.
		//
		// The snapshot's Columns/Include/Where still reference the OLD
		// column name after a RENAMED FROM — translate them before
		// comparing, or every index on a renamed column spuriously compares
		// unequal and gets dropped + recreated for no reason (real
		// PostgreSQL's ALTER TABLE RENAME COLUMN keeps every dependent
		// index transparently, no rebuild needed at all — confirmed live).
		// Mirrors diffConstraints' identical translateConstraintExpr step.
		translatedSnap := *si
		translatedSnap.Columns = translateIndexColumnList(si.Columns, renamedCols)
		translatedSnap.Include = translateIndexColumnList(si.Include, renamedCols)
		translatedSnap.Where = replaceQuotedIdents(si.Where, renamedCols)
		desiredSnap := snapshot.ToSnapIndex(idx)
		// Comment is excluded from the recreate-triggering comparison below
		// (createIndex already emits it on any DROP+CREATE, so it's still
		// applied when a real definition change happens alongside it) — an
		// index has no in-place ALTER for its structural definition, but it
		// does for its comment (COMMENT ON INDEX), so a comment-only edit
		// must not trigger a destructive rebuild. Name is also excluded — a
		// pure rename is its own ALTER INDEX below, not a definition change
		// requiring DROP+CREATE.
		definitionDesired, definitionSnap := desiredSnap, translatedSnap
		definitionDesired.Comment, definitionSnap.Comment = "", ""
		definitionDesired.Name, definitionSnap.Name = "", ""
		// Where is normalized (not excluded) the same way Comment is
		// excluded above — see stripOuterParens' doc comment — since
		// pg_get_indexdef always wraps a WHERE predicate in one extra pair
		// of parens on reconstruction, which would otherwise compare
		// unequal against an unchanged, hand-written predicate that never
		// had them.
		definitionDesired.Where = normalizeExprForCompare(definitionDesired.Where)
		definitionSnap.Where = normalizeExprForCompare(definitionSnap.Where)
		if definitionDesired != definitionSnap {
			ops = append(ops, cautionOp(
				fmt.Sprintf("DROP INDEX IF EXISTS %s;", quoteIdent(si.Name)),
				idx.Pos,
			))
			ops = append(ops, createIndex(schema, table, idx, existingIndexCreate(concurrent))...)
			continue
		}
		if si.Name != idx.Name {
			// Same mechanism and safety class as Constraint/composite-attribute
			// rename — metadata-only, no in-place ALTER exists for anything
			// else about an index's definition.
			ops = append(ops, safeOp(
				fmt.Sprintf("ALTER INDEX %s RENAME TO %s;", quoteIdent(si.Name), quoteIdent(idx.Name)),
				idx.Pos,
			))
		}
		if desiredSnap.Comment != translatedSnap.Comment {
			if idx.Comment != nil {
				ops = append(ops, safeOp(
					fmt.Sprintf("COMMENT ON INDEX %s IS %s;", quoteIdent(idx.Name), quoteLit(*idx.Comment)),
					idx.Pos,
				))
			} else {
				ops = append(ops, safeOp(
					fmt.Sprintf("COMMENT ON INDEX %s IS NULL;", quoteIdent(idx.Name)),
					idx.Pos,
				))
			}
		}
	}
	return ops, nil
}

// translateConstraintExpr rewrites quoted column identifiers inside a
// constraint's local-column list (or, for CHECK, the entire expression) so a
// snapshot expression captured before a RENAMED FROM matches the desired one.
// For PRIMARY KEY / UNIQUE / FOREIGN KEY only the first parenthesized group is
// touched — substituting globally would also rewrite remote-column refs after
// REFERENCES if a renamed name happened to collide.
func translateConstraintExpr(expr string, renamedCols map[string]string) string {
	if len(renamedCols) == 0 || expr == "" {
		return expr
	}
	upper := strings.ToUpper(strings.TrimSpace(expr))
	switch {
	case strings.HasPrefix(upper, "PRIMARY KEY"),
		strings.HasPrefix(upper, "UNIQUE"),
		strings.HasPrefix(upper, "FOREIGN KEY"):
		open, close := firstParenGroup(expr)
		if open == -1 {
			return expr
		}
		return expr[:open] + replaceQuotedIdents(expr[open:close+1], renamedCols) + expr[close+1:]
	case strings.HasPrefix(upper, "CHECK"), strings.HasPrefix(upper, "EXCLUDE"):
		// Unlike PRIMARY KEY/UNIQUE/FOREIGN KEY (which restrict translation to
		// the first paren group, to avoid touching FOREIGN KEY's unrelated
		// "REFERENCES othertable (othercol)" group), EXCLUDE has no such
		// second, different-table group to protect against: its element list
		// AND its optional WHERE clause both reference only this table's own
		// columns, same as CHECK. Translating the whole string catches a
		// renamed column wherever it appears (element list, an
		// expression-based element, or WHERE) — quoted column names always
		// match the quoteIdent form renderExclude emits.
		return replaceQuotedIdents(expr, renamedCols)
	}
	return expr
}

// localConstraintCols returns the unquoted local column names referenced in
// the first parenthesized group of a constraint expression. Used to decide
// whether a snapshot constraint's columns are entirely being dropped.
func localConstraintCols(expr string) []string {
	// NOT NULL (PostgreSQL 18+, RFC Section 7.3) is the one constraint kind
	// whose Expr carries no parenthesized column list at all — "NOT NULL
	// colname" or "NOT NULL colname NO INHERIT" — so it needs its own
	// extraction instead of falling through to firstParenGroup below, which
	// would otherwise return nil (no referenced columns found) and silently
	// disable both the column-dropped cascade-skip and any other caller
	// that needs this constraint's column.
	if rest, ok := strings.CutPrefix(expr, "NOT NULL "); ok {
		col, _, _ := strings.Cut(rest, " ")
		col = strings.Trim(col, `"`)
		if col == "" {
			return nil
		}
		return []string{col}
	}
	open, close := firstParenGroup(expr)
	if open == -1 {
		return nil
	}
	inside := expr[open+1 : close]
	var names []string
	for part := range strings.SplitSeq(inside, ",") {
		part = strings.TrimSpace(part)
		// Strip PostgreSQL 18+'s leading "PERIOD " on a temporal FOREIGN
		// KEY's local/referenced column (RFC Section 7.3) before the
		// trailing-suffix strip below — otherwise that strip (built for a
		// trailing modifier) would truncate "PERIOD valid_at" at its own
		// internal space and return the literal word "PERIOD" as the
		// column name instead of "valid_at".
		if rest, ok := strings.CutPrefix(part, "PERIOD "); ok {
			part = rest
		}
		// Strip optional sort/nulls suffixes that may appear on PK/UNIQUE
		// (or, for a temporal PRIMARY KEY/UNIQUE, "WITHOUT OVERLAPS").
		if sp := strings.IndexAny(part, " \t"); sp != -1 {
			part = part[:sp]
		}
		if len(part) >= 2 && part[0] == '"' && part[len(part)-1] == '"' {
			part = part[1 : len(part)-1]
		}
		if part != "" {
			names = append(names, part)
		}
	}
	return names
}

// translateIndexCols applies the rename map to a SnapIndex.Columns field
// (a comma-separated list of column names or `(expression)` entries, each
// optionally suffixed with ASC/DESC and/or NULLS FIRST/LAST — see
// snapshot.ToSnapIndex) and returns the resulting plain column names.
// Expression entries are returned as empty strings so they don't accidentally
// appear "dropped".
func translateIndexCols(cols string, renamedCols map[string]string) []string {
	if cols == "" {
		return nil
	}
	parts := strings.Split(cols, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "(") {
			out = append(out, "")
			continue
		}
		// Strip an optional " DESC"/" ASC"/" NULLS FIRST"/" NULLS LAST" suffix,
		// mirroring localConstraintCols's identical treatment for constraints.
		if sp := strings.IndexAny(p, " \t"); sp != -1 {
			p = p[:sp]
		}
		if newName, ok := renamedCols[p]; ok {
			p = newName
		}
		out = append(out, p)
	}
	return out
}

// translateIndexColumnList rewrites a SnapIndex-style comma-separated
// column/Include list (bare names, optionally suffixed with ASC/DESC/NULLS
// …, or a parenthesized "(expr)" entry — see snapshot.ToSnapIndex) so a
// renamed column's OLD name compares equal to the desired side's NEW name.
// Unlike translateIndexCols (which only extracts base names for the
// column-dropped check), this reconstructs the full entry text, suffix and
// all, for direct SnapIndex-to-SnapIndex comparison.
func translateIndexColumnList(list string, renamedCols map[string]string) string {
	if list == "" || len(renamedCols) == 0 {
		return list
	}
	parts := strings.Split(list, ", ")
	for i, p := range parts {
		if strings.HasPrefix(p, "(") {
			parts[i] = replaceQuotedIdents(p, renamedCols)
			continue
		}
		name, rest, hasSuffix := strings.Cut(p, " ")
		if newName, ok := renamedCols[name]; ok {
			name = newName
		}
		if hasSuffix {
			parts[i] = name + " " + rest
		} else {
			parts[i] = name
		}
	}
	return strings.Join(parts, ", ")
}

// firstParenGroup returns the byte indices of the matching '(' and ')' that
// open and close the first balanced parenthesized group, or (-1, -1).
func firstParenGroup(s string) (int, int) {
	open := strings.IndexByte(s, '(')
	if open == -1 {
		return -1, -1
	}
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return open, i
			}
		}
	}
	return -1, -1
}

// stripOuterParens removes one layer of genuinely-wrapping outer parens from
// s, e.g. "(a OR b)" -> "a OR b", but leaves "(a) OR (b)" untouched (the
// leading "(" there closes long before the string ends, so it isn't a
// wrapping pair at all) — reuses firstParenGroup's balanced-depth matching
// rather than a naive "starts with ( and ends with )" check, which would
// mishandle exactly that case.
//
// Exists because PostgreSQL's own expression deparser (pg_get_expr)
// unconditionally wraps a policy's USING/WITH CHECK qual and an index's
// WHERE predicate in one such pair when reconstructing it from the catalog
// (confirmed live: "(owner_id = 1)", "(status = 'active'::text)"),
// regardless of how the expression was originally written — while DPG's own
// parser either never captures those parens at all (USING/WITH CHECK, whose
// "(" ")" the parser consumes as its own delimiters) or preserves the
// expression exactly as written, parens or not (index WHERE, where they're
// grammatically optional). Either way, the desired and introspected sides of
// an unchanged expression can end up differing only by this one wrapping
// pair, comparing unequal and triggering a destructive drop+recreate for no
// real change. This is comparison-only normalization, the same "never
// change what's stored or emitted, only what's used to decide drift"
// precedent stripStringLiteralCasts/translateConstraintExpr already use —
// callers apply it to freshly-built comparison copies, never to the stored
// field itself.
func stripOuterParens(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '(' || s[len(s)-1] != ')' {
		return s
	}
	open, close := firstParenGroup(s)
	if open != 0 || close != len(s)-1 {
		return s
	}
	return strings.TrimSpace(s[1 : len(s)-1])
}

// normalizeExprForCompare combines stripOuterParens with
// stripStringLiteralCasts (below) — comparison-only normalization for the
// same class of expression (policy USING/WITH CHECK, index WHERE, trigger
// WHEN) that PostgreSQL's own deparser (pg_get_expr/pg_get_triggerdef)
// reconstructs with both an added outer-paren pair and an explicit
// "::type" cast on any string-literal operand that isn't already typed
// unambiguously by context — confirmed live for all three call sites
// (`(status = 'active'::text)` from a hand-written `status = 'active'`, no
// cast, on a policy, an index predicate, and a trigger WHEN condition
// alike). Applying only stripOuterParens (as the policy/index sites
// originally did) still leaves the cast difference as a false positive for
// any string-literal comparison — the single most common shape of a real
// WHEN/USING/WHERE clause — so both normalizations always travel together.
func normalizeExprForCompare(s string) string {
	return stripStringLiteralCasts(stripOuterParens(s))
}

// stripStringLiteralCasts removes a trailing "::typename" (optionally
// "::typename(args)", e.g. "::character varying(10)") immediately following
// a single-quoted string literal, without touching anything inside a quoted
// literal itself (including a doubled ” escaped quote). Comparison-only,
// same "never change what's stored or emitted, only what's used to decide
// drift" precedent as stripOuterParens/translateConstraintExpr.
func stripStringLiteralCasts(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] != '\'' {
			b.WriteByte(s[i])
			i++
			continue
		}
		j := i + 1
		for j < len(s) {
			if s[j] == '\'' {
				if j+1 < len(s) && s[j+1] == '\'' {
					j += 2
					continue
				}
				break
			}
			j++
		}
		end := j
		if end < len(s) {
			end++ // include the closing quote
		}
		b.WriteString(s[i:end])
		i = end
		if i+1 < len(s) && s[i] == ':' && s[i+1] == ':' {
			k := i + 2
			for k < len(s) && (isIdentByte(s[k]) || s[k] == ' ') {
				k++
			}
			for k > i+2 && s[k-1] == ' ' {
				k--
			}
			if k < len(s) && s[k] == '(' {
				depth := 0
				for ; k < len(s); k++ {
					if s[k] == '(' {
						depth++
					} else if s[k] == ')' {
						depth--
						if depth == 0 {
							k++
							break
						}
					}
				}
			}
			i = k // skip the cast, don't write it
		}
	}
	return b.String()
}

// foldTriggerPseudoRelNames lowercases whole-word "NEW"/"OLD" tokens outside
// any quoted span, matching PostgreSQL's own identifier-folding behavior for
// unquoted identifiers (pg_get_triggerdef always deparses a trigger's
// transition-relation references as lowercase "new"/"old" regardless of how
// the user originally cased them, confirmed live). Trigger-condition-only:
// Policy/Index expressions have no NEW/OLD pseudo-relations to fold.
// Comparison-only, same precedent as stripOuterParens/stripStringLiteralCasts.
func foldTriggerPseudoRelNames(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		c := s[i]
		if c == '\'' || c == '"' {
			j := i + 1
			for j < len(s) {
				if s[j] == c {
					if j+1 < len(s) && s[j+1] == c {
						j += 2
						continue
					}
					break
				}
				j++
			}
			end := j
			if end < len(s) {
				end++
			}
			b.WriteString(s[i:end])
			i = end
			continue
		}
		if isIdentStart(c) {
			j := i + 1
			for j < len(s) && isIdentByte(s[j]) {
				j++
			}
			word := s[i:j]
			lower := strings.ToLower(word)
			if lower == "new" || lower == "old" {
				b.WriteString(lower)
			} else {
				b.WriteString(word)
			}
			i = j
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentByte(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

// replaceQuotedIdents substitutes a renamed column's references in s, both
// quoted ("old" → "new") and bare (old → new, at a word boundary — no
// partial-identifier match, so renaming "a" never touches "cat"). The bare
// form matters because nodeToText/pg_query.Deparse — used to render CHECK
// expressions and EXCLUDE's WHERE clause/expression-based elements — only
// quotes an identifier that actually needs it (confirmed live: Deparse
// renders a plain lowercase column reference unquoted), unlike the
// hand-built, always-quoted column lists PRIMARY KEY/UNIQUE/FOREIGN KEY's
// Expr uses. Matching case-sensitively is protects against clobbering a SQL
// keyword: PostgreSQL folds an unquoted identifier to lowercase at
// declaration time, so a renamed column's key here is always lowercase,
// while Deparse always renders keywords (AND, OR, ASC, DESC, NULL, ...) in
// uppercase — confirmed live — so the two can never collide case-sensitively.
//
// Both substitutions are applied only OUTSIDE single-quoted SQL string
// literals (splitSQLStringLiterals) — an independent verification pass
// caught this as a real gap: a string literal's contents are delimited by
// `'`, a non-word character, so an unqualified `\bold\b` regex would
// otherwise treat 'old' *the string value* exactly like the bare identifier
// old (e.g. CHECK (status <> 'room') with column "room" being renamed would
// have mangled the unrelated literal 'room', producing a spurious
// unchanged-constraint drop+recreate — the exact class of noise this whole
// rename-translation feature exists to eliminate, reintroduced through a
// different path). The same literal-skip is applied to the quoted-ident
// substitution too, for the same reason, even though a real-world collision
// there would need a double-quoted substring INSIDE a single-quoted
// literal — pathological, but the fix is the same either way.
func replaceQuotedIdents(s string, renamedCols map[string]string) string {
	segs := splitSQLStringLiterals(s)
	for i, seg := range segs {
		if seg.isLiteral {
			continue
		}
		for old, newName := range renamedCols {
			seg.text = strings.ReplaceAll(seg.text, `"`+old+`"`, `"`+newName+`"`)
			seg.text = wordBoundaryRegexp(old).ReplaceAllString(seg.text, newName)
		}
		segs[i] = seg
	}
	var b strings.Builder
	for _, seg := range segs {
		b.WriteString(seg.text)
	}
	return b.String()
}

// wordBoundaryRegexp returns a compiled regexp matching ident as a whole
// word (bounded by non-identifier characters, including string start/end).
func wordBoundaryRegexp(ident string) *regexp.Regexp {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(ident) + `\b`)
}

// sqlSegment is one maximal run of text from splitSQLStringLiterals, tagged
// as either inside or outside a single-quoted SQL string literal.
type sqlSegment struct {
	text      string
	isLiteral bool
}

// splitSQLStringLiterals splits s into alternating literal/non-literal runs,
// so a caller can transform identifiers in the code portions without ever
// touching the contents of a string literal. Handles SQL's doubled-quote
// escape (” inside a literal means a literal single quote, not the end of
// the string) — confirmed this is the real PostgreSQL escaping rule, not
// backslash-escaping, which pg_query.Deparse's own output also uses (e.g.
// 'it”s'), so a naive "split on every apostrophe" would incorrectly end
// the literal early on that case.
func splitSQLStringLiterals(s string) []sqlSegment {
	var segs []sqlSegment
	i := 0
	for i < len(s) {
		start := i
		for i < len(s) && s[i] != '\'' {
			i++
		}
		if i > start {
			segs = append(segs, sqlSegment{text: s[start:i]})
		}
		if i >= len(s) {
			break
		}
		// s[i] == '\'': scan the literal, treating '' as an escaped quote.
		litStart := i
		i++
		for i < len(s) {
			if s[i] == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i += 2
					continue
				}
				i++
				break
			}
			i++
		}
		segs = append(segs, sqlSegment{text: s[litStart:i], isLiteral: true})
	}
	return segs
}

// anyDropped reports whether at least one of cols is being dropped —
// confirmed live (a real bug found implementing RFC Section 7.3's temporal
// FOREIGN KEY, PostgreSQL 18+, but general to every multi-column
// constraint/index, not specific to that feature) that PostgreSQL cascades
// away a constraint or index referencing SEVERAL columns the moment ANY
// ONE of them is dropped via ALTER TABLE ... DROP COLUMN — not only when
// every referenced column is dropped, which this function (formerly named
// allDropped) required before. The stricter, pre-fix ALL-columns condition
// meant a multi-column constraint/index losing only one of its columns
// still got an explicit DROP CONSTRAINT/DROP INDEX emitted for it,
// targeting a name PostgreSQL had already removed via cascade — confirmed
// live to fail outright ("constraint ... does not exist") for a temporal
// FOREIGN KEY's PERIOD column specifically, but the same failure mode
// applies to a plain multi-column UNIQUE and to a plain multi-column index
// too (both confirmed live). Both call sites (diffConstraints' and
// diffIndexes' removal loops) only ever reach this for a snapshot
// entry with no matching desired entry at all, so there is no risk of also
// suppressing a legitimate drop-and-recreate for one the user still wants
// on a narrowed column set — that entry would already have matched by key
// earlier and never reached here.
func anyDropped(cols []string, droppedCols map[string]bool) bool {
	if len(cols) == 0 || len(droppedCols) == 0 {
		return false
	}
	for _, c := range cols {
		if c != "" && droppedCols[c] {
			return true
		}
	}
	return false
}

// policyRoleKey returns a canonical, order-independent key for a policy's TO
// role list, treating an empty list the same as an explicit ["PUBLIC"] —
// PostgreSQL's own default (CREATE POLICY with no TO clause implicitly sets
// polroles to {0}/PUBLIC) — so a live-introspected policy that always shows
// a concrete PUBLIC role doesn't spuriously drift against source that never
// wrote a TO clause at all.
func policyRoleKey(roles []string) string {
	r := append([]string(nil), roles...)
	if len(r) == 0 {
		r = []string{"PUBLIC"}
	}
	sort.Strings(r)
	return strings.Join(r, ",")
}

// validatePolicyRenames mirrors validateIndexRenames' identical three-state
// resolution (genuine rename / post-apply no-op / stale-directive error),
// scoped to one table's own policy list — a policy has no independent
// schema, so matching is always within THIS table only, never cross-schema
// (RFC Section 7.8: PostgreSQL provides no mechanism to move a policy to a
// different table via rename).
func validatePolicyRenames(desired []*ir.Policy, desiredByName map[string]*ir.Policy, snapByName map[string]*snapshot.SnapPolicy) (map[string]string, error) {
	renamedFrom := make(map[string]string) // snap name -> desired name
	for _, pol := range desired {
		if pol.RenamedFrom == nil {
			continue
		}
		if _, collide := desiredByName[*pol.RenamedFrom]; collide {
			return nil, pipeline.Errorf(pol.Pos,
				"RENAMED FROM %q on policy %q collides with another policy of the same name in the desired declaration. Remove the stale policy.",
				*pol.RenamedFrom, pol.Name)
		}
		_, oldInSnap := snapByName[*pol.RenamedFrom]
		_, newInSnap := snapByName[pol.Name]
		if newInSnap {
			// Post-apply / no-op state: the snapshot already has the new name.
			continue
		}
		if !oldInSnap {
			return nil, pipeline.ErrorfCode(pol.Pos, "DPG-E021",
				"RENAMED FROM %q on policy %q does not match the snapshot — neither the old nor the new name exists there. Remove RENAMED FROM if this is a genuinely new policy.",
				*pol.RenamedFrom, pol.Name)
		}
		renamedFrom[*pol.RenamedFrom] = pol.Name
	}
	return renamedFrom, nil
}

func diffPolicies(schema, table string, o *ir.Table, snap *snapshot.SnapTable) ([]pipeline.DiffOp, error) {
	var ops []pipeline.DiffOp
	tblIdent := qualIdent(schema, table)

	snapByName := make(map[string]*snapshot.SnapPolicy, len(snap.Policies))
	for i := range snap.Policies {
		snapByName[snap.Policies[i].Name] = &snap.Policies[i]
	}
	desiredByName := make(map[string]*ir.Policy, len(o.Policies))
	for _, p := range o.Policies {
		desiredByName[p.Name] = p
	}

	renamedFrom, err := validatePolicyRenames(o.Policies, desiredByName, snapByName)
	if err != nil {
		return nil, err
	}

	for _, sp := range snap.Policies {
		if _, ok := desiredByName[sp.Name]; ok {
			continue
		}
		if _, wasRenamed := renamedFrom[sp.Name]; wasRenamed {
			continue
		}
		// Dropping a RESTRICTIVE policy widens what roles can see or change,
		// so it is CAUTION (RFC Section 17.2). Dropping a PERMISSIVE policy
		// only narrows access, so it is SAFE.
		dropSQL := fmt.Sprintf("DROP POLICY IF EXISTS %s ON %s;", quoteIdent(sp.Name), tblIdent)
		if sp.Permissive {
			ops = append(ops, safeOp(dropSQL, pipeline.SourcePos{}))
		} else {
			ops = append(ops, cautionOp(dropSQL, pipeline.SourcePos{}))
		}
	}
	for _, pol := range o.Policies {
		existing, exists := snapByName[pol.Name]
		if !exists && pol.RenamedFrom != nil {
			if oldSp, ok := snapByName[*pol.RenamedFrom]; ok {
				existing, exists = oldSp, true
				// RENAMED FROM on a policy (Section 7.8): SAFE, metadata-only
				// — matches Constraint/Index's identical sub-object rename
				// classification, not the CAUTION used for an independently-
				// referenceable top-level object's RENAME TO. Emitted before
				// any other change below, which then compares against
				// existing's OLD definition but always names the policy by
				// its new pol.Name, consistent with the rename having
				// already applied.
				ops = append(ops, safeOp(
					fmt.Sprintf("ALTER POLICY %s ON %s RENAME TO %s;", quoteIdent(*pol.RenamedFrom), tblIdent, quoteIdent(pol.Name)),
					pol.Pos,
				))
			}
		}
		if !exists {
			ops = append(ops, createPolicy(schema, table, pol)...)
		} else if pol.Command != existing.Command || pol.Permissive != existing.Permissive {
			// Command (FOR ...) and PERMISSIVE/RESTRICTIVE are fixed at
			// creation — real PostgreSQL's ALTER POLICY has no clause for
			// either, so a change here still requires drop + recreate.
			ops = append(ops, cautionOp(
				fmt.Sprintf("DROP POLICY IF EXISTS %s ON %s;", quoteIdent(pol.Name), tblIdent),
				pol.Pos,
			))
			ops = append(ops, createPolicy(schema, table, pol)...)
		} else if usingRemoved, withCheckRemoved := existing.Using != "" && pol.Using == nil, existing.WithCheck != "" && pol.WithCheck == nil; usingRemoved || withCheckRemoved {
			// ALTER POLICY has no way to clear an existing USING/WITH CHECK
			// clause back to unset — it can only replace one expression
			// with another. Going from set to unset genuinely needs
			// drop + recreate, same as the Command/Permissive branch above.
			ops = append(ops, cautionOp(
				fmt.Sprintf("DROP POLICY IF EXISTS %s ON %s;", quoteIdent(pol.Name), tblIdent),
				pol.Pos,
			))
			ops = append(ops, createPolicy(schema, table, pol)...)
		} else if rolesChanged, usingChanged, withCheckChanged :=
			policyRoleKey(pol.Roles) != policyRoleKey(existing.Roles),
			normalizeExprForCompare(ptrStr(pol.Using)) != normalizeExprForCompare(existing.Using),
			normalizeExprForCompare(ptrStr(pol.WithCheck)) != normalizeExprForCompare(existing.WithCheck); rolesChanged || usingChanged || withCheckChanged {
			// RFC audit item #77: real PostgreSQL's ALTER POLICY ... TO ...
			// USING (...) WITH CHECK (...) (confirmed via pg_query.Parse)
			// covers all three in one atomic, SAFE statement — no need for
			// drop+recreate, which previously opened a window with zero
			// active policy for the command in between.
			var b strings.Builder
			fmt.Fprintf(&b, "ALTER POLICY %s ON %s", quoteIdent(pol.Name), tblIdent)
			if rolesChanged {
				b.WriteString(" TO ")
				if len(pol.Roles) > 0 {
					b.WriteString(roleList(pol.Roles))
				} else {
					b.WriteString("PUBLIC")
				}
			}
			if usingChanged {
				fmt.Fprintf(&b, " USING (%s)", ptrStr(pol.Using))
			}
			if withCheckChanged {
				fmt.Fprintf(&b, " WITH CHECK (%s)", ptrStr(pol.WithCheck))
			}
			b.WriteString(";")
			ops = append(ops, safeOp(b.String(), pol.Pos))
			if ptrStr(pol.Comment) != existing.Comment {
				if pol.Comment != nil {
					ops = append(ops, safeOp(
						fmt.Sprintf("COMMENT ON POLICY %s ON %s IS %s;", quoteIdent(pol.Name), tblIdent, quoteLit(*pol.Comment)),
						pol.Pos,
					))
				} else {
					ops = append(ops, safeOp(
						fmt.Sprintf("COMMENT ON POLICY %s ON %s IS NULL;", quoteIdent(pol.Name), tblIdent),
						pol.Pos,
					))
				}
			}
		} else if ptrStr(pol.Comment) != existing.Comment {
			if pol.Comment != nil {
				ops = append(ops, safeOp(
					fmt.Sprintf("COMMENT ON POLICY %s ON %s IS %s;", quoteIdent(pol.Name), tblIdent, quoteLit(*pol.Comment)),
					pol.Pos,
				))
			} else {
				ops = append(ops, safeOp(
					fmt.Sprintf("COMMENT ON POLICY %s ON %s IS NULL;", quoteIdent(pol.Name), tblIdent),
					pol.Pos,
				))
			}
		}
	}
	return ops, nil
}

// validateTriggerRenames mirrors validatePolicyRenames — see its doc comment
// for the general shape shared by every sub-object RENAMED FROM.
func validateTriggerRenames(desired []*ir.Trigger, desiredByName map[string]*ir.Trigger, snapByName map[string]*snapshot.SnapTrigger) (map[string]string, error) {
	renamedFrom := make(map[string]string) // snap name -> desired name
	for _, trg := range desired {
		if trg.RenamedFrom == nil {
			continue
		}
		if _, collide := desiredByName[*trg.RenamedFrom]; collide {
			return nil, pipeline.Errorf(trg.Pos,
				"RENAMED FROM %q on trigger %q collides with another trigger of the same name in the desired declaration. Remove the stale trigger.",
				*trg.RenamedFrom, trg.Name)
		}
		_, oldInSnap := snapByName[*trg.RenamedFrom]
		_, newInSnap := snapByName[trg.Name]
		if newInSnap {
			// Post-apply / no-op state: the snapshot already has the new name.
			continue
		}
		if !oldInSnap {
			return nil, pipeline.ErrorfCode(trg.Pos, "DPG-E021",
				"RENAMED FROM %q on trigger %q does not match the snapshot — neither the old nor the new name exists there. Remove RENAMED FROM if this is a genuinely new trigger.",
				*trg.RenamedFrom, trg.Name)
		}
		renamedFrom[*trg.RenamedFrom] = trg.Name
	}
	return renamedFrom, nil
}

func diffTriggers(schema, table string, o *ir.Table, snap *snapshot.SnapTable) ([]pipeline.DiffOp, error) {
	var ops []pipeline.DiffOp
	tblIdent := qualIdent(schema, table)

	snapByName := make(map[string]*snapshot.SnapTrigger, len(snap.Triggers))
	for i := range snap.Triggers {
		snapByName[snap.Triggers[i].Name] = &snap.Triggers[i]
	}
	desiredByName := make(map[string]*ir.Trigger, len(o.Triggers))
	for _, t := range o.Triggers {
		desiredByName[t.Name] = t
	}

	renamedFrom, err := validateTriggerRenames(o.Triggers, desiredByName, snapByName)
	if err != nil {
		return nil, err
	}

	for _, st := range snap.Triggers {
		if _, ok := desiredByName[st.Name]; ok {
			continue
		}
		if _, wasRenamed := renamedFrom[st.Name]; wasRenamed {
			continue
		}
		// A dropped trigger silently stops enforcing whatever business
		// logic it implemented — behavior-changing (RFC audit item #26).
		ops = append(ops, cautionOp(
			fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON %s;", quoteIdent(st.Name), tblIdent),
			pipeline.SourcePos{},
		))
	}
	for _, trg := range o.Triggers {
		existing, exists := snapByName[trg.Name]
		var renaming bool
		if !exists && trg.RenamedFrom != nil {
			if oldSt, ok := snapByName[*trg.RenamedFrom]; ok {
				existing, exists = oldSt, true
				renaming = true
			}
		}
		if !exists {
			ops = append(ops, createTrigger(schema, table, trg)...)
		} else if trg.When != existing.When ||
			strings.Join(trg.Events, ", ") != existing.Events ||
			trg.ForEach != existing.ForEach ||
			strings.Join(trg.UpdateOfColumns, ", ") != existing.UpdateOfColumns ||
			ptrStr(trg.OldTransitionName) != existing.OldTransitionName ||
			ptrStr(trg.NewTransitionName) != existing.NewTransitionName ||
			qualifyFuncForCompare(trg.Function) != qualifyFuncForCompare(existing.Function) ||
			trg.Constraint != existing.Constraint ||
			qualifyRelForCompare(trg.ConstraintFrom, schema) != qualifyRelForCompare(existing.ConstraintFrom, schema) ||
			trg.Deferrable != existing.Deferrable ||
			trg.InitiallyDeferred != existing.InitiallyDeferred ||
			foldTriggerPseudoRelNames(normalizeExprForCompare(ptrStr(trg.Condition))) != foldTriggerPseudoRelNames(normalizeExprForCompare(existing.Condition)) {
			// RENAMED FROM (Section 7.9): when another property change
			// already forces a drop+recreate, the recreated trigger is
			// created directly under its final (new) name, so the DROP
			// must target the trigger's CURRENT (old) name in the
			// catalog — no separate RENAME TO is emitted in this case.
			dropName := trg.Name
			if renaming {
				dropName = existing.Name
			}
			ops = append(ops, cautionOp(
				fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON %s;", quoteIdent(dropName), tblIdent),
				trg.Pos,
			))
			ops = append(ops, createTrigger(schema, table, trg)...)
		} else {
			if renaming {
				// RENAMED FROM on a trigger (Section 7.9): SAFE,
				// metadata-only — matches Constraint/Index/Policy's
				// identical sub-object rename classification. Emitted
				// before any other change below so that the
				// DEPENDS ON EXTENSION ops that follow can reference the
				// trigger by its new name, per real PostgreSQL's
				// ALTER TRIGGER not allowing RENAME TO and
				// DEPENDS ON EXTENSION in the same statement.
				ops = append(ops, safeOp(
					fmt.Sprintf("ALTER TRIGGER %s ON %s RENAME TO %s;", quoteIdent(existing.Name), tblIdent, quoteIdent(trg.Name)),
					trg.Pos,
				))
			}
			if ptrStr(trg.Comment) != existing.Comment {
				if trg.Comment != nil {
					ops = append(ops, safeOp(
						fmt.Sprintf("COMMENT ON TRIGGER %s ON %s IS %s;", quoteIdent(trg.Name), tblIdent, quoteLit(*trg.Comment)),
						trg.Pos,
					))
				} else {
					ops = append(ops, safeOp(
						fmt.Sprintf("COMMENT ON TRIGGER %s ON %s IS NULL;", quoteIdent(trg.Name), tblIdent),
						trg.Pos,
					))
				}
			}
			// trigger-enable-state (Section 7.9, audit item #56) — a
			// targeted, SAFE ALTER TABLE ... ENABLE/DISABLE TRIGGER,
			// deliberately excluded from the recreate condition above:
			// real PostgreSQL supports changing this in place, no need
			// for a drop+recreate the way When/Events/Function etc. do.
			if trg.EnableState != existing.EnableState {
				ops = append(ops, safeOp(triggerEnableStateSQL(tblIdent, trg.Name, trg.EnableState), trg.Pos))
			}
			// [NO] DEPENDS ON EXTENSION (Section 9.1, reused verbatim for
			// triggers — Section 7.9, audit item #75): real PostgreSQL's
			// ALTER TRIGGER ... [NO] DEPENDS ON EXTENSION grammar is
			// identical to the function form, just against
			// "name ON table" instead of "name(args)".
			added, removed := stringSetDiff(trg.DependsOnExtensions, existing.DependsOnExtensions)
			for _, ext := range added {
				ops = append(ops, safeOp(
					fmt.Sprintf("ALTER TRIGGER %s ON %s DEPENDS ON EXTENSION %s;", quoteIdent(trg.Name), tblIdent, quoteIdent(ext)),
					trg.Pos,
				))
			}
			for _, ext := range removed {
				ops = append(ops, safeOp(
					fmt.Sprintf("ALTER TRIGGER %s ON %s NO DEPENDS ON EXTENSION %s;", quoteIdent(trg.Name), tblIdent, quoteIdent(ext)),
					trg.Pos,
				))
			}
		}
	}
	return ops, nil
}

// qualifyFuncForCompare normalizes a trigger's EXECUTE FUNCTION reference for
// comparison purposes only (never for rendering — createTrigger still emits
// whatever the user wrote). Introspection always returns a fully schema-
// qualified name (funcSchema + "." + funcName, from a live pg_proc join),
// while hand-written source commonly references a function unqualified,
// relying on the default "public" schema — the same convention DPG's own
// objects get via applySchemaContext. Comparing raw strings treated every
// unqualified trigger function as changed on every verify/plan --live.
// triggerEnableStateVerb resolves Section 7.9/14.1's shared trigger-enable-
// state value set ("" (ENABLED, PostgreSQL's own default), "DISABLED",
// "ENABLE REPLICA", or "ENABLE ALWAYS" — ir.Trigger.EnableState/
// ir.EventTrigger.EnableState's identical value set) to the verb an ALTER
// statement actually uses ("ENABLE"/"DISABLE"/"ENABLE REPLICA"/
// "ENABLE ALWAYS").
func triggerEnableStateVerb(state string) string {
	switch state {
	case "DISABLED":
		return "DISABLE"
	case "ENABLE REPLICA", "ENABLE ALWAYS":
		return state
	default:
		return "ENABLE"
	}
}

// triggerEnableStateSQL renders Section 7.9's trigger-enable-state as a
// real PostgreSQL ALTER TABLE ... {ENABLE|DISABLE}[ REPLICA|ALWAYS] TRIGGER
// statement.
func triggerEnableStateSQL(tblIdent, triggerName, state string) string {
	return fmt.Sprintf("ALTER TABLE %s %s TRIGGER %s;", tblIdent, triggerEnableStateVerb(state), quoteIdent(triggerName))
}

// eventTriggerEnableStateSQL is triggerEnableStateSQL's Section 14.1
// counterpart: same value set and ALTER verb, but EVENT TRIGGER's own
// grammar has no table name and no trailing "TRIGGER name" clause — the
// event trigger name comes right after the ALTER EVENT TRIGGER keywords.
func eventTriggerEnableStateSQL(name, state string) string {
	return fmt.Sprintf("ALTER EVENT TRIGGER %s %s;", quoteIdent(name), triggerEnableStateVerb(state))
}

func qualifyFuncForCompare(f string) string {
	if strings.Contains(f, ".") {
		return f
	}
	return "public." + f
}

var _ pipeline.Differ = (*Differ)(nil)
