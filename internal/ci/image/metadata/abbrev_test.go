package metadata_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// storeCommit encodes a root commit with the given message and the shared
// tree. It writes the commit to the repository only when store is true.
func storeCommit(t *testing.T, repository *git.Repository, tree plumbing.Hash, message string, store bool) plumbing.Hash {
	t.Helper()
	when := time.Unix(1_700_000_000, 0).UTC()
	commit := &object.Commit{Author: *testSignature(when), Committer: *testSignature(when), Message: message, TreeHash: tree}
	encoded := repository.Storer.NewEncodedObject()
	if err := commit.Encode(encoded); err != nil {
		t.Fatal(err)
	}
	if !store {
		return encoded.Hash()
	}
	hash, err := repository.Storer.SetEncodedObject(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func commonPrefixLength(left, right string) int {
	length := 0
	for length < len(left) && length < len(right) && left[length] == right[length] {
		length++
	}
	return length
}

func TestMetadataCommandExtendsAmbiguousAbbreviation(t *testing.T) {
	const prefixLength = 7
	repository, root := newRepository(t)
	first, err := repository.Head()
	if err != nil {
		t.Fatal(err)
	}
	firstCommit, err := repository.CommitObject(first.Hash())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	var left, right int
	for attempt := 0; ; attempt++ {
		hash := storeCommit(t, repository, firstCommit.TreeHash, "attempt "+strconv.Itoa(attempt), false)
		prefix := hash.String()[:prefixLength]
		if earlier, found := seen[prefix]; found {
			left, right = earlier, attempt
			break
		}
		seen[prefix] = attempt
	}
	leftHash := storeCommit(t, repository, firstCommit.TreeHash, "attempt "+strconv.Itoa(left), true)
	rightHash := storeCommit(t, repository, firstCommit.TreeHash, "attempt "+strconv.Itoa(right), true)
	if leftHash.String()[:prefixLength] != rightHash.String()[:prefixLength] {
		t.Fatal("synthetic commits do not share a prefix")
	}
	reference := plumbing.NewHashReference(first.Name(), rightHash)
	if err := repository.Storer.SetReference(reference); err != nil {
		t.Fatal(err)
	}
	short := shortCommit(t, root)
	if len(short) <= prefixLength || !strings.HasPrefix(rightHash.String(), short) || strings.HasPrefix(leftHash.String(), short) {
		t.Fatalf("short = %s, head = %s, other = %s", short, rightHash, leftHash)
	}
	if len(short) != commonPrefixLength(leftHash.String(), rightHash.String())+1 {
		t.Fatalf("short = %s is not the shortest unique prefix", short)
	}
}

// shortCommit runs the command and returns the short output.
func shortCommit(t *testing.T, root string) string {
	t.Helper()
	outputPath := filepath.Join(t.TempDir(), "output")
	if output, err := runMetadata(t, root, map[string]string{"GITHUB_OUTPUT": outputPath, branchNameKey: mainBranch}); err != nil {
		t.Fatalf("metadata failed: %v: %s", err, output)
	}
	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	return readOutputs(t, string(content))["short"]
}

func TestMetadataCommandCoreAbbreviation(t *testing.T) {
	for _, test := range []struct {
		name, global, local string
		length              int
	}{
		{"unset", "", "", 7},
		{"auto", "", "auto", 7},
		{"local", "", "12", 12},
		{"global", "10", "", 10},
		{"local overrides global", "10", "9", 9},
		{"smallest accepted", "", "4", 4},
		{"full", "", "no", 40},
		{"false", "", "false", 40},
		{"off", "", "off", 40},
		{"longer than hash", "", "50", 40},
		{"empty local overrides global", "10", emptySetting, 40},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, root := newRepository(t)
			configureAbbreviation(t, root, test.global, test.local)
			if short := shortCommit(t, root); len(short) != test.length {
				t.Fatalf("short = %s, want length %d", short, test.length)
			}
		})
	}
	for _, test := range []struct{ name, global, local, message string }{
		{"below minimum", "", "2", "read core.abbrev 2"},
		{"zero", "", "0", "read core.abbrev 0"},
		{"negative", "", "-1", "read core.abbrev -1"},
		{"not a number", "", "wide", "parse core.abbrev wide"},
		{"invalid global replaced by valid local", "2", "12", "read core.abbrev 2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, root := newRepository(t)
			configureAbbreviation(t, root, test.global, test.local)
			outputPath := filepath.Join(t.TempDir(), "output")
			expectFailure(t, root, map[string]string{"GITHUB_OUTPUT": outputPath, branchNameKey: mainBranch}, test.message)
		})
	}
}

// emptySetting requests a present core.abbrev key with an empty value.
const emptySetting = "<empty>"

func configureAbbreviation(t *testing.T, root, globalSetting, localSetting string) {
	t.Helper()
	if globalSetting != "" {
		path := filepath.Join(os.Getenv("HOME"), ".gitconfig")
		appendText(t, path, "[core]\n\tabbrev = "+globalSetting+"\n")
	}
	if localSetting == emptySetting {
		localSetting = " "
	}
	if localSetting != "" {
		appendText(t, filepath.Join(root, ".git", "config"), "[core]\n\tabbrev = "+localSetting+"\n")
	}
}
