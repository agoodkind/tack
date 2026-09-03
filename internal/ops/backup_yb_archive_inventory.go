// backup_yb_archive_inventory.go records what one node's tablet archive
// carries. Every check between the export and the restore is a claim by
// whichever tool touched the archive last, and a tool that loses part of a
// tablet without saying so leaves a restore that cannot tell. The inventory is
// a fact recorded at capture time: the path and size of every file the archive
// holds, uploaded beside it, so the restore compares its extraction against
// what this backup actually captured instead of guessing what a tablet ought to
// contain.

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

// writeYBArchiveInventory records what the staged archive carries and stages
// that record beside it under its own object name, ready for the upload that
// publishes both.
func writeYBArchiveInventory(ctx context.Context, runID, node, stageDir, tarPath string) error {
	logger := telemetry.L(ctx)
	inventory, err := readYBArchiveInventory(ctx, runID, node, tarPath)
	if err != nil {
		return err
	}
	path := filepath.Join(stageDir, ybNodeInventoryObject)
	if err := os.WriteFile(path, inventory.render(), 0o600); err != nil {
		wrapped := fmt.Errorf("write tablet inventory %s: %w", path, err)
		logger.ErrorContext(ctx, "backup.yb_archive.inventory_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	logger.InfoContext(ctx, "backup.yb_archive.inventoried",
		slog.String("node", node), slog.Int("files", len(inventory.Files)))
	return nil
}

// ybArchivedFile is one regular file the node archive carries: its path
// relative to the rocksdb root the export tarred from, and the size it was
// archived at.
type ybArchivedFile struct {
	Path string
	Size int64
}

// ybArchiveInventory is everything one node's archive carries, together with
// the run and node it was recorded for. The restore verifies an extraction
// against it, so it declares its own run and node the way the run manifest
// does: an inventory read for the wrong node describes a different set of files
// and would condemn a healthy extraction.
type ybArchiveInventory struct {
	RunID string
	Node  string
	Files []ybArchivedFile
}

// readYBArchiveInventory reads the staged archive back and records every file
// in it. It reads the finished artifact rather than the tree the tar walked,
// so the inventory describes the bytes that get uploaded: a file the walk saw
// but the archive did not keep can never reach it. The cost is one sequential
// pass over a local file, against the compress pass that produced it.
func readYBArchiveInventory(ctx context.Context, runID, node, tarPath string) (ybArchiveInventory, error) {
	logger := telemetry.L(ctx)
	empty := ybArchiveInventory{RunID: "", Node: "", Files: nil}
	archive, err := os.Open(tarPath)
	if err != nil {
		wrapped := fmt.Errorf("open %s to inventory it: %w", tarPath, err)
		logger.ErrorContext(ctx, "backup.yb_archive.inventory_failed", slog.String("err", wrapped.Error()))
		return empty, wrapped
	}
	defer archive.Close()
	unzipped, err := gzip.NewReader(archive)
	if err != nil {
		wrapped := fmt.Errorf("read %s as a gzip tablet archive: %w", tarPath, err)
		logger.ErrorContext(ctx, "backup.yb_archive.inventory_failed", slog.String("err", wrapped.Error()))
		return empty, wrapped
	}
	defer unzipped.Close()
	files, err := readYBArchivedFiles(ctx, tarPath, tar.NewReader(unzipped))
	if err != nil {
		return empty, err
	}
	if len(files) == 0 {
		err := fmt.Errorf("tablet archive %s carries no files, only directory entries", tarPath)
		logger.ErrorContext(ctx, "backup.yb_archive.inventory_failed", slog.String("err", err.Error()))
		return empty, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return ybArchiveInventory{RunID: runID, Node: node, Files: files}, nil
}

// readYBArchivedFiles walks the archive's members. Directory entries carry no
// bytes and are skipped; anything that is neither a directory nor a regular
// file is refused rather than skipped, because a member the inventory does not
// describe is a member the restore never checks, which is the silence this
// whole record exists to remove. The export tars each tablet's snapshot
// directory once, so no two members name one inode and tar emits no hard links.
func readYBArchivedFiles(ctx context.Context, tarPath string, reader *tar.Reader) ([]ybArchivedFile, error) {
	logger := telemetry.L(ctx)
	var files []ybArchivedFile
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return files, nil
		}
		if err != nil {
			wrapped := fmt.Errorf("read %s as a tablet archive: %w", tarPath, err)
			logger.ErrorContext(ctx, "backup.yb_archive.inventory_failed", slog.String("err", wrapped.Error()))
			return nil, wrapped
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg {
			err := fmt.Errorf("tablet archive %s carries %q as a %q entry, which an inventory cannot describe",
				tarPath, header.Name, string(header.Typeflag))
			logger.ErrorContext(ctx, "backup.yb_archive.inventory_failed", slog.String("err", err.Error()))
			return nil, err
		}
		path, err := ybArchivedPath(header.Name)
		if err != nil {
			logger.ErrorContext(ctx, "backup.yb_archive.inventory_failed", slog.String("err", err.Error()))
			return nil, err
		}
		files = append(files, ybArchivedFile{Path: path, Size: header.Size})
	}
}

// ybArchivedPath is the inventory's form of one member name: the path relative
// to the rocksdb root, without the "./" the export's find prefixes every entry
// with. The inventory is one line per file with a tab between path and size, so
// a name carrying either delimiter is refused here rather than splitting a line
// the restore would then read as two files it never finds.
func ybArchivedPath(name string) (string, error) {
	path := strings.TrimPrefix(name, "./")
	if path == "" || strings.ContainsAny(path, "\t\n") {
		return "", fmt.Errorf("tablet archive entry %q cannot be recorded as an inventory line", name)
	}
	return path, nil
}
