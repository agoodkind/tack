package integration

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/datagen"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
)

// QueryFixture aliases the shared production graph and search test data.
type QueryFixture = queryFixture

// OpaqueKind aliases a generated node type with one included text property
// and one excluded text property.
type OpaqueKind = opaqueKind

// SearchPage aliases the parsed response from the public tack_search tool.
type SearchPage = searchPage

// ScaleQuery supplies the matching text for the duplicate page corpus.
const ScaleQuery = scaleQuery

// NativeSearchMemoryBytes sets the memory allocation for the shared
// OpenSearch test engine.
const NativeSearchMemoryBytes = nativeSearchMemoryBytes

// NewQueryFixture builds a production graph with real search and storage
// dependencies, bearer authentication, and identities in two organizations.
func NewQueryFixture(t *testing.T) QueryFixture {
	t.Helper()
	return newQueryFixture(t, defaultQueryOptions())
}

// EntryPoint finds the workspace entry node through organization metadata.
func EntryPoint(t *testing.T, fixture QueryFixture, workspace datagen.WorkspaceIdentity) uuid.UUID {
	t.Helper()
	return entryPoint(t, fixture, workspace)
}

// OtherOrganization returns a workspace from the fixture's second
// organization.
func OtherOrganization(t *testing.T, fixture QueryFixture) datagen.WorkspaceIdentity {
	t.Helper()
	return otherOrganization(t, fixture)
}

// PutOpaqueKind stores a generated node type in orgID with explicit text
// inclusion and exclusion.
func PutOpaqueKind(t *testing.T, fixture QueryFixture, orgID uuid.UUID) OpaqueKind {
	t.Helper()
	return putOpaqueKind(t, fixture, orgID)
}

// PutOpaqueNode creates a node under parentID with the supplied name and
// included and excluded text values.
func PutOpaqueNode(t *testing.T, fixture QueryFixture, kind OpaqueKind, parentID uuid.UUID, name, included, excluded string) uuid.UUID {
	t.Helper()
	return putOpaqueNode(t, fixture, kind, parentID, name, included, excluded)
}

// DrainSearchWork runs production worker slices until the worker cannot
// claim more work.
func DrainSearchWork(t *testing.T, worker *service.SearchWorker, maxSlices int) {
	t.Helper()
	drainSearchWork(t, worker, maxSlices)
}

// CallerAccess compiles the access filter for the workspace's first actor
// at entryID.
func CallerAccess(t *testing.T, fixture QueryFixture, workspace datagen.WorkspaceIdentity, entryID uuid.UUID) searchdomain.AccessFilter {
	t.Helper()
	return callerAccess(t, fixture, workspace, entryID)
}

// PutDuplicatePages writes count additional page documents for nodeID with
// the supplied access filter.
func PutDuplicatePages(t *testing.T, fixture QueryFixture, kind OpaqueKind, nodeID uuid.UUID, filter searchdomain.AccessFilter, count int) {
	t.Helper()
	putDuplicatePages(t, fixture, kind, nodeID, filter, count)
}

// TrySearch calls tack_search with query and cursor and converts an error
// response into a Go error.
func TrySearch(harness *MCPHarness, query, cursor string) (SearchPage, error) {
	return trySearch(harness, query, cursor)
}

// TryTypedSearch calls tack_search with a node_type filter and converts an
// error response into a Go error.
func TryTypedSearch(harness *MCPHarness, query, nodeType, cursor string) (SearchPage, error) {
	return tryTypedSearch(harness, query, nodeType, cursor)
}

// CallSearch calls tack_search with query and cursor and fails the test if
// the call returns an error.
func CallSearch(t *testing.T, harness *MCPHarness, query, cursor string) SearchPage {
	t.Helper()
	return callSearch(t, harness, query, cursor)
}

// CallEverySearchPage follows continuation cursors until search completes
// and returns the result IDs from every page.
func CallEverySearchPage(t *testing.T, query string, harness *MCPHarness) []uuid.UUID {
	t.Helper()
	return callEverySearchPage(t, query, harness).IDs
}

// RequireCorpusOnce checks that every expected ID occurs exactly once.
// It permits only entryID as an additional result, at most once.
func RequireCorpusOnce(t *testing.T, got, expected []uuid.UUID, entryID uuid.UUID) {
	t.Helper()
	requireCorpusOnce(t, got, expected, entryID)
}

// RequireExactlyOnce checks that every expected ID occurs exactly once and
// rejects every unexpected ID.
func RequireExactlyOnce(t *testing.T, got, expected []uuid.UUID) {
	t.Helper()
	requireExactlyOnce(t, got, expected)
}

// QueryConfig loads the server configuration for the shared query fixture.
func QueryConfig(t *testing.T) *config.Config {
	t.Helper()
	return queryConfig(t)
}

// ProcessHarness builds another production graph from cfg and returns an
// MCP harness that uses the original harness's bearer token.
func ProcessHarness(t *testing.T, harness *MCPHarness, cfg *config.Config) *MCPHarness {
	t.Helper()
	return processHarness(harness, buildQueryGraph(t, cfg))
}
