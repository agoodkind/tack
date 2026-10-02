package datagen

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/testenv"
)

const (
	// searchRefusalSeed is the fixed seed of TestRefusedRejectsNonAuthorizationToolError.
	// The integration package uses 538_000_001 to 538_000_003.
	searchRefusalSeed = 538_000_004
	// malformedSearchCursor fails cursor decoding in tack_search before the
	// search service runs.
	malformedSearchCursor = "not-a-search-cursor"
	// invalidCursorProblem is the tack_search problem text for a cursor that
	// does not decode.
	invalidCursorProblem = "cursor is invalid"
	// uncontactedSearchEndpoint configures public search. No engine listens
	// here. The malformed cursor fails before any engine request.
	uncontactedSearchEndpoint = "https://[::1]:9"
	// memberEntryArgument is the tack_search entry argument that the seeded
	// workspace metadata advertises to a member.
	memberEntryArgument = "workspace_reference"
)

// TestRefusedRejectsNonAuthorizationToolError runs refused against the real
// MCP handler with public search enabled. A member's malformed cursor returns
// the tool error "cursor is invalid", and refused must fail on this tool
// error. An unknown bearer token and a caller with no organization have
// their own cases below.
func TestRefusedRejectsNonAuthorizationToolError(t *testing.T) {
	ctx := t.Context()
	t.Setenv("DATABASE_URL", testenv.Ledger(t))
	t.Setenv("FDB_CLUSTER_FILE", testenv.FoundationDB(t))
	configureUncontactedSearch(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.AuditKafkaBrokers, cfg.AuditWriterDSN, cfg.AuditAllowUnrecorded = "", "", true
	graph, err := runtime.BuildGraph(ctx, cfg)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}
	t.Cleanup(graph.Close)
	scale, err := ParseScale(searchVerificationScale)
	if err != nil {
		t.Fatalf("parse scale: %v", err)
	}
	identities, err := BootstrapIdentities(ctx, cfg, searchRefusalSeed, scale)
	if err != nil {
		t.Fatalf("bootstrap identities: %v", err)
	}
	workspace := identities.Workspaces[0]
	member := workspace.Actors[0].Token
	run := searchRun{
		driver: NewDriver(graph, false, searchRefusalSeed), token: member, entry: workspace.Slug,
		limits: searchLimits{MaxResults: 0, MaxResponseBytes: 0}, fixture: searchFixture{}, phrases: searchPhrases{}, pages: graph,
	}
	_, callErr := callSearch(ctx, run.driver, member, workspace.Slug, accessPhrase, malformedSearchCursor)
	var toolError *toolCallError
	if !errors.As(callErr, &toolError) || !strings.Contains(toolError.message, invalidCursorProblem) {
		t.Fatalf("member search with a malformed cursor = %v, want the tool error %q", callErr, invalidCursorProblem)
	}
	if err := run.refused(ctx, member, workspace.Slug, malformedSearchCursor); err == nil {
		t.Fatalf("refused accepted the tool error %q as an authorization refusal", invalidCursorProblem)
	}
	requireUnknownBearerRejected(t, run, identities.BogusToken, workspace.Slug)
	requireNoOrganizationRefusal(t, cfg, run, workspace)
}

// requireUnknownBearerRejected requires HTTP 401 from tools/list and from
// tack_search for an unknown bearer token. isAuthorizationRefusal accepts the
// tack_search 401. refused fails for this token because it requires a
// successful tools/list read before the tack_search call.
func requireUnknownBearerRejected(t *testing.T, run searchRun, token, entry string) {
	t.Helper()
	ctx := t.Context()
	_, listErr := run.driver.searchEntryArgument(ctx, token)
	if !isAuthenticationRejection(listErr) {
		t.Fatalf("tools/list with an unknown bearer token = %v, want HTTP 401", listErr)
	}
	_, searchErr := callSearch(ctx, run.driver, token, entry, accessPhrase, "")
	if !isAuthenticationRejection(searchErr) || !isAuthorizationRefusal(searchErr, entry) {
		t.Fatalf("tack_search with an unknown bearer token = %v, want an accepted HTTP 401", searchErr)
	}
	if err := run.refused(ctx, token, entry, ""); err == nil {
		t.Fatal("refused passed for an unknown bearer token without a successful tools/list read")
	}
}

// requireNoOrganizationRefusal removes actor 1 from its only organization.
// The member schema requires workspace_reference; the schema of a caller with
// no organization requires _reference. refused must read each name from
// tools/list, and the caller with no organization must be refused.
func requireNoOrganizationRefusal(t *testing.T, cfg *config.Config, run searchRun, workspace WorkspaceIdentity) {
	t.Helper()
	ctx := t.Context()
	actor := workspace.Actors[1]
	if argument, err := run.driver.searchEntryArgument(ctx, actor.Token); err != nil || argument != memberEntryArgument {
		t.Fatalf("member tack_search entry argument = %q, %v, want %q", argument, err, memberEntryArgument)
	}
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, nil)
	if err != nil {
		t.Fatalf("open ledger pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.NewOrgMemberRepo(pool).RemoveMember(ctx, workspace.OrgID, actor.UserID); err != nil {
		t.Fatalf("remove member %s: %v", actor.UserID, err)
	}
	if argument, err := run.driver.searchEntryArgument(ctx, actor.Token); err != nil || argument != entryArgumentSuffix {
		t.Fatalf("no-organization tack_search entry argument = %q, %v, want %q", argument, err, entryArgumentSuffix)
	}
	if err := run.refused(ctx, actor.Token, workspace.Slug, ""); err != nil {
		t.Fatalf("refused for a caller with no organization = %v, want nil", err)
	}
}

// configureUncontactedSearch enables public search on uncontactedSearchEndpoint
// with a generated CA and cursor key.
func configureUncontactedSearch(t *testing.T) {
	t.Helper()
	caPath := filepath.Join(t.TempDir(), "opensearch-ca.pem")
	if err := os.WriteFile(caPath, generatedCA(t), 0o600); err != nil {
		t.Fatalf("write search CA: %v", err)
	}
	cursorKey := make([]byte, 32)
	if _, err := rand.Read(cursorKey); err != nil {
		t.Fatalf("generate cursor key: %v", err)
	}
	settings := map[string]string{
		"ENV": "production", "OPENSEARCH_ENDPOINT": uncontactedSearchEndpoint, "OPENSEARCH_CA": caPath,
		"OPENSEARCH_USERNAME": "tack", "OPENSEARCH_PASSWORD": "unused", "OPENSEARCH_PUBLIC_ENABLED": "true",
		"OPENSEARCH_CURSOR_KEY": base64.StdEncoding.EncodeToString(cursorKey), "OPENSEARCH_SHARDS": "1",
		"OPENSEARCH_ROUTING_SHARDS": "24", "OPENSEARCH_REPLICAS": "0",
		"AUTH_MEMBERSHIP_CACHE_LIFETIME": "0s", "AUTH_TOKEN_CACHE_LIFETIME": "0s",
	}
	for name, value := range settings {
		t.Setenv(name, value)
	}
}

// generatedCA returns one self-signed CA certificate in PEM form.
func generatedCA(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	now := clock.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "tack datagen test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	certificate, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate})
}
