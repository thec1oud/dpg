package executor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/pipeline"
)

// ── mock Conn / Tx ────────────────────────────────────────────────────────────

type mockTx struct {
	executed  []string
	commitErr error
	rollback  bool
}

func (t *mockTx) Exec(_ context.Context, sql string, _ ...any) (int64, error) {
	t.executed = append(t.executed, sql)
	return 0, nil
}
func (t *mockTx) Commit(_ context.Context) error   { return t.commitErr }
func (t *mockTx) Rollback(_ context.Context) error { t.rollback = true; return nil }

type mockConn struct {
	txn       *mockTx
	nonTxnSQL []string
	beginErr  error
}

func (c *mockConn) Exec(_ context.Context, sql string, _ ...any) (int64, error) {
	c.nonTxnSQL = append(c.nonTxnSQL, sql)
	return 0, nil
}
func (c *mockConn) Begin(_ context.Context) (pipeline.Tx, error) {
	if c.beginErr != nil {
		return nil, c.beginErr
	}
	c.txn = &mockTx{}
	return c.txn, nil
}
func (c *mockConn) Close(_ context.Context) error { return nil }

// ── test ops ──────────────────────────────────────────────────────────────────

type testOp struct {
	sql string
	txn bool
}

func (o testOp) SQL() string             { return o.sql }
func (o testOp) Safety() pipeline.Safety { return pipeline.Safe }
func (o testOp) Mode() pipeline.ExecMode {
	if o.txn {
		return pipeline.ModeTransactional
	}
	return pipeline.ModeNonTransactional
}
func (o testOp) Pos() pipeline.SourcePos { return pipeline.SourcePos{} }

// ── tests ─────────────────────────────────────────────────────────────────────

func TestApplyTransactional(t *testing.T) {
	conn := &mockConn{}
	e := New()
	m := pipeline.Migration{
		Transactional: []pipeline.DiffOp{testOp{"CREATE TABLE t (id int);", true}},
	}
	if err := e.Apply(context.Background(), m, conn); err != nil {
		t.Fatal(err)
	}
	if conn.txn == nil {
		t.Fatal("expected transaction to be started")
	}
	if len(conn.txn.executed) != 1 || conn.txn.executed[0] != "CREATE TABLE t (id int);" {
		t.Errorf("unexpected executed SQL: %v", conn.txn.executed)
	}
	if len(conn.nonTxnSQL) != 0 {
		t.Errorf("transactional op leaked into non-txn path: %v", conn.nonTxnSQL)
	}
}

func TestApplyNonTransactional(t *testing.T) {
	conn := &mockConn{}
	e := New()
	m := pipeline.Migration{
		NonTransactional: []pipeline.DiffOp{testOp{"CREATE INDEX CONCURRENTLY idx_status ON t (status);", false}},
	}
	if err := e.Apply(context.Background(), m, conn); err != nil {
		t.Fatal(err)
	}
	if conn.txn != nil {
		t.Fatal("expected no transaction for non-transactional ops")
	}
	if len(conn.nonTxnSQL) != 1 {
		t.Fatalf("expected 1 non-txn SQL, got %d", len(conn.nonTxnSQL))
	}
}

func TestApplyEmpty(t *testing.T) {
	conn := &mockConn{}
	e := New()
	if err := e.Apply(context.Background(), pipeline.Migration{}, conn); err != nil {
		t.Fatal(err)
	}
	if conn.txn != nil {
		t.Fatal("expected no transaction for empty migration")
	}
}

func TestApplyBeginError(t *testing.T) {
	conn := &mockConn{beginErr: errors.New("connection refused")}
	e := New()
	m := pipeline.Migration{
		Transactional: []pipeline.DiffOp{testOp{"CREATE TABLE t (id int);", true}},
	}
	if err := e.Apply(context.Background(), m, conn); err == nil {
		t.Fatal("expected error from Begin failure")
	}
}

func TestApplyRollbackOnExecError(t *testing.T) {
	conn := &mockConn{}
	// Use real Begin but have a tx that errors on exec.
	failTx := &mockTx{}
	conn.txn = failTx

	// Wrap a conn that uses our pre-built failing tx.
	type failConn struct{ mockConn }
	fc := &failConn{}
	fc.txn = &mockTx{}

	// Use a conn that returns an exec-failing tx.
	errConn := &errExecConn{}
	e := New()
	m := pipeline.Migration{
		Transactional: []pipeline.DiffOp{testOp{"BAD SQL", true}},
	}
	if err := e.Apply(context.Background(), m, errConn); err == nil {
		t.Fatal("expected error from exec failure")
	}
	if !errConn.txn.rollback {
		t.Error("expected rollback to be called on exec failure")
	}
}

type errExecConn struct {
	txn *errExecTx
}

func (c *errExecConn) Exec(_ context.Context, sql string, _ ...any) (int64, error) { return 0, nil }
func (c *errExecConn) Begin(_ context.Context) (pipeline.Tx, error) {
	c.txn = &errExecTx{}
	return c.txn, nil
}
func (c *errExecConn) Close(_ context.Context) error { return nil }

type errExecTx struct{ rollback bool }

func (t *errExecTx) Exec(_ context.Context, _ string, _ ...any) (int64, error) {
	return 0, errors.New("syntax error")
}
func (t *errExecTx) Commit(_ context.Context) error   { return nil }
func (t *errExecTx) Rollback(_ context.Context) error { t.rollback = true; return nil }

// ── pipeline.SecretBearingOp handling ───────────────────────────────────────────

type fakeSecretResolver struct{ err error }

func (f *fakeSecretResolver) Resolve(uri string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return "resolved:" + uri, nil
}

// secretOp implements pipeline.SecretBearingOp on top of testOp. SQL() (from
// the embedded testOp) always stays the placeholder/redacted form; execSQL
// is what ExecSQL returns — standing in for a real resolved statement.
type secretOp struct {
	testOp
	execSQL string
	execErr error
}

func (o secretOp) ExecSQL(_ pipeline.SecretResolver) (string, error) {
	if o.execErr != nil {
		return "", o.execErr
	}
	return o.execSQL, nil
}

var _ pipeline.SecretBearingOp = secretOp{}

// withSecretResolver registers r under pipeline.KeySecretResolver for the
// duration of t, restoring whatever was registered before (if anything).
func withSecretResolver(t *testing.T, r any) {
	t.Helper()
	prev, hadPrev := pipeline.Resolve[pipeline.SecretResolver](pipeline.Default, pipeline.KeySecretResolver)
	pipeline.Default.Register(pipeline.KeySecretResolver, r)
	t.Cleanup(func() {
		if hadPrev {
			pipeline.Default.Register(pipeline.KeySecretResolver, prev)
		}
	})
}

func TestApplyUsesExecSQLForSecretBearingOp(t *testing.T) {
	withSecretResolver(t, &fakeSecretResolver{})
	conn := &mockConn{}
	e := New()
	op := secretOp{
		testOp:  testOp{sql: "CREATE SUBSCRIPTION s CONNECTION '-' PUBLICATION p;", txn: true},
		execSQL: "CREATE SUBSCRIPTION s CONNECTION 'host=real password=s3cr3t' PUBLICATION p;",
	}
	m := pipeline.Migration{Transactional: []pipeline.DiffOp{op}}
	if err := e.Apply(context.Background(), m, conn); err != nil {
		t.Fatal(err)
	}
	if len(conn.txn.executed) != 1 || conn.txn.executed[0] != op.execSQL {
		t.Errorf("expected the resolved ExecSQL text to be executed, got: %v", conn.txn.executed)
	}
}

// TestApplySecretBearingOpFailureNeverLeaksResolvedText is the redaction
// guard: if the resolved statement fails to execute, the returned error
// must describe the failure using SQL()'s placeholder form, never the
// resolved text ExecSQL produced — otherwise a real secret would leak into
// a log line or terminal on every failed apply of a secret-bearing op.
func TestApplySecretBearingOpFailureNeverLeaksResolvedText(t *testing.T) {
	withSecretResolver(t, &fakeSecretResolver{})
	errConn := &errExecConn{}
	e := New()
	op := secretOp{
		testOp:  testOp{sql: "CREATE SUBSCRIPTION s CONNECTION '-' PUBLICATION p;", txn: true},
		execSQL: "CREATE SUBSCRIPTION s CONNECTION 'host=real password=TOP-SECRET-VALUE' PUBLICATION p;",
	}
	m := pipeline.Migration{Transactional: []pipeline.DiffOp{op}}
	err := e.Apply(context.Background(), m, errConn)
	if err == nil {
		t.Fatal("expected an error from exec failure")
	}
	if strings.Contains(err.Error(), "TOP-SECRET-VALUE") {
		t.Fatalf("error message leaked the resolved secret value: %v", err)
	}
	if !strings.Contains(err.Error(), "CONNECTION '-'") {
		t.Errorf("expected error to contain the redacted placeholder form (op.SQL()), got: %v", err)
	}
}

func TestApplySecretBearingOpResolveErrorNeverExecutes(t *testing.T) {
	withSecretResolver(t, &fakeSecretResolver{})
	conn := &mockConn{}
	e := New()
	op := secretOp{
		testOp:  testOp{sql: "CREATE SUBSCRIPTION s CONNECTION '-' PUBLICATION p;", txn: true},
		execErr: errors.New("vault unreachable"),
	}
	m := pipeline.Migration{Transactional: []pipeline.DiffOp{op}}
	if err := e.Apply(context.Background(), m, conn); err == nil {
		t.Fatal("expected an error when ExecSQL itself fails to resolve")
	}
	if conn.txn != nil && len(conn.txn.executed) != 0 {
		t.Errorf("expected nothing to be executed when resolution fails, got: %v", conn.txn.executed)
	}
}

func TestApplyNoResolverRegisteredErrorsClearly(t *testing.T) {
	withSecretResolver(t, "not-a-resolver") // simulates "nothing valid registered"
	conn := &mockConn{}
	e := New()
	op := secretOp{
		testOp:  testOp{sql: "CREATE SUBSCRIPTION s CONNECTION '-' PUBLICATION p;", txn: true},
		execSQL: "unused",
	}
	m := pipeline.Migration{Transactional: []pipeline.DiffOp{op}}
	if err := e.Apply(context.Background(), m, conn); err == nil {
		t.Fatal("expected an error when no SecretResolver is registered")
	}
}

// ── resolveDatabaseConfig ────────────────────────────────────────────────────

func TestResolveDatabaseConfigURIForm(t *testing.T) {
	cfg, err := resolveDatabaseConfig("postgres://user:pass@host:5432/a?sslmode=disable", "b")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database != "b" {
		t.Errorf("Database = %q, want %q", cfg.Database, "b")
	}
	if cfg.Host != "host" || cfg.User != "user" {
		t.Errorf("host/user not preserved: host=%q user=%q", cfg.Host, cfg.User)
	}
}

func TestResolveDatabaseConfigKeywordValueForm(t *testing.T) {
	cfg, err := resolveDatabaseConfig("host=host dbname=a user=user", "b")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database != "b" {
		t.Errorf("Database = %q, want %q", cfg.Database, "b")
	}
}

func TestResolveDatabaseConfigNoOpWhenAlreadyCorrect(t *testing.T) {
	cfg, err := resolveDatabaseConfig("postgres://host/b", "b")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database != "b" {
		t.Errorf("Database = %q, want %q", cfg.Database, "b")
	}
}

func TestResolveDatabaseConfigEmptyDBNameErrors(t *testing.T) {
	if _, err := resolveDatabaseConfig("postgres://host/a", ""); err == nil {
		t.Fatal("expected error for empty database name")
	}
}

func TestResolveDatabaseConfigMalformedConnStrErrors(t *testing.T) {
	if _, err := resolveDatabaseConfig("://not a valid conn string", "b"); err == nil {
		t.Fatal("expected parse error for a malformed connection string")
	}
}
