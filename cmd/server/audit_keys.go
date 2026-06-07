package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
)

// loadAuditKey reads the ed25519 private key and derives its key id. The path
// is an operator-supplied signing key location.
func loadAuditKey(path string) (ed25519.PrivateKey, string, error) {
	if path == "" {
		return nil, "", fmt.Errorf("AUDIT_SIGNING_KEY_PATH not set")
	}
	raw, err := os.ReadFile(path) // #nosec G304,G703 operator-provided audit signing key path
	if err != nil {
		slog.Error("audit.key_read_failed", slog.String("err", err.Error()))
		return nil, "", fmt.Errorf("read audit key: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, "", fmt.Errorf("audit key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		slog.Error("audit.key_parse_failed", slog.String("err", err.Error()))
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
	keyID := "ed25519:" + fmt.Sprintf("%x", pub[:8])
	return priv, keyID, nil
}

// loadAuditPublic reads the ed25519 public key from a private key PEM file.
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
