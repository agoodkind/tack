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

// YBRecorder writes audit events synchronously to YugabyteDB. One transaction
// appends to audit.events and advances audit.chain_heads for the event's
// organization and shard.
//
// The caller supplies the connection's DSN. The audit_writer role's grants
// and row-level security policies permit appends without permitting changes
// to existing audit rows. The constructor does not enforce the login name.
//
// Successful writes increment the audit counter and emit structured slog
// output. Failed writes increment a dropped-event counter on the failure
// paths that Record handles.
type YBRecorder struct {
	pool *pgxpool.Pool
}

// NewYBRecorder opens and pings a pool using dsn.
//
// The DSN must authenticate with audit_writer membership. The ledger's
// role grants and row-level security policies control permitted writes; this
// constructor does not validate role membership.
//
//   - An empty DSN returns an error.
//   - A failed ping closes the pool before returning the error.
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

// Close releases the underlying pool.
//
// The method accepts a nil recorder or pool.
//
//   - Repeated calls are safe.
func (r *YBRecorder) Close() {
	if r != nil && r.pool != nil {
		r.pool.Close()
	}
}

// Record writes one audit event in a YugabyteDB transaction and advances its
// organization and shard chain head with the shared compare-and-swap helper.
//
// Concurrent writers, including another replica, can advance the same head.
// Record retries retryable chain errors. Marshal failures and final chain
// failures increment a dropped-event counter and return an error. The audit
// completeness contract requires callers to refuse unaudited operations.
//
// TACK-173 introduced recording product state changes in the FoundationDB
// outbox within the transaction that changes product data, followed by relay
// delivery. This recorder supplies a direct YugabyteDB write path. The
// original TACK-335 incident account reports that the earlier MCP wrapper
// recorded after handler completion and logged failures at Warn. It attributes
// eight issue creations without audit rows on 2026-07-06 to that ordering.
//
//   - A previously projected event returns nil without another append.
//   - A successful append commits its event and chain head together.
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

// recordOnce opens a transaction, writes actor personally identifiable
// information (PII), appends the event, advances the chain head, and commits.
// The transaction stores PII separately in audit.pii under a fresh pii_ref.
// The hash covers that reference rather than the PII payload. GDPR erasure
// can remove the payload without breaking the hash chain.
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

// hasPII reports whether the actor has a nonempty field for audit.pii.
// Empty fields do not require later redaction.
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
