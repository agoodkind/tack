package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// operatorNamespace seeds the version 5 UUID derived from an operator email,
// which is what gives the same person the same actor id with no registry to
// maintain. Changing it renames every operator in the ledger, so it is fixed
// for the life of the deployment.
var operatorNamespace = uuid.MustParse("0a6f7572-cafe-dead-beef-000000000004")

type gitConfigKey string

const (
	gitConfigKeyName  gitConfigKey = "name"
	gitConfigKeyEmail gitConfigKey = "email"
)

// GitConfigOperatorSource resolves an operator from the local gitconfig file.
type GitConfigOperatorSource struct{}

// Resolve reads user.name and user.email from gitconfig without invoking git.
func (GitConfigOperatorSource) Resolve(ctx context.Context) (audit.OperatorPrincipal, error) {
	candidates, err := gitConfigCandidates()
	if err != nil {
		slog.ErrorContext(ctx, "operator.git_config.path_failed", slog.String("err", err.Error()))
		return audit.OperatorPrincipal{}, err
	}
	name, email, read := readGitConfigIdentity(ctx, candidates)
	if read == 0 {
		err := fmt.Errorf("no readable gitconfig among %d candidate path(s)", len(candidates))
		slog.ErrorContext(ctx, "operator.git_config.read_failed", slog.String("err", err.Error()))
		return audit.OperatorPrincipal{}, err
	}
	if email == "" {
		err := errors.New("gitconfig has no user.email; set it or pass --operator-email")
		slog.ErrorContext(ctx, "operator.git_config.email_missing", slog.String("err", err.Error()))
		return audit.OperatorPrincipal{}, err
	}
	normalizedEmail := strings.ToLower(email)
	return audit.OperatorPrincipal{
		ID:     uuid.NewSHA1(operatorNamespace, []byte(normalizedEmail)),
		Email:  email,
		Name:   name,
		Source: "git",
	}, nil
}

// gitConfigPath returns the global config file, following git's own search
// order: the explicit override, then the XDG location, then the home file. An
// operator whose identity lives in the XDG file would otherwise look
// unconfigured and every command would refuse to run.
// readGitConfigIdentity reads every candidate that opens, in git's own order,
// and lets a later file override an earlier one. Git reads both the XDG file
// and the home file and takes the home file's value, so returning whichever
// opened first would attribute an action to the wrong person whenever an
// operator has an identity in each. It returns the resolved name and email
// plus how many files were actually read.
//
// Each candidate is cleaned before use, so a path assembled from the
// environment cannot climb out of the directory it names.
func readGitConfigIdentity(ctx context.Context, candidates []string) (string, string, int) {
	var name, email string
	read := 0
	for _, candidate := range candidates {
		cleaned := filepath.Clean(candidate)
		contents, err := os.ReadFile(cleaned)
		if err != nil {
			continue
		}
		read++
		fileName, fileEmail := parseGitConfig(contents)
		if fileName != "" {
			name = fileName
		}
		if fileEmail != "" {
			email = fileEmail
		}
		slog.DebugContext(ctx, "operator.git_config.read", slog.String("path", cleaned))
	}
	return name, email, read
}

// gitConfigCandidates lists the global config files to read, in git's own
// search order: an explicit override first, then the XDG location, then the
// two home-directory paths. Order matters to the caller, which lets a later
// file override an earlier one exactly as git does. An operator whose identity
// lives only in the XDG file would otherwise look unconfigured, and every
// command would refuse to run.
func gitConfigCandidates() ([]string, error) {
	if path := strings.TrimSpace(os.Getenv("GIT_CONFIG_GLOBAL")); path != "" {
		return []string{path}, nil
	}
	var candidates []string
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		candidates = append(candidates, filepath.Join(xdg, "git", "config"))
	}
	home := strings.TrimSpace(os.Getenv("HOME"))
	if home == "" {
		if len(candidates) == 0 {
			return nil, fmt.Errorf("HOME is not set and GIT_CONFIG_GLOBAL is empty")
		}
		return candidates, nil
	}
	candidates = append(candidates,
		filepath.Join(home, ".config", "git", "config"),
		filepath.Join(home, ".gitconfig"),
	)
	return candidates, nil
}

// gitConfigValue strips what git strips from a value: a trailing comment, then
// surrounding double quotes. Without this the same person yields a different
// actor id depending on whether they quoted their email, because the id is
// derived from the value.
func gitConfigValue(raw string) string {
	value := strings.TrimSpace(raw)
	if !strings.HasPrefix(value, "\"") {
		if index := strings.IndexAny(value, "#;"); index >= 0 {
			value = strings.TrimSpace(value[:index])
		}
		return value
	}
	if closing := strings.LastIndex(value, "\""); closing > 0 {
		return value[1:closing]
	}
	return strings.TrimPrefix(value, "\"")
}

func parseGitConfig(contents []byte) (string, string) {
	var name string
	var email string
	inUserSection := false
	for rawLine := range strings.SplitSeq(string(contents), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section := strings.TrimSpace(line[1 : len(line)-1])
			inUserSection = strings.EqualFold(section, "user")
			continue
		}
		if !inUserSection {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = gitConfigValue(value)
		switch gitConfigKey(key) {
		case gitConfigKeyName:
			name = value
		case gitConfigKeyEmail:
			email = value
		}
	}
	return name, email
}
