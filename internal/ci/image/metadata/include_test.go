package metadata_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	firstGeneratedID  = "0b1c2d3e-4f50-4a61-8b72-93a4b5c6d7e8"
	secondGeneratedID = "1c2d3e4f-5061-4b72-8c83-a4b5c6d7e8f9"
)

func includeIfEntry(gitDirectory, includePath string) string {
	return "[includeIf \"gitdir:" + gitDirectory + "\"]\n\tpath = " + includePath + "\n"
}

func credentialFile(directory, identifier string) string {
	return directory + "/git-credentials-" + identifier + ".config"
}

// generatedIncludes returns the entries that actions/checkout adds for the
// host and container. Each set includes the checkout and its linked worktrees.
func generatedIncludes(root, runnerTemp, hostID, containerID string) string {
	hostGit := root + "/.git"
	containerGit := "/github/workspace/.git"
	hostFile := credentialFile(runnerTemp, hostID)
	containerFile := credentialFile("/github/runner_temp", containerID)
	return includeIfEntry(hostGit, hostFile) +
		includeIfEntry(hostGit+"/worktrees/*", hostFile) +
		includeIfEntry(containerGit, containerFile) +
		includeIfEntry(containerGit+"/worktrees/*", containerFile)
}

func runWithLocalConfiguration(t *testing.T, configuration func(root, runnerTemp string) string) (string, string, error) {
	t.Helper()
	_, created := newRepository(t)
	root, err := filepath.EvalSymlinks(created)
	if err != nil {
		t.Fatal(err)
	}
	runnerTemp := t.TempDir()
	appendText(t, filepath.Join(root, ".git", "config"), configuration(root, runnerTemp))
	outputPath := filepath.Join(t.TempDir(), "output")
	environment := map[string]string{
		"GITHUB_OUTPUT":    outputPath,
		branchNameKey:      mainBranch,
		"RUNNER_TEMP":      runnerTemp,
		"GITHUB_WORKSPACE": root,
	}
	output, runErr := runMetadata(t, root, environment)
	return outputPath, output, runErr
}

func TestMetadataCommandAcceptsCheckoutCredentialIncludes(t *testing.T) {
	outputPath, output, err := runWithLocalConfiguration(t, func(root, runnerTemp string) string {
		return "[core]\n\tabbrev = 9\n" + generatedIncludes(root, runnerTemp, firstGeneratedID, firstGeneratedID)
	})
	if err != nil {
		t.Fatalf("metadata failed: %v: %s", err, output)
	}
	content, readErr := os.ReadFile(outputPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if short := readOutputs(t, string(content))["short"]; len(short) != 9 {
		t.Fatalf("short = %s, want core.abbrev 9 applied", short)
	}
}

func TestMetadataCommandRejectsOtherIncludes(t *testing.T) {
	for _, test := range []struct {
		name          string
		configuration func(root, runnerTemp string) string
	}{
		{"ordinary include", func(_, runnerTemp string) string {
			return "[include]\n\tpath = " + credentialFile(runnerTemp, firstGeneratedID) + "\n"
		}},
		{"include with abbreviation", func(_, runnerTemp string) string {
			return includeIfEntry("/", runnerTemp+"/abbrev.config")
		}},
		{"outside runner temporary directory", func(root, _ string) string {
			return includeIfEntry(root+"/.git", credentialFile("/tmp", firstGeneratedID))
		}},
		{"file name suffix", func(root, runnerTemp string) string {
			return includeIfEntry(root+"/.git", credentialFile(runnerTemp, firstGeneratedID)+".bak")
		}},
		{"file name not an identifier", func(root, runnerTemp string) string {
			return includeIfEntry(root+"/.git", runnerTemp+"/git-credentials-token.config")
		}},
		{"identifier mismatch", func(root, runnerTemp string) string {
			return generatedIncludes(root, runnerTemp, firstGeneratedID, secondGeneratedID)
		}},
		{"other checkout", func(_, runnerTemp string) string {
			return includeIfEntry("/home/runner/work/other/other/.git", credentialFile(runnerTemp, firstGeneratedID))
		}},
		{"container path on host", func(root, _ string) string {
			return includeIfEntry(root+"/.git", credentialFile("/github/runner_temp", firstGeneratedID))
		}},
		{"extra option", func(root, runnerTemp string) string {
			return includeIfEntry(root+"/.git", credentialFile(runnerTemp, firstGeneratedID)) + "\tabbrev = 12\n"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			outputPath, output, err := runWithLocalConfiguration(t, test.configuration)
			if err == nil || !strings.Contains(output, "include and includeIf directives are not supported") {
				t.Fatalf("err = %v, output = %s", err, output)
			}
			if _, statErr := os.Stat(outputPath); !os.IsNotExist(statErr) {
				t.Fatalf("output file written on failure: %v", statErr)
			}
		})
	}
}
