package metadata_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"

	"goodkind.io/tack/internal/ci/image/imagetest"
)

const (
	branchNameKey = "BRANCH_NAME"
	mainBranch    = "main"
)

// TestMain builds the image command once and runs the package tests against it.
func TestMain(m *testing.M) {
	os.Exit(imagetest.RunTests(m))
}

var metadataTimePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)

func testSignature(when time.Time) *object.Signature {
	return &object.Signature{Name: "Test", Email: "test@example.invalid", When: when}
}

func commitFile(t *testing.T, repository *git.Repository, name string) plumbing.Hash {
	t.Helper()
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(worktree.Filesystem().Root(), name)
	if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := worktree.Add(name); err != nil {
		t.Fatal(err)
	}

	hash, err := worktree.Commit(name, &git.CommitOptions{Author: testSignature(time.Now())})
	if err != nil {
		t.Fatal(err)
	}

	return hash
}

func newRepository(t *testing.T) (*git.Repository, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	repository, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}

	commitFile(t, repository, "first")

	return repository, root
}

func lightweightTag(t *testing.T, repository *git.Repository, name string, target plumbing.Hash) {
	t.Helper()
	if err := repository.Storer.SetReference(
		plumbing.NewHashReference(plumbing.NewTagReferenceName(name), target),
	); err != nil {
		t.Fatal(err)
	}
}

func annotatedTag(
	t *testing.T,
	repository *git.Repository,
	name string,
	target plumbing.Hash,
	when time.Time,
) {
	t.Helper()
	options := &git.CreateTagOptions{Tagger: testSignature(when), Message: name}
	if _, err := repository.CreateTag(name, target, options); err != nil {
		t.Fatal(err)
	}
}

// runMetadata runs the built command with system Git configuration
// disabled and the test's temporary home directory.
func runMetadata(t *testing.T, directory string, environment map[string]string) (string, error) {
	t.Helper()

	command := metadataCommand(t, directory, environment)
	output, err := command.CombinedOutput()

	return string(output), err
}

func metadataBuild(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "./ci-image", "metadata")
}

func metadataCommand(t *testing.T, directory string, environment map[string]string) *exec.Cmd {
	t.Helper()
	command := imagetest.Prepare(t, directory, metadataBuild)
	command.Env = []string{
		"HOME=" + os.Getenv("HOME"),
		"XDG_CONFIG_HOME=" + os.Getenv("XDG_CONFIG_HOME"),
		"GIT_CONFIG_NOSYSTEM=1",
	}

	for key, value := range environment {
		command.Env = append(command.Env, key+"="+value)
	}

	return command
}

// expectFailure runs the command, requires a failure that mentions message, and
// requires that the command did not create GITHUB_OUTPUT.
func expectFailure(t *testing.T, directory string, environment map[string]string, message string) {
	t.Helper()
	output, err := runMetadata(t, directory, environment)
	if err == nil || !strings.Contains(output, message) {
		t.Fatalf("err = %v, output = %s", err, output)
	}

	outputPath, set := environment["GITHUB_OUTPUT"]
	if !set {
		return
	}

	if _, statErr := os.Stat(outputPath); !os.IsNotExist(statErr) {
		t.Fatalf("output file written on failure: %v", statErr)
	}
}

func appendText(t *testing.T, path, text string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := file.WriteString(text); err != nil {
		t.Fatal(err)
	}

	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func readOutputs(t *testing.T, content string) map[string]string {
	t.Helper()
	if !strings.HasSuffix(content, "\n") {
		t.Fatalf("output lacks final newline: %q", content)
	}

	outputs := map[string]string{}
	var order []string

	for _, line := range strings.Split(strings.TrimSuffix(content, "\n"), "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found {
			t.Fatalf("malformed line %q", line)
		}

		order = append(order, key)
		outputs[key] = value
	}

	if got := strings.Join(order, ","); got != "commit,short,build_time,tag" {
		t.Fatalf("line order = %s", got)
	}

	return outputs
}

func checkMetadata(t *testing.T, directory string, head plumbing.Hash, branch, wantTag string) {
	t.Helper()
	const earlier = "earlier=1\n"
	outputPath := filepath.Join(t.TempDir(), "output")
	if err := os.WriteFile(outputPath, []byte(earlier), 0o600); err != nil {
		t.Fatal(err)
	}

	if output, err := runMetadata(
		t,
		directory,
		map[string]string{"GITHUB_OUTPUT": outputPath, branchNameKey: branch},
	); err != nil {
		t.Fatalf("metadata failed: %v: %s", err, output)
	}

	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(string(content), earlier) {
		t.Fatalf("earlier content lost: %q", content)
	}

	outputs := readOutputs(t, strings.TrimPrefix(string(content), earlier))
	if outputs["commit"] != head.String() {
		t.Fatalf("commit = %s, want %s", outputs["commit"], head)
	}

	if len(outputs["short"]) < 7 || !strings.HasPrefix(head.String(), outputs["short"]) {
		t.Fatalf("short = %s", outputs["short"])
	}

	if !metadataTimePattern.MatchString(outputs["build_time"]) {
		t.Fatalf("build_time = %s", outputs["build_time"])
	}

	if outputs["tag"] != wantTag {
		t.Fatalf("tag = %s, want %s", outputs["tag"], wantTag)
	}
}
