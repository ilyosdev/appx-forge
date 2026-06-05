package store

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AssignPendingSandboxUnderCap atomically assigns a PENDING sandbox to a node
// only if the node's live schedulable count is strictly below cap, closing the
// concurrent-create overshoot window that a plain check-then-act count cap
// leaves open.
//
// It runs in ONE transaction that FIRST takes a per-node advisory lock
// (pg_advisory_xact_lock) so concurrent creates targeting the SAME node
// serialize, THEN runs the cap-checked conditional assign
// (AssignSandboxToNodeUnderCap) as a SEPARATE statement. Under READ COMMITTED
// that second statement gets a fresh snapshot taken AFTER the lock is granted,
// so its count subquery observes the prior holder's committed assign — the
// count is never stale for a serialized contender. The lock is released
// automatically on COMMIT/ROLLBACK (transaction-scoped).
//
// Returns assigned=false (nil error) when the node is at/over cap (the
// conditional UPDATE matched zero rows), so the caller can fall back to another
// node or return a no-capacity error.
func AssignPendingSandboxUnderCap(ctx context.Context, pool *pgxpool.Pool, nodeID, sandboxID pgtype.UUID, cap int32) (bool, Sandbox, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, Sandbox{}, fmt.Errorf("begin tx: %w", err)
	}
	// Rollback is a no-op after a successful Commit, so this defer is safe.
	defer tx.Rollback(ctx)

	// Per-node advisory lock: serialize concurrent assigns to this node so the
	// cap re-check below cannot be defeated by two creates reading the same
	// pre-burst count.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", nodeAdvisoryLockKey(nodeID)); err != nil {
		return false, Sandbox{}, fmt.Errorf("acquire node advisory lock: %w", err)
	}

	qtx := New(tx)
	sb, err := qtx.AssignSandboxToNodeUnderCap(ctx, AssignSandboxToNodeUnderCapParams{
		NodeID: nodeID,
		ID:     sandboxID,
		Cap:    cap,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Node is at/over cap (or the row was no longer pending). Not an
			// error — the caller falls back to another node.
			if cErr := tx.Commit(ctx); cErr != nil {
				return false, Sandbox{}, fmt.Errorf("commit (no-assign): %w", cErr)
			}
			return false, Sandbox{}, nil
		}
		return false, Sandbox{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, Sandbox{}, fmt.Errorf("commit assign: %w", err)
	}
	return true, sb, nil
}

// nodeAdvisoryLockKey derives a stable 64-bit Postgres advisory-lock key from a
// node UUID under a fixed namespace salt, so the per-node create-path lock does
// not collide with any other pg_advisory_*_lock caller sharing the lock space.
func nodeAdvisoryLockKey(nodeID pgtype.UUID) int64 {
	sum := sha256.Sum256(append([]byte("forge:sched:node:"), nodeID.Bytes[:]...))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}
