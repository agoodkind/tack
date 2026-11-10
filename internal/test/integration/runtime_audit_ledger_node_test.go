package integration_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

const (
	// launcherImage runs the stand-in ledger node container.
	launcherImage = "alpine:3"
	// launcherConfigPath is the saved launcher config inside the container.
	launcherConfigPath = "home/yugabyte/var/conf/yugabyted.conf"
	// staleLauncherConfig is a saved launcher config with a stale master list.
	staleLauncherConfig = `{"current_masters":"yb-old:7100","join":"yb-old"}`
)

// nodePrepareEvidence is the node-prepare step evidence.
type nodePrepareEvidence struct {
	State   string `json:"state"`
	Before  string `json:"before"`
	After   string `json:"after"`
	Stopped bool   `json:"stopped"`
	DryRun  bool   `json:"dry_run"`
}

// TestRuntimeAuditLedgerNodePrepare starts a container under the ledger node
// name with a stale saved master list. The dry run of ops ledger
// node-prepare must record one completed read with a payload digest. The
// --execute run must record a step intent with the planned change, a
// completed step result with the stopped node, and a completed result, and
// the container must stop.
func TestRuntimeAuditLedgerNodePrepare(t *testing.T) {
	docker, err := client.New(client.WithHost(client.DefaultDockerHost))
	if err != nil {
		t.Fatalf("building the Docker client failed: %v", err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	startLauncherContainer(t, docker)
	pool := ledgerAdminPool(t)
	cfg := &config.Config{DatabaseURL: testenv.Ledger(t), LedgerNodeHosts: "yb-new=192.0.2.10"}

	reader := uuid.Must(uuid.NewV7())
	preview, err := runOpsCommand(t, cfg, pool, "--output", "json", "--operator-id", reader.String(),
		"--operator-email", outcomeOperatorEmail, "ops", "ledger", "node-prepare")
	if err != nil {
		t.Fatalf("the node-prepare dry run failed: %v\n%s", err, preview)
	}
	reads := multipartOutboxEvents(t, pool, audit.VerbOpsLedgerNodePrepare, reader)
	if len(reads) != 1 || reads[0].Outcome != audit.OutcomeCompleted || reads[0].Context.OperationClass != audit.OperationRead ||
		reads[0].Context.Read == nil || len(reads[0].Context.Read.PayloadSHA256) != 64 {
		t.Fatalf("the dry run recorded %+v instead of one completed read with a payload digest; the dry run printed:\n%s", reads, preview)
	}

	operator := uuid.Must(uuid.NewV7())
	if output, err := runOpsCommand(t, cfg, pool, "--execute", "--operator-id", operator.String(),
		"--operator-email", outcomeOperatorEmail, "ops", "ledger", "node-prepare"); err != nil {
		t.Fatalf("node-prepare --execute failed: %v\n%s", err, output)
	}
	events := outboxEventsByQuery(t, pool, "runtime_audit_verb_outbox.sql", string(audit.VerbOpsLedgerNodePrepare), operator.String())
	intents := eventsInPhase(events, audit.PhaseIntent, uuid.Nil)
	if len(intents) != 1 {
		t.Fatalf("node-prepare recorded %d intents instead of 1", len(intents))
	}
	operationID := intents[0].Context.OperationID
	planned := decodeNodePrepareStep(t, eventsInPhase(events, audit.PhaseIntent, operationID))
	if planned.State != "changed" || planned.Before != "yb-old:7100" || planned.After != "yb-new:7100" || planned.Stopped || planned.DryRun {
		t.Fatalf("the step intent recorded %+v instead of the planned change from yb-old:7100 to yb-new:7100", planned)
	}
	stepResults := eventsInPhase(events, audit.PhaseResult, operationID)
	observed := decodeNodePrepareStep(t, stepResults)
	if stepResults[0].Outcome != audit.OutcomeCompleted || observed.State != "changed" || !observed.Stopped {
		t.Fatalf("the step result is %s with %+v instead of a completed change that stopped the node", stepResults[0].Outcome, observed)
	}
	requireCompletedResult(t, events, operationID)
	inspected, err := docker.ContainerInspect(t.Context(), drillLedgerContainer, client.ContainerInspectOptions{Size: false})
	if err != nil || inspected.Container.State == nil || inspected.Container.State.Running {
		t.Fatalf("container %s is still running or could not be inspected after node-prepare (error %v)", drillLedgerContainer, err)
	}
}

// startLauncherContainer starts the stand-in ledger node container under the
// fixed ledger node name and removes it when the test ends.
func startLauncherContainer(t *testing.T, docker *client.Client) {
	t.Helper()
	pulled, err := docker.ImagePull(t.Context(), launcherImage, client.ImagePullOptions{})
	if err != nil {
		t.Fatalf("pulling %s failed: %v", launcherImage, err)
	}
	if err := pulled.Wait(t.Context()); err != nil {
		t.Fatalf("pulling %s failed: %v", launcherImage, err)
	}
	_ = pulled.Close()
	if _, err := docker.ContainerCreate(t.Context(), client.ContainerCreateOptions{
		Config: &container.Config{Image: launcherImage, Cmd: []string{"sleep", "300"}},
		Name:   drillLedgerContainer,
	}); err != nil {
		t.Fatalf("creating container %s failed: %v", drillLedgerContainer, err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := docker.ContainerRemove(cleanup, drillLedgerContainer, client.ContainerRemoveOptions{Force: true}); err != nil {
			t.Errorf("removing container %s failed: %v", drillLedgerContainer, err)
		}
	})
	if _, err := docker.CopyToContainer(t.Context(), drillLedgerContainer, client.CopyToContainerOptions{
		DestinationPath: "/", Content: launcherConfigArchive(t), AllowOverwriteDirWithFile: false, CopyUIDGID: true,
	}); err != nil {
		t.Fatalf("copying the saved launcher config into %s failed: %v", drillLedgerContainer, err)
	}
	if _, err := docker.ContainerStart(t.Context(), drillLedgerContainer, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("starting container %s failed: %v", drillLedgerContainer, err)
	}
}

// launcherConfigArchive returns a tar stream with the stale saved launcher
// config and its directories, owned by the yugabyte user.
func launcherConfigArchive(t *testing.T) *bytes.Buffer {
	t.Helper()
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for _, directory := range []string{"home/", "home/yugabyte/", "home/yugabyte/var/", "home/yugabyte/var/conf/"} {
		if err := writer.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: directory, Mode: 0o755, Uid: 1000, Gid: 1000}); err != nil {
			t.Fatalf("writing directory %s to the archive failed: %v", directory, err)
		}
	}
	header := &tar.Header{Typeflag: tar.TypeReg, Name: launcherConfigPath, Mode: 0o644, Uid: 1000, Gid: 1000, Size: int64(len(staleLauncherConfig))}
	if err := writer.WriteHeader(header); err != nil {
		t.Fatalf("writing the config header to the archive failed: %v", err)
	}
	if _, err := writer.Write([]byte(staleLauncherConfig)); err != nil {
		t.Fatalf("writing the config to the archive failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("closing the archive failed: %v", err)
	}
	return &archive
}

// multipartOutboxEvents returns the multipart events in public.ops_outbox
// with verb and actor. A read with targets saves a manifest record in
// public.ops_outbox and the event in public.ops_outbox_parts.
func multipartOutboxEvents(t *testing.T, pool *pgxpool.Pool, verb audit.Verb, actor uuid.UUID) []audit.Event {
	t.Helper()
	rows, err := pool.Query(t.Context(), readTestSQL(t, "runtime_audit_manifest_outbox.sql"))
	if err != nil {
		t.Fatalf("reading the outbox manifests failed: %v", err)
	}
	eventIDs, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		t.Fatalf("scanning the outbox manifests failed: %v", err)
	}
	var selected []audit.Event
	for _, eventID := range eventIDs {
		for _, event := range outboxEvents(t, yugabyteOutboxRecords(t, pool, eventID), eventID) {
			if event.Verb == string(verb) && event.Actor.ID == actor {
				selected = append(selected, event)
			}
		}
	}
	return selected
}

// decodeNodePrepareStep requires one step event and decodes its evidence.
func decodeNodePrepareStep(t *testing.T, steps []audit.Event) nodePrepareEvidence {
	t.Helper()
	if len(steps) != 1 {
		t.Fatalf("node-prepare recorded %d step events in one phase instead of 1", len(steps))
	}
	var evidence nodePrepareEvidence
	if err := json.Unmarshal(steps[0].Extra, &evidence); err != nil {
		t.Fatalf("decoding the step evidence of event %s failed: %v", steps[0].EventID, err)
	}
	return evidence
}
