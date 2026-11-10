package integration

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
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

// requireNativeChunks requires the generated chunks to cover source in order
// with no gap and no overlap. Each chunk must be valid UTF-8 of at most 160
// characters and have finite sparse weights, nonempty unless the chunk is
// whitespace only.
func requireNativeChunks(t *testing.T, semantic json.RawMessage, source string) {
	t.Helper()
	chunks := decodeNativeChunks(t, semantic)
	if len(chunks) == 0 {
		t.Fatalf("native chunks are absent for a %d-byte source", len(source))
	}
	covered := 0
	for number, chunk := range chunks {
		if chunk.Text == "" || !utf8.ValidString(chunk.Text) || utf8.RuneCountInString(chunk.Text) > 160 {
			t.Fatalf("chunk %d has invalid text or more than 160 characters: %q", number, chunk.Text)
		}
		if !strings.HasPrefix(source[covered:], chunk.Text) {
			t.Fatalf("chunk %d %q does not start at source byte %d, the end of the previous chunk", number, chunk.Text, covered)
		}
		covered += len(chunk.Text)
		// A whitespace-only chunk has no model tokens, so its embedding may
		// be empty but must still exist.
		if chunk.Embedding == nil || len(chunk.Embedding) == 0 && strings.TrimSpace(chunk.Text) != "" {
			t.Fatalf("chunk %d %q has no sparse weights", number, chunk.Text)
		}
		for term, weight := range chunk.Embedding {
			if term == "" || math.IsNaN(weight) || math.IsInf(weight, 0) || weight < 0 {
				t.Fatalf("chunk %d has an invalid sparse weight", number)
			}
		}
	}
	if covered != len(source) {
		t.Fatalf("native chunks cover %d of %d source bytes", covered, len(source))
	}
}

// nativeChunkMapping is the part of the stored index mapping that defines
// page_text chunking and the generated embedding field type.
type nativeChunkMapping struct {
	Properties struct {
		PageText struct {
			Chunking []struct {
				Algorithm  string `json:"algorithm"`
				Parameters struct {
					CharLimit     *float64 `json:"char_limit"`
					OverlapRate   *float64 `json:"overlap_rate"`
					MaxChunkLimit *float64 `json:"max_chunk_limit"`
				} `json:"parameters"`
			} `json:"chunking"`
		} `json:"page_text"`
		Semantic struct {
			Properties struct {
				Chunks struct {
					Properties struct {
						Embedding struct {
							Type string `json:"type"`
						} `json:"embedding"`
					} `json:"properties"`
				} `json:"chunks"`
			} `json:"properties"`
		} `json:"page_text_semantic_info"`
	} `json:"properties"`
}

// requireNativeChunkMapping reads the stored mapping of index from OpenSearch
// and requires fixed 160-character chunks with 0 overlap, an unlimited chunk
// count (-1), and rank_features embeddings.
func requireNativeChunkMapping(t *testing.T, client *opensearchapi.Client, index string) {
	t.Helper()
	mappingJSON := nativeMapping(t, client, index)
	var mapping nativeChunkMapping
	if err := json.Unmarshal(mappingJSON, &mapping); err != nil {
		t.Fatalf("decode mapping of %s: %v", index, err)
	}
	chunking := mapping.Properties.PageText.Chunking
	if len(chunking) != 1 || chunking[0].Algorithm != "fixed_char_length" {
		t.Fatalf("page_text chunking = %s, want one fixed_char_length chunker", mappingJSON)
	}
	parameters := chunking[0].Parameters
	if parameters.CharLimit == nil || *parameters.CharLimit != 160 || parameters.OverlapRate == nil || *parameters.OverlapRate != 0 ||
		parameters.MaxChunkLimit == nil || *parameters.MaxChunkLimit != -1 {
		t.Fatalf("page_text chunking = %s, want char_limit 160, overlap_rate 0, max_chunk_limit -1", mappingJSON)
	}
	if embeddingType := mapping.Properties.Semantic.Properties.Chunks.Properties.Embedding.Type; embeddingType != "rank_features" {
		t.Fatalf("page_text_semantic_info.chunks.embedding type = %q, want rank_features", embeddingType)
	}
}

func nativeMapping(t *testing.T, client *opensearchapi.Client, index string) json.RawMessage {
	t.Helper()
	response, err := client.Indices.GetMapping(WithTestOperation(t, t.Context()), &opensearchapi.IndicesGetMappingReq{Indices: []string{index}})
	if err != nil {
		t.Fatalf("read mapping of %s: %v", index, err)
	}
	var indices map[string]struct {
		Mappings json.RawMessage `json:"mappings"`
	}
	if err := json.NewDecoder(response.RawBody()).Decode(&indices); err != nil {
		t.Fatal(err)
	}
	entry, exists := indices[index]
	if !exists {
		t.Fatalf("mapping of %s is absent", index)
	}
	return entry.Mappings
}
