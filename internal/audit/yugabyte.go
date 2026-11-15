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

// YBRecorder synchronously appends audit.events and advances audit.chain_heads
// in one YugabyteDB transaction per (org, shard) hash chain. The
// audit_writer_app role cannot update or delete ledger rows. Each successful
// Record also emits structured logs and increments telemetry counters.
type YBRecorder struct {
	pool *pgxpool.Pool
}

// NewYBRecorder opens the audit_writer DSN. The login requires audit_writer
// membership under the migration's row-level security policies.
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

// Close releases the pool and supports repeated calls.
func (r *YBRecorder) Close() {
	if r != nil && r.pool != nil {
		r.pool.Close()
	}
}

// Record appends an event and advances its chain head in one transaction,
// retrying concurrent head conflicts. Final failures increment the dropped
// counter and return an error. MCP product changes instead commit their
// evidence in the FoundationDB outbox with the product transaction before
// relay publication to the ledger (TACK-173).
func (r *YBRecorder) Record(ctx context.Context, ev Event) error {
	ctx, span := telemetry.StartSpan(
		ctx,
		"audit.record",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("audit.verb", ev.Verb),
			attribute.String("audit.entity_type", ev.Entity.Type),
			attribute.String("audit.entity_id", ev.Entity.ID.String()),
		),
	)
	defer span.End()
	ctx = telemetry.WithTraceLogger(
		ctx,
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
	telemetry.L(ctx).Debug(
		"audit.record",
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

// recordOnce writes PII, appends the event, and advances the head before
// commit.
// The chain hashes the pii_ref rather than the payload, allowing later GDPR
// erasure in audit.pii without invalidating chain hashes.
func (r *YBRecorder) recordOnce(
	ctx context.Context,
	ev Event,
	shard int16,
	contextJSON []byte,
	deltaJSON []byte,
) (chainAppendResult, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		slog.ErrorContext(ctx, "audit.record.tx_begin_failed", slog.String("err", err.Error()))
		return chainAppendResult{}, fmt.Errorf("audit tx begin: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	piiRef, err := writeEventPII(ctx, tx, ev)
	if err != nil {
		return chainAppendResult{}, err
	}

	res, err := appendChainRow(
		ctx,
		tx,
		chainAppendInput{
			Event:       ev,
			EventID:     ev.EventID,
			Shard:       shard,
			PIIRef:      piiRef,
			ContextJSON: contextJSON,
			DeltaJSON:   deltaJSON,
		},
	)
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

// hasPII reports whether an actor includes nonempty personal data for
// audit.pii.
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
	case ActorUnauthenticated:
		return 6
	default:
		return 0
	}
}

func bumpDropped(verb, stage string) {
	telemetry.IncAuditDropped(verb, stage)
}
