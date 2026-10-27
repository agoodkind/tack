package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/datagen"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/testenv"
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

// CursorSession decodes cursor with the configured key and loads its stored
// search session.
func CursorSession(t *testing.T, fixture QueryFixture, cursor string) searchdomain.Session {
	t.Helper()
	return cursorSession(t, fixture, cursor)
}

// NodeResolveRawKey returns the key of the resolution record of nodeID under
// the test prefix.
func NodeResolveRawKey(nodeID uuid.UUID) fdb.Key { return nodeResolveRawKey(nodeID) }

// ReplaceRawValue stores value under key and returns the previous value.
func ReplaceRawValue(t *testing.T, key fdb.Key, value []byte) []byte {
	t.Helper()
	return replaceRawValue(t, key, value)
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

// UndeployNativeModel lets the external test package undeploy the model and
// registers cleanup to redeploy the pinned model when the test ends.
func UndeployNativeModel(t *testing.T, adapter *search.Adapter, client *opensearchapi.Client, modelID string) {
	t.Helper()
	undeployNativeModel(t, adapter, client, modelID)
}

// NewSearchWorker builds the production search worker over the fixture's
// stores and adapter with pageBytes as the page bound.
func NewSearchWorker(t *testing.T, fixture QueryFixture, pageBytes int) *service.SearchWorker {
	t.Helper()
	return newSearchWorker(t, fixture.Stores, fixture.Adapter, clock.Wall{}, searchWorkerSettings(pageBytes))
}

// SearchPageSource aliases one indexed page document read from OpenSearch.
type SearchPageSource = searchPageSource

// SearchNodePages refreshes the fixture's serving index and returns the
// active page documents of nodeID in page order.
func SearchNodePages(t *testing.T, fixture QueryFixture, nodeID uuid.UUID) []SearchPageSource {
	t.Helper()
	return searchNodePages(t, fixture.Client, fixture.Index, nodeID, false)
}

// SearchVerifyReport aliases the exclusion and stuck work listing that ops
// search verify writes.
type SearchVerifyReport = searchVerifyReport

// RunSearchVerifyCommand runs ops search verify with cfg and returns its
// listing and the command error.
func RunSearchVerifyCommand(t *testing.T, cfg *config.Config) (SearchVerifyReport, error) {
	t.Helper()
	return runSearchVerifyCommand(t, cfg)
}

// DeployFixture aliases the disposable daemon and registry for deploy verify tests.
type DeployFixture = deployFixture

// StartContainerdDeployFixture starts a disposable Docker daemon with the
// containerd image store and a local registry. It sets DOCKER_HOST to that
// daemon.
func StartContainerdDeployFixture(t *testing.T) DeployFixture {
	t.Helper()
	fixture := startDeployFixture(t, testenv.ContainerdDocker)
	requireContainerdImageStore(t, fixture.cli)
	return fixture
}

// PushDeployIndexes pushes the fixture's tack-server and tack-audit-consumer
// image indexes to its registry and returns their digests in that order.
func PushDeployIndexes(t *testing.T, fixture DeployFixture) (string, string) {
	t.Helper()
	return fixture.pushIndexes(t)
}

// CreateDeployContainers creates app and audit-consumer containers from the
// fixture's tagged images with Docker Compose labels, without starting them.
func CreateDeployContainers(t *testing.T, fixture DeployFixture) {
	t.Helper()
	fixture.createContainers(t)
}

// AddConsumerReplica pushes a tack-audit-consumer image index under tag and
// creates an audit-consumer container with the supplied name and Docker Compose
// labels, without starting it.
func AddConsumerReplica(t *testing.T, fixture DeployFixture, name, tag string) {
	t.Helper()
	fixture.registry.pushMultiPlatformIndex(t, "tack-audit-consumer", tag, "consumer "+tag)
	createFromIndex(t, fixture.cli, name, "audit-consumer", fixture.registryHost+"/tack-audit-consumer:"+tag)
}

// RemoveDeployReplica removes the named container from the fixture daemon.
func RemoveDeployReplica(t *testing.T, fixture DeployFixture, name string) {
	t.Helper()
	removeDeployContainer(t, fixture.cli, name)
}

// ReplaceAppReplica pushes a tack-server image index under tag, replaces
// tack-app-1 with a container created from that index, and returns the index
// digest.
func ReplaceAppReplica(t *testing.T, fixture DeployFixture, tag string) string {
	t.Helper()
	digest := fixture.registry.pushMultiPlatformIndex(t, "tack-server", tag, "server "+tag)
	removeDeployContainer(t, fixture.cli, "tack-app-1")
	createFromIndex(t, fixture.cli, "tack-app-1", "app", fixture.registryHost+"/tack-server:"+tag)
	return digest
}

// VerifyDeployAs runs ops deploy verify as operatorID against the fixture with
// the supplied server and consumer index digests. It returns the command's
// output and error.
func VerifyDeployAs(t *testing.T, fixture DeployFixture, operatorID, serverDigest, consumerDigest string) (string, error) {
	t.Helper()
	cfg := &config.Config{DeployRegistry: fixture.registryHost, DeployImageTag: fixture.tag, DeployComposeProject: deployComposeProject}
	return runOpsDeployVerify(t, cfg, fixture.ledgerDSN, operatorID, serverDigest, consumerDigest)
}

// DeployTag returns the fixture's image tag.
func DeployTag(fixture DeployFixture) string { return fixture.tag }

// TryCall invokes one MCP tool through the harness's bearer token. It returns
// an error instead of failing the test, and it is safe to use from any
// goroutine.
func (h *MCPHarness) TryCall(ctx context.Context, toolName string, args datagen.ToolArguments) (datagen.Result, error) {
	result, err := h.driver.Call(ctx, h.token, toolName, args)
	if err != nil {
		return result, fmt.Errorf("call %s: %w", toolName, err)
	}
	return result, nil
}
