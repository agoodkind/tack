package metadata

import (
	"errors"
	"math/bits"
	"os"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/x/plugin"
)

const (
	minimumConfiguredAbbreviation = 4
	automaticAbbreviation         = "auto"
)

var errAbbreviationBelowMinimum = errors.New("below the minimum of 4")

// configuredAbbreviation returns the minimum short ID length from
// core.abbrev. Git applies system, global and local configuration in that
// order and the last definition wins. Git rejects an invalid value even
// when a later value replaces it. The command validates every value the
// same way. The result is zero for an unset or "auto" value.
func configuredAbbreviation(repository *git.Repository, root string) (int, error) {
	source, err := plugin.Get(plugin.ConfigLoader())
	if err != nil {
		return 0, &os.PathError{Op: "load Git configuration reader", Path: root, Err: err}
	}

	length := 0

	for _, scope := range []config.Scope{config.SystemScope, config.GlobalScope, config.LocalScope} {
		var settings *config.Config
		if scope == config.LocalScope {
			settings, err = repository.Config()
		} else {
			var storer config.ConfigStorer
			storer, err = source.Load(scope)
			if err == nil {
				settings, err = storer.Config()
			}
		}

		if err != nil {
			return 0, &os.PathError{Op: "read Git configuration", Path: root, Err: err}
		}

		if settings.Raw == nil {
			continue
		}

		if err := rejectUnsupportedIncludes(settings.Raw, root); err != nil {
			return 0, err
		}

		for _, configured := range settings.Raw.Section("core").OptionAll("abbrev") {
			length, err = parseAbbreviation(configured, root)
			if err != nil {
				return 0, err
			}
		}
	}

	return length, nil
}

// The go-git parser stores an empty assignment and a key without "=" as an
// empty value. Both forms select the full hash here. Git rejects a key
// without "=".
func parseAbbreviation(value, root string) (int, error) {
	switch strings.ToLower(value) {
	case automaticAbbreviation:
		return 0, nil
	case "", "no", "false", "off":
		return len(plumbing.ZeroHash.String()), nil
	}

	length, err := strconv.Atoi(value)
	if err != nil {
		return 0, &os.PathError{Op: "parse core.abbrev " + value, Path: root, Err: err}
	}

	if length < minimumConfiguredAbbreviation {
		return 0, &os.PathError{
			Op:   "read core.abbrev " + value,
			Path: root,
			Err:  errAbbreviationBelowMinimum,
		}
	}

	return length, nil
}

// abbreviateCommit returns the shortest unique prefix that is at least as
// long as core.abbrev. Without core.abbrev, the minimum derives from the
// count of stored objects, where Git estimates that count from pack
// indexes.
func abbreviateCommit(repository *git.Repository, root, commit string) (string, error) {
	objects, err := repository.Storer.IterEncodedObjects(plumbing.AnyObject)
	if err != nil {
		return "", &os.PathError{Op: "list Git objects", Path: root, Err: err}
	}

	var hashes []string
	err = objects.ForEach(func(object plumbing.EncodedObject) error {
		hashes = append(hashes, object.Hash().String())
		return nil
	})
	if err != nil {
		return "", &os.PathError{Op: "read Git objects", Path: root, Err: err}
	}

	length, err := configuredAbbreviation(repository, root)
	if err != nil {
		return "", err
	}

	if length == 0 {
		length = max((bits.Len(uint(len(hashes)))+1)/2, minimumShortCommitLength)
	}

	for ; length < len(commit); length++ {
		prefix := commit[:length]
		matches := 0

		for _, hash := range hashes {
			if strings.HasPrefix(hash, prefix) {
				matches++
			}
		}

		if matches <= 1 {
			break
		}
	}

	return commit[:min(length, len(commit))], nil
}
