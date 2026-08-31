// export_rows.go writes a bundle's rows. It is the half of an export whose cost
// must not grow with the ledger.

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
)

// exportWriteBufferBytes buffers the bundle's writes. Encoding straight to the
// file descriptor costs one write syscall per row, which a ledger-sized export
// pays hundreds of thousands of times.
const exportWriteBufferBytes = 1 << 20

// writeExportRows streams the filter's rows straight into the bundle file and
// reports how many it wrote and the digest of what it wrote.
//
// Nothing here grows with the export: a row is encoded and released as it
// arrives, the digest folds in the same bytes the file receives, and the count
// is a counter. Collecting the rows into a slice first is what made a
// production-sized org unexportable, because the slice, not the file, is what
// had to fit in memory.
//
// The path is this export's own, named for its export id, so the create below
// can truncate nothing another export is writing or has published, and every
// failure removes the file rather than leaving rows no manifest will name.
func writeExportRows(ctx context.Context, source RowSource, filter QueryFilter, path string) (int, string, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		slog.ErrorContext(ctx, "audit.export.open_failed",
			slog.String("path", path), slog.String("err", err.Error()))
		return 0, "", fmt.Errorf("audit export open: %w", err)
	}
	fileDigest := sha256.New()
	buffered := bufio.NewWriterSize(file, exportWriteBufferBytes)
	encoder := json.NewEncoder(io.MultiWriter(buffered, fileDigest))

	rowCount := 0
	streamErr := source.StreamQuery(ctx, filter, func(row Row) error {
		if encodeErr := encoder.Encode(row); encodeErr != nil {
			return fmt.Errorf("encode %s: %w", row.EventID, encodeErr)
		}
		rowCount++
		return nil
	})
	if streamErr != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return 0, "", fmt.Errorf("audit export query: %w", streamErr)
	}
	// The buffer holds the tail of the file, and the digest has already counted
	// those bytes. Skipping the flush would sign a digest of rows the bundle
	// does not carry, so the failure has to reach the caller rather than be
	// swallowed by the deferred close.
	if flushErr := buffered.Flush(); flushErr != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return 0, "", fmt.Errorf("audit export flush: %w", flushErr)
	}
	if closeErr := file.Close(); closeErr != nil {
		_ = os.Remove(path)
		return 0, "", fmt.Errorf("audit export close: %w", closeErr)
	}
	return rowCount, hex.EncodeToString(fileDigest.Sum(nil)), nil
}
