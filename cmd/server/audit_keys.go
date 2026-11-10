package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"

	"goodkind.io/tack/internal/audit"
)

// loadAuditKey reads an Ed25519 private key from the operator-supplied
// signing-key path and derives its key ID.
func loadAuditKey(path string) (ed25519.PrivateKey, string, error) {
	if path == "" {
		return nil, "", fmt.Errorf("AUDIT_SIGNING_KEY_PATH not set")
	}

	raw, err := os.ReadFile(path) // #nosec G304,G703 operator-provided audit signing key path
	if err != nil {
		slog.Error("audit.key_read_failed", slog.Any("err", err))
		return nil, "", fmt.Errorf("read audit key: %w", err)
	}

	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, "", fmt.Errorf("audit key is not PEM")
	}

	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		slog.Error("audit.key_parse_failed", slog.Any("err", err))
		return nil, "", fmt.Errorf("parse audit key: %w", err)
	}

	priv, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, "", fmt.Errorf("audit key is not ed25519")
	}

	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, "", fmt.Errorf("audit key public half is not ed25519")
	}

	// Export manifests and audit.notarizations use audit.KeyIdentifier for
	// their signer IDs (TACK-437). Older CLI manifests used the first eight
	// raw public-key bytes instead of the first eight SHA-256 digest bytes.
	// ParseSignerSet accepts either identifier's format. With a configured
	// signer set, bundle verification checks the ID derived from the supplied
	// public key against that set and rejects a different manifest signer ID.
	// Without a configured set, verification does not compare those IDs.
	return priv, audit.KeyIdentifier(pub), nil
}

// loadAuditPublic derives the Ed25519 public key from a private-key PEM file.
func loadAuditPublic(path string) (ed25519.PublicKey, error) {
	priv, _, err := loadAuditKey(path)
	if err != nil {
		return nil, err
	}

	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("audit key public half is not ed25519")
	}

	return pub, nil
}
