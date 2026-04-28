package audit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
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

type chainHead struct {
	shard int16
	seq   int64
	hash  []byte
}

func (n *Notarizer) runOnce(ctx context.Context) {
	start := time.Now()
	rows, err := n.pool.Query(ctx, `
		SELECT org_id, shard, last_seq, last_hash
		  FROM audit.chain_heads
		 ORDER BY org_id, shard
	`)
	if err != nil {
		telemetry.L(ctx).Error("audit.notarizer.scan_failed", slog.String("err", err.Error()))
		return
	}
	defer rows.Close()

	heads := map[uuid.UUID][]chainHead{}
	for rows.Next() {
		var orgID uuid.UUID
		var h chainHead
		if err := rows.Scan(&orgID, &h.shard, &h.seq, &h.hash); err != nil {
			telemetry.L(ctx).Error("audit.notarizer.scan_row", slog.String("err", err.Error()))
			return
		}
		heads[orgID] = append(heads[orgID], h)
	}
	if err := rows.Err(); err != nil {
		telemetry.L(ctx).Error("audit.notarizer.iter", slog.String("err", err.Error()))
		return
	}

	for orgID, list := range heads {
		root, manifest := merkleAndManifest(list)
		sig := ed25519.Sign(n.signer, root)
		if _, err := n.pool.Exec(ctx, `
			INSERT INTO audit.notarizations (
				org_id, notarized_at, merkle_root, shard_heads, signature, signing_key
			) VALUES ($1, now(), $2, $3, $4, $5)
		`, orgID, root, manifest, sig, n.keyID); err != nil {
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
	n.lastAt.Store(time.Now().Unix())
}

// merkleAndManifest computes the Merkle root over (shard, seq, hash)
// tuples and returns the root plus a JSON manifest used as the on-disk
// shard_heads column. Single-leaf roots hash sha256(leaf) with a fixed
// domain separator to avoid the "single-leaf == root" footgun in
// rolling-implementation Merkle code.
func merkleAndManifest(list []chainHead) ([]byte, []byte) {
	sort.Slice(list, func(i, j int) bool { return list[i].shard < list[j].shard })

	leaves := make([][]byte, 0, len(list))
	manifestRows := make([]map[string]any, 0, len(list))
	for _, h := range list {
		hasher := sha256.New()
		hasher.Write([]byte{0x00}) // leaf domain separator
		var b [10]byte
		binary.BigEndian.PutUint16(b[0:2], uint16(h.shard))
		binary.BigEndian.PutUint64(b[2:10], uint64(h.seq))
		hasher.Write(b[:])
		hasher.Write(h.hash)
		leaves = append(leaves, hasher.Sum(nil))
		manifestRows = append(manifestRows, map[string]any{
			"shard":     h.shard,
			"last_seq":  h.seq,
			"last_hash": hex.EncodeToString(h.hash),
		})
	}
	root := merkleRoot(leaves)
	manifestJSON, _ := json.Marshal(manifestRows)
	return root, manifestJSON
}

// merkleRoot computes a SHA-256 Merkle root over leaves with a fixed
// domain separator on internal nodes. Empty input returns sha256(empty).
// Odd levels promote the last element by hashing it with itself.
func merkleRoot(leaves [][]byte) []byte {
	if len(leaves) == 0 {
		empty := sha256.Sum256(nil)
		return empty[:]
	}
	level := leaves
	for len(level) > 1 {
		next := make([][]byte, 0, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			h := sha256.New()
			h.Write([]byte{0x01}) // internal-node domain separator
			h.Write(level[i])
			if i+1 < len(level) {
				h.Write(level[i+1])
			} else {
				h.Write(level[i])
			}
			next = append(next, h.Sum(nil))
		}
		level = next
	}
	return level[0]
}

// loadEd25519Key parses a PEM-encoded ed25519 private key and returns it
// alongside a stable key id (sha256 of the public half, hex-truncated).
func loadEd25519Key(path string) (ed25519.PrivateKey, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read signing key: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, "", errors.New("audit signing key: not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, "", fmt.Errorf("audit signing key: %w", err)
	}
	priv, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, "", errors.New("audit signing key: not ed25519")
	}
	pub := priv.Public().(ed25519.PublicKey)
	pubHash := sha256.Sum256(pub)
	return priv, "ed25519:" + hex.EncodeToString(pubHash[:8]), nil
}

// GenerateAuditSigningKey writes a fresh ed25519 private key in PKCS#8 PEM
// format to path. Helper for the operator workflow:
//
//	go run ./cmd/server gen-audit-key /etc/tack/audit-signing.pem
func GenerateAuditSigningKey(path string) error {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	return os.WriteFile(path, pemBytes, 0o600)
}
