//go:build !linux

package objectstage

import (
	"context"
	"os"
)

// releaseCachedPages does not release cached pages outside Linux. The restore
// drill runs in the Linux tack-ops container. Other supported development
// platforms do not provide a per-range cache release call.
func releaseCachedPages(_ context.Context, _ *os.File, _, _ int64) {}
