package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/telemetry"
)

// YBRecorder is the production audit Recorder. It writes synchronously to
// audit.events and atomically advances audit.chain_heads inside one
// Yugabyte transaction. The write path is hash-chained per (org, shard) and
// uses the audit_writer_app role exclusively, so even an SQL injection in
// the application layer cannot mutate or delete audit rows.
//
// The recorder is dual-track with telemetry: every Record call emits
// structured slog and bumps an expvar counter alongside the database write.
type YBRecorder struct {
	pool *pgxpool.Pool
}

// NewYBRecorder opens a pool against the audit_writer DSN. The DSN must
// authenticate as a member of the audit_writer base role; the migration's
// RLS policies block writes from any other identity.
func NewYBRecorder(ctx context.Context, dsn string) (*YBRecorder, error) {
	if dsn == "" {
		return nil, errors.New("audit: AUDIT_WRITER_DSN required")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("audit pool config: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("audit pool open: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("audit pool ping: %w", err)
	}
	return &YBRecorder{pool: pool}, nil
}

// Close releases the underlying pool. Idempotent.
func (r *YBRecorder) Close() {
	if r != nil && r.pool != nil {
		r.pool.Close()
	}
}

// Record writes one audit event synchronously. Any failure increments the
// dropped counter and returns; the caller decides whether the parent
// operation should fail. State-change verbs are expected to abort their
// parent FDB transaction on audit failure (TACK-173); read verbs treat it
// as best-effort with a loud slog.Error.
func (r *YBRecorder) Record(ctx context.Context, ev Event) error {
	start := time.Now()
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now().UTC()
	}
	eventID := uuid.Must(uuid.NewV7())
	shard := shardOf(ev.Actor.ID, eventID)

	contextJSON, err := json.Marshal(ev.Context)
	if err != nil {
		return fmt.Errorf("audit marshal context: %w", err)
	}
	deltaJSON := []byte("null")
	if ev.Delta != nil {
		deltaJSON, err = json.Marshal(ev.Delta)
		if err != nil {
			return fmt.Errorf("audit marshal delta: %w", err)
		}
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		bumpDropped(ev.Verb, "begin")
		return fmt.Errorf("audit tx begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// PII split: any actor PII (email, name, ip, user_agent, api_token_label)
	// goes to audit.pii under a fresh pii_ref UUID. The audit.events row
	// stores only that reference, and the hash chain is computed over the
	// reference, not the payload. GDPR Right to Erasure can then redact
	// audit.pii without breaking integrity.
	var piiRef uuid.UUID
	if hasPII(ev.Actor) {
		piiRef = uuid.Must(uuid.NewV7())
		piiPayload, perr := json.Marshal(map[string]string{
			"email":           ev.Actor.Email,
			"name":            ev.Actor.Name,
			"ip":              ev.Actor.IP,
			"ua":              ev.Actor.UserAgent,
			"api_token_label": ev.Actor.APITokenLabel,
		})
		if perr != nil {
			bumpDropped(ev.Verb, "pii_marshal")
			return fmt.Errorf("audit pii marshal: %w", perr)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO audit.pii (pii_ref, payload, redacted)
			VALUES ($1, $2, false)
		`, piiRef, piiPayload); err != nil {
			bumpDropped(ev.Verb, "pii_insert")
			return fmt.Errorf("audit pii insert: %w", err)
		}
	}

	var lastSeq int64
	var lastHash []byte
	err = tx.QueryRow(ctx, `
		SELECT last_seq, last_hash FROM audit.chain_heads
		WHERE org_id = $1 AND shard = $2
	`, ev.Context.OrgID, shard).Scan(&lastSeq, &lastHash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		bumpDropped(ev.Verb, "head_read")
		return fmt.Errorf("audit head read: %w", err)
	}
	seq := lastSeq + 1

	// Hash payload covers everything that determines ledger meaning.
	rowHash, err := hashRow(lastHash, map[string]any{
		"org_id":      ev.Context.OrgID,
		"shard":       shard,
		"seq":         seq,
		"event_id":    eventID,
		"event_time":  ev.OccurredAt.UTC().Format(time.RFC3339Nano),
		"actor_id":    ev.Actor.ID,
		"actor_kind":  ev.Actor.Type,
		"action":      ev.Verb,
		"entity_kind": ev.Entity.Type,
		"entity_id":   ev.Entity.ID,
		"pii_ref":     piiRef,
		"context":     json.RawMessage(contextJSON),
		"delta":       json.RawMessage(deltaJSON),
		"idempotency": ev.IdempotencyKey,
	})
	if err != nil {
		bumpDropped(ev.Verb, "hash")
		return fmt.Errorf("audit hash: %w", err)
	}

	var piiArg any
	if piiRef != uuid.Nil {
		piiArg = piiRef
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit.events (
			org_id, shard, event_time, event_id, seq,
			actor_id, actor_kind, action, entity_kind, entity_id,
			context, delta, pii_ref, prev_hash, row_hash, idempotency_key
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15, $16
		)
	`,
		ev.Context.OrgID, shard, ev.OccurredAt.UTC(), eventID, seq,
		ev.Actor.ID, actorKindCode(ev.Actor.Type), ev.Verb, ev.Entity.Type, ev.Entity.ID,
		contextJSON, deltaJSON, piiArg, lastHash, rowHash, ev.IdempotencyKey,
	); err != nil {
		bumpDropped(ev.Verb, "insert")
		return fmt.Errorf("audit insert: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO audit.chain_heads (org_id, shard, last_seq, last_hash, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (org_id, shard) DO UPDATE
			SET last_seq = EXCLUDED.last_seq,
			    last_hash = EXCLUDED.last_hash,
			    updated_at = EXCLUDED.updated_at
	`, ev.Context.OrgID, shard, seq, rowHash); err != nil {
		bumpDropped(ev.Verb, "head_write")
		return fmt.Errorf("audit head write: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		bumpDropped(ev.Verb, "commit")
		return fmt.Errorf("audit commit: %w", err)
	}

	telemetry.IncAuditRecord(ev.Verb)
	telemetry.L(ctx).Debug("audit.record",
		slog.String("verb", ev.Verb),
		slog.String("actor_id", ev.Actor.ID.String()),
		slog.String("entity_id", ev.Entity.ID.String()),
		slog.Int("shard", int(shard)),
		slog.Int64("seq", seq),
		slog.String("idempotency_key", ev.IdempotencyKey),
		slog.Int64("duration_us", time.Since(start).Microseconds()),
	)
	return nil
}

// hasPII reports whether actor carries any field worth splitting into
// audit.pii. Empty strings imply nothing to redact later.
func hasPII(a Actor) bool {
	return a.Email != "" || a.Name != "" || a.IP != "" || a.UserAgent != "" || a.APITokenLabel != ""
}

func actorKindCode(t ActorType) int16 {
	switch t {
	case ActorUser:
		return 1
	case ActorService:
		return 2
	case ActorSystem:
		return 3
	case ActorToken:
		return 4
	default:
		return 0
	}
}

func bumpDropped(verb, stage string) {
	telemetry.IncAuditDropped(verb, stage)
}
