package node

import "github.com/google/uuid"

// IDPage is one bounded page of node IDs. NextCursor continues the read
// after the last entry the page examined, and Done reports that no entry
// remains. A page can contain fewer IDs than its limit while Done is false.
type IDPage struct {
	IDs        []uuid.UUID
	NextCursor string
	Done       bool
}
