//go:build runtimeaudit

package integration_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v5/errmask"
	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// TestSearchSnapshotPartialCreateResponse verifies the pinned SDK's partial
// create response and real PIT identity without provisioning the model.
func TestSearchSnapshotPartialCreateResponse(t *testing.T) {
	engine := testenv.OpenSearch(t)
	direct := integration.OpenSearchClient(t, engine)
	ctx := t.Context()
	index := "tack-pit-partial-" + uuid.NewString()
	if _, err := direct.Indices.Create(
		ctx,
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
		if _, err := direct.Indices.Delete(
			context.WithoutCancel(ctx),
			&opensearchapi.IndicesDeleteReq{Indices: []string{index}},
		); err != nil {
			t.Errorf("delete index %s: %v", index, err)
		}
	})
	proxy, endpoint, caPath := startPredictHoldProxy(t, engine.Endpoint, engine.CA, "")
	rewrite := proxy.engine.Rewrite
	proxy.engine.Rewrite = func(request *httputil.ProxyRequest) {
		rewrite(request)
		request.Out.Header.Del("Accept-Encoding")
	}
	proxy.engine.ModifyResponse = failPointInTimeShards
	certificate, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatalf("read the proxy certificate: %v", err)
	}
	settings := search.Config{
		Endpoint:       endpoint,
		CA:             string(certificate),
		Username:       engine.Username,
		Password:       engine.Password,
		RequestTimeout: time.Minute,
		MaxRetries:     0,
	}
	client, err := opensearchapi.NewClient(opensearchapi.Config{Client: settings.ClientConfig(nil)})
	if err != nil {
		t.Fatalf("create the pinned API client: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Client.Close(); err != nil {
			t.Errorf("close the pinned API client: %v", err)
		}
	})
	if client.ErrorMask() != errmask.Empty {
		t.Fatalf("the adapter client settings masked partial failures: %s", client.ErrorMask())
	}
	created, err := client.PointInTime.Create(
		ctx,
		&opensearchapi.CreatePITReq{
			Indices: []string{index},
			Params:  &opensearchapi.CreatePITParams{KeepAlive: time.Minute},
		},
	)
	var partial *opensearchapi.PartialSearchError
	if !errors.As(err, &partial) || !created.PITID.IsSet() || created.Shards.Failed != 1 ||
		created.Inspect().Response.StatusCode != http.StatusOK {
		t.Fatalf(
			"the partial create did not retain its PIT identity and HTTP response: created=%+v error=%v",
			created,
			err,
		)
	}
	t.Logf(
		"partial create: mask=%s HTTP=%d failed_shards=%d pit_id=%s error=%v",
		client.ErrorMask(),
		created.Inspect().Response.StatusCode,
		created.Shards.Failed,
		created.PITID,
		err,
	)
	listed, err := direct.PointInTime.GetAll(ctx, nil)
	if err != nil {
		t.Fatalf("list the real partial point in time: %v", err)
	}
	open := false
	for _, pit := range listed.PITs {
		if pit.PITID == created.PITID {
			open = true
		}
	}
	if !open {
		t.Fatal("the real engine did not retain the point in time from the partial response")
	}
	if _, err := direct.PointInTime.Delete(
		ctx,
		&opensearchapi.DeletePITReq{
			Body: &opensearchapi.DeletePITBody{PITID: []opensearchapi.PITID{created.PITID}},
		},
	); err != nil {
		t.Fatalf("delete the partial point in time: %v", err)
	}
}
