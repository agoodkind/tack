package audit

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

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

// GenerateAuditSigningKey writes a fresh Ed25519 private key in PKCS#8 PEM
// format to path for the operator workflow.
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
