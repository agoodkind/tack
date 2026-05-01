package main

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
)

// runAuditExport produces a signed JSONL bundle of audit.events for an org
// and time range. Usage:
//
//	./server audit-export --org <uuid> --oldest <RFC3339> --latest <RFC3339> --out <dir>
func runAuditExport(cfg *config.Config, argv []string) {
	fs := flag.NewFlagSet("audit-export", flag.ExitOnError)
	orgStr := fs.String("org", "", "org_id (UUID)")
	oldestStr := fs.String("oldest", "", "RFC3339 lower bound (inclusive)")
	latestStr := fs.String("latest", "", "RFC3339 upper bound (exclusive)")
	requestID := fs.String("request-id", "", "request_id stored in audit context")
	traceID := fs.String("trace-id", "", "trace_id stored in audit context")
	out := fs.String("out", "", "output directory")
	limit := fs.Int("limit", 100000, "max rows")
	_ = fs.Parse(argv)

	if *orgStr == "" || *oldestStr == "" || *latestStr == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: audit-export --org <uuid> --oldest <RFC3339> --latest <RFC3339> --out <dir> [--limit N]")
		os.Exit(2)
	}
	orgID, err := uuid.Parse(*orgStr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "audit-export: --org not a UUID")
		os.Exit(2)
	}
	oldest, err := time.Parse(time.RFC3339, *oldestStr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "audit-export: --oldest must be RFC3339")
		os.Exit(2)
	}
	latest, err := time.Parse(time.RFC3339, *latestStr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "audit-export: --latest must be RFC3339")
		os.Exit(2)
	}

	ctx := context.Background()
	reader, err := audit.NewReader(ctx, cfg.AuditReaderDSN)
	if err != nil {
		slog.Error("audit-export: reader", "err", err)
		os.Exit(1)
	}
	defer reader.Close()

	priv, keyID, err := loadAuditKey(cfg.AuditSigningKeyPath)
	if err != nil {
		slog.Error("audit-export: key", "err", err)
		os.Exit(1)
	}

	manifest, err := audit.Export(ctx, reader, priv, keyID, audit.QueryFilter{
		OrgID:     orgID,
		Oldest:    oldest,
		Latest:    latest,
		RequestID: *requestID,
		TraceID:   *traceID,
		Limit:     *limit,
	}, *out)
	if err != nil {
		slog.Error("audit-export: write", "err", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(manifest)
}

// runAuditVerify validates a bundle directory against the public key derived
// from AUDIT_SIGNING_KEY_PATH (or a path passed via --pub).
func runAuditVerify(argv []string) {
	fs := flag.NewFlagSet("audit-verify", flag.ExitOnError)
	bundle := fs.String("bundle", "", "bundle directory produced by audit-export")
	pubPath := fs.String("pub", "", "PEM path to ed25519 public key (defaults to AUDIT_SIGNING_KEY_PATH)")
	_ = fs.Parse(argv)
	if *bundle == "" {
		fmt.Fprintln(os.Stderr, "usage: audit-verify --bundle <dir> [--pub <pem>]")
		os.Exit(2)
	}

	keyPath := *pubPath
	if keyPath == "" {
		keyPath = os.Getenv("AUDIT_SIGNING_KEY_PATH")
	}
	if keyPath == "" {
		fmt.Fprintln(os.Stderr, "audit-verify: --pub or AUDIT_SIGNING_KEY_PATH required")
		os.Exit(2)
	}
	pub, err := loadAuditPublic(keyPath)
	if err != nil {
		slog.Error("audit-verify: key", "err", err)
		os.Exit(1)
	}
	report, err := audit.VerifyBundle(*bundle, pub)
	if err != nil {
		slog.Error("audit-verify: scan", "err", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(report)
}

func loadAuditKey(path string) (ed25519.PrivateKey, string, error) {
	if path == "" {
		return nil, "", fmt.Errorf("AUDIT_SIGNING_KEY_PATH not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, "", fmt.Errorf("not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, "", err
	}
	priv, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, "", fmt.Errorf("not ed25519")
	}
	pub := priv.Public().(ed25519.PublicKey)
	keyID := "ed25519:" + fmt.Sprintf("%x", pub[:8])
	return priv, keyID, nil
}

func loadAuditPublic(path string) (ed25519.PublicKey, error) {
	priv, _, err := loadAuditKey(path)
	if err != nil {
		return nil, err
	}
	return priv.Public().(ed25519.PublicKey), nil
}
