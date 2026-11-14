package testselection

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

func packageVisitor(
	root string,
	recursive bool,
	directories map[string]packageDirectory,
) fs.WalkDirFunc {
	return func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			slog.Error(
				"testselection.directory_failed",
				slog.String("path", path),
				slog.Any("error", walkErr),
			)

			return fmt.Errorf("read package directory %s: %w", path, walkErr)
		}

		if !entry.IsDir() {
			return nil
		}

		if path != root {
			if excludedDirectory(entry.Name()) {
				return filepath.SkipDir
			}

			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return filepath.SkipDir
			} else if !os.IsNotExist(err) {
				slog.Error(
					"testselection.module_failed",
					slog.String("path", path),
					slog.Any("error", err),
				)

				return fmt.Errorf("inspect nested module %s: %w", path, err)
			}
		}

		relative, err := filepath.Rel(root, path)
		if err != nil {
			slog.Error(
				"testselection.relative_path_failed",
				slog.String("path", path),
				slog.Any("error", err),
			)

			return fmt.Errorf("resolve package directory %s: %w", path, err)
		}

		if relative != "." {
			relative = "./" + filepath.ToSlash(relative)
		}

		directories[path] = packageDirectory{absolute: path, relative: relative}
		if !recursive {
			return filepath.SkipDir
		}

		return nil
	}
}
