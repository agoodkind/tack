package ops_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/testenv"
)

const (
	ybSnapshotRunIDLayout    = "20060102T150405Z"
	ybSnapshotManifestObject = "manifest.json"
	ybSnapshotSchemaObject   = "schema.sql"
	ybNodeInventoryObject    = "tablets.inventory"
	retentionDeleteBatchSize = 1000
	backupOperationLockName  = ".backup-operation.lock"
)

type retentionCommandFixture struct {
	store   *retentionObjectStore
	config  *config.Config
	pool    *pgxpool.Pool
	service string
	cutoff  time.Time
}

func newRetentionCommandFixture(t *testing.T) *retentionCommandFixture {
	t.Helper()
	bucket := testenv.ObjectStore(t)
	cfg := &config.Config{
		BackupRoot: t.TempDir(), BackupS3Endpoint: bucket.Endpoint, BackupS3Region: bucket.Region,
		BackupS3BucketMain: bucket.Bucket, BackupS3AccessKey: bucket.AccessKey, BackupS3SecretKey: bucket.SecretKey,
	}
	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(bucket.Endpoint), Region: bucket.Region, UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(bucket.AccessKey, bucket.SecretKey, ""),
	})
	pool, err := pgxpool.New(t.Context(), testenv.Ledger(t))
	if err != nil {
		t.Fatalf("open retention audit ledger: %v", err)
	}
	t.Cleanup(pool.Close)
	now := time.Date(2026, time.October, 23, 12, 0, 0, 0, time.UTC)
	ops.SetBackupRetentionTestClock(t, now)
	return &retentionCommandFixture{
		store:  &retentionObjectStore{t: t, bucket: bucket, client: client, stopped: false},
		config: cfg, pool: pool, service: "retention-test-" + uuid.NewString(), cutoff: now.AddDate(0, 0, -14),
	}
}

func (fixture *retentionCommandFixture) run(t *testing.T, execute bool) (string, error) {
	return fixture.runContext(t, t.Context(), execute)
}

func (fixture *retentionCommandFixture) runContext(t *testing.T, ctx context.Context, execute bool) (string, error) {
	t.Helper()
	var output bytes.Buffer
	factory := cli.System(fixture.config)
	factory.Out = &output
	factory.SetAuditOutbox(audit.NewPoolOutbox(fixture.pool))
	factory.SetOperatorIdentitySource(cli.NewOperatorSource(factory))
	registry := clispec.NewRegistry()
	ops.RegisterCommands(registry, factory)
	root := &cobra.Command{Use: "tack", SilenceErrors: true, SilenceUsage: true}
	factory.RegisterGlobalFlags(root)
	for _, command := range clispec.RenderCobra(registry, factory) {
		root.AddCommand(command)
	}
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetContext(ctx)
	arguments := []string{"ops", "backup", "retention", "--operator-service", fixture.service}
	if execute {
		arguments = append(arguments, "--execute")
	}
	root.SetArgs(arguments)
	err := root.Execute()
	return output.String(), err
}

type retentionManifestNode struct {
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
}

type retentionManifest struct {
	RunID      string                  `json:"run_id"`
	SnapshotID string                  `json:"snapshot_id"`
	Database   string                  `json:"database"`
	Artifacts  []string                `json:"artifacts"`
	Nodes      []retentionManifestNode `json:"nodes"`
}

func (fixture *retentionCommandFixture) completeRun(t *testing.T, timestamp time.Time) string {
	t.Helper()
	run := timestamp.UTC().Format(ybSnapshotRunIDLayout)
	manifest := retentionManifest{
		RunID: run, SnapshotID: "retention-snapshot", Database: "tack", Artifacts: []string{"metadata.snapshot", "schema.sql", "roles.sql"},
		Nodes: []retentionManifestNode{{Name: "yb1", Prefix: "nodes/yb1/"}},
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range []string{"metadata.snapshot", "schema.sql", "roles.sql", "nodes/yb1/tablets.tar.gz", "nodes/yb1/tablets.inventory"} {
		fixture.store.put(ybRunArtifactKey(run, artifact), []byte("retention artifact"))
	}
	fixture.store.put(ybRunArtifactKey(run, ybSnapshotManifestObject), body)
	return run
}

func (fixture *retentionCommandFixture) requireObject(t *testing.T, key string, present bool) {
	t.Helper()
	_, found := fixture.store.object(key)
	if found != present {
		t.Fatalf("object %s exists = %t; expected %t", key, found, present)
	}
}

func (fixture *retentionCommandFixture) events(t *testing.T) []audit.Event {
	t.Helper()
	rows, err := audit.NewPoolOutbox(fixture.pool).ReadBatch(t.Context(), 10000)
	if err != nil {
		t.Fatalf("read retention outbox: %v", err)
	}
	var events []audit.Event
	for _, row := range rows {
		if row.Event.Verb == string(audit.VerbOpsBackupRetention) && row.Event.Actor.ID == cli.ServiceActorID(fixture.service) {
			events = append(events, row.Event)
		}
	}
	return events
}

func ybSnapshotKeyPrefix(run string) string        { return "yugabyte-snapshot/" + run + "/" }
func ybRunArtifactKey(run, artifact string) string { return ybSnapshotKeyPrefix(run) + artifact }
func retentionExtraKey(run string, index int) string {
	return fmt.Sprintf("%sextra/%05d", ybSnapshotKeyPrefix(run), index)
}
