package clispec

import "time"

// BackfillNamePrefix starts the terminal name of every backfill. Rendering
// derives the prefix from the lifetime. Do not type the prefix by hand.
const BackfillNamePrefix = "once-"

// Lifetime distinguishes a permanent product command from a backfill, a
// one-time job for one ticket. Delete a backfill and its tests by its removal
// day. Every [Operation] declares [Permanent] or a literal with the ticket
// and removal day.
//
// Write RemoveBy as a [time.Date] literal. The backfillcheck source checker
// fails make build after the removal day or when RemoveBy is not a literal.
type Lifetime struct {
	Ticket   string
	RemoveBy time.Time
}

// Permanent is the lifetime of a command that is part of the product.
var Permanent Lifetime

func (l Lifetime) isBackfill() bool {
	return l.Ticket != "" || !l.RemoveBy.IsZero()
}

// terminalName returns the canonical CLI command word, with the backfill
// prefix when the operation has a backfill lifetime.
func (op Operation[I]) terminalName() string {
	if op.Lifetime.isBackfill() {
		return BackfillNamePrefix + op.Name.CLI()
	}
	return op.Name.CLI()
}

// backfillGroups stores one rendered backfill group per parent group.
// Each family's backfills appear separately from its product commands.
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

// renderParent returns the operation's group for a product command or that
// group's backfill subgroup for a backfill.
func (op Operation[I]) renderParent(backfills backfillGroups) *Group {
	if op.Lifetime.isBackfill() {
		return backfills.under(op.Group)
	}
	return op.Group
}
