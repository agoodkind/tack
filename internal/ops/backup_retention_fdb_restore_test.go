//go:build integration

package ops_test

import (
	"archive/tar"
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

func retentionFDBTestRestore(t *testing.T, cfg *config.Config, source, name string, cutoff time.Time) {
	t.Helper()
	cluster := testenv.FreshFoundationDB(t)
	retentionFDBTestAgent(t, cluster)
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: "retention-source.cluster", Mode: 0o644, Size: int64(len(contents))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(contents); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	if _, err := cli.CopyToContainer(t.Context(), testenv.FoundationDBContainer(cluster), client.CopyToContainerOptions{
		DestinationPath: "/var/fdb", Content: &archive,
	}); err != nil {
		t.Fatal(err)
	}
	retentionFDBTestExec(t, cfg, cluster, []string{
		"fdbrestore", "start", "--dest-cluster-file", "/var/fdb/fdb.cluster",
		"-r", retentionFDBTestURL(cfg, name), "--timestamp", cutoff.UTC().Format("2006/01/02.15:04:05-0700"),
		"--orig-cluster-file", "/var/fdb/retention-source.cluster", "--waitfordone",
	})
	result := retentionFDBTestExec(t, cfg, cluster, []string{"fdbcli", "-C", "/var/fdb/fdb.cluster", "--exec", "get retention-key"})
	if !strings.Contains(result, "retained-value") {
		t.Fatalf("restored key differs from retained value: %s", result)
	}
}

func retentionFDBTestKeys(t *testing.T, fixture *retentionCommandFixture) map[string]struct{} {
	t.Helper()
	keys := make(map[string]struct{})
	paginator := s3.NewListObjectsV2Paginator(fixture.store.client, &s3.ListObjectsV2Input{Bucket: aws.String(fixture.config.BackupS3BucketMain)})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, object := range page.Contents {
			keys[aws.ToString(object.Key)] = struct{}{}
		}
	}
	return keys
}

func retentionFDBTestRefusal(t *testing.T, cfg *config.Config, cluster, name string, cutoff time.Time) {
	t.Helper()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	timestamp := cutoff.UTC().Format("2006/01/02.15:04:05-0700")
	created, err := cli.ExecCreate(t.Context(), testenv.FoundationDBContainer(cluster), client.ExecCreateOptions{
		Cmd: []string{
			"fdbbackup", "expire", "-C", "/var/fdb/fdb.cluster", "-d", retentionFDBTestURL(cfg, name),
			"--expire-before-timestamp", timestamp, "--restorable-after-timestamp", timestamp,
		},
		AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	attached, err := cli.ExecAttach(t.Context(), created.ID, client.ExecAttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer attached.Close()
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attached.Reader); err != nil {
		t.Fatal(err)
	}
	state, err := cli.ExecInspect(t.Context(), created.ID, client.ExecInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if state.ExitCode == 0 || !strings.Contains(stdout.String()+stderr.String(), "minimum restorability") {
		t.Fatal("native minimum-restorability guard did not refuse the unsafe expiration")
	}
}
