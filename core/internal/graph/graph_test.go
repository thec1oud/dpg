package graph_test

import (
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/graph"
	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/pipeline"
)

var pos = pipeline.SourcePos{File: "test.dpg", Line: 1, Col: 1}

func schema(name string) *ir.Schema {
	return &ir.Schema{Name: name, SrcPos: pos}
}

func table(schema, name string, constraints ...*ir.Constraint) *ir.Table {
	return &ir.Table{Schema: schema, Name: name, SrcPos: pos, Constraints: constraints}
}

func fk(expr string, deferrable bool) *ir.Constraint {
	return &ir.Constraint{Type: "FOREIGN KEY", Expr: expr, Deferrable: deferrable, Pos: pos}
}

func enumType(schema, name string) *ir.Type {
	return &ir.Type{Schema: schema, Name: name, Variant: "ENUM", SrcPos: pos}
}

func columnWithType(colName, schema, typeName string) *ir.Column {
	return &ir.Column{
		Name:   colName,
		Type:   ir.TypeRef{Schema: schema, Name: typeName},
		SrcPos: pos,
	}
}

func sortObjects(t *testing.T, objects []pipeline.IRObject) []pipeline.IRObject {
	t.Helper()
	r := graph.New()
	sorted, err := r.Sort(objects)
	if err != nil {
		t.Fatalf("Sort: %v", err)
	}
	return sorted
}

func indexOf(sorted []pipeline.IRObject, qualName string) int {
	for i, o := range sorted {
		if o.QualifiedName() == qualName {
			return i
		}
	}
	return -1
}

func assertBefore(t *testing.T, sorted []pipeline.IRObject, before, after string) {
	t.Helper()
	bi := indexOf(sorted, before)
	ai := indexOf(sorted, after)
	if bi < 0 {
		t.Errorf("%q not found in sorted output", before)
		return
	}
	if ai < 0 {
		t.Errorf("%q not found in sorted output", after)
		return
	}
	if bi >= ai {
		t.Errorf("expected %q (pos %d) before %q (pos %d)", before, bi, after, ai)
	}
}

// ── Basic ordering ────────────────────────────────────────────────────────────

func TestSort_EmptyInput(t *testing.T) {
	r := graph.New()
	out, err := r.Sort(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty output, got %d", len(out))
	}
}

func TestSort_SchemaBeforeTable(t *testing.T) {
	objects := []pipeline.IRObject{
		table("iam", "users"),
		schema("iam"),
	}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "iam", "iam.users")
}

func TestSort_TypeBeforeTable(t *testing.T) {
	tbl := table("app", "orders")
	tbl.Columns = []*ir.Column{columnWithType("status", "app", "order_status")}
	objects := []pipeline.IRObject{
		tbl,
		schema("app"),
		enumType("app", "order_status"),
	}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "app.order_status", "app.orders")
}

// An introspected column type is format_type()'s text, "cron.state" with
// no separate schema, for a type outside the search path.
func TestDependencies_IntrospectedQualifiedColumnType(t *testing.T) {
	tbl := table("cron", "job")
	tbl.Columns = []*ir.Column{columnWithType("s", "", "cron.state")}
	objects := []pipeline.IRObject{tbl, schema("cron"), enumType("cron", "state")}
	if got := strings.Join(graph.Dependencies(objects)["cron.job"], ","); got != "cron,cron.state" {
		t.Errorf("dependencies of cron.job: %q", got)
	}
	if tbl.Columns[0].Type.Name != "cron.state" || tbl.Columns[0].Type.Schema != "" {
		t.Errorf("the column's type was modified: %#v", tbl.Columns[0].Type)
	}
	assertBefore(t, sortObjects(t, objects), "cron.state", "cron.job")
}

// TestSort_FunctionBeforeTableTrigger guards a real bug found live-testing a
// demo project: a table-level trigger's EXECUTE FUNCTION target (Trigger.Function,
// already a structured field) was never turned into a graph edge — the earlier
// working case only happened to order correctly by coincidental alphabetical
// file-discovery order (a function file sorting before the table's own file),
// not real dependency tracking, which is exactly what let the twin
// EventTrigger bug (below) go unnoticed until file order happened to go the
// other way.
func TestSort_FunctionBeforeTableTrigger(t *testing.T) {
	tbl := table("app", "orders")
	tbl.Triggers = []*ir.Trigger{
		{Name: "touch", When: "BEFORE", Events: []string{"UPDATE"}, ForEach: "ROW", Function: "touch_updated_at", Pos: pos},
	}
	fn := &ir.Function{Schema: "app", Name: "touch_updated_at", SrcPos: pos}
	objects := []pipeline.IRObject{
		schema("app"),
		tbl,
		fn,
	}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "app.touch_updated_at()", "app.orders")
}

// TestSort_FunctionBeforeEventTrigger guards the same bug shape as
// TestSort_FunctionBeforeTableTrigger for EVENT TRIGGER specifically — the
// scenario actually found live-testing: an event trigger created before its
// EXECUTE FUNCTION target failed at apply time ("function ... does not
// exist") because event_triggers.dpg happened to sort alphabetically before
// functions.dpg. Event triggers aren't schema-scoped, so an unqualified
// function reference falls back to "public".
func TestSort_FunctionBeforeEventTrigger(t *testing.T) {
	evt := &ir.EventTrigger{Name: "log_ddl", Function: "log_ddl_command", SrcPos: pos}
	fn := &ir.Function{Schema: "public", Name: "log_ddl_command", SrcPos: pos}
	objects := []pipeline.IRObject{
		evt,
		fn,
	}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "public.log_ddl_command()", "log_ddl")
}

// TestSort_FunctionBeforeOperatorClass guards the fifth instance of the same
// bug shape (Trigger, EventTrigger, Cast, Operator, now OperatorClass) — the
// trickiest to pin down: an initial test WITH an explicit FAMILY clause
// appeared to pass even with adversarially reversed file names, but only
// because the class→family edge happens to delay the class's readiness in
// Kahn's algorithm's queue just long enough for the (genuinely edge-less)
// function to slot in first — an even more fragile coincidence than plain
// alphabetical luck, not a real fix. Removing FAMILY (relying on
// PostgreSQL's own same-name auto-creation, so no family edge exists at
// all) exposed the real bug cleanly. Unlike Cast/Operator's single
// function, a class can declare several FUNCTION support-slots, so this
// checks both a real edge is added for the referenced one.
func TestSort_FunctionBeforeOperatorClass(t *testing.T) {
	class := &ir.OperatorClass{
		Schema: "public", Name: "op_repro_class", AccessMethod: "btree",
		Functions: []string{"op_repro_cmp"}, SrcPos: pos,
	}
	fn := &ir.Function{
		Schema: "public", Name: "op_repro_cmp",
		Args:   []ir.FuncArg{{Type: ir.TypeRef{Name: "text"}}, {Type: ir.TypeRef{Name: "text"}}},
		SrcPos: pos,
	}
	objects := []pipeline.IRObject{class, fn}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "public.op_repro_cmp(text, text)", "public.op_repro_class USING btree")
}

// TestSort_FunctionBeforeOperator guards the fourth instance of the same bug
// shape (Trigger, EventTrigger, Cast, now Operator): found by proactively
// checking, since the same "opaque object references a function" pattern
// had already broken three times — an operator's PROCEDURE reference is not
// tracked by the dependency graph either. Reproduced with adversarially
// reversed file names (an "a_operators.dpg" sorting before "z_functions.dpg")
// to prove the demo project's own passing case was just alphabetical luck,
// not a real fix, exactly like the other three. Matched by prefix like Cast,
// not the exact zero-arg key Trigger/EventTrigger use, since an operator's
// function always takes real arguments (its operand types).
func TestSort_FunctionBeforeOperator(t *testing.T) {
	leftType, rightType := ir.TypeRef{Name: "bigint"}, ir.TypeRef{Name: "bigint"}
	op := &ir.Operator{
		Schema: "public", Name: "##%",
		LeftType: &leftType, RightType: &rightType,
		Function: "op_repro_fn", SrcPos: pos,
	}
	fn := &ir.Function{
		Schema: "public", Name: "op_repro_fn",
		Args:   []ir.FuncArg{{Type: ir.TypeRef{Name: "bigint"}}, {Type: ir.TypeRef{Name: "bigint"}}},
		SrcPos: pos,
	}
	objects := []pipeline.IRObject{op, fn}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "public.op_repro_fn(bigint, bigint)", "public.##%(bigint, bigint)")
}

// TestSort_FunctionBeforeCast guards the same bug shape as
// TestSort_FunctionBeforeEventTrigger/TestSort_FunctionBeforeTableTrigger for
// CAST's WITH FUNCTION target — found live-testing a demo project:
// CREATE CAST ... WITH FUNCTION order_status_to_int(order_status) failed at
// apply time with "function ... does not exist" because casts.dpg happened
// to sort alphabetically before functions.dpg. Unlike a trigger's
// always-zero-argument function, a cast's function takes the source type as
// a real argument, so the edge is matched by "schema.name(" prefix against
// every Function object rather than an exact zero-arg key.
func TestSort_FunctionBeforeCast(t *testing.T) {
	cast := &ir.Cast{
		SourceType: ir.TypeRef{Name: "order_status"}, TargetType: ir.TypeRef{Name: "integer"},
		Function: "order_status_to_int", SrcPos: pos,
	}
	fn := &ir.Function{
		Schema: "public", Name: "order_status_to_int",
		Args:   []ir.FuncArg{{Type: ir.TypeRef{Name: "order_status"}}},
		SrcPos: pos,
	}
	objects := []pipeline.IRObject{cast, fn}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "public.order_status_to_int(order_status)", "order_status->integer")
}

func TestSort_FKDependency(t *testing.T) {
	orders := table("app", "orders",
		fk(`FOREIGN KEY (user_id) REFERENCES app.users (id)`, false),
	)
	users := table("app", "users")
	objects := []pipeline.IRObject{
		schema("app"),
		orders,
		users,
	}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "app.users", "app.orders")
}

func TestSort_UnqualifiedFKSameSchema(t *testing.T) {
	orders := table("app", "orders",
		fk(`FOREIGN KEY (user_id) REFERENCES users (id)`, false),
	)
	users := table("app", "users")
	objects := []pipeline.IRObject{schema("app"), orders, users}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "app.users", "app.orders")
}

// ── Circular FK via DEFERRABLE ─────────────────────────────────────────────────

func TestSort_CircularDeferrableFKResolved(t *testing.T) {
	a := table("pub", "a", fk(`FOREIGN KEY (b_id) REFERENCES pub.b (id)`, true))
	b := table("pub", "b", fk(`FOREIGN KEY (a_id) REFERENCES pub.a (id)`, true))
	objects := []pipeline.IRObject{schema("pub"), a, b}

	r := graph.New()
	sorted, err := r.Sort(objects)
	if err != nil {
		t.Fatalf("Sort returned error for resolvable circular FKs: %v", err)
	}
	if len(sorted) != 3 {
		t.Errorf("expected 3 objects, got %d", len(sorted))
	}
}

func TestSort_CircularNonDeferrableFKErrors(t *testing.T) {
	a := table("pub", "a", fk(`FOREIGN KEY (b_id) REFERENCES pub.b (id)`, false))
	b := table("pub", "b", fk(`FOREIGN KEY (a_id) REFERENCES pub.a (id)`, false))
	objects := []pipeline.IRObject{schema("pub"), a, b}

	r := graph.New()
	_, err := r.Sort(objects)
	if err == nil {
		t.Fatal("expected error for non-deferrable circular FK")
	}
}

// TestSort_ThreeNodeCycleWithNonDeferrableMiddleFKErrors is the regression
// guard for RFC audit item #23: findCycle used to record only the two
// endpoints of the DFS back-edge that closed a cycle, not every node on the
// cycle. For a 3+-node cycle, the middle member(s) never made it into the
// returned cycle slice at all, so canDefer's contract ("resolvable only when
// EVERY cycle-closing FK is DEFERRABLE") silently checked a strict subset of
// the real cycle-closing FKs.
//
// Here A→C (deferrable), B→A (NOT deferrable), C→B (deferrable) form a
// single 3-node cycle with pub.a first in the object list, so the DFS
// deterministically enters at A, walks A→C→B, and finds the back edge B→A —
// the buggy findCycle returned only [a, b], never mentioning c. canDefer
// then only examined a's FK (→c, deferrable) and never saw b's FK (→a, NOT
// deferrable) because c wasn't in its cycle set — wrongly approving the
// cycle as resolvable. With the fix, findCycle reconstructs the full [a, c,
// b] path via parent pointers, canDefer sees b's non-deferrable FK, and Sort
// correctly refuses instead of silently leaving an undeferred, cycle-closing
// FK to depend on removal of an unrelated edge.
func TestSort_ThreeNodeCycleWithNonDeferrableMiddleFKErrors(t *testing.T) {
	a := table("pub", "a", fk(`FOREIGN KEY (c_id) REFERENCES pub.c (id)`, true))
	b := table("pub", "b", fk(`FOREIGN KEY (a_id) REFERENCES pub.a (id)`, false))
	c := table("pub", "c", fk(`FOREIGN KEY (b_id) REFERENCES pub.b (id)`, true))
	objects := []pipeline.IRObject{a, b, c}

	r := graph.New()
	_, err := r.Sort(objects)
	if err == nil {
		t.Fatal("expected error: cycle contains a non-deferrable closing FK (pub.b → pub.a) that a truncated cycle would miss")
	}
}

// TestSort_RangeTypeAutoConstructorNoCycle is the regression guard for RFC
// audit item C.1: dumping a real RANGE type crashed dpg dump/plan/apply with
// a goroutine stack overflow. PostgreSQL auto-generates an eponymous
// constructor function for a RANGE type (LANGUAGE internal, e.g.
// `range_constructor2`, ReturnType == the range type itself), which
// introspection captures as an ordinary managed ir.Function. Before this fix,
// that function got a Function→Type edge (only BASE types were exempted from
// the usual "function depends on its return type" edge) while the range
// type's own Body text — which necessarily contains the type's own name —
// matched bodyCallsFuncEdge's whole-word scan and added a Type→Function edge
// back, a genuine 2-node cycle with zero Tables in it. canDefer used to
// default to true for a cycle with no FK-bearing Table at all, so Sort's
// constraint-stripping recovery removed nothing and recursed on an unchanged
// object set forever. This reproduces that exact shape end-to-end through
// the real Sort path (not a canDefer unit test) — before the fix this
// crashed the process; it must now return without error.
func TestSort_RangeTypeAutoConstructorNoCycle(t *testing.T) {
	rangeType := &ir.Type{
		Schema: "app", Name: "price_range", Variant: "RANGE",
		Body:   "CREATE TYPE app.price_range AS RANGE (SUBTYPE = numeric)",
		SrcPos: pos,
	}
	ctor := &ir.Function{
		Schema: "app", Name: "price_range",
		Args:       []ir.FuncArg{{Type: ir.TypeRef{Name: "numeric"}}, {Type: ir.TypeRef{Name: "numeric"}}},
		ReturnType: ir.TypeRef{Schema: "app", Name: "price_range"},
		Attrs:      ir.FuncAttrs{Language: "internal"},
		SrcPos:     pos,
	}
	objects := []pipeline.IRObject{schema("app"), rangeType, ctor}

	r := graph.New()
	sorted, err := r.Sort(objects)
	if err != nil {
		t.Fatalf("Sort returned error for RANGE type + auto-generated constructor: %v", err)
	}
	if len(sorted) != 3 {
		t.Errorf("expected 3 objects, got %d", len(sorted))
	}
}

// ── Unknown FK / type targets in managed schemas ───────────────────────────────

func TestSort_UnresolvedFKInManagedSchemaErrors(t *testing.T) {
	// "app" schema is in source, but "app.nonexistent" is not.
	orders := table("app", "orders",
		fk(`FOREIGN KEY (x_id) REFERENCES app.nonexistent (id)`, false),
	)
	objects := []pipeline.IRObject{schema("app"), orders}

	r := graph.New()
	_, err := r.Sort(objects)
	if err == nil {
		t.Fatal("expected error for unresolved FK target in managed schema")
	}
}

func TestSort_UnresolvedTypeInManagedSchemaErrors(t *testing.T) {
	tbl := table("app", "things")
	tbl.Columns = []*ir.Column{columnWithType("kind", "app", "ghost_type")}
	objects := []pipeline.IRObject{schema("app"), tbl}

	r := graph.New()
	_, err := r.Sort(objects)
	if err == nil {
		t.Fatal("expected error for unresolved type in managed schema")
	}
}

// ── VIRTUAL TYPE case-mismatch detection ────────────────────────────────────
// Regression guard: a column or composite attribute referencing a VIRTUAL
// TYPE with the wrong case used to silently render as a bogus literal type
// name, only failing much later as a confusing raw PostgreSQL error at
// apply time. VIRTUAL TYPE is DPG-native with zero catalog footprint, so
// unlike an ordinary custom type reference, its full name universe is known
// offline — a case-insensitive near-miss is unambiguous and now errors.

func vtype(schema, name string) *ir.VirtualType {
	return &ir.VirtualType{Schema: schema, Name: name, Body: ir.VtypeTypeRef{Name: "jsonb"}}
}

func TestSort_VirtualTypeCaseMismatchInColumnErrors(t *testing.T) {
	tbl := table("app", "events")
	tbl.Columns = []*ir.Column{columnWithType("payload", "", "MyJson")}
	objects := []pipeline.IRObject{schema("app"), tbl, vtype("app", "myjson")}

	r := graph.New()
	_, err := r.Sort(objects)
	if err == nil {
		t.Fatal("expected error for VIRTUAL TYPE case mismatch")
	}
	if !strings.Contains(err.Error(), "myjson") || !strings.Contains(err.Error(), "case-sensitive") {
		t.Errorf("expected error to cite the correct casing, got: %v", err)
	}
}

func TestSort_VirtualTypeQualifiedCaseMismatchErrors(t *testing.T) {
	tbl := table("app", "events")
	tbl.Columns = []*ir.Column{columnWithType("payload", "app", "MyJson")}
	objects := []pipeline.IRObject{schema("app"), tbl, vtype("app", "myjson")}

	r := graph.New()
	_, err := r.Sort(objects)
	if err == nil {
		t.Fatal("expected error for qualified VIRTUAL TYPE case mismatch")
	}
}

func TestSort_VirtualTypeExactCaseNoError(t *testing.T) {
	tbl := table("app", "events")
	tbl.Columns = []*ir.Column{columnWithType("payload", "", "myjson")}
	objects := []pipeline.IRObject{schema("app"), tbl, vtype("app", "myjson")}

	sorted := sortObjects(t, objects)
	if len(sorted) != 3 {
		t.Errorf("expected 3 objects, got %d", len(sorted))
	}
}

func TestSort_OrdinaryBuiltinTypeNoVirtualTypeFalsePositive(t *testing.T) {
	tbl := table("app", "events")
	tbl.Columns = []*ir.Column{columnWithType("created_at", "", "TIMESTAMP")}
	// A declared virtual type exists but shares no name with the column's
	// type at all — must never be flagged.
	objects := []pipeline.IRObject{schema("app"), tbl, vtype("app", "myjson")}

	sorted := sortObjects(t, objects)
	if len(sorted) != 3 {
		t.Errorf("expected 3 objects, got %d", len(sorted))
	}
}

func TestSort_CompositeAttrVirtualTypeCaseMismatchErrors(t *testing.T) {
	comp := &ir.Type{
		Schema: "app", Name: "envelope", Variant: "COMPOSITE",
		CompositeAttrs: []*ir.Column{columnWithType("payload", "", "MyJson")},
		SrcPos:         pos,
	}
	objects := []pipeline.IRObject{schema("app"), comp, vtype("app", "myjson")}

	r := graph.New()
	_, err := r.Sort(objects)
	if err == nil {
		t.Fatal("expected error for VIRTUAL TYPE case mismatch in a composite attribute")
	}
}

func TestSort_UnresolvedFKInExternalSchemaAllowed(t *testing.T) {
	// "extensions" schema is NOT in source, so FK to it should be silently allowed.
	orders := table("app", "orders",
		fk(`FOREIGN KEY (geom) REFERENCES extensions.geometry (id)`, false),
	)
	objects := []pipeline.IRObject{schema("app"), orders}

	sorted := sortObjects(t, objects)
	if len(sorted) != 2 {
		t.Errorf("expected 2 objects, got %d", len(sorted))
	}
}

// ── View heuristic ────────────────────────────────────────────────────────────

// TestSort_ViewAfterReferencedTables is RFC Section 22.1 item 9's (audit item #30)
// regression guard: a view's dependency edges now come from a real AST walk
// of its query (ir.ExtractTableRefs), not the old "depends on every table in
// the object set" heuristic (graph.go used to comment this explicitly:
// "Heuristic: all views depend on all tables (query AST analysis
// deferred)"). Table references here are schema-qualified because DPG never
// wraps a view's CREATE VIEW with a SET search_path (unlike the opaque
// object kinds that do) — an unqualified reference resolves via "public",
// same convention as Publication/EventTrigger/Cast elsewhere in graph.go.
func TestSort_ViewAfterReferencedTables(t *testing.T) {
	v := &ir.View{Schema: "app", Name: "user_view", Query: "SELECT u.id FROM app.users u JOIN app.orders o ON o.user_id = u.id", SrcPos: pos}
	u := table("app", "users")
	o := table("app", "orders")
	s := schema("app")
	objects := []pipeline.IRObject{v, u, o, s}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "app.users", "app.user_view")
	assertBefore(t, sorted, "app.orders", "app.user_view")
}

// TestSort_ViewNotDependentOnUnreferencedTable is the sibling proof that the
// real AST walk is actually narrower than the old heuristic, not just
// additionally correct in the positive case: a table the view's query never
// mentions must NOT gain a forced ordering relative to the view. v's query
// references "app.users", deliberately absent from this test's object set —
// refEdge only adds an edge when the target resolves in idx (see graph.go),
// so there is no possible edge at all between v and unrelated here. Relies
// on the same input-order-preservation property
// TestSort_PlpgsqlFunctionBodyNotScanned already relies on to prove an
// absent edge — mirrors that test's exact two-object shape (no schema
// object either) so Kahn's algorithm's tie-breaking isn't perturbed by
// unrelated schema edges.
func TestSort_ViewNotDependentOnUnreferencedTable(t *testing.T) {
	v := &ir.View{Schema: "app", Name: "user_view", Query: "SELECT u.id FROM app.users u", SrcPos: pos}
	unrelated := table("app", "unrelated")
	objects := []pipeline.IRObject{v, unrelated}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "app.user_view", "app.unrelated")
}

// A column whose custom type is written UNQUALIFIED (as introspected columns are
// — e.g. "mood" rather than "public.mood") must still order the type before the
// table, resolved against the table's own schema. Regression: dump emitted the
// table before its enum, so apply-to-fresh-DB failed with "type does not exist".
func TestSort_UnqualifiedTypeSameSchema(t *testing.T) {
	tbl := table("app", "orders")
	tbl.Columns = []*ir.Column{columnWithType("status", "", "order_status")}
	objects := []pipeline.IRObject{
		tbl,
		schema("app"),
		enumType("app", "order_status"),
	}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "app.order_status", "app.orders")
}

// An unqualified type name with no matching custom type is a built-in and must
// not error or create a dependency.
func TestSort_UnqualifiedBuiltinTypeNoError(t *testing.T) {
	tbl := table("app", "orders")
	tbl.Columns = []*ir.Column{columnWithType("n", "", "integer")}
	objects := []pipeline.IRObject{tbl, schema("app")}
	if _, err := graph.New().Sort(objects); err != nil {
		t.Fatalf("unqualified built-in type must not error: %v", err)
	}
}

// ── Operator class → family, TS config → parser, TS dict → template ────────────
//
// These three mirror the same shape: an opaque object that names another
// opaque object by qualified reference, needing a real dependency edge so
// CREATE order is correct regardless of declaration order — unlike a table's
// column type or FK reference, PostgreSQL's CREATE OPERATOR FAMILY/TEXT
// SEARCH PARSER/TEXT SEARCH TEMPLATE have no IF NOT EXISTS to self-heal a
// wrong order, and CREATE OPERATOR CLASS ... FAMILY x / CREATE TEXT SEARCH
// CONFIGURATION ... (PARSER = x) / CREATE TEXT SEARCH DICTIONARY ... (TEMPLATE
// = x) all hard-error if x doesn't exist yet. Each test deliberately declares
// the referencing object BEFORE its reference in the input slice, so a
// passing assertBefore proves the edge actually reorders things rather than
// coincidentally matching input order.

func opClass(schema, name, am, famSchema, famName string) *ir.OperatorClass {
	return &ir.OperatorClass{
		Schema: schema, Name: name, AccessMethod: am,
		FamilySchema: famSchema, FamilyName: famName, SrcPos: pos,
	}
}

func opFamily(schema, name, am string) *ir.OperatorFamily {
	return &ir.OperatorFamily{Schema: schema, Name: name, AccessMethod: am, SrcPos: pos}
}

func TestSort_OperatorClassBeforeFamily_QualifiedRef(t *testing.T) {
	objects := []pipeline.IRObject{
		opClass("app", "my_ops", "btree", "app", "my_fam"),
		opFamily("app", "my_fam", "btree"),
	}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "app.my_fam USING btree FAMILY", "app.my_ops USING btree")
}

// Unqualified FAMILY name (FamilySchema empty) resolves against the class's
// own schema — same convention as an unqualified column type reference.
func TestSort_OperatorClassBeforeFamily_UnqualifiedRef(t *testing.T) {
	objects := []pipeline.IRObject{
		opClass("app", "my_ops", "btree", "", "my_fam"),
		opFamily("app", "my_fam", "btree"),
	}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "app.my_fam USING btree FAMILY", "app.my_ops USING btree")
}

// A class whose FAMILY isn't part of the managed object set (e.g. a built-in
// pg_catalog family, or simply omitted in hand-written source relying on
// PostgreSQL's own auto-creation) must not error — mirrors the unqualified
// built-in type case above.
func TestSort_OperatorClassNoFamilyReferenceNoError(t *testing.T) {
	objects := []pipeline.IRObject{opClass("app", "my_ops", "btree", "", "")}
	if _, err := graph.New().Sort(objects); err != nil {
		t.Fatalf("operator class with no FAMILY reference must not error: %v", err)
	}
}

func tsConfig(schema, name, parserSchema, parserName string) *ir.TSConfig {
	return &ir.TSConfig{Schema: schema, Name: name, ParserSchema: parserSchema, ParserName: parserName, SrcPos: pos}
}

func tsParser(schema, name string) *ir.TSParser {
	return &ir.TSParser{Schema: schema, Name: name, SrcPos: pos}
}

func TestSort_TSConfigBeforeParser(t *testing.T) {
	objects := []pipeline.IRObject{
		tsConfig("app", "my_cfg", "app", "my_parser"),
		tsParser("app", "my_parser"),
	}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "app.my_parser", "app.my_cfg")
}

// The overwhelmingly common case: PARSER references a pg_catalog built-in
// (e.g. "default"), which is never part of the managed object set — must not
// error.
func TestSort_TSConfigBuiltinParserNoError(t *testing.T) {
	objects := []pipeline.IRObject{tsConfig("app", "my_cfg", "pg_catalog", "default")}
	if _, err := graph.New().Sort(objects); err != nil {
		t.Fatalf("TS config referencing a built-in parser must not error: %v", err)
	}
}

func tsDict(schema, name, tmplSchema, tmplName string) *ir.TSDict {
	return &ir.TSDict{Schema: schema, Name: name, TemplateSchema: tmplSchema, TemplateName: tmplName, SrcPos: pos}
}

func tsTemplate(schema, name string) *ir.TSTemplate {
	return &ir.TSTemplate{Schema: schema, Name: name, SrcPos: pos}
}

func TestSort_TSDictBeforeTemplate(t *testing.T) {
	objects := []pipeline.IRObject{
		tsDict("app", "my_dict", "app", "my_tmpl"),
		tsTemplate("app", "my_tmpl"),
	}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "app.my_tmpl", "app.my_dict")
}

func TestSort_TSDictBuiltinTemplateNoError(t *testing.T) {
	objects := []pipeline.IRObject{tsDict("app", "my_dict", "pg_catalog", "simple")}
	if _, err := graph.New().Sort(objects); err != nil {
		t.Fatalf("TS dict referencing a built-in template must not error: %v", err)
	}
}

// TestSort_FunctionBeforeTSTemplate guards the sixth instance of the same
// bug shape (Trigger, EventTrigger, Cast, Operator, OperatorClass, now
// TSTemplate): CREATE TEXT SEARCH TEMPLATE's INIT/LEXIZE support functions
// were not tracked by the dependency graph either — found by proactively
// checking every remaining opaque-body kind after the pattern held five
// times running. Custom TS template functions require C in practice (the
// required internal-Datum return type can't be produced from SQL/PLpgSQL),
// so this guards a real ordering hazard that's harder to hit than the other
// five, not one that's impractical to ever occur — a DPG project with a
// real C extension declaring its support functions via a normal FUNCTION
// object (LANGUAGE c) would still hit this.
func TestSort_FunctionBeforeTSTemplate(t *testing.T) {
	tmpl := &ir.TSTemplate{Schema: "public", Name: "my_tmpl", Functions: []string{"ts_init", "ts_lexize"}, SrcPos: pos}
	initFn := &ir.Function{Schema: "public", Name: "ts_init", SrcPos: pos}
	lexizeFn := &ir.Function{Schema: "public", Name: "ts_lexize", SrcPos: pos}
	objects := []pipeline.IRObject{tmpl, initFn, lexizeFn}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "public.ts_init()", "public.my_tmpl")
	assertBefore(t, sorted, "public.ts_lexize()", "public.my_tmpl")
}

// TestSort_FunctionBeforeTSParser guards the same bug shape for CREATE TEXT
// SEARCH PARSER's START/GETTOKEN/END/LEXTYPES support functions.
func TestSort_FunctionBeforeTSParser(t *testing.T) {
	prs := &ir.TSParser{Schema: "public", Name: "my_prs", Functions: []string{"ts_start", "ts_gettoken"}, SrcPos: pos}
	startFn := &ir.Function{Schema: "public", Name: "ts_start", SrcPos: pos}
	gettokenFn := &ir.Function{Schema: "public", Name: "ts_gettoken", SrcPos: pos}
	objects := []pipeline.IRObject{prs, startFn, gettokenFn}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "public.ts_start()", "public.my_prs")
	assertBefore(t, sorted, "public.ts_gettoken()", "public.my_prs")
}

// ── Dependency-graph gaps flagged 2026-08-14, closed here ──────────────────────
//
// Three edges the graph never computed: PUBLICATION ... FOR TABLE x → table x;
// a LANGUAGE sql function/procedure body calling another function/procedure;
// and a function/procedure's parameter or return TYPE referencing a custom
// type. Each test declares the referencing object BEFORE its reference in the
// input slice (same convention as the operator-class/TS-config tests above),
// so a passing assertBefore proves a real edge, not input-order luck.

func TestSort_TableBeforePublicationForTable(t *testing.T) {
	pub := &ir.Publication{Name: "orders_pub", Tables: []ir.PublicationTableRef{{Schema: "app", Name: "orders"}}, SrcPos: pos}
	objects := []pipeline.IRObject{pub, table("app", "orders"), schema("app")}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "app.orders", "orders_pub")
}

// An unqualified FOR TABLE reference falls back to "public", the same
// convention already used for EventTrigger/Cast's unqualified function refs.
func TestSort_TableBeforePublicationForTable_UnqualifiedRef(t *testing.T) {
	pub := &ir.Publication{Name: "orders_pub", Tables: []ir.PublicationTableRef{{Name: "orders"}}, SrcPos: pos}
	objects := []pipeline.IRObject{pub, table("public", "orders")}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "public.orders", "orders_pub")
}

// A publication with no fixed table target (FOR ALL TABLES, or FOR TABLES IN
// SCHEMA — neither populates Tables) must not error.
func TestSort_PublicationNoTableTargetNoError(t *testing.T) {
	objects := []pipeline.IRObject{&ir.Publication{Name: "all_pub", SrcPos: pos}}
	if _, err := graph.New().Sort(objects); err != nil {
		t.Fatalf("publication with no FOR TABLE target must not error: %v", err)
	}
}

// TestSort_SqlFunctionCallsAnotherFunction guards the second dependency-graph
// gap: real PostgreSQL validates a LANGUAGE sql body immediately at CREATE
// FUNCTION time (unlike plpgsql, compiled lazily), so a LANGUAGE sql
// function calling another not-yet-created function fails at apply time.
// TestSort_SqlFunctionBodyReferencesTable and
// TestSort_PlpgsqlFunctionBodyReferencesTable are RFC Section 22.1 item 9's (audit
// item #30) core regression guard: a function/procedure body's SQL is now
// scanned for real table references (bodies were previously opaque to the
// dependency graph entirely, for every language) — a function that INSERTs
// into a table must be ordered after that table. Both languages are covered
// separately: unlike bodyCallsFuncEdge (function-calls-function, which
// deliberately skips plpgsql — see TestSort_PlpgsqlFunctionBodyNotScanned),
// table-reference extraction applies to plpgsql too (ir.ExtractTableRefs
// reuses the same fragment-parsing machinery BodyHash already relies on).
func TestSort_SqlFunctionBodyReferencesTable(t *testing.T) {
	fn := &ir.Function{
		Schema: "app", Name: "log_event",
		Attrs:  ir.FuncAttrs{Language: "sql", Body: "INSERT INTO app.events (msg) VALUES ('x')"},
		SrcPos: pos,
	}
	tbl := table("app", "events")
	objects := []pipeline.IRObject{fn, tbl, schema("app")}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "app.events", "app.log_event()")
}

// TestSort_PlpgsqlFunctionBodyTableRefsNotScanned mirrors
// TestSort_PlpgsqlFunctionBodyNotScanned but for table references instead of
// function calls: a plpgsql body is deliberately NOT scanned for tables it
// INSERTs into either, for the identical reason (PostgreSQL compiles
// plpgsql lazily and never resolves embedded SQL against the catalog at
// CREATE FUNCTION time, so there's no matching ordering hazard to guard
// against). Forcing this edge anyway is what let RFC audit finding #121's
// self-referencing-trigger-function cycle happen — see
// TestSort_TriggerFunctionSelfReferencingNoCycle. Proven the same
// no-forced-order way: fn declared before tbl with nothing else creating a
// dependency between them, so if an edge were wrongly added, tbl would sort
// first instead.
func TestSort_PlpgsqlFunctionBodyTableRefsNotScanned(t *testing.T) {
	fn := &ir.Function{
		Schema: "app", Name: "log_event",
		Attrs:      ir.FuncAttrs{Language: "plpgsql", Body: "BEGIN INSERT INTO app.events (msg) VALUES ('x'); END;"},
		ReturnType: ir.TypeRef{Name: "void"},
		SrcPos:     pos,
	}
	tbl := table("app", "events")
	objects := []pipeline.IRObject{fn, tbl, schema("app")}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "app.log_event()", "app.events")
}

// TestSort_ProcedureBodyReferencesTable is the *ir.Procedure sibling.
func TestSort_ProcedureBodyReferencesTable(t *testing.T) {
	proc := &ir.Procedure{
		Schema: "app", Name: "log_event_proc",
		Attrs:  ir.FuncAttrs{Language: "sql", Body: "INSERT INTO app.events (msg) VALUES ('x')"},
		SrcPos: pos,
	}
	tbl := table("app", "events")
	objects := []pipeline.IRObject{proc, tbl, schema("app")}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "app.events", "app.log_event_proc()")
}

// TestSort_TriggerFunctionNotSelfReferencingNoCycle guards the exact
// regression the RFC Section 22.1 item 9 design conversation identified and
// rejected a cheaper "function depends on all tables" heuristic over:
// combined with the existing table→trigger-function edge (a trigger's
// EXECUTE FUNCTION target must exist first), a blunt function→all-tables
// heuristic would manufacture a 2-node cycle for every trigger-bearing
// table, since the heuristic always includes the trigger's own table. A
// real AST walk doesn't have this problem for the (overwhelmingly common)
// case of a trigger function that never references its own table — proven
// here with a real INSERT/UPDATE-touching-NEW-only body, no cycle, no error.
func TestSort_TriggerFunctionNotSelfReferencingNoCycle(t *testing.T) {
	tbl := table("app", "orders")
	tbl.Triggers = []*ir.Trigger{
		{Name: "touch", When: "BEFORE", Events: []string{"UPDATE"}, ForEach: "ROW", Function: "touch_updated_at", Pos: pos},
	}
	fn := &ir.Function{
		Schema: "app", Name: "touch_updated_at",
		Attrs:  ir.FuncAttrs{Language: "plpgsql", Body: "BEGIN NEW.updated_at := now(); RETURN NEW; END;"},
		SrcPos: pos,
	}
	objects := []pipeline.IRObject{schema("app"), tbl, fn}
	if _, err := graph.New().Sort(objects); err != nil {
		t.Fatalf("expected no cycle for a non-self-referencing trigger function, got: %v", err)
	}
}

// TestSort_TriggerFunctionSelfReferencingNoCycle is the counterpart RFC audit
// finding #121 flagged: an ordinary validation/audit trigger function whose
// body queries its OWN table (e.g. `SELECT count(*) FROM app.orders`) closes
// a 2-node cycle with the pre-existing table→trigger-function edge (source
// 6) plus a function→table edge from the same table (source 9) — a shape
// Section 22.2's cycle-breaker can't resolve since it only knows how to break FK
// cycles via DEFERRABLE, and there is no FK anywhere in this cycle. Must not
// error.
func TestSort_TriggerFunctionSelfReferencingNoCycle(t *testing.T) {
	tbl := table("app", "orders")
	tbl.Triggers = []*ir.Trigger{
		{Name: "check_dupe", When: "BEFORE", Events: []string{"INSERT"}, ForEach: "ROW", Function: "check_dupe_order", Pos: pos},
	}
	fn := &ir.Function{
		Schema: "app", Name: "check_dupe_order",
		Attrs:      ir.FuncAttrs{Language: "plpgsql", Body: "BEGIN PERFORM 1 FROM app.orders WHERE id = NEW.id; RETURN NEW; END;"},
		ReturnType: ir.TypeRef{Name: "trigger"},
		SrcPos:     pos,
	}
	objects := []pipeline.IRObject{schema("app"), tbl, fn}
	if _, err := graph.New().Sort(objects); err != nil {
		t.Fatalf("expected no cycle for a trigger function referencing its own table, got: %v", err)
	}
}

func TestSort_SqlFunctionCallsAnotherFunction(t *testing.T) {
	caller := &ir.Function{
		Schema: "app", Name: "total_price",
		Attrs:  ir.FuncAttrs{Language: "sql", Body: "SELECT base_price() * 2"},
		SrcPos: pos,
	}
	callee := &ir.Function{Schema: "app", Name: "base_price", SrcPos: pos}
	objects := []pipeline.IRObject{caller, callee, schema("app")}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "app.base_price()", "app.total_price()")
}

// A LANGUAGE plpgsql body is deliberately NOT scanned — plpgsql is compiled
// lazily and PostgreSQL does not resolve called-function names against the
// catalog at CREATE FUNCTION time, so there is no matching ordering hazard to
// guard against. Proven by declaring caller before callee with no schema
// dependency forcing either order: Kahn's algorithm preserves input order
// for nodes with no edge between them (both start at inDegree 0, tiebroken
// by original position), so caller staying first shows no edge was added —
// if plpgsql were wrongly scanned like sql, the added edge would force
// callee first instead, exactly like TestSort_SqlFunctionCallsAnotherFunction.
func TestSort_PlpgsqlFunctionBodyNotScanned(t *testing.T) {
	caller := &ir.Function{
		Schema: "app", Name: "total_price",
		Attrs:  ir.FuncAttrs{Language: "plpgsql", Body: "BEGIN RETURN base_price() * 2; END;"},
		SrcPos: pos,
	}
	callee := &ir.Function{Schema: "app", Name: "base_price", SrcPos: pos}
	objects := []pipeline.IRObject{caller, callee}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "app.total_price()", "app.base_price()")
}

// TestSort_SqlProcedureCallsFunction guards the same gap for a LANGUAGE sql
// PROCEDURE body.
func TestSort_SqlProcedureCallsFunction(t *testing.T) {
	proc := &ir.Procedure{
		Schema: "app", Name: "apply_discount",
		Attrs:  ir.FuncAttrs{Language: "sql", Body: "SELECT base_price()"},
		SrcPos: pos,
	}
	fn := &ir.Function{Schema: "app", Name: "base_price", SrcPos: pos}
	objects := []pipeline.IRObject{proc, fn, schema("app")}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "app.base_price()", "app.apply_discount()")
}

// TestSort_TypeBeforeFunctionParam guards the third dependency-graph gap: a
// function whose parameter type is a custom TYPE, created before that type,
// fails at apply time ("type ... does not exist").
func TestSort_TypeBeforeFunctionParam(t *testing.T) {
	fn := &ir.Function{
		Schema: "app", Name: "classify",
		Args:   []ir.FuncArg{{Name: "s", Type: ir.TypeRef{Schema: "app", Name: "order_status"}}},
		SrcPos: pos,
	}
	objects := []pipeline.IRObject{fn, enumType("app", "order_status"), schema("app")}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "app.order_status", "app.classify(app.order_status)")
}

// Same gap for a function's RETURN TYPE.
func TestSort_TypeBeforeFunctionReturnType(t *testing.T) {
	fn := &ir.Function{
		Schema: "app", Name: "current_status",
		ReturnType: ir.TypeRef{Schema: "app", Name: "order_status"},
		SrcPos:     pos,
	}
	objects := []pipeline.IRObject{fn, enumType("app", "order_status"), schema("app")}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "app.order_status", "app.current_status()")
}

// An unqualified parameter type resolves against the function's own schema —
// same convention as an unqualified table column type.
func TestSort_TypeBeforeFunctionParam_UnqualifiedRef(t *testing.T) {
	fn := &ir.Function{
		Schema: "app", Name: "classify",
		Args:   []ir.FuncArg{{Name: "s", Type: ir.TypeRef{Name: "order_status"}}},
		SrcPos: pos,
	}
	objects := []pipeline.IRObject{fn, enumType("app", "order_status"), schema("app")}
	sorted := sortObjects(t, objects)
	assertBefore(t, sorted, "app.order_status", "app.classify(order_status)")
}

// A built-in parameter/return type must not error.
func TestSort_FunctionBuiltinTypesNoError(t *testing.T) {
	fn := &ir.Function{
		Schema:     "app",
		Name:       "add",
		Args:       []ir.FuncArg{{Type: ir.TypeRef{Schema: "pg_catalog", Name: "int4"}}},
		ReturnType: ir.TypeRef{Name: "integer"},
		SrcPos:     pos,
	}
	objects := []pipeline.IRObject{fn, schema("app")}
	if _, err := graph.New().Sort(objects); err != nil {
		t.Fatalf("function with only built-in types must not error: %v", err)
	}
}

// ── Operator family loose members (RFC Section 14.4) ─────────────────────────────────

// TestSort_FunctionBeforeOpFamilyMember guards the same ordering hazard as
// TestSort_FunctionBeforeOperatorClass, one level down: a FUNCTION loose
// member's ALTER OPERATOR FAMILY ... ADD fails at apply time if it runs
// before the function exists.
func TestSort_FunctionBeforeOpFamilyMember(t *testing.T) {
	fam := &ir.OperatorFamily{
		Schema: "public", Name: "opfam_repro_fn_family", AccessMethod: "btree", SrcPos: pos,
		Members: []pipeline.OpFamilyMember{
			{IsFunction: true, Number: 1, Name: pipeline.Identifier{Name: "opfam_repro_fn"},
				LeftType: "integer", RightType: "bigint", FuncArgs: []string{"integer", "bigint"}},
		},
	}
	fn := &ir.Function{
		Schema: "public", Name: "opfam_repro_fn",
		Args:   []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}, {Type: ir.TypeRef{Name: "bigint"}}},
		SrcPos: pos,
	}
	sorted := sortObjects(t, []pipeline.IRObject{fam, fn})
	assertBefore(t, sorted, "public.opfam_repro_fn(integer, bigint)", "public.opfam_repro_fn_family USING btree FAMILY")
}

// TestSort_OperatorBeforeOpFamilyMember guards the same hazard for an
// OPERATOR loose member.
func TestSort_OperatorBeforeOpFamilyMember(t *testing.T) {
	leftType, rightType := ir.TypeRef{Name: "integer"}, ir.TypeRef{Name: "bigint"}
	fam := &ir.OperatorFamily{
		Schema: "public", Name: "opfam_repro_op_family", AccessMethod: "btree", SrcPos: pos,
		Members: []pipeline.OpFamilyMember{
			{Number: 1, Name: pipeline.Identifier{Name: "##%"}, LeftType: "integer", RightType: "bigint"},
		},
	}
	op := &ir.Operator{
		Schema: "public", Name: "##%",
		LeftType: &leftType, RightType: &rightType, SrcPos: pos,
	}
	sorted := sortObjects(t, []pipeline.IRObject{fam, op})
	assertBefore(t, sorted, "public.##%(integer, bigint)", "public.opfam_repro_op_family USING btree FAMILY")
}

// TestSort_TypeBeforeOpFamilyMember guards the op_type reference case: a
// member naming a user-defined type must be ordered after that type.
func TestSort_TypeBeforeOpFamilyMember(t *testing.T) {
	fam := &ir.OperatorFamily{
		Schema: "public", Name: "opfam_repro_type_family", AccessMethod: "btree", SrcPos: pos,
		Members: []pipeline.OpFamilyMember{
			{Number: 1, Name: pipeline.Identifier{Name: "="}, LeftType: "public.opfam_repro_type", RightType: "public.opfam_repro_type"},
		},
	}
	typ := enumType("public", "opfam_repro_type")
	sorted := sortObjects(t, []pipeline.IRObject{fam, typ})
	assertBefore(t, sorted, "public.opfam_repro_type", "public.opfam_repro_type_family USING btree FAMILY")
}

// TestSort_OpFamilyMemberUnresolvedReferenceNoError guards the same
// no-error-on-miss behavior refEdge/typeRefEdge/funcPrefixEdge already give
// OperatorClass — a pg_catalog built-in referenced by a loose member (the
// overwhelmingly common case) legitimately isn't part of the managed
// object set.
func TestSort_OpFamilyMemberUnresolvedReferenceNoError(t *testing.T) {
	fam := &ir.OperatorFamily{
		Schema: "public", Name: "opfam_builtin_only", AccessMethod: "btree", SrcPos: pos,
		Members: []pipeline.OpFamilyMember{
			{Number: 1, Name: pipeline.Identifier{Schema: "pg_catalog", Name: "<"}, LeftType: "integer", RightType: "bigint"},
			{IsFunction: true, Number: 1, Name: pipeline.Identifier{Schema: "pg_catalog", Name: "btint48cmp"},
				LeftType: "integer", RightType: "bigint", FuncArgs: []string{"integer", "bigint"}},
		},
	}
	if _, err := graph.New().Sort([]pipeline.IRObject{fam}); err != nil {
		t.Fatalf("built-in-only loose members must not error: %v", err)
	}
}
