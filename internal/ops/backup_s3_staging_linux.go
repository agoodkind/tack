//go:build linux

package ops

import (
	"context"
	"log/slog"
	"os"

	"golang.org/x/sys/unix"
)

// releaseCachedPages asks the kernel to drop the cached pages of file's
// length bytes at offset, which the caller has just synced, so they stop
// counting against this process's memory limit. The advice is best effort: a
// refusal leaves clean pages the kernel can still reclaim on its own, so it is
// logged and the copy goes on.
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
		slog.InfoContext(ctx, "backup.s3.stage_release_refused",
			slog.String("path", file.Name()), slog.String("reason", err.Error()))
	}
}
