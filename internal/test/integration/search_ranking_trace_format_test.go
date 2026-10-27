package integration_test

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
)

const traceDescriptionBytes = 120

type rankedNode struct {
	ID    uuid.UUID
	Score float32
}

func decodeTraceWeights(t *testing.T, raw json.RawMessage) map[string]float64 {
	t.Helper()
	var weights map[string]float64
	if err := json.Unmarshal(raw, &weights); err != nil {
		t.Fatalf("decode saved query tokens: %v", err)
	}
	return weights
}

func topTraceWeights(weights map[string]float64, count int) string {
	tokens := make([]string, 0, len(weights))
	for token := range weights {
		tokens = append(tokens, token)
	}
	sort.Slice(tokens, func(i, j int) bool {
		if weights[tokens[i]] != weights[tokens[j]] {
			return weights[tokens[i]] > weights[tokens[j]]
		}
		return tokens[i] < tokens[j]
	})
	parts := make([]string, 0, count)
	for _, token := range tokens[:min(count, len(tokens))] {
		parts = append(parts, fmt.Sprintf("%q:%.3f", token, weights[token]))
	}
	return "[" + strings.Join(parts, " ") + "]"
}

func traceDot(query, chunk map[string]float64) float64 {
	total := 0.0
	for token, weight := range query {
		total += weight * chunk[token]
	}
	return total
}

func traceHitNode(t *testing.T, hit opensearchapi.SearchHit) uuid.UUID {
	t.Helper()
	if hit.ID == nil {
		t.Fatal("trace hit requires a document ID")
	}
	var source struct {
		NodeID string `json:"node_id"`
	}
	if err := json.Unmarshal(hit.Source, &source); err != nil {
		t.Fatalf("decode trace hit %s: %v", *hit.ID, err)
	}
	nodeID, err := uuid.Parse(source.NodeID)
	if err != nil {
		t.Fatalf("parse trace hit node ID %q: %v", source.NodeID, err)
	}
	return nodeID
}

func rankedTraceNodes(t *testing.T, hits []opensearchapi.SearchHit) []rankedNode {
	t.Helper()
	seen := map[uuid.UUID]bool{}
	ranked := make([]rankedNode, 0, len(hits))
	for _, hit := range hits {
		if hit.Score == nil {
			t.Fatal("ranked trace hit requires a score")
		}
		nodeID := traceHitNode(t, hit)
		if seen[nodeID] {
			continue
		}
		seen[nodeID] = true
		ranked = append(ranked, rankedNode{ID: nodeID, Score: float32(*hit.Score)})
	}
	return ranked
}

func explanationParts(explanation *opensearchapi.ExplainExplanation, depth int) string {
	if explanation == nil || depth == 0 {
		return ""
	}
	parts := make([]string, 0, len(explanation.Details))
	for _, detail := range explanation.Details {
		description := detail.Description
		if len(description) > traceDescriptionBytes {
			description = description[:traceDescriptionBytes]
		}
		part := fmt.Sprintf("%.4f %q", detail.Value, description)
		if nested := explanationParts(&detail, depth-1); nested != "" {
			part += " {" + nested + "}"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}
