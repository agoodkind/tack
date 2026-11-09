package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
)

// bearerTokenBytes is the entropy behind one bearer token. 32 bytes is well
// past what a SHA-256 hash lookup needs to make guessing infeasible.
const bearerTokenBytes = 32

// bearerTokenPrefix marks a value as a Tack bearer so a leaked one is
// recognisable in a scanner's output.
const bearerTokenPrefix = "tack_"

// NewBearerToken returns a fresh random bearer value.
//
// The database stores only its SHA-256 hash. The caller must give the value
// to the token recipient and then discard its copy.
//
// Each generated value has these parts:
//   - The tack_ prefix identifies a Tack bearer value.
//   - The suffix encodes 32 random bytes as unpadded, URL-safe Base64.
func NewBearerToken() (string, error) {
	raw := make([]byte, bearerTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		slog.Error("auth.bearer_token_generate_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("generate bearer token: %w", err)
	}
	return bearerTokenPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}
