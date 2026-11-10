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

// TestSearchSnapshotInterruptedResponseBody checks a real PIT delete after
// cancellation, client timeout, body truncation or a complete backend error.
// Each case verifies the engine state independently.
func TestSearchSnapshotInterruptedResponseBody(t *testing.T) {
	for _, interruption := range []string{
		"cancel",
		"client_timeout",
		"truncated_body",
		"backend_error",
		"security_backend_error",
	} {
		t.Run(
			interruption,
			func(t *testing.T) {
				engine := testenv.OpenSearch(t)
				client := integration.OpenSearchClient(t, engine)
				index := "tack-pit-body-" + uuid.NewString()
				if _, err := client.Indices.Create(
					t.Context(),
					opensearchapi.IndicesCreateReq{
						Index: index,
						BodyReader: strings.NewReader(
							`{"settings":{"number_of_shards":1,"number_of_replicas":0}}`,
						),
					},
				); err != nil {
					t.Fatalf("create index %s: %v", index, err)
				}
				t.Cleanup(func() {
					if _, err := client.Indices.Delete(
						context.WithoutCancel(t.Context()),
						&opensearchapi.IndicesDeleteReq{Indices: []string{index}},
					); err != nil {
						t.Errorf("delete index %s: %v", index, err)
					}
				})
				created, err := client.PointInTime.Create(
					t.Context(),
					&opensearchapi.CreatePITReq{
						Indices: []string{
							index,
						},
						Params: &opensearchapi.CreatePITParams{KeepAlive: time.Minute},
					},
				)
				if err != nil {
					t.Fatalf("create point in time on %s: %v", index, err)
				}
				proxy, endpoint, caPath := startPredictHoldProxy(t, engine.Endpoint, engine.CA, "")
				proxy.engine.FlushInterval = -1
				proxy.engine.ModifyResponse = func(response *http.Response) error {
					if response.Request.Method != http.MethodDelete {
						return nil
					}
					if interruption == "security_backend_error" {
						_, err := replaceSnapshotResponseWithSecurityError(response)
						return err
					}
					if interruption == "backend_error" {
						_, err := replaceSnapshotResponseWithError(response)
						return err
					}
					if interruption == "truncated_body" {
						_, err := truncateSnapshotResponse(response)
						return err
					}
					if _, err := readSnapshotResponse(response); err != nil {
						return err
					}
					response.Body = delayedSnapshotBody{
						ReadCloser: response.Body,
						ctx:        response.Request.Context(),
						released:   proxy.released,
					}
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
				adapter, err := search.New(
					t.Context(),
					search.Config{
						Endpoint:       endpoint,
						CA:             string(certificate),
						Username:       engine.Username,
						Password:       engine.Password,
						RequestTimeout: requestTimeout,
						MaxRetries:     0,
					},
				)
				if err != nil {
					t.Fatalf("create the proxied search adapter: %v", err)
				}
				t.Cleanup(func() {
					if err := adapter.Close(context.WithoutCancel(t.Context())); err != nil {
						t.Errorf("close the proxied search adapter: %v", err)
					}
				})
				headers := make(chan struct{})
				traced := httptrace.WithClientTrace(
					t.Context(),
					&httptrace.ClientTrace{
						GotFirstResponseByte: func() { close(headers) },
					},
				)
				ctx, cancel := context.WithCancel(traced)
				defer cancel()
				ranker := adapter.Ranker(search.RankerSettings{
					KeepAlive:  time.Minute,
					TokenBytes: 65536,
					BatchSize:  100,
				})
				done := make(chan error, 1)
				go func() {
					done <- ranker.Close(
						ctx,
						searchdomain.Snapshot{PITID: created.PITID.String(), Index: index},
					)
				}()
				waitForSignal(t, headers, "point in time delete response headers")
				listed, err := client.PointInTime.GetAll(t.Context(), nil)
				if err != nil {
					t.Fatalf("list points in time after the delete response headers: %v", err)
				}
				for _, pit := range listed.PITs {
					if pit.PITID == created.PITID {
						t.Fatal(
							"the engine did not apply the delete before the response body stalled",
						)
					}
				}
				if interruption == "cancel" {
					cancel()
				}
				select {
				case err := <-done:
					t.Logf(
						"interrupted response: read_body=%t unexpected_eof=%t uncertain=%t parent_error=%v error=%v",
						errors.Is(err, opensearch.ErrReadBody),
						errors.Is(err, io.ErrUnexpectedEOF),
						errors.Is(err, searchdomain.ErrSnapshotUncertain),
						ctx.Err(),
						err,
					)
					if !errors.Is(err, searchdomain.ErrSnapshotUncertain) {
						t.Fatalf(
							"the applied delete with an unconfirmed result returned %v, want an uncertain effect",
							err,
						)
					}
					if interruption == "backend_error" || interruption == "security_backend_error" {
						var backendError *opensearch.StructError
						expectedType := "exception"
						if interruption == "security_backend_error" {
							expectedType = "security_exception"
						}
						if !errors.As(err, &backendError) ||
							backendError.Status != http.StatusInternalServerError ||
							backendError.Err.Type != expectedType {
							t.Fatalf(
								"the complete backend error response returned %v, want its parsed HTTP 500 exception",
								err,
							)
						}
					} else if !errors.Is(err, opensearch.ErrReadBody) {
						t.Fatalf("the unread response returned %v, want a body-read failure", err)
					}
					if interruption == "truncated_body" && !errors.Is(err, io.ErrUnexpectedEOF) {
						t.Fatalf(
							"the truncated response returned %v, want an unexpected end of its body",
							err,
						)
					}
				case <-time.After(sessionTimeoutHoldLimit):
					t.Fatal("the interrupted response did not finish")
				}
				if interruption != "cancel" && ctx.Err() != nil {
					t.Fatalf("the %s case ended the parent context: %v", interruption, ctx.Err())
				}
			},
		)
	}
}
