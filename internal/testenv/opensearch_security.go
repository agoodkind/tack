package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/moby/moby/client"
)

// OpenSearchWithSecurityFiles starts an isolated engine with the supplied
// rendered security files and synthetic maintenance password.
func OpenSearchWithSecurityFiles(t T, directory string) OpenSearchFixture {
	t.Helper()
	skipWhenShort(t)
	skipWithoutSearchIntegration(t)
	if directory == "" {
		_, _ = fmt.Fprintln(t.Output(), "TACK_TEST_SEARCH_SECURITY_DIRECTORY must identify rendered synthetic security files")
		t.FailNow()
	}
	ctx, cancel := context.WithTimeout(t.Context(), provisionTimeout)
	defer cancel()
	fixture, _, err := provisionOpenSearch(ctx, openSearchEngineSettings{
		memoryBytes: openSearchMemoryBytes, dataVolume: "", nodeSettings: nil,
		securityDirectory: directory,
	})
	if err != nil {
		_, _ = fmt.Fprintf(t.Output(), "start security fixture: %v\n", err)
		t.FailNow()
	}
	return fixture
}

type openSearchSecurityLogin struct {
	username     string
	passwordPath string
}

func prepareOpenSearchSecurity(ctx context.Context, settings openSearchEngineSettings) (map[string][]byte, string, string, error) {
	if settings.securityDirectory != "" {
		return readOpenSearchSecurity(settings.securityDirectory, settings.securityLogin)
	}
	password, err := randomHex(ctx, 24)
	if err != nil {
		return nil, "", "", err
	}
	users, err := openSearchInternalUsers(ctx, openSearchAdminUser, password)
	if err != nil {
		return nil, "", "", err
	}
	return map[string][]byte{"internal_users.yml": users}, openSearchAdminUser, password, nil
}

func readOpenSearchSecurity(directory string, login *openSearchSecurityLogin) (map[string][]byte, string, string, error) {
	files := make(map[string][]byte)
	for _, name := range []string{"roles.yml", "roles_mapping.yml", "internal_users.yml"} {
		contents, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			wrapped := fmt.Errorf("read rendered search security %s: %w", name, err)
			slog.Error("testenv.search_security_failed", slog.String("err", wrapped.Error()))
			return nil, "", "", wrapped
		}
		files[name] = contents
	}
	if login == nil {
		login = &openSearchSecurityLogin{username: "tack_maintenance", passwordPath: filepath.Join(directory, "OPENSEARCH_MAINTENANCE_PASSWORD")}
	}
	password, err := os.ReadFile(login.passwordPath)
	if err != nil {
		wrapped := fmt.Errorf("read synthetic fixture password: %w", err)
		slog.Error("testenv.search_security_failed", slog.String("err", wrapped.Error()))
		return nil, "", "", wrapped
	}
	if strings.TrimSpace(string(password)) == "" {
		return nil, "", "", fmt.Errorf("the synthetic fixture password is empty")
	}
	return files, login.username, strings.TrimSpace(string(password)), nil
}

func installOpenSearchSecurity(ctx context.Context, docker *client.Client, name string, files map[string][]byte) error {
	for path, contents := range files {
		if err := writeContainerFileAs(ctx, docker, name, "/usr/share/opensearch/config/opensearch-security/"+path, contents, openSearchOwner); err != nil {
			return err
		}
	}
	return nil
}
