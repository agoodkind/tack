package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
)

// bearerTokenBytes sets the random input length for one bearer token. The
// 32-byte input provides 256 bits of entropy against guessing tokens that
// the database looks up by SHA-256 hash.
const bearerTokenBytes = 32

// bearerTokenPrefix identifies Tack bearer tokens in scanner output,
// including leaked tokens.
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
