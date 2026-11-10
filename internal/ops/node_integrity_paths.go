package ops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"goodkind.io/tack/internal/domain"
)

// repairDirectory stores the resolved absolute path of the repair directory
// in its root field.
type repairDirectory struct {
	root string
}

// openRepairDirectory requires configured to be an absolute path, resolves
// its symbolic links, and requires the result to be an existing directory.
// It returns an error that wraps domain.ErrInvalidArgument when configured
// is not absolute or does not identify a directory.
func openRepairDirectory(ctx context.Context, configured string) (repairDirectory, error) {
	if !filepath.IsAbs(configured) {
		return repairDirectory{}, repairPathRefused(ctx,
			fmt.Sprintf("TACK_REPAIR_DIR %q is not an absolute path; set it to the repair directory that the app service mounts", configured))
	}
	root, err := filepath.EvalSymlinks(configured)
	if err != nil {
		return repairDirectory{}, repairPathFailure(ctx, "resolve TACK_REPAIR_DIR "+configured, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return repairDirectory{}, repairPathFailure(ctx, "read TACK_REPAIR_DIR "+root, err)
	}
	if !info.IsDir() {
		return repairDirectory{}, repairPathRefused(ctx, fmt.Sprintf("TACK_REPAIR_DIR %q is not a directory", root))
	}
	return repairDirectory{root: root}, nil
}

// require requires path to be absolute and to resolve inside the repair
// directory, excluding the directory itself. It resolves the existing part
// of path with resolveExistingPrefix. It returns an error that wraps
// domain.ErrInvalidArgument and includes label when path is relative or
// resolves outside the directory or to the directory itself.
func (directory repairDirectory) require(ctx context.Context, label, path string) error {
	if !filepath.IsAbs(path) {
		return repairPathRefused(ctx, fmt.Sprintf("the %s path %q is not an absolute path", label, path))
	}
	resolved, err := resolveExistingPrefix(ctx, filepath.Clean(path))
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(directory.root, resolved)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return repairPathRefused(ctx, fmt.Sprintf("the %s path %q is outside the repair directory %s", label, path, directory.root))
	}
	return nil
}

// requireExports calls require for the export file path of each delete
// entry in entries.
func (directory repairDirectory) requireExports(ctx context.Context, entries []NodeIntegrityEntry) error {
	for _, entry := range entries {
		if entry.Delete == nil {
			continue
		}
		if err := directory.require(ctx, "export file of repair "+entry.RepairID, entry.Delete.ExportFile); err != nil {
			return err
		}
	}
	return nil
}

// resolveExistingPrefix resolves the symbolic links in path. When path does
// not exist, it resolves the nearest existing parent directory and joins the
// remaining path elements to that result. It returns any other resolution
// error.
func resolveExistingPrefix(ctx context.Context, path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	parent := filepath.Dir(path)
	if !errors.Is(err, fs.ErrNotExist) || parent == path {
		return "", repairPathFailure(ctx, "resolve "+path, err)
	}
	resolvedParent, err := resolveExistingPrefix(ctx, parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedParent, filepath.Base(path)), nil
}

func repairPathRefused(ctx context.Context, reason string) error {
	wrapped := fmt.Errorf("repair directory: %s: %w", reason, domain.ErrInvalidArgument)
	slog.ErrorContext(ctx, "repair.path_refused", slog.String("err", wrapped.Error()))
	return wrapped
}

func repairPathFailure(ctx context.Context, step string, err error) error {
	wrapped := fmt.Errorf("repair directory: %s: %w", step, err)
	slog.ErrorContext(ctx, "repair.path_failed", slog.String("err", wrapped.Error()))
	return wrapped
}
