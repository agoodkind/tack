package ops_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"goodkind.io/tack/internal/audit"
)

func TestBackupRetentionPaginatedDeletion(t *testing.T) {
	fixture := newRetentionCommandFixture(t)
	expired := fixture.completeRun(t, fixture.cutoff.Add(-time.Hour))
	retained := fixture.completeRun(t, fixture.cutoff)
	putRetentionObjects(t, fixture, expired, retentionDeleteBatchSize+5)
	if output, err := fixture.run(t, true); err != nil {
		t.Fatalf("paginated retention failed: %v\n%s", err, output)
	}
	page, err := fixture.store.client.ListObjectsV2(t.Context(), &s3.ListObjectsV2Input{
		Bucket: aws.String(fixture.config.BackupS3BucketMain), Prefix: aws.String(ybSnapshotKeyPrefix(expired)),
	})
	if err != nil || len(page.Contents) != 0 {
		t.Fatalf("expired objects remain after paginated cleanup: count=%d err=%v", len(page.Contents), err)
	}
	fixture.requireObject(t, ybRunArtifactKey(retained, ybSnapshotManifestObject), true)
}

func TestBackupRetentionDeleteFailurePreservesObjects(t *testing.T) {
	fixture := newRetentionCommandFixture(t)
	expired := fixture.completeRun(t, fixture.cutoff.Add(-time.Hour))
	fixture.completeRun(t, fixture.cutoff)
	fixture.store.signAsReader(fixture.config)
	if _, err := fixture.run(t, true); err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Fatalf("read-only cleanup returned %v; expected an access refusal", err)
	}
	fixture.requireObject(t, ybRunArtifactKey(expired, ybSnapshotManifestObject), true)
	results := 0
	for _, event := range fixture.events(t) {
		if event.Context.Phase == audit.PhaseResult {
			results++
			if event.Outcome != audit.OutcomeFailed {
				t.Fatalf("delete refusal recorded outcome %s", event.Outcome)
			}
		}
	}
	if results != 2 {
		t.Fatalf("delete refusal recorded %d terminal results; expected a step and command result", results)
	}
}

func TestBackupRetentionCancellationReportsPartialDeletion(t *testing.T) {
	fixture := newRetentionCommandFixture(t)
	expired := fixture.completeRun(t, fixture.cutoff.Add(-time.Hour))
	retained := fixture.completeRun(t, fixture.cutoff)
	putRetentionObjects(t, fixture, expired, retentionDeleteBatchSize*5)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() { _, err := fixture.runContext(t, ctx, true); finished <- err }()
	deadline := time.NewTimer(time.Minute)
	defer deadline.Stop()
	for {
		_, present := fixture.store.object(retentionExtraKey(expired, retentionDeleteBatchSize))
		if !present {
			cancel()
			break
		}
		select {
		case err := <-finished:
			t.Fatalf("cleanup finished before the partial-delete cancellation: %v", err)
		case <-deadline.C:
			t.Fatal("cleanup did not delete the first object before the deadline")
		default:
		}
	}
	if err := <-finished; err == nil {
		t.Fatal("canceled cleanup did not return an error")
	}
	fixture.requireObject(t, ybRunArtifactKey(retained, ybSnapshotManifestObject), true)
	partial := false
	uncertain := false
	for _, event := range fixture.events(t) {
		if event.Context.Phase == audit.PhaseResult && event.Outcome == audit.OutcomePartiallyCompleted {
			partial = true
		}
		if event.Context.Phase == audit.PhaseResult {
			var evidence struct {
				Uncertain bool `json:"uncertain"`
			}
			if err := json.Unmarshal(event.Extra, &evidence); err != nil {
				t.Fatalf("decode retention cancellation evidence: %v", err)
			}
			uncertain = uncertain || evidence.Uncertain
		}
	}
	if !partial {
		t.Fatal("partial deletion did not record a partially completed outcome")
	}
	if !uncertain {
		t.Fatal("canceled deletion did not record uncertainty about its last batch")
	}
	if _, err := fixture.run(t, true); err != nil {
		t.Fatalf("repeat cleanup after cancellation: %v", err)
	}
}

func putRetentionObjects(t *testing.T, fixture *retentionCommandFixture, run string, count int) {
	t.Helper()
	var workers sync.WaitGroup
	for worker := range 8 {
		workers.Go(func() {
			for index := worker; index < count; index += 8 {
				_, err := fixture.store.client.PutObject(t.Context(), &s3.PutObjectInput{
					Bucket: aws.String(fixture.config.BackupS3BucketMain), Key: aws.String(retentionExtraKey(run, index)),
					Body: bytes.NewReader([]byte("retention page object")),
				})
				if err != nil {
					t.Errorf("write retention pagination object %d: %v", index, err)
					return
				}
			}
		})
	}
	workers.Wait()
}
