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
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/telemetry"
)

// Notarizer periodically reads each shard's chain head, builds a Merkle tree,
// signs its root with Ed25519, and inserts a row into audit.notarizations.
// The Merkle tree combines the chain heads into one hash. Its signed root
// provides a checkpoint for detecting changes to the preceding audit.events
// history during verification.
//
// The supplied database connection must permit reading audit.chain_heads and
// inserting audit.notarizations. The audit_writer grants provide those
// permissions. Each Notarizer runs its own loop.
// The proposed FoundationDB compare-and-swap leader election would preserve
// this contract. The implementation does not elect a leader.
type Notarizer struct {
	pool   *pgxpool.Pool
	signer ed25519.PrivateKey
	keyID  string
	host   string
	period time.Duration

	stop    chan struct{}
	stopped chan struct{}

	lastAt atomic.Int64 // lastAt stores Unix seconds after a run finishes its chain-head scan.
}

// NotarizerConfig configures the signing key, claimed host, and run interval.
type NotarizerConfig struct {
	// SigningKeyPath specifies a PEM-encoded Ed25519 private key. The key
	// generation command is:
	//
	// 	openssl genpkey -algorithm ed25519 -out audit-signing.pem
	//
	// NewConsumer skips the notarizer when the path is empty. NewNotarizer
	// requires a nonempty path.
	SigningKeyPath string
	// SigningHost records the host that claims to sign each row (TACK-437).
	// The value appears beside the signature, outside its signed content. The
	// signer report uses it as a label, not proof of the row's origin. An empty
	// value remains empty in storage.
	SigningHost string
	// Period sets the run interval. A nonpositive value selects 60s.
	Period time.Duration
}

// NewNotarizer parses the signing key and opens the supplied database DSN.
//
// The audit_writer grants permit reading and writing chain heads and inserting
// notarizations. YBRecorder uses these permissions for ledger writes, and the
// consumer supplies its YugabyteDB DSN when it creates a notarizer.
//
//   - A missing key path or DSN returns an error.
//   - Key loading, pool construction, and database ping errors return an error.
//   - A successful call returns a notarizer that has not started its loop.
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
		host:    cfg.SigningHost,
		period:  period,
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}, nil
}

// Start launches a notarizer loop in a goroutine and returns immediately.
//
// Each call starts another loop. The implementation does not suppress repeated
// calls. The loop attempts a notarization before its first timer tick.
//
//   - Canceling ctx stops the loop.
//   - Close signals the loop to stop and waits for its exit.
func (n *Notarizer) Start(ctx context.Context) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.ErrorContext(ctx, "audit.notarizer.panic", slog.Any("err", r))
			}
		}()
		n.loop(ctx)
	}()
}

func (n *Notarizer) loop(ctx context.Context) {
	ctx = telemetry.WithTraceLogger(ctx, slog.String("worker", "audit.notarizer"))
	defer close(n.stopped)
	t := time.NewTicker(n.period)
	defer t.Stop()
	// Attempt a notarization at startup before the first timer tick.
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

// Close signals the notarizer loop to stop, waits for its exit, and closes
// the database pool.
//
// Sequential repeated calls reuse the closed stop channel. Close waits for
// the stopped channel, which the loop closes on exit.
//
//  1. Start the loop before calling Close.
//  2. Call Close to wait for loop termination and release the pool.
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

// LastNotarizedUnix returns the Unix-second timestamp that the most recent
// run stored after scanning the chain heads.
//
// The run stores this timestamp even if an organization insert fails. The
// value does not prove that every notarization insert succeeded.
//
//   - Zero means a run has not stored a timestamp.
//   - A nonzero value records the end of a run that completed its scan.
func (n *Notarizer) LastNotarizedUnix() int64 { return n.lastAt.Load() }

func (n *Notarizer) runOnce(ctx context.Context) {
	ctx, span := telemetry.StartSpan(ctx, "audit.notarizer.run_once",
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	defer span.End()

	start := clock.Now()
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
				org_id, notarized_at, merkle_root, shard_heads, signature, signing_key, signing_host
			) VALUES ($1, now(), $2, $3, $4, $5, $6)
		`, orgID, root, manifest, sig, n.keyID, n.host); err != nil {
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
			slog.String("signing_host", n.host),
			slog.Int64("duration_ms", clock.Since(start).Milliseconds()),
		)
	}
	span.SetStatus(codes.Ok, "ok")
	span.SetAttributes(attribute.Int64("audit.notarizer.duration_ms", clock.Since(start).Milliseconds()))
	n.lastAt.Store(clock.Now().Unix())
}
