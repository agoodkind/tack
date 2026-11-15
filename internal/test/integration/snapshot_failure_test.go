//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"testing"

	"goodkind.io/tack/internal/test/integration"
)

// TestSearchPartialPointInTimeDeleted requires failed snapshot creation to
// return an error and close the partial snapshot in the real engine.
func TestSearchPartialPointInTimeDeleted(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	harness, capture := newCaptureHarness(t, fixture, false)
	rewrite := capture.engine.Rewrite
	capture.engine.Rewrite = func(outbound *httputil.ProxyRequest) {
		rewrite(outbound)
		outbound.Out.Header.Del("Accept-Encoding")
	}
	created := make(chan string, 1)
	capture.engine.ModifyResponse = func(response *http.Response) error {
		if response.Request.Method != http.MethodPost ||
			!strings.HasSuffix(response.Request.URL.Path, pointInTimePathSuffix) {
			return nil
		}

		observed, err := readSnapshotResponse(response)
		if err != nil {
			return err
		}

		created <- observed.id
		return failPointInTimeShards(response)
	}

	if _, err := integration.TrySearch(harness, failureAuditQuery, ""); err == nil ||
		!strings.Contains(err.Error(), unexpectedErrorText) {
		t.Fatalf(
			"partial snapshot search returned %v, want an error containing %q",
			err,
			unexpectedErrorText,
		)
	}

	select {
	case identifier := <-created:
		requireDeleteSent(t, capture, []string{identifier})
		requirePITsClosed(t, fixture, []string{identifier})
	default:
		t.Fatal("the engine did not create a partial snapshot")
	}
}

// failPointInTimeShards makes a real snapshot response report a failed shard.
func failPointInTimeShards(response *http.Response) error {
	payload, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		return fmt.Errorf("read the point in time response: %w", err)
	}

	var created, shards map[string]json.RawMessage
	if err := json.Unmarshal(payload, &created); err != nil {
		return fmt.Errorf("decode the point in time response: %w", err)
	}
	if err := json.Unmarshal(created["_shards"], &shards); err != nil {
		return fmt.Errorf("decode the point in time shards: %w", err)
	}

	shards["failed"] = json.RawMessage("1")
	if created["_shards"], err = json.Marshal(shards); err != nil {
		return fmt.Errorf("encode the point in time shards: %w", err)
	}
	if payload, err = json.Marshal(created); err != nil {
		return fmt.Errorf("encode the point in time response: %w", err)
	}
	response.Body = io.NopCloser(bytes.NewReader(payload))
	response.ContentLength = int64(len(payload))
	response.Header.Set("Content-Length", strconv.Itoa(len(payload)))
	return nil
}
