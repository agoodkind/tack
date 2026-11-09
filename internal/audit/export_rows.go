// Writing a bundle's rows must use memory that does not grow with the ledger
// size.

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

// exportWriteBufferBytes sets the bundle's write-buffer size. Encoding directly
// to the file descriptor requires one write syscall per row, which repeats
// hundreds of thousands of times for a ledger-sized export.
const exportWriteBufferBytes = 1 << 20

// writeExportRows writes the filter's rows into the bundle file and returns
// their count and digest.
//
// The encoder processes one row at a time without retaining an export-sized
// slice. The digest covers the bytes sent to the file, and a counter records
// the row count. An earlier implementation collected all rows before writing;
// fitting that slice in memory prevented export of a production-sized
// organization.
//
// The path includes this export's ID. Exclusive creation prevents truncating
// another export's active or published file. After creation, every failure
// attempts to remove the incomplete file.
//
// The function returns success only after synchronizing the rows and their
// directory entry to stable storage. The caller can then sign their manifest
// and remove the rows of the superseded bundle.
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
		if partsErr := attachManifestParts(ctx, source, &row); partsErr != nil {
			return partsErr
		}
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
	// The write buffer may still contain bytes that the digest already includes.
	// Flush the buffer and return any failure before signing; otherwise the
	// manifest could certify bytes that the file lacks.
	if flushErr := buffered.Flush(); flushErr != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return 0, "", fmt.Errorf("audit export flush: %w", flushErr)
	}
	// A successful flush writes bytes to the kernel's page cache. Power loss can
	// discard those bytes before disk synchronization. Synchronize the file before
	// the caller signs its manifest and deletes superseded rows; both later
	// operations depend on durable rows.
	if syncErr := file.Sync(); syncErr != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return 0, "", fmt.Errorf("audit export sync: %w", syncErr)
	}
	if closeErr := file.Close(); closeErr != nil {
		_ = os.Remove(path)
		return 0, "", fmt.Errorf("audit export close: %w", closeErr)
	}
	// Synchronizing the file does not synchronize its new directory entry.
	// Synchronize the directory before returning success to preserve the filename
	// after power loss.
	if syncErr := syncExportDirectory(filepath.Dir(path)); syncErr != nil {
		_ = os.Remove(path)
		return 0, "", syncErr
	}
	return rowCount, hex.EncodeToString(fileDigest.Sum(nil)), nil
}
