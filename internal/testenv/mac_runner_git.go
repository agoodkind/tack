package testenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-git/go-git/v6"
)

func openMacRunnerRepository(root string) (*git.Repository, error) {
	repository, err := git.PlainOpen(root)
	if err != nil {
		return nil, macRunnerFailure("open Git checkout "+root, err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		_ = repository.Close()
		return nil, macRunnerFailure("read Git worktree "+root, err)
	}
	if filepath.Clean(worktree.Filesystem().Root()) != root {
		_ = repository.Close()
		return nil, fmt.Errorf("checkout %s must identify the Git repository root", root)
	}
	return repository, nil
}

func captureMacRunnerSource(root, evidence string) (macRunnerSource, error) {
	var source macRunnerSource
	repository, err := openMacRunnerRepository(root)
	if err != nil {
		return source, err
	}
	defer func() { _ = repository.Close() }()
	head, err := repository.Head()
	if err != nil {
		return source, macRunnerFailure("read Git HEAD in "+root, err)
	}
	if _, err := repository.CommitObject(head.Hash()); err != nil {
		return source, macRunnerFailure("read Git HEAD commit "+head.Hash().String(), err)
	}
	source.revision = head.Hash().String()
	index, err := repository.Storer.Index()
	if err != nil {
		return source, macRunnerFailure("read Git index in "+root, err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		return source, macRunnerFailure("read Git worktree in "+root, err)
	}
	if err := configureMacRunnerIgnores(repository, worktree, root); err != nil {
		return source, err
	}
	status, err := worktree.Status()
	if err != nil {
		return source, macRunnerFailure("read Git dirty status in "+root, err)
	}
	paths := make(map[string]bool)
	for _, entry := range index.Entries {
		paths[entry.Name] = true
	}
	statusPaths := make([]string, 0, len(status))
	for path := range status {
		paths[path] = true
		statusPaths = append(statusPaths, path)
	}
	slices.Sort(statusPaths)
	var dirty strings.Builder
	for _, path := range statusPaths {
		state := status[path]
		if state.Staging != git.Unmodified || state.Worktree != git.Unmodified {
			_, _ = fmt.Fprintf(&dirty, "%c%c %s\n", state.Staging, state.Worktree, path)
		}
	}
	source.dirty = dirty.String()
	for path := range paths {
		absolute := filepath.Join(root, filepath.FromSlash(path))
		if !macRunnerWithin(root, absolute) {
			return source, fmt.Errorf("source path escapes Git checkout: %s", path)
		}
		if !macRunnerWithin(evidence, absolute) {
			source.paths = append(source.paths, path)
		}
	}
	slices.Sort(source.paths)
	for _, path := range source.paths {
		if !macRunnerSelected(path) {
			continue
		}
		information, err := os.Stat(filepath.Join(root, filepath.FromSlash(path)))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return source, macRunnerFailure("inspect source path "+path, err)
		}
		if !information.Mode().IsRegular() {
			continue
		}
		digest, err := macRunnerFileHash(root, path)
		if err != nil {
			return source, err
		}
		source.manifest = append(source.manifest, macRunnerSourceHash{path: path, digest: digest})
	}
	return source, nil
}
