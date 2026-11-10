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

// Redactor implements the GDPR right to erasure for audit.pii payloads. The
// audit_redactor_app role can UPDATE only payload, redacted, and redacted_at
// in audit.pii. Other audit tables grant read access or deny access to this
// role. The audit.events chain hashes pii_ref UUIDs rather than payloads;
// payload erasure does not invalidate those hashes.
type Redactor struct {
	pool *pgxpool.Pool
}

// NewRedactor opens a pool using the audit_redactor connection string in dsn.
//
// The constructor rejects an empty DSN and returns pool configuration or open
// errors. A failed initial ping logs a warning and still returns the redactor.
//
//   - A successful ping establishes connectivity during construction.
//   - Later operations can attempt to connect after a failed ping.
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

	// The initial ping attempts a connection, as NewReader does. A failed ping
	// logs a warning; later operations can attempt to connect again.
	if err := pool.Ping(ctx); err != nil {
		slog.WarnContext(ctx, "audit.redactor.ping_deferred", slog.String("err", err.Error()))
	}

	return &Redactor{pool: pool}, nil
}

func (r *Redactor) Close() {
	if r != nil && r.pool != nil {
		r.pool.Close()
	}
}

// CountUnredacted counts the specified audit.pii rows with redacted = false.
//
// The redactor role can SELECT audit.pii. A redaction plan can count these
// rows without reading audit.events.
//
//   - An empty reference list returns zero without a query.
//   - An unconfigured redactor or failed query returns an error.
func (r *Redactor) CountUnredacted(ctx context.Context, refs []uuid.UUID) (int64, error) {
	if r == nil || r.pool == nil {
		return 0, errors.New("audit redactor not configured")
	}

	if len(refs) == 0 {
		return 0, nil
	}

	var count int64
	err := r.pool.QueryRow(
		ctx,
		`
		SELECT count(*) FROM audit.pii
		 WHERE pii_ref = ANY($1::uuid[]) AND redacted = false
	`,
		uuidStrings(refs),
	).Scan(
		&count,
	)
	if err != nil {
		slog.ErrorContext(ctx, "audit.pii.count_unredacted_failed", slog.String("err", err.Error()))
		return 0, fmt.Errorf("audit count unredacted pii: %w", err)
	}

	return count, nil
}

// RedactPIIRefs erases the specified audit.pii payloads and returns the number
// of changed rows.
//
// The caller resolves references within one organization. Event-actor
// references require the reader; protected-value subject references use the
// redactor. This method does not read audit.events.
//
//   - Rows with redacted = false receive a NULL payload, redacted = true, and
//     the current redacted_at timestamp.
//   - An empty reference list returns zero without changing rows.
func (r *Redactor) RedactPIIRefs(ctx context.Context, refs []uuid.UUID) (int64, error) {
	ctx, span := telemetry.StartSpan(
		ctx,
		"audit.redact_pii_refs",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.Int("audit.pii_ref_count", len(refs))),
	)
	defer span.End()
	ctx = telemetry.WithTraceLogger(ctx, slog.Int("pii_ref_count", len(refs)))
	if r == nil || r.pool == nil {
		return 0, errors.New("audit redactor not configured")
	}

	if len(refs) == 0 {
		return 0, nil
	}

	tag, err := r.pool.Exec(
		ctx,
		`
		UPDATE audit.pii
		   SET payload = NULL, redacted = true, redacted_at = now()
		 WHERE pii_ref = ANY($1::uuid[]) AND redacted = false
	`,
		uuidStrings(refs),
	)
	if err != nil {
		slog.ErrorContext(ctx, "audit.pii.redact_refs_failed", slog.String("err", err.Error()))
		return 0, fmt.Errorf("audit redact pii refs: %w", err)
	}

	count := tag.RowsAffected()
	telemetry.L(ctx).Info(
		"audit.pii.redacted",
		slog.Int("pii_ref_count", len(refs)),
		slog.Int64("redacted_count", count),
	)

	return count, nil
}

func uuidStrings(ids []uuid.UUID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}

	return out
}
