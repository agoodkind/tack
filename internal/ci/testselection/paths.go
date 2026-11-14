package testselection

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

func excludedDirectory(name string) bool {
	return name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") ||
		strings.HasPrefix(name, "_")
}

func validatePackagePath(root, relative string) error {
	if relative == "." {
		return nil
	}

	path := root

	for component := range strings.SplitSeq(relative, string(filepath.Separator)) {
		path = filepath.Join(path, component)
		if excludedDirectory(component) {
			slog.Error(
				"testselection.path_failed",
				slog.String("path", path),
				slog.Any("error", errExcludedDirectory),
			)

			return fmt.Errorf("directory %s: %w", path, errExcludedDirectory)
		}

		if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
			slog.Error(
				"testselection.module_failed",
				slog.String("path", path),
				slog.Any("error", errNestedModule),
			)

			return fmt.Errorf("directory %s: %w", path, errNestedModule)
		} else if !os.IsNotExist(err) {
			slog.Error(
				"testselection.module_failed",
				slog.String("path", path),
				slog.Any("error", err),
			)

			return fmt.Errorf("inspect nested module %s: %w", path, err)
		}
	}

	return nil
}
