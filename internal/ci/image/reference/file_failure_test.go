package reference_test

import (
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/tack/internal/ci/image/imagetest"
)

// TestFileFailureReportsOnce checks that digest and tag file failures each
// produce one error log with the operation and path.
func TestFileFailureReportsOnce(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "file")
	writeFile(t, blocked, fileContent)

	digest := "sha256:" + strings.Repeat("a", 64)
	tagList := environmentList(tagEnvironment(branchRefType, root))
	tagList = append(tagList, "BRANCH_NAME="+mainBranch)

	for _, test := range []struct {
		name, path, context string
		newCommand          imagetest.Command
		environment         []string
	}{
		{
			"directory", blocked, "create digest directory",
			digestCommand,
			environmentList(digestEnvironment(amd64Architecture, digest, blocked)),
		},
		{
			"open", root, "open image file",
			tagsCommand,
			tagList,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := imagetest.Prepare(t, t.TempDir(), test.newCommand)
			command.Env = test.environment

			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatal("expected file failure")
			}

			message := string(output)
			singleBoundary := strings.Count(message, "ERROR ") == 1 &&
				strings.Contains(message, "ci-image.failed")

			if !singleBoundary || !strings.Contains(message, test.context) ||
				!strings.Contains(message, test.path) {
				t.Fatalf("unexpected error output: %s", message)
			}
		})
	}
}
