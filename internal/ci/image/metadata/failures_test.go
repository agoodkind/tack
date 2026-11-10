package metadata_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMetadataCommandFailures(t *testing.T) {
	_, root := newRepository(t)
	outputPath := filepath.Join(t.TempDir(), "output")
	t.Run(
		"missing output variable",
		func(t *testing.T) {
			expectFailure(
				t,
				root,
				map[string]string{branchNameKey: mainBranch},
				"GITHUB_OUTPUT: required environment variable is unset",
			)
		},
	)
	t.Run(
		"missing branch without tag",
		func(t *testing.T) {
			expectFailure(
				t,
				root,
				map[string]string{"GITHUB_OUTPUT": outputPath},
				"BRANCH_NAME: required environment variable is unset",
			)
		},
	)
	t.Run(
		"not a repository",
		func(t *testing.T) {
			expectFailure(
				t,
				t.TempDir(),
				map[string]string{"GITHUB_OUTPUT": outputPath, branchNameKey: mainBranch},
				"open Git checkout",
			)
		},
	)
	t.Run(
		"unwritable output",
		func(t *testing.T) {
			directoryPath := t.TempDir()
			output, err := runMetadata(
				t,
				root,
				map[string]string{"GITHUB_OUTPUT": directoryPath, branchNameKey: mainBranch},
			)
			if err == nil || !strings.Contains(output, "open metadata output file") ||
				!strings.Contains(output, directoryPath) {
				t.Fatalf("err = %v, output = %s", err, output)
			}
		},
	)
}

func TestMetadataCommandStopsWhenStandardOutputFails(t *testing.T) {
	_, root := newRepository(t)
	readOnly := filepath.Join(t.TempDir(), "stdout")
	if err := os.WriteFile(readOnly, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, err := os.Open(readOnly)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = stdout.Close() }()
	outputPath := filepath.Join(t.TempDir(), "output")
	command := metadataCommand(
		t,
		root,
		map[string]string{"GITHUB_OUTPUT": outputPath, branchNameKey: mainBranch},
	)
	command.Stdout = stdout
	var standardError strings.Builder
	command.Stderr = &standardError
	if err := command.Run(); err == nil ||
		!strings.Contains(standardError.String(), "write build metadata message") {
		t.Fatalf("err = %v, stderr = %s", err, standardError.String())
	}

	if _, statErr := os.Stat(outputPath); !os.IsNotExist(statErr) {
		t.Fatalf("output file written after stdout failure: %v", statErr)
	}
}

func TestMetadataCommandRejectsConfigurationIncludes(t *testing.T) {
	for _, test := range []struct{ name, section string }{
		{"include", "[include]\n\tpath = other\n"},
		{"conditional include", "[includeIf \"gitdir:/\"]\n\tpath = other\n"},
	} {
		for _, location := range []string{"local", "global"} {
			t.Run(
				test.name+" "+location,
				func(t *testing.T) {
					_, root := newRepository(t)
					path := filepath.Join(root, ".git", "config")
					if location == "global" {
						path = filepath.Join(os.Getenv("HOME"), ".gitconfig")
					}

					appendText(t, path, test.section)
					outputPath := filepath.Join(t.TempDir(), "output")
					expectFailure(
						t,
						root,
						map[string]string{"GITHUB_OUTPUT": outputPath, branchNameKey: mainBranch},
						"include and includeIf directives are not supported",
					)
				},
			)
		}
	}
}
