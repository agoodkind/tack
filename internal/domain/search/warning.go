package search

import "goodkind.io/tack/internal/domain/node"

// WarningPage contains one page of verified property warnings in key order.
type WarningPage struct {
	Warnings   []node.PropertyDiagnostic
	NextCursor string
	Done       bool
}
