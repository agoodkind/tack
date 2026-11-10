// The tablet inventory records each archived file's path and size at capture
// time. The export uploads it beside the archive. The restore compares the
// extracted files with this record instead of assuming which files a tablet
// should contain. A tool that silently omits a file can otherwise leave a
// restore unable to detect that loss.

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

// writeYBArchiveInventory records the staged archive's files in a separate
// object beside the archive. The upload publishes both artifacts.
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

// ybArchivedFile records a regular file's path relative to the RocksDB root
// and its archived size.
type ybArchivedFile struct {
	Path string
	Size int64
}

// ybArchiveInventory records a node archive's files and the run and node
// identities. The restore checks those identities before comparing an
// extraction. An inventory for another node lists different files and can
// reject a healthy extraction.
type ybArchiveInventory struct {
	RunID string
	Node  string
	Files []ybArchivedFile
}

// readYBArchiveInventory records files from the finished staged archive
// instead of the source tree. It cannot list a file that the tree walk found
// but the archive omitted. Reading the archive requires a sequential local
// pass after the compression pass that created it.
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
		err := fmt.Errorf("tablet archive %s carries no files, only directory entries", tarPath)
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

// readYBArchivedFiles reads archive members and skips directories. It rejects
// members that are not regular files because the restore cannot check an
// unrecorded member. The export archives each tablet snapshot directory once
// to avoid duplicate inode entries and tar hard links.
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
				"tablet archive %s carries %q as a %q entry, which an inventory cannot describe",
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

// ybArchivedPath removes the export's ./ prefix from a member path relative
// to the RocksDB root. Inventory lines separate each path and size with a
// tab and end with a newline. The helper rejects either delimiter in a path
// to prevent a filename from creating extra inventory fields or records.
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
