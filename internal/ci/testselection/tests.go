package testselection

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

func testExpression(directory packageDirectory, ordinary, tagged build.Context) (string, error) {
	entries, err := os.ReadDir(directory.absolute)
	if err != nil {
		slog.Error(
			"testselection.tests_failed",
			slog.String("package", directory.relative),
			slog.Any("error", err),
		)

		return "", fmt.Errorf("read tests in %s: %w", directory.relative, err)
	}

	names := make(map[string]bool)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}

		baseline, err := ordinary.MatchFile(directory.absolute, entry.Name())
		if err != nil {
			slog.Error(
				"testselection.match_failed",
				slog.String("file", entry.Name()),
				slog.Any("error", err),
			)

			return "", fmt.Errorf(
				"match ordinary test %s/%s: %w",
				directory.relative,
				entry.Name(),
				err,
			)
		}

		included, err := tagged.MatchFile(directory.absolute, entry.Name())
		if err != nil {
			slog.Error(
				"testselection.match_failed",
				slog.String("file", entry.Name()),
				slog.Any("error", err),
			)

			return "", fmt.Errorf(
				"match tagged test %s/%s: %w",
				directory.relative,
				entry.Name(),
				err,
			)
		}

		if !baseline && !included {
			continue
		}

		path := filepath.Join(directory.absolute, entry.Name())
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			slog.Error(
				"testselection.parse_failed",
				slog.String("path", path),
				slog.Any("error", err),
			)

			return "", fmt.Errorf("parse test source %s: %w", path, err)
		}

		if included && !baseline {
			addTests(file, names)
		}
	}

	selected := make([]string, 0, len(names))
	for name := range names {
		selected = append(selected, regexp.QuoteMeta(name))
	}

	if len(selected) == 0 {
		return "", nil
	}

	slices.Sort(selected)

	return "^(" + strings.Join(selected, "|") + ")$", nil
}

func addTests(file *ast.File, names map[string]bool) {
	for _, declaration := range file.Decls {
		function, valid := declaration.(*ast.FuncDecl)
		if !valid || function.Recv != nil {
			continue
		}

		name := function.Name.Name
		if name == "TestMain" || !strings.HasPrefix(name, "Test") {
			continue
		}

		suffix, _ := strings.CutPrefix(name, "Test")
		first, _ := utf8.DecodeRuneInString(suffix)
		if unicode.IsLower(first) {
			continue
		}

		names[name] = true
	}
}
