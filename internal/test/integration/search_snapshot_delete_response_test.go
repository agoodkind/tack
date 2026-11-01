package integration_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"

	"goodkind.io/tack/internal/adapters/search"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// TestSearchSnapshotDeleteAlreadyClosedResponse checks the real engine's
// idempotent delete response without provisioning a model.
func TestSearchSnapshotDeleteAlreadyClosedResponse(t *testing.T) {
	engine := testenv.OpenSearch(t)
	client := integration.OpenSearchClient(t, engine)
	adapter := seedlessAdapter(t, engine)
	ctx := t.Context()
	index := "tack-pit-delete-" + uuid.NewString()
	if _, err := client.Indices.Create(ctx, opensearchapi.IndicesCreateReq{
		Index: index, BodyReader: strings.NewReader(`{"settings":{"number_of_shards":1,"number_of_replicas":0}}`),
	}); err != nil {
		t.Fatalf("create index %s: %v", index, err)
	}
	t.Cleanup(func() {
		if _, err := client.Indices.Delete(context.WithoutCancel(ctx), &opensearchapi.IndicesDeleteReq{Indices: []string{index}}); err != nil {
			t.Errorf("delete index %s: %v", index, err)
		}
	})
	created, err := client.PointInTime.Create(ctx, &opensearchapi.CreatePITReq{
		Indices: []string{index}, Params: &opensearchapi.CreatePITParams{KeepAlive: time.Minute},
	})
	if err != nil {
		t.Fatalf("create point in time on %s: %v", index, err)
	}
	for attempt := range 2 {
		deleted, err := client.PointInTime.Delete(ctx, &opensearchapi.DeletePITReq{
			Body: &opensearchapi.DeletePITBody{PITID: []opensearchapi.PITID{created.PITID}},
		})
		if err != nil {
			t.Fatalf("delete attempt %d: %v", attempt+1, err)
		}
		status := deleted.Inspect().Response.StatusCode
		t.Logf("delete attempt %d: HTTP %d, results %+v", attempt+1, status, deleted.PITs)
		if status != http.StatusOK || len(deleted.PITs) != 1 || !deleted.PITs[0].Successful || deleted.PITs[0].PITID != created.PITID {
			t.Fatalf("delete attempt %d did not confirm the requested point in time: %+v", attempt+1, deleted)
		}
		listed, err := client.PointInTime.GetAll(ctx, nil)
		if err != nil {
			t.Fatalf("list points in time after delete attempt %d: %v", attempt+1, err)
		}
		for _, pit := range listed.PITs {
			if pit.PITID == created.PITID {
				t.Fatalf("point in time remains open after delete attempt %d", attempt+1)
			}
		}
	}
	ranker := adapter.Ranker(search.RankerSettings{KeepAlive: time.Minute, TokenBytes: 65536, BatchSize: 100})
	if err := ranker.Close(ctx, searchdomain.Snapshot{PITID: created.PITID.String(), Index: index}); err != nil {
		t.Fatalf("close the absent point in time through the production adapter: %v", err)
	}
}
