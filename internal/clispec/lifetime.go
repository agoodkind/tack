package clispec

import "time"

// BackfillNamePrefix starts every backfill command name.
// The renderer adds the prefix from the command lifetime.
const BackfillNamePrefix = "once-"

// Lifetime distinguishes permanent commands from backfills, which perform
// one-time work for a ticket. RemoveBy sets the deadline for deleting a
// backfill and its tests.
// Every Operation specifies Permanent or a Lifetime literal with a ticket and
// removal date. RemoveBy requires a [time.Date] literal. cmd/backfillcheck
// checks the source during make build and rejects nonliteral or expired dates.
type Lifetime struct {
	Ticket   string
	RemoveBy time.Time
}

// Permanent specifies the lifetime of a product command.
var Permanent Lifetime

func (l Lifetime) isBackfill() bool {
	return l.Ticket != "" || !l.RemoveBy.IsZero()
}

// terminalName returns the CLI verb with the backfill prefix when required.
func (op Operation[OperationInput]) terminalName() string {
	if op.Lifetime.isBackfill() {
		return BackfillNamePrefix + op.Name.CLI()
	}

	return op.Name.CLI()
}

type backfillGroups map[*Group]*Group

func (b backfillGroups) under(parent *Group) *Group {
	if group, ok := b[parent]; ok {
		return group
	}

	group := &Group{
		Use:    "backfill",
		Short:  "Run one-time jobs before their removal dates",
		Long:   "",
		Parent: parent,
	}
	b[parent] = group

	return group
}

// renderParent returns the command's group for a permanent command or its
// nested backfill group for a backfill command.
func (op Operation[OperationInput]) renderParent(backfills backfillGroups) *Group {
	if op.Lifetime.isBackfill() {
		return backfills.under(op.Group)
	}

	return op.Group
}
