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

// configuredAbbreviation returns the minimum short ID length from core.abbrev.
//
// The function reads system, global, and local configuration in that order.
// Like git describe, it rejects an invalid core.abbrev entry before checking
// a later override. Otherwise, the last entry determines the result. This
// command parses numeric values as decimal integers with [strconv.Atoi]. Git's
// integer parser also accepts other bases and unit suffixes.
//
// An unset or auto value returns zero for automatic abbreviation. A positive
// result sets the minimum length, subject to the full hash length.
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

// The go-git parser stores an empty core.abbrev assignment and core.abbrev
// without "=" as an empty value. Both forms select the full hash here.
// Git rejects core.abbrev without "=" but accepts bare keys for boolean
// settings.
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

// abbreviateCommit returns the shortest unique commit prefix.
//
// The configured core.abbrev value sets the minimum length, up to the full
// hash length. An unset or auto value selects a minimum from the count of
// stored objects. Git estimates that count from pack indexes instead.
// The function extends the prefix for uniqueness and caps the result at
// the full hash length.
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
