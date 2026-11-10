package integration_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

const (
	// archiveNodeContainer is the fixed yugabyte container name that
	// yb-archive-node inspects for the node name.
	archiveNodeContainer = drillLedgerContainer
	// archiveRunID is the export run that the seeded manifest describes.
	archiveRunID = "20261023T120000Z"
	// archiveSnapshotID is the snapshot that the seeded manifest names. The
	// test node does not contain it, and the archive tar fails.
	archiveSnapshotID = "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"
)

// archiveNodeObjects are the node artifacts in upload order.
var archiveNodeObjects = []string{"tablets.inventory", "tablets.tar.gz"}

// nodeArchiveStep is the step evidence that yb-archive-node records.
type nodeArchiveStep struct {
	Bucket     string   `json:"bucket"`
	RunID      string   `json:"run_id"`
	SnapshotID string   `json:"snapshot_id"`
	Node       string   `json:"node"`
	KeyPrefix  string   `json:"key_prefix"`
	Artifacts  []string `json:"artifacts"`
	Present    []string `json:"present"`
	State      string   `json:"state"`
}

// testArchiveNodeRuns starts a yugabyte node under the fixed container name
// and seeds an export manifest that lists that node. The manifest subtest
// requires the step intent to record the run, snapshot, prefix, and artifact
// keys of the manifest. The already_archived subtest seeds both node
// artifacts and requires the command to record the observed artifacts and
// to leave the seeded objects unchanged.
func testArchiveNodeRuns(t *testing.T) {
	testenv.StartEmptyLedger(t, archiveNodeContainer)
	pool := ledgerAdminPool(t)
	bucket := testenv.ObjectStore(t)
	cfg := backupBucketConfig(bucket)
	cfg.BackupRoot, cfg.BackupYBRocksDBDir = t.TempDir(), drillLedgerRocksDBDir
	runBucketsInit(t, cfg, pool)
	client := objectStoreClient(bucket)
	prefix := "yugabyte-snapshot/" + archiveRunID + "/nodes/" + archiveNodeContainer + "/"
	putArchiveObject(t, client, cfg, "yugabyte-snapshot/"+archiveRunID+"/manifest.json", archiveManifest(t))
	t.Run("manifest", func(t *testing.T) {
		events, operationID := runArchiveNode(t, cfg, pool, true)
		steps := eventsInPhase(events, audit.PhaseIntent, operationID)
		if len(steps) != 1 {
			t.Fatalf("yb-archive-node recorded %d step intents instead of 1", len(steps))
		}
		planned := decodeArchiveStep(t, steps[0])
		wantArtifacts := []string{prefix + archiveNodeObjects[0], prefix + archiveNodeObjects[1]}
		if planned.Bucket != cfg.BackupS3BucketMain || planned.RunID != archiveRunID || planned.SnapshotID != archiveSnapshotID ||
			planned.Node != archiveNodeContainer || planned.KeyPrefix != prefix || !slices.Equal(planned.Artifacts, wantArtifacts) {
			t.Fatalf("the step intent recorded %+v instead of the manifest run %s, snapshot %s, node %s, and prefix %s",
				planned, archiveRunID, archiveSnapshotID, archiveNodeContainer, prefix)
		}
		results := eventsInPhase(events, audit.PhaseResult, operationID)
		if len(results) != 1 || results[0].Outcome != audit.OutcomeFailed {
			t.Fatalf("yb-archive-node recorded %d step results; the test requires one failed step result", len(results))
		}
	})
	t.Run("already_archived", func(t *testing.T) {
		for _, object := range archiveNodeObjects {
			putArchiveObject(t, client, cfg, prefix+object, []byte("seeded "+object))
		}
		events, operationID := runArchiveNode(t, cfg, pool, false)
		results := eventsInPhase(events, audit.PhaseResult, operationID)
		if len(results) != 1 || results[0].Outcome != audit.OutcomeCompleted {
			t.Fatalf("yb-archive-node recorded %d step results; the test requires one completed step result", len(results))
		}
		observed := decodeArchiveStep(t, results[0])
		if observed.State != "already_archived" || observed.KeyPrefix != prefix || len(observed.Present) != len(archiveNodeObjects) ||
			!slices.Equal(observed.Present, observed.Artifacts) {
			t.Fatalf("the step result recorded %+v instead of every artifact under %s in the already_archived state", observed, prefix)
		}
		for _, object := range archiveNodeObjects {
			if body := getArchiveObject(t, client, cfg, prefix+object); string(body) != "seeded "+object {
				t.Fatalf("yb-archive-node replaced %s%s with %q", prefix, object, body)
			}
		}
		requireCompletedResult(t, events, operationID)
	})
}

// runArchiveNode runs yb-archive-node for the seeded run and returns the
// events and the operation ID of that run. failing reports whether the case
// requires an error.
func runArchiveNode(t *testing.T, cfg *config.Config, pool *pgxpool.Pool, failing bool) ([]audit.Event, uuid.UUID) {
	t.Helper()
	operatorID := uuid.Must(uuid.NewV7())
	output, err := runOpsCommand(t, cfg, pool, "--execute", "--operator-id", operatorID.String(),
		"--operator-email", outcomeOperatorEmail, "ops", "backup", "yb-archive-node", "--run-key", archiveRunID)
	if (err != nil) != failing {
		t.Fatalf("yb-archive-node returned error %v, and the case requires an error: %t\n%s", err, failing, output)
	}
	events := outboxEventsByQuery(t, pool, "runtime_audit_verb_outbox.sql", string(audit.VerbOpsBackupYBArchiveNode), operatorID.String())
	intents := eventsInPhase(events, audit.PhaseIntent, uuid.Nil)
	if len(intents) != 1 {
		t.Fatalf("yb-archive-node recorded %d intents instead of 1", len(intents))
	}
	return events, intents[0].Context.OperationID
}

// runBucketsInit creates the backup bucket of cfg.
func runBucketsInit(t *testing.T, cfg *config.Config, pool *pgxpool.Pool) {
	t.Helper()
	if output, err := runOpsCommand(t, cfg, pool, "--execute", "--operator-id", uuid.Must(uuid.NewV7()).String(),
		"--operator-email", outcomeOperatorEmail, "ops", "backup", "buckets-init"); err != nil {
		t.Fatalf("buckets-init failed: %v\n%s", err, output)
	}
}

// archiveManifest returns a manifest for archiveRunID that lists the node
// under the fixed container name.
func archiveManifest(t *testing.T) []byte {
	t.Helper()
	manifest := map[string]any{
		"run_id": archiveRunID, "snapshot_id": archiveSnapshotID, "database": "yugabyte",
		"created_at": "2026-10-23T12:00:00Z", "artifacts": []string{"metadata.snapshot", "schema.sql", "roles.sql"},
		"nodes": []map[string]string{{"name": archiveNodeContainer, "prefix": "nodes/" + archiveNodeContainer + "/"}},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encoding the export manifest failed: %v", err)
	}
	return encoded
}

// putArchiveObject writes body to key in the backup bucket of cfg.
func putArchiveObject(t *testing.T, client *s3.Client, cfg *config.Config, key string, body []byte) {
	t.Helper()
	if _, err := client.PutObject(t.Context(), &s3.PutObjectInput{
		Bucket: aws.String(cfg.BackupS3BucketMain), Key: aws.String(key), Body: bytes.NewReader(body),
	}); err != nil {
		t.Fatalf("writing object %s failed: %v", key, err)
	}
}

// getArchiveObject reads key from the backup bucket of cfg.
func getArchiveObject(t *testing.T, client *s3.Client, cfg *config.Config, key string) []byte {
	t.Helper()
	object, err := client.GetObject(t.Context(), &s3.GetObjectInput{Bucket: aws.String(cfg.BackupS3BucketMain), Key: aws.String(key)})
	if err != nil {
		t.Fatalf("reading object %s failed: %v", key, err)
	}
	defer func() { _ = object.Body.Close() }()
	var body bytes.Buffer
	if _, err := body.ReadFrom(object.Body); err != nil {
		t.Fatalf("reading the body of object %s failed: %v", key, err)
	}
	return body.Bytes()
}

// decodeArchiveStep decodes the archive step evidence of event.
func decodeArchiveStep(t *testing.T, event audit.Event) nodeArchiveStep {
	t.Helper()
	var evidence nodeArchiveStep
	if err := json.Unmarshal(event.Extra, &evidence); err != nil {
		t.Fatalf("decoding the archive step evidence of event %s failed: %v", event.EventID, err)
	}
	return evidence
}
