package integration_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv/opsoutbox"
)

const searchMixedOperatorID = "019dd226-440e-729a-a442-281aaf73ca30"

// requireSearchMixedReindexAudit checks that the reindex operation records
// one intent, one completed replacement step with the expected target index,
// and one completed operation result.
func requireSearchMixedReindexAudit(t *testing.T, targetIndex string) {
	t.Helper()
	pool := ledgerAdminPool(t)
	events := opsoutbox.Events(t, pool, opsoutbox.Filter{
		Verb: audit.VerbOpsSearchReindex, Path: []string{"actor", "id"}, Value: searchMixedOperatorID,
	})
	intents := eventsInPhase(events, audit.PhaseIntent, uuid.Nil)
	if len(intents) != 1 {
		t.Fatalf("qa datagen search-mixed: the reindex operation recorded %d intent events instead of 1", len(intents))
	}
	operationID := intents[0].Context.OperationID
	stepResults := eventsInPhase(events, audit.PhaseResult, operationID)
	if len(stepResults) != 1 || stepResults[0].Outcome != audit.OutcomeCompleted {
		t.Fatalf("qa datagen search-mixed: reindex operation %s did not record exactly one completed step result; the test found %d step results", operationID, len(stepResults))
	}
	var evidence struct {
		TargetIndex string `json:"target_index"`
	}
	if err := json.Unmarshal(stepResults[0].Extra, &evidence); err != nil || evidence.TargetIndex != targetIndex {
		t.Fatalf("qa datagen search-mixed: the reindex step evidence did not decode to the expected target index (recorded=%q, expected=%q, decode error=%v)", evidence.TargetIndex, targetIndex, err)
	}
	requireCompletedResult(t, eventsOfOperation(events, operationID), operationID)
}

func deleteSearchMixedIndex(t *testing.T, cfg *config.Config, index string) {
	t.Helper()
	if index == "" {
		return
	}
	certificate, err := os.ReadFile(cfg.SearchCA)
	if err != nil {
		t.Fatalf("qa datagen search-mixed: the test could not read the OpenSearch CA certificate: %v", err)
	}
	settings := search.Config{Endpoint: cfg.SearchEndpoint, CA: string(certificate), Username: cfg.SearchUsername, Password: cfg.SearchPassword, RequestTimeout: 2 * time.Minute, MaxRetries: 1}
	client, err := opensearch.NewClient(settings.ClientConfig(nil))
	if err != nil {
		t.Fatalf("qa datagen search-mixed: the test could not create the OpenSearch client: %v", err)
	}
	t.Cleanup(func() {
		request := opensearchapi.IndicesDeleteReq{Indices: []string{index}}
		if _, err := opensearchapi.NewFromClient(client).Indices.Delete(context.WithoutCancel(integration.WithTestOperation(t, t.Context())), request); err != nil {
			t.Logf("qa datagen search-mixed: the test could not delete OpenSearch index %s: %v", index, err)
		}
	})
}
