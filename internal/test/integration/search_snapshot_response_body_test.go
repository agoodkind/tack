//go:build runtimeaudit

package integration_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	opensearch "github.com/opensearch-project/opensearch-go/v5"
	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
	"goodkind.io/tack/internal/adapters/search"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// delayedSnapshotBody prevents the proxy from forwarding the real engine
// body after it flushes the response headers.
type delayedSnapshotBody struct {
	io.ReadCloser
	ctx      context.Context
	released <-chan struct{}
}

func (body delayedSnapshotBody) Read(buffer []byte) (int, error) {
	select {
	case <-body.released:
		return body.ReadCloser.Read(buffer)
	case <-body.ctx.Done():
		return 0, body.ctx.Err()
	}
}

// TestSearchSnapshotInterruptedResponseBody deletes a real PIT, flushes its
// successful HTTP headers, and interrupts the client before its body arrives.
func TestSearchSnapshotInterruptedResponseBody(t *testing.T) {
	for _, interruption := range []string{"cancel", "client_timeout"} {
		t.Run(interruption, func(t *testing.T) {
			engine := testenv.OpenSearch(t)
			client := integration.OpenSearchClient(t, engine)
			index := "tack-pit-body-" + uuid.NewString()
			if _, err := client.Indices.Create(t.Context(), opensearchapi.IndicesCreateReq{
				Index: index, BodyReader: strings.NewReader(`{"settings":{"number_of_shards":1,"number_of_replicas":0}}`),
			}); err != nil {
				t.Fatalf("create index %s: %v", index, err)
			}
			t.Cleanup(func() {
				if _, err := client.Indices.Delete(context.WithoutCancel(t.Context()), &opensearchapi.IndicesDeleteReq{Indices: []string{index}}); err != nil {
					t.Errorf("delete index %s: %v", index, err)
				}
			})
			created, err := client.PointInTime.Create(t.Context(), &opensearchapi.CreatePITReq{
				Indices: []string{index}, Params: &opensearchapi.CreatePITParams{KeepAlive: time.Minute},
			})
			if err != nil {
				t.Fatalf("create point in time on %s: %v", index, err)
			}
			proxy, endpoint, caPath := startPredictHoldProxy(t, engine.Endpoint, engine.CA, "")
			proxy.engine.FlushInterval = -1
			proxy.engine.ModifyResponse = func(response *http.Response) error {
				if response.Request.Method != http.MethodDelete {
					return nil
				}
				if _, err := readSnapshotResponse(response); err != nil {
					return err
				}
				response.Body = delayedSnapshotBody{ReadCloser: response.Body, ctx: response.Request.Context(), released: proxy.released}
				return nil
			}
			certificate, err := os.ReadFile(caPath)
			if err != nil {
				t.Fatalf("read the proxy certificate: %v", err)
			}
			requestTimeout := time.Minute
			if interruption == "client_timeout" {
				requestTimeout = time.Second
			}
			adapter, err := search.New(t.Context(), search.Config{
				Endpoint: endpoint, CA: string(certificate), Username: engine.Username, Password: engine.Password,
				RequestTimeout: requestTimeout, MaxRetries: 0,
			})
			if err != nil {
				t.Fatalf("create the proxied search adapter: %v", err)
			}
			t.Cleanup(func() {
				if err := adapter.Close(context.WithoutCancel(t.Context())); err != nil {
					t.Errorf("close the proxied search adapter: %v", err)
				}
			})
			headers := make(chan struct{})
			traced := httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{
				GotFirstResponseByte: func() { close(headers) },
			})
			ctx, cancel := context.WithCancel(traced)
			defer cancel()
			ranker := adapter.Ranker(search.RankerSettings{KeepAlive: time.Minute, TokenBytes: 65536, BatchSize: 100})
			done := make(chan error, 1)
			go func() {
				done <- ranker.Close(ctx, searchdomain.Snapshot{PITID: created.PITID.String(), Index: index})
			}()
			waitForSignal(t, headers, "point in time delete response headers")
			listed, err := client.PointInTime.GetAll(t.Context(), nil)
			if err != nil {
				t.Fatalf("list points in time after the delete response headers: %v", err)
			}
			for _, pit := range listed.PITs {
				if pit.PITID == created.PITID {
					t.Fatal("the engine did not apply the delete before the response body stalled")
				}
			}
			if interruption == "cancel" {
				cancel()
			}
			select {
			case err := <-done:
				t.Logf("interrupted response: read_body=%t uncertain=%t error=%v", errors.Is(err, opensearch.ErrReadBody), errors.Is(err, searchdomain.ErrSnapshotUncertain), err)
				if !errors.Is(err, opensearch.ErrReadBody) || !errors.Is(err, searchdomain.ErrSnapshotUncertain) {
					t.Fatalf("the applied delete with an unread response returned %v, want an uncertain effect and a body-read failure", err)
				}
			case <-time.After(sessionTimeoutHoldLimit):
				t.Fatal("the interrupted response did not finish")
			}
			if interruption == "client_timeout" && ctx.Err() != nil {
				t.Fatalf("the client timeout ended the parent context: %v", ctx.Err())
			}
		})
	}
}
