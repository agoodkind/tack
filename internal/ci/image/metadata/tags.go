package metadata

import (
	"os"
	"slices"
	"strings"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

const (
	lightweightTagPriority = 1
	annotatedTagPriority   = 2
)

// exactTag is a tag that resolves to the checked-out commit.
type exactTag struct {
	name     string
	priority int
	date     int64
}

// outranks compares exact tag candidates using the priority rules of
// git describe --tags.
//
// The caller visits tags in name order and retains the earlier candidate
// when this method returns false. The comparison uses these priorities:
//
//   - Annotated tags take priority over lightweight tags.
//   - Newer annotated tags take priority over older annotated tags.
//   - The first tag by name wins ties.
func (candidate exactTag) outranks(current exactTag) bool {
	if candidate.priority != current.priority {
		return candidate.priority > current.priority
	}

	return candidate.priority == annotatedTagPriority && candidate.date > current.date
}

func findExactTag(
	repository *git.Repository,
	root string,
	head plumbing.Hash,
) (string, bool, error) {
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

func classifyTag(
	repository *git.Repository,
	root string,
	reference *plumbing.Reference,
	head plumbing.Hash,
) (exactTag, bool, error) {
	var none exactTag

	name := reference.Name().Short()
	if reference.Hash() == head {
		return exactTag{name: name, priority: lightweightTagPriority, date: 0}, true, nil
	}

	target, err := repository.Object(plumbing.AnyObject, reference.Hash())
	if err != nil {
		return none, false, &os.PathError{Op: "read Git tag " + name, Path: root, Err: err}
	}

	outer, annotated := target.(*object.Tag)
	if !annotated {
		return none, false, nil
	}

	candidate := exactTag{name: name, priority: annotatedTagPriority, date: outer.Tagger.When.Unix()}
	for {
		next, err := outer.Object()
		if err != nil {
			return none, false, &os.PathError{Op: "read Git tag " + name + " target", Path: root, Err: err}
		}

		nested, isTag := next.(*object.Tag)
		if !isTag {
			return candidate, next.Type() == plumbing.CommitObject && next.ID() == head, nil
		}

		outer = nested
	}
}
