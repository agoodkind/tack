package datagen

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/config"
)

// searchMixedNodeMetrics limits each OpenSearch request to these statistics groups.
var searchMixedNodeMetrics = []string{"os", "process", "jvm", "fs"}

type searchMixedNodeStats struct {
	Nodes map[string]searchMixedNodeStat `json:"nodes"`
}

type searchMixedNodeStat struct {
	Name    string                 `json:"name"`
	OS      searchMixedOSStat      `json:"os"`
	Process searchMixedProcessStat `json:"process"`
	JVM     searchMixedJVMStat     `json:"jvm"`
	FS      searchMixedFSStat      `json:"fs"`
}

type searchMixedOSStat struct {
	CPU searchMixedCPUStat    `json:"cpu"`
	Mem searchMixedMemoryStat `json:"mem"`
}

type searchMixedCPUStat struct {
	Percent int `json:"percent"`
}

type searchMixedMemoryStat struct {
	Total int64 `json:"total_in_bytes"`
	Used  int64 `json:"used_in_bytes"`
}

type searchMixedProcessStat struct {
	CPU searchMixedCPUStat `json:"cpu"`
}

type searchMixedJVMStat struct {
	Mem searchMixedHeapStat `json:"mem"`
}

type searchMixedHeapStat struct {
	HeapUsed int64 `json:"heap_used_in_bytes"`
	HeapMax  int64 `json:"heap_max_in_bytes"`
}

type searchMixedFSStat struct {
	Total searchMixedDiskStat `json:"total"`
}

type searchMixedDiskStat struct {
	Total     int64 `json:"total_in_bytes"`
	Available int64 `json:"available_in_bytes"`
}

// searchMixedResources reads OpenSearch node statistics with the configured search endpoint,
// CA, and credentials.
type searchMixedResources struct {
	client *opensearch.Client
}

func newSearchMixedResources(ctx context.Context, cfg *config.Config) (*searchMixedResources, error) {
	client, err := newSearchStatisticsClient(ctx, cfg, "qa datagen search-mixed")
	if err != nil {
		return nil, err
	}
	return &searchMixedResources{client: client}, nil
}

func (r *searchMixedResources) close(ctx context.Context) {
	if err := r.client.Close(); err != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_mixed_resources_close_failed", slog.String("err", err.Error()))
	}
}

// read returns statistics for every OpenSearch node in node name order.
func (r *searchMixedResources) read(ctx context.Context) ([]SearchMixedNodeResources, error) {
	var stats searchMixedNodeStats
	request := opensearchapi.NodesStatsReq{Metric: searchMixedNodeMetrics}
	response, err := opensearch.Do(ctx, r.client, http.MethodGet, request, &stats)
	if err != nil {
		return nil, loggedError(ctx, "qa datagen search-mixed: the command could not read OpenSearch node statistics", err)
	}
	if response.IsError() {
		_ = response.Body.Close()
		return nil, loggedError(ctx, "qa datagen search-mixed: the command could not read OpenSearch node statistics", fmt.Errorf("OpenSearch returned HTTP status %d", response.StatusCode))
	}
	nodes := make([]SearchMixedNodeResources, 0, len(stats.Nodes))
	for _, node := range stats.Nodes {
		nodes = append(nodes, SearchMixedNodeResources{
			Name: node.Name, CPUPercent: node.OS.CPU.Percent, ProcessCPUPercent: node.Process.CPU.Percent,
			MemoryUsedBytes: node.OS.Mem.Used, MemoryTotalBytes: node.OS.Mem.Total,
			HeapUsedBytes: node.JVM.Mem.HeapUsed, HeapMaxBytes: node.JVM.Mem.HeapMax,
			DiskTotalBytes: node.FS.Total.Total, DiskAvailableBytes: node.FS.Total.Available,
		})
	}
	slices.SortFunc(nodes, func(left, right SearchMixedNodeResources) int { return strings.Compare(left.Name, right.Name) })
	return nodes, nil
}
