//go:build linux

package objectstage

import (
	"context"
	"log/slog"
	"os"

	"golang.org/x/sys/unix"
)

// releaseCachedPages asks the kernel to release cached pages for length bytes
// at offset after the caller syncs them. Releasing pages reduces this process's
// memory usage. The kernel can refuse the request and reclaim clean pages
// later. The function logs refusals without interrupting the copy.
func releaseCachedPages(ctx context.Context, file *os.File, offset, length int64) {
	raw, err := file.SyscallConn()
	if err == nil {
		controlErr := raw.Control(func(descriptor uintptr) {
			err = unix.Fadvise(int(descriptor), offset, length, unix.FADV_DONTNEED)
		})
		if controlErr != nil {
			err = controlErr
		}
	}

	if err != nil {
		slog.InfoContext(
			ctx,
			"backup.s3.stage_release_refused",
			slog.String("path", file.Name()),
			slog.String("reason", err.Error()),
		)
	}
}
