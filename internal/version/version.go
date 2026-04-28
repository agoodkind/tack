// Package version exposes build metadata injected at compile time via ldflags
// and a self-computed binary hash at startup.
package version

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sync"
)

// Set at compile time via -ldflags:
//
//	-X goodkind.io/tack/internal/version.commit=$(COMMIT)
//	-X goodkind.io/tack/internal/version.buildTime=$(BUILD_TIME)
//	-X goodkind.io/tack/internal/version.tag=$(VERSION)
//	-X goodkind.io/tack/internal/version.dirty=$(DIRTY)
var (
	commit    string
	buildTime string
	tag       string
	dirty     string
)

var (
	buildHash     string
	buildHashOnce sync.Once
)

func Commit() string    { return def(commit, "dev") }
func BuildTime() string { return def(buildTime, "unknown") }
func Tag() string       { return def(tag, "dev") }
func Dirty() bool       { return dirty == "true" }

// BuildHash returns the first 12 hex chars of the SHA-256 of the running binary.
// Computed once on first call.
func BuildHash() string {
	buildHashOnce.Do(func() {
		buildHash = computeBuildHash()
	})
	return buildHash
}

func computeBuildHash() string {
	exe, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	f, err := os.Open(exe)
	if err != nil {
		return "unknown"
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

func def(val, fallback string) string {
	if val == "" {
		return fallback
	}
	return val
}
