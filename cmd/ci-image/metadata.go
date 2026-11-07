package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

const (
	buildTimeLayout            = "2006-01-02T15:04:05Z"
	minimumShortCommitLength   = 7
	lightweightTagPriority     = 1
	annotatedTagPriority       = 2
	metadataOutputFilePermBits = 0o666
)

// exactTag is a tag that resolves to the checked-out commit.
type exactTag struct {
	name     string
	priority int
	date     int64
}

// outranks mirrors the selection in git describe: annotated tags beat lightweight
// tags, newer annotated tags beat older ones, and the first tag by name wins ties.
func (candidate exactTag) outranks(current exactTag) bool {
	if candidate.priority != current.priority {
		return candidate.priority > current.priority
	}
	return candidate.priority == annotatedTagPriority && candidate.date > current.date
}

func writeMetadata() error {
	outputPath, err := required("GITHUB_OUTPUT")
	if err != nil {
		return err
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return &os.PathError{Op: "read working directory", Path: ".", Err: err}
	}
	repository, err := git.PlainOpen(workingDirectory)
	if err != nil {
		return &os.PathError{Op: "open Git checkout", Path: workingDirectory, Err: err}
	}
	defer func() { _ = repository.Close() }()
	head, err := repository.Head()
	if err != nil {
		return &os.PathError{Op: "read Git HEAD", Path: workingDirectory, Err: err}
	}
	if _, err := repository.CommitObject(head.Hash()); err != nil {
		return &os.PathError{Op: "read Git HEAD commit " + head.Hash().String(), Path: workingDirectory, Err: err}
	}
	commit := head.Hash().String()
	shortCommit, err := abbreviateCommit(repository, workingDirectory, commit)
	if err != nil {
		return err
	}
	buildTime := time.Now().UTC().Format(buildTimeLayout)
	tag, found, err := findExactTag(repository, workingDirectory, head.Hash())
	if err != nil {
		return err
	}
	stopSignals := handleBrokenPipe()
	defer stopSignals()
	if found {
		err = printMetadataMessage("Build metadata uses tag %s\n", tag)
	} else {
		tag, err = required("BRANCH_NAME")
		if err != nil {
			return err
		}
		err = printMetadataMessage("HEAD does not have an exact tag. Build metadata uses branch %s\n", tag)
	}
	if err != nil {
		return err
	}
	content := "commit=" + commit + "\nshort=" + shortCommit + "\nbuild_time=" + buildTime + "\ntag=" + tag + "\n"
	return appendFile(outputPath, content)
}

func printMetadataMessage(format, tag string) error {
	if _, err := fmt.Printf(format, tag); err != nil {
		return &os.PathError{Op: "write build metadata message", Path: "stdout", Err: err}
	}
	return nil
}

func findExactTag(repository *git.Repository, root string, head plumbing.Hash) (string, bool, error) {
	references, err := repository.Tags()
	if err != nil {
		return "", false, &os.PathError{Op: "list Git tags", Path: root, Err: err}
	}
	var tags []*plumbing.Reference
	err = references.ForEach(func(reference *plumbing.Reference) error {
		tags = append(tags, reference)
		return nil
	})
	if err != nil {
		return "", false, &os.PathError{Op: "read Git tags", Path: root, Err: err}
	}
	slices.SortFunc(tags, func(left, right *plumbing.Reference) int {
		return strings.Compare(left.Name().String(), right.Name().String())
	})
	var best exactTag
	found := false
	for _, reference := range tags {
		candidate, matches, err := classifyTag(repository, root, reference, head)
		if err != nil {
			return "", false, err
		}
		if matches && (!found || candidate.outranks(best)) {
			best, found = candidate, true
		}
	}
	return best.name, found, nil
}

func classifyTag(repository *git.Repository, root string, reference *plumbing.Reference, head plumbing.Hash) (exactTag, bool, error) {
	name := reference.Name().Short()
	if reference.Hash() == head {
		return exactTag{name: name, priority: lightweightTagPriority}, true, nil
	}
	target, err := repository.Object(plumbing.AnyObject, reference.Hash())
	if err != nil {
		return exactTag{}, false, &os.PathError{Op: "read Git tag " + name, Path: root, Err: err}
	}
	outer, annotated := target.(*object.Tag)
	if !annotated {
		return exactTag{}, false, nil
	}
	candidate := exactTag{name: name, priority: annotatedTagPriority, date: outer.Tagger.When.Unix()}
	for {
		next, err := outer.Object()
		if err != nil {
			return exactTag{}, false, &os.PathError{Op: "read Git tag " + name + " target", Path: root, Err: err}
		}
		nested, isTag := next.(*object.Tag)
		if !isTag {
			return candidate, next.Type() == plumbing.CommitObject && next.ID() == head, nil
		}
		outer = nested
	}
}

func appendFile(path, content string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, metadataOutputFilePermBits)
	if err != nil {
		return &os.PathError{Op: "open metadata output file", Path: path, Err: err}
	}
	if _, err := file.WriteString(content); err != nil {
		writeErr := &os.PathError{Op: "write metadata output file", Path: path, Err: err}
		_ = file.Close()
		return writeErr
	}
	if err := file.Close(); err != nil {
		return &os.PathError{Op: "close metadata output file", Path: path, Err: err}
	}
	return nil
}
