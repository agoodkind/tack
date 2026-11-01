//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"goodkind.io/tack/internal/adapters/search"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

type snapshotResponse struct {
	id     string
	status int
}

type snapshotSearchResult struct {
	text   string
	status int
	err    error
}

// holdSnapshotResponse delays the first matching real engine response.
// Cleanup releases the response before the existing proxy closes.
func holdSnapshotResponse(t *testing.T, capture *searchCapture, method string) (<-chan snapshotResponse, func()) {
	t.Helper()
	responses := make(chan snapshotResponse, 1)
	released := make(chan struct{})
	var releaseOnce, responseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(released) }) }
	t.Cleanup(release)
	capture.engine.ModifyResponse = func(response *http.Response) error {
		if response.Request.Method != method || !strings.HasSuffix(response.Request.URL.Path, pointInTimePathSuffix) {
			return capture.record(response)
		}
		var responseError error
		responseOnce.Do(func() {
			observed, err := readSnapshotResponse(response)
			if err != nil {
				responseError = err
				return
			}
			responses <- observed
			select {
			case <-released:
			case <-response.Request.Context().Done():
			}
		})
		return responseError
	}
	return responses, release
}

func readSnapshotResponse(response *http.Response) (snapshotResponse, error) {
	payload, err := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err != nil {
		return snapshotResponse{}, fmt.Errorf("read the point in time response: %w", err)
	}
	if closeErr != nil {
		return snapshotResponse{}, fmt.Errorf("close the point in time response: %w", closeErr)
	}
	response.Body = io.NopCloser(bytes.NewReader(payload))
	var decoded struct {
		PITID string `json:"pit_id"`
		PITs  []struct {
			PITID string `json:"pit_id"`
		} `json:"pits"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return snapshotResponse{}, fmt.Errorf("decode the point in time response: %w", err)
	}
	identifier := decoded.PITID
	if response.Request.Method == http.MethodDelete && len(decoded.PITs) == 1 {
		identifier = decoded.PITs[0].PITID
	}
	if identifier == "" {
		return snapshotResponse{}, fmt.Errorf("the point in time response omits its ID: %s", payload)
	}
	return snapshotResponse{id: identifier, status: response.StatusCode}, nil
}

func waitSnapshotResponse(t *testing.T, responses <-chan snapshotResponse) snapshotResponse {
	t.Helper()
	select {
	case response := <-responses:
		if response.status != http.StatusOK {
			t.Fatalf("the engine returned HTTP %d for point in time %s", response.status, response.id)
		}
		return response
	case <-time.After(sessionTimeoutHoldLimit):
		t.Fatal("the proxy did not receive the point in time response")
		return snapshotResponse{}
	}
}

func deleteObservedSnapshot(t *testing.T, fixture failureFixture, identifier string) {
	t.Helper()
	ranker := fixture.Adapter.Ranker(search.RankerSettings{KeepAlive: time.Minute, TokenBytes: 65536, BatchSize: 100})
	if err := ranker.Close(context.WithoutCancel(t.Context()), searchdomain.Snapshot{PITID: identifier, Index: fixture.Index}); err != nil {
		t.Errorf("delete the test point in time %s: %v", identifier, err)
	}
}
