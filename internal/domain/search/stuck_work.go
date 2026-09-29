package search

// StuckWork is one work item that keeps retrying after its counted failures
// equal the attempt limit. ItemID is the node, the organization, or the
// index replacement the item works on.
type StuckWork struct {
	Class     WorkClass
	ItemID    string
	Attempts  int64
	LastError string
}

// StuckWorkPage is one bounded read of attempt counts in key order. Work
// lists only the items at or past the attempt limit.
type StuckWorkPage struct {
	Work       []StuckWork
	NextCursor string
	Done       bool
}
