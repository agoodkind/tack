package main_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v6/plumbing"
)

func TestMetadataCommandTags(t *testing.T) {
	repository, root := newRepository(t)
	head, err := repository.Head()
	if err != nil {
		t.Fatal(err)
	}
	checkMetadata(t, root, head.Hash(), "feature/a", "feature/a")

	lightweightTag(t, repository, "light-b", head.Hash())
	lightweightTag(t, repository, "light-a", head.Hash())
	checkMetadata(t, root, head.Hash(), "feature/a", "light-a")

	now := time.Now()
	annotatedTag(t, repository, "annotated-early", head.Hash(), now.Add(-time.Hour))
	checkMetadata(t, root, head.Hash(), "feature/a", "annotated-early")

	annotatedTag(t, repository, "annotated-late", head.Hash(), now)
	checkMetadata(t, root, head.Hash(), "feature/a", "annotated-late")

	following := commitFile(t, repository, "second")
	checkMetadata(t, root, following, "feature/a", "feature/a")
}

func TestMetadataCommandLongTagChain(t *testing.T) {
	const chainLength = 40
	repository, root := newRepository(t)
	head, err := repository.Head()
	if err != nil {
		t.Fatal(err)
	}
	target := head.Hash()
	start := time.Now().Add(-time.Hour)
	var outermost string
	for i := range chainLength {
		outermost = "chain-" + strconv.Itoa(i)
		annotatedTag(t, repository, outermost, target, start.Add(time.Duration(i)*time.Second))
		reference, err := repository.Tag(outermost)
		if err != nil {
			t.Fatal(err)
		}
		target = reference.Hash()
	}
	checkMetadata(t, root, head.Hash(), "main", outermost)
}

func TestMetadataCommandRejectsDanglingTag(t *testing.T) {
	repository, root := newRepository(t)
	lightweightTag(t, repository, "dangling", plumbing.NewHash(strings.Repeat("ab", 20)))
	outputPath := filepath.Join(t.TempDir(), "output")
	expectFailure(t, root, map[string]string{"GITHUB_OUTPUT": outputPath, "BRANCH_NAME": "main"}, "read Git tag dangling")
}

func TestMetadataCommandCheckoutKinds(t *testing.T) {
	repository, root := newRepository(t)
	head, err := repository.Head()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("shallow", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(root, ".git", "shallow"), []byte(head.Hash().String()+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		checkMetadata(t, root, head.Hash(), "main", "main")
		if err := os.Remove(filepath.Join(root, ".git", "shallow")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("linked", func(t *testing.T) {
		linked := t.TempDir()
		administrative := filepath.Join(root, ".git", "worktrees", "linked")
		if err := os.MkdirAll(administrative, 0o700); err != nil {
			t.Fatal(err)
		}
		files := map[string]string{
			filepath.Join(administrative, "HEAD"):      head.Hash().String() + "\n",
			filepath.Join(administrative, "commondir"): "../..\n",
			filepath.Join(administrative, "gitdir"):    filepath.Join(linked, ".git") + "\n",
			filepath.Join(linked, ".git"):              "gitdir: " + administrative + "\n",
		}
		for path, content := range files {
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		lightweightTag(t, repository, "linked-tag", head.Hash())
		checkMetadata(t, linked, head.Hash(), "main", "linked-tag")
	})
}
