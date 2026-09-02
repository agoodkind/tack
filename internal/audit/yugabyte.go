package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/clock"
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
	cfg.ConnConfig.Tracer = &telemetry.QueryTracer{}
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

// Record writes one audit event synchronously inside a Yugabyte transaction.
// It appends to audit.events and advances audit.chain_heads through the shared
// compare-and-swap helper, retrying when a concurrent writer (another app
// replica) advances the same (org, shard) head. Any final failure increments
// the dropped counter and returns the error to the caller.
//
// What the caller does with that error is the caller's decision, and today no
// product write is gated on it. The MCP tool wrapper records after the handler
// has returned and logs a failure at Warn, so a state change whose audit row
// could not be written is committed and unrecorded. This comment used to claim
// the opposite, that state-change verbs abort their parent FoundationDB
// transaction on audit failure under TACK-173; that was the design's intent
// and never its code, and reading it as fact is how TACK-335 spent its
// investigation looking for rejected writes that were in fact accepted. The
// 2026-07-06 gap left eight issue creations in the store with no row here.
func (r *YBRecorder) Record(ctx context.Context, ev Event) error {
	ctx, span := telemetry.StartSpan(ctx, "audit.record",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("audit.verb", ev.Verb),
			attribute.String("audit.entity_type", ev.Entity.Type),
			attribute.String("audit.entity_id", ev.Entity.ID.String()),
		),
	)
	defer span.End()
	ctx = telemetry.WithTraceLogger(ctx,
		slog.String("audit_verb", ev.Verb),
		slog.String("entity_type", ev.Entity.Type),
		slog.String("entity_id", ev.Entity.ID.String()),
	)
	start := clock.Now()
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = clock.Now().UTC()
	}
	canonicalizeCorrelation(&ev)
	if ev.EventID == uuid.Nil {
		ev.EventID = uuid.Must(uuid.NewV7())
	}
	shard := shardOf(ev.Actor.ID, ev.EventID)

	contextJSON, err := json.Marshal(ev.Context)
	if err != nil {
		bumpDropped(ev.Verb, "context_marshal")
		return fmt.Errorf("audit marshal context: %w", err)
	}
	deltaJSON := []byte("null")
	if ev.Delta != nil {
		deltaJSON, err = json.Marshal(ev.Delta)
		if err != nil {
			bumpDropped(ev.Verb, "delta_marshal")
			return fmt.Errorf("audit marshal delta: %w", err)
		}
	}

	const maxChainRetries = 8
	var res chainAppendResult
	for attempt := 1; ; attempt++ {
		res, err = r.recordOnce(ctx, ev, shard, contextJSON, deltaJSON)
		if err == nil {
			break
		}
		if errors.Is(err, errAlreadyProjected) {
			return nil
		}
		if attempt < maxChainRetries && isRetryableChainErr(err) {
			continue
		}
		bumpDropped(ev.Verb, "chain")
		return err
	}

	telemetry.IncAuditRecord(ev.Verb)
	telemetry.L(ctx).Debug("audit.record",
		slog.String("verb", ev.Verb),
		slog.String("actor_id", ev.Actor.ID.String()),
		slog.String("entity_id", ev.Entity.ID.String()),
		slog.Int("shard", int(shard)),
		slog.Int64("seq", res.Seq),
		slog.String("idempotency_key", ev.IdempotencyKey),
		slog.Int64("duration_us", clock.Since(start).Microseconds()),
	)
	return nil
}

// recordOnce runs one attempt of the audit write: open a transaction, write the
// actor PII row, append the event and advance the chain head, then commit. The
// PII split keeps actor PII in audit.pii under a fresh pii_ref so GDPR erasure
// never breaks the hash chain, which covers the reference rather than the
// payload.
func (r *YBRecorder) recordOnce(ctx context.Context, ev Event, shard int16, contextJSON, deltaJSON []byte) (chainAppendResult, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		slog.ErrorContext(ctx, "audit.record.tx_begin_failed", slog.String("err", err.Error()))
		return chainAppendResult{}, fmt.Errorf("audit tx begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	piiRef, err := writePIIRow(ctx, tx, ev.Actor)
	if err != nil {
		return chainAppendResult{}, err
	}

	res, err := appendChainRow(ctx, tx, chainAppendInput{
		Event:       ev,
		EventID:     ev.EventID,
		Shard:       shard,
		PIIRef:      piiRef,
		ContextJSON: contextJSON,
		DeltaJSON:   deltaJSON,
	})
	if err != nil {
		return chainAppendResult{}, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "audit.record.commit_failed", slog.String("err", err.Error()))
		return chainAppendResult{}, fmt.Errorf("audit commit: %w", err)
	}
	return res, nil
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
	case ActorOperator:
		return 5
	default:
		return 0
	}
}

func bumpDropped(verb, stage string) {
	telemetry.IncAuditDropped(verb, stage)
}
