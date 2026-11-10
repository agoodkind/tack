package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"

	"goodkind.io/tack/internal/test/integration"
)

// searchSettingsRuntimeRole copies the cluster and index permissions of the
// tack_runtime role in Configs tack/opensearch-roles.yml.j2, without the
// node statistics permission that only QA enables.
//
//go:embed testdata/search_settings_runtime_role.json
var searchSettingsRuntimeRole []byte

// searchSettingsMaintenanceRole copies the tack_maintenance role in Configs
// tack/opensearch-roles.yml.j2.
//
//go:embed testdata/search_settings_maintenance_role.json
var searchSettingsMaintenanceRole []byte

const (
	// searchSettingsRuntimeRoleName and searchSettingsMaintenanceRoleName
	// name the copies of the two Configs roles on the shared engine.
	searchSettingsRuntimeRoleName     = "tack_settings_test_runtime"
	searchSettingsMaintenanceRoleName = "tack_settings_test_maintenance"
	// searchSettingsMaintenanceUser is the internal user that the test maps
	// to both copied roles and to no other role.
	searchSettingsMaintenanceUser = "tack_settings_test_maintenance"
)

// useSearchSettingsMaintenanceIdentity creates the two copied Configs roles
// and an internal user mapped to exactly those roles through the security
// REST API of the shared engine. It then points the maintenance credential
// variables at that user and a password file, as production does. Cleanup
// deletes the user and both roles.
func useSearchSettingsMaintenanceIdentity(t *testing.T, admin *opensearchapi.Client) {
	t.Helper()
	putSecurityResource(t, admin, "roles/"+searchSettingsRuntimeRoleName, searchSettingsRuntimeRole)
	putSecurityResource(t, admin, "roles/"+searchSettingsMaintenanceRoleName, searchSettingsMaintenanceRole)
	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("generate the maintenance test password: %v", err)
	}
	password := "Tk-" + hex.EncodeToString(secret)
	user, err := json.Marshal(struct {
		Password string   `json:"password"`
		Roles    []string `json:"opendistro_security_roles"`
	}{Password: password, Roles: []string{searchSettingsRuntimeRoleName, searchSettingsMaintenanceRoleName}})
	if err != nil {
		t.Fatalf("encode the maintenance test user: %v", err)
	}
	putSecurityResource(t, admin, "internalusers/"+searchSettingsMaintenanceUser, user)
	t.Cleanup(func() {
		for _, resource := range []string{
			"internalusers/" + searchSettingsMaintenanceUser,
			"roles/" + searchSettingsRuntimeRoleName,
			"roles/" + searchSettingsMaintenanceRoleName,
		} {
			securityRequest(t, admin, http.MethodDelete, resource, nil)
		}
	})
	path := filepath.Join(t.TempDir(), "restricted-maintenance-password")
	if err := os.WriteFile(path, []byte(password), 0o600); err != nil {
		t.Fatalf("write the maintenance test password file: %v", err)
	}
	t.Setenv("OPENSEARCH_MAINTENANCE_USERNAME", searchSettingsMaintenanceUser)
	t.Setenv("OPENSEARCH_MAINTENANCE_PASSWORD", "")
	t.Setenv("OPENSEARCH_MAINTENANCE_PASSWORD_FILE", path)
}

// putSecurityResource writes one security plugin resource as the fixture
// administrator.
func putSecurityResource(t *testing.T, admin *opensearchapi.Client, resource string, body []byte) {
	t.Helper()
	securityRequest(t, admin, http.MethodPut, resource, body)
}

// securityRequest sends one request to the security REST API as the
// fixture administrator and fails the test unless OpenSearch returns HTTP
// 200 or 201.
func securityRequest(t *testing.T, admin *opensearchapi.Client, method, resource string, body []byte) {
	t.Helper()
	ctx := context.WithoutCancel(integration.WithTestOperation(t, t.Context()))
	request, err := http.NewRequestWithContext(ctx, method, "/_plugins/_security/api/"+resource, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build security request %s %s: %v", method, resource, err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := admin.Client.Request(request)
	if err != nil {
		t.Fatalf("security request %s %s: %v", method, resource, err)
	}
	defer func() { _ = response.Body.Close() }()
	reply, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read security response %s %s: %v", method, resource, err)
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		t.Fatalf("security request %s %s returned HTTP %d: %s", method, resource, response.StatusCode, reply)
	}
}
