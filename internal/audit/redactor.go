package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/telemetry"
)

// Redactor implements GDPR Right to Erasure for the audit ledger. It runs
// against the audit_redactor_app role, which can UPDATE only the payload,
// redacted, and redacted_at columns of audit.pii. Every other audit table
// is read-only or invisible to this role; the hash chain in audit.events
// stays intact because it was computed over pii_ref (a UUID) and not the
// payload itself.
type Redactor struct {
	pool *pgxpool.Pool
}

// NewRedactor opens a pool against the audit_redactor DSN.
func NewRedactor(ctx context.Context, dsn string) (*Redactor, error) {
	if dsn == "" {
		return nil, errors.New("audit: AUDIT_REDACTOR_DSN required")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("audit redactor pool config: %w", err)
	}
	cfg.ConnConfig.Tracer = &telemetry.QueryTracer{}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("audit redactor pool open: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("audit redactor pool ping: %w", err)
	}
	return &Redactor{pool: pool}, nil
}

func (r *Redactor) Close() {
	if r != nil && r.pool != nil {
		r.pool.Close()
	}
}

// RedactActor erases every PII payload referenced by audit.events rows whose
// actor_id matches. Returns the number of audit.pii rows updated. The
// audit.events rows stay; their hash chain stays valid; only the PII
// payload becomes [redacted].
func (r *Redactor) RedactActor(ctx context.Context, actorID uuid.UUID) (int64, error) {
	ctx, span := telemetry.StartSpan(ctx, "audit.redact_actor",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.String("audit.actor_id", actorID.String())),
	)
	defer span.End()
	ctx = telemetry.WithTraceLogger(ctx, slog.String("actor_id", actorID.String()))
	if r == nil || r.pool == nil {
		return 0, errors.New("audit redactor not configured")
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE audit.pii
		   SET payload = NULL, redacted = true, redacted_at = now()
		 WHERE pii_ref IN (
		     SELECT pii_ref FROM audit.events
		      WHERE actor_id = $1 AND pii_ref IS NOT NULL
		 )
		   AND redacted = false
	`, actorID)
	if err != nil {
		return 0, fmt.Errorf("audit redact actor: %w", err)
	}
	count := tag.RowsAffected()
	telemetry.L(ctx).Info("audit.pii.redacted",
		slog.String("actor_id", actorID.String()),
		slog.Int64("redacted_count", count),
	)
	return count, nil
}
