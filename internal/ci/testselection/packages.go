package testselection

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type packageDirectory struct {
	absolute string
	relative string
}

func packageDirectories(root string, patterns []string) ([]packageDirectory, error) {
	if len(patterns) == 0 {
		return nil, errPatternsRequired
	}

	root, err := filepath.Abs(root)
	if err != nil {
		slog.Error("testselection.root_failed", slog.String("path", root), slog.Any("error", err))

		return nil, fmt.Errorf("resolve module root %s: %w", root, err)
	}

	module, err := os.Stat(filepath.Join(root, "go.mod"))
	if err != nil {
		slog.Error("testselection.module_failed", slog.String("path", root), slog.Any("error", err))

		return nil, fmt.Errorf("read module root %s: %w", root, err)
	}

	if module.IsDir() {
		slog.Error(
			"testselection.module_failed",
			slog.String("path", root),
			slog.Any("error", errModuleFile),
		)

		return nil, fmt.Errorf("module root %s: %w", root, errModuleFile)
	}

	directories := make(map[string]packageDirectory)

	for _, pattern := range patterns {
		if err := addPattern(root, pattern, directories); err != nil {
			return nil, err
		}
	}

	result := make([]packageDirectory, 0, len(directories))
	for _, directory := range directories {
		result = append(result, directory)
	}

	slices.SortFunc(
		result,
		func(left, right packageDirectory) int {
			return strings.Compare(left.relative, right.relative)
		},
	)

	return result, nil
}

func addPattern(root, pattern string, directories map[string]packageDirectory) error {
	recursive := strings.HasSuffix(pattern, "/...")
	base := strings.TrimSuffix(pattern, "/...")
	if !strings.HasPrefix(pattern, "./") || strings.ContainsAny(base, "\t\r\n *?[]") {
		slog.Error(
			"testselection.pattern_failed",
			slog.String("pattern", pattern),
			slog.Any("error", errRelativePattern),
		)

		return fmt.Errorf("invalid package pattern %q: %w", pattern, errRelativePattern)
	}

	clean := filepath.Clean(base)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) ||
		strings.Contains(clean, "...") {
		slog.Error(
			"testselection.pattern_failed",
			slog.String("pattern", pattern),
			slog.Any("error", errModulePath),
		)

		return fmt.Errorf("invalid package pattern %q: %w", pattern, errModulePath)
	}

	absolute := filepath.Join(root, clean)
	info, err := os.Stat(absolute)
	if err != nil {
		slog.Error(
			"testselection.pattern_failed",
			slog.String("pattern", pattern),
			slog.Any("error", err),
		)

		return fmt.Errorf("read package pattern %q: %w", pattern, err)
	}

	if !info.IsDir() {
		slog.Error(
			"testselection.pattern_failed",
			slog.String("pattern", pattern),
			slog.Any("error", errPackageDirectory),
		)

		return fmt.Errorf("package pattern %q: %w", pattern, errPackageDirectory)
	}

	if err := validatePackagePath(root, clean); err != nil {
		slog.Error(
			"testselection.path_failed",
			slog.String("pattern", pattern),
			slog.Any("error", err),
		)

		return fmt.Errorf("invalid package pattern %q: %w", pattern, err)
	}

	err = filepath.WalkDir(absolute, packageVisitor(root, recursive, directories))
	if err != nil {
		slog.Error(
			"testselection.walk_failed",
			slog.String("pattern", pattern),
			slog.Any("error", err),
		)

		return fmt.Errorf("walk package pattern %q: %w", pattern, err)
	}

	return nil
}
