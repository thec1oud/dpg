// Package executor implements pipeline.ApplyExecutor using pgx.
package executor

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/thec1oud/dpg/internal/pipeline"
)

func init() {
	pipeline.Default.Register(pipeline.KeyApplyExecutor, New())
}

// PgxExecutor implements pipeline.ApplyExecutor over a pgx connection.
type PgxExecutor struct{}

// New returns a PgxExecutor.
func New() *PgxExecutor { return &PgxExecutor{} }

// Apply executes a Migration. Transactional ops run inside BEGIN/COMMIT;
// non-transactional ops run individually outside any transaction.
func (e *PgxExecutor) Apply(ctx context.Context, m pipeline.Migration, conn pipeline.Conn) error {
	// Lazily resolved on first use: most migrations have no
	// pipeline.SecretBearingOp at all, and a plain registry lookup is cheap
	// but pointless to do unconditionally.
	var resolver pipeline.SecretResolver
	var resolverLoaded bool
	execText := func(op pipeline.DiffOp) (string, error) {
		sb, ok := op.(pipeline.SecretBearingOp)
		if !ok {
			return op.SQL(), nil
		}
		if !resolverLoaded {
			resolver, _ = pipeline.Resolve[pipeline.SecretResolver](pipeline.Default, pipeline.KeySecretResolver)
			resolverLoaded = true
		}
		if resolver == nil {
			return "", fmt.Errorf("no SecretResolver registered to resolve this statement's secret reference")
		}
		return sb.ExecSQL(resolver)
	}

	// Steps that must be committed before the transaction starts (an enum
	// value the transaction uses, RFC Section 5.1.1).
	for _, op := range m.PreTransactional {
		text, err := execText(op)
		if err != nil {
			return &StatementError{SQL: op.SQL(), Err: err}
		}
		if _, err := conn.Exec(ctx, text); err != nil {
			return &StatementError{SQL: op.SQL(), Err: err}
		}
	}

	// Transactional block.
	if len(m.Transactional) > 0 {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("executor: begin transaction: %w", err)
		}
		for _, op := range m.Transactional {
			text, err := execText(op)
			if err != nil {
				_ = tx.Rollback(ctx)
				return &StatementError{SQL: op.SQL(), Err: err}
			}
			if _, err := tx.Exec(ctx, text); err != nil {
				_ = tx.Rollback(ctx)
				// Always op.SQL() (the placeholder/reference form), never
				// text (which may hold a resolved secret) — see
				// pipeline.SecretBearingOp's doc comment.
				return &StatementError{SQL: op.SQL(), Err: err}
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("executor: commit: %w", err)
		}
	}

	// Non-transactional steps (e.g. CREATE INDEX CONCURRENTLY).
	for _, op := range m.NonTransactional {
		text, err := execText(op)
		if err != nil {
			return &AfterCommitError{&StatementError{SQL: op.SQL(), Err: err}}
		}
		if _, err := conn.Exec(ctx, text); err != nil {
			return &AfterCommitError{&StatementError{SQL: op.SQL(), Err: err}}
		}
	}
	return nil
}

// AfterCommitError is a failure in the steps after the migration's
// transaction committed: the database has the transaction's changes.
type AfterCommitError struct{ Err error }

// StatementError is a statement that failed, and why. Its message quotes
// the statement, as dpg's output always has; a caller that keeps SQL out
// of its logs names the statement by SQL, which is the step's as the
// migration log records it, with secret references unresolved.
type StatementError struct {
	SQL string
	Err error
}

func (e *StatementError) Error() string { return "executor: " + e.SQL + ": " + e.Err.Error() }
func (e *StatementError) Unwrap() error { return e.Err }

func (e *AfterCommitError) Error() string { return e.Err.Error() }
func (e *AfterCommitError) Unwrap() error { return e.Err }

var _ pipeline.ApplyExecutor = (*PgxExecutor)(nil)

// ── pgxConn wraps *pgx.Conn to satisfy pipeline.Conn ─────────────────────────

// PgxConn wraps a *pgx.Conn to implement pipeline.Conn.
type PgxConn struct {
	conn *pgx.Conn
	// borrowed: the connection's owner closes it, not Close.
	borrowed bool
}

// Borrow wraps a connection its owner keeps: Close leaves it open.
func Borrow(conn *pgx.Conn) *PgxConn { return &PgxConn{conn: conn, borrowed: true} }

// Connect opens a pgx connection to connStr and returns a PgxConn.
func Connect(ctx context.Context, connStr string) (*PgxConn, error) {
	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		return nil, fmt.Errorf("executor: connect: %w", err)
	}
	return &PgxConn{conn: conn}, nil
}

// resolveDatabaseConfig parses connStr and overrides its target database with
// dbName. Split out from ConnectToDatabase so this can be unit tested without
// a live Postgres connection (pgx.ParseConfig does no network I/O).
func resolveDatabaseConfig(connStr, dbName string) (*pgx.ConnConfig, error) {
	if dbName == "" {
		return nil, fmt.Errorf("executor: connect to database: empty database name")
	}
	cfg, err := pgx.ParseConfig(connStr)
	if err != nil {
		return nil, fmt.Errorf("executor: parse connection string: %w", err)
	}
	cfg.Database = dbName
	return cfg, nil
}

// ConnectToDatabase is Connect, except it overrides connStr's target database
// with dbName before connecting. The cluster's connection string only ever
// carries one database name; per-database commands (dump, plan, verify,
// apply) must swap in the actual target database, or every database in a
// multi-database cluster silently connects to whatever database happens to
// be embedded in the cluster's url/link.
func ConnectToDatabase(ctx context.Context, connStr, dbName string) (*PgxConn, error) {
	cfg, err := resolveDatabaseConfig(connStr, dbName)
	if err != nil {
		return nil, err
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("executor: connect: %w", err)
	}
	return &PgxConn{conn: conn}, nil
}

func (c *PgxConn) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	ct, err := c.conn.Exec(ctx, sql, args...)
	return ct.RowsAffected(), err
}

func (c *PgxConn) Begin(ctx context.Context) (pipeline.Tx, error) {
	tx, err := c.conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &pgxTx{tx: tx}, nil
}

func (c *PgxConn) Close(ctx context.Context) error {
	if c.borrowed {
		return nil
	}
	return c.conn.Close(ctx)
}

// QueryRows implements pipeline.Querier. It returns rows from a query.
func (c *PgxConn) QueryRows(ctx context.Context, sql string, args ...any) (pipeline.Rows, error) {
	rows, err := c.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return &pgxRows{rows: rows}, nil
}

// pgxRows wraps pgx.Rows to implement pipeline.Rows.
type pgxRows struct {
	rows interface {
		Next() bool
		Scan(dest ...any) error
		Err() error
		Close()
	}
}

func (r *pgxRows) Next() bool          { return r.rows.Next() }
func (r *pgxRows) Scan(d ...any) error { return r.rows.Scan(d...) }
func (r *pgxRows) Err() error          { return r.rows.Err() }
func (r *pgxRows) Close()              { r.rows.Close() }

var _ pipeline.Querier = (*PgxConn)(nil)

// pgxTx wraps pgx.Tx to implement pipeline.Tx.
type pgxTx struct {
	tx pgx.Tx
}

func (t *pgxTx) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	ct, err := t.tx.Exec(ctx, sql, args...)
	return ct.RowsAffected(), err
}

func (t *pgxTx) Commit(ctx context.Context) error   { return t.tx.Commit(ctx) }
func (t *pgxTx) Rollback(ctx context.Context) error { return t.tx.Rollback(ctx) }

var _ pipeline.Conn = (*PgxConn)(nil)
var _ pipeline.Tx = (*pgxTx)(nil)
