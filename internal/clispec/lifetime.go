package clispec

import "time"

// BackfillNamePrefix starts the terminal name of every backfill. The prefix
// is derived from the lifetime, never typed by hand.
const BackfillNamePrefix = "once-"

// Lifetime says whether a command is part of the product or is a backfill: a
// one-time job that exists for one ticket and is deleted, with its tests, by
// a removal day. Every Operation states its lifetime: Permanent for a product
// command, or a literal naming the ticket and the removal day for a backfill.
// Write RemoveBy as a [time.Date] literal, because cmd/backfillcheck reads it
// from the source at build time and fails `make build` once that day has
// passed or when the value is not a literal.
type Lifetime struct {
	Ticket   string
	RemoveBy time.Time
}

// Permanent is the lifetime of a command that is part of the product.
var Permanent Lifetime

func (l Lifetime) isBackfill() bool {
	return l.Ticket != "" || !l.RemoveBy.IsZero()
}

// terminalName is the command word cobra renders: the canonical CLI name,
// prefixed for a backfill.
func (op Operation[I]) terminalName() string {
	if op.Lifetime.isBackfill() {
		return BackfillNamePrefix + op.Name.CLI()
	}
	return op.Name.CLI()
}

// backfillGroups holds one rendered `backfill` group per parent group, so
// every backfill sits apart from the product commands of its family.
type backfillGroups map[*Group]*Group

func (b backfillGroups) under(parent *Group) *Group {
	if group, ok := b[parent]; ok {
		return group
	}
	group := &Group{
		Use:    "backfill",
		Short:  "One-time jobs, each deleted by its removal day",
		Long:   "",
		Parent: parent,
	}
	b[parent] = group
	return group
}

// renderParent returns the group a command attaches to: its own group for a
// product command, or the backfill group under that group for a backfill.
func (op Operation[I]) renderParent(backfills backfillGroups) *Group {
	if op.Lifetime.isBackfill() {
		return backfills.under(op.Group)
	}
	return op.Group
}
