// Command backfillcheck rejects expired backfills during the build. It parses
// non-test Go files below the working directory, except hidden directories.
// The Makefile requires backfill-expiry before build (TACK-512).
//
// Each Lifetime literal must meet these conditions:
//
//   - Ticket must contain a nonempty string literal.
//   - RemoveBy must call [time.Date] with a literal year and day and a literal
//     month or a named month constant.
//   - The removal deadline must not precede the current UTC time.
package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const lifetimeTypeName = "Lifetime"

func main() {
	code := run(time.Now().UTC(), os.Stderr)
	if code != 0 {
		err := errors.New("a backfill declaration failed the check")
		slog.Error("backfillcheck.failed", slog.String("err", err.Error()), slog.Int("exit", code))
	}

	os.Exit(code)
}

// run scans the working directory and reports declaration failures.
//
// The return value identifies the check result:
//
//   - 0 indicates that every checked declaration passes.
//   - 1 indicates an expired or invalid declaration.
//   - 2 indicates a failure to scan or parse a file.
func run(today time.Time, out io.Writer) int {
	problems, err := scanTree(today)
	if err != nil {
		fmt.Fprintf(out, "backfillcheck: %v\n", err)
		return 2
	}

	for _, problem := range problems {
		fmt.Fprintln(out, problem)
	}

	if len(problems) > 0 {
		return 1
	}

	return 0
}

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

func typeName(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.SelectorExpr:
		return typed.Sel.Name
	case *ast.Ident:
		return typed.Name
	default:
		return ""
	}
}

func checkLifetimeLiteral(literal *ast.CompositeLit, today time.Time) string {
	fields := map[string]ast.Expr{}

	for _, element := range literal.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok {
			return "Lifetime must name its fields"
		}

		fields[typeName(pair.Key)] = pair.Value
	}

	ticket, ok := stringLiteral(fields["Ticket"])
	if !ok || ticket == "" {
		return "Lifetime.Ticket must be a string literal naming the ticket"
	}

	removeBy, ok := dateLiteral(fields["RemoveBy"])
	if !ok {
		return "Lifetime.RemoveBy must be a time.Date literal with a constant year, month, and day"
	}

	if today.After(removeBy) {
		return fmt.Sprintf(
			"backfill for %s was to be deleted by %s: delete the command and its tests",
			ticket,
			removeBy.Format(time.DateOnly),
		)
	}

	return ""
}

func stringLiteral(expr ast.Expr) (string, bool) {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}

	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		return "", false
	}

	return value, true
}

// dateLiteral reads a [time.Date] call with a literal year and day and a
// literal month or a named [time.Month] constant. It returns the removal
// deadline at 23:59:59 UTC on that calendar day.
func dateLiteral(expr ast.Expr) (time.Time, bool) {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) < 3 || typeName(call.Fun) != "Date" {
		return time.Time{}, false
	}

	year, ok := intLiteral(call.Args[0])
	if !ok {
		return time.Time{}, false
	}

	month, ok := monthLiteral(call.Args[1])
	if !ok {
		return time.Time{}, false
	}

	day, ok := intLiteral(call.Args[2])
	if !ok {
		return time.Time{}, false
	}

	return time.Date(year, month, day, 23, 59, 59, 0, time.UTC), true
}

func intLiteral(expr ast.Expr) (int, bool) {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.INT {
		return 0, false
	}

	value, err := strconv.Atoi(literal.Value)
	if err != nil {
		return 0, false
	}

	return value, true
}

func monthLiteral(expr ast.Expr) (time.Month, bool) {
	if value, ok := intLiteral(expr); ok {
		return time.Month(value), true
	}

	for month := time.January; month <= time.December; month++ {
		if typeName(expr) == month.String() {
			return month, true
		}
	}

	return 0, false
}
