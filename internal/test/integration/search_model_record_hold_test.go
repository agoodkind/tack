package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/test/integration"
)

const (
	staleRecordInterval      = 100 * time.Millisecond
	deployingRecord          = "DEPLOYING"
	deployTaskCompletedState = "COMPLETED"
	deployTaskFailedState    = "FAILED"
	modelIndexSettings       = "/.plugins-ml-model/_settings"
	modelWriteBlockOn        = `{"index":{"blocks":{"write":true}}}`
	modelWriteBlockOff       = `{"index":{"blocks":{"write":null}}}`
	modelRecordCleanup       = time.Minute
)

// startStaleRecord waits for the deploy to write DEPLOYING, writes DEPLOY_FAILED,
// and blocks writes to the model index. It releases the block and writes DEPLOYED
// when a deploy task of modelID has been COMPLETED for lag. It removes the block
// before the test's redeploy cleanup runs.
func startStaleRecord(t *testing.T, fixture integration.QueryFixture, modelID string, lag time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(integration.WithTestOperation(t, t.Context()))
	since := clock.Now().UTC()
	done := make(chan error, 1)
	go func() { done <- holdStaleRecord(ctx, fixture, modelID, since, lag) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("hold stale model record: %v", err)
		}
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), modelRecordCleanup)
		defer stop()
		if _, err := modelRecordCall(cleanup, fixture, http.MethodPut, modelIndexSettings, modelWriteBlockOff); err != nil {
			t.Errorf("remove the model index write block: %v", err)
		}
	})
}

// holdStaleRecord blocks the record for one deploy. A deploy task can fail when
// the ML Commons memory breaker rejects it, and the retry must write DEPLOYING.
// A failed task removes the block, and the next deploy sets it again.
func holdStaleRecord(ctx context.Context, fixture integration.QueryFixture, modelID string, since time.Time, lag time.Duration) error {
	failures := 0
	for ctx.Err() == nil {
		if err := blockStaleRecord(ctx, fixture, modelID); err != nil {
			return ignoreCanceled(ctx, err)
		}
		retry, err := awaitBlockedDeploy(ctx, fixture, modelID, since, lag, &failures)
		if err != nil {
			return ignoreCanceled(ctx, err)
		}
		if !retry {
			return nil
		}
	}
	return nil
}

func blockStaleRecord(ctx context.Context, fixture integration.QueryFixture, modelID string) error {
	if err := waitForDeployingRecord(ctx, fixture, modelID); err != nil {
		return err
	}
	if err := writeModelState(ctx, fixture, modelID, staleRecordState); err != nil {
		return err
	}
	_, err := modelRecordCall(ctx, fixture, http.MethodPut, modelIndexSettings, modelWriteBlockOn)
	return err
}

// awaitBlockedDeploy returns true after a deploy task fails and it removes the
// block. It returns false after it releases the record, or when ctx ends.
func awaitBlockedDeploy(ctx context.Context, fixture integration.QueryFixture, modelID string, since time.Time, lag time.Duration, failures *int) (bool, error) {
	var completed time.Time
	for ctx.Err() == nil {
		if completed.IsZero() {
			finished, err := deployTaskCount(ctx, fixture, modelID, since, deployTaskCompletedState)
			if err != nil {
				return false, err
			}
			if finished > 0 {
				completed = clock.Now()
			}
			failed, err := deployTaskCount(ctx, fixture, modelID, since, deployTaskFailedState)
			if err != nil {
				return false, err
			}
			if failed > *failures {
				*failures = failed
				_, err := modelRecordCall(ctx, fixture, http.MethodPut, modelIndexSettings, modelWriteBlockOff)
				return true, err
			}
		}
		if !completed.IsZero() && clock.Now().Sub(completed) >= lag {
			if _, err := modelRecordCall(ctx, fixture, http.MethodPut, modelIndexSettings, modelWriteBlockOff); err != nil {
				return false, err
			}
			return false, writeModelState(ctx, fixture, modelID, deployedRecordState)
		}
		time.Sleep(staleRecordInterval)
	}
	return false, nil
}

func waitForDeployingRecord(ctx context.Context, fixture integration.QueryFixture, modelID string) error {
	path := "/_plugins/_ml/models/" + url.PathEscape(modelID)
	for ctx.Err() == nil {
		raw, err := modelRecordCall(ctx, fixture, http.MethodGet, path, "")
		if err != nil {
			return err
		}
		var record struct {
			State string `json:"model_state"`
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			return fmt.Errorf("decode model %s: %w", modelID, err)
		}
		if record.State == deployingRecord {
			return nil
		}
		time.Sleep(staleRecordInterval)
	}
	return ctx.Err()
}

// ignoreCanceled drops the error of a request that the end of the test canceled.
func ignoreCanceled(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return nil
	}
	return err
}
