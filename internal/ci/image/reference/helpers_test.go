package reference_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"

	"goodkind.io/tack/internal/ci/image/imagetest"
)

const (
	amd64Architecture = "amd64"
	imageArchKey      = "IMAGE_ARCH"
	tagRefType        = "tag"
	branchRefType     = "branch"
	mainBranch        = "main"
	shortCommit       = "abc"
	fileContent       = "file"
	registryPrefix    = "registry/owner/image:"
)

// TestMain builds the image command once and runs the package tests against it.
func TestMain(m *testing.M) {
	os.Exit(imagetest.RunTests(m))
}

func environmentList(environment map[string]string) []string {
	list := make([]string, 0, len(environment))
	for key, value := range environment {
		list = append(list, key+"="+value)
	}

	return list
}

func digestCommand(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "./ci-image", "digest")
}

func tagsCommand(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "./ci-image", "tags")
}

func runCommand(t *testing.T, build imagetest.Command, environment map[string]string) error {
	t.Helper()

	command := imagetest.Prepare(t, t.TempDir(), build)
	command.Env = environmentList(environment)

	output, err := command.CombinedOutput()
	if err != nil {
		t.Logf("command output: %s", output)

		return fmt.Errorf("run %s: %w", command.String(), err)
	}

	return nil
}

func tagEnvironment(refType, path string) map[string]string {
	return map[string]string{
		"REGISTRY":        "registry",
		"IMAGE_OWNER":     "owner",
		"IMAGE_NAME":      "image",
		"COMMIT_SHORT":    shortCommit,
		"GITHUB_SHA":      "abcdef",
		"GITHUB_REF_TYPE": refType,
		"IMAGE_TAG_FILE":  path,
	}
}

func digestEnvironment(architecture, digest, directory string) map[string]string {
	return map[string]string{
		imageArchKey:       architecture,
		"IMAGE_DIGEST":     digest,
		"DIGEST_DIRECTORY": directory,
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	err := os.WriteFile(path, []byte(content), 0o600)
	if err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(content)
}
