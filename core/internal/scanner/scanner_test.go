package scanner_test

import (
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/scanner"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func scan(t *testing.T, src string) []pipeline.RawObject {
	t.Helper()
	sc := scanner.New()
	objs, err := sc.Scan("test.dpg", []byte(src))
	if err != nil {
		t.Fatalf("scan error: %v", err)
	}
	return objs
}

func scanErr(t *testing.T, src string) error {
	t.Helper()
	sc := scanner.New()
	_, err := sc.Scan("test.dpg", []byte(src))
	return err
}

func assertOne(t *testing.T, objs []pipeline.RawObject) pipeline.RawObject {
	t.Helper()
	if len(objs) != 1 {
		t.Fatalf("expected 1 object, got %d", len(objs))
	}
	return objs[0]
}

func assertKind(t *testing.T, obj pipeline.RawObject, want pipeline.ObjectKind) {
	t.Helper()
	if obj.Kind != want {
		t.Errorf("kind: got %s, want %s", obj.Kind, want)
	}
}

func assertPart1Contains(t *testing.T, obj pipeline.RawObject, want string) {
	t.Helper()
	if !strings.Contains(obj.Part1, want) {
		t.Errorf("Part1 %q does not contain %q", obj.Part1, want)
	}
}

func assertPart2Contains(t *testing.T, obj pipeline.RawObject, want string) {
	t.Helper()
	if !strings.Contains(obj.Part2, want) {
		t.Errorf("Part2 %q does not contain %q", obj.Part2, want)
	}
}

func assertPart2Empty(t *testing.T, obj pipeline.RawObject) {
	t.Helper()
	if obj.Part2 != "" {
		t.Errorf("expected empty Part2, got %q", obj.Part2)
	}
}

func assertSchema(t *testing.T, obj pipeline.RawObject, want string) {
	t.Helper()
	if obj.Schema != want {
		t.Errorf("Schema: got %q, want %q", obj.Schema, want)
	}
}

// ── single-kind detection ─────────────────────────────────────────────────────

func TestSimpleTable(t *testing.T) {
	src := `TABLE users (
    id    BIGINT GENERATED ALWAYS AS IDENTITY,
    email TEXT   NOT NULL,
    CONSTRAINT pk_users PRIMARY KEY (id)
);`
	obj := assertOne(t, scan(t, src))
	assertKind(t, obj, pipeline.KindTable)
	assertPart1Contains(t, obj, "users")
	assertPart1Contains(t, obj, "pk_users")
	assertPart2Empty(t, obj)
}

func TestTableWithBlock(t *testing.T) {
	src := `TABLE users (
    id    BIGINT GENERATED ALWAYS AS IDENTITY,
    email TEXT   NOT NULL
)
{
    INDEXES { idx_email (email); }
    GRANTS  { SELECT TO app_readonly; }
}`
	obj := assertOne(t, scan(t, src))
	assertKind(t, obj, pipeline.KindTable)
	assertPart1Contains(t, obj, "users")
	assertPart2Contains(t, obj, "idx_email")
	assertPart2Contains(t, obj, "app_readonly")
}

// TestTableExplicitEmptyBlockSetsHasPart2 is TABLE's sibling of
// TestFunctionExplicitEmptyBlockSetsHasPart2 — TABLE's Part1 is read via a
// different scanner path (readRawUntil, not readFunctionPart1) but shares
// the same scanBody-level readOptionalPart2 call, so it must behave
// identically: an explicit empty "{ }" sets HasPart2 even though Part2's
// content is "".
func TestTableExplicitEmptyBlockSetsHasPart2(t *testing.T) {
	src := `TABLE t (id integer) {}`
	obj := assertOne(t, scan(t, src))
	assertKind(t, obj, pipeline.KindTable)
	assertPart2Empty(t, obj)
	if !obj.HasPart2 {
		t.Error("expected HasPart2 == true for an explicit, empty '{ }' block")
	}
}

func TestTableWithStorageOptions(t *testing.T) {
	src := `TABLE large_events (
    id      BIGINT GENERATED ALWAYS AS IDENTITY,
    payload JSONB
) WITH (fillfactor = 70)
{
    INDEXES { idx_large_events_ts (created_at); }
}`
	obj := assertOne(t, scan(t, src))
	assertKind(t, obj, pipeline.KindTable)
	assertPart1Contains(t, obj, "fillfactor")
	assertPart2Contains(t, obj, "idx_large_events_ts")
}

func TestUnloggedTable(t *testing.T) {
	obj := assertOne(t, scan(t, `UNLOGGED TABLE session_cache (
    key   TEXT,
    value JSONB
);`))
	assertKind(t, obj, pipeline.KindUnloggedTable)
	assertPart1Contains(t, obj, "session_cache")
}

func TestForeignTable(t *testing.T) {
	obj := assertOne(t, scan(t, `FOREIGN TABLE remote_events (
    id      BIGINT,
    payload JSONB
) SERVER log_server OPTIONS (table_name 'events');`))
	assertKind(t, obj, pipeline.KindForeignTable)
	assertPart1Contains(t, obj, "log_server")
}

func TestView(t *testing.T) {
	src := `VIEW active_users AS
    SELECT id, email FROM users WHERE status = 'active';`
	obj := assertOne(t, scan(t, src))
	assertKind(t, obj, pipeline.KindView)
	assertPart1Contains(t, obj, "active_users")
	assertPart1Contains(t, obj, "SELECT")
	assertPart2Empty(t, obj)
}

func TestViewWithBlock(t *testing.T) {
	src := `VIEW active_users AS
    SELECT id, email FROM users WHERE status = 'active' {
    GRANTS { SELECT TO app_readonly; }
}`
	obj := assertOne(t, scan(t, src))
	assertKind(t, obj, pipeline.KindView)
	assertPart2Contains(t, obj, "app_readonly")
}

func TestMaterializedView(t *testing.T) {
	obj := assertOne(t, scan(t, `MATERIALIZED VIEW daily_revenue AS
    SELECT date_trunc('day', created_at) AS day, SUM(total_amount) AS revenue
    FROM orders WHERE status = 'completed' GROUP BY 1
WITH NO DATA;`))
	assertKind(t, obj, pipeline.KindMaterializedView)
	assertPart1Contains(t, obj, "daily_revenue")
	assertPart1Contains(t, obj, "WITH NO DATA")
}

func TestRecursiveView(t *testing.T) {
	obj := assertOne(t, scan(t, `RECURSIVE VIEW org_tree (id, parent_id, depth) AS
    SELECT id, parent_id, 0 FROM departments WHERE parent_id IS NULL
    UNION ALL
    SELECT d.id, d.parent_id, t.depth + 1 FROM departments d JOIN org_tree t ON d.parent_id = t.id;`))
	assertKind(t, obj, pipeline.KindRecursiveView)
}

func TestSimpleFunction(t *testing.T) {
	src := `FUNCTION active_user_count() RETURNS BIGINT
LANGUAGE sql STABLE PARALLEL SAFE
AS $$
    SELECT COUNT(*) FROM users WHERE status = 'active';
$$;`
	obj := assertOne(t, scan(t, src))
	assertKind(t, obj, pipeline.KindFunction)
	assertPart1Contains(t, obj, "active_user_count")
	assertPart1Contains(t, obj, "SELECT COUNT(*)")
	assertPart2Empty(t, obj)
}

func TestFunctionWithBlock(t *testing.T) {
	src := `FUNCTION get_user(p_email TEXT) RETURNS users
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = public
AS $$
BEGIN
    RETURN (SELECT * FROM users WHERE email = p_email);
END;
$$ {
    COMMENT 'Fetch user by email';
    GRANTS { EXECUTE TO app_service; }
}`
	obj := assertOne(t, scan(t, src))
	assertKind(t, obj, pipeline.KindFunction)
	assertPart1Contains(t, obj, "get_user")
	assertPart1Contains(t, obj, "$$")
	assertPart2Contains(t, obj, "app_service")
}

func TestNamedDollarQuote(t *testing.T) {
	src := `FUNCTION format_price(p NUMERIC) RETURNS TEXT
LANGUAGE plpgsql IMMUTABLE STRICT
AS $func$
BEGIN
    RETURN '$' || TO_CHAR(p, 'FM999,999,990.00');
END;
$func$;`
	obj := assertOne(t, scan(t, src))
	assertKind(t, obj, pipeline.KindFunction)
	assertPart1Contains(t, obj, "$func$")
	assertPart1Contains(t, obj, "RETURN '$'")
}

// TestFunctionPart1ExcludesTrailingSemicolon is a regression guard for the
// `dpg fmt` double-";;" bug: readFunctionPart1 used to consume a trailing
// ';' and include it in the returned Part1 string, unlike every other kind's
// readPart1 path (which strips ';' separately in scanBody and never bakes it
// into Part1). Since format/render.go always appends its own ';' when no
// "{ }" block follows, a Part1 that already ended in ';' rendered as ";;" —
// and worsened by one extra ';' on every subsequent `dpg fmt` run, since the
// same double-consumption happened again on the reformatted output.
func TestFunctionPart1ExcludesTrailingSemicolon(t *testing.T) {
	src := `FUNCTION f() RETURNS integer LANGUAGE sql AS $$SELECT 1$$;`
	obj := assertOne(t, scan(t, src))
	assertKind(t, obj, pipeline.KindFunction)
	if strings.HasSuffix(strings.TrimSpace(obj.Part1), ";") {
		t.Errorf("Part1 must not include the trailing ';': %q", obj.Part1)
	}
	if obj.HasPart2 {
		t.Errorf("expected HasPart2 == false for a bare ';'-terminated function, got true")
	}
}

// TestFunctionExplicitEmptyBlockSetsHasPart2 guards the second half of the
// same fix: an explicit, genuinely empty "{ }" block reads as Part2 == ""
// (identical to no block at all), so HasPart2 is the only signal that lets
// the formatter round-trip "{ }" as "{ }" instead of collapsing it to a bare
// ';'.
func TestFunctionExplicitEmptyBlockSetsHasPart2(t *testing.T) {
	src := `FUNCTION f() RETURNS integer LANGUAGE sql AS $$SELECT 1$$ {}`
	obj := assertOne(t, scan(t, src))
	assertKind(t, obj, pipeline.KindFunction)
	assertPart2Empty(t, obj)
	if !obj.HasPart2 {
		t.Error("expected HasPart2 == true for an explicit, empty '{ }' block")
	}
}

// TestFunctionObjFileLinkSymbolPart1 guards RFC audit item #27's scanner
// support: the "AS 'obj_file', 'link_symbol'" form has no dollar-quote at
// all, so readFunctionPart1 previously ran to EOF looking for one and
// errored — this form was entirely unusable in a hand-written .dpg source
// file before this fix, regardless of what any other layer did with it.
func TestFunctionObjFileLinkSymbolPart1(t *testing.T) {
	src := `FUNCTION f(x integer) RETURNS integer LANGUAGE c AS 'MODULE_PATHNAME', 'f_impl';
FUNCTION g() RETURNS integer LANGUAGE sql AS $$SELECT 1$$;`
	objs := scan(t, src)
	if len(objs) != 2 {
		t.Fatalf("expected 2 objects, got %d: %v", len(objs), objs)
	}
	assertKind(t, objs[0], pipeline.KindFunction)
	assertPart1Contains(t, objs[0], "'MODULE_PATHNAME'")
	assertPart1Contains(t, objs[0], "'f_impl'")
	if strings.HasSuffix(strings.TrimSpace(objs[0].Part1), ";") {
		t.Errorf("Part1 must not include the trailing ';': %q", objs[0].Part1)
	}
	// The second function proves the scanner correctly resumed scanning
	// after the first statement's bare ';' instead of running past it.
	assertKind(t, objs[1], pipeline.KindFunction)
}

// TestFunctionAtomicBodyPart1 guards RFC audit item #28's scanner support:
// BEGIN ATOMIC ... END has no dollar-quote either, and its own statement
// list can itself contain a "CASE ... END" expression — the scanner must
// distinguish that from BEGIN ATOMIC's own closing END, not just stop at
// the first "END" word it sees.
func TestFunctionAtomicBodyPart1(t *testing.T) {
	src := `FUNCTION f(x integer) RETURNS integer LANGUAGE sql BEGIN ATOMIC SELECT CASE WHEN x > 0 THEN 1 ELSE 0 END; END;
FUNCTION g() RETURNS integer LANGUAGE sql AS $$SELECT 1$$;`
	objs := scan(t, src)
	if len(objs) != 2 {
		t.Fatalf("expected 2 objects, got %d: %v", len(objs), objs)
	}
	assertKind(t, objs[0], pipeline.KindFunction)
	assertPart1Contains(t, objs[0], "BEGIN ATOMIC")
	assertPart1Contains(t, objs[0], "CASE")
	if strings.HasSuffix(strings.TrimSpace(objs[0].Part1), ";") {
		t.Errorf("Part1 must not include the trailing ';': %q", objs[0].Part1)
	}
	if !strings.HasSuffix(strings.TrimSpace(objs[0].Part1), "END") {
		t.Errorf("Part1 must end at the outer BEGIN ATOMIC's own closing END, not the CASE expression's: %q", objs[0].Part1)
	}
	assertKind(t, objs[1], pipeline.KindFunction)
}

// TestProcedureAtomicBodyPart1 is TestFunctionAtomicBodyPart1's PROCEDURE
// counterpart.
func TestProcedureAtomicBodyPart1(t *testing.T) {
	src := `PROCEDURE p(x integer) LANGUAGE sql BEGIN ATOMIC SELECT x; END;`
	obj := assertOne(t, scan(t, src))
	assertKind(t, obj, pipeline.KindProcedure)
	assertPart1Contains(t, obj, "BEGIN ATOMIC")
}

func TestProcedure(t *testing.T) {
	obj := assertOne(t, scan(t, `PROCEDURE process_settlements()
LANGUAGE plpgsql SECURITY DEFINER
AS $$
BEGIN
    PERFORM settle_order(id) FROM settlements WHERE processed = false;
END;
$$;`))
	assertKind(t, obj, pipeline.KindProcedure)
}

func TestEnum(t *testing.T) {
	obj := assertOne(t, scan(t, `TYPE user_status AS ENUM ('active', 'suspended', 'deleted');`))
	assertKind(t, obj, pipeline.KindEnum)
	assertPart1Contains(t, obj, "user_status")
	assertPart1Contains(t, obj, "'active'")
}

func TestEnumWithBlock(t *testing.T) {
	obj := assertOne(t, scan(t, `TYPE invoice_status AS ENUM ('draft', 'sent', 'paid', 'void') {
    COMMENT 'Billing lifecycle states';
}`))
	assertKind(t, obj, pipeline.KindEnum)
	assertPart2Contains(t, obj, "Billing lifecycle states")
}

// ── terminator rules (RFC Section 4.5, Rules T1-T3 unified) ────────────────────
//
// Guards the launch-readiness audit finding: the RFC declared Section 4.5's
// rules NORMATIVE ("a compiler that silently accepts deviations is non-
// conforming"), but the scanner enforced neither direction — a ';' before an
// immediately-following '{ }' block, and a missing mandatory ';' when no
// block follows, both compiled silently. DPG-E037/E038 close both gaps, for
// a representative kind from each Part-1-ending shape (paren-close: table;
// dollar-quote: function; plain text: view).

// assertErrCode fails the test unless err is a *pipeline.CompilerError
// carrying the given DPG-Ennn code.
func assertErrCode(t *testing.T, err error, want string) {
	t.Helper()
	ce, ok := err.(*pipeline.CompilerError)
	if !ok {
		t.Fatalf("expected *pipeline.CompilerError, got %T: %v", err, err)
	}
	if ce.Code != want {
		t.Fatalf("expected code %s, got %q (message: %s)", want, ce.Code, ce.Message)
	}
}

func TestSemicolonBeforeBlockForbiddenTable(t *testing.T) {
	assertErrCode(t, scanErr(t, `TABLE t (id integer) ; { COMMENT 'x'; }`), "DPG-E037")
}

func TestSemicolonBeforeBlockForbiddenFunction(t *testing.T) {
	assertErrCode(t, scanErr(t, `FUNCTION f() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$; { COMMENT 'x'; }`), "DPG-E037")
}

func TestSemicolonBeforeBlockForbiddenView(t *testing.T) {
	assertErrCode(t, scanErr(t, `VIEW v AS SELECT 1; { COMMENT 'x'; }`), "DPG-E037")
}

func TestMissingMandatorySemicolonTable(t *testing.T) {
	assertErrCode(t, scanErr(t, `TABLE t (id integer)`), "DPG-E038")
}

func TestMissingMandatorySemicolonFunction(t *testing.T) {
	assertErrCode(t, scanErr(t, `FUNCTION f() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$`), "DPG-E038")
}

func TestMissingMandatorySemicolonView(t *testing.T) {
	assertErrCode(t, scanErr(t, `VIEW v AS SELECT 1`), "DPG-E038")
}

// TestTerminatorRulesPositiveCases proves the correct form for each kind
// still compiles cleanly in both shapes: no block (mandatory ';'), and a
// block immediately following (no ';', brace on the same line).
func TestTerminatorRulesPositiveCases(t *testing.T) {
	cases := []string{
		`TABLE t (id integer);`,
		`TABLE t (id integer) { COMMENT 'x'; }`,
		`FUNCTION f() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$;`,
		`FUNCTION f() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$ { COMMENT 'x'; }`,
		`VIEW v AS SELECT 1;`,
		`VIEW v AS SELECT 1 { COMMENT 'x'; }`,
	}
	for _, src := range cases {
		if _, err := scanner.New().Scan("test.dpg", []byte(src)); err != nil {
			t.Errorf("scan(%q) unexpectedly failed: %v", src, err)
		}
	}
}

func TestCompositeType(t *testing.T) {
	obj := assertOne(t, scan(t, `TYPE address AS (
    street TEXT,
    city   TEXT,
    state  CHAR(2)
);`))
	assertKind(t, obj, pipeline.KindCompositeType)
	assertPart1Contains(t, obj, "address")
}

func TestRangeType(t *testing.T) {
	obj := assertOne(t, scan(t, `TYPE float8range AS RANGE (
    SUBTYPE      = float8,
    SUBTYPE_DIFF = float8mi
);`))
	assertKind(t, obj, pipeline.KindRangeType)
}

func TestDomainType(t *testing.T) {
	obj := assertOne(t, scan(t, `DOMAIN positive_integer AS INTEGER
{
    CONSTRAINT positive_only CHECK (VALUE > 0);
}`))
	assertKind(t, obj, pipeline.KindDomainType)
	assertPart1Contains(t, obj, "positive_integer")
	assertPart2Contains(t, obj, "positive_only")
}

func TestSchema(t *testing.T) {
	src := `SCHEMA analytics {
    OWNER "analytics_role";
    COMMENT 'Derived tables and event aggregations';
}`
	objs := scan(t, src)
	if len(objs) != 1 {
		t.Fatalf("expected 1 object (schema only), got %d", len(objs))
	}
	assertKind(t, objs[0], pipeline.KindSchema)
	if objs[0].Part1 != "analytics" {
		t.Errorf("schema Part1: got %q, want %q", objs[0].Part1, "analytics")
	}
	assertPart2Contains(t, objs[0], "OWNER")
	assertPart2Contains(t, objs[0], "COMMENT")
}

// Part1 for a quoted schema name stays quoted (re-escaped from the unescaped
// value) because it feeds into "CREATE SCHEMA <part1>" for pg_query parsing
// (see pgparser.Reconstruct) — an unquoted reserved word there is a real PG
// syntax error, same as in raw SQL.
func TestSchemaQuotedReservedWordName(t *testing.T) {
	src := `SCHEMA "select" {
    OWNER "postgres";
}`
	objs := scan(t, src)
	if len(objs) != 1 {
		t.Fatalf("expected 1 object (schema only), got %d", len(objs))
	}
	assertKind(t, objs[0], pipeline.KindSchema)
	if objs[0].Part1 != `"select"` {
		t.Errorf("schema Part1: got %q, want %q", objs[0].Part1, `"select"`)
	}
}

func TestSchemaQuotedNameWithSpecialChars(t *testing.T) {
	src := `SCHEMA "my-schema" {
}`
	objs := scan(t, src)
	if len(objs) != 1 {
		t.Fatalf("expected 1 object (schema only), got %d", len(objs))
	}
	assertKind(t, objs[0], pipeline.KindSchema)
	if objs[0].Part1 != `"my-schema"` {
		t.Errorf("schema Part1: got %q, want %q", objs[0].Part1, `"my-schema"`)
	}
}

func TestSchemaQuotedNameWithEscapedQuote(t *testing.T) {
	src := `SCHEMA "weird""name" {
}`
	objs := scan(t, src)
	if len(objs) != 1 {
		t.Fatalf("expected 1 object (schema only), got %d", len(objs))
	}
	assertKind(t, objs[0], pipeline.KindSchema)
	if objs[0].Part1 != `"weird""name"` {
		t.Errorf("schema Part1: got %q, want %q", objs[0].Part1, `"weird""name"`)
	}
}

// Unquoted schema names must still round-trip as bare Part1 (no regression).
func TestSchemaUnquotedNameStaysBare(t *testing.T) {
	src := `SCHEMA analytics {
}`
	objs := scan(t, src)
	if len(objs) != 1 {
		t.Fatalf("expected 1 object (schema only), got %d", len(objs))
	}
	assertKind(t, objs[0], pipeline.KindSchema)
	if objs[0].Part1 != "analytics" {
		t.Errorf("schema Part1: got %q, want %q", objs[0].Part1, "analytics")
	}
}

func TestSchemaWithNestedObjects(t *testing.T) {
	src := `SCHEMA public {
    OWNER "postgres";

    TABLE users (
        id    BIGINT GENERATED ALWAYS AS IDENTITY,
        email TEXT NOT NULL,
        CONSTRAINT pk_users PRIMARY KEY (id)
    )
    {
        INDEXES { idx_email (email); }
    }

    FUNCTION active_count() RETURNS BIGINT
    LANGUAGE sql STABLE
    AS $$
        SELECT COUNT(*) FROM users WHERE active;
    $$;
}`
	objs := scan(t, src)

	// Expect: schema + table + function = 3 objects.
	if len(objs) != 3 {
		t.Fatalf("expected 3 objects, got %d: %v", len(objs), kindList(objs))
	}

	var schemaObj, tableObj, funcObj pipeline.RawObject
	for _, o := range objs {
		switch o.Kind {
		case pipeline.KindSchema:
			schemaObj = o
		case pipeline.KindTable:
			tableObj = o
		case pipeline.KindFunction:
			funcObj = o
		}
	}

	// Schema itself has attributes in Part2.
	assertKind(t, schemaObj, pipeline.KindSchema)
	if schemaObj.Part1 != "public" {
		t.Errorf("schema name: got %q, want %q", schemaObj.Part1, "public")
	}
	assertPart2Contains(t, schemaObj, "OWNER")

	// Nested table has Schema = "public".
	assertKind(t, tableObj, pipeline.KindTable)
	assertSchema(t, tableObj, "public")
	assertPart1Contains(t, tableObj, "users")
	assertPart2Contains(t, tableObj, "idx_email")

	// Nested function has Schema = "public".
	assertKind(t, funcObj, pipeline.KindFunction)
	assertSchema(t, funcObj, "public")
	assertPart1Contains(t, funcObj, "active_count")
}

func TestExtension(t *testing.T) {
	obj := assertOne(t, scan(t, `EXTENSION pgcrypto;`))
	assertKind(t, obj, pipeline.KindExtension)
	assertPart1Contains(t, obj, "pgcrypto")
}

func TestSequence(t *testing.T) {
	obj := assertOne(t, scan(t, `SEQUENCE order_number_seq
    AS BIGINT
    START WITH  10000
    INCREMENT BY 1
    MAXVALUE     99999999
    CACHE        50
    NO CYCLE
    OWNED BY orders.order_number;`))
	assertKind(t, obj, pipeline.KindSequence)
	assertPart1Contains(t, obj, "order_number_seq")
}

// TestUnloggedSequence guards RFC Section 10's UNLOGGED prefix — CREATE
// SEQUENCE and CREATE UNLOGGED SEQUENCE parse to the identical pg_query
// node, distinguished only by Sequence.Relpersistence, so the scanner
// (which decides Kind purely from the leading keyword text, before
// pg_query ever sees it) must recognize the prefix explicitly, mirroring
// UNLOGGED TABLE's identical mechanism.
func TestUnloggedSequence(t *testing.T) {
	obj := assertOne(t, scan(t, `UNLOGGED SEQUENCE session_counter;`))
	assertKind(t, obj, pipeline.KindUnloggedSequence)
	assertPart1Contains(t, obj, "session_counter")
}

// TestUnloggedRejectsUnknownFollower guards the corrected error message
// after UNLOGGED gained a second valid follower (SEQUENCE, alongside
// TABLE) — some other keyword must still error clearly, not silently
// misparse.
func TestUnloggedRejectsUnknownFollower(t *testing.T) {
	err := scanErr(t, `UNLOGGED VIEW v AS SELECT 1;`)
	if err == nil {
		t.Fatal("expected an error for UNLOGGED VIEW")
	}
}

func TestRole(t *testing.T) {
	obj := assertOne(t, scan(t, `ROLE app_readonly {
    NOLOGIN;
    COMMENT 'Read-only access';
}`))
	assertKind(t, obj, pipeline.KindRole)
	assertPart1Contains(t, obj, "app_readonly")
	assertPart2Contains(t, obj, "NOLOGIN")
}

func TestFDW(t *testing.T) {
	obj := assertOne(t, scan(t, `FOREIGN DATA WRAPPER myfdw
    HANDLER   myfdw_handler
    VALIDATOR myfdw_validator;`))
	assertKind(t, obj, pipeline.KindFDW)
}

func TestServer(t *testing.T) {
	obj := assertOne(t, scan(t, `SERVER analytics_warehouse
    FOREIGN DATA WRAPPER postgres_fdw
    OPTIONS (host 'warehouse.internal', dbname 'analytics');`))
	assertKind(t, obj, pipeline.KindServer)
}

func TestUserMapping(t *testing.T) {
	obj := assertOne(t, scan(t, `USER MAPPING FOR app_service
    SERVER analytics_warehouse
    OPTIONS (user 'fdw_user', password 'env:FDW_PASSWORD');`))
	assertKind(t, obj, pipeline.KindUserMapping)
}

func TestPublication(t *testing.T) {
	obj := assertOne(t, scan(t, `PUBLICATION user_data
    FOR TABLE users, profiles
    WITH (publish = 'insert, update, delete');`))
	assertKind(t, obj, pipeline.KindPublication)
}

func TestSubscription(t *testing.T) {
	obj := assertOne(t, scan(t, `SUBSCRIPTION replica_users
    CONNECTION 'host=primary.db.internal dbname=myapp user=replicator'
    PUBLICATION user_data
    WITH (enabled = true, copy_data = true);`))
	assertKind(t, obj, pipeline.KindSubscription)
}

func TestEventTrigger(t *testing.T) {
	obj := assertOne(t, scan(t, `EVENT TRIGGER prevent_drop_table
    ON sql_drop
    WHEN TAG IN ('DROP TABLE', 'DROP SCHEMA')
    EXECUTE FUNCTION abort_drop();`))
	assertKind(t, obj, pipeline.KindEventTrigger)
}

func TestCollation(t *testing.T) {
	obj := assertOne(t, scan(t, `COLLATION case_insensitive (
    PROVIDER      = icu,
    LOCALE        = 'und-u-ks-level2',
    DETERMINISTIC = false
);`))
	assertKind(t, obj, pipeline.KindCollation)
}

func TestOperator(t *testing.T) {
	obj := assertOne(t, scan(t, `OPERATOR === (
    LEFTARG   = complex,
    RIGHTARG  = complex,
    PROCEDURE = complex_eq
);`))
	assertKind(t, obj, pipeline.KindOperator)
}

func TestOperatorClass(t *testing.T) {
	obj := assertOne(t, scan(t, `OPERATOR CLASS my_ops USING btree FOR TYPE mytype (
    OPERATOR 1 < ,
    OPERATOR 3 =
);`))
	assertKind(t, obj, pipeline.KindOperatorClass)
}

func TestOperatorFamily(t *testing.T) {
	obj := assertOne(t, scan(t, `OPERATOR FAMILY my_family USING btree;`))
	assertKind(t, obj, pipeline.KindOperatorFamily)
}

func TestCast(t *testing.T) {
	obj := assertOne(t, scan(t, `CAST (mytype AS TEXT)
    WITH FUNCTION mytype_to_text(mytype)
    AS IMPLICIT;`))
	assertKind(t, obj, pipeline.KindCast)
}

func TestStatistics(t *testing.T) {
	obj := assertOne(t, scan(t, `STATISTICS orders_stats (dependencies, ndistinct, mcv)
    ON customer_id, created_at
    FROM orders;`))
	assertKind(t, obj, pipeline.KindStatisticsObject)
}

func TestTextSearchConfiguration(t *testing.T) {
	obj := assertOne(t, scan(t, `TEXT SEARCH CONFIGURATION english_unaccented (COPY = pg_catalog.english)
{
    MAPPING FOR hword, hword_part WITH unaccent, english_stem;
}`))
	assertKind(t, obj, pipeline.KindTSConfig)
}

func TestTextSearchDictionary(t *testing.T) {
	obj := assertOne(t, scan(t, `TEXT SEARCH DICTIONARY english_ispell (
    TEMPLATE  = ispell,
    DictFile  = english,
    AffFile   = english
);`))
	assertKind(t, obj, pipeline.KindTSDict)
}

func TestTextSearchParser(t *testing.T) {
	obj := assertOne(t, scan(t, `TEXT SEARCH PARSER my_parser (
    START    = prsd_start,
    GETTOKEN = prsd_nexttoken,
    END      = prsd_end,
    LEXTYPES = prsd_lextype
);`))
	assertKind(t, obj, pipeline.KindTSParser)
}

func TestTextSearchTemplate(t *testing.T) {
	obj := assertOne(t, scan(t, `TEXT SEARCH TEMPLATE ispell_template (
    LEXIZE = dispell_lexize,
    INIT   = dispell_init
);`))
	assertKind(t, obj, pipeline.KindTSTemplate)
}

func TestDefaultPrivileges(t *testing.T) {
	obj := assertOne(t, scan(t, `DEFAULT PRIVILEGES FOR ROLE app_admin {
    GRANTS {
        SELECT  ON TABLES    TO app_readonly;
        EXECUTE ON FUNCTIONS TO app_service;
    }
}`))
	assertKind(t, obj, pipeline.KindDefaultPrivileges)
}

func TestAggregate(t *testing.T) {
	obj := assertOne(t, scan(t, `AGGREGATE product (DOUBLE PRECISION) (
    SFUNC    = float8mul,
    STYPE    = DOUBLE PRECISION,
    INITCOND = '1'
);`))
	assertKind(t, obj, pipeline.KindAggregate)
}

func TestTablespace(t *testing.T) {
	obj := assertOne(t, scan(t, `TABLESPACE fast_ssd LOCATION '/mnt/nvme/pg_data';`))
	assertKind(t, obj, pipeline.KindTablespace)
}

// ── multi-declaration files ───────────────────────────────────────────────────

func TestMultipleTopLevelDeclarations(t *testing.T) {
	src := `
EXTENSION pgcrypto;

ROLE app_service { LOGIN; CONNECTION LIMIT 10; }

SEQUENCE order_seq AS BIGINT START WITH 1 INCREMENT BY 1;
`
	objs := scan(t, src)
	if len(objs) != 3 {
		t.Fatalf("expected 3 objects, got %d", len(objs))
	}
	assertKind(t, objs[0], pipeline.KindExtension)
	assertKind(t, objs[1], pipeline.KindRole)
	assertKind(t, objs[2], pipeline.KindSequence)
}

func TestMixedTopLevelAndSchema(t *testing.T) {
	src := `
EXTENSION pgcrypto;

SCHEMA public {
    OWNER "postgres";
    TYPE status AS ENUM ('a', 'b');
}

ROLE admin { LOGIN; }
`
	objs := scan(t, src)
	// extension + schema + enum + role = 4
	if len(objs) != 4 {
		t.Fatalf("expected 4 objects, got %d: %v", len(objs), kindList(objs))
	}

	kinds := kindList(objs)
	if kinds[0] != "EXTENSION" {
		t.Errorf("expected EXTENSION first, got %s", kinds[0])
	}
	if kinds[3] != "ROLE" {
		t.Errorf("expected ROLE last, got %s", kinds[3])
	}

	// Check enum has schema context.
	for _, o := range objs {
		if o.Kind == pipeline.KindEnum {
			assertSchema(t, o, "public")
		}
	}
}

// ── source positions ──────────────────────────────────────────────────────────

func TestSourcePosition(t *testing.T) {
	src := "\n\nTABLE users (id BIGINT);\n"
	obj := assertOne(t, scan(t, src))
	if obj.Pos.Line != 3 {
		t.Errorf("expected line 3, got %d", obj.Pos.Line)
	}
}

// ── dollar-quote edge cases ───────────────────────────────────────────────────

func TestSemicolonInsideDollarQuoteNotTerminator(t *testing.T) {
	src := `FUNCTION many_stmts() RETURNS VOID LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO t VALUES (1);
    INSERT INTO t VALUES (2);
    INSERT INTO t VALUES (3);
END;
$$;`
	obj := assertOne(t, scan(t, src))
	assertKind(t, obj, pipeline.KindFunction)
	// All semicolons inside $$ should be part of Part1, not terminators.
	assertPart1Contains(t, obj, "INSERT INTO t VALUES (1)")
	assertPart1Contains(t, obj, "INSERT INTO t VALUES (3)")
}

func TestBraceInsideDollarQuoteNotPart2(t *testing.T) {
	src := `FUNCTION returns_json() RETURNS JSONB LANGUAGE sql STABLE AS $$
    SELECT '{"key": "value"}'::jsonb;
$$;`
	obj := assertOne(t, scan(t, src))
	assertKind(t, obj, pipeline.KindFunction)
	assertPart2Empty(t, obj)
}

func TestSingleQuoteInsideDollarQuote(t *testing.T) {
	src := `FUNCTION greet(name TEXT) RETURNS TEXT LANGUAGE sql IMMUTABLE AS $$
    SELECT 'Hello, ' || name || '!';
$$;`
	obj := assertOne(t, scan(t, src))
	assertKind(t, obj, pipeline.KindFunction)
	assertPart1Contains(t, obj, "'Hello, '")
}

// ── comment handling ──────────────────────────────────────────────────────────

func TestLineCommentBeforeDeclaration(t *testing.T) {
	src := `-- This is a comment
TABLE users (id BIGINT);`
	obj := assertOne(t, scan(t, src))
	assertKind(t, obj, pipeline.KindTable)
}

func TestBlockCommentBetweenDeclarations(t *testing.T) {
	src := `EXTENSION pgcrypto; /* a comment */ EXTENSION pg_trgm;`
	objs := scan(t, src)
	if len(objs) != 2 {
		t.Fatalf("expected 2, got %d", len(objs))
	}
}

// ── no-verb mandate ───────────────────────────────────────────────────────────

func TestCreateVerbForbidden(t *testing.T) {
	if err := scanErr(t, `CREATE TABLE users (id BIGINT);`); err == nil {
		t.Error("expected error for CREATE at declaration level, got nil")
	}
}

func TestAlterVerbForbidden(t *testing.T) {
	if err := scanErr(t, `ALTER TABLE users ADD COLUMN name TEXT;`); err == nil {
		t.Error("expected error for ALTER at declaration level, got nil")
	}
}

func TestDropVerbForbidden(t *testing.T) {
	if err := scanErr(t, `DROP TABLE users;`); err == nil {
		t.Error("expected error for DROP at declaration level, got nil")
	}
}

func TestTemporaryTableForbidden(t *testing.T) {
	if err := scanErr(t, `TEMPORARY TABLE tmp (id INT);`); err == nil {
		t.Error("expected error for TEMPORARY TABLE, got nil")
	}
}

// ── string literal edge cases ─────────────────────────────────────────────────

func TestSemicolonInsideStringNotTerminator(t *testing.T) {
	src := `VIEW odd_alias AS
    SELECT id, ';' AS separator FROM users;`
	obj := assertOne(t, scan(t, src))
	assertKind(t, obj, pipeline.KindView)
	assertPart1Contains(t, obj, "';'")
}

// ── empty / whitespace-only source ───────────────────────────────────────────

func TestEmptySource(t *testing.T) {
	objs := scan(t, "")
	if len(objs) != 0 {
		t.Errorf("expected 0 objects for empty source, got %d", len(objs))
	}
}

func TestWhitespaceOnlySource(t *testing.T) {
	objs := scan(t, "   \n\t\n  -- comment only\n  ")
	if len(objs) != 0 {
		t.Errorf("expected 0 objects, got %d", len(objs))
	}
}

// ── registry ──────────────────────────────────────────────────────────────────

func TestRegistered(t *testing.T) {
	impl, ok := pipeline.Resolve[pipeline.Tokenizer](pipeline.Default, pipeline.KeyTokenizer)
	if !ok {
		t.Fatal("scanner not registered in pipeline.Default")
	}
	if impl == nil {
		t.Fatal("registered Tokenizer is nil")
	}
}

// ── VirtualType ───────────────────────────────────────────────────────────────

func TestScanVirtualType(t *testing.T) {
	obj := assertOne(t, scan(t, `VIRTUAL TYPE status AS "active" | "pending" | "inactive";`))
	assertKind(t, obj, pipeline.KindVirtualType)
	if !strings.Contains(obj.Part1, "status") {
		t.Errorf("Part1 should contain 'status', got %q", obj.Part1)
	}
	if !strings.Contains(obj.Part1, "active") {
		t.Errorf("Part1 should contain 'active', got %q", obj.Part1)
	}
}

func TestScanVirtualTypeWithBlock(t *testing.T) {
	obj := assertOne(t, scan(t,
		`VIRTUAL TYPE user_id_variants AS integer | bigint { COMMENT 'ID type union'; }`))
	assertKind(t, obj, pipeline.KindVirtualType)
	assertPart1Contains(t, obj, "user_id_variants")
	assertPart2Contains(t, obj, "COMMENT")
}

func TestScanVirtualTypeSchemaQualified(t *testing.T) {
	obj := assertOne(t, scan(t, `VIRTUAL TYPE billing.money_variant AS numeric | integer;`))
	assertKind(t, obj, pipeline.KindVirtualType)
	assertPart1Contains(t, obj, "billing.money_variant")
}

// ── Macro spread ──────────────────────────────────────────────────────────────

func TestMacroColumnExpansion(t *testing.T) {
	src := `
MACRO audit_cols (
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
)

TABLE orders (
    id BIGINT PRIMARY KEY,
    ...audit_cols
);
`
	objs := scan(t, src)
	if len(objs) != 1 {
		t.Fatalf("expected 1 object after macro expansion, got %d", len(objs))
	}
	obj := objs[0]
	assertKind(t, obj, pipeline.KindTable)
	if !strings.Contains(obj.Part1, "created_at") {
		t.Errorf("expanded Part1 should contain 'created_at', got %q", obj.Part1)
	}
	if !strings.Contains(obj.Part1, "updated_at") {
		t.Errorf("expanded Part1 should contain 'updated_at', got %q", obj.Part1)
	}
}

func TestMacroBlockExpansion(t *testing.T) {
	src := `
MACRO std_grants {
    GRANTS { SELECT TO app_reader; INSERT, UPDATE TO app_writer; }
}

TABLE items (id BIGINT) {
    ...std_grants
}
`
	objs := scan(t, src)
	if len(objs) != 1 {
		t.Fatalf("expected 1 object after macro expansion, got %d", len(objs))
	}
	obj := objs[0]
	assertKind(t, obj, pipeline.KindTable)
	if !strings.Contains(obj.Part2, "app_reader") {
		t.Errorf("expanded Part2 should contain 'app_reader', got %q", obj.Part2)
	}
}

func TestMacroUndefinedErrors(t *testing.T) {
	src := `TABLE orders (id BIGINT, ...missing_macro);`
	err := scanErr(t, src)
	if err == nil {
		t.Fatal("expected error for undefined macro spread, got nil")
	}
	if !strings.Contains(err.Error(), "missing_macro") {
		t.Errorf("error should mention 'missing_macro', got: %v", err)
	}
	if !strings.Contains(err.Error(), "DPG-E010") {
		t.Errorf("error should carry DPG-E010, got: %v", err)
	}
}

func TestMacroDuplicateNameIsError(t *testing.T) {
	src := `
MACRO ts_cols (created_at TIMESTAMPTZ)
MACRO ts_cols (updated_at TIMESTAMPTZ)

TABLE events (id BIGINT, ...ts_cols);
`
	err := scanErr(t, src)
	if err == nil {
		t.Fatal("expected error for duplicate macro name, got nil")
	}
	if !strings.Contains(err.Error(), "DPG-E011") {
		t.Errorf("error should carry DPG-E011, got: %v", err)
	}
}

func TestMacroInsideBlockIsError(t *testing.T) {
	src := `
TABLE orders (id BIGINT) {
    MACRO evil (x INT);
}
`
	err := scanErr(t, src)
	if err == nil {
		t.Fatal("expected error for MACRO declared inside a block, got nil")
	}
	if !strings.Contains(err.Error(), "DPG-E007") {
		t.Errorf("error should carry DPG-E007, got: %v", err)
	}
}

func TestParenMacroSpreadInsideBlockIsError(t *testing.T) {
	src := `
MACRO audit_cols (created_at TIMESTAMPTZ)

TABLE orders (id BIGINT) {
    ...audit_cols
}
`
	err := scanErr(t, src)
	if err == nil {
		t.Fatal("expected error for paren-body macro spread inside a { } block, got nil")
	}
	if !strings.Contains(err.Error(), "DPG-E008") {
		t.Errorf("error should carry DPG-E008, got: %v", err)
	}
}

func TestBraceMacroSpreadInsideParenIsError(t *testing.T) {
	src := `
MACRO std_grants {
    GRANTS { SELECT TO app_reader; }
}

TABLE orders (id BIGINT, ...std_grants);
`
	err := scanErr(t, src)
	if err == nil {
		t.Fatal("expected error for brace-body macro spread inside a ( ) list, got nil")
	}
	if !strings.Contains(err.Error(), "DPG-E009") {
		t.Errorf("error should carry DPG-E009, got: %v", err)
	}
}

func TestMacroMultipleSpreads(t *testing.T) {
	src := `
MACRO ts_cols (created_at TIMESTAMPTZ, updated_at TIMESTAMPTZ)
MACRO soft_delete (deleted_at TIMESTAMPTZ)

TABLE events (
    id BIGINT,
    ...ts_cols,
    name TEXT,
    ...soft_delete
);
`
	objs := scan(t, src)
	if len(objs) != 1 {
		t.Fatalf("expected 1 object, got %d", len(objs))
	}
	p1 := objs[0].Part1
	for _, col := range []string{"created_at", "updated_at", "deleted_at"} {
		if !strings.Contains(p1, col) {
			t.Errorf("Part1 should contain %q after expansion, got %q", col, p1)
		}
	}
}

// ── cross-file macro sharing ──────────────────────────────────────────────────

func TestGlobalMacro_AvailableInSecondFile(t *testing.T) {
	macroFile := []byte(`MACRO audit_cols (
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`)
	consumerFile := []byte(`TABLE events (id BIGINT, ...audit_cols);`)

	sc := scanner.New()
	if err := sc.AddGlobalMacros(macroFile); err != nil {
		t.Fatalf("AddGlobalMacros: %v", err)
	}
	objs, err := sc.Scan("consumer.dpg", consumerFile)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(objs) != 1 {
		t.Fatalf("expected 1 object, got %d", len(objs))
	}
	if !strings.Contains(objs[0].Part1, "created_at") {
		t.Errorf("global macro not expanded: Part1 = %q", objs[0].Part1)
	}
}

func TestGlobalMacro_LocalOverridesGlobal(t *testing.T) {
	globalFile := []byte(`MACRO ts_cols (created_at TIMESTAMPTZ)`)
	localFile := []byte(`
MACRO ts_cols (created_at TIMESTAMPTZ, updated_at TIMESTAMPTZ)
TABLE events (id BIGINT, ...ts_cols);
`)
	sc := scanner.New()
	if err := sc.AddGlobalMacros(globalFile); err != nil {
		t.Fatalf("AddGlobalMacros: %v", err)
	}
	objs, err := sc.Scan("local.dpg", localFile)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(objs) != 1 {
		t.Fatalf("expected 1 object, got %d", len(objs))
	}
	// local definition has both columns; global only has created_at
	if !strings.Contains(objs[0].Part1, "updated_at") {
		t.Errorf("local macro should override global: Part1 = %q", objs[0].Part1)
	}
}

func TestGlobalMacro_MultipleFiles(t *testing.T) {
	file1 := []byte(`MACRO col_a (a TEXT NOT NULL)`)
	file2 := []byte(`MACRO col_b (b INTEGER NOT NULL DEFAULT 0)`)
	consumer := []byte(`TABLE t (id BIGINT, ...col_a, ...col_b);`)

	sc := scanner.New()
	for _, f := range [][]byte{file1, file2} {
		if err := sc.AddGlobalMacros(f); err != nil {
			t.Fatalf("AddGlobalMacros: %v", err)
		}
	}
	objs, err := sc.Scan("consumer.dpg", consumer)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(objs) != 1 {
		t.Fatalf("expected 1 object, got %d", len(objs))
	}
	for _, col := range []string{"a TEXT", "b INTEGER"} {
		if !strings.Contains(objs[0].Part1, col) {
			t.Errorf("expected %q in Part1, got %q", col, objs[0].Part1)
		}
	}
}

// ── nested macro tests ────────────────────────────────────────────────────────

func TestNestedMacro_ParenInParen(t *testing.T) {
	src := `
MACRO base_cols (id UUID NOT NULL)
MACRO extended_cols (...base_cols, name TEXT NOT NULL)

TABLE users (
    ...extended_cols,
    CONSTRAINT pk PRIMARY KEY (id)
);`
	objs := scan(t, src)
	obj := assertOne(t, objs)
	if !strings.Contains(obj.Part1, "id UUID NOT NULL") {
		t.Errorf("expected base_cols expanded, got %q", obj.Part1)
	}
	if !strings.Contains(obj.Part1, "name TEXT NOT NULL") {
		t.Errorf("expected extended_cols own cols, got %q", obj.Part1)
	}
}

func TestNestedMacro_BraceInBrace(t *testing.T) {
	src := `
MACRO base_block {
    OWNER "admin";
}
MACRO full_block {
    ...base_block
    ENABLE ROW LEVEL SECURITY;
}

TABLE orders (id BIGINT PRIMARY KEY) { ...full_block }`
	objs := scan(t, src)
	obj := assertOne(t, objs)
	if !strings.Contains(obj.Part2, "OWNER") {
		t.Errorf("expected base_block expanded into full_block, Part2=%q", obj.Part2)
	}
	if !strings.Contains(obj.Part2, "ENABLE ROW LEVEL SECURITY") {
		t.Errorf("expected full_block own content, Part2=%q", obj.Part2)
	}
}

func TestNestedMacro_ThreeLevels(t *testing.T) {
	src := `
MACRO a (x INT)
MACRO b (...a, y TEXT)
MACRO c (...b, z BOOL)

TABLE t (...c);`
	objs := scan(t, src)
	obj := assertOne(t, objs)
	for _, col := range []string{"x INT", "y TEXT", "z BOOL"} {
		if !strings.Contains(obj.Part1, col) {
			t.Errorf("expected %q in Part1, got %q", col, obj.Part1)
		}
	}
}

func TestNestedMacro_CircularIsError(t *testing.T) {
	src := `
MACRO a (...b, x INT)
MACRO b (...a, y TEXT)

TABLE t (...a);`
	err := scanErr(t, src)
	if err == nil {
		t.Fatal("expected error for circular macro reference, got nil")
	}
	if !strings.Contains(err.Error(), "circular") && !strings.Contains(err.Error(), "E012") {
		t.Errorf("expected circular reference error, got %v", err)
	}
}

// ── helper ───────────────────────────────────────────────────────────────────

func kindList(objs []pipeline.RawObject) []string {
	out := make([]string, len(objs))
	for i, o := range objs {
		out[i] = o.Kind.String()
	}
	return out
}

func TestEnumShorthandRejected(t *testing.T) {
	err := scanErr(t, `ENUM mood ('sad', 'happy');`)
	if err == nil || !strings.Contains(err.Error(), "TYPE name AS ENUM") {
		t.Fatalf("expected error pointing at TYPE name AS ENUM, got %v", err)
	}
}

func TestTypeAsEnumQualifiedName(t *testing.T) {
	obj := assertOne(t, scan(t, `TYPE archive.mood AS ENUM ('sad', 'happy') {
    COMMENT 'm';
}`))
	if obj.Kind != pipeline.KindEnum {
		t.Fatalf("expected KindEnum, got %v", obj.Kind)
	}
	if !strings.HasPrefix(obj.Part1, "archive.mood AS ENUM (") {
		t.Errorf("unexpected Part1 %q", obj.Part1)
	}
}
