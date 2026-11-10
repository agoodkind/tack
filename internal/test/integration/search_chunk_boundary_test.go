package integration_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"

	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// boundaryEmoji is U+1F642. UTF-16 stores it as the surrogate pair D83D
	// DE42.
	boundaryEmoji = "🙂"
	// boundaryChunkUnits is the char_limit of the page_text fixed_char_length
	// chunker. OpenSearch counts this limit in UTF-16 code units.
	boundaryChunkUnits    = 160
	boundaryNodeName      = "Surrogate boundary node"
	boundaryBeforeWord    = "zephyrquill"
	boundaryAfterWord     = "marigoldvortex"
	boundaryFinalWord     = "obsidianlattice"
	boundaryExcludedValue = "EXCLUDED-SECRET-VALUE"
)

// TestSearchIndexesEmojiAcrossChunkBoundaries indexes one node with a page
// text that puts an emoji surrogate pair across each of the first two chunk
// boundaries, at UTF-16 units 159 and 160 and at units 319 and 320. The
// worker uses the production 4,096-byte page bound, and the node indexes as
// one page. The test logs the generated chunk text as evidence and does not
// check it. Indexing must finish without a worker slice failure. ops search
// verify must not list an exclusion or stuck work for the node. The indexed
// page_text must equal the rendered text. tack_search must return the node
// for the word before the first emoji, the word after it, and the word after
// the second emoji.
func TestSearchIndexesEmojiAcrossChunkBoundaries(t *testing.T) {
	text := boundaryPageText()
	offsets := emojiUTF16Offsets(text)
	wantOffsets := []int{boundaryChunkUnits - 1, 2*boundaryChunkUnits - 1}
	if !slices.Equal(offsets, wantOffsets) {
		t.Fatalf("emoji UTF-16 offsets = %v, want %v", offsets, wantOffsets)
	}
	fixture := integration.NewQueryFixture(t)
	workspace := fixture.Workspaces[0]
	kind := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	parent := integration.EntryPoint(t, fixture, workspace)
	included := strings.TrimSuffix(strings.TrimPrefix(text, boundaryNodeName+"\n"), "\n")
	nodeID := integration.PutOpaqueNode(t, fixture, kind, parent, boundaryNodeName, included, boundaryExcludedValue)
	t.Logf("node %s page has %d UTF-16 units; the emoji start at units %v", nodeID, utf16Units(text), offsets)

	worker := integration.NewSearchWorker(t, fixture, searchdomain.MaxPageBytes)
	integration.DrainSearchWork(t, worker, 2000)
	documents := integration.SearchNodePages(t, fixture, nodeID)
	logBoundaryChunks(t, documents)

	report, verifyErr := integration.RunSearchVerifyCommand(t, fixture.Config)
	t.Logf("ops search verify physical checks: %v", verifyErr)
	for _, exclusion := range report.Exclusions {
		if exclusion.NodeID == nodeID.String() {
			t.Fatalf("ops search verify lists node %s as excluded: %s", nodeID, exclusion.Reason)
		}
	}
	for _, stuck := range report.StuckWork {
		if stuck.ItemID == nodeID.String() {
			t.Fatalf("ops search verify lists stuck work for node %s: %+v", nodeID, stuck)
		}
	}
	if len(documents) != 1 || documents[0].PageText == nil || *documents[0].PageText != text {
		t.Fatalf("node %s has %d active page documents, want one with the rendered boundary text", nodeID, len(documents))
	}
	for _, word := range []string{boundaryBeforeWord, boundaryAfterWord, boundaryFinalWord} {
		results := integration.CallEverySearchPage(t, word, fixture.Harness)
		if !slices.Contains(results, nodeID) {
			t.Fatalf("tack_search %q returned %v, want node %s", word, results, nodeID)
		}
	}
}

// boundaryPageText returns the rendered search text of the boundary node:
// the name line and one included value line. The high surrogate of the first
// emoji is the last UTF-16 unit of chunk 0, and the high surrogate of the
// second emoji is the last UTF-16 unit of chunk 1.
func boundaryPageText() string {
	var text strings.Builder
	text.WriteString(boundaryNodeName + "\n" + boundaryBeforeWord + " ")
	padUTF16(&text, boundaryChunkUnits-2)
	text.WriteString(" " + boundaryEmoji + " " + boundaryAfterWord + " ")
	padUTF16(&text, 2*boundaryChunkUnits-2)
	text.WriteString(" " + boundaryEmoji + " " + boundaryFinalWord + "\n")
	return text.String()
}

// padUTF16 appends the letter x until text has units UTF-16 code units.
func padUTF16(text *strings.Builder, units int) {
	for utf16Units(text.String()) < units {
		text.WriteByte('x')
	}
}

// utf16Units returns the number of UTF-16 code units in text.
func utf16Units(text string) int {
	return len(utf16.Encode([]rune(text)))
}

// emojiUTF16Offsets returns the UTF-16 offset of each boundaryEmoji in text.
func emojiUTF16Offsets(text string) []int {
	offsets := []int{}
	start := 0
	for {
		found := strings.Index(text[start:], boundaryEmoji)
		if found < 0 {
			return offsets
		}
		offsets = append(offsets, utf16Units(text[:start+found]))
		start += found + len(boundaryEmoji)
	}
}

// boundaryChunk keeps the generated chunk text as the raw JSON that
// OpenSearch returns, including any \u escape.
type boundaryChunk struct {
	Text json.RawMessage `json:"text"`
}

// logBoundaryChunks logs the raw JSON text of each generated chunk of
// documents. The log shows where the chunker split each surrogate pair.
func logBoundaryChunks(t *testing.T, documents []integration.SearchPageSource) {
	t.Helper()
	for _, document := range documents {
		var chunks []boundaryChunk
		if err := json.Unmarshal(document.Semantic, &chunks); err != nil {
			var nested struct {
				Chunks []boundaryChunk `json:"chunks"`
			}
			if err := json.Unmarshal(document.Semantic, &nested); err != nil {
				t.Logf("page %d generated semantic info does not decode as chunks: %v", document.PageOrdinal, err)
				continue
			}
			chunks = nested.Chunks
		}
		t.Logf("page %d has %d generated chunks", document.PageOrdinal, len(chunks))
		for number, chunk := range chunks {
			t.Logf("page %d chunk %d raw text %s", document.PageOrdinal, number, chunk.Text)
		}
	}
}
