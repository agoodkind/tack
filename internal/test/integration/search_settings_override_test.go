package integration_test

import (
	"context"
	"strings"
	"testing"

	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"

	"goodkind.io/tack/internal/test/integration"
)

func putPointInTimeSetting(t *testing.T, client *opensearchapi.Client, section, value string) {
	t.Helper()
	putSearchSetting(t, client, section, "search.max_open_pit_context", value)
}

func clearSearchSettings(t *testing.T, client *opensearchapi.Client) {
	t.Helper()
	for _, section := range []string{"persistent", "transient"} {
		for _, setting := range []string{"search.max_open_pit_context", "plugins.ml_commons.jvm_heap_memory_threshold"} {
			putSearchSetting(t, client, section, setting, "null")
		}
	}
}

// putSearchSetting removes the override when value is null.
func putSearchSetting(t *testing.T, client *opensearchapi.Client, section, setting, value string) {
	t.Helper()
	body := `{"` + section + `":{"` + setting + `":` + value + `}}`
	ctx := context.WithoutCancel(integration.WithTestOperation(t, t.Context()))
	response, err := client.Cluster.PutSettings(ctx, &opensearchapi.ClusterPutSettingsReq{
		BodyReader: strings.NewReader(body), Header: nil, Params: &opensearchapi.ClusterPutSettingsParams{},
	})
	if err != nil {
		t.Fatalf("set %s %s to %s: %v", section, setting, value, err)
	}
	if !response.Acknowledged {
		t.Fatalf("OpenSearch did not acknowledge %s %s %s", section, setting, value)
	}
}
