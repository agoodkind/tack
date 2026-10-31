package resourcesummary

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strings"

	"goodkind.io/tack/internal/datagen"
)

const (
	resultBlockStart = "{"
	resultBlockEnd   = "}"
)

// NodeSummary reports the largest resource readings of one OpenSearch node during a
// search-mixed run.
type NodeSummary struct {
	Name                 string
	Readings             int
	CPUPercentMax        int
	ProcessCPUPercentMax int
	HeapUsedMaxBytes     int64
	// HeapUsedMaxFraction is the largest heap_used_bytes divided by heap_max_bytes of the
	// same reading. It can come from a reading other than the one with HeapUsedMaxBytes.
	HeapUsedMaxFraction   float64
	MemoryUsedMaxFraction float64
	DiskAvailableMinBytes int64
	// DiskTotalBytes is disk_total_bytes of the reading with DiskAvailableMinBytes.
	DiskTotalBytes int64
}

// RunSummary reports the workload samples of one search-mixed run, with nodes sorted by name.
type RunSummary struct {
	Samples int
	Nodes   []NodeSummary
}

type runResult struct {
	Samples []runSample `json:"samples"`
}

type runSample struct {
	Nodes []datagen.SearchMixedNodeResources `json:"opensearch_nodes"`
}

// SummarizeRun reads the indented search-mixed result that a run log contains between a line
// that is only "{" and a line that is only "}", and summarizes its OpenSearch node samples.
func SummarizeRun(ctx context.Context, path string) (RunSummary, error) {
	block, err := readResultBlock(path)
	if err != nil {
		return RunSummary{}, err
	}
	var result runResult
	err = json.Unmarshal(block, &result)
	if err != nil {
		return RunSummary{}, wrapError("decode run result in "+path, err)
	}
	byName := map[string]*NodeSummary{}
	for _, sample := range result.Samples {
		for _, node := range sample.Nodes {
			summary, found := byName[node.Name]
			if !found {
				summary = newNodeSummary(node)
				byName[node.Name] = summary
			}
			addNodeReading(summary, node)
		}
	}
	nodes := make([]NodeSummary, 0, len(byName))
	for _, summary := range byName {
		nodes = append(nodes, *summary)
	}
	slices.SortFunc(nodes, func(left NodeSummary, right NodeSummary) int {
		return strings.Compare(left.Name, right.Name)
	})
	slog.InfoContext(ctx, "resource_summary.run_read",
		slog.String("path", path), slog.Int("samples", len(result.Samples)),
		slog.Int("nodes", len(nodes)))
	return RunSummary{Samples: len(result.Samples), Nodes: nodes}, nil
}

// newNodeSummary seeds the maximum and minimum fields from the first reading and sets
// Readings to zero. addNodeReading then applies every reading, including the first.
func newNodeSummary(node datagen.SearchMixedNodeResources) *NodeSummary {
	return &NodeSummary{
		Name:                  node.Name,
		Readings:              0,
		CPUPercentMax:         node.CPUPercent,
		ProcessCPUPercentMax:  node.ProcessCPUPercent,
		HeapUsedMaxBytes:      node.HeapUsedBytes,
		HeapUsedMaxFraction:   fraction(node.HeapUsedBytes, node.HeapMaxBytes),
		MemoryUsedMaxFraction: fraction(node.MemoryUsedBytes, node.MemoryTotalBytes),
		DiskAvailableMinBytes: node.DiskAvailableBytes,
		DiskTotalBytes:        node.DiskTotalBytes,
	}
}

func addNodeReading(summary *NodeSummary, node datagen.SearchMixedNodeResources) {
	summary.Readings++
	summary.CPUPercentMax = max(summary.CPUPercentMax, node.CPUPercent)
	summary.ProcessCPUPercentMax = max(summary.ProcessCPUPercentMax, node.ProcessCPUPercent)
	memoryFraction := fraction(node.MemoryUsedBytes, node.MemoryTotalBytes)
	summary.MemoryUsedMaxFraction = max(summary.MemoryUsedMaxFraction, memoryFraction)
	heapFraction := fraction(node.HeapUsedBytes, node.HeapMaxBytes)
	summary.HeapUsedMaxFraction = max(summary.HeapUsedMaxFraction, heapFraction)
	summary.HeapUsedMaxBytes = max(summary.HeapUsedMaxBytes, node.HeapUsedBytes)
	if node.DiskAvailableBytes < summary.DiskAvailableMinBytes {
		summary.DiskAvailableMinBytes = node.DiskAvailableBytes
		summary.DiskTotalBytes = node.DiskTotalBytes
	}
}

func fraction(part int64, whole int64) float64 {
	return float64(part) / float64(whole)
}

func readResultBlock(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, wrapError("open run log "+path, err)
	}
	block, scanErr := scanResultBlock(file, path)
	err = closeFile(file, scanErr)
	if err != nil {
		return nil, err
	}
	return block, nil
}

func scanResultBlock(file *os.File, path string) ([]byte, error) {
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, initialLineBytes), maxLineBytes)
	var block []byte
	inBlock := false
	for scanner.Scan() {
		line := scanner.Text()
		if !inBlock && line == resultBlockStart {
			inBlock = true
		}
		if !inBlock {
			continue
		}
		block = append(block, line...)
		block = append(block, '\n')
		if line == resultBlockEnd {
			return block, nil
		}
	}
	scanErr := scanner.Err()
	if scanErr != nil {
		return nil, wrapError("scan run log "+path, scanErr)
	}
	return nil, errors.New("run log " + path + " has no complete indented result")
}
