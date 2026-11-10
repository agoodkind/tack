//go:build searchconversion

package integration_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

func TestServiceSecretOpenSearchConversion(t *testing.T) {
	if os.Getenv(testenv.SearchIntegrationVariable) != "1" {
		t.Fatal("TACK_SEARCH_INTEGRATION=1 is required for the native conversion acceptance")
	}
	directory := os.Getenv("TACK_TEST_SEARCH_CONVERSION_DIRECTORY")
	if directory == "" {
		t.Fatal("TACK_TEST_SEARCH_CONVERSION_DIRECTORY must identify a private synthetic conversion run")
	}
	verifyGeneratedSearchHashes(t, filepath.Join(directory, "desired-source"))
	conversion := testenv.OpenSearchForSecurityConversion(t, testenv.OpenSearchConversionOptions{
		SecurityDirectory: filepath.Join(directory, "legacy-source"), LegacyUsername: "tack_legacy",
		LegacyPasswordFile: filepath.Join(directory, "OPENSEARCH_LEGACY_PASSWORD"), AdministratorDirectory: directory,
	})
	engine := conversion.Fixture
	legacyPassword := readFixturePassword(t, directory, "OPENSEARCH_LEGACY_PASSWORD")
	verifyConversionLogin(t, engine, "tack_legacy", legacyPassword, http.StatusOK)
	verifyConversionIdentities(t, engine, directory, http.StatusUnauthorized)
	writeConversionEngine(t, directory, conversion)
	waitForConversionPhase(t, directory, "retain.complete")
	verifyConversionIdentities(t, engine, directory, http.StatusOK)
	verifyConversionLogin(t, engine, "tack_legacy", legacyPassword, http.StatusOK)
	writeConversionMarker(t, directory, "retire.ready")
	waitForConversionPhase(t, directory, "retire.complete")
	verifyConversionIdentities(t, engine, directory, http.StatusOK)
	verifyConversionLogin(t, engine, "tack_legacy", legacyPassword, http.StatusUnauthorized)
	t.Log("The native security conversion accepted new identities, retained the legacy login during compatibility, and rejected it after retirement.")
}

func verifyConversionIdentities(t *testing.T, engine testenv.OpenSearchFixture, directory string, status int) {
	t.Helper()
	for _, identity := range []struct{ username, passwordFile string }{
		{"tack_runtime", "OPENSEARCH_PASSWORD"},
		{"tack_maintenance", "OPENSEARCH_MAINTENANCE_PASSWORD"},
		{"tack_health", "OPENSEARCH_HEALTH_PASSWORD"},
	} {
		password := readFixturePassword(t, filepath.Join(directory, "desired-source"), identity.passwordFile)
		verifyConversionLogin(t, engine, identity.username, password, status)
	}
}

func verifyConversionLogin(t *testing.T, engine testenv.OpenSearchFixture, username, password string, expected int) {
	t.Helper()
	status := secretSearchRequest(t, engine, username, password, http.MethodGet, "/_cluster/health", nil)
	if status != expected {
		t.Fatalf("identity %s health returned %d, expected %d", username, status, expected)
	}
}
