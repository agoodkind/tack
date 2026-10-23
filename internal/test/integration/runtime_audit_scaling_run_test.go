//go:build runtimeaudit

package integration_test

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/datagen"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/telemetry"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// scalingRun stores the app handlers, shared test state, and measurements for
// one scaling run. Its mutex protects request counts and latency samples during
// concurrent tool calls. leaseAcquisitions counts each time a relay of the run
// acquires the FoundationDB relay lease.
type scalingRun struct {
	server            *publicServer
	handlers          []http.Handler
	prefix            []byte
	crashAfter        int
	mu                sync.Mutex
	result            scalingResult
	leaseAcquisitions atomic.Int64
}

// runScalingWorkload runs the generated MCP workload in a subtest for the
// requested app instance count. It times the workload, measures audit recovery
// and storage bytes, and returns the results.
func runScalingWorkload(t *testing.T, label string, instances, crashAfter int) scalingResult {
	t.Helper()
	var result scalingResult
	t.Run(label+" instances", func(t *testing.T) {
		run := startScalingRun(t, instances)
		run.result.label, run.crashAfter = label, crashAfter
		started := clock.Now()
		run.execute(t)
		run.result.duration = clock.Since(started)
		run.measure(t)
		result = run.result
	})
	return result
}

// startScalingRun builds the requested number of runtime graphs on one
// FoundationDB prefix and one ledger, then creates test identities. It leaves
// audit relays and consumers stopped until measurement begins.
func startScalingRun(t *testing.T, instances int) *scalingRun {
	t.Helper()
	ctx := integration.WithTestOperation(t, t.Context())
	clusterFile := testenv.FoundationDB(t)
	identity := uuid.New()
	prefix := append([]byte("tack-test:"), identity[:]...)
	fdbadapter.SetTestPrefix(prefix)
	t.Cleanup(func() { clearReadPrefix(t, clusterFile, prefix) })
	t.Setenv("DATABASE_URL", testenv.Ledger(t))
	t.Setenv("FDB_CLUSTER_FILE", clusterFile)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Configuration loading failed; the scaling run requires valid configuration: %v", err)
	}
	cfg.AuditKafkaBrokers, cfg.AuditWriterDSN, cfg.AuditAllowUnrecorded = "", "", true
	handlers := make([]http.Handler, 0, instances)
	var first *appruntime.Graph
	for range instances {
		graph, err := appruntime.BuildGraph(ctx, cfg)
		if err != nil {
			t.Fatalf("Building a runtime graph failed; the scaling run requires a graph for each app instance: %v", err)
		}
		t.Cleanup(graph.Close)
		graph.StartDeleteResumer(ctx)
		graph.StartAuditReadRecovery(ctx)
		handlers = append(handlers, telemetry.RequestLogger(graph.Routes))
		if first == nil {
			first = graph
		}
	}
	scale, err := datagen.ParseScale("small")
	if err != nil {
		t.Fatalf("Parsing the small data generation scale failed; the scaling run requires that scale: %v", err)
	}
	identities, err := datagen.BootstrapIdentities(ctx, cfg, clock.Now().UnixNano(), scale)
	if err != nil {
		t.Fatalf("Creating test identities failed; the scaling workload requires an organization, workspace, and actor: %v", err)
	}
	workspace := identities.Workspaces[0]
	server := &publicServer{
		handler: handlers[0], config: cfg, graph: first, token: workspace.Actors[0].Token, userID: workspace.Actors[0].UserID,
		orgID: workspace.OrgID, workspace: workspace.Slug, clusterFile: clusterFile,
	}
	return &scalingRun{
		server: server, handlers: handlers, prefix: prefix, crashAfter: 0, mu: sync.Mutex{}, result: scalingResult{instances: instances},
		leaseAcquisitions: atomic.Int64{},
	}
}

// call selects an app handler by taking the index modulo the handler count and
// sends an MCP tool call. It records the request count and response latency,
// then returns the tool text or a response error.
func (run *scalingRun) call(t *testing.T, index int, tool string, arguments map[string]any) (string, error) {
	t.Helper()
	server := *run.server
	server.handler = run.handlers[index%len(run.handlers)]
	started := clock.Now()
	response := server.rpc(t, newRequestID(t, "scaling"), "tools/call", map[string]any{"name": tool, "arguments": arguments})
	latency := clock.Since(started)
	run.mu.Lock()
	run.result.latencies = append(run.result.latencies, latency)
	run.result.requests++
	run.mu.Unlock()
	return successfulToolText(response)
}

// mustCall returns the tool text from call and fails the test if call returns
// an error.
func (run *scalingRun) mustCall(t *testing.T, index int, tool string, arguments map[string]any) string {
	t.Helper()
	text, err := run.call(t, index, tool, arguments)
	if err != nil {
		t.Fatalf("The scaling workload received an error instead of a successful result from tool %s: %v", tool, err)
	}
	return text
}

// execute creates the projects, runs issue creation, read, and update jobs with
// a fixed number of concurrent clients, then lists issues in each project. It
// distributes the calls across the app handlers and fails the test if an
// operation fails.
func (run *scalingRun) execute(t *testing.T) {
	t.Helper()
	workspace := run.server.workspace
	projects := make([]string, scalingProjects)
	for index := range projects {
		identifier := fmt.Sprintf("SCALE%d", index)
		text := run.mustCall(t, index, "tack_create_project", map[string]any{
			"workspace_reference": workspace, "name": "Scaling " + identifier, "properties": map[string]any{"identifier": identifier},
		})
		projects[index] = rawID(t, text)
	}
	jobs := make(chan int)
	failures := make(chan error, scalingProjects*scalingIssuesPerProject)
	var workers sync.WaitGroup
	for range scalingClients {
		workers.Go(func() {
			for job := range jobs {
				if err := run.issueJob(t, job, projects[job%len(projects)]); err != nil {
					failures <- err
				}
			}
		})
	}
	for job := range scalingProjects * scalingIssuesPerProject {
		jobs <- job
	}
	close(jobs)
	workers.Wait()
	close(failures)
	for err := range failures {
		t.Fatalf("An issue job returned an error; the scaling workload requires every issue job to succeed: %v", err)
	}
	for index, project := range projects {
		run.mustCall(t, index, "tack_list_issues", map[string]any{"workspace_reference": workspace, "project_reference": project})
	}
}

// issueJob creates an issue in the supplied project, reads it, then updates its
// name. It selects handlers using successive indexes and returns the first tool
// or ID parsing error.
func (run *scalingRun) issueJob(t *testing.T, job int, project string) error {
	workspace := run.server.workspace
	created, err := run.call(t, job, "tack_create_issue", map[string]any{
		"workspace_reference": workspace, "project_reference": project, "name": fmt.Sprintf("Scaling issue %d", job),
	})
	if err != nil {
		return err
	}
	issue, err := rawIDOf(created)
	if err != nil {
		return err
	}
	if _, err := run.call(t, job+1, "tack_get_issue", map[string]any{"workspace_reference": workspace, "node_id": issue}); err != nil {
		return err
	}
	_, err = run.call(t, job+2, "tack_update_issue", map[string]any{
		"workspace_reference": workspace, "node_id": issue, "name": fmt.Sprintf("Scaling issue %d updated", job),
	})
	return err
}

// scalingDeadline returns the current time plus scalingDrainTimeout for an
// audit recovery wait.
func scalingDeadline() time.Time { return clock.Now().Add(scalingDrainTimeout) }
