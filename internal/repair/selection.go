package repair

import (
	"os"

	"github.com/google/uuid"
)

// Selection captures the optional org- or node-scoped target parsed from CLI flags.
type Selection struct {
	NodeID *uuid.UUID
	OrgID  *uuid.UUID
}

func ParseSelection(command string, argv []string) Selection {
	flagSet := newFlagSet(command)
	nodeIDRaw := flagSet.String("node", "", "node UUID")
	orgIDRaw := flagSet.String("org", "", "org UUID")
	if err := flagSet.Parse(argv); err != nil {
		os.Exit(2)
	}
	selection := Selection{}
	if *nodeIDRaw != "" && *orgIDRaw != "" {
		failUsage(command, "%s: --node and --org are mutually exclusive", command)
	}
	if *nodeIDRaw != "" {
		nodeID := mustParseUUID(command, "--node", *nodeIDRaw)
		selection.NodeID = &nodeID
	}
	if *orgIDRaw != "" {
		orgID := mustParseUUID(command, "--org", *orgIDRaw)
		selection.OrgID = &orgID
	}
	return selection
}
