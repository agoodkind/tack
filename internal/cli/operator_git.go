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

// operatorNamespace defines the version 5 UUID namespace for operator emails.
// The same normalized email produces the same actor ID without a registry.
// Changing the namespace changes every derived operator ID. Keep it fixed for
// the lifetime of the deployment.
var operatorNamespace = uuid.MustParse("0a6f7572-cafe-dead-beef-000000000004")

type gitConfigKey string

const (
	gitConfigKeyName  gitConfigKey = "name"
	gitConfigKeyEmail gitConfigKey = "email"
)

// GitConfigOperatorSource resolves an operator from local Git configuration.
type GitConfigOperatorSource struct{}

// Resolve reads user.name and user.email from Git configuration files.
//
// The resolver reads files directly without invoking Git. It requires an email
// and derives a stable version 5 UUID from the lowercase email while preserving
// the email's original case in the principal.
//
//	source := GitConfigOperatorSource{}
//	principal, err := source.Resolve(ctx)
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

// readGitConfigIdentity reads every readable candidate in order and returns
// the resolved name, email, and number of files read. Later nonempty values
// replace earlier values. The home file therefore overrides values from the
// XDG file without discarding fields that the home file omits. Reading only
// the first file could attribute an action to the wrong operator.
//
// Path cleaning only normalizes each candidate. It does not restrict the file
// to a directory or prevent the operator from selecting another readable
// file through the environment. Do not rely on path cleaning for confinement.
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

// gitConfigCandidates selects global Git configuration paths after trimming
// surrounding whitespace from the environment values. A nonempty
// GIT_CONFIG_GLOBAL selects only that file. Otherwise, the candidates follow
// this order:
//
//  1. Use XDG_CONFIG_HOME/git/config when XDG_CONFIG_HOME is nonempty;
//     otherwise, use HOME/.config/git/config when HOME is nonempty.
//  2. Add HOME/.gitconfig when HOME is nonempty.
//
// The caller lets later nonempty fields override earlier fields. Do not also
// read HOME/.config/git/config when XDG_CONFIG_HOME selects another location;
// a stale fallback identity could replace the intended operator identity.
func gitConfigCandidates() ([]string, error) {
	if path := strings.TrimSpace(os.Getenv("GIT_CONFIG_GLOBAL")); path != "" {
		return []string{path}, nil
	}
	var candidates []string
	xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	home := strings.TrimSpace(os.Getenv("HOME"))
	if xdg != "" {
		candidates = append(candidates, filepath.Join(xdg, "git", "config"))
	} else if home != "" {
		candidates = append(candidates, filepath.Join(home, ".config", "git", "config"))
	}
	if home == "" {
		if len(candidates) == 0 {
			return nil, fmt.Errorf("HOME is not set and GIT_CONFIG_GLOBAL is empty")
		}
		return candidates, nil
	}
	candidates = append(candidates, filepath.Join(home, ".gitconfig"))
	return candidates, nil
}

// gitConfigValue trims surrounding whitespace and removes a trailing comment
// from an unquoted value. For a value that starts with a double quote, it
// returns the text between the opening and last closing double quotes, or
// removes the opening quote when a closing quote is absent. The email value
// determines the actor ID, and surrounding quotes must not change the identity.
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
