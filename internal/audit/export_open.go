// export_open.go opens the file a manifest names. The manifest is untrusted
// input until its signature verifies, and the verifier has to read the rows to
// compute the digest that check is made against, so the open happens before any
// verdict exists. What the name resolves to is therefore settled here rather
// than by the name itself.

package audit

import (
	"fmt"
	"log/slog"
	"os"
	"syscall"
)

// openBundleRowsFile opens a bundle's rows for reading and refuses anything that
// is not a regular file.
//
// Checking the name cannot do this job. A name that is a plain entry in the
// bundle directory still resolves to whatever is sitting at that entry, and two
// kinds of entry attack the verifier: a symbolic link reads a file of the
// planter's choosing while the digest and the row hashes are computed over it,
// and a named pipe with no writer parks the verifier inside open(2) with no
// timeout and no verdict.
//
// O_NOFOLLOW fails the open with ELOOP when the final path component is a
// symbolic link, so the link is never traversed and the target is never read.
// O_NONBLOCK makes the open of a pipe return at once instead of waiting for a
// writer, which converts the hang into an open that then fails the check below.
// Neither flag changes how a regular file is read.
//
// The kind is decided from the descriptor that was opened, not from a separate
// look at the path, so there is no window in which the entry can be swapped
// between the check and the open.
func openBundleRowsFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		slog.Error("audit.verify.rows_open_refused",
			slog.String("path", path), slog.String("err", err.Error()))
		return nil, fmt.Errorf("open bundle rows %s: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		slog.Error("audit.verify.rows_stat_failed",
			slog.String("path", path), slog.String("err", err.Error()))
		return nil, fmt.Errorf("stat bundle rows %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		kindErr := fmt.Errorf("bundle rows %s is not a regular file: mode %s", path, info.Mode())
		slog.Error("audit.verify.rows_not_regular",
			slog.String("path", path), slog.String("err", kindErr.Error()))
		return nil, kindErr
	}
	return file, nil
}
