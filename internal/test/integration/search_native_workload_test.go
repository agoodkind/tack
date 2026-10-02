package integration

import (
	"net/http"
	"strings"
	"testing"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
)

func mixedUnicodePage4096() string {
	const unit = "A🙂 Ελληνικά 日本語 e\u0301\n"
	var page strings.Builder
	for page.Len()+len(unit) <= 4096 {
		page.WriteString(unit)
	}
	page.WriteString(strings.Repeat("x", 4096-page.Len()))
	return page.String()
}

func nativeCompletePageCases() []struct{ name, text string } {
	return []struct{ name, text string }{
		{name: "digits", text: unicodePage4096()},
		{name: "archived-mixed-unicode", text: mixedUnicodePage4096()},
	}
}

func requireNativeJVMHeap(t *testing.T, client *opensearchapi.Client) {
	t.Helper()
	var stats opensearchapi.NodesStatsResp
	response, err := opensearch.Do(t.Context(), client.Client, http.MethodGet,
		opensearchapi.NodesStatsReq{Metric: []string{"jvm"}}, &stats)
	if err != nil || response == nil {
		t.Fatalf("read native JVM stats: %v", err)
	}
	if response.IsError() {
		t.Fatalf("native JVM stats rejected: %v", opensearch.ParseError(response))
	}
	if len(stats.Nodes) != 1 || stats.NodesInfo.Failed != 0 {
		t.Fatalf("native JVM stats returned unexpected nodes: %+v", stats.NodesInfo)
	}
	for nodeID, value := range stats.Nodes {
		if value.JVM.Mem.HeapMaxInBytes != 2147483648 || value.JVM.Mem.HeapCommittedInBytes != 2147483648 {
			t.Fatalf("native JVM heap=%+v, want 2 GiB committed and maximum", value.JVM.Mem)
		}
		t.Logf("native JVM node=%s heap_max=%d heap_committed=%d", nodeID, value.JVM.Mem.HeapMaxInBytes, value.JVM.Mem.HeapCommittedInBytes)
	}
}
