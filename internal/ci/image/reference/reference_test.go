package reference_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/tack/internal/ci/image/imagetest"
)

func TestDigestCommand(t *testing.T) {
	valid := "sha256:" + strings.Repeat("a1", 32)

	for _, architecture := range []string{amd64Architecture, "arm64"} {
		t.Run(
			architecture,
			func(t *testing.T) {
				directory := filepath.Join(t.TempDir(), "new")

				err := runCommand(
					t,
					digestCommand,
					digestEnvironment(architecture, valid, directory),
				)
				if err != nil {
					t.Fatal(err)
				}

				content := readFile(t, filepath.Join(directory, architecture+".digest"))
				if content != valid+"\n" {
					t.Fatalf("digest = %q", content)
				}
			},
		)
	}

	for _, test := range []struct{ name, architecture, digest, directory string }{
		{"architecture", "386", valid, t.TempDir()},
		{"uppercase", amd64Architecture, "sha256:" + strings.Repeat("A", 64), t.TempDir()},
		{"short", amd64Architecture, "sha256:abc", t.TempDir()},
		{
			"filesystem",
			amd64Architecture,
			valid,
			filepath.Join(t.TempDir(), "missing", "..", "file"),
		},
	} {
		t.Run(
			test.name,
			func(t *testing.T) {
				directory := test.directory
				if test.name == "filesystem" {
					directory = filepath.Join(t.TempDir(), "file")
					writeFile(t, directory, fileContent)
				}

				err := runCommand(
					t,
					digestCommand,
					digestEnvironment(test.architecture, test.digest, directory),
				)
				if err == nil {
					t.Fatal("expected failure")
				}
			},
		)
	}

	err := runCommand(t, digestCommand, map[string]string{})
	if err == nil {
		t.Fatal("expected missing input failure")
	}
}

// TestTagsCommand checks tag file contents for releases and branches and
// checks failures for a filesystem error and a missing branch name.
func TestTagsCommand(t *testing.T) {
	for _, test := range []struct{ name, refType, refName, branch, expected string }{
		{
			tagRefType,
			tagRefType,
			"release/v1",
			"",
			registryPrefix + "abc\n" + registryPrefix + "abcdef\n" +
				registryPrefix + "release-v1\n" + registryPrefix + "latest\n",
		},
		{
			mainBranch,
			branchRefType,
			"",
			mainBranch,
			registryPrefix + "abc\n" + registryPrefix + "abcdef\n" +
				registryPrefix + "main\n" + registryPrefix + "latest\n",
		},
		{
			branchRefType,
			branchRefType,
			"",
			"feature/a",
			registryPrefix + "abc\n" + registryPrefix + "abcdef\n" + registryPrefix + "feature-a\n",
		},
		{
			"duplicate",
			branchRefType,
			"",
			shortCommit,
			registryPrefix + "abc\n" + registryPrefix + "abcdef\n" + registryPrefix + "abc\n",
		},
	} {
		t.Run(
			test.name,
			func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "tags")
				writeFile(t, path, strings.Repeat("old", 100))

				environment := tagEnvironment(test.refType, path)
				if test.refType == tagRefType {
					environment["GITHUB_REF_NAME"] = test.refName
				} else {
					environment["BRANCH_NAME"] = test.branch
				}

				err := runCommand(t, tagsCommand, environment)
				if err != nil {
					t.Fatal(err)
				}

				content := readFile(t, path)
				if content != test.expected {
					t.Fatalf("tags = %q", content)
				}
			},
		)
	}

	environment := tagEnvironment(branchRefType, t.TempDir())
	environment["BRANCH_NAME"] = mainBranch

	err := runCommand(t, tagsCommand, environment)
	if err == nil {
		t.Fatal("expected filesystem failure")
	}

	environment["IMAGE_TAG_FILE"] = filepath.Join(t.TempDir(), "tags")
	delete(environment, "BRANCH_NAME")

	command := imagetest.Prepare(t, t.TempDir(), tagsCommand)
	command.Env = environmentList(environment)

	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("expected missing input failure")
	}

	if !strings.Contains(string(output), "BRANCH_NAME: required environment variable is unset") {
		t.Fatalf("missing input error = %q", output)
	}
}

func TestDigestDirectoryWithSymlinkParent(t *testing.T) {
	root := t.TempDir()
	realDirectory := filepath.Join(root, "real")
	child := filepath.Join(realDirectory, "child")

	err := os.MkdirAll(child, 0o700)
	if err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(root, "link")

	err = os.Symlink(child, link)
	if err != nil {
		t.Fatal(err)
	}

	digest := "sha256:" + strings.Repeat("a", 64)

	err = runCommand(t, digestCommand, digestEnvironment(amd64Architecture, digest, link+"/.."))
	if err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(realDirectory, "amd64.digest"))
	if content != digest+"\n" {
		t.Fatalf("digest = %q", content)
	}

	_, err = os.Stat(filepath.Join(root, "amd64.digest"))
	if !os.IsNotExist(err) {
		t.Fatalf("unexpected digest at cleaned path: %v", err)
	}
}
