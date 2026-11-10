package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// chainHeadState is the state of one (org, shard) hash chain before an append.
type chainHeadState struct {
	// LastSeq contains the newest row's sequence. It equals zero before the
	// chain contains any rows.
	LastSeq int64
	// LastHash contains the newest row's hash for the next row's hash input.
	// The first row of a chain uses an empty slice.
	LastHash []byte
	// Exists reports whether audit.chain_heads already stores this chain.
	// The writer updates an existing head and inserts a missing head.
	Exists bool
}

// readChainHead reads one (org, shard) chain head inside tx. Writers use this
// function and writeChainHead for the shared chain protocol.
func readChainHead(
	ctx context.Context,
	tx pgx.Tx,
	orgID uuid.UUID,
	shard int16,
) (chainHeadState, error) {
	head := chainHeadState{LastSeq: 0, LastHash: []byte{}, Exists: true}
	err := tx.QueryRow(
		ctx,
		`
		SELECT last_seq, last_hash FROM audit.chain_heads
		WHERE org_id = $1 AND shard = $2
	`,
		orgID,
		shard,
	).Scan(
		&head.LastSeq,
		&head.LastHash,
	)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return chainHeadState{LastSeq: 0, LastHash: []byte{}, Exists: false}, nil
	case err != nil:
		slog.ErrorContext(ctx, "audit.chain.head_read_failed", slog.String("err", err.Error()))

		return chainHeadState{
			LastSeq:  0,
			LastHash: nil,
			Exists:   false,
		}, fmt.Errorf("chain head read: %w", err)
	}

	if head.LastHash == nil {
		head.LastHash = []byte{}
	}

	return head, nil
}

// writeChainHead advances the chain to seq and rowHash only if its head still
// matches the head the caller read. It returns errChainConflict after another
// writer advances that chain. The caller retries in a fresh transaction.
func writeChainHead(
	ctx context.Context,
	tx pgx.Tx,
	orgID uuid.UUID,
	shard int16,
	head chainHeadState,
	seq int64,
	rowHash []byte,
) error {
	var tag pgconn.CommandTag
	var err error
	if head.Exists {
		tag, err = tx.Exec(
			ctx,
			`
			UPDATE audit.chain_heads
			SET last_seq = $3, last_hash = $4, updated_at = now()
			WHERE org_id = $1 AND shard = $2 AND last_seq = $5
		`,
			orgID,
			shard,
			seq,
			rowHash,
			head.LastSeq,
		)
	} else {
		tag, err = tx.Exec(
			ctx,
			`
			INSERT INTO audit.chain_heads (org_id, shard, last_seq, last_hash, updated_at)
			VALUES ($1, $2, $3, $4, now())
			ON CONFLICT (org_id, shard) DO NOTHING
		`,
			orgID,
			shard,
			seq,
			rowHash,
		)
	}

	if err != nil {
		slog.ErrorContext(ctx, "audit.chain.head_write_failed", slog.String("err", err.Error()))
		return fmt.Errorf("chain head write: %w", err)
	}

	if tag.RowsAffected() == 0 {
		return errChainConflict
	}

	return nil
}
