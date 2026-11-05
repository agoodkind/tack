package testenv

import (
	"errors"
	"os"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/x/plugin"
)

// macRunnerExcludesFile reads the raw option that the native typed merge omits.
func macRunnerExcludesFile(repository *git.Repository, filesystem billy.Filesystem) (string, bool, error) {
	loader, err := plugin.Get(plugin.ConfigLoader())
	if err != nil {
		return "", false, macRunnerFailure("load native Git configuration reader", err)
	}
	value, present := "", false
	for _, scope := range []config.Scope{config.SystemScope, config.GlobalScope} {
		storage, err := loader.Load(scope)
		if err != nil {
			return "", false, macRunnerFailure("read Git configuration scope", err)
		}
		configuration, err := storage.Config()
		if err != nil {
			return "", false, macRunnerFailure("decode Git configuration scope", err)
		}
		if configuration.Raw == nil {
			return "", false, errors.New("native Git scope reader omitted raw ignore configuration")
		}
		section := configuration.Raw.Section("core")
		if section.HasOption("excludesfile") {
			value, present = section.Option("excludesfile"), true
		}
	}
	configuration, err := repository.Config()
	if err != nil {
		return "", false, macRunnerFailure("read Git worktree configuration", err)
	}
	paths := []string{"config"}
	if configuration.Extensions.WorktreeConfig {
		paths = append(paths, "config.worktree")
	}
	for _, path := range paths {
		localValue, localPresent, err := readMacRunnerExcludesFile(filesystem, path)
		if err != nil {
			return "", false, err
		}
		if localPresent {
			value, present = localValue, true
		}
	}
	return value, present, nil
}

func readMacRunnerExcludesFile(filesystem billy.Filesystem, path string) (string, bool, error) {
	file, err := filesystem.Open(path)
	if path == "config.worktree" && errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, macRunnerFailure("read Git ignore configuration "+path, err)
	}
	defer func() { _ = file.Close() }()
	configuration, err := config.ReadConfig(file)
	if err != nil {
		return "", false, macRunnerFailure("decode Git ignore configuration "+path, err)
	}
	section := configuration.Raw.Section("core")
	return section.Option("excludesfile"), section.HasOption("excludesfile"), nil
}
