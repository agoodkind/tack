package metadata

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	format "github.com/go-git/go-git/v6/plumbing/format/config"
)

// errUnsupportedInclude reports configuration that could change core.abbrev
// through a file this command does not read.
var errUnsupportedInclude = errors.New("include and includeIf directives are not supported")

const (
	unsupportedIncludeOperation = "read Git configuration"
	gitDirectoryCondition       = "gitdir:"
	worktreeSuffix              = "/worktrees/*"
	containerWorkspace          = "/github/workspace"
	containerRunnerTemp         = "/github/runner_temp"
)

// checkoutCredentialFile matches the file name actions/checkout generates
// for its HTTP authentication header.
var checkoutCredentialFile = regexp.MustCompile(
	`^git-credentials-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.config$`,
)

// rejectUnsupportedIncludes fails when the configuration contains an
// include other than the credential references that actions/checkout
// writes for this checkout. Those references must share one generated file
// name and point at the runner temporary directory. This command never
// opens an included file.
func rejectUnsupportedIncludes(raw *format.Config, root string) error {
	for _, section := range raw.Sections {
		switch {
		case section.IsName("include"):
			if len(section.Options) > 0 || len(section.Subsections) > 0 {
				return unsupportedInclude(root)
			}
		case section.IsName("includeIf"):
			if len(section.Options) > 0 {
				return unsupportedInclude(root)
			}
		}
	}

	generatedFile := ""

	for _, subsection := range raw.Section("includeIf").Subsections {
		file, allowed := checkoutCredentialReference(subsection, root)
		if !allowed || (generatedFile != "" && file != generatedFile) {
			return unsupportedInclude(root)
		}

		generatedFile = file
	}

	return nil
}

func unsupportedInclude(root string) error {
	return &os.PathError{Op: unsupportedIncludeOperation, Path: root, Err: errUnsupportedInclude}
}

// checkoutCredentialReference returns the generated file name when the
// subsection is one of the includeIf entries actions/checkout writes for
// this checkout, on the host or in a container, for the checkout or its
// linked worktrees.
func checkoutCredentialReference(subsection *format.Subsection, root string) (string, bool) {
	paths := subsection.Options.GetAll("path")
	if len(subsection.Options) != 1 || len(paths) != 1 {
		return "", false
	}

	gitDirectory, found := strings.CutPrefix(subsection.Name, gitDirectoryCondition)
	if !found {
		return "", false
	}

	gitDirectory = strings.TrimSuffix(gitDirectory, worktreeSuffix)
	if gitDirectory == "" {
		return "", false
	}

	file := path.Base(paths[0])
	if !checkoutCredentialFile.MatchString(file) {
		return "", false
	}

	directory := path.Dir(paths[0])
	hostGitDirectory := filepath.ToSlash(filepath.Join(root, ".git"))
	if gitDirectory == hostGitDirectory && directory == hostRunnerTemp() {
		return file, true
	}

	return file, gitDirectory == containerGitDirectory(root) && directory == containerRunnerTemp
}

func hostRunnerTemp() string {
	temporary := os.Getenv("RUNNER_TEMP")
	if temporary == "" {
		return ""
	}

	return filepath.ToSlash(filepath.Clean(temporary))
}

func containerGitDirectory(root string) string {
	workspace := os.Getenv("GITHUB_WORKSPACE")
	if workspace == "" {
		return ""
	}

	relative, err := filepath.Rel(workspace, root)
	if err != nil {
		return ""
	}

	return path.Join(containerWorkspace, filepath.ToSlash(relative), ".git")
}
