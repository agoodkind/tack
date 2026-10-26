package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/datagen"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// revocationCacheLifetime is the token and membership cache lifetime of
	// both processes. Search admission runs for a removed member only while
	// these caches still list the membership.
	revocationCacheLifetime = 20 * time.Second
	// revocationHold is how long the test search occupies the only search
	// slot of the second process. It is shorter than the default search
	// deadline and longer than two default admission waits.
	revocationHold = 12 * time.Second
	// revocationHoldTimeout bounds the wait for the test search's predict request.
	revocationHoldTimeout = 30 * time.Second
	// revocationRemovalPoll is the interval of the membership removal check.
	revocationRemovalPoll = 50 * time.Millisecond
	// revocationScale and revocationActor select the member that search-mixed
	// removes: the third actor of the first workspace at the medium scale.
	revocationScale = "medium"
	revocationActor = 2
	// revocationCursors is the size of the smallest revocation round: the replay and new search.
	revocationCursors = 2
	// sessionMismatchText is part of the tack_search error for a cursor of
	// another query.
	sessionMismatchText = "does not match this request"
)

type revocationOutput struct {
	Result struct {
		Revocation struct {
			Checks                    int  `json:"checks"`
			PagesWithinCacheLifetime  int  `json:"pages_within_cache_lifetime"`
			ResultsAfterCacheLifetime int  `json:"results_after_cache_lifetime"`
			Unavailable               int  `json:"unavailable"`
			Refused                   bool `json:"refused"`
		} `json:"revocation"`
	} `json:"result"`
}

// TestSearchMixedRevocationRetriesUnavailableSearch runs search-mixed against two processes
// with token and membership caches. The second process has one search slot, and its engine
// requests pass through predictHoldProxy. After search-mixed removes the member, the test starts
// its own search on the second process, and the proxy keeps that search's predict request open.
// While the test search occupies the slot, the removed member's searches on the second process
// return the unavailable text. The run must succeed and count those calls as unavailable. It must
// not return an issue after the cache lifetime, and it must end with a round of real refusals.
func TestSearchMixedRevocationRetriesUnavailableSearch(t *testing.T) {
	cfg := newCohortEngines(t)
	t.Setenv("OPENSEARCH_WORKER_CONCURRENCY", "1")
	t.Setenv("OPENSEARCH_WORKER_IDLE_INTERVAL", "1s")
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", "30s")
	cfg.AuthTokenCacheLifetime, cfg.AuthMembershipCacheLifetime = revocationCacheLifetime, revocationCacheLifetime
	seed := clock.Now().UnixNano()
	scale, err := datagen.ParseScale(revocationScale)
	if err != nil {
		t.Fatalf("parse scale: %v", err)
	}
	identities, err := datagen.BootstrapIdentities(integration.WithTestOperation(t, t.Context()), cfg, seed, scale)
	if err != nil {
		t.Fatalf("bootstrap the search-mixed identities: %v", err)
	}
	marker := "revocation hold " + strconv.FormatInt(seed, 10)
	endpoints, second, proxy := startRevocationProcesses(t, cfg, marker)
	watchContext, stopWatch := context.WithCancel(integration.WithTestOperation(t, t.Context()))
	watched := make(chan error, 1)
	go func() {
		watched <- occupySlotAfterRemoval(watchContext, cfg, proxy, datagen.NewDriver(second, false, seed), identities.Workspaces[0], marker)
	}()
	output, runErr := runSearchMixedCommand(t, cfg, "--seed", strconv.FormatInt(seed, 10), "--rate", "6", "--write-rate", "6",
		"--duration", "40s", "--concurrency", "2", "--burst-writes", "4", "--catch-up", "5m",
		"--endpoints", strings.Join(endpoints, ","), "--commit")
	stopWatch()
	if err := <-watched; err != nil {
		t.Fatalf("the test could not occupy the search slot during revocation: %v (output=%s)", err, output)
	}
	if runErr != nil || strings.Contains(string(output), sessionMismatchText) {
		t.Fatalf("search-mixed failed during revocation: %v (output=%s)", runErr, output)
	}
	var envelope revocationOutput
	if err := json.Unmarshal(output, &envelope); err != nil {
		t.Fatalf("decode the search-mixed result: %v (output=%s)", err, output)
	}
	revocation := envelope.Result.Revocation
	refusedCalls := revocation.Checks - revocation.PagesWithinCacheLifetime - revocation.Unavailable
	if revocation.Unavailable <= 0 || revocation.ResultsAfterCacheLifetime != 0 || !revocation.Refused || refusedCalls < revocationCursors {
		t.Fatalf("revocation must count unavailable calls, return no issue after the cache lifetime, and end with %d refused calls: %s",
			revocationCursors, output)
	}
}

// startRevocationProcesses serves MCP from two runtime graphs and runs the search workers in the
// first one. The second graph has one search slot and sends engine requests through a
// predictHoldProxy that keeps open only the predict request with marker. It returns the two base
// URLs, the second graph, and the proxy.
func startRevocationProcesses(t *testing.T, cfg *config.Config, marker string) ([]string, *appruntime.Graph, *predictHoldProxy) {
	t.Helper()
	first, err := appruntime.BuildGraph(integration.WithTestOperation(t, t.Context()), cfg)
	if err != nil {
		t.Fatalf("build the first runtime graph: %v", err)
	}
	t.Cleanup(first.Close)
	first.StartSearchWorkers(integration.WithTestOperation(t, t.Context()))
	engineCA, err := os.ReadFile(cfg.SearchCA)
	if err != nil {
		t.Fatalf("read the OpenSearch CA: %v", err)
	}
	proxy, endpoint, caPath := startPredictHoldProxy(t, cfg.SearchEndpoint, string(engineCA), marker)
	t.Setenv("OPENSEARCH_QUERY_CONCURRENCY", "1")
	proxied := *cfg
	proxied.SearchEndpoint, proxied.SearchCA = endpoint, caPath
	second, err := appruntime.BuildGraph(integration.WithTestOperation(t, t.Context()), &proxied)
	if err != nil {
		t.Fatalf("build the second runtime graph: %v", err)
	}
	t.Cleanup(second.Close)
	endpoints := make([]string, 0, 2)
	for _, graph := range []*appruntime.Graph{first, second} {
		server := httptest.NewServer(twoProcessHandler(graph))
		t.Cleanup(server.Close)
		endpoints = append(endpoints, server.URL)
	}
	return endpoints, second, proxy
}

// occupySlotAfterRemoval waits until the ledger no longer lists the revoked member in the
// workspace organization. It then arms the proxy and starts a search for marker through driver.
// After the proxy receives the predict request of that search, it waits revocationHold and
// releases the request.
func occupySlotAfterRemoval(ctx context.Context, cfg *config.Config, proxy *predictHoldProxy, driver *datagen.Driver, workspace datagen.WorkspaceIdentity, marker string) error {
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, nil)
	if err != nil {
		return errors.New("open the ledger pool: " + err.Error())
	}
	defer pool.Close()
	if err := awaitRemoval(ctx, postgres.NewOrgMemberRepo(pool), workspace.OrgID, workspace.Actors[revocationActor].UserID); err != nil {
		return err
	}
	proxy.arm()
	searched := make(chan error, 1)
	go func() {
		_, searchErr := driver.Call(ctx, workspace.Actors[0].Token, "tack_search", datagen.ToolArguments{
			WorkspaceReference: workspace.Slug, ProjectReference: "", IssueReference: "", Name: "", Properties: nil,
			NodeID: "", Query: marker, NodeType: "", Direction: "", SourceID: "", RelationType: "", TargetID: "", Cursor: "",
		})
		searched <- searchErr
	}()
	select {
	case <-proxy.held:
	case searchErr := <-searched:
		if searchErr != nil {
			return errors.New("the test search failed before its predict request arrived: " + searchErr.Error())
		}
		return errors.New("the test search returned a page before its predict request arrived")
	case <-time.After(revocationHoldTimeout):
		return errors.New("the proxy did not receive the predict request of the test search within " + revocationHoldTimeout.String())
	}
	<-time.After(revocationHold)
	proxy.release()
	<-searched
	return nil
}

// awaitRemoval polls the ledger until userID no longer belongs to orgID or ctx ends.
func awaitRemoval(ctx context.Context, members *postgres.OrgMemberRepo, orgID, userID uuid.UUID) error {
	for {
		orgIDs, err := members.ListOrgIDsForUser(ctx, userID)
		if err != nil {
			return errors.New("list the organizations of user " + userID.String() + ": " + err.Error())
		}
		if !slices.Contains(orgIDs, orgID) {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("search-mixed did not remove user " + userID.String() + " from organization " + orgID.String())
		case <-time.After(revocationRemovalPoll):
		}
	}
}
