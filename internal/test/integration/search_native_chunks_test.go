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
	coveredBytes := 0
	nextSearchByte := 0
	for number, chunk := range chunks {
		if chunk.Text == "" || utf8.RuneCountInString(chunk.Text) > 160 {
			t.Fatalf("chunk %d has invalid character length", number)
		}
		position := strings.Index(source[nextSearchByte:], chunk.Text)
		if position < 0 {
			t.Fatalf("chunk %d does not occur in the source", number)
		}
		position += nextSearchByte
		if position > coveredBytes {
			t.Fatalf("source bytes %d through %d are not covered", coveredBytes, position)
		}
		if end := position + len(chunk.Text); end > coveredBytes {
			coveredBytes = end
		}
		nextSearchByte = position + 1
		if len(chunk.Embedding) == 0 {
			t.Fatalf("chunk %d has no sparse weights", number)
		}
		for term, weight := range chunk.Embedding {
			if term == "" || math.IsNaN(weight) || math.IsInf(weight, 0) || weight < 0 {
				t.Fatalf("chunk %d has an invalid sparse weight", number)
			}
		}
	}
	if coveredBytes != len(source) {
		t.Fatalf("native chunks cover %d of %d source bytes", coveredBytes, len(source))
	}
}
