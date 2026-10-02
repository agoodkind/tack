package integration

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"unicode/utf8"
)

type nativeChunk struct {
	Text      string             `json:"text"`
	Embedding map[string]float64 `json:"embedding"`
}

func decodeNativeChunks(t *testing.T, semantic json.RawMessage) []nativeChunk {
	t.Helper()
	var chunks []nativeChunk
	if err := json.Unmarshal(semantic, &chunks); err != nil {
		var nested struct {
			Chunks []nativeChunk `json:"chunks"`
		}
		if err := json.Unmarshal(semantic, &nested); err != nil {
			t.Fatalf("decode native chunks: %v", err)
		}
		chunks = nested.Chunks
	}
	return chunks
}

func requireNativeChunks(t *testing.T, semantic json.RawMessage, source string) {
	t.Helper()
	chunks := decodeNativeChunks(t, semantic)
	if len(chunks) < 2 {
		t.Fatalf("native chunks = %d, want multiple overlapping chunks", len(chunks))
	}
	positions := map[int]int{-1: 0}
	for number, chunk := range chunks {
		if chunk.Text == "" || utf8.RuneCountInString(chunk.Text) > 160 {
			t.Fatalf("chunk %d has invalid character length", number)
		}
		positions = advanceNativeCoverage(source, chunk.Text, positions)
		if len(positions) == 0 {
			t.Fatalf("chunk %d cannot extend contiguous ordered source coverage", number)
		}
		if len(chunk.Embedding) == 0 {
			t.Fatalf("chunk %d has no sparse weights", number)
		}
		for term, weight := range chunk.Embedding {
			if term == "" || math.IsNaN(weight) || math.IsInf(weight, 0) || weight < 0 {
				t.Fatalf("chunk %d has an invalid sparse weight", number)
			}
		}
	}
	coveredBytes := 0
	for _, end := range positions {
		coveredBytes = max(coveredBytes, end)
	}
	if coveredBytes != len(source) {
		t.Fatalf("native chunks cover %d of %d source bytes", coveredBytes, len(source))
	}
}

// Repeated text can match several source positions. Each retained assignment
// extends coverage without a gap and advances the next chunk's start.
func advanceNativeCoverage(source, text string, previous map[int]int) map[int]int {
	next := make(map[int]int)
	for previousStart, covered := range previous {
		searchStart := previousStart + 1
		for searchStart < len(source) {
			position := strings.Index(source[searchStart:], text)
			if position < 0 {
				break
			}
			position += searchStart
			if position > covered {
				break
			}
			end := position + len(text)
			if end > covered {
				next[position] = end
			}
			searchStart = position + 1
		}
	}
	return next
}
