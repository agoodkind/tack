package audit

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/telemetry"
)

// Notarizer periodically reads every per-shard chain head, builds a Merkle
// tree, signs the root with Ed25519, and inserts a row into
// audit.notarizations. The result is a tamper-evidence checkpoint: a row in
// audit.events that pre-dates the notarization is covered by the signed
// Merkle root, so any retroactive change to that row breaks the proof.
//
// The notarizer connects via the audit_writer_app role (which has SELECT +
// INSERT on chain_heads + notarizations per the migration). Single-process
// for now; FDB-CAS leader election can layer underneath later without
// changing the contract.
type Notarizer struct {
	pool   *pgxpool.Pool
	signer ed25519.PrivateKey
	keyID  string
	period time.Duration

	stop    chan struct{}
	stopped chan struct{}

	lastAt atomic.Int64 // unix seconds of the last successful run
}

// NotarizerConfig collects the runtime knobs.
type NotarizerConfig struct {
	// SigningKeyPath points at a PEM-encoded Ed25519 private key. Generate
	// with `openssl genpkey -algorithm ed25519 -out audit-signing.pem`.
	// Empty disables the notarizer entirely.
	SigningKeyPath string
	// Period is how often the notarizer runs. Defaults to 60s.
	Period time.Duration
}

// NewNotarizer parses the signing key and connects via the audit_writer DSN.
// The same role used by YBRecorder is reused; chain_heads + notarizations
// are both writable by audit_writer per migration grants.
func NewNotarizer(ctx context.Context, dsn string, cfg NotarizerConfig) (*Notarizer, error) {
	if cfg.SigningKeyPath == "" {
		return nil, errors.New("audit notarizer: SigningKeyPath required")
	}
	if dsn == "" {
		return nil, errors.New("audit notarizer: AUDIT_WRITER_DSN required")
	}
	priv, keyID, err := loadEd25519Key(cfg.SigningKeyPath)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("audit notarizer pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("audit notarizer ping: %w", err)
	}
	period := cfg.Period
	if period <= 0 {
		period = 60 * time.Second
	}
	return &Notarizer{
		pool:    pool,
		signer:  priv,
		keyID:   keyID,
		period:  period,
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}, nil
}

// Start launches the notarizer loop. Idempotent calls return immediately.
func (n *Notarizer) Start(ctx context.Context) {
	go n.loop(ctx)
}

func (n *Notarizer) loop(ctx context.Context) {
	ctx = telemetry.WithTraceLogger(ctx, slog.String("worker", "audit.notarizer"))
	defer close(n.stopped)
	t := time.NewTicker(n.period)
	defer t.Stop()
	// Run once at boot so a freshly deployed cluster has a notarization
	// before the first ticker fires.
	n.runOnce(ctx)
	for {
		select {
		case <-n.stop:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			n.runOnce(ctx)
		}
	}
}

// Close stops the loop. Idempotent.
func (n *Notarizer) Close() error {
	select {
	case <-n.stop:
	default:
		close(n.stop)
	}
	<-n.stopped
	if n.pool != nil {
		n.pool.Close()
	}
	return nil
}

// LastNotarizedUnix returns the unix-second timestamp of the most recent
// successful run. Zero means no run has succeeded yet.
func (n *Notarizer) LastNotarizedUnix() int64 { return n.lastAt.Load() }

func (n *Notarizer) runOnce(ctx context.Context) {
	ctx, span := telemetry.StartSpan(ctx, "audit.notarizer.run_once",
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	defer span.End()

	start := time.Now()
	rows, err := n.pool.Query(ctx, `
		SELECT org_id, shard, last_seq, last_hash
		  FROM audit.chain_heads
		 ORDER BY org_id, shard
	`)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		telemetry.L(ctx).Error("audit.notarizer.scan_failed", slog.String("err", err.Error()))
		return
	}
	defer rows.Close()

	heads := map[uuid.UUID][]chainHead{}
	for rows.Next() {
		var orgID uuid.UUID
		var h chainHead
		if err := rows.Scan(&orgID, &h.shard, &h.seq, &h.hash); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			telemetry.L(ctx).Error("audit.notarizer.scan_row", slog.String("err", err.Error()))
			return
		}
		heads[orgID] = append(heads[orgID], h)
	}
	if err := rows.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		telemetry.L(ctx).Error("audit.notarizer.iter", slog.String("err", err.Error()))
		return
	}

	span.SetAttributes(attribute.Int("audit.notarizer.org_count", len(heads)))

	for orgID, list := range heads {
		root, manifest := merkleAndManifest(list)
		sig := ed25519.Sign(n.signer, root)
		if _, err := n.pool.Exec(ctx, `
			INSERT INTO audit.notarizations (
				org_id, notarized_at, merkle_root, shard_heads, signature, signing_key
			) VALUES ($1, now(), $2, $3, $4, $5)
		`, orgID, root, manifest, sig, n.keyID); err != nil {
			span.RecordError(err)
			telemetry.L(ctx).Error("audit.notarizer.insert_failed",
				slog.String("org_id", orgID.String()),
				slog.String("err", err.Error()),
			)
			continue
		}
		telemetry.L(ctx).Info("audit.notarizer.signed",
			slog.String("org_id", orgID.String()),
			slog.String("merkle_root", hex.EncodeToString(root)),
			slog.Int("shard_count", len(list)),
			slog.String("key_id", n.keyID),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
		)
	}
	span.SetStatus(codes.Ok, "ok")
	span.SetAttributes(attribute.Int64("audit.notarizer.duration_ms", time.Since(start).Milliseconds()))
	n.lastAt.Store(time.Now().Unix())
}
