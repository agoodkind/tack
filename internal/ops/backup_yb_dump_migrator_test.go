package ops_test

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/migrations"
)

func TestYBSnapshotExportDumpsAsMigrator(t *testing.T) {
	ctx := t.Context()
	node := testenv.StartEmptyLedger(t, "tack-test-dump-"+uuid.NewString()[:8])
	if err := postgres.Migrate(ctx, node.DSN, migrations.FS); err != nil {
		t.Fatalf("migrate the empty ledger: %v", err)
	}
	bucket := testenv.ObjectStore(t)
	cfg := &config.Config{
		DatabaseURL: node.DSN, YugabyteDB: "tack",
		BackupRoot:              filepath.Join(testenv.SharedDir(t), "backups"),
		BackupYBImage:           node.Image,
		BackupFDBNetwork:        node.Network,
		BackupYBMasterAddresses: node.MasterAddress,
		BackupS3Endpoint:        bucket.Endpoint,
		BackupS3Region:          bucket.Region,
		BackupS3BucketMain:      bucket.Bucket,
	}
	cfg.BackupS3AccessKey, cfg.BackupS3SecretKey = signerOf(bucket)
	for _, generated := range []*string{
		&cfg.AuditWriterPassword, &cfg.AuditReaderPassword, &cfg.AuditRedactorPassword,
		&cfg.AuditOperatorPassword, &cfg.AppPassword, &cfg.MigratorPassword,
	} {
		*generated = uuid.NewString()
	}
	if err := ops.RunAuditSeedRoles(ctx, cfg); err != nil {
		t.Fatalf("seed-roles: %v", err)
	}

	if err := ops.RunBackupYBSnapshotExport(ctx, cfg); err != nil {
		t.Fatalf("export snapshot as tack_migrator: %v", err)
	}

	exported := exportedText(ctx, t, bucket)
	for _, statement := range []string{
		"CREATE TABLE audit.events", "CREATE EVENT TRIGGER tack_audit_schema_guard",
		"CREATE POLICY events_migrator_select", "CREATE ROLE tack_migrator", "CREATE ROLE tack_audit_writer",
	} {
		if !strings.Contains(exported, statement) {
			t.Fatalf("exported dumps do not contain %q", statement)
		}
	}
	if strings.Contains(exported, "PASSWORD") {
		t.Fatal("an exported dump contains a role password")
	}
}

func signerOf(bucket testenv.ObjectStoreBucket) (string, string) {
	return bucket.AccessKey, bucket.SecretKey
}

func exportedText(ctx context.Context, t *testing.T, bucket testenv.ObjectStoreBucket) string {
	t.Helper()
	signerID, signerValue := signerOf(bucket)
	signer := credentials.NewStaticCredentialsProvider(signerID, signerValue, "")
	client := s3.NewFromConfig(aws.Config{
		Region:      bucket.Region,
		Credentials: signer,
	}, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(bucket.Endpoint)
		options.UsePathStyle = true
	})
	listed, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket.Bucket)})
	if err != nil {
		t.Fatalf("list the export bucket: %v", err)
	}
	var text strings.Builder
	for _, object := range listed.Contents {
		fetched, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket.Bucket), Key: object.Key})
		if err != nil {
			t.Fatalf("get %s: %v", aws.ToString(object.Key), err)
		}
		body, err := io.ReadAll(fetched.Body)
		_ = fetched.Body.Close()
		if err != nil {
			t.Fatalf("read %s: %v", aws.ToString(object.Key), err)
		}
		text.Write(body)
		text.WriteString("\n")
	}
	return text.String()
}
