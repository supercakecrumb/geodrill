package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/supercakecrumb/geodrill/internal/storage/db"
)

// WithTx runs fn inside a single pgx transaction, exposing a tx-bound
// *db.Queries so fn can call any generated query with the same
// begin/commit-or-rollback semantics. Two v2 flows need this (architecture
// §5.5): answer (PutCard + InsertReview + tier recompute) and introduce
// (user_items upsert + introductions insert + tier recompute) — both need
// the lifecycle/gating writes to commit atomically or not at all.
//
// fn's error (or a panic) rolls the transaction back; a nil return commits.
func (s *Store) WithTx(ctx context.Context, fn func(q *db.Queries) error) (err error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return
		}
		err = tx.Commit(ctx)
	}()

	err = fn(s.q.WithTx(tx))
	return err
}
