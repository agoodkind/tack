package main_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var commandPath string

func TestMain(m *testing.M) {
	directory, err := os.MkdirTemp("", "ci-image-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	commandPath = filepath.Join(directory, "ci-image")
	build := exec.CommandContext(context.Background(), "go", "build", "-buildvcs=false", "-o", commandPath, ".")
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.RemoveAll(directory)
		os.Exit(1)
	}
	result := m.Run()
	if err := os.RemoveAll(directory); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(result)
}

func runCommand(t *testing.T, operation string, environment map[string]string) error {
	t.Helper()
	command := exec.CommandContext(t.Context(), commandPath, operation)
	command.Env = []string{}
	for key, value := range environment {
		command.Env = append(command.Env, key+"="+value)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Logf("command output: %s", output)
	}
	return err
}

func TestDigestCommand(t *testing.T) {
	valid := "sha256:" + strings.Repeat("a1", 32)
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "new")
			environment := map[string]string{"IMAGE_ARCH": architecture, "IMAGE_DIGEST": valid, "DIGEST_DIRECTORY": directory}
			if err := runCommand(t, "digest", environment); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, architecture+".digest")
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(content) != valid+"\n" {
				t.Fatalf("digest = %q", content)
			}
		})
	}
	for _, test := range []struct{ name, architecture, digest, directory string }{
		{"architecture", "386", valid, t.TempDir()},
		{"uppercase", "amd64", "sha256:" + strings.Repeat("A", 64), t.TempDir()},
		{"short", "amd64", "sha256:abc", t.TempDir()},
		{"filesystem", "amd64", valid, filepath.Join(t.TempDir(), "missing", "..", "file")},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := test.directory
			if test.name == "filesystem" {
				directory = filepath.Join(t.TempDir(), "file")
				if err := os.WriteFile(directory, []byte("file"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := runCommand(t, "digest", map[string]string{"IMAGE_ARCH": test.architecture, "IMAGE_DIGEST": test.digest, "DIGEST_DIRECTORY": directory}); err == nil {
				t.Fatal("expected failure")
			}
		})
	}
	if err := runCommand(t, "digest", map[string]string{}); err == nil {
		t.Fatal("expected missing input failure")
	}
}

func TestTagsCommand(t *testing.T) {
	for _, test := range []struct{ name, refType, refName, branch, expected string }{
		{"tag", "tag", "release/v1", "", "registry/owner/image:abc\nregistry/owner/image:abcdef\nregistry/owner/image:release-v1\nregistry/owner/image:latest\n"},
		{"main", "branch", "", "main", "registry/owner/image:abc\nregistry/owner/image:abcdef\nregistry/owner/image:main\nregistry/owner/image:latest\n"},
		{"branch", "branch", "", "feature/a", "registry/owner/image:abc\nregistry/owner/image:abcdef\nregistry/owner/image:feature-a\n"},
		{"duplicate", "branch", "", "abc", "registry/owner/image:abc\nregistry/owner/image:abcdef\nregistry/owner/image:abc\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tags")
			if err := os.WriteFile(path, []byte(strings.Repeat("old", 100)), 0o600); err != nil {
				t.Fatal(err)
			}
			environment := map[string]string{"REGISTRY": "registry", "IMAGE_OWNER": "owner", "IMAGE_NAME": "image", "COMMIT_SHORT": "abc", "GITHUB_SHA": "abcdef", "GITHUB_REF_TYPE": test.refType, "IMAGE_TAG_FILE": path}
			if test.refType == "tag" {
				environment["GITHUB_REF_NAME"] = test.refName
			} else {
				environment["BRANCH_NAME"] = test.branch
			}
			if err := runCommand(t, "tags", environment); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(content) != test.expected {
				t.Fatalf("tags = %q", content)
			}
		})
	}
	environment := map[string]string{"REGISTRY": "registry", "IMAGE_OWNER": "owner", "IMAGE_NAME": "image", "COMMIT_SHORT": "abc", "GITHUB_SHA": "abcdef", "GITHUB_REF_TYPE": "branch", "BRANCH_NAME": "main", "IMAGE_TAG_FILE": t.TempDir()}
	if err := runCommand(t, "tags", environment); err == nil {
		t.Fatal("expected filesystem failure")
	}
	delete(environment, "BRANCH_NAME")
	if err := runCommand(t, "tags", environment); err == nil {
		t.Fatal("expected missing input failure")
	}
}
