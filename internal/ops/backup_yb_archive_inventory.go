// The archive inventory records archived file paths and sizes at capture time.
// The restore compares extracted files against this inventory to detect missing
// files and size mismatches.

package ops

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"goodkind.io/tack/internal/telemetry"
)

// writeYBArchiveInventory writes an inventory beside the staged archive for upload.
func writeYBArchiveInventory(ctx context.Context, runID, node, stageDir, tarPath string) error {
	logger := telemetry.L(ctx)
	inventory, err := readYBArchiveInventory(ctx, runID, node, tarPath)
	if err != nil {
		return err
	}

	path := filepath.Join(stageDir, ybNodeInventoryObject)
	if err := os.WriteFile(path, inventory.render(), 0o600); err != nil {
		wrapped := fmt.Errorf("write tablet inventory %s: %w", path, err)
		logger.ErrorContext(
			ctx,
			"backup.yb_archive.inventory_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	logger.InfoContext(
		ctx,
		"backup.yb_archive.inventoried",
		slog.String("node", node),
		slog.Int("files", len(inventory.Files)),
	)

	return nil
}

// ybArchivedFile records a regular file's path relative to the rocksdb root
// and its archived size.
type ybArchivedFile struct {
	Path string
	Size int64
}

// ybArchiveInventory records a node archive's files, run, and node.
// Run and node IDs prevent comparisons against a different archive's inventory.
type ybArchiveInventory struct {
	RunID string
	Node  string
	Files []ybArchivedFile
}

// readYBArchiveInventory inventories the completed archive rather than the source
// directory. This sequential pass records only files the archive actually contains.
func readYBArchiveInventory(
	ctx context.Context,
	runID string,
	node string,
	tarPath string,
) (ybArchiveInventory, error) {
	logger := telemetry.L(ctx)
	empty := ybArchiveInventory{RunID: "", Node: "", Files: nil}
	archive, err := os.Open(tarPath)
	if err != nil {
		wrapped := fmt.Errorf("open %s to inventory it: %w", tarPath, err)
		logger.ErrorContext(
			ctx,
			"backup.yb_archive.inventory_failed",
			slog.String("err", wrapped.Error()),
		)

		return empty, wrapped
	}
	defer archive.Close()
	unzipped, err := gzip.NewReader(archive)
	if err != nil {
		wrapped := fmt.Errorf("read %s as a gzip tablet archive: %w", tarPath, err)
		logger.ErrorContext(
			ctx,
			"backup.yb_archive.inventory_failed",
			slog.String("err", wrapped.Error()),
		)

		return empty, wrapped
	}
	defer unzipped.Close()
	files, err := readYBArchivedFiles(ctx, tarPath, tar.NewReader(unzipped))
	if err != nil {
		return empty, err
	}

	if len(files) == 0 {
		err := fmt.Errorf("tablet archive %s does not contain regular files", tarPath)
		logger.ErrorContext(
			ctx,
			"backup.yb_archive.inventory_failed",
			slog.String("err", err.Error()),
		)

		return empty, err
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	return ybArchiveInventory{RunID: runID, Node: node, Files: files}, nil
}

// readYBArchivedFiles skips directories and rejects entries other than regular
// files. Silently skipping unsupported entries would omit them from restore
// verification. The export archives each snapshot directory once without hard links.
func readYBArchivedFiles(
	ctx context.Context,
	tarPath string,
	reader *tar.Reader,
) ([]ybArchivedFile, error) {
	logger := telemetry.L(ctx)
	var files []ybArchivedFile

	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return files, nil
		}

		if err != nil {
			wrapped := fmt.Errorf("read %s as a tablet archive: %w", tarPath, err)
			logger.ErrorContext(
				ctx,
				"backup.yb_archive.inventory_failed",
				slog.String("err", wrapped.Error()),
			)

			return nil, wrapped
		}

		if header.Typeflag == tar.TypeDir {
			continue
		}

		if header.Typeflag != tar.TypeReg {
			err := fmt.Errorf(
				"tablet archive %s contains %q with unsupported entry type %q",
				tarPath,
				header.Name,
				string(header.Typeflag),
			)
			logger.ErrorContext(
				ctx,
				"backup.yb_archive.inventory_failed",
				slog.String("err", err.Error()),
			)

			return nil, err
		}

		path, err := ybArchivedPath(header.Name)
		if err != nil {
			logger.ErrorContext(
				ctx,
				"backup.yb_archive.inventory_failed",
				slog.String("err", err.Error()),
			)

			return nil, err
		}

		files = append(files, ybArchivedFile{Path: path, Size: header.Size})
	}
}

// ybArchivedPath removes the export's "./" prefix from a rocksdb-relative path.
// It rejects empty paths and tab or newline delimiters that would corrupt the
// inventory's tab-separated path and size records.
func ybArchivedPath(name string) (string, error) {
	path := strings.TrimPrefix(name, "./")
	if path == "" || strings.ContainsAny(path, "\t\n") {
		return "", fmt.Errorf(
			"tablet archive entry %q cannot be recorded as an inventory line",
			name,
		)
	}

	return path, nil
}
