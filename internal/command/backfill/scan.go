package backfill

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"time"
)

const lifetimeTypeName = "Lifetime"

func scanTree(today time.Time) ([]string, error) {
	var problems []string
	err := filepath.WalkDir(
		".",
		func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			if entry.IsDir() {
				if path != "." && strings.HasPrefix(entry.Name(), ".") {
					return filepath.SkipDir
				}

				return nil
			}

			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			found, err := checkFile(path, today)
			if err != nil {
				return err
			}

			problems = append(problems, found...)

			return nil
		},
	)
	if err != nil {
		slog.Error("backfillcheck.scan_failed", slog.String("err", err.Error()))

		return nil, fmt.Errorf("scan the working directory: %w", err)
	}

	return problems, nil
}

func checkFile(path string, today time.Time) ([]string, error) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, nil, 0)
	if err != nil {
		slog.Error(
			"backfillcheck.parse_failed",
			slog.String("path", path),
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	var problems []string
	ast.Inspect(
		file,
		func(node ast.Node) bool {
			literal, ok := node.(*ast.CompositeLit)
			if !ok || typeName(literal.Type) != lifetimeTypeName {
				return true
			}

			if problem := checkLifetimeLiteral(literal, today); problem != "" {
				problems = append(
					problems,
					fmt.Sprintf("%s: %s", fileSet.Position(literal.Pos()), problem),
				)
			}

			return true
		},
	)

	return problems, nil
}
