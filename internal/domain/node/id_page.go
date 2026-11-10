package node

import "github.com/google/uuid"

// IDPage contains one bounded page of node IDs. NextCursor continues the read
// after the last entry that the page examined. Done reports whether the read
// has examined every entry. A page can contain fewer IDs than its limit while
// Done is false.
type IDPage struct {
	IDs        []uuid.UUID
	NextCursor string
	Done       bool
}
