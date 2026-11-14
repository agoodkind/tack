// Package testselection selects tests that additional build tags include.
package testselection

import (
	"flag"
	"fmt"
	"go/build"
	"go/build/constraint"
	"io"
	"log/slog"
	"strings"
)

// Run prints sorted package paths and anchored test expressions as TSV.
//
// Run compares test file membership with and without -tags under buildContext.
// It writes output only after every package has passed source inspection.
//
//	err := testselection.Run(root, []string{"-tags", "integration", "./internal/audit/..."}, output, build.Default)
func Run(root string, arguments []string, output io.Writer, buildContext build.Context) error {
	flags := flag.NewFlagSet("testselection", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	requestedTags := flags.String("tags", "", "additional comma-separated build tags")
	if err := flags.Parse(arguments); err != nil {
		slog.Error("testselection.arguments_failed", slog.Any("error", err))

		return fmt.Errorf("parse test selection arguments: %w", err)
	}

	tags, err := parseTags(*requestedTags)
	if err != nil {
		return err
	}

	packages, err := packageDirectories(root, flags.Args())
	if err != nil {
		return err
	}

	ordinary := buildContext
	ordinary.BuildTags = nil
	tagged := buildContext
	tagged.BuildTags = tags
	var records strings.Builder

	for _, directory := range packages {
		expression, err := testExpression(directory, ordinary, tagged)
		if err != nil {
			return err
		}

		if expression != "" {
			_, _ = fmt.Fprintf(&records, "%s\t%s\n", directory.relative, expression)
		}
	}

	if _, err := io.WriteString(output, records.String()); err != nil {
		slog.Error("testselection.output_failed", slog.Any("error", err))

		return fmt.Errorf("write test selections: %w", err)
	}

	return nil
}

func parseTags(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, errTagsRequired
	}

	tags := strings.Split(value, ",")
	for index, tag := range tags {
		tag = strings.TrimSpace(tag)
		expression, err := constraint.Parse("//go:build " + tag)
		if err != nil {
			slog.Error("testselection.tag_failed", slog.String("tag", tag), slog.Any("error", err))

			return nil, fmt.Errorf("invalid build tag %q: %w", tag, err)
		}

		parsed, valid := expression.(*constraint.TagExpr)
		if !valid || parsed.Tag != tag {
			slog.Error(
				"testselection.tag_failed",
				slog.String("tag", tag),
				slog.Any("error", errTagName),
			)

			return nil, fmt.Errorf("invalid build tag %q: %w", tag, errTagName)
		}

		tags[index] = tag
	}

	return tags, nil
}
