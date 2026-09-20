package ops

import (
	"context"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// storeGroup is the parent of the commands that change the product store's
// cluster configuration. Together they are the online migration from one
// process on the owner guest to one process per data guest: join the new
// processes by starting them, raise the redundancy, move the coordinators onto
// the data guests' pinned addresses, then exclude the process that is leaving
// (TACK-408).
var storeGroup = &clispec.Group{
	Use: "store", Short: "Read and change the product store's cluster configuration",
	Long: "", Parent: opsGroup,
}

// storeRedundancyInput is the redundancy word, empty for the environment's own
// TACK_OPS_FDB_REDUNDANCY_MODE.
type storeRedundancyInput struct {
	clispec.InputMarker
	Mode string
}

// storeAddressesInput is a comma-separated list of the guests' pinned
// addresses.
type storeAddressesInput struct {
	clispec.InputMarker
	Addresses string
}

// storeStatusOp declares `ops store status`.
func storeStatusOp(f *cli.Factory) clispec.Operation[noInput] {
	return clispec.Operation[noInput]{
		Name:     clispec.Name{Canonical: "status", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsStoreStatus), Reads: true},
		Group:    storeGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Print what the product store reports about itself",
		Long: "Runs the cluster's own status report and prints it verbatim: the " +
			"process count, the redundancy mode, the coordinators, the fault " +
			"tolerance, and the running backup sessions. Every acceptance " +
			"reading for the distributed store is taken from this output rather " +
			"than from what a deploy intended.",
		Examples: nil,
		Args:     nil,
		Params:   nil,
		New:      func() noInput { return noInput{InputMarker: clispec.InputMarker{}} },
		Run: func(ctx context.Context, _ noInput, sink clispec.ResultSink) error {
			return RunStoreStatus(ctx, f.Cfg, sink)
		},
	}
}

// storeSetRedundancyOp declares `ops store set-redundancy`.
func storeSetRedundancyOp(f *cli.Factory) clispec.Operation[storeRedundancyInput] {
	return clispec.Operation[storeRedundancyInput]{
		Name:     clispec.Name{Canonical: "set-redundancy", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsStoreSetRedundancy), Mutates: true},
		Group:    storeGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Set how many copies of every key the product store keeps",
		Long: "double keeps two copies and survives the loss of one data guest; " +
			"triple needs all three guests live, so it turns one guest's loss " +
			"into an outage on a three-guest cluster; single keeps one copy and " +
			"is the local and single-guest value. The change is online: the " +
			"cluster replicates in the background while it keeps serving. " +
			"Without --mode the command sets TACK_OPS_FDB_REDUNDANCY_MODE.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[storeRedundancyInput]{
			clispec.StringParam("mode", "single, double, or triple (default: this environment's configured mode)",
				"", false, func(in *storeRedundancyInput, v string) { in.Mode = v }),
		},
		New: func() storeRedundancyInput {
			return storeRedundancyInput{InputMarker: clispec.InputMarker{}, Mode: ""}
		},
		Run: func(ctx context.Context, in storeRedundancyInput, sink clispec.ResultSink) error {
			return RunStoreSetRedundancy(ctx, f.Cfg, in.Mode, sink)
		},
	}
}

// storeSetCoordinatorsOp declares `ops store set-coordinators`.
func storeSetCoordinatorsOp(f *cli.Factory) clispec.Operation[storeAddressesInput] {
	return clispec.Operation[storeAddressesInput]{
		Name:     clispec.Name{Canonical: "set-coordinators", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsStoreSetCoordinators), Mutates: true},
		Group:    storeGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Replace the product store's coordinators with the given addresses",
		Long: "Takes a comma-separated list of the data guests' pinned addresses " +
			"from the service inventory, each with an optional port that " +
			"defaults to 4500. The count must be odd, because the coordinators " +
			"decide by quorum. The cluster writes the new list into every " +
			"connected client's cluster file itself, so a client that mounts " +
			"that file writable needs no restart. Container addresses and names " +
			"are refused: a container address disappears on the next recreate, " +
			"and a name leaves for site DNS, where the wildcard record answers " +
			"as the proxy.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[storeAddressesInput]{
			clispec.StringParam("addresses",
				"comma-separated pinned guest addresses, for example 3d06:bad:b01:210::220,3d06:bad:b01:210::221,3d06:bad:b01:210::222",
				"", true, func(in *storeAddressesInput, v string) { in.Addresses = v }),
		},
		New: func() storeAddressesInput {
			return storeAddressesInput{InputMarker: clispec.InputMarker{}, Addresses: ""}
		},
		Run: func(ctx context.Context, in storeAddressesInput, sink clispec.ResultSink) error {
			return RunStoreSetCoordinators(ctx, f.Cfg, in.Addresses, sink)
		},
	}
}

// storeExcludeOp declares `ops store exclude`.
func storeExcludeOp(f *cli.Factory) clispec.Operation[storeAddressesInput] {
	return clispec.Operation[storeAddressesInput]{
		Name:     clispec.Name{Canonical: "exclude", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsStoreExclude), Mutates: true},
		Group:    storeGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Take the named store processes out of service, moving their data first",
		Long: "The cluster moves every copy of a key off the named addresses and " +
			"the command returns only once it has, so the processes can then be " +
			"stopped without losing a copy. This is the step that retires the " +
			"process on the guest the store started on. While an address still " +
			"stores the last copy of a key, the command keeps waiting rather " +
			"than letting that key go.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[storeAddressesInput]{
			clispec.StringParam("addresses", "comma-separated addresses of the processes to take out of service",
				"", true, func(in *storeAddressesInput, v string) { in.Addresses = v }),
		},
		New: func() storeAddressesInput {
			return storeAddressesInput{InputMarker: clispec.InputMarker{}, Addresses: ""}
		},
		Run: func(ctx context.Context, in storeAddressesInput, sink clispec.ResultSink) error {
			return RunStoreExclude(ctx, f.Cfg, in.Addresses, sink)
		},
	}
}
