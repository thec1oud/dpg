package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/config"
	"github.com/thec1oud/dpg/internal/diff"
	"github.com/thec1oud/dpg/internal/format"
	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/project"
	"github.com/thec1oud/dpg/internal/snapshot"
)

// TestRenderColumnSerialCompiles proves a column with Serial set renders the
// literal SERIAL keyword (not the normalized underlying type with a
// hand-reconstructed NOT NULL/DEFAULT nextval(...) referencing a sequence
// dump never declares — the exact "genuinely broken, non-reapplicable
// output" bug this workstream closes), and that the result recompiles.
func TestRenderColumnSerialCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	serial := "SERIAL"
	tbl := &ir.Table{
		Schema: "public", Name: "widgets",
		Columns: []*ir.Column{
			{Name: "id", Type: ir.TypeRef{Name: "integer"}, Serial: &serial, NotNull: true},
			{Name: "name", Type: ir.TypeRef{Name: "text"}},
		},
	}

	var b strings.Builder
	renderObjectDPG(&b, tbl, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "id SERIAL") {
		t.Errorf("rendered column does not use the literal SERIAL keyword: %q", rendered)
	}
	if strings.Contains(rendered, "nextval") {
		t.Errorf("rendered column must not reference nextval(...) directly: %q", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := compiler.Compile([]string{f}, dir, pipeline.Default); err != nil {
		t.Fatalf("dumped table with a SERIAL column failed to recompile: %v\n---\n%s", err, rendered)
	}
}

// TestRenderGeneratedColumnVirtualCompiles guards a real bug found while
// implementing VIRTUAL generated-column diffing (RFC Section 7.2, PG18+):
// renderObjectDPG's column-rendering loop hardcoded the STORED keyword for
// every generated column regardless of col.Generated.Stored, so dumping a
// table with a VIRTUAL generated column silently rewrote it as STORED.
func TestRenderGeneratedColumnVirtualCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	tbl := &ir.Table{
		Schema: "public", Name: "orders",
		Columns: []*ir.Column{
			{Name: "amount", Type: ir.TypeRef{Name: "numeric"}},
			{
				Name: "amount_with_tax", Type: ir.TypeRef{Name: "numeric"},
				Generated: &ir.Generated{Expr: "amount * 1.08", Stored: false},
			},
		},
	}

	var b strings.Builder
	renderObjectDPG(&b, tbl, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "GENERATED ALWAYS AS (amount * 1.08) VIRTUAL") {
		t.Errorf("rendered column does not use VIRTUAL: %q", rendered)
	}
	if strings.Contains(rendered, ") STORED") {
		t.Errorf("rendered column must not fall back to STORED for a VIRTUAL generated column: %q", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := compiler.Compile([]string{f}, dir, pipeline.Default); err != nil {
		t.Fatalf("dumped table with a VIRTUAL generated column failed to recompile: %v\n---\n%s", err, rendered)
	}
}

// TestRenderNamedNotNullConstraintCompiles guards RFC Section 7.3's
// table-level named NOT NULL constraint (PostgreSQL 18+): a single-column,
// validated one renders inline on its column (isInlineable/
// inlineConstraintClause), without ALSO emitting a redundant bare "NOT
// NULL" from Column.NotNull's own rendering — and the result recompiles.
func TestRenderNamedNotNullConstraintCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	tbl := &ir.Table{
		Schema: "public", Name: "widgets",
		Columns: []*ir.Column{
			{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
			{Name: "sku", Type: ir.TypeRef{Name: "text"}, NotNull: true},
		},
		Constraints: []*ir.Constraint{
			{Name: "named_nn", Type: "NOT NULL", Columns: []string{"sku"}, NoInherit: true, Expr: `NOT NULL "sku" NO INHERIT`},
		},
	}

	var b strings.Builder
	renderObjectDPG(&b, tbl, fmtOpts)
	rendered := b.String()

	// Constraint names are intentionally dropped when inlined — matching
	// the pre-existing PRIMARY KEY/UNIQUE/FOREIGN KEY precedent
	// (inlineConstraintClause's own doc comment: "PostgreSQL auto-generates
	// them"), extended here to NOT NULL for consistency.
	if !strings.Contains(rendered, "sku text NOT NULL NO INHERIT") {
		t.Errorf("rendered column does not inline NOT NULL NO INHERIT: %q", rendered)
	}
	if strings.Count(rendered, "NOT NULL") != 1 {
		t.Errorf("expected exactly one NOT NULL occurrence (no redundant bare one), got: %q", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := compiler.Compile([]string{f}, dir, pipeline.Default); err != nil {
		t.Fatalf("dumped table with a named NOT NULL constraint failed to recompile: %v\n---\n%s", err, rendered)
	}
}

// TestRenderNotValidNotNullConstraintCompiles is
// TestRenderNamedNotNullConstraintCompiles' NOT VALID counterpart — a
// constraint with NOT VALID can't be inlined (same exclusion CHECK/FOREIGN
// KEY already have), so it must render as its own table-level item instead.
func TestRenderNotValidNotNullConstraintCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	tbl := &ir.Table{
		Schema: "public", Name: "widgets",
		Columns: []*ir.Column{
			{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
			{Name: "sku", Type: ir.TypeRef{Name: "text"}, NotNull: true},
		},
		Constraints: []*ir.Constraint{
			{Name: "named_nn", Type: "NOT NULL", Columns: []string{"sku"}, NotValid: true, Expr: `NOT NULL "sku"`},
		},
	}

	var b strings.Builder
	renderObjectDPG(&b, tbl, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, `CONSTRAINT named_nn NOT NULL "sku"`) {
		t.Errorf("rendered constraint missing or malformed: %q", rendered)
	}
	if !strings.Contains(rendered, "NOT VALID") {
		t.Errorf("rendered constraint does not carry NOT VALID: %q", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := compiler.Compile([]string{f}, dir, pipeline.Default); err != nil {
		t.Fatalf("dumped table with a NOT VALID NOT NULL constraint failed to recompile: %v\n---\n%s", err, rendered)
	}
}

// TestRenderCheckNotEnforcedCompiles guards RFC Section 7.2's ENFORCED/NOT
// ENFORCED (PostgreSQL 18+, CHECK/FOREIGN KEY only): a NOT ENFORCED CHECK
// renders as its own table-level item (deliberately excluded from inlining,
// same treatment as NOT VALID — see isInlineable's call site) and
// recompiles.
func TestRenderCheckNotEnforcedCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	tbl := &ir.Table{
		Schema: "public", Name: "widgets",
		Columns: []*ir.Column{
			{Name: "amount", Type: ir.TypeRef{Name: "numeric"}},
		},
		Constraints: []*ir.Constraint{
			{Name: "ck", Type: "CHECK", Columns: []string{"amount"}, Expr: "CHECK (amount > 0)", NotEnforced: true},
		},
	}

	var b strings.Builder
	renderObjectDPG(&b, tbl, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "CONSTRAINT ck CHECK (amount > 0) NOT ENFORCED") {
		t.Errorf("rendered constraint missing or malformed: %q", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := compiler.Compile([]string{f}, dir, pipeline.Default); err != nil {
		t.Fatalf("dumped table with a NOT ENFORCED CHECK constraint failed to recompile: %v\n---\n%s", err, rendered)
	}
}

// TestRenderForeignKeyNotEnforcedCompiles is
// TestRenderCheckNotEnforcedCompiles' FOREIGN KEY counterpart.
func TestRenderForeignKeyNotEnforcedCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	tbl := &ir.Table{
		Schema: "public", Name: "orders",
		Columns: []*ir.Column{
			{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
			{Name: "parent_id", Type: ir.TypeRef{Name: "bigint"}},
		},
		Constraints: []*ir.Constraint{
			{
				Name: "fk_parent", Type: "FOREIGN KEY", Columns: []string{"parent_id"},
				Expr:        `FOREIGN KEY ("parent_id") REFERENCES "orders" ("id")`,
				NotEnforced: true,
			},
		},
	}

	var b strings.Builder
	renderObjectDPG(&b, tbl, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "NOT ENFORCED") {
		t.Errorf("rendered constraint does not carry NOT ENFORCED: %q", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := compiler.Compile([]string{f}, dir, pipeline.Default); err != nil {
		t.Fatalf("dumped table with a NOT ENFORCED FOREIGN KEY constraint failed to recompile: %v\n---\n%s", err, rendered)
	}
}

// TestRenderTemporalPrimaryKeyCompiles guards RFC Section 7.3's WITHOUT
// OVERLAPS temporal-key modifier (PostgreSQL 18+, SQL:2011): the promoted
// constraint's Expr already carries the modifier verbatim (see the
// builder's colListWithTrailingSuffix), so this table-level clause renders
// unchanged and recompiles — no special-casing needed in dump itself.
func TestRenderTemporalPrimaryKeyCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	tbl := &ir.Table{
		Schema: "public", Name: "room_bookings",
		Columns: []*ir.Column{
			{Name: "room_id", Type: ir.TypeRef{Name: "bigint"}, NotNull: true},
			{Name: "valid_at", Type: ir.TypeRef{Name: "daterange"}, NotNull: true},
		},
		Constraints: []*ir.Constraint{
			{
				Name: "no_double_booking", Type: "PRIMARY KEY", Columns: []string{"room_id", "valid_at"},
				Expr:            `PRIMARY KEY ("room_id", "valid_at" WITHOUT OVERLAPS)`,
				WithoutOverlaps: true,
			},
		},
	}

	var b strings.Builder
	renderObjectDPG(&b, tbl, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "WITHOUT OVERLAPS") {
		t.Errorf("rendered constraint does not carry WITHOUT OVERLAPS: %q", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := compiler.Compile([]string{f}, dir, pipeline.Default); err != nil {
		t.Fatalf("dumped table with a temporal PRIMARY KEY failed to recompile: %v\n---\n%s", err, rendered)
	}
}

// TestRenderTemporalForeignKeyCompiles is
// TestRenderTemporalPrimaryKeyCompiles' FOREIGN KEY/PERIOD counterpart.
func TestRenderTemporalForeignKeyCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	tbl := &ir.Table{
		Schema: "public", Name: "room_events",
		Columns: []*ir.Column{
			{Name: "room_id", Type: ir.TypeRef{Name: "bigint"}, NotNull: true},
			{Name: "valid_at", Type: ir.TypeRef{Name: "daterange"}, NotNull: true},
		},
		Constraints: []*ir.Constraint{
			{
				Name: "fk_room", Type: "FOREIGN KEY", Columns: []string{"room_id", "valid_at"},
				Expr:         `FOREIGN KEY ("room_id", PERIOD "valid_at") REFERENCES "room_bookings" ("room_id", PERIOD "valid_at")`,
				FkWithPeriod: true, PkWithPeriod: true,
			},
		},
	}

	var b strings.Builder
	renderObjectDPG(&b, tbl, fmtOpts)
	rendered := b.String()

	if strings.Count(rendered, "PERIOD") != 2 {
		t.Errorf("rendered constraint missing one or both PERIOD markers: %q", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	// room_bookings doesn't exist in this single-table fixture, so this
	// intentionally only proves the DPG-level syntax round-trips (parses
	// and rebuilds identically), not that the REFERENCES target resolves —
	// matching TestRenderForeignKeyNotEnforcedCompiles's identical
	// single-table scope just above.
	if _, _, err := compiler.Compile([]string{f}, dir, pipeline.Default); err != nil {
		t.Fatalf("dumped table with a temporal FOREIGN KEY failed to recompile: %v\n---\n%s", err, rendered)
	}
}

// TestRenderIndexUsingMethodCompiles guards a real bug found live-testing a
// demo project: the RFC's ABNF and every one of its worked examples
// (rfc/dpg-1.md Section 7.7, e.g. "idx_location USING gist (location);") place
// USING before the column list, matching real PostgreSQL's own
// CREATE INDEX ... USING method (columns) order — but the blockparser used
// to accept USING only AFTER the columns, silently rejecting the RFC's own
// syntax. This guards both halves of the round-trip: renderIndex must emit
// USING before the columns (matching the parser's corrected grammar), and
// the result must actually recompile.
func TestRenderIndexUsingMethodCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	tbl := &ir.Table{
		Schema: "public", Name: "t",
		Columns: []*ir.Column{{Name: "tags", Type: ir.TypeRef{Name: "jsonb"}}},
		Indexes: []*ir.Index{
			{Name: "t_tags_gin", Method: "gin", Columns: []pipeline.IndexColumn{{Name: "tags"}}},
		},
	}

	var b strings.Builder
	renderObjectDPG(&b, tbl, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "USING gin (") {
		t.Errorf("rendered index does not place USING before the column list: %q", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := compiler.Compile([]string{f}, dir, pipeline.Default); err != nil {
		t.Fatalf("dumped table with a USING-method index failed to recompile: %v\n---\n%s", err, rendered)
	}
}

// TestRenderIndexUniqueConcurrentlyPrefixCompiles guards the CONCURRENTLY
// grammar fix: real PostgreSQL's CONCURRENTLY is a bare presence keyword
// (CREATE [UNIQUE] INDEX [CONCURRENTLY] name), never a boolean toggle — DPG
// used to accept a trailing "CONCURRENTLY <bool>;" clause with no PG
// equivalent. renderIndex must emit both UNIQUE and CONCURRENTLY as prefix
// keywords before the name, in that order, matching real PG exactly, and
// the result must recompile.
// TestRenderForeignTableCompiles guards a real bug found live-testing a
// demo project: renderObjectDPG's *ir.Table case hardcoded the "TABLE"
// keyword regardless of Foreign, and never emitted SERVER/OPTIONS at all —
// dump would silently turn a foreign table into a (broken, unrecompilable)
// plain table declaration. Also covers Unlogged's keyword, found broken the
// same way while fixing this.
func TestRenderForeignTableCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	server := "loopback_server"
	tbl := &ir.Table{
		Schema: "public", Name: "remote_users",
		Foreign: true, ForeignServer: &server,
		ForeignOptions: []pipeline.StorageParam{
			{Key: "table_name", Value: "users"},
			{Key: "schema_name", Value: "public"},
		},
		Columns: []*ir.Column{
			{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
			{Name: "email", Type: ir.TypeRef{Name: "text"}},
		},
	}

	var b strings.Builder
	renderObjectDPG(&b, tbl, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "FOREIGN TABLE remote_users") {
		t.Errorf("rendered table does not use the FOREIGN TABLE keyword: %q", rendered)
	}
	if !strings.Contains(rendered, `SERVER loopback_server`) {
		t.Errorf("rendered table missing SERVER clause: %q", rendered)
	}
	if !strings.Contains(rendered, "OPTIONS (table_name 'users', schema_name 'public')") {
		t.Errorf("rendered table missing OPTIONS clause: %q", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped foreign table failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.Table
	for _, o := range compiled {
		if tb, ok := o.(*ir.Table); ok && tb.Name == "remote_users" {
			found = tb
		}
	}
	if found == nil {
		t.Fatal("remote_users missing after recompile")
	}
	if !found.Foreign || found.ForeignServer == nil || *found.ForeignServer != "loopback_server" {
		t.Errorf("Foreign/ForeignServer did not round-trip: Foreign=%v ForeignServer=%v", found.Foreign, found.ForeignServer)
	}
	if len(found.ForeignOptions) != 2 {
		t.Errorf("ForeignOptions did not round-trip: got %v", found.ForeignOptions)
	}
}

// TestRenderUnloggedTableCompiles guards the UNLOGGED keyword gap found
// alongside the FOREIGN TABLE one: dump's table renderer never checked
// o.Unlogged either.
func TestRenderUnloggedTableCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	tbl := &ir.Table{
		Schema: "public", Name: "sessions",
		Unlogged: true,
		Columns:  []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
	}

	var b strings.Builder
	renderObjectDPG(&b, tbl, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "UNLOGGED TABLE sessions") {
		t.Errorf("rendered table does not use the UNLOGGED TABLE keyword: %q", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped unlogged table failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.Table
	for _, o := range compiled {
		if tb, ok := o.(*ir.Table); ok && tb.Name == "sessions" {
			found = tb
		}
	}
	if found == nil || !found.Unlogged {
		t.Errorf("Unlogged did not round-trip: %+v", found)
	}
}

// TestRenderUnloggedSequenceCompiles guards RFC Section 10's UNLOGGED
// prefix on sequences, mirroring TestRenderUnloggedTableCompiles's identical
// table-side round trip.
func TestRenderUnloggedSequenceCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	seq := &ir.Sequence{Schema: "public", Name: "session_counter", Unlogged: true}

	var b strings.Builder
	renderObjectDPG(&b, seq, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "UNLOGGED SEQUENCE session_counter") {
		t.Errorf("rendered sequence does not use the UNLOGGED SEQUENCE keyword: %q", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped unlogged sequence failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.Sequence
	for _, o := range compiled {
		if s, ok := o.(*ir.Sequence); ok && s.Name == "session_counter" {
			found = s
		}
	}
	if found == nil || !found.Unlogged {
		t.Errorf("Unlogged did not round-trip: %+v", found)
	}
}

// TestRenderFunctionLeakproofTransformCompiles guards RFC audit items
// #25/#26's dump-and-recompile round trip: dump.go's Function case
// previously had no LEAKPROOF/TRANSFORM rendering at all, so a live-
// introspected function declaring either would dump into source that
// silently dropped them (recompiling to something different from the live
// object it was dumped from).
func TestRenderFunctionLeakproofTransformCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	fn := &ir.Function{
		Schema: "public", Name: "f",
		Args:       []ir.FuncArg{{Name: "x", Mode: "IN", Type: ir.TypeRef{Name: "hstore"}}},
		ReturnType: ir.TypeRef{Name: "integer"},
		Attrs: ir.FuncAttrs{
			Language: "plpython3u", Leakproof: true,
			Transforms: []ir.TypeRef{{Name: "hstore"}}, Body: "return 1",
		},
	}

	var b strings.Builder
	renderObjectDPG(&b, fn, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "LEAKPROOF") {
		t.Errorf("rendered function missing LEAKPROOF: %q", rendered)
	}
	if !strings.Contains(rendered, "TRANSFORM FOR TYPE hstore") {
		t.Errorf("rendered function missing TRANSFORM FOR TYPE: %q", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped function failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.Function
	for _, o := range compiled {
		if fo, ok := o.(*ir.Function); ok && fo.Name == "f" {
			found = fo
		}
	}
	if found == nil {
		t.Fatal("f missing after recompile")
	}
	if !found.Attrs.Leakproof {
		t.Error("Leakproof did not round-trip")
	}
	if len(found.Attrs.Transforms) != 1 || found.Attrs.Transforms[0].Name != "hstore" {
		t.Errorf("Transforms did not round-trip: got %v", found.Attrs.Transforms)
	}
}

// TestRenderFunctionObjFileCompiles guards RFC audit item #27's dump-and-
// recompile round trip for LANGUAGE C's two-string AS form — dump.go
// previously always rendered a single dollar-quoted body, which for a
// LANGUAGE C function would drop the shared-object path (ObjFile) entirely
// and misinterpret Body as an obj_file with an implicit default symbol.
func TestRenderFunctionObjFileCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	objFile, linkSymbol := "$libdir/pgcrypto", "pg_digest"
	fn := &ir.Function{
		Schema: "public", Name: "digest",
		Args: []ir.FuncArg{
			{Name: "data", Mode: "IN", Type: ir.TypeRef{Name: "text"}},
			{Name: "typ", Mode: "IN", Type: ir.TypeRef{Name: "text"}},
		},
		ReturnType: ir.TypeRef{Name: "bytea"},
		Attrs:      ir.FuncAttrs{Language: "c", ObjFile: &objFile, LinkSymbol: &linkSymbol},
	}

	var b strings.Builder
	renderObjectDPG(&b, fn, fmtOpts)
	rendered := b.String()

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped function failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.Function
	for _, o := range compiled {
		if fo, ok := o.(*ir.Function); ok && fo.Name == "digest" {
			found = fo
		}
	}
	if found == nil {
		t.Fatal("digest missing after recompile")
	}
	if found.Attrs.ObjFile == nil || *found.Attrs.ObjFile != objFile {
		t.Errorf("ObjFile did not round-trip: got %v", found.Attrs.ObjFile)
	}
	if found.Attrs.LinkSymbol == nil || *found.Attrs.LinkSymbol != linkSymbol {
		t.Errorf("LinkSymbol did not round-trip: got %v", found.Attrs.LinkSymbol)
	}
}

// TestRenderFunctionAtomicBodyCompiles guards RFC audit item #28's dump-
// and-recompile round trip: dump.go previously always rendered "AS
// $$...$$", so a live-introspected BEGIN ATOMIC function (whose Body was
// silently empty before this batch) would dump into an empty-bodied
// function that recompiles to something semantically different.
func TestRenderFunctionAtomicBodyCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	fn := &ir.Function{
		Schema: "public", Name: "f",
		Args:       []ir.FuncArg{{Name: "x", Mode: "IN", Type: ir.TypeRef{Name: "integer"}}},
		ReturnType: ir.TypeRef{Name: "integer"},
		Attrs:      ir.FuncAttrs{Language: "sql", AtomicBody: true, Body: "SELECT x + 1;"},
	}

	var b strings.Builder
	renderObjectDPG(&b, fn, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "BEGIN ATOMIC") {
		t.Errorf("rendered function missing BEGIN ATOMIC: %q", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped function failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.Function
	for _, o := range compiled {
		if fo, ok := o.(*ir.Function); ok && fo.Name == "f" {
			found = fo
		}
	}
	if found == nil {
		t.Fatal("f missing after recompile")
	}
	if !found.Attrs.AtomicBody {
		t.Error("AtomicBody did not round-trip")
	}
	if found.Attrs.Body == "" {
		t.Error("Body did not round-trip (extracted empty)")
	}
}

// TestRenderTypedTableCompiles guards RFC Section 7.1's "Form 2" typed
// table (CREATE TABLE ... OF type_name) dump-and-recompile round trip, both
// the bare form (no parenthesized list — real PostgreSQL rejects empty
// parens on this form outright) and with a table-level constraint declared.
func TestRenderTypedTableCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	bare := &ir.Table{Schema: "public", Name: "shipping_address", OfType: ir.TypeRef{Name: "address"}}
	withCst := &ir.Table{
		Schema: "public", Name: "billing_address", OfType: ir.TypeRef{Name: "address"},
		Constraints: []*ir.Constraint{
			{Name: "zip_format", Type: "CHECK", Expr: "CHECK (zip ~ '^[0-9]{5}$')"},
		},
	}

	var b strings.Builder
	renderObjectDPG(&b, bare, fmtOpts)
	renderObjectDPG(&b, withCst, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "TABLE shipping_address OF address") {
		t.Errorf("expected bare OF-type rendering with no parens, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "shipping_address OF address (") {
		t.Errorf("bare typed table must not render empty parens, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "TABLE billing_address OF address (") {
		t.Errorf("expected OF-type rendering with a parenthesized constraint list, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped typed tables failed to recompile: %v\n---\n%s", err, rendered)
	}
	found := map[string]*ir.Table{}
	for _, o := range compiled {
		if tb, ok := o.(*ir.Table); ok {
			found[tb.Name] = tb
		}
	}
	if found["shipping_address"] == nil || found["shipping_address"].OfType.Name != "address" {
		t.Errorf("shipping_address OfType did not round-trip: %+v", found["shipping_address"])
	}
	if found["billing_address"] == nil || len(found["billing_address"].Constraints) != 1 {
		t.Errorf("billing_address constraint did not round-trip: %+v", found["billing_address"])
	}
}

// TestRenderTableAccessMethodCompiles guards RFC Section 7.1's USING method
// (table access method) dump-and-recompile round trip.
func TestRenderTableAccessMethodCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	tbl := &ir.Table{
		Schema: "public", Name: "events", AccessMethod: "heap2",
		Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
	}

	var b strings.Builder
	renderObjectDPG(&b, tbl, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "USING heap2") {
		t.Errorf("expected USING heap2, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped table failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.Table
	for _, o := range compiled {
		if tb, ok := o.(*ir.Table); ok && tb.Name == "events" {
			found = tb
		}
	}
	if found == nil || found.AccessMethod != "heap2" {
		t.Errorf("AccessMethod did not round-trip: %+v", found)
	}
}

// TestRenderTableRLSForcedCompiles guards a real bug: RLSForced (Section
// 7.11's FORCE ROW LEVEL SECURITY, set from real applied state — see
// mergeTableBlock/diffTable) was never rendered by renderObjectDPG at all,
// so a `dpg dump` of a table with RLS forced silently dropped that
// directive, and re-applying the dumped source would have emitted a
// spurious NO FORCE ROW LEVEL SECURITY on the next plan.
func TestRenderTableRLSForcedCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	tbl := &ir.Table{
		Schema: "public", Name: "events", RLSEnabled: true, RLSForced: true,
		Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
	}

	var b strings.Builder
	renderObjectDPG(&b, tbl, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "FORCE ROW LEVEL SECURITY;") {
		t.Errorf("expected FORCE ROW LEVEL SECURITY, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped table failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.Table
	for _, o := range compiled {
		if tb, ok := o.(*ir.Table); ok && tb.Name == "events" {
			found = tb
		}
	}
	if found == nil || !found.RLSForced {
		t.Errorf("RLSForced did not round-trip: %+v", found)
	}
}

// TestRenderTableInheritsPartitionByUsingWithOrderingCompiles guards a real
// bug: renderObjectDPG previously wrote INHERITS after Foreign/Tablespace and
// USING before PARTITION BY — real PostgreSQL's own fixed CREATE TABLE
// clause order is INHERITS, then PARTITION BY, then USING, then WITH (...),
// then TABLESPACE (confirmed live via pg_query.Parse: the reverse ordering of
// USING/PARTITION BY is an outright syntax error), so a `dpg dump` of a table
// declaring more than one of these together produced DPG source that failed
// to recompile.
func TestRenderTableInheritsPartitionByUsingWithOrderingCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	tbl := &ir.Table{
		Schema: "public", Name: "events", AccessMethod: "heap2",
		Columns:       []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}, {Name: "created_at", Type: ir.TypeRef{Name: "date"}}},
		PartitionBy:   &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"created_at"}},
		StorageParams: []pipeline.StorageParam{{Key: "fillfactor", Value: "70"}},
	}

	var b strings.Builder
	renderObjectDPG(&b, tbl, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "PARTITION BY RANGE (created_at) USING heap2 WITH (fillfactor=70)") {
		t.Errorf("expected PARTITION BY before USING before WITH, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped table failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.Table
	for _, o := range compiled {
		if tb, ok := o.(*ir.Table); ok && tb.Name == "events" {
			found = tb
		}
	}
	if found == nil || found.AccessMethod != "heap2" || found.PartitionBy == nil ||
		len(found.StorageParams) != 1 || found.StorageParams[0].Key != "fillfactor" {
		t.Errorf("AccessMethod/PartitionBy/StorageParams did not round-trip: %+v", found)
	}
}

// TestRenderTableAndIndexTablespaceCompiles guards a real bug found live-
// testing a demo project: case *ir.Table in renderObjectDPG had no
// TABLESPACE rendering at all, table-level or per-index, despite both being
// fully parsed and (after this fix) diffed — a table's or index's declared
// TABLESPACE silently vanished on dump.
func TestRenderTableAndIndexTablespaceCompiles(t *testing.T) {
	strPtr := func(s string) *string { return &s }
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	tbl := &ir.Table{
		Schema: "public", Name: "archive",
		Columns:    []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
		Tablespace: strPtr("archive_space"),
		Indexes: []*ir.Index{
			{Name: "archive_id_idx", Columns: []pipeline.IndexColumn{{Name: "id"}}, Tablespace: strPtr("archive_space")},
		},
	}

	var b strings.Builder
	renderObjectDPG(&b, tbl, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "TABLESPACE archive_space") {
		t.Errorf("rendered table missing table-level TABLESPACE, got:\n%s", rendered)
	}
	if strings.Count(rendered, "TABLESPACE archive_space") != 2 {
		t.Errorf("expected both table-level and index-level TABLESPACE, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped table/index tablespace failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.Table
	for _, o := range compiled {
		if tb, ok := o.(*ir.Table); ok && tb.Name == "archive" {
			found = tb
		}
	}
	if found == nil {
		t.Fatalf("table archive missing after recompile\n---\n%s", rendered)
	}
	if found.Tablespace == nil || *found.Tablespace != "archive_space" {
		t.Errorf("Table.Tablespace did not round-trip: %v", found.Tablespace)
	}
	if len(found.Indexes) != 1 || found.Indexes[0].Tablespace == nil || *found.Indexes[0].Tablespace != "archive_space" {
		t.Errorf("Index.Tablespace did not round-trip: %+v", found.Indexes)
	}
}

// TestRenderVirtualTypeCompiles guards a real bug found live-testing a demo
// project: renderObjectDPG had no case at all for *ir.VirtualType — a
// declared VIRTUAL TYPE silently vanished from dump output entirely. Doubly
// consequential for this kind: since virtual types are DPG-native (RFC
// Section 5.6, no backing CREATE TYPE), running `dpg dump` against a project that
// already had one declared didn't just fail to render it — with nothing in
// the live catalog for introspection to find, `dpg dump` overwrote the
// project's own snapshot with zero virtual_type entries, a genuine, silent
// loss of the structural type info the snapshot exists to preserve
// (confirmed live: a project with 2 declared virtual types had 0 left
// immediately after a dump run). See mergeExistingVirtualTypes for the
// other half of that fix (recovering the declarations pre-overwrite); this
// test covers the render path that fix depends on to be worth anything.
func TestRenderVirtualTypeCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	comment := "polymorphic payment method blob"
	vt := &ir.VirtualType{
		Schema: "public", Name: "payment",
		Body: ir.VtypeUnion{Members: []ir.VtypeBody{
			ir.VtypeComposite{Fields: []ir.VtypeField{
				{Name: "kind", Type: ir.VtypeTypeRef{Name: "text"}},
				{Name: "amount", Type: ir.VtypeTypeRef{Name: "numeric"}},
			}},
			ir.VtypeComposite{Fields: []ir.VtypeField{
				{Name: "kind", Type: ir.VtypeTypeRef{Name: "text"}},
				{Name: "token", Type: ir.VtypeTypeRef{Name: "text"}},
			}},
			ir.VtypeTypeRef{Name: "text"},
		}},
		JsonFormat: "json",
		Comment:    &comment,
	}

	var b strings.Builder
	renderObjectDPG(&b, vt, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "VIRTUAL TYPE payment AS") {
		t.Errorf("expected VIRTUAL TYPE declaration, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "PREFERRED JSON FORMAT json;") {
		t.Errorf("expected PREFERRED JSON FORMAT, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "COMMENT 'polymorphic payment method blob';") {
		t.Errorf("expected COMMENT, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped virtual type failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.VirtualType
	for _, o := range compiled {
		if v, ok := o.(*ir.VirtualType); ok && v.Name == "payment" {
			found = v
		}
	}
	if found == nil {
		t.Fatalf("virtual type payment missing after recompile\n---\n%s", rendered)
	}
	union, ok := found.Body.(ir.VtypeUnion)
	if !ok || len(union.Members) != 3 {
		t.Errorf("Body did not round-trip as a 3-member union: %+v", found.Body)
	}
	if found.JsonFormat != "json" {
		t.Errorf("JsonFormat did not round-trip: %v", found.JsonFormat)
	}
	if found.Comment == nil || *found.Comment != comment {
		t.Errorf("Comment did not round-trip: %v", found.Comment)
	}
}

// TestRenderDomainCompiles guards RFC Section 5.4's structured domain rendering:
// case "DOMAIN" previously rendered the entire domain (base type, DEFAULT,
// NOT NULL, every CHECK) as one opaque Body string crammed inline after AS
// — not just a cosmetically different shape from the RFC's own worked
// example, but one that couldn't round-trip through diffType's new
// property-level diffing (an inline blob recompiles back into Body, never
// into DomainDefault/DomainConstraints/etc., so a dumped-then-reapplied
// domain would silently lose all future per-property diffing). Now renders
// via the RFC Section 5.4 block form directly.
func TestRenderDomainCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	def := "1"
	comment := "must be positive"
	dt := &ir.Type{
		Schema: "public", Name: "positive_integer", Variant: "DOMAIN",
		DomainBaseType:  ir.TypeRef{Name: "text"},
		DomainCollation: "en_US",
		DomainDefault:   &def,
		DomainNotNull:   true,
		DomainConstraints: []*ir.Constraint{
			{Name: "positive_only", Type: "CHECK", Expr: "CHECK (VALUE > 0)"},
		},
		Comment: &comment,
	}

	var b strings.Builder
	renderObjectDPG(&b, dt, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, `DOMAIN positive_integer AS text COLLATE "en_US" {`) {
		t.Errorf("expected DOMAIN ... AS text COLLATE \"en_US\" { block, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "DEFAULT 1;") {
		t.Errorf("expected DEFAULT directive, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "NOT NULL;") {
		t.Errorf("expected NOT NULL directive, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, `CONSTRAINT positive_only CHECK (VALUE > 0);`) {
		t.Errorf("expected CONSTRAINT directive, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped domain failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.Type
	for _, o := range compiled {
		if tp, ok := o.(*ir.Type); ok && tp.Name == "positive_integer" {
			found = tp
		}
	}
	if found == nil {
		t.Fatalf("domain positive_integer missing after recompile\n---\n%s", rendered)
	}
	if found.DomainBaseType.Name != "text" {
		t.Errorf("DomainBaseType did not round-trip: %v", found.DomainBaseType)
	}
	if found.DomainCollation != "en_US" {
		t.Errorf("DomainCollation did not round-trip: %v", found.DomainCollation)
	}
	if found.DomainDefault == nil || *found.DomainDefault != "1" {
		t.Errorf("DomainDefault did not round-trip: %v", found.DomainDefault)
	}
	if !found.DomainNotNull {
		t.Error("DomainNotNull did not round-trip")
	}
	if len(found.DomainConstraints) != 1 || found.DomainConstraints[0].Name != "positive_only" {
		t.Errorf("DomainConstraints did not round-trip: %+v", found.DomainConstraints)
	}
	if found.Comment == nil || *found.Comment != comment {
		t.Errorf("Comment did not round-trip: %v", found.Comment)
	}
}

// TestMergeExistingVirtualTypesRecovers guards the other half of the same
// bug TestRenderVirtualTypeCompiles covers: virtual types are DPG-native, so
// introspector.Introspect can never find one — mergeExistingVirtualTypes is
// what recovers them from the project's own current source before dump
// overwrites it. Also covers the two edge cases: no source files at all
// (brand-new project bootstrap, nothing to recover — not an error), and
// source that fails to recompile (surfaced as a warning, not fatal to dump).
func TestMergeExistingVirtualTypesRecovers(t *testing.T) {
	dir := t.TempDir()
	src := "VIRTUAL TYPE line_item AS (sku text, quantity integer);\n"
	f := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	db := &project.Database{Dir: dir, SourceFiles: []string{f}}
	objs := mergeExistingVirtualTypes(db)
	if len(objs) != 1 {
		t.Fatalf("expected 1 recovered virtual type, got %d: %+v", len(objs), objs)
	}
	vt, ok := objs[0].(*ir.VirtualType)
	if !ok || vt.Name != "line_item" {
		t.Errorf("expected VirtualType line_item, got %+v", objs[0])
	}
}

func TestMergeExistingVirtualTypesNoSourceFiles(t *testing.T) {
	db := &project.Database{Dir: t.TempDir()}
	if objs := mergeExistingVirtualTypes(db); objs != nil {
		t.Errorf("expected nil for a project with no source files, got %+v", objs)
	}
}

func TestMergeExistingVirtualTypesBrokenSourceIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(f, []byte("TABLE ( totally not valid dpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	db := &project.Database{Dir: dir, SourceFiles: []string{f}}
	if objs := mergeExistingVirtualTypes(db); objs != nil {
		t.Errorf("expected nil (warning, not panic/error) for unparseable source, got %+v", objs)
	}
}

// TestRenderTableGrantsAndRevocationsCompiles guards a real bug found live-
// testing REVOCATIONS against a demo project: case *ir.Table in
// renderObjectDPG had NO Grants/Revocations rendering at all — neither
// table-level nor per-column — even though both are fully diffed and applied
// correctly. A dumped table silently lost every GRANT/REVOCATION declaration,
// so a dumped project could never detect drift on them. Uses the flat Mode B
// GRANT/REVOCATION style (matching writeViewBlock's existing convention),
// both at the table level and nested inside a per-column COLUMNS entry —
// mirroring the RFC Section 7.5 worked example's "COLUMN ssn { GRANTS {...}
// REVOCATIONS {...} }" shape.
func TestRenderTableGrantsAndRevocationsCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	tbl := &ir.Table{
		Schema: "public", Name: "users",
		Columns: []*ir.Column{
			{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
			{
				Name: "email", Type: ir.TypeRef{Name: "text"},
				Grants:      []ir.Grant{{Privileges: []string{"SELECT"}, Roles: []string{"app_service"}}},
				Revocations: []ir.Revocation{{Privileges: nil, Roles: []string{"PUBLIC"}}},
			},
		},
		Grants:      []ir.Grant{{Privileges: []string{"SELECT"}, Roles: []string{"app_service"}}},
		Revocations: []ir.Revocation{{Privileges: nil, Roles: []string{"PUBLIC"}}},
	}

	var b strings.Builder
	renderObjectDPG(&b, tbl, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "GRANT SELECT TO app_service;") {
		t.Errorf("expected table-level GRANT, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "REVOCATION ALL FROM PUBLIC;") {
		t.Errorf("expected table-level REVOCATION, got:\n%s", rendered)
	}
	if strings.Count(rendered, "GRANT SELECT TO app_service;") != 2 {
		t.Errorf("expected both table-level and column-level GRANT, got:\n%s", rendered)
	}
	if strings.Count(rendered, "REVOCATION ALL FROM PUBLIC;") != 2 {
		t.Errorf("expected both table-level and column-level REVOCATION, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped table grants/revocations failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.Table
	for _, o := range compiled {
		if tb, ok := o.(*ir.Table); ok && tb.Name == "users" {
			found = tb
		}
	}
	if found == nil {
		t.Fatalf("table users missing after recompile\n---\n%s", rendered)
	}
	if len(found.Grants) != 1 || found.Grants[0].Roles[0] != "app_service" {
		t.Errorf("table Grants did not round-trip: %+v", found.Grants)
	}
	if len(found.Revocations) != 1 || found.Revocations[0].Roles[0] != "PUBLIC" {
		t.Errorf("table Revocations did not round-trip: %+v", found.Revocations)
	}
	var emailCol *ir.Column
	for _, c := range found.Columns {
		if c.Name == "email" {
			emailCol = c
		}
	}
	if emailCol == nil {
		t.Fatal("email column missing after recompile")
	}
	if len(emailCol.Grants) != 1 || emailCol.Grants[0].Roles[0] != "app_service" {
		t.Errorf("column Grants did not round-trip: %+v", emailCol.Grants)
	}
	if len(emailCol.Revocations) != 1 || emailCol.Revocations[0].Roles[0] != "PUBLIC" {
		t.Errorf("column Revocations did not round-trip: %+v", emailCol.Revocations)
	}
}

// TestRenderSubPartitionedTableCompiles guards a real, pre-existing gap
// found while implementing RFC Section 7.13 sub-partitioning: dump had NO support
// for rendering PARTITION BY or PARTITIONS { } at all (not even the flat,
// non-nested case) — a dumped partitioned table silently lost its entire
// partition structure. This exercises both the top-level PARTITION BY clause
// and a nested sub-partition (a partition with its own PARTITION BY and
// PARTITIONS block), round-tripped through the real compiler.
func TestRenderSubPartitionedTableCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	tbl := &ir.Table{
		Schema: "public", Name: "metrics",
		Columns: []*ir.Column{
			{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
			{Name: "created_at", Type: ir.TypeRef{Name: "timestamptz"}, NotNull: true},
			{Name: "channel", Type: ir.TypeRef{Name: "text"}, NotNull: true},
		},
		PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"created_at"}},
		Partitions: []*ir.Partition{
			{Name: "metrics_2025", Bounds: "FOR VALUES FROM ('2025-01-01') TO ('2026-01-01')"},
			{
				Name:        "metrics_2026",
				Bounds:      "FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')",
				PartitionBy: &ir.PartitionSpec{Strategy: "LIST", Columns: []string{"channel"}},
				Partitions: []*ir.Partition{
					{Name: "metrics_2026_web", Bounds: "FOR VALUES IN ('web')"},
					{Name: "metrics_2026_other", Bounds: "FOR VALUES IN ('mobile', 'api')"},
				},
			},
		},
	}

	var b strings.Builder
	renderObjectDPG(&b, tbl, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "PARTITION BY RANGE (created_at)") {
		t.Errorf("rendered table missing top-level PARTITION BY: %q", rendered)
	}
	if !strings.Contains(rendered, "PARTITION BY LIST (channel)") {
		t.Errorf("rendered table missing nested PARTITION BY: %q", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped sub-partitioned table failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.Table
	for _, o := range compiled {
		if tb, ok := o.(*ir.Table); ok && tb.Name == "metrics" {
			found = tb
		}
	}
	if found == nil {
		t.Fatalf("table metrics missing after recompile\n---\n%s", rendered)
	}
	if found.PartitionBy == nil || found.PartitionBy.Strategy != "RANGE" {
		t.Errorf("top-level PartitionBy did not round-trip: %+v", found.PartitionBy)
	}
	if len(found.Partitions) != 2 {
		t.Fatalf("expected 2 partitions after round-trip, got %d\n---\n%s", len(found.Partitions), rendered)
	}
	var sub *ir.Partition
	for _, p := range found.Partitions {
		if p.Name == "metrics_2026" {
			sub = p
		}
	}
	if sub == nil {
		t.Fatal("metrics_2026 partition missing after round-trip")
	}
	if sub.PartitionBy == nil || sub.PartitionBy.Strategy != "LIST" {
		t.Fatalf("metrics_2026 sub-partition PartitionBy did not round-trip: %+v", sub.PartitionBy)
	}
	if len(sub.Partitions) != 2 {
		t.Fatalf("expected 2 nested sub-partitions after round-trip, got %d", len(sub.Partitions))
	}
}

// TestRenderPartitionLocalConstraintCompiles guards RFC Section 7.3's "DROP
// CONSTRAINT ... ONLY" gap (PostgreSQL 18+): a partition's own local
// constraint (ir.Partition.Constraints) must round-trip through dump ->
// recompile, including alongside a sub-partitioned partition's own nested
// PARTITIONS { } block.
func TestRenderPartitionLocalConstraintCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	tbl := &ir.Table{
		Schema: "public", Name: "orders",
		Columns: []*ir.Column{
			{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
			{Name: "amount", Type: ir.TypeRef{Name: "numeric"}, NotNull: true},
		},
		PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"id"}},
		Partitions: []*ir.Partition{
			{
				Name:   "orders_1",
				Bounds: "FOR VALUES FROM (1) TO (1000)",
				Constraints: []*ir.Constraint{
					{Name: "ck_amount", Type: "CHECK", Expr: "CHECK (amount > 0)"},
				},
			},
			{
				Name:        "orders_2",
				Bounds:      "FOR VALUES FROM (1000) TO (2000)",
				PartitionBy: &ir.PartitionSpec{Strategy: "LIST", Columns: []string{"amount"}},
				Partitions: []*ir.Partition{
					{Name: "orders_2_small", Bounds: "FOR VALUES IN ('1')"},
				},
				Constraints: []*ir.Constraint{
					{Name: "ck_amount_valid", Type: "CHECK", Expr: "CHECK (amount > 0)", NotValid: true},
				},
			},
		},
	}

	var b strings.Builder
	renderObjectDPG(&b, tbl, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, `CONSTRAINT ck_amount CHECK (amount > 0)`) {
		t.Errorf("rendered table missing partition-local constraint: %q", rendered)
	}
	if !strings.Contains(rendered, "NOT VALID") {
		t.Errorf("rendered table missing NOT VALID on second partition's constraint: %q", rendered)
	}
	if !strings.Contains(rendered, "PARTITIONS {") {
		t.Errorf("rendered table missing nested PARTITIONS alongside a constraint: %q", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped table with partition-local constraints failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.Table
	for _, o := range compiled {
		if tb, ok := o.(*ir.Table); ok && tb.Name == "orders" {
			found = tb
		}
	}
	if found == nil {
		t.Fatalf("table orders missing after recompile\n---\n%s", rendered)
	}
	if len(found.Partitions) != 2 {
		t.Fatalf("expected 2 partitions after round-trip, got %d", len(found.Partitions))
	}
	p1 := found.Partitions[0]
	if len(p1.Constraints) != 1 || p1.Constraints[0].Name != "ck_amount" {
		t.Fatalf("orders_1 constraint did not round-trip: %+v", p1.Constraints)
	}
	p2 := found.Partitions[1]
	if len(p2.Constraints) != 1 || !p2.Constraints[0].NotValid {
		t.Fatalf("orders_2 NOT VALID constraint did not round-trip: %+v", p2.Constraints)
	}
	if p2.PartitionBy == nil || len(p2.Partitions) != 1 {
		t.Fatalf("orders_2 sub-partitioning did not round-trip alongside its constraint: %+v", p2)
	}
}

// TestRenderMaterializedViewCompiles guards a real bug found live-testing a
// demo project: renderObjectDPG's View case always emitted the bare "VIEW"
// keyword regardless of o.Materialized, and never rendered Owner, Comment,
// Grants, Revocations, or WITH NO DATA at all — a live MATERIALIZED VIEW
// round-tripped through dump as a plain VIEW, silently losing the object
// kind (confirmed against a real demo database: introspection correctly set
// Materialized=true, but the rendered source dropped it entirely).
func TestRenderMaterializedViewCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	owner := "app_role"
	comment := "precomputed totals"
	mv := &ir.View{
		Schema:       "public",
		Name:         "order_status_totals",
		Materialized: true,
		Query:        "SELECT status, count(*) AS c FROM orders GROUP BY status",
		Owner:        &owner,
		Comment:      &comment,
		Grants:       []ir.Grant{{Privileges: []string{"SELECT"}, Roles: []string{"app_readonly"}}},
		Revocations:  []ir.Revocation{{Privileges: []string{"SELECT"}, Roles: []string{"PUBLIC"}}},
		Indexes: []*ir.Index{
			{Name: "order_status_totals_status_uq", Unique: true, Columns: []pipeline.IndexColumn{{Name: "status"}}},
		},
	}

	var b strings.Builder
	renderObjectDPG(&b, mv, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "MATERIALIZED VIEW order_status_totals") {
		t.Errorf("rendered materialized view does not use the MATERIALIZED VIEW keyword: %q", rendered)
	}
	if !strings.Contains(rendered, "OWNER app_role") {
		t.Errorf("rendered materialized view missing OWNER: %q", rendered)
	}
	if !strings.Contains(rendered, "GRANT SELECT TO app_readonly") {
		t.Errorf("rendered materialized view missing GRANT: %q", rendered)
	}
	if !strings.Contains(rendered, "REVOCATION SELECT FROM PUBLIC") {
		t.Errorf("rendered materialized view missing REVOCATION (RFC Section 11.3 Mode B singular keyword — not REVOKE, which the block parser doesn't recognize): %q", rendered)
	}
	if !strings.Contains(rendered, "INDICES") || !strings.Contains(rendered, "order_status_totals_status_uq") {
		t.Errorf("rendered materialized view missing INDICES block: %q", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped materialized view failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.View
	for _, o := range compiled {
		if v, ok := o.(*ir.View); ok && v.Name == "order_status_totals" {
			found = v
		}
	}
	if found == nil {
		t.Fatalf("view order_status_totals missing after recompile\n---\n%s", rendered)
	}
	if !found.Materialized {
		t.Error("Materialized did not round-trip")
	}
	if found.Owner == nil || *found.Owner != "app_role" {
		t.Errorf("Owner did not round-trip: %v", found.Owner)
	}
	if found.Comment == nil || *found.Comment != "precomputed totals" {
		t.Errorf("Comment did not round-trip: %v", found.Comment)
	}
	if len(found.Grants) != 1 {
		t.Errorf("Grants did not round-trip: %v", found.Grants)
	}
	if len(found.Revocations) != 1 || found.Revocations[0].Roles[0] != "PUBLIC" {
		t.Errorf("Revocations did not round-trip: %v", found.Revocations)
	}
	if len(found.Indexes) != 1 || found.Indexes[0].Name != "order_status_totals_status_uq" || !found.Indexes[0].Unique {
		t.Errorf("Indexes did not round-trip: %v", found.Indexes)
	}
}

// TestRenderRecursiveViewRendersAsPlainViewCompiles guards a self-recursive
// view's dump output: it must NOT reuse the special "RECURSIVE VIEW (cols)
// AS ..." PG syntax, because that requires an explicit column list DPG
// doesn't track and dump only ever sees introspected objects, whose Query
// (from pg_get_viewdef) already comes back as a self-contained "WITH
// RECURSIVE ..." CTE — which is already valid and self-recursive under a
// plain CREATE VIEW (the same reconstruction pg_dump itself uses). Emitting
// "RECURSIVE VIEW" with that query shape is a PG syntax error.
func TestRenderRecursiveViewRendersAsPlainViewCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	v := &ir.View{
		Schema:    "public",
		Name:      "category_tree",
		Recursive: true,
		Query: "WITH RECURSIVE category_tree AS (" +
			"SELECT id FROM categories WHERE parent_id IS NULL " +
			"UNION ALL " +
			"SELECT c.id FROM categories c JOIN category_tree t ON c.parent_id = t.id" +
			") SELECT id FROM category_tree",
	}

	var b strings.Builder
	renderObjectDPG(&b, v, fmtOpts)
	rendered := b.String()

	if strings.Contains(rendered, "RECURSIVE VIEW") {
		t.Errorf("rendered view must not use the RECURSIVE VIEW keyword (query already self-contains WITH RECURSIVE): %q", rendered)
	}
	if !strings.Contains(rendered, "VIEW category_tree AS WITH RECURSIVE") {
		t.Errorf("rendered view should be a plain VIEW wrapping the WITH RECURSIVE query: %q", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped recursive view failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.View
	for _, o := range compiled {
		if vv, ok := o.(*ir.View); ok && vv.Name == "category_tree" {
			found = vv
		}
	}
	if found == nil {
		t.Fatalf("view category_tree missing after recompile\n---\n%s", rendered)
	}
}

func TestRenderIndexUniqueConcurrentlyPrefixCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	tbl := &ir.Table{
		Schema: "public", Name: "t",
		Columns: []*ir.Column{{Name: "email", Type: ir.TypeRef{Name: "text"}}},
		Indexes: []*ir.Index{
			{Name: "t_email_uc", Unique: true, Concurrently: true, Columns: []pipeline.IndexColumn{{Name: "email"}}},
		},
	}

	var b strings.Builder
	renderObjectDPG(&b, tbl, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "UNIQUE CONCURRENTLY t_email_uc (") {
		t.Errorf("rendered index does not place UNIQUE then CONCURRENTLY before the name: %q", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped table with a UNIQUE CONCURRENTLY index failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.Index
	for _, o := range compiled {
		if tb, ok := o.(*ir.Table); ok {
			for _, idx := range tb.Indexes {
				if idx.Name == "t_email_uc" {
					found = idx
				}
			}
		}
	}
	if found == nil {
		t.Fatal("index t_email_uc missing after recompile")
	}
	if !found.Unique || !found.Concurrently {
		t.Errorf("Unique/Concurrently did not round-trip: got Unique=%v Concurrently=%v", found.Unique, found.Concurrently)
	}
}

func TestObjectSchema(t *testing.T) {
	cases := []struct {
		obj  pipeline.IRObject
		want string
	}{
		{&ir.Table{Schema: "public"}, "public"},
		{&ir.View{Schema: "reports"}, "reports"},
		{&ir.Function{Schema: "util"}, "util"},
		{&ir.Procedure{Schema: "util"}, "util"},
		{&ir.Aggregate{Schema: "stats"}, "stats"},
		{&ir.Type{Schema: "domain"}, "domain"},
		{&ir.Sequence{Schema: "public"}, "public"},
		{&ir.Role{}, ""},
		{&ir.Schema{}, ""},
	}
	for _, tc := range cases {
		got := objectSchema(tc.obj)
		if got != tc.want {
			t.Errorf("objectSchema(%T) = %q, want %q", tc.obj, got, tc.want)
		}
	}
}

// TestRenderOpaqueObjectsCompile guards the no-verb mandate: dump renders
// introspected opaque objects (whose Body is a canonical "CREATE …" statement)
// as DPG declarations, which MUST NOT begin with CREATE and MUST re-compile.
func TestRenderOpaqueObjectsCompile(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}

	objs := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "t", Columns: []*ir.Column{
			{Name: "a", Type: ir.TypeRef{Name: "int4"}},
			{Name: "b", Type: ir.TypeRef{Name: "int4"}},
		}},
		&ir.ForeignDataWrapper{Name: "dummy_fdw", Body: "CREATE FOREIGN DATA WRAPPER dummy_fdw"},
		&ir.ForeignServer{Name: "dummy_srv", Body: "CREATE SERVER dummy_srv FOREIGN DATA WRAPPER dummy_fdw"},
		&ir.UserMapping{Server: "dummy_srv", Body: "CREATE USER MAPPING FOR PUBLIC SERVER dummy_srv"},
		&ir.Publication{Name: "my_pub", Body: "CREATE PUBLICATION my_pub FOR ALL TABLES"},
		&ir.Subscription{Name: "my_sub", Body: "CREATE SUBSCRIPTION my_sub CONNECTION 'host=x' PUBLICATION my_pub"},
		&ir.EventTrigger{Name: "et", Body: "CREATE EVENT TRIGGER et ON ddl_command_start EXECUTE FUNCTION f()"},
		&ir.Collation{Schema: "public", Name: "my_coll", Body: "CREATE COLLATION public.my_coll (locale = 'C')"},
		&ir.Cast{SourceType: ir.TypeRef{Name: "int4"}, TargetType: ir.TypeRef{Name: "bool"},
			Body: "CREATE CAST (integer AS boolean) WITHOUT FUNCTION"},
		&ir.StatisticsObject{Schema: "public", Name: "st",
			Body: "CREATE STATISTICS public.st (dependencies) ON a, b FROM public.t"},
		&ir.Tablespace{Name: "ts", Body: "CREATE TABLESPACE ts LOCATION '/tmp/x'"},
		&ir.Operator{Schema: "public", Name: "===",
			Body: "CREATE OPERATOR public.=== (FUNCTION = int4eq, LEFTARG = integer, RIGHTARG = integer)"},
		&ir.OperatorClass{Schema: "public", Name: "my_opc", AccessMethod: "btree",
			Body: "CREATE OPERATOR CLASS public.my_opc FOR TYPE integer USING btree AS OPERATOR 3 =, FUNCTION 1 btint4cmp(integer, integer)"},
		&ir.OperatorFamily{Schema: "public", Name: "my_fam", AccessMethod: "btree",
			Body: "CREATE OPERATOR FAMILY public.my_fam USING btree"},
		&ir.TSConfig{Schema: "public", Name: "my_cfg",
			Body: `CREATE TEXT SEARCH CONFIGURATION public.my_cfg (PARSER = pg_catalog."default")`},
		&ir.TSDict{Schema: "public", Name: "my_dict",
			Body: "CREATE TEXT SEARCH DICTIONARY public.my_dict (TEMPLATE = pg_catalog.simple)"},
		&ir.TSParser{Schema: "public", Name: "my_prs",
			Body: "CREATE TEXT SEARCH PARSER public.my_prs (START = prsd_start, GETTOKEN = prsd_nexttoken, END = prsd_end, LEXTYPES = prsd_lextype)"},
		&ir.TSTemplate{Schema: "public", Name: "my_tmpl",
			Body: "CREATE TEXT SEARCH TEMPLATE public.my_tmpl (LEXIZE = dsimple_lexize)"},
	}

	var b strings.Builder
	for _, o := range objs {
		renderObjectDPG(&b, o, fmtOpts)
	}
	rendered := b.String()

	// The no-verb mandate: no declaration may begin with CREATE.
	for line := range strings.SplitSeq(rendered, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "CREATE ") {
			t.Errorf("rendered declaration begins with CREATE (violates no-verb mandate): %q", line)
		}
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped .dpg failed to recompile: %v\n---\n%s", err, rendered)
	}

	// Every opaque object must survive the round-trip.
	wantKinds := map[string]bool{
		"*ir.ForeignDataWrapper": false, "*ir.ForeignServer": false, "*ir.UserMapping": false,
		"*ir.Publication": false, "*ir.Subscription": false, "*ir.EventTrigger": false, "*ir.Collation": false,
		"*ir.Cast": false, "*ir.StatisticsObject": false, "*ir.Tablespace": false,
		"*ir.Operator": false, "*ir.OperatorClass": false, "*ir.OperatorFamily": false,
		"*ir.TSConfig": false, "*ir.TSDict": false, "*ir.TSParser": false, "*ir.TSTemplate": false,
	}
	for _, o := range compiled {
		k := reflect.TypeOf(o).String()
		if _, ok := wantKinds[k]; ok {
			wantKinds[k] = true
		}
	}
	for k, seen := range wantKinds {
		if !seen {
			t.Errorf("kind %s missing after recompile", k)
		}
	}

	// Every opaque object must carry a Body after recompile — an empty one aborts
	// createOpaque ("body not captured") and would break plan/apply for the whole
	// database. The differ's create path is the direct guard: running it against
	// an empty snapshot exercises createOpaque for each object.
	if _, err := diff.New().Diff(compiled, &pipeline.Snapshot{}); err != nil {
		t.Fatalf("diffing dumped opaque objects failed (empty body?): %v", err)
	}
}

// TestRenderOpaqueObjectsWithCommentCompile guards a real bug found live-
// testing a demo project: renderOpaqueBody unconditionally terminated every
// opaque declaration with a bare ";", regardless of the object's Comment
// field — so a Comment correctly populated by introspection (see
// internal/introspect/opaque.go) was silently dropped by dump, never even
// reaching a "{ COMMENT ...; }" block, though the analogous FUNCTION/
// PROCEDURE path (writeFuncBlock) already did this correctly. This is
// TestRenderOpaqueObjectsCompile's exact object set, but with Comment set on
// every kind that has the field (all but UserMapping/Publication, which
// don't support COMMENT ON in PostgreSQL) — the prior test's all-nil
// Comments never exercised the "{ }" branch at all, which is exactly how
// this gap went unnoticed.
func TestRenderOpaqueObjectsWithCommentCompile(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	c := func(s string) *string { return &s }

	objs := []pipeline.IRObject{
		&ir.ForeignDataWrapper{Name: "dummy_fdw", Body: "CREATE FOREIGN DATA WRAPPER dummy_fdw", Comment: c("fdw comment")},
		&ir.ForeignServer{Name: "dummy_srv", Body: "CREATE SERVER dummy_srv FOREIGN DATA WRAPPER dummy_fdw", Comment: c("srv comment")},
		&ir.Subscription{Name: "my_sub", Body: "CREATE SUBSCRIPTION my_sub CONNECTION 'host=x' PUBLICATION my_pub", Comment: c("sub comment")},
		&ir.EventTrigger{Name: "et", Body: "CREATE EVENT TRIGGER et ON ddl_command_start EXECUTE FUNCTION f()", Comment: c("et comment")},
		&ir.Collation{Schema: "public", Name: "my_coll", Body: "CREATE COLLATION public.my_coll (locale = 'C')", Comment: c("coll comment")},
		&ir.Cast{SourceType: ir.TypeRef{Name: "int4"}, TargetType: ir.TypeRef{Name: "bool"},
			Body: "CREATE CAST (integer AS boolean) WITHOUT FUNCTION", Comment: c("cast comment")},
		&ir.StatisticsObject{Schema: "public", Name: "st",
			Body: "CREATE STATISTICS public.st (dependencies) ON a, b FROM public.t", Comment: c("stats comment")},
		&ir.Tablespace{Name: "ts", Body: "CREATE TABLESPACE ts LOCATION '/tmp/x'", Comment: c("ts comment")},
		&ir.Operator{Schema: "public", Name: "===",
			Body: "CREATE OPERATOR public.=== (FUNCTION = int4eq, LEFTARG = integer, RIGHTARG = integer)", Comment: c("op comment")},
		&ir.OperatorClass{Schema: "public", Name: "my_opc", AccessMethod: "btree",
			Body: "CREATE OPERATOR CLASS public.my_opc FOR TYPE integer USING btree AS OPERATOR 3 =, FUNCTION 1 btint4cmp(integer, integer)", Comment: c("opc comment")},
		&ir.OperatorFamily{Schema: "public", Name: "my_fam", AccessMethod: "btree",
			Body: "CREATE OPERATOR FAMILY public.my_fam USING btree", Comment: c("opf comment")},
		&ir.TSConfig{Schema: "public", Name: "my_cfg",
			Body: `CREATE TEXT SEARCH CONFIGURATION public.my_cfg (PARSER = pg_catalog."default")`, Comment: c("cfg comment")},
		&ir.TSDict{Schema: "public", Name: "my_dict",
			Body: "CREATE TEXT SEARCH DICTIONARY public.my_dict (TEMPLATE = pg_catalog.simple)", Comment: c("dict comment")},
		&ir.TSParser{Schema: "public", Name: "my_prs",
			Body: "CREATE TEXT SEARCH PARSER public.my_prs (START = prsd_start, GETTOKEN = prsd_nexttoken, END = prsd_end, LEXTYPES = prsd_lextype)", Comment: c("prs comment")},
		&ir.TSTemplate{Schema: "public", Name: "my_tmpl",
			Body: "CREATE TEXT SEARCH TEMPLATE public.my_tmpl (LEXIZE = dsimple_lexize)", Comment: c("tmpl comment")},
	}

	var b strings.Builder
	for _, o := range objs {
		renderObjectDPG(&b, o, fmtOpts)
	}
	rendered := b.String()

	if !strings.Contains(rendered, "{\n    COMMENT 'fdw comment';\n}") {
		t.Errorf("rendered output does not carry an expected COMMENT block:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped .dpg (with comments) failed to recompile: %v\n---\n%s", err, rendered)
	}

	wantComments := map[string]string{
		"my_coll": "coll comment",
		"st":      "stats comment",
		"ts":      "ts comment",
		"my_opc":  "opc comment",
		"my_fam":  "opf comment",
		"my_cfg":  "cfg comment",
		"my_dict": "dict comment",
		"my_prs":  "prs comment",
		"my_tmpl": "tmpl comment",
	}
	found := map[string]bool{}
	for _, o := range compiled {
		var name string
		var comment *string
		switch v := o.(type) {
		case *ir.Collation:
			name, comment = v.Name, v.Comment
		case *ir.StatisticsObject:
			name, comment = v.Name, v.Comment
		case *ir.Tablespace:
			name, comment = v.Name, v.Comment
		case *ir.OperatorClass:
			name, comment = v.Name, v.Comment
		case *ir.OperatorFamily:
			name, comment = v.Name, v.Comment
		case *ir.TSConfig:
			name, comment = v.Name, v.Comment
		case *ir.TSDict:
			name, comment = v.Name, v.Comment
		case *ir.TSParser:
			name, comment = v.Name, v.Comment
		case *ir.TSTemplate:
			name, comment = v.Name, v.Comment
		default:
			continue
		}
		want, ok := wantComments[name]
		if !ok {
			continue
		}
		found[name] = true
		if comment == nil {
			t.Errorf("%s: Comment is nil after round-trip, want %q", name, want)
			continue
		}
		if *comment != want {
			t.Errorf("%s: Comment = %q after round-trip, want %q", name, *comment, want)
		}
	}
	for name := range wantComments {
		if !found[name] {
			t.Errorf("%s: object missing after round-trip compile", name)
		}
	}
}

// TestRenderTSConfigMappingCompiles guards RFC Section 12.1's MAPPING FOR block on
// a TEXT SEARCH CONFIGURATION — previously dropped entirely: renderOpaqueBody
// only ever knew about Comment/Grants, with no path at all for
// o.Mappings, so a declared (or introspected) mapping never round-tripped
// through dump. Covers both a single-dictionary mapping and the real PG
// multi-dictionary fallback-chain syntax (WITH dict1, dict2).
func TestRenderTSConfigMappingCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	comment := "demo search config"
	tc := &ir.TSConfig{
		Schema: "public", Name: "demo_search",
		ParserSchema: "pg_catalog", ParserName: "default",
		Body:    `CREATE TEXT SEARCH CONFIGURATION public.demo_search (PARSER = pg_catalog."default")`,
		Comment: &comment,
		Mappings: []pipeline.TSMappingDef{
			{TokenTypes: []string{"word", "hword"}, Dictionaries: []pipeline.Identifier{{Name: "unaccent"}, {Name: "english_stem"}}},
			{TokenTypes: []string{"asciiword"}, Dictionaries: []pipeline.Identifier{{Name: "english_stem"}}},
		},
	}

	var b strings.Builder
	renderObjectDPG(&b, tc, fmtOpts)
	rendered := b.String()

	if strings.Contains(rendered, "CREATE TEXT SEARCH CONFIGURATION") {
		t.Errorf("DPG source must not begin a declaration with CREATE, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "MAPPING FOR word, hword WITH unaccent, english_stem;") {
		t.Errorf("expected the fallback-chain mapping, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "MAPPING FOR asciiword WITH english_stem;") {
		t.Errorf("expected the single-dictionary mapping, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped ts config failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.TSConfig
	for _, o := range compiled {
		if c, ok := o.(*ir.TSConfig); ok && c.Name == "demo_search" {
			found = c
		}
	}
	if found == nil {
		t.Fatalf("ts config demo_search missing after recompile\n---\n%s", rendered)
	}
	if len(found.Mappings) != 2 {
		t.Fatalf("Mappings did not round-trip: got %d, want 2: %+v", len(found.Mappings), found.Mappings)
	}
	var sawChain, sawSingle bool
	for _, m := range found.Mappings {
		if len(m.Dictionaries) == 2 && m.Dictionaries[0].Name == "unaccent" && m.Dictionaries[1].Name == "english_stem" {
			sawChain = true
		}
		if len(m.TokenTypes) == 1 && m.TokenTypes[0] == "asciiword" && len(m.Dictionaries) == 1 {
			sawSingle = true
		}
	}
	if !sawChain {
		t.Errorf("fallback-chain mapping did not round-trip: %+v", found.Mappings)
	}
	if !sawSingle {
		t.Errorf("single-dictionary mapping did not round-trip: %+v", found.Mappings)
	}
}

// TestRenderTSConfigOwnerRoundtrip guards RFC audit item #33: renderTSConfigBody
// previously had no rendering path for Owner at all, despite it now being
// genuinely diffed (diffTSConfig).
func TestRenderTSConfigOwnerRoundtrip(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	owner := "search_admin"
	tc := &ir.TSConfig{
		Schema: "public", Name: "demo_search",
		ParserSchema: "pg_catalog", ParserName: "default",
		Body:  `CREATE TEXT SEARCH CONFIGURATION public.demo_search (PARSER = pg_catalog."default")`,
		Owner: &owner,
	}

	var b strings.Builder
	renderObjectDPG(&b, tc, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "OWNER search_admin;") {
		t.Fatalf("expected OWNER search_admin, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped ts config failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.TSConfig
	for _, o := range compiled {
		if c, ok := o.(*ir.TSConfig); ok && c.Name == "demo_search" {
			found = c
		}
	}
	if found == nil || found.Owner == nil || *found.Owner != "search_admin" {
		t.Errorf("Owner did not round-trip: %+v", found)
	}
}

// ── Operator family loose members (RFC Section 14.4) ─────────────────────────────────

// TestRenderOpFamilyBareFormNoChurn guards zero output churn for the common
// case — a family with only class-owned members (e.g. the demo project's
// text_ci_ops) — renderOpFamilyBody must still render the bare "...;" form,
// exactly like a member-less family always did, not a spurious empty "{ }".
func TestRenderOpFamilyBareFormNoChurn(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	fam := &ir.OperatorFamily{
		Schema: "public", Name: "text_ci_ops", AccessMethod: "btree",
		Body: `CREATE OPERATOR FAMILY public.text_ci_ops USING btree`,
	}
	var b strings.Builder
	renderObjectDPG(&b, fam, fmtOpts)
	rendered := b.String()
	if strings.Contains(rendered, "{") {
		t.Errorf("expected bare form (no members, no comment), got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "OPERATOR FAMILY public.text_ci_ops USING btree;") {
		t.Errorf("expected the bare declaration, got:\n%s", rendered)
	}
}

// TestRenderOpFamilyWithMembersRoundtrips guards the full spine: rendered
// source must not begin with "CREATE" (the no-verb mandate) and must
// recompile back to the same structured Members, using m.AddClause() — the
// exact renderer the differ itself uses — so dump output and generated SQL
// can never drift apart.
func TestRenderOpFamilyWithMembersRoundtrips(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	fam := &ir.OperatorFamily{
		Schema: "public", Name: "myfam", AccessMethod: "btree",
		Body: `CREATE OPERATOR FAMILY public.myfam USING btree`,
		Members: []pipeline.OpFamilyMember{
			{Number: 1, Name: pipeline.Identifier{Name: "<"}, LeftType: "integer", RightType: "bigint"},
			{IsFunction: true, Number: 1, Name: pipeline.Identifier{Schema: "public", Name: "btint48cmp"},
				LeftType: "integer", RightType: "bigint", FuncArgs: []string{"integer", "bigint"}},
		},
	}
	var b strings.Builder
	renderObjectDPG(&b, fam, fmtOpts)
	rendered := b.String()
	if strings.Contains(rendered, "CREATE OPERATOR FAMILY") {
		t.Errorf("DPG source must not begin a declaration with CREATE, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped operator family failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.OperatorFamily
	for _, o := range compiled {
		if of, ok := o.(*ir.OperatorFamily); ok && of.Name == "myfam" {
			found = of
		}
	}
	if found == nil {
		t.Fatalf("operator family myfam missing after recompile\n---\n%s", rendered)
	}
	if len(found.Members) != 2 {
		t.Fatalf("Members did not round-trip: got %d, want 2: %+v", len(found.Members), found.Members)
	}
}

// TestRenderCompositeAndRangeTypesCompile guards a real bug found
// live-testing a demo project: renderObjectDPG's *ir.Type switch only ever
// had cases for "ENUM" and "DOMAIN" — COMPOSITE and RANGE both fell through
// to a "-- type X (VARIANT) omitted" comment instead of real DPG source,
// even though introspection already fully captured COMPOSITE (via
// CompositeAttrs) and, after the matching introspectRangeBodies fix, RANGE
// too. Same discipline as TestRenderOpaqueObjectsCompile: render, write to
// a real file, recompile, and diff the recompiled object against an empty
// snapshot to prove createType actually succeeds for it, not just that the
// rendered text looks plausible.
func TestRenderCompositeAndRangeTypesCompile(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}

	objs := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "address", Variant: "COMPOSITE",
			CompositeAttrs: []*ir.Column{
				{Name: "street", Type: ir.TypeRef{Name: "text"}, Collation: "en_US"},
				{Name: "city", Type: ir.TypeRef{Name: "text"}},
			},
		},
		&ir.Type{
			Schema: "public", Name: "price_range", Variant: "RANGE",
			Body: "SUBTYPE = numeric",
		},
	}

	var b strings.Builder
	for _, o := range objs {
		renderObjectDPG(&b, o, fmtOpts)
	}
	rendered := b.String()

	if strings.Contains(rendered, "omitted") {
		t.Fatalf("rendered output still contains an omitted-type comment:\n%s", rendered)
	}
	for line := range strings.SplitSeq(rendered, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "CREATE ") {
			t.Errorf("rendered declaration begins with CREATE (violates no-verb mandate): %q", line)
		}
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped .dpg failed to recompile: %v\n---\n%s", err, rendered)
	}

	wantVariants := map[string]bool{"COMPOSITE": false, "RANGE": false}
	for _, o := range compiled {
		if tp, ok := o.(*ir.Type); ok {
			if _, tracked := wantVariants[tp.Variant]; tracked {
				wantVariants[tp.Variant] = true
			}
			if tp.Variant == "COMPOSITE" {
				if len(tp.CompositeAttrs) == 0 || tp.CompositeAttrs[0].Collation != "en_US" {
					t.Errorf("composite attribute COLLATE did not round-trip: %+v", tp.CompositeAttrs)
				}
			}
		}
	}
	for v, seen := range wantVariants {
		if !seen {
			t.Errorf("variant %s missing after recompile", v)
		}
	}

	if _, err := diff.New().Diff(compiled, &pipeline.Snapshot{}); err != nil {
		t.Fatalf("diffing dumped composite/range types failed: %v", err)
	}
}

// TestRenderRoleAttributesCompile guards dump's Role rendering (RFC Section 11.1):
// every attribute except PASSWORD (never introspected, see the renderer's
// own doc comment) must round-trip through dump -> recompile with matching
// values — a bare "ROLE name;" would silently drop LOGIN/SUPERUSER/
// membership/etc. from dumped source, defeating dump's actual purpose of
// capturing a live role's real configuration (harmless for plan --live
// specifically, since "undeclared means unmanaged", but still a real
// fidelity gap for anyone reading or version-controlling the dumped file).
func TestRenderRoleAttributesCompile(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	boolp := func(v bool) *bool { return &v }
	intp := func(v int) *int { return &v }
	strp := func(v string) *string { return &v }
	comment := "application service account"

	role := &ir.Role{
		Name: "app_service", CanLogin: boolp(true), Superuser: boolp(false),
		CreateDB: boolp(false), CreateRole: boolp(false), Inherit: boolp(true),
		IsReplication: boolp(false), BypassRLS: boolp(false), ConnectionLimit: intp(20),
		ValidUntil: strp("2030-01-01"),
		Memberships: []ir.RoleMembership{
			{Role: "reader_group", Direction: "IN_ROLE"},
			{Role: "member_role", Direction: "ROLE"},
			{Role: "admin_role", Direction: "ROLE", Admin: true},
		},
		Comment: &comment,
		// Password deliberately unset — never introspected, must never be
		// rendered (there's nothing to render: dump only ever sees
		// introspected Role objects, which never populate Password).
	}

	var b strings.Builder
	renderObjectDPG(&b, role, fmtOpts)
	rendered := b.String()

	if strings.Contains(rendered, "PASSWORD") {
		t.Errorf("rendered Role must never mention PASSWORD (never introspected): %s", rendered)
	}
	for line := range strings.SplitSeq(rendered, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "CREATE ") {
			t.Errorf("rendered declaration begins with CREATE (violates no-verb mandate): %q", line)
		}
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "roles.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped Role failed to recompile: %v\n---\n%s", err, rendered)
	}
	if len(compiled) != 1 {
		t.Fatalf("expected 1 compiled object, got %d", len(compiled))
	}
	got, ok := compiled[0].(*ir.Role)
	if !ok {
		t.Fatalf("expected *ir.Role, got %T", compiled[0])
	}

	check := func(name string, got, want any) {
		t.Helper()
		gv, wv := reflect.ValueOf(got), reflect.ValueOf(want)
		if gv.Kind() == reflect.Pointer && wv.Kind() == reflect.Pointer {
			if gv.IsNil() != wv.IsNil() {
				t.Errorf("%s: got nil=%v, want nil=%v", name, gv.IsNil(), wv.IsNil())
				return
			}
			if !gv.IsNil() && gv.Elem().Interface() != wv.Elem().Interface() {
				t.Errorf("%s: got %v, want %v", name, gv.Elem(), wv.Elem())
			}
			return
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}
	check("CanLogin", got.CanLogin, role.CanLogin)
	check("Superuser", got.Superuser, role.Superuser)
	check("CreateDB", got.CreateDB, role.CreateDB)
	check("CreateRole", got.CreateRole, role.CreateRole)
	check("Inherit", got.Inherit, role.Inherit)
	check("IsReplication", got.IsReplication, role.IsReplication)
	check("BypassRLS", got.BypassRLS, role.BypassRLS)
	check("ConnectionLimit", got.ConnectionLimit, role.ConnectionLimit)
	check("ValidUntil", got.ValidUntil, role.ValidUntil)
	check("Comment", got.Comment, role.Comment)
	if len(got.Memberships) != len(role.Memberships) {
		t.Fatalf("Memberships: got %+v, want %+v", got.Memberships, role.Memberships)
	}
	for i := range role.Memberships {
		want, g := role.Memberships[i], got.Memberships[i]
		if g.Role != want.Role || g.Direction != want.Direction || g.Admin != want.Admin {
			t.Errorf("Memberships[%d]: got %+v, want %+v", i, g, want)
		}
	}
	if got.Password != nil {
		t.Errorf("Password: got %v, want nil", got.Password)
	}
}

// TestRenderRoleMembershipsCompile guards RFC audit item #32's dump-and-
// recompile round trip: a mix of bare memberships (render inline via the
// plain IN ROLE/ROLE/ADMIN lists) and WITH-modifier-bearing memberships
// (render as their own block directive, the only shape that can carry
// INHERIT/SET at all) must both produce valid, recompilable .dpg source.
func TestRenderRoleMembershipsCompile(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	inheritFalse := false
	setFalse := false
	role := &ir.Role{
		Name: "app_service",
		Memberships: []ir.RoleMembership{
			{Role: "bare_parent", Direction: "IN_ROLE"},
			{Role: "bare_child", Direction: "ROLE"},
			{Role: "bare_admin", Direction: "ROLE", Admin: true},
			{Role: "modified_parent", Direction: "IN_ROLE", Admin: true, Inherit: &inheritFalse},
			{Role: "modified_child", Direction: "ROLE", Set: &setFalse},
		},
	}

	var b strings.Builder
	renderObjectDPG(&b, role, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "IN ROLE") || !strings.Contains(rendered, "bare_parent") {
		t.Errorf("rendered role missing bare inline IN ROLE: %q", rendered)
	}
	if !strings.Contains(rendered, "WITH INHERIT FALSE") {
		t.Errorf("rendered role missing WITH INHERIT FALSE block directive: %q", rendered)
	}
	if !strings.Contains(rendered, "WITH SET FALSE") {
		t.Errorf("rendered role missing WITH SET FALSE block directive: %q", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "roles.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped role failed to recompile: %v\n---\n%s", err, rendered)
	}
	got, ok := compiled[0].(*ir.Role)
	if !ok {
		t.Fatalf("expected *ir.Role, got %T", compiled[0])
	}
	if len(got.Memberships) != len(role.Memberships) {
		t.Fatalf("Memberships did not round-trip: got %d entries, want %d: %+v", len(got.Memberships), len(role.Memberships), got.Memberships)
	}
	byRole := map[string]ir.RoleMembership{}
	for _, m := range got.Memberships {
		byRole[m.Role] = m
	}
	mp, ok := byRole["modified_parent"]
	if !ok || !mp.Admin || mp.Inherit == nil || *mp.Inherit {
		t.Errorf("modified_parent did not round-trip: got %+v", mp)
	}
	mc, ok := byRole["modified_child"]
	if !ok || mc.Set == nil || *mc.Set {
		t.Errorf("modified_child did not round-trip: got %+v", mc)
	}
	ba, ok := byRole["bare_admin"]
	if !ok || !ba.Admin || ba.Inherit != nil || ba.Set != nil {
		t.Errorf("bare_admin did not round-trip: got %+v", ba)
	}
}

// TestRenderRoleMembershipRoleDirectionInheritCompaction guards the
// remaining half of RFC audit item #32's dump-compaction gap: an
// IN_ROLE-direction entry's WITH INHERIT can already be compacted away
// trivially (o.Inherit is this exact role's own attribute), but a
// ROLE-direction entry names a DIFFERENT role — compacting it needs that
// other role's own INHERIT default, only available via
// renderObjectDPGWithRoleInheritDefaults' roleInheritDefaults map (built
// from every introspected role in a real dump run). Exercises all three
// cases: matching default (compacted), non-matching (rendered explicitly,
// a real declared override), and the target role absent from the map
// entirely (rendered explicitly — the same safe fallback renderObjectDPG's
// nil-map behavior already has, e.g. for a system role dump never
// introspects).
func TestRenderRoleMembershipRoleDirectionInheritCompaction(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	inheritTrue, inheritFalse := true, false
	role := &ir.Role{
		Name: "app_service",
		Memberships: []ir.RoleMembership{
			{Role: "child_default_true", Direction: "ROLE", Inherit: &inheritTrue},
			{Role: "child_default_false", Direction: "ROLE", Inherit: &inheritFalse},
			{Role: "child_override", Direction: "ROLE", Inherit: &inheritFalse},
			{Role: "child_unknown", Direction: "ROLE", Inherit: &inheritFalse},
		},
	}
	roleInheritDefaults := map[string]bool{
		"child_default_true":  true,
		"child_default_false": false,
		"child_override":      true,
		// "child_unknown" deliberately absent.
	}

	var b strings.Builder
	renderObjectDPGWithRoleInheritDefaults(&b, role, fmtOpts, roleInheritDefaults)
	rendered := b.String()

	if strings.Contains(rendered, "child_default_true") && strings.Contains(rendered, "WITH INHERIT") {
		for line := range strings.SplitSeq(rendered, "\n") {
			if strings.Contains(line, "child_default_true") && strings.Contains(line, "WITH INHERIT") {
				t.Errorf("child_default_true: WITH INHERIT should be compacted away (matches its own default), got line: %q", line)
			}
		}
	}
	if strings.Contains(rendered, "child_default_false") && strings.Contains(rendered, "WITH INHERIT") {
		for line := range strings.SplitSeq(rendered, "\n") {
			if strings.Contains(line, "child_default_false") && strings.Contains(line, "WITH INHERIT") {
				t.Errorf("child_default_false: WITH INHERIT should be compacted away (matches its own default), got line: %q", line)
			}
		}
	}
	var sawOverride, sawUnknown bool
	for line := range strings.SplitSeq(rendered, "\n") {
		if strings.Contains(line, "child_override") && strings.Contains(line, "WITH INHERIT FALSE") {
			sawOverride = true
		}
		if strings.Contains(line, "child_unknown") && strings.Contains(line, "WITH INHERIT FALSE") {
			sawUnknown = true
		}
	}
	if !sawOverride {
		t.Errorf("child_override: expected an explicit WITH INHERIT FALSE (a real declared override), got: %s", rendered)
	}
	if !sawUnknown {
		t.Errorf("child_unknown: expected an explicit WITH INHERIT FALSE (target role absent from roleInheritDefaults), got: %s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "roles.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped role failed to recompile: %v\n---\n%s", err, rendered)
	}
	got, ok := compiled[0].(*ir.Role)
	if !ok {
		t.Fatalf("expected *ir.Role, got %T", compiled[0])
	}
	byRole := map[string]ir.RoleMembership{}
	for _, m := range got.Memberships {
		byRole[m.Role] = m
	}
	// A compacted entry (child_default_true/child_default_false) is
	// expected to recompile with Inherit == nil — compaction only ever
	// drops the WITH INHERIT clause from rendered source, and it's
	// introspectRoleMemberships' job (not dump's) to re-derive the real
	// live value regardless of what source spells out explicitly. The
	// still-explicit entries (child_override/child_unknown) must round-
	// trip their real declared value unchanged.
	for name, wantCompacted := range map[string]bool{
		"child_default_true":  true,
		"child_default_false": true,
		"child_override":      false,
		"child_unknown":       false,
	} {
		m, ok := byRole[name]
		if !ok {
			t.Errorf("%s: missing from recompiled memberships", name)
			continue
		}
		if wantCompacted && m.Inherit != nil {
			t.Errorf("%s: expected no WITH INHERIT clause in recompiled source (compacted), got Inherit=%v", name, *m.Inherit)
		}
		if !wantCompacted && (m.Inherit == nil || *m.Inherit != false) {
			t.Errorf("%s: expected WITH INHERIT FALSE to round-trip, got Inherit=%v", name, m.Inherit)
		}
	}
}

// TestRenderRoleConfigsCompile guards RFC audit item #74's dump-and-
// recompile round trip: a live-introspected Role with SET/RESET config
// entries (as introspectRoleConfigs would populate — bare, unquoted
// values) must render into valid, recompilable .dpg source.
func TestRenderRoleConfigsCompile(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	timeoutVal := "5s"
	memVal := "64MB"
	db := "mydb"
	role := &ir.Role{
		Name: "app_service",
		Configs: []ir.RoleConfig{
			{Param: "statement_timeout", Value: &timeoutVal},
			{Param: "work_mem", Value: &memVal, InDatabase: &db},
			{Reset: true, ResetAll: true},
		},
	}

	var b strings.Builder
	renderObjectDPG(&b, role, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "SET statement_timeout = '5s'") {
		t.Errorf("rendered role missing SET statement_timeout: %q", rendered)
	}
	if !strings.Contains(rendered, "SET work_mem = '64MB' IN DATABASE") {
		t.Errorf("rendered role missing SET work_mem IN DATABASE: %q", rendered)
	}
	if !strings.Contains(rendered, "RESET ALL") {
		t.Errorf("rendered role missing RESET ALL: %q", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "roles.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped role failed to recompile: %v\n---\n%s", err, rendered)
	}
	got, ok := compiled[0].(*ir.Role)
	if !ok {
		t.Fatalf("expected *ir.Role, got %T", compiled[0])
	}
	if len(got.Configs) != 3 {
		t.Fatalf("Configs did not round-trip: got %d entries: %+v", len(got.Configs), got.Configs)
	}
	if got.Configs[0].Param != "statement_timeout" || got.Configs[0].Value == nil || *got.Configs[0].Value != "'5s'" {
		t.Errorf("Configs[0]: got %+v", got.Configs[0])
	}
	if got.Configs[1].Param != "work_mem" || got.Configs[1].InDatabase == nil || *got.Configs[1].InDatabase != "mydb" {
		t.Errorf("Configs[1]: got %+v", got.Configs[1])
	}
	if !got.Configs[2].Reset || !got.Configs[2].ResetAll {
		t.Errorf("Configs[2]: got %+v", got.Configs[2])
	}
}

// TestRenderBareRoleCompile guards the minimal case: a role with no
// attributes set at all (every pointer/slice nil) must still render valid,
// recompilable source — the pre-existing "ROLE name;" behavior, unchanged.
func TestRenderBareRoleCompile(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	var b strings.Builder
	renderObjectDPG(&b, &ir.Role{Name: "plain_role"}, fmtOpts)
	rendered := b.String()

	dir := t.TempDir()
	f := filepath.Join(dir, "roles.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped bare Role failed to recompile: %v\n---\n%s", err, rendered)
	}
	got, ok := compiled[0].(*ir.Role)
	if !ok || got.Name != "plain_role" {
		t.Fatalf("expected *ir.Role named plain_role, got %+v", compiled[0])
	}
	if got.CanLogin != nil || got.Superuser != nil || got.ConnectionLimit != nil {
		t.Errorf("expected all attributes nil for a bare role, got: %+v", got)
	}
}

func TestIsClusterScoped(t *testing.T) {
	cluster := []pipeline.IRObject{&ir.Role{Name: "r"}, &ir.Tablespace{Name: "ts"}}
	database := []pipeline.IRObject{
		&ir.ForeignDataWrapper{Name: "f"}, &ir.ForeignServer{Name: "s"},
		&ir.UserMapping{Server: "s"}, &ir.Publication{Name: "p"},
		&ir.Subscription{Name: "sub"},
		&ir.EventTrigger{Name: "e"}, &ir.Cast{}, &ir.Collation{Name: "c"},
		&ir.Table{Schema: "public", Name: "t"},
	}
	for _, o := range cluster {
		if !isClusterScoped(o) {
			t.Errorf("%T should be cluster-scoped", o)
		}
	}
	for _, o := range database {
		if isClusterScoped(o) {
			t.Errorf("%T should NOT be cluster-scoped", o)
		}
	}
	// excludeClusterScoped keeps exactly the database objects.
	kept := excludeClusterScoped(append(append([]pipeline.IRObject{}, cluster...), database...))
	if len(kept) != len(database) {
		t.Errorf("excludeClusterScoped kept %d, want %d", len(kept), len(database))
	}
}

func TestQuoteIdentIfNeeded(t *testing.T) {
	cases := map[string]string{
		"items":     "items",       // safe lowercase
		"a1_$":      "a1_$",        // safe with digit/underscore/dollar
		"name":      "name",        // unreserved/col-name keyword -> bare
		"order":     `"order"`,     // reserved
		"select":    `"select"`,    // reserved
		"user":      `"user"`,      // reserved
		"left":      `"left"`,      // type_func_name
		"MyTable":   `"MyTable"`,   // uppercase folds -> must quote
		"has space": `"has space"`, // special char
		"1st":       `"1st"`,       // leading digit
		"":          `""`,          // empty
	}
	for in, want := range cases {
		if got := quoteIdentIfNeeded(in); got != want {
			t.Errorf("quoteIdentIfNeeded(%q) = %q, want %q", in, got, want)
		}
	}
}

// Dumped source for reserved-word / schema / extension objects must quote where
// needed and recompile cleanly (regression: renderObjectDPG emitted identifiers
// raw, so a reserved-word table/column produced non-compiling source).
func TestRenderReservedWordAndStructuralCompile(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	comment := "reporting schema"
	tblComment := "a table's comment"
	where := `"select" > 0`
	objs := []pipeline.IRObject{
		&ir.Schema{Name: "reporting", Comment: &comment},
		&ir.Schema{Name: "select"},
		&ir.Extension{Name: "hstore"},
		// ENUM must render canonical "ENUM name (...)" form (RFC Section
		// 5.1, no "AS ENUM") with single-quoted labels.
		&ir.Type{Schema: "public", Name: "mood", Variant: "ENUM", EnumValues: []string{"happy", "it's ok"}},
		// Reserved-word names + a COMMENT + an INDEX (INDICES block) must all recompile.
		&ir.Table{Schema: "public", Name: "user", Comment: &tblComment,
			Columns: []*ir.Column{
				{Name: "select", Type: ir.TypeRef{Name: "integer"}},
				{Name: "id", Type: ir.TypeRef{Name: "integer"}},
			},
			Indexes: []*ir.Index{
				{Name: "idx_user_sel", Columns: []pipeline.IndexColumn{{Name: "select"}}, Where: &where},
			}},
	}
	var b strings.Builder
	for _, o := range objs {
		renderObjectDPG(&b, o, fmtOpts)
	}
	rendered := b.String()
	// Reserved-word table/column/schema names must all be quoted.
	for _, q := range []string{`"user"`, `"select"`} {
		if !strings.Contains(rendered, q) {
			t.Errorf("expected %s quoted in output:\n%s", q, rendered)
		}
	}
	if !strings.Contains(rendered, `SCHEMA "select" {`) {
		t.Errorf("expected reserved-word schema name quoted, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "EXTENSION hstore;") {
		t.Errorf("expected bare EXTENSION hstore, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "SCHEMA reporting {") {
		t.Errorf("expected SCHEMA reporting block, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("reserved-word/structural dump did not recompile: %v\n---\n%s", err, rendered)
	}
	var sawSchema, sawReservedSchema, sawExt, sawTable bool
	for _, o := range compiled {
		switch v := o.(type) {
		case *ir.Schema:
			if v.Name == "reporting" {
				sawSchema = true
			}
			if v.Name == "select" {
				sawReservedSchema = true
			}
		case *ir.Extension:
			if v.Name == "hstore" {
				sawExt = true
			}
		case *ir.Table:
			if v.Name == "user" {
				sawTable = true
			}
		}
	}
	if !sawSchema || !sawReservedSchema || !sawExt || !sawTable {
		t.Errorf("recompile missing objects: schema=%v reservedSchema=%v ext=%v table=%v", sawSchema, sawReservedSchema, sawExt, sawTable)
	}

	// Index columns must render bare in source (the differ's createIndex quotes
	// them when generating SQL). Emitting """select""" would create an index on a
	// literal 8-char column name. The dumped INDICES entry must contain (select),
	// not ("select").
	if strings.Contains(rendered, `("select")`) {
		t.Errorf("index column must be bare in dumped source, got quoted:\n%s", rendered)
	}

	// End-to-end: diffing the recompiled objects against an empty snapshot must
	// produce a CREATE INDEX with the column quoted exactly once, and with the
	// partial-index WHERE preserved.
	ops, err := diff.New().Diff(compiled, &pipeline.Snapshot{})
	if err != nil {
		t.Fatalf("diff of recompiled objects failed: %v", err)
	}
	var idxSQL string
	for _, o := range ops {
		if strings.Contains(o.SQL(), "CREATE") && strings.Contains(o.SQL(), "INDEX") {
			idxSQL = o.SQL()
			break
		}
	}
	if idxSQL == "" {
		t.Fatal("no CREATE INDEX op from recompiled table")
	}
	if strings.Contains(idxSQL, `"""select"""`) || !strings.Contains(idxSQL, `("select")`) {
		t.Errorf("index column not single-quoted correctly: %s", idxSQL)
	}
	if !strings.Contains(idxSQL, `WHERE "select" > 0`) {
		t.Errorf("partial-index WHERE lost through round-trip: %s", idxSQL)
	}
}

// TestRenderOwnerAndColumnStorage guards a dump false-negative found during
// the diff-coverage push: Owner and column Comment/Storage/Compression/
// Statistics are all genuinely diffed (diffTable/diffSchema/diffSequence/
// diffColumns) but were never rendered by dump, so a dumped project could
// never detect drift on any of them, forever. Also proves the Storage-vs-
// type-default suppression: only a genuine STORAGE override renders, not
// every column's ordinary type-driven default.
func TestRenderOwnerAndColumnStorage(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	owner := "app_owner"
	colComment := "internal id"
	compression := "lz4"
	stats := 500
	overrideStorage := "EXTERNAL"
	defaultStorage := "EXTENDED"
	objs := []pipeline.IRObject{
		&ir.Schema{Name: "app", Owner: &owner},
		&ir.Sequence{Schema: "public", Name: "seq_id", Owner: &owner},
		&ir.Table{Schema: "public", Name: "t", Owner: &owner,
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "integer"}, Comment: &colComment,
					Compression: &compression, Statistics: &stats,
					Storage: &overrideStorage, StorageIsTypeDefault: false},
				{Name: "body", Type: ir.TypeRef{Name: "text"},
					Storage: &defaultStorage, StorageIsTypeDefault: true},
			},
		},
	}
	var b strings.Builder
	for _, o := range objs {
		renderObjectDPG(&b, o, fmtOpts)
	}
	rendered := b.String()

	if !strings.Contains(rendered, "SCHEMA app {") || !strings.Contains(rendered, "OWNER app_owner;") {
		t.Errorf("expected schema OWNER, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "SEQUENCE") || strings.Count(rendered, "OWNER app_owner;") != 3 {
		t.Errorf("expected 3 OWNER declarations (schema, sequence, table), got:\n%s", rendered)
	}
	if !strings.Contains(rendered, `COMMENT 'internal id';`) {
		t.Errorf("expected column COMMENT, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, `COMPRESSION lz4;`) {
		t.Errorf("expected column COMPRESSION, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "STATISTICS 500;") {
		t.Errorf("expected column STATISTICS, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, `STORAGE "EXTERNAL";`) {
		t.Errorf("expected the overridden column's STORAGE to render, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "EXTENDED") {
		t.Errorf("type-default STORAGE must be suppressed (noise), got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("owner/storage dump did not recompile: %v\n---\n%s", err, rendered)
	}
	var sawSchemaOwner, sawSeqOwner, sawTableOwner bool
	var sawColAttrs bool
	for _, o := range compiled {
		switch v := o.(type) {
		case *ir.Schema:
			sawSchemaOwner = v.Owner != nil && *v.Owner == owner
		case *ir.Sequence:
			sawSeqOwner = v.Owner != nil && *v.Owner == owner
		case *ir.Table:
			sawTableOwner = v.Owner != nil && *v.Owner == owner
			for _, col := range v.Columns {
				if col.Name == "id" && col.Comment != nil && *col.Comment == colComment &&
					col.Compression != nil && *col.Compression == compression &&
					col.Statistics != nil && *col.Statistics == stats &&
					col.Storage != nil && *col.Storage == overrideStorage {
					sawColAttrs = true
				}
			}
		}
	}
	if !sawSchemaOwner || !sawSeqOwner || !sawTableOwner {
		t.Errorf("recompile missing owner: schema=%v sequence=%v table=%v", sawSchemaOwner, sawSeqOwner, sawTableOwner)
	}
	if !sawColAttrs {
		t.Error("recompile missing column comment/compression/statistics/storage")
	}
}

// TestRenderFunctionAndProcedureBody guards the fix for a much deeper dump
// gap than a rendering tweak: FUNCTION previously rendered only a
// placeholder comment ("body omitted"), and PROCEDURE had no case in
// renderObjectDPG's switch at all — dump silently dropped every procedure
// from generated source. dump can now actually reconstruct both, using
// Attrs.Body (already populated by the IR builder from source, and now also
// by introspection from pg_proc.prosrc). Also guards the "no CREATE verb"
// rule (RFC Section 3.4-adjacent convention already enforced elsewhere in this
// file, e.g. renderOpaqueBody) — a naive port of differ.go's
// buildFunctionSQL/createProcedure (which DO emit CREATE, since that's real
// SQL, not DPG source) would have produced unparseable DPG source.
func TestRenderFunctionAndProcedureBody(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	fnComment := "adds two integers"
	fnBody := "BEGIN\n    RETURN a + b;\nEND;"
	procBody := "BEGIN\n    NULL;\nEND;"
	objs := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "add_ints",
			Args: []ir.FuncArg{
				{Name: "a", Type: ir.TypeRef{Name: "integer"}},
				{Name: "b", Type: ir.TypeRef{Name: "integer"}},
			},
			ReturnType:  ir.TypeRef{Name: "integer"},
			Attrs:       ir.FuncAttrs{Language: "plpgsql", Volatility: "IMMUTABLE", Strict: true, Body: fnBody},
			Comment:     &fnComment,
			Grants:      []ir.Grant{{Privileges: []string{"EXECUTE"}, Roles: []string{"app_user"}}},
			Revocations: []ir.Revocation{{Privileges: []string{"EXECUTE"}, Roles: []string{"PUBLIC"}}},
		},
		&ir.Procedure{
			Schema: "public", Name: "recalc",
			Attrs:       ir.FuncAttrs{Language: "plpgsql", Body: procBody},
			Revocations: []ir.Revocation{{Privileges: []string{"EXECUTE"}, Roles: []string{"PUBLIC"}}},
		},
	}
	var b strings.Builder
	for _, o := range objs {
		renderObjectDPG(&b, o, fmtOpts)
	}
	rendered := b.String()

	if strings.Contains(rendered, "CREATE FUNCTION") || strings.Contains(rendered, "CREATE PROCEDURE") {
		t.Errorf("DPG source must not begin a declaration with CREATE, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "FUNCTION add_ints(") {
		t.Errorf("expected bare FUNCTION declaration, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "RETURN a + b;") {
		t.Errorf("expected the real function body rendered, not a placeholder, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "IMMUTABLE") || !strings.Contains(rendered, "STRICT") {
		t.Errorf("expected IMMUTABLE/STRICT rendered, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "COMMENT 'adds two integers';") {
		t.Errorf("expected function COMMENT, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "GRANT EXECUTE TO app_user;") {
		t.Errorf("expected function GRANT, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "REVOCATION EXECUTE FROM PUBLIC;") {
		t.Errorf("expected function REVOCATION (RFC Section 11.3 REVOCATIONS, Mode B singular keyword), got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "PROCEDURE recalc(") {
		t.Errorf("expected a PROCEDURE declaration (previously dropped entirely), got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "NULL;") {
		t.Errorf("expected the real procedure body rendered, got:\n%s", rendered)
	}
	if strings.Count(rendered, "REVOCATION EXECUTE FROM PUBLIC;") != 2 {
		t.Errorf("expected both function and procedure REVOCATION, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("function/procedure dump did not recompile: %v\n---\n%s", err, rendered)
	}
	var sawFunc, sawProc bool
	for _, o := range compiled {
		switch v := o.(type) {
		case *ir.Function:
			if v.Name == "add_ints" {
				// BodyHash is intentionally not compared to a specific
				// algorithm here (ir/typeutil_test.go owns that) — a
				// plpgsql body now canonicalizes structurally rather than
				// matching raw HashBody(fnBody); non-empty is enough to
				// prove the body round-tripped through dump+recompile.
				sawFunc = v.BodyHash != "" && v.Comment != nil && *v.Comment == fnComment &&
					len(v.Grants) == 1 && v.Attrs.Volatility == "IMMUTABLE" && v.Attrs.Strict &&
					len(v.Revocations) == 1 && v.Revocations[0].Roles[0] == "PUBLIC"
			}
		case *ir.Procedure:
			if v.Name == "recalc" {
				sawProc = v.BodyHash != "" &&
					len(v.Revocations) == 1 && v.Revocations[0].Roles[0] == "PUBLIC"
			}
		}
	}
	if !sawFunc {
		t.Error("recompile missing function body/comment/grant/revocation/attrs fidelity")
	}
	if !sawProc {
		t.Error("recompile missing procedure body/revocation fidelity")
	}
}

// TestRenderAggregateCompiles guards a real bug found live-testing a demo
// project: renderObjectDPG had NO case at all for *ir.Aggregate — an
// AGGREGATE object silently produced no output whatsoever (not even a wrong
// stub, unlike the earlier VIEW bug). Uses o.Options (structured
// SFUNC/STYPE/INITCOND list) rather than o.Body, since Body is the full
// "CREATE AGGREGATE ..." SQL text — a different, invalid shape for DPG
// source, which never starts a declaration with CREATE.
// TestRenderDefaultPrivilegesCompiles guards a real bug found live-testing a
// demo project: renderObjectDPG had no case at all for *ir.DefaultPrivileges
// (silent, total drop — same class as the earlier Aggregate bug), and
// introspection never populated the object in the first place. o.ObjectType
// is single-valued per object (Builder.BuildDefaultPrivileges splits a
// multi-type declaration into one object per type; introspectDefaultPrivileges
// does the same from pg_default_acl), so this renders one full DEFAULT
// PRIVILEGES declaration per object, each with its own ON <type> clause on
// every grant/revoke entry — matching real PostgreSQL's actual ALTER
// DEFAULT PRIVILEGES grammar (confirmed live via \h ALTER DEFAULT
// PRIVILEGES), not a DPG-invented whole-declaration wrapper.
func TestRenderDefaultPrivilegesCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	forRole := "app_admin"
	inSchema := "public"
	dp := &ir.DefaultPrivileges{
		ForRole:    &forRole,
		InSchema:   &inSchema,
		ObjectType: "TABLES",
		Grants:     []ir.Grant{{Privileges: []string{"SELECT"}, Roles: []string{"app_readonly"}}},
		Revocations: []ir.Revocation{
			{Privileges: []string{"INSERT"}, Roles: []string{"app_writer"}, Cascade: true},
		},
	}

	var b strings.Builder
	renderObjectDPG(&b, dp, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "DEFAULT PRIVILEGES FOR ROLE app_admin IN SCHEMA public {") {
		t.Errorf("expected DEFAULT PRIVILEGES header, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "SELECT ON TABLES TO app_readonly;") {
		t.Errorf("expected GRANT entry with ON TABLES, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "INSERT ON TABLES FROM app_writer CASCADE;") {
		t.Errorf("expected REVOCATION entry with ON TABLES and CASCADE, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped default privileges failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.DefaultPrivileges
	for _, o := range compiled {
		if d, ok := o.(*ir.DefaultPrivileges); ok {
			found = d
		}
	}
	if found == nil {
		t.Fatalf("default privileges object missing after recompile\n---\n%s", rendered)
	}
	if found.ForRole == nil || *found.ForRole != "app_admin" {
		t.Errorf("ForRole did not round-trip: %v", found.ForRole)
	}
	if found.InSchema == nil || *found.InSchema != "public" {
		t.Errorf("InSchema did not round-trip: %v", found.InSchema)
	}
	if len(found.Grants) != 1 || found.Grants[0].Privileges[0] != "SELECT" {
		t.Errorf("Grants did not round-trip: %+v", found.Grants)
	}
	if len(found.Revocations) != 1 || !found.Revocations[0].Cascade {
		t.Errorf("Revocations (incl. CASCADE) did not round-trip: %+v", found.Revocations)
	}
}

// TestRenderParameterPrivilegesGrantedByRoundtrip guards Parameter
// Privileges' GRANTED BY (RFC Section 11.6) round-tripping through dump —
// real PostgreSQL's GRANT/REVOKE ... ON PARAMETER accepts the same optional
// GRANTED BY role-spec as every other GRANT form (confirmed live, PG17).
func TestRenderParameterPrivilegesGrantedByRoundtrip(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	adminRole := "admin_role"
	pp := &ir.ParameterPrivileges{
		Grants: []ir.ParameterGrant{
			{Privileges: []string{"SET"}, Parameters: []string{"work_mem"}, Roles: []string{"app_admin"}, WithGrant: true, GrantedBy: &adminRole},
		},
		Revocations: []ir.ParameterRevocation{
			{Privileges: []string{"SET"}, Parameters: []string{"work_mem"}, Roles: []string{"app_readonly"}, Cascade: true, GrantedBy: &adminRole},
		},
	}

	var b strings.Builder
	renderObjectDPG(&b, pp, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "GRANTED BY admin_role") {
		t.Errorf("expected GRANTED BY in rendered output, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped parameter privileges failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.ParameterPrivileges
	for _, o := range compiled {
		if p, ok := o.(*ir.ParameterPrivileges); ok {
			found = p
		}
	}
	if found == nil {
		t.Fatalf("parameter privileges object missing after recompile\n---\n%s", rendered)
	}
	if len(found.Grants) != 1 || found.Grants[0].GrantedBy == nil || *found.Grants[0].GrantedBy != "admin_role" {
		t.Errorf("Grant GrantedBy did not round-trip: %+v", found.Grants)
	}
	if len(found.Revocations) != 1 || found.Revocations[0].GrantedBy == nil || *found.Revocations[0].GrantedBy != "admin_role" {
		t.Errorf("Revocation GrantedBy did not round-trip: %+v", found.Revocations)
	}
}

// TestRenderForeignDataWrapperOwnerRoundtrip guards the lower-priority
// fresh-audit finding that ir.ForeignDataWrapper had no Owner support at
// all, unlike ForeignServer (its closest sibling opaque kind).
func TestRenderForeignDataWrapperOwnerRoundtrip(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	owner := "alice"
	fdw := &ir.ForeignDataWrapper{Name: "myfdw", Body: "CREATE FOREIGN DATA WRAPPER myfdw", Owner: &owner}

	var b strings.Builder
	renderObjectDPG(&b, fdw, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "OWNER alice") {
		t.Errorf("expected OWNER in rendered output, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped fdw failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.ForeignDataWrapper
	for _, o := range compiled {
		if f2, ok := o.(*ir.ForeignDataWrapper); ok {
			found = f2
		}
	}
	if found == nil {
		t.Fatalf("fdw object missing after recompile\n---\n%s", rendered)
	}
	if found.Owner == nil || *found.Owner != "alice" {
		t.Errorf("Owner did not round-trip: %v", found.Owner)
	}
}

// TestRenderSubscriptionOwnerRoundtrip is TestRenderForeignDataWrapperOwnerRoundtrip's
// Subscription counterpart.
func TestRenderSubscriptionOwnerRoundtrip(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	owner := "alice"
	sub := &ir.Subscription{Name: "my_sub", Body: "CREATE SUBSCRIPTION my_sub CONNECTION 'host=x' PUBLICATION my_pub", Owner: &owner}

	var b strings.Builder
	renderObjectDPG(&b, sub, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "OWNER alice") {
		t.Errorf("expected OWNER in rendered output, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped subscription failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.Subscription
	for _, o := range compiled {
		if s2, ok := o.(*ir.Subscription); ok {
			found = s2
		}
	}
	if found == nil {
		t.Fatalf("subscription object missing after recompile\n---\n%s", rendered)
	}
	if found.Owner == nil || *found.Owner != "alice" {
		t.Errorf("Owner did not round-trip: %v", found.Owner)
	}
}

// TestRenderExtensionCommentRoundtrip guards a real gap: ir.Extension had no
// Comment field at all, so a declared COMMENT was silently dropped by dump
// (and by build/snapshot/diff before this fix) even though COMMENT ON
// EXTENSION is valid PostgreSQL syntax.
func TestRenderExtensionCommentRoundtrip(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	comment := "crypto functions"
	ext := &ir.Extension{Name: "pgcrypto", Comment: &comment}

	var b strings.Builder
	renderObjectDPG(&b, ext, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, `EXTENSION pgcrypto {`) {
		t.Errorf("expected EXTENSION pgcrypto block, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "COMMENT 'crypto functions';") {
		t.Errorf("expected COMMENT directive, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped extension failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.Extension
	for _, o := range compiled {
		if e, ok := o.(*ir.Extension); ok && e.Name == "pgcrypto" {
			found = e
		}
	}
	if found == nil {
		t.Fatalf("extension pgcrypto missing after recompile\n---\n%s", rendered)
	}
	if found.Comment == nil || *found.Comment != comment {
		t.Errorf("Comment did not round-trip: %v", found.Comment)
	}
}

func TestRenderAggregateCompiles(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	comment := "multiplicative aggregate"
	agg := &ir.Aggregate{
		Schema: "public", Name: "amount_product",
		Args: []ir.FuncArg{{Type: ir.TypeRef{Name: "numeric"}}},
		Options: []pipeline.StorageParam{
			{Key: "sfunc", Value: "numeric_mul"},
			{Key: "stype", Value: "numeric"},
			{Key: "initcond", Value: "'1'"},
		},
		Comment:     &comment,
		Grants:      []ir.Grant{{Privileges: []string{"EXECUTE"}, Roles: []string{"app_service"}}},
		Revocations: []ir.Revocation{{Privileges: []string{"EXECUTE"}, Roles: []string{"PUBLIC"}}},
	}

	var b strings.Builder
	renderObjectDPG(&b, agg, fmtOpts)
	rendered := b.String()

	if strings.Contains(rendered, "CREATE AGGREGATE") {
		t.Errorf("DPG source must not begin a declaration with CREATE, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "AGGREGATE amount_product(numeric) (SFUNC = numeric_mul, STYPE = numeric, INITCOND = '1')") {
		t.Errorf("expected the AGGREGATE declaration with its options, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped aggregate failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.Aggregate
	for _, o := range compiled {
		if a, ok := o.(*ir.Aggregate); ok && a.Name == "amount_product" {
			found = a
		}
	}
	if found == nil {
		t.Fatalf("aggregate amount_product missing after recompile\n---\n%s", rendered)
	}
	if len(found.Args) != 1 || found.Args[0].Type.Name != "numeric" {
		t.Errorf("Args did not round-trip: %+v", found.Args)
	}
	if found.Comment == nil || *found.Comment != comment {
		t.Errorf("Comment did not round-trip: %v", found.Comment)
	}
	if len(found.Grants) != 1 {
		t.Errorf("Grants did not round-trip: %v", found.Grants)
	}
	if len(found.Revocations) != 1 || found.Revocations[0].Roles[0] != "PUBLIC" {
		t.Errorf("Revocations did not round-trip: %v", found.Revocations)
	}
	if !strings.Contains(found.Body, "sfunc = numeric_mul") || !strings.Contains(found.Body, "stype = numeric") {
		t.Errorf("Body should carry the recompiled options, got %q", found.Body)
	}
}

// TestRenderFunctionParallelCostRowsRoundtrip guards the full render →
// recompile chain for PARALLEL/COST/ROWS specifically — previously not
// rendered by dump at all despite ir.FuncAttrs already having fields for
// them. Covers both the explicit-value case and the default-suppression
// case (PARALLEL UNSAFE and unset Cost/Rows must render nothing, matching
// the same "don't render PostgreSQL's own default" precedent already
// established for column STORAGE).
func TestRenderFunctionParallelCostRowsRoundtrip(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	cost := 500.0
	rows := 50.0
	objs := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f_explicit",
			ReturnType: ir.TypeRef{Name: "integer"},
			Attrs: ir.FuncAttrs{
				Language: "sql", Volatility: "STABLE", Parallel: "SAFE",
				Cost: &cost, Rows: &rows, Body: "SELECT 1",
			},
		},
		&ir.Function{
			Schema: "public", Name: "f_default",
			ReturnType: ir.TypeRef{Name: "integer"},
			Attrs:      ir.FuncAttrs{Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", Body: "SELECT 1"},
		},
	}
	var b strings.Builder
	for _, o := range objs {
		renderObjectDPG(&b, o, fmtOpts)
	}
	rendered := b.String()

	if !strings.Contains(rendered, "PARALLEL SAFE") {
		t.Errorf("expected PARALLEL SAFE rendered for f_explicit, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "COST 500") {
		t.Errorf("expected COST 500 rendered for f_explicit, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "ROWS 50") {
		t.Errorf("expected ROWS 50 rendered for f_explicit, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("did not recompile: %v\n---\n%s", err, rendered)
	}
	for _, o := range compiled {
		fn, ok := o.(*ir.Function)
		if !ok {
			continue
		}
		switch fn.Name {
		case "f_explicit":
			if fn.Attrs.Parallel != "SAFE" {
				t.Errorf("f_explicit Parallel round-trip: got %q", fn.Attrs.Parallel)
			}
			if fn.Attrs.Cost == nil || *fn.Attrs.Cost != 500 {
				t.Errorf("f_explicit Cost round-trip: got %v", fn.Attrs.Cost)
			}
			if fn.Attrs.Rows == nil || *fn.Attrs.Rows != 50 {
				t.Errorf("f_explicit Rows round-trip: got %v", fn.Attrs.Rows)
			}
		case "f_default":
			if strings.Contains(rendered[strings.Index(rendered, "f_default"):], "PARALLEL") {
				t.Errorf("f_default must not render PARALLEL (matches PostgreSQL's own UNSAFE default)")
			}
			if fn.Attrs.Cost != nil {
				t.Errorf("f_default Cost: got %v, want nil (never rendered, so never re-parsed)", fn.Attrs.Cost)
			}
			if fn.Attrs.Rows != nil {
				t.Errorf("f_default Rows: got %v, want nil", fn.Attrs.Rows)
			}
		}
	}
}

// TestRenderFunctionSetOfRoundtrip guards the render -> recompile chain for
// RETURNS SETOF specifically: ReturnType.SetOf (pg_query's TypeName.Setof)
// was previously never read anywhere in the codebase, so dump never rendered
// SETOF and the compiler silently dropped it from hand-written source too.
// Also covers the plain (non-SETOF) case as a negative control.
func TestRenderFunctionSetOfRoundtrip(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	objs := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f_setof",
			ReturnType: ir.TypeRef{Name: "integer", SetOf: true},
			Attrs:      ir.FuncAttrs{Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", Body: "SELECT n"},
		},
		&ir.Function{
			Schema: "public", Name: "f_plain",
			ReturnType: ir.TypeRef{Name: "integer", SetOf: false},
			Attrs:      ir.FuncAttrs{Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", Body: "SELECT n"},
		},
	}
	var b strings.Builder
	for _, o := range objs {
		renderObjectDPG(&b, o, fmtOpts)
	}
	rendered := b.String()

	if !strings.Contains(rendered, "RETURNS SETOF integer") {
		t.Errorf("expected RETURNS SETOF integer rendered for f_setof, got:\n%s", rendered)
	}
	if strings.Contains(rendered[strings.Index(rendered, "f_plain"):], "SETOF") {
		t.Errorf("f_plain must not render SETOF, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("did not recompile: %v\n---\n%s", err, rendered)
	}
	for _, o := range compiled {
		fn, ok := o.(*ir.Function)
		if !ok {
			continue
		}
		switch fn.Name {
		case "f_setof":
			if !fn.ReturnType.SetOf {
				t.Error("f_setof ReturnType.SetOf round-trip: got false, want true")
			}
		case "f_plain":
			if fn.ReturnType.SetOf {
				t.Error("f_plain ReturnType.SetOf round-trip: got true, want false")
			}
		}
	}
}

// TestRenderFunctionReturnsTableRoundtrip guards the render -> recompile
// chain for RETURNS TABLE(...) specifically: TABLE-mode Args were previously
// rendered inline in the main parameter list as an invalid "TABLE a integer"
// literal (PostgreSQL rejects this as a syntax error) instead of inside a
// separate RETURNS TABLE(...) clause.
func TestRenderFunctionReturnsTableRoundtrip(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	objs := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f_table",
			ReturnType: ir.TypeRef{Name: "record", SetOf: true},
			Args: []ir.FuncArg{
				{Name: "n", Mode: "IN", Type: ir.TypeRef{Name: "integer"}},
				{Name: "a", Mode: "TABLE", Type: ir.TypeRef{Name: "integer"}},
				{Name: "b", Mode: "TABLE", Type: ir.TypeRef{Name: "text"}},
			},
			Attrs: ir.FuncAttrs{Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", Body: "SELECT 1, 'x'"},
		},
	}
	var b strings.Builder
	for _, o := range objs {
		renderObjectDPG(&b, o, fmtOpts)
	}
	rendered := b.String()

	if !strings.Contains(rendered, "(n integer) RETURNS TABLE(a integer, b text)") {
		t.Errorf("expected (n integer) RETURNS TABLE(a integer, b text), got:\n%s", rendered)
	}
	if strings.Contains(rendered, "TABLE a integer") || strings.Contains(rendered, "TABLE b text") {
		t.Errorf("TABLE-mode columns must not render inline in the parameter list, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("did not recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.Function
	for _, o := range compiled {
		if fn, ok := o.(*ir.Function); ok && fn.Name == "f_table" {
			found = fn
		}
	}
	if found == nil {
		t.Fatal("f_table not found after recompile")
	}
	cols := ir.FuncTableColumns(found.Args)
	if got := ir.FormatTableColumns(cols); got != "a integer, b text" {
		t.Errorf("f_table TABLE column round-trip: got %q, want %q", got, "a integer, b text")
	}
}

// TestRenderIndexVariantsRoundtrip guards the full render → recompile → createIndex
// chain for every index variant. Apply runs createIndex's SQL, so asserting it
// here (fast) is equivalent to dump → apply for the index class. Regressions in
// this chain (sort-order lost on parse, INCLUDE dropped, columns double-quoted)
// are invisible to plan and only surface on apply.
func TestRenderIndexVariantsRoundtrip(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	where := "b > 0"
	tbl := &ir.Table{
		Schema: "public", Name: "t",
		Columns: []*ir.Column{
			{Name: "a", Type: ir.TypeRef{Name: "int4"}},
			{Name: "b", Type: ir.TypeRef{Name: "int4"}},
			{Name: "MixedCol", Type: ir.TypeRef{Name: "text"}},
		},
		Indexes: []*ir.Index{
			{Name: "i_uniq", Unique: true, Columns: []pipeline.IndexColumn{{Name: "a"}}},
			{Name: "i_sort", Columns: []pipeline.IndexColumn{{Name: "MixedCol", SortOrder: "DESC", Nulls: "LAST"}, {Name: "b", SortOrder: "ASC"}}},
			{Name: "i_partial", Columns: []pipeline.IndexColumn{{Name: "b"}}, Where: &where},
			{Name: "i_cover", Columns: []pipeline.IndexColumn{{Name: "a"}}, Include: []string{"MixedCol", "b"}},
			{Name: "i_expr", Columns: []pipeline.IndexColumn{{Expr: &pipeline.RawExpr{Text: "lower(\"MixedCol\")"}}}},
			{Name: "i_gin", Method: "gin", Columns: []pipeline.IndexColumn{{Expr: &pipeline.RawExpr{Text: "to_tsvector('english', \"MixedCol\")"}}}},
			{Name: "i_nulls_nd", Unique: true, Columns: []pipeline.IndexColumn{{Name: "a"}}, NullsNotDistinct: true},
			{Name: "i_with", Columns: []pipeline.IndexColumn{{Name: "a"}}, With: []pipeline.StorageParam{{Key: "fillfactor", Value: "70"}}},
			{Name: "i_opclass", Columns: []pipeline.IndexColumn{{
				Name:          "MixedCol",
				Collation:     &pipeline.Identifier{Name: "C"},
				OpClass:       &pipeline.Identifier{Name: "text_pattern_ops"},
				OpClassParams: []pipeline.StorageParam{{Key: "siglen", Value: "32"}},
				SortOrder:     "DESC",
			}}},
			{Name: "i_only", Only: true, Columns: []pipeline.IndexColumn{{Name: "a"}}},
		},
	}
	var b strings.Builder
	renderObjectDPG(&b, tbl, fmtOpts)
	rendered := b.String()

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("index variants did not recompile: %v\n---\n%s", err, rendered)
	}
	ops, err := diff.New().Diff(compiled, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]string{}
	for _, o := range ops {
		sql := o.SQL()
		for _, n := range []string{"i_uniq", "i_sort", "i_partial", "i_cover", "i_expr", "i_gin", "i_nulls_nd", "i_with", "i_opclass", "i_only"} {
			if strings.Contains(sql, `"`+n+`"`) {
				byName[n] = sql
			}
		}
	}
	// Each variant's generated SQL must be correct — no double-quoting, sort-order
	// preserved, INCLUDE present, WHERE present, method carried.
	checks := map[string][]string{
		"i_uniq":     {`CREATE UNIQUE INDEX "i_uniq"`, `("a")`},
		"i_sort":     {`"MixedCol" DESC NULLS LAST`, `"b" ASC`},
		"i_partial":  {`("b")`, `WHERE b > 0`},
		"i_cover":    {`("a")`, `INCLUDE ("MixedCol", "b")`},
		"i_expr":     {`(lower("MixedCol"))`},
		"i_gin":      {`USING gin`, `to_tsvector('english', "MixedCol")`},
		"i_nulls_nd": {`CREATE UNIQUE INDEX "i_nulls_nd"`, `NULLS NOT DISTINCT`},
		"i_with":     {`("a")`, `WITH (fillfactor=70)`},
		"i_opclass":  {`"MixedCol" COLLATE "C" text_pattern_ops(siglen = 32) DESC`},
		"i_only":     {`ON ONLY "public"."t"`},
	}
	for name, wants := range checks {
		sql, ok := byName[name]
		if !ok {
			t.Errorf("%s: no CREATE INDEX op emitted", name)
			continue
		}
		if strings.Contains(sql, `"""`) {
			t.Errorf("%s: double-quoted identifier: %s", name, sql)
		}
		for _, w := range wants {
			if !strings.Contains(sql, w) {
				t.Errorf("%s: missing %q in: %s", name, w, sql)
			}
		}
	}
}

// TestRenderPolicyAndTriggerRoundtrip guards a real bug found live-testing a
// demo project: renderObjectDPG's Table case had no rendering path at all
// for POLICIES/TRIGGERS — despite both being genuinely diffed
// (diffPolicies/diffTriggers) and correctly introspected
// (introspectPolicies/introspectTriggers), a live table's RLS policy and
// trigger silently vanished from a dumped project entirely (confirmed
// against a real demo table: orders.owner_only / orders.set_updated_at).
func TestRenderPolicyAndTriggerRoundtrip(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	using := "user_id = current_setting('app.current_user_id', true)::bigint"
	withCheck := "user_id = current_setting('app.current_user_id', true)::bigint"
	whenCond := "OLD.status IS DISTINCT FROM NEW.status"
	tbl := &ir.Table{
		Schema: "public", Name: "orders",
		Columns: []*ir.Column{
			{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
			{Name: "user_id", Type: ir.TypeRef{Name: "bigint"}},
			{Name: "status", Type: ir.TypeRef{Name: "text"}},
		},
		Policies: []*ir.Policy{
			{Name: "owner_only", Command: "ALL", Permissive: true, Using: &using},
			{Name: "admin_write", Command: "INSERT", Permissive: false, Roles: []string{"admin_role"}, WithCheck: &withCheck},
		},
		Triggers: []*ir.Trigger{
			{Name: "set_updated_at", When: "BEFORE", Events: []string{"UPDATE"}, ForEach: "ROW", Function: "touch_updated_at"},
			{Name: "audit_status", When: "AFTER", Events: []string{"UPDATE"}, ForEach: "ROW", Condition: &whenCond, Function: "audit_status_change", Args: []string{"'orders'"}},
		},
	}

	var b strings.Builder
	renderObjectDPG(&b, tbl, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "POLICIES {") || !strings.Contains(rendered, "TRIGGERS {") {
		t.Fatalf("expected POLICIES and TRIGGERS blocks, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "owner_only FOR ALL USING") {
		t.Errorf("expected permissive ALL policy without AS clause, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "admin_write AS RESTRICTIVE FOR INSERT TO admin_role WITH CHECK") {
		t.Errorf("expected restrictive policy with TO/WITH CHECK, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "set_updated_at BEFORE UPDATE FOR EACH ROW EXECUTE FUNCTION touch_updated_at();") {
		t.Errorf("expected simple trigger, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "audit_status AFTER UPDATE FOR EACH ROW WHEN") || !strings.Contains(rendered, "EXECUTE FUNCTION audit_status_change('orders');") {
		t.Errorf("expected WHEN-conditioned trigger with args, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped policies/triggers failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.Table
	for _, o := range compiled {
		if tb, ok := o.(*ir.Table); ok && tb.Name == "orders" {
			found = tb
		}
	}
	if found == nil {
		t.Fatalf("table orders missing after recompile\n---\n%s", rendered)
	}
	if len(found.Policies) != 2 {
		t.Fatalf("Policies did not round-trip: got %d, want 2", len(found.Policies))
	}
	if len(found.Triggers) != 2 {
		t.Fatalf("Triggers did not round-trip: got %d, want 2", len(found.Triggers))
	}
	for _, p := range found.Policies {
		if p.Name == "admin_write" {
			if p.Permissive {
				t.Error("admin_write: Permissive did not round-trip (should be RESTRICTIVE)")
			}
			if len(p.Roles) != 1 || p.Roles[0] != "admin_role" {
				t.Errorf("admin_write: Roles did not round-trip: %v", p.Roles)
			}
			if p.WithCheck == nil {
				t.Error("admin_write: WithCheck did not round-trip")
			}
		}
	}
	for _, trg := range found.Triggers {
		if trg.Name == "audit_status" {
			if trg.Condition == nil {
				t.Error("audit_status: Condition (WHEN) did not round-trip")
			}
			if len(trg.Args) != 1 || trg.Args[0] != "'orders'" {
				t.Errorf("audit_status: Args did not round-trip: %v", trg.Args)
			}
		}
	}
}

// TestRenderPolicyRenamedFromRoundtrip guards RFC Section 7.8's policy
// RENAMED FROM clause — renderPolicy previously had no rendering path for
// it at all, despite it now being genuinely diffed (diffPolicies).
func TestRenderPolicyRenamedFromRoundtrip(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	renamedFrom := "view_own"
	tbl := &ir.Table{
		Schema: "public", Name: "orders",
		Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
		Policies: []*ir.Policy{
			{Name: "view_self", Command: "SELECT", Permissive: true, RenamedFrom: &renamedFrom},
		},
	}

	var b strings.Builder
	renderObjectDPG(&b, tbl, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "RENAMED FROM view_own") {
		t.Fatalf("expected RENAMED FROM view_own, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped policy failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.Table
	for _, o := range compiled {
		if tb, ok := o.(*ir.Table); ok && tb.Name == "orders" {
			found = tb
		}
	}
	if found == nil || len(found.Policies) != 1 || found.Policies[0].RenamedFrom == nil || *found.Policies[0].RenamedFrom != "view_own" {
		t.Errorf("Policy RenamedFrom did not round-trip: %+v", found)
	}
}

// TestRenderEventTriggerEnableStateRoundtrip guards RFC Section 14.1's
// DISABLED/ENABLE REPLICA/ENABLE ALWAYS block directive (audit item #76's
// remainder) — renderEventTriggerBody previously never rendered it at all,
// so a `dpg dump` of a disabled event trigger would have silently dropped
// that state, and the trigger would come back enabled on the next apply.
func TestRenderEventTriggerEnableStateRoundtrip(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	evt := &ir.EventTrigger{
		Name: "audit_ddl", Event: "ddl_command_end", Function: "public.log_ddl",
		Body:        "CREATE EVENT TRIGGER audit_ddl ON ddl_command_end EXECUTE FUNCTION public.log_ddl()",
		EnableState: "ENABLE REPLICA",
	}

	var b strings.Builder
	renderObjectDPG(&b, evt, fmtOpts)
	rendered := b.String()

	if !strings.Contains(rendered, "ENABLE REPLICA;") {
		t.Fatalf("expected ENABLE REPLICA, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped event trigger failed to recompile: %v\n---\n%s", err, rendered)
	}
	var found *ir.EventTrigger
	for _, o := range compiled {
		if et, ok := o.(*ir.EventTrigger); ok && et.Name == "audit_ddl" {
			found = et
		}
	}
	if found == nil || found.EnableState != "ENABLE REPLICA" {
		t.Errorf("EnableState did not round-trip: %+v", found)
	}
}

// TestRenderExcludeConstraintRoundtrip proves dump can reconstruct a real
// EXCLUDE constraint into valid, recompilable DPG source. EXCLUDE isn't in
// isInlineable, so it must render via the generic table-level constraint
// path (CONSTRAINT name <Expr>) — this exercises that path with an Expr
// that's genuinely populated (access method, two elements + operators, a
// WHERE clause) rather than the old bare "EXCLUDE" placeholder, which would
// have failed to even parse back.
func TestRenderExcludeConstraintRoundtrip(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	tbl := &ir.Table{
		Schema: "public", Name: "bookings",
		Columns: []*ir.Column{
			{Name: "room", Type: ir.TypeRef{Name: "int4"}},
			{Name: "during", Type: ir.TypeRef{Name: "tsrange"}},
		},
		Constraints: []*ir.Constraint{
			{Name: "no_overlap", Type: "EXCLUDE", Columns: []string{"room", "during"},
				Exclude: &ir.ExcludeSpec{
					AccessMethod: "gist",
					Elements: []ir.ExcludeElement{
						{Column: "room", Operator: "="},
						{Column: "during", Operator: "&&"},
					},
					Where: "room > 0",
				},
				Expr: `EXCLUDE USING gist ("room" WITH =, "during" WITH &&) WHERE (room > 0)`,
			},
		},
	}
	var b strings.Builder
	renderObjectDPG(&b, tbl, fmtOpts)
	rendered := b.String()

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("EXCLUDE constraint did not recompile: %v\n---\n%s", err, rendered)
	}
	ops, err := diff.New().Diff(compiled, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 {
		t.Fatal("expected a CREATE TABLE op")
	}
	sql := ops[0].SQL()
	if !strings.Contains(sql, `CONSTRAINT "no_overlap" EXCLUDE USING gist ("room" WITH =, "during" WITH &&) WHERE (room > 0)`) {
		t.Errorf("expected the real EXCLUDE body to survive render+recompile, got: %s", sql)
	}
}

// ── dumpClusterTargets (dpg dump -o sandboxing) ─────────────────────────────

// TestDumpClusterTargetsNoOutputDirUsesRealLocations proves that without -o,
// dumpClusterTargets returns exactly the real, permanent project locations
// unchanged — a pure regression guard that the fix below doesn't alter the
// default (no -o) behavior at all.
func TestDumpClusterTargetsNoOutputDirUsesRealLocations(t *testing.T) {
	realStore := &snapshot.FileStore{Dir: "/real/project/.dpg/snapshots"}
	cl := &project.Cluster{ObjectsDir: "/real/project/cluster1/cluster"}

	clusterOut, dumpStore := dumpClusterTargets(cl, realStore, "")

	if clusterOut != cl.ObjectsDir {
		t.Errorf("clusterOut without -o: got %q, want cl.ObjectsDir %q", clusterOut, cl.ObjectsDir)
	}
	if dumpStore != pipeline.SnapshotStore(realStore) {
		t.Error("dumpStore without -o: expected the exact same store instance passed in, got a different one")
	}
}

// TestDumpClusterTargetsWithOutputDirSandboxes proves that with -o set,
// dumpClusterTargets redirects BOTH cluster-scoped output and the snapshot
// under outputDir instead of the real project — this is the actual fix:
// previously dump.go always used cl.ObjectsDir and the registered store
// unconditionally, regardless of -o.
func TestDumpClusterTargetsWithOutputDirSandboxes(t *testing.T) {
	realStore := &snapshot.FileStore{Dir: "/real/project/.dpg/snapshots"}
	cl := &project.Cluster{
		Config:     config.ClusterConfig{Cluster: config.ClusterDef{Name: "cluster1"}},
		ObjectsDir: "/real/project/cluster1/cluster",
	}

	clusterOut, dumpStore := dumpClusterTargets(cl, realStore, "/scratch/out")

	wantClusterOut := filepath.Join("/scratch/out", "cluster1", "cluster")
	if clusterOut != wantClusterOut {
		t.Errorf("clusterOut with -o: got %q, want %q", clusterOut, wantClusterOut)
	}
	if clusterOut == cl.ObjectsDir {
		t.Error("clusterOut with -o must never equal the real project's cl.ObjectsDir")
	}

	fs, ok := dumpStore.(*snapshot.FileStore)
	if !ok {
		t.Fatalf("dumpStore with -o: expected *snapshot.FileStore, got %T", dumpStore)
	}
	wantStoreDir := filepath.Join("/scratch/out", ".dpg", "snapshots")
	if fs.Dir != wantStoreDir {
		t.Errorf("dumpStore.Dir with -o: got %q, want %q", fs.Dir, wantStoreDir)
	}
	if fs.Dir == realStore.Dir {
		t.Error("dumpStore with -o must never write to the real project's snapshot store directory")
	}
}

// TestDumpClusterTargetsMultipleClustersNamespaced proves two different
// clusters sharing the same -o value get distinct clusterOut paths (each
// namespaced by cluster name) — otherwise a single dump invocation spanning
// multiple clusters would silently mix their roles.dpg content together,
// a regression this fix must not introduce relative to the pre-fix
// behavior (which always wrote each cluster to its own distinct real
// ObjectsDir and never had this collision risk at all).
func TestDumpClusterTargetsMultipleClustersNamespaced(t *testing.T) {
	store := &snapshot.FileStore{Dir: "/real/.dpg/snapshots"}
	cl1 := &project.Cluster{Config: config.ClusterConfig{Cluster: config.ClusterDef{Name: "cluster1"}}}
	cl2 := &project.Cluster{Config: config.ClusterConfig{Cluster: config.ClusterDef{Name: "cluster2"}}}

	out1, _ := dumpClusterTargets(cl1, store, "/scratch/out")
	out2, _ := dumpClusterTargets(cl2, store, "/scratch/out")

	if out1 == out2 {
		t.Errorf("two different clusters sharing one -o value must not resolve to the same clusterOut, both got %q", out1)
	}
}

// ── dumpDatabaseOutputDir (dpg dump default output path) ────────────────────

// TestDumpDatabaseOutputDirNoOutputDirUsesRealDatabaseDirectory proves the
// fix: without -o, the default output path must be db.Dir — the database's
// own already-resolved real directory — not a path reconstructed from
// declared names. Before this fix, a database whose directory name diverged
// from its declared [database] name (a rename, a copy-pasted template, or a
// typo) would silently get dumped into a brand-new, disconnected sibling
// directory instead of its real one.
func TestDumpDatabaseOutputDirNoOutputDirUsesRealDatabaseDirectory(t *testing.T) {
	db := &project.Database{
		Dir:    "/real/project/cluster1/mydb_v2",
		Config: config.DatabaseConfig{Database: config.DatabaseDef{Name: "dupdb1"}},
	}

	out := dumpDatabaseOutputDir("", db)

	if out != db.Dir {
		t.Errorf("dumpDatabaseOutputDir(\"\", db): got %q, want db.Dir %q", out, db.Dir)
	}
	nameDerivedPath := filepath.Join("cluster1", "dupdb1")
	if out == nameDerivedPath {
		t.Errorf("dumpDatabaseOutputDir must not reconstruct a name-derived path, got %q", out)
	}
}

// TestDumpDatabaseOutputDirWithOutputDirUsesOutputDirUnchanged proves -o's
// existing behavior is untouched by this fix: with -o set, the caller's
// value is used verbatim, regardless of db.Dir.
func TestDumpDatabaseOutputDirWithOutputDirUsesOutputDirUnchanged(t *testing.T) {
	db := &project.Database{
		Dir:    "/real/project/cluster1/mydb_v2",
		Config: config.DatabaseConfig{Database: config.DatabaseDef{Name: "dupdb1"}},
	}

	out := dumpDatabaseOutputDir("/scratch/out", db)

	if out != "/scratch/out" {
		t.Errorf("dumpDatabaseOutputDir(\"/scratch/out\", db): got %q, want %q", out, "/scratch/out")
	}
}

// ── confirmOverwrite (dpg dump overwrite-protection) ────────────────────────

// TestConfirmOverwriteNoExistingFilesNoPrompt proves the common, safe case
// (bootstrapping a brand-new project, or a fresh scratch -o directory) is
// completely frictionless: no existing files means no warning, no prompt,
// nil error, and — critically — no attempt to read from r at all (a nil
// reader would panic bufio.NewScanner if confirmOverwrite ever tried).
func TestConfirmOverwriteNoExistingFilesNoPrompt(t *testing.T) {
	dir := t.TempDir()
	var out strings.Builder
	err := confirmOverwrite(&out, nil, []string{filepath.Join(dir, "does-not-exist.dpg")}, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("expected no output when nothing exists yet, got: %s", out.String())
	}
}

// TestConfirmOverwriteEmptyPathsNoPrompt is the same guarantee for a nil/
// empty candidate list (e.g. a dump that wrote nothing at all this call).
func TestConfirmOverwriteEmptyPathsNoPrompt(t *testing.T) {
	var out strings.Builder
	if err := confirmOverwrite(&out, nil, nil, false, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("expected no output for an empty path list, got: %s", out.String())
	}
}

// TestConfirmOverwriteSkipConfirmStillWarns proves --yes (skipConfirm) still
// prints the loud warning listing every file — silently skipping only the
// interactive prompts, never the visibility — so non-interactive/CI use
// still surfaces exactly what got overwritten.
func TestConfirmOverwriteSkipConfirmStillWarns(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := confirmOverwrite(&out, nil, []string{existing}, true, false); err != nil {
		t.Fatalf("unexpected error with skipConfirm: %v", err)
	}
	if !strings.Contains(out.String(), existing) {
		t.Errorf("expected the warning to name %s even with --yes, got: %s", existing, out.String())
	}
	if !strings.Contains(out.String(), "WARNING") {
		t.Errorf("expected a WARNING banner even with --yes, got: %s", out.String())
	}
}

// TestConfirmOverwriteInteractiveBothConfirmationsSucceed proves the full
// double-confirmation path: "y" to the first prompt, then the literal word
// "overwrite" to the second, distinct prompt — both required.
func TestConfirmOverwriteInteractiveBothConfirmationsSucceed(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	in := strings.NewReader("y\noverwrite\n")
	if err := confirmOverwrite(&out, in, []string{existing}, false, false); err != nil {
		t.Fatalf("expected success after both confirmations, got: %v", err)
	}
}

// TestConfirmOverwriteDeclineFirstPrompt proves "n" (or anything other than
// "y") at the first prompt aborts immediately — the second prompt is never
// reached (confirmed by the reader having nothing left, since only one line
// was ever provided; a second Scan() call would return false/EOF, which the
// "not \"overwrite\"" branch would also reject, but we want to fail on the
// FIRST prompt specifically here).
func TestConfirmOverwriteDeclineFirstPrompt(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	in := strings.NewReader("n\n")
	err := confirmOverwrite(&out, in, []string{existing}, false, false)
	if err == nil {
		t.Fatal("expected an error when the user declines the first prompt")
	}
	if !strings.Contains(err.Error(), "aborted") {
		t.Errorf("expected an 'aborted' error, got: %v", err)
	}
}

// TestConfirmOverwriteWrongSecondConfirmationText proves saying "y" to the
// first prompt but typing anything other than the exact word "overwrite" to
// the second still aborts — the two prompts are independently enforced, not
// just two chances to say yes.
func TestConfirmOverwriteWrongSecondConfirmationText(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	in := strings.NewReader("y\nyes\n")
	err := confirmOverwrite(&out, in, []string{existing}, false, false)
	if err == nil {
		t.Fatal("expected an error when the second confirmation text doesn't match \"overwrite\" exactly")
	}
}

// TestConfirmOverwriteEOFAborts proves an unexpectedly-closed input stream
// (e.g. dump run with stdin redirected from /dev/null in a broken script)
// aborts safely rather than panicking or silently proceeding.
func TestConfirmOverwriteEOFAborts(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	in := strings.NewReader("") // immediate EOF
	err := confirmOverwrite(&out, in, []string{existing}, false, false)
	if err == nil {
		t.Fatal("expected an error on immediate EOF, not silent success")
	}
}

// TestConfirmOverwriteOnlyExistingPathsWarned proves the warning lists only
// the paths that actually already exist, not every candidate path passed in
// (most of which, in the real dump flow, are new files that don't exist yet).
func TestConfirmOverwriteOnlyExistingPathsWarned(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "exists.dpg")
	missing := filepath.Join(dir, "does-not-exist.dpg")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := confirmOverwrite(&out, nil, []string{existing, missing}, true, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), existing) {
		t.Errorf("expected warning to mention the existing file %s, got: %s", existing, out.String())
	}
	if strings.Contains(out.String(), missing) {
		t.Errorf("expected warning NOT to mention the non-existent file %s, got: %s", missing, out.String())
	}
}

// TestRenderSecurityLabelsRoundtrip guards RFC Section 14.11 across every kind
// PostgreSQL's real SECURITY LABEL statement supports: previously no IR
// kind had a SecurityLabels field at all, so a declared SECURITY LABEL
// directive had nowhere to go at any layer. One multi-provider entry per
// kind (plus a second, unqualified-provider entry on Table) proves both the
// FOR provider and bare forms render and recompile correctly.
func TestRenderSecurityLabelsRoundtrip(t *testing.T) {
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	sl := func(provider, label string) []pipeline.SecurityLabel {
		return []pipeline.SecurityLabel{{Provider: provider, Label: label}}
	}
	objs := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "t",
			Columns:        []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "integer"}, SecurityLabels: sl("dummy", "secret")}},
			SecurityLabels: []pipeline.SecurityLabel{{Provider: "dummy", Label: "classified"}, {Label: "unclassified"}},
		},
		&ir.View{Schema: "public", Name: "v", Query: "SELECT 1", SecurityLabels: sl("dummy", "classified")},
		&ir.View{Schema: "public", Name: "mv", Materialized: true, Query: "SELECT 1", SecurityLabels: sl("dummy", "classified")},
		&ir.Function{Schema: "public", Name: "f", ReturnType: ir.TypeRef{Name: "integer"},
			Attrs: ir.FuncAttrs{Language: "sql", Body: "SELECT 1"}, SecurityLabels: sl("dummy", "classified")},
		&ir.Procedure{Schema: "public", Name: "p",
			Attrs: ir.FuncAttrs{Language: "sql", Body: "SELECT 1"}, SecurityLabels: sl("dummy", "classified")},
		&ir.Aggregate{Schema: "public", Name: "agg", Args: []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}},
			Options:        []pipeline.StorageParam{{Key: "sfunc", Value: "int4pl"}, {Key: "stype", Value: "integer"}},
			SecurityLabels: sl("dummy", "classified")},
		&ir.Type{Schema: "public", Name: "dom", Variant: "DOMAIN", DomainBaseType: ir.TypeRef{Name: "integer"}, SecurityLabels: sl("dummy", "classified")},
		&ir.Type{Schema: "public", Name: "en", Variant: "ENUM", EnumValues: []string{"a", "b"}, SecurityLabels: sl("dummy", "classified")},
		&ir.Schema{Name: "myschema", SecurityLabels: sl("dummy", "classified")},
		&ir.Sequence{Schema: "public", Name: "seq", SecurityLabels: sl("dummy", "classified")},
		&ir.Role{Name: "myrole", SecurityLabels: sl("dummy", "classified")},
		&ir.Tablespace{Name: "ts", Location: "/data/ts", Body: "CREATE TABLESPACE ts LOCATION '/data/ts'", SecurityLabels: sl("dummy", "classified")},
		&ir.Publication{Name: "pub", Body: "CREATE PUBLICATION pub FOR ALL TABLES", AllTables: true, SecurityLabels: sl("dummy", "classified")},
		&ir.Subscription{Name: "sub", Body: "CREATE SUBSCRIPTION sub CONNECTION 'x' PUBLICATION p", SecurityLabels: sl("dummy", "classified")},
		&ir.EventTrigger{Name: "evt", Event: "ddl_command_start", Function: "public.f",
			Body: "CREATE EVENT TRIGGER evt ON ddl_command_start EXECUTE FUNCTION public.f()", SecurityLabels: sl("dummy", "classified")},
	}

	var b strings.Builder
	for _, o := range objs {
		renderObjectDPG(&b, o, fmtOpts)
	}
	rendered := b.String()

	if !strings.Contains(rendered, "SECURITY LABEL FOR dummy 'classified';") {
		t.Fatalf("expected FOR-provider SECURITY LABEL directive, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "SECURITY LABEL 'unclassified';") {
		t.Fatalf("expected unqualified SECURITY LABEL directive, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "SECURITY LABEL FOR dummy 'secret';") {
		t.Fatalf("expected column-level SECURITY LABEL directive, got:\n%s", rendered)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "objects.dpg")
	if err := os.WriteFile(f, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("dumped SecurityLabels failed to recompile: %v\n---\n%s", err, rendered)
	}

	wantOne := func(kind string, labels []pipeline.SecurityLabel) {
		t.Helper()
		if len(labels) != 1 || labels[0].Provider != "dummy" || labels[0].Label != "classified" {
			t.Errorf("%s: SecurityLabels did not round-trip: %+v", kind, labels)
		}
	}

	found := map[string]bool{}
	for _, o := range compiled {
		switch v := o.(type) {
		case *ir.Table:
			if v.Name == "t" {
				found["table"] = true
				if len(v.SecurityLabels) != 2 {
					t.Errorf("table: SecurityLabels did not round-trip: %+v", v.SecurityLabels)
				}
				for _, c := range v.Columns {
					if c.Name == "id" {
						found["column"] = true
						if len(c.SecurityLabels) != 1 || c.SecurityLabels[0].Provider != "dummy" || c.SecurityLabels[0].Label != "secret" {
							t.Errorf("column: SecurityLabels did not round-trip: %+v", c.SecurityLabels)
						}
					}
				}
			}
		case *ir.View:
			if v.Name == "v" && !v.Materialized {
				found["view"] = true
				wantOne("view", v.SecurityLabels)
			}
			if v.Name == "mv" && v.Materialized {
				found["matview"] = true
				wantOne("matview", v.SecurityLabels)
			}
		case *ir.Function:
			found["function"] = true
			wantOne("function", v.SecurityLabels)
		case *ir.Procedure:
			found["procedure"] = true
			wantOne("procedure", v.SecurityLabels)
		case *ir.Aggregate:
			found["aggregate"] = true
			wantOne("aggregate", v.SecurityLabels)
		case *ir.Type:
			if v.Variant == "DOMAIN" {
				found["domain"] = true
				wantOne("domain", v.SecurityLabels)
			}
			if v.Variant == "ENUM" {
				found["enum"] = true
				wantOne("enum", v.SecurityLabels)
			}
		case *ir.Schema:
			if v.Name == "myschema" {
				found["schema"] = true
				wantOne("schema", v.SecurityLabels)
			}
		case *ir.Sequence:
			found["sequence"] = true
			wantOne("sequence", v.SecurityLabels)
		case *ir.Role:
			if v.Name == "myrole" {
				found["role"] = true
				wantOne("role", v.SecurityLabels)
			}
		case *ir.Tablespace:
			found["tablespace"] = true
			wantOne("tablespace", v.SecurityLabels)
		case *ir.Publication:
			found["publication"] = true
			wantOne("publication", v.SecurityLabels)
		case *ir.Subscription:
			found["subscription"] = true
			wantOne("subscription", v.SecurityLabels)
		case *ir.EventTrigger:
			found["event_trigger"] = true
			wantOne("event_trigger", v.SecurityLabels)
		}
	}

	for _, kind := range []string{"table", "column", "view", "matview", "function", "procedure",
		"aggregate", "domain", "enum", "schema", "sequence", "role", "tablespace",
		"publication", "subscription", "event_trigger"} {
		if !found[kind] {
			t.Errorf("%s: object missing after recompile", kind)
		}
	}
}
