package testselection_test

import (
	"bytes"
	"go/build"
	"strings"
	"testing"

	"goodkind.io/tack/internal/ci/testselection"
)

func TestRunRejectsInvalidInputsBeforeWritingSelections(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		arguments []string
		message   string
	}{
		{"missing tags", []string{"./sample"}, "require -tags"},
		{"missing patterns", []string{"-tags", "integration"}, "require at least one"},
		{
			"tag expression",
			[]string{"-tags", "integration || other", "./sample"},
			"invalid build tag",
		},
		{"empty tag", []string{"-tags", "integration,", "./sample"}, "invalid build tag"},
		{"unknown flag", []string{"-unknown", "./sample"}, "flag provided but not defined"},
		{
			"missing directory",
			[]string{"-tags", "integration", "./missing/..."},
			"read package pattern",
		},
		{"outside module", []string{"-tags", "integration", "./../outside"}, "inside the module"},
		{
			"invalid wildcard",
			[]string{"-tags", "integration", "./sample/*"},
			"invalid package pattern",
		},
		{
			"file path",
			[]string{"-tags", "integration", "./sample/tagged_test.go"},
			"does not identify a directory",
		},
		{
			"nested module",
			[]string{"-tags", "integration", "./sample/nested/child"},
			"nested module",
		},
	} {
		t.Run(
			scenario.name,
			func(t *testing.T) {
				root := moduleFixture(t)
				writeFixture(
					t,
					root,
					"sample/tagged_test.go",
					testSource("integration", "TestAdded"),
				)
				writeFixture(t, root, "sample/nested/go.mod", "module nested.example\n")
				writeFixture(
					t,
					root,
					"sample/nested/child/tagged_test.go",
					testSource("integration", "TestNested"),
				)
				var output bytes.Buffer
				err := testselection.Run(root, scenario.arguments, &output, build.Default)
				if err == nil || !strings.Contains(err.Error(), scenario.message) {
					t.Fatalf("selection error = %v, want %q", err, scenario.message)
				}

				if output.Len() != 0 {
					t.Fatalf("failed selection wrote records: %q", output.String())
				}
			},
		)
	}
}

func TestRunRejectsMalformedTestSourceWithoutPartialOutput(t *testing.T) {
	for _, tags := range []string{"", "//go:build integration\n\n"} {
		t.Run(
			tags,
			func(t *testing.T) {
				root := moduleFixture(t)
				writeFixture(
					t,
					root,
					"first/tagged_test.go",
					testSource("integration", "TestFirst"),
				)
				writeFixture(
					t,
					root,
					"second/broken_test.go",
					tags+"package broken\nfunc TestBroken( {",
				)
				var output bytes.Buffer
				err := testselection.Run(
					root,
					[]string{"-tags", "integration", "./first", "./second"},
					&output,
					build.Default,
				)
				if err == nil || !strings.Contains(err.Error(), "broken_test.go") {
					t.Fatalf("malformed source error = %v, want the broken file path", err)
				}

				if output.Len() != 0 {
					t.Fatalf("malformed source produced partial output: %q", output.String())
				}
			},
		)
	}
}
