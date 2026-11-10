package node

import (
	"unicode/utf8"

	"github.com/google/uuid"
)

// SummaryStatus is the current source state of one requested node.
type SummaryStatus string

const (
	// SummaryFound means the node exists and AccessKeys lists its current keys.
	SummaryFound SummaryStatus = "found"
	// SummaryDeleted means the node no longer exists in FoundationDB.
	SummaryDeleted SummaryStatus = "deleted"
)

// Summary supplies a node's ID, node type, and bounded name for search pages.
type Summary struct {
	ID       uuid.UUID
	NodeType string
	Name     string
}

// SummaryResult contains one node's current summary and opaque access keys.
// A deleted node does not have summary values or access keys.
type SummaryResult struct {
	NodeID     uuid.UUID
	Status     SummaryStatus
	Summary    Summary
	AccessKeys []string
}

// TruncateUTF8 returns the longest prefix within maxBytes that ends on a
// rune boundary.
//
// The function shortens the prefix when the byte limit splits a rune.
//
//   - Text within the byte limit remains unchanged.
//   - A nonpositive byte limit returns an empty string.
func TruncateUTF8(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}

	end := max(maxBytes, 0)
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}

	return text[:end]
}
