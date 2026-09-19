// backup_s3_staging.go writes a downloaded backup artifact to disk without
// letting the download pile up in the page cache. The kernel charges a file's
// cached pages, dirty ones included, to the memory limit of the container that
// wrote them. An artifact staged in one unbroken copy fills the restore
// drill's limit with its own pages, and dirty pages cannot be reclaimed until
// the disk has written them (TACK-516).

package ops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
)

// stagingSyncBytes is how much of a download copyToStagedFile writes between
// syncs of the staged file. It bounds the staged data that sits dirty or
// under writeback at once, and on Linux the data that stays cached at all.
const stagingSyncBytes = 8 << 20

// copyToStagedFile copies body into file one stagingSyncBytes chunk at a time,
// syncing the file after every chunk and then releasing the chunk's cached
// pages, and returns the bytes written. The sync makes the chunk's pages clean
// before the next chunk dirties more, and the release returns them to the
// kernel instead of leaving them charged to this process's memory limit.
func copyToStagedFile(ctx context.Context, file *os.File, body io.Reader) (int64, error) {
	var written int64
	for {
		copied, copyErr := io.CopyN(file, body, stagingSyncBytes)
		if copied > 0 {
			if err := file.Sync(); err != nil {
				wrapped := fmt.Errorf("sync %s after %d bytes: %w", file.Name(), written+copied, err)
				slog.ErrorContext(ctx, "backup.s3.stage_failed", slog.String("err", wrapped.Error()))
				return written + copied, wrapped
			}
			releaseCachedPages(ctx, file, written, copied)
			written += copied
		}
		if errors.Is(copyErr, io.EOF) {
			return written, nil
		}
		if copyErr != nil {
			wrapped := fmt.Errorf("copy into %s after %d bytes: %w", file.Name(), written, copyErr)
			slog.ErrorContext(ctx, "backup.s3.stage_failed", slog.String("err", wrapped.Error()))
			return written, wrapped
		}
	}
}
