package executor

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/thec1oud/dpg/internal/pipeline"
)

// Hook runs on a migration's connection, always in a transaction: first in
// each of the migration's transactions, before its statements, so what it
// does commits or rolls back with them; and before each statement that
// runs outside a transaction, in one of its own committed just before the
// statement. An error stops that transaction or statement.
type Hook func(ctx context.Context, tx pgx.Tx) error

// Hooked returns conn with hook run as Hook describes, for Apply.
func Hooked(conn *PgxConn, hook Hook) pipeline.Conn { return &hooked{conn: conn, hook: hook} }

type hooked struct {
	conn *PgxConn
	hook Hook
}

func (h *hooked) Begin(ctx context.Context) (pipeline.Tx, error) {
	tx, err := h.conn.conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.hook(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return &pgxTx{tx: tx}, nil
}

func (h *hooked) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	if err := pgx.BeginFunc(ctx, h.conn.conn, func(tx pgx.Tx) error { return h.hook(ctx, tx) }); err != nil {
		return 0, err
	}
	return h.conn.Exec(ctx, sql, args...)
}

func (h *hooked) Close(ctx context.Context) error { return h.conn.Close(ctx) }
