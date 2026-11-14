// export_rows.go writes bundle rows with bounded memory.

package audit

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// exportWriteBufferBytes limits buffered writes instead of issuing one write
// system call per row.
const exportWriteBufferBytes = 1 << 20

// writeExportRows encodes one row at a time and returns the row count and file
// digest. Each export uses its own exclusive path. Failures remove that file
// without changing another export's rows. Success requires durable file contents
// and a durable directory entry before signing or reclaiming superseded rows.
func writeExportRows(
	ctx context.Context,
	source RowSource,
	filter QueryFilter,
	path string,
) (int, string, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.export.open_failed",
			slog.String("path", path),
			slog.String("err", err.Error()),
		)

		return 0, "", fmt.Errorf("audit export open: %w", err)
	}

	fileDigest := sha256.New()
	buffered := bufio.NewWriterSize(file, exportWriteBufferBytes)
	encoder := json.NewEncoder(io.MultiWriter(buffered, fileDigest))

	rowCount := 0
	streamErr := source.StreamQuery(
		ctx,
		filter,
		func(row Row) error {
			if partsErr := attachManifestParts(ctx, source, &row); partsErr != nil {
				return partsErr
			}

			if encodeErr := encoder.Encode(row); encodeErr != nil {
				return fmt.Errorf("encode %s: %w", row.EventID, encodeErr)
			}

			rowCount++

			return nil
		},
	)
	if streamErr != nil {
		_ = file.Close()
		_ = os.Remove(path)

		return 0, "", fmt.Errorf("audit export query: %w", streamErr)
	}

	// The digest includes buffered bytes. Flush them before signing their
	// digest, and return any failure rather than accepting a truncated file.
	if flushErr := buffered.Flush(); flushErr != nil {
		_ = file.Close()
		_ = os.Remove(path)

		return 0, "", fmt.Errorf("audit export flush: %w", flushErr)
	}

	// Flush writes to the kernel cache. Sync must persist contents before
	// the caller publishes a manifest or removes superseded rows.
	if syncErr := file.Sync(); syncErr != nil {
		_ = file.Close()
		_ = os.Remove(path)

		return 0, "", fmt.Errorf("audit export sync: %w", syncErr)
	}

	if closeErr := file.Close(); closeErr != nil {
		_ = os.Remove(path)
		return 0, "", fmt.Errorf("audit export close: %w", closeErr)
	}

	// Persist the directory entry as well as the file contents before success.
	if syncErr := syncExportDirectory(filepath.Dir(path)); syncErr != nil {
		_ = os.Remove(path)
		return 0, "", syncErr
	}

	return rowCount, hex.EncodeToString(fileDigest.Sum(nil)), nil
}
