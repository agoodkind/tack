package testenv

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/format/gitignore"
	"github.com/go-git/go-git/v6/storage/filesystem"
)

func configureMacRunnerIgnores(repository *git.Repository, worktree *git.Worktree, root string) error {
	storage, supported := repository.Storer.(*filesystem.Storage)
	if !supported {
		return errors.New("source reader requires native Git filesystem storage")
	}
	global, configured, err := macRunnerExcludesFile(repository, storage.Filesystem())
	if err != nil {
		return err
	}
	if !configured {
		// Git uses the XDG location on macOS rather than Library/Application Support.
		directory := os.Getenv("XDG_CONFIG_HOME")
		if directory == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return macRunnerFailure("resolve default Git ignore home", err)
			}
			directory = filepath.Join(home, ".config")
		}
		global = filepath.Join(directory, "git", "ignore")
	}
	patterns, err := macRunnerGlobalIgnores(root, global, configured)
	if err != nil {
		return err
	}
	common, err := readMacRunnerIgnores(storage.Filesystem(), "info/exclude", true)
	if err != nil {
		return err
	}
	patterns = append(patterns, common...)
	rootPatterns, err := gitignore.RootPatterns(worktree.Filesystem())
	if err != nil {
		return macRunnerFailure("read checkout Git ignore patterns", err)
	}
	patterns = append(patterns, rootPatterns...)
	worktree.Excludes = patterns
	return nil
}

func macRunnerGlobalIgnores(root, path string, configured bool) ([]gitignore.Pattern, error) {
	if path == "" {
		return nil, nil
	}
	tildePrefix := "~" + string(filepath.Separator)
	if strings.HasPrefix(path, tildePrefix) {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, macRunnerFailure("resolve configured Git ignore home", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, tildePrefix))
	}
	if strings.HasPrefix(path, "~") || strings.Contains(path, "%(prefix)") {
		return nil, fmt.Errorf("unsupported Git excludesFile path %s", path)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	return readMacRunnerIgnores(osfs.New(string(filepath.Separator)), path, !configured)
}

func readMacRunnerIgnores(filesystem billy.Filesystem, path string, optional bool) ([]gitignore.Pattern, error) {
	file, err := filesystem.Open(path)
	if optional && errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, macRunnerFailure("read Git ignore file "+path, err)
	}
	defer func() { _ = file.Close() }()
	var patterns []gitignore.Pattern
	lines := bufio.NewScanner(file)
	for lines.Scan() {
		line := lines.Text()
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "#") {
			patterns = append(patterns, gitignore.ParsePattern(line, nil))
		}
	}
	if err := lines.Err(); err != nil {
		return nil, macRunnerFailure("scan Git ignore file "+path, err)
	}
	return patterns, nil
}
