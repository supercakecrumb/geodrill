package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/supercakecrumb/geodrill/internal/storage/db"
)

// WithTx runs fn inside a single pgx transaction, exposing a tx-bound
// *db.Queries so fn can call any generated query with the same
// begin/commit-or-rollback semantics. Two flows need this (architecture
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

// WithTxStore is the ergonomic counterpart to WithTx: instead of a raw
// *db.Queries, fn receives a *Store bound to the same transaction, so every
// existing high-level Store method (PutUserItem, InsertReview,
// RecomputeTierProgressForTier, UpsertTierProgress, MarkExerciseAnswered,
// AnswerIntroductionOnce, ...) can be called transactionally without the
// caller hand-building db.XxxParams/pgtype conversions itself. Additive
// convenience over WithTx (architecture §5.5) for callers (internal/study)
// that want the same API surface they already use outside a transaction.
func (s *Store) WithTxStore(ctx context.Context, fn func(tx *Store) error) error {
	return s.WithTx(ctx, func(q *db.Queries) error {
		return fn(&Store{pool: s.pool, q: q})
	})
}
