//go:build darwin && integration

package testenv_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/cache"
	"github.com/go-git/go-git/v6/storage/filesystem"
)

func TestMacRunnerSourceEvidence(t *testing.T) {
	options, repository := macFixture(t)
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	macWrite(t, options.Root, "staged.sql", "select 1;\n")
	if _, err := worktree.Add("staged.sql"); err != nil {
		t.Fatal(err)
	}
	macWrite(t, options.Root, "untracked.go", "package example\n")
	macWrite(t, options.Root, "ignored.go", "package ignored\n")
	macWrite(t, options.Root, "note.txt", "not a selected source file\n")
	if err := os.Remove(filepath.Join(options.Root, "internal/test/integration/example_test.go")); err != nil {
		t.Fatal(err)
	}
	macWrite(t, options.Root, "internal/test/integration/replacement_test.go", "package integration\n")
	options.EvidenceDirectory = filepath.Join(options.Root, "evidence")
	macPrepare(t, options)
	manifest := macRead(t, options.EvidenceDirectory, "source.sha256")
	digest := sha256.Sum256([]byte("package example\n"))
	if !strings.Contains(manifest, fmt.Sprintf("%x  untracked.go\n", digest)) || !strings.Contains(manifest, "  staged.sql\n") {
		t.Fatalf("manifest omitted source files: %s", manifest)
	}
	for _, excluded := range []string{"ignored.go", "example_test.go", "note.txt", "evidence/"} {
		if strings.Contains(manifest, excluded) {
			t.Fatalf("manifest included %s: %s", excluded, manifest)
		}
	}
	paths := strings.Split(strings.TrimSpace(macRead(t, options.EvidenceDirectory, "source-paths.stdout")), "\n")
	if !slices.IsSorted(paths) || !slices.Contains(paths, "internal/test/integration/example_test.go") {
		t.Fatalf("source paths must sort and retain deleted tracked files: %v", paths)
	}
	dirty := macRead(t, options.EvidenceDirectory, "dirty.stdout")
	if !strings.Contains(dirty, "A  staged.sql") || !strings.Contains(dirty, " D internal/test/integration/example_test.go") {
		t.Fatalf("dirty evidence omitted staged or deleted files: %s", dirty)
	}
}

func macRead(t *testing.T, root, path string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func TestMacRunnerLinkedWorktree(t *testing.T) {
	options, repository := macFixture(t)
	commonRoot := options.Root
	configuration, err := repository.Config()
	if err != nil {
		t.Fatal(err)
	}
	configuration.Extensions.WorktreeConfig = true
	if err := repository.SetConfig(configuration); err != nil {
		t.Fatal(err)
	}
	head, err := repository.Head()
	if err != nil {
		t.Fatal(err)
	}
	linked, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"go.mod", "docker-compose.test.yml", ".gitignore", "internal/test/integration/example_test.go"} {
		macWrite(t, linked, path, macRead(t, options.Root, path))
	}
	metadata := filepath.Join(options.Root, ".git/worktrees/linked")
	macWrite(t, metadata, "commondir", "../..\n")
	macWrite(t, metadata, "gitdir", filepath.Join(linked, ".git")+"\n")
	macWrite(t, metadata, "index", macRead(t, options.Root, ".git/index"))
	macWrite(t, linked, "worktree-ignore", "*.go\n")
	macWrite(t, linked, "local-ignored.go", "package ignored\n")
	macWrite(t, linked, "common-ignored.json", "{}\n")
	macWrite(t, linked, "common-keep.go", "package example\n")
	macWrite(t, linked, "root-keep.go", "package example\n")
	macWrite(t, linked, "nested/nested-keep.go", "package example\n")
	macWrite(t, linked, "nested/.gitignore", "!nested-keep.go\n")
	macWrite(t, linked, ".gitignore", macRead(t, linked, ".gitignore")+"!root-keep.go\n")
	macWrite(t, commonRoot, ".git/info/exclude", "common-ignored.json\n!common-keep.go\nroot-keep.go\nnested/nested-keep.go\n")
	macWrite(t, metadata, "config.worktree", "[core]\n\texcludesFile = "+filepath.Join(linked, "worktree-ignore")+"\n")
	macWrite(t, linked, ".git", "gitdir: "+metadata+"\n")
	for _, detached := range []bool{false, true} {
		t.Run(fmt.Sprintf("detached=%t", detached), func(t *testing.T) {
			if detached {
				macWrite(t, metadata, "HEAD", head.Hash().String()+"\n")
			} else {
				branch := plumbing.NewBranchReferenceName("linked")
				if err := repository.Storer.SetReference(plumbing.NewHashReference(branch, head.Hash())); err != nil {
					t.Fatal(err)
				}
				macWrite(t, metadata, "HEAD", "ref: "+branch.String()+"\n")
			}
			storage := filesystem.NewStorage(osfs.New(filepath.Join(commonRoot, ".git")), cache.NewObjectLRUDefault())
			if err := storage.PackRefs(); err != nil {
				t.Fatal(err)
			}
			options.Root = linked
			options.EvidenceDirectory = filepath.Join(t.TempDir(), "evidence")
			macPrepare(t, options)
			if revision := macRead(t, options.EvidenceDirectory, "revision.stdout"); revision != head.Hash().String()+"\n" {
				t.Fatalf("unexpected linked-worktree revision: %s", revision)
			}
			for _, artifact := range []string{"source-paths.stdout", "source.sha256"} {
				contents := macRead(t, options.EvidenceDirectory, artifact)
				for _, excluded := range []string{"local-ignored.go", "common-ignored.json"} {
					if strings.Contains(contents, excluded) {
						t.Fatalf("%s included ignored %s: %s", artifact, excluded, contents)
					}
				}
				for _, included := range []string{"example_test.go", "common-keep.go", "root-keep.go", "nested/nested-keep.go"} {
					if !strings.Contains(contents, included) {
						t.Fatalf("%s omitted tracked or unignored %s: %s", artifact, included, contents)
					}
				}
			}
		})
	}
}
