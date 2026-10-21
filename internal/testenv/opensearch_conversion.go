package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"
)

// OpenSearchConversionAdministratorDN identifies the synthetic administrative certificate.
const OpenSearchConversionAdministratorDN = "CN=tack-testenv-security-admin"

// OpenSearchConversionOptions selects initial security files and private certificate outputs.
type OpenSearchConversionOptions struct {
	SecurityDirectory      string
	LegacyUsername         string
	LegacyPasswordFile     string
	AdministratorDirectory string
}

// OpenSearchConversionFixture supplies the engine and its exact container cleanup set.
type OpenSearchConversionFixture struct {
	Fixture    OpenSearchFixture
	Containers []string
}

// OpenSearchForSecurityConversion starts the existing engine fixture with a distinct administrator.
func OpenSearchForSecurityConversion(t T, options OpenSearchConversionOptions) OpenSearchConversionFixture {
	t.Helper()
	skipWhenShort(t)
	skipWithoutSearchIntegration(t)
	if options.SecurityDirectory == "" || options.LegacyUsername == "" || options.LegacyPasswordFile == "" || options.AdministratorDirectory == "" {
		_, _ = fmt.Fprintln(t.Output(), "security conversion requires initial files, legacy credentials, and an administrator output directory")
		t.FailNow()
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	fixture, containers, err := provisionOpenSearch(ctx, openSearchEngineSettings{
		memoryBytes: openSearchMemoryBytes, dataVolume: "",
		nodeSettings:           []string{"plugins.security.authcz.admin_dn=" + OpenSearchConversionAdministratorDN},
		securityDirectory:      options.SecurityDirectory,
		securityLogin:          &openSearchSecurityLogin{username: options.LegacyUsername, passwordPath: options.LegacyPasswordFile},
		administratorDirectory: options.AdministratorDirectory,
	})
	if err != nil {
		_, _ = fmt.Fprintf(t.Output(), "start conversion fixture: %v\n", err)
		t.FailNow()
	}
	return OpenSearchConversionFixture{Fixture: fixture, Containers: containers}
}

func openSearchCertificatesForSettings(ctx context.Context, address net.IP, administratorDirectory string) (openSearchCertificates, error) {
	if administratorDirectory == "" {
		return makeOpenSearchCertificates(ctx, address)
	}
	authority, err := newClusterAuthority(ctx)
	if err != nil {
		return openSearchCertificates{}, err
	}
	node, err := authority.issue(ctx, clusterNodeCommonName, []string{address.String(), "localhost", "127.0.0.1"})
	if err != nil {
		return openSearchCertificates{}, err
	}
	administrator, err := authority.issue(ctx, "tack-testenv-security-admin", nil)
	if err != nil {
		return openSearchCertificates{}, err
	}
	for name, contents := range map[string][]byte{"ca.crt": authority.pem, "admin.crt": administrator.certificate, "admin.key": administrator.key} {
		if err := os.WriteFile(filepath.Join(administratorDirectory, name), contents, 0o600); err != nil {
			return openSearchCertificates{}, certificateError(ctx, fmt.Errorf("write synthetic conversion certificate %s: %w", name, err))
		}
	}
	slog.DebugContext(ctx, "testenv.search_conversion.certificates_created", slog.String("directory", administratorDirectory))
	return openSearchCertificates{ca: authority.pem, node: node.certificate, key: node.key}, nil
}
