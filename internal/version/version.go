// Package version provides build metadata that linker flags set during
// compilation. It computes the running binary's hash on the first
// BuildHash call. The server requests this hash during startup.
package version

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sync"
)

// Linker flags (-ldflags) set these values during compilation:
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

// BuildHash returns the first 12 hexadecimal characters of the running
// binary's SHA-256 hash, a digest of its contents.
//
// The first call computes and caches the result. Later calls reuse it.
//
//   - It returns the hash prefix after reading the binary successfully.
//   - It returns "unknown" if locating, opening, or reading the binary fails.
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
