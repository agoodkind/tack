package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
)

// loadAuditKey reads the ed25519 private key and derives its key id.
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

// loadAuditPublic reads the ed25519 public key from a private key PEM file.
func loadAuditPublic(path string) (ed25519.PublicKey, error) {
	priv, _, err := loadAuditKey(path)
	if err != nil {
		return nil, err
	}
	return priv.Public().(ed25519.PublicKey), nil
}
