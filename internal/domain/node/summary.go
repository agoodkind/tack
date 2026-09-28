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

// Summary is the ID, node type, and bounded name that search displays for
// one node.
type Summary struct {
	ID       uuid.UUID
	NodeType string
	Name     string
}

// SummaryResult is the current summary and opaque access keys of one node.
// A deleted node has an empty summary and no keys.
type SummaryResult struct {
	NodeID     uuid.UUID
	Status     SummaryStatus
	Summary    Summary
	AccessKeys []string
}

// TruncateUTF8 returns the longest prefix of text within maxBytes that ends
// on a rune boundary.
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
