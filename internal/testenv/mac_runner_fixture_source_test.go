//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

func macOwnershipFixture(t *testing.T, mode string) (testenv.MacTestOptions, *client.Client, string) {
	t.Helper()
	options, docker, project := macDockerFixture(t)
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"go.mod", "go.sum"} {
		contents, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatal(err)
		}
		macWrite(t, options.Root, relative, string(contents))
	}
	for _, directory := range []string{"internal", "migrations"} {
		err = filepath.WalkDir(filepath.Join(root, directory), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "test" || entry.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			macWrite(t, options.Root, relative, string(contents))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for source, target := range map[string]string{
		"testdata/macrunnerfixture/ownership.go":      "internal/testenv/mac_runner_ownership_fixture.go",
		"testdata/macrunnerfixture/ownership_test.go": "internal/test/integration/example_test.go",
	} {
		contents, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		macWrite(t, options.Root, target, string(contents))
	}
	options.TestArguments = []string{"-run=^TestMacOwnership" + mode + "$", "-count=1", "-timeout=10m", "-tags=macrunnerfixture,runtimeaudit"}
	return options, docker, project
}
