package flows

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is the minimal database interface required by Flows helper functions.
//
// It is intentionally small so callers can pass either a transaction (pgx.Tx)
// or a direct connection/pool that supports Exec and QueryRow.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
