//go:build !linux

package ops

import (
	"context"
	"os"
)

// releaseCachedPages does nothing off Linux. The restore drill runs only in
// the Linux tack-ops container, and the other platforms this package builds
// on, for local development, have no per-range cache release call.
func releaseCachedPages(_ context.Context, _ *os.File, _, _ int64) {}
