package ops

import (
	"context"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// storeGroup defines cluster configuration commands for distributed storage
// (TACK-408). Migration starts new processes, raises redundancy, replaces
// coordinators with pinned data-guest addresses, and excludes old processes.
var storeGroup = &clispec.Group{
	Use:    "store",
	Short:  "Read and change the product store's cluster configuration",
	Long:   "",
	Parent: opsGroup,
}

// storeRedundancyInput selects a mode. Empty uses TACK_OPS_FDB_REDUNDANCY_MODE.
type storeRedundancyInput struct {
	clispec.InputMarker
	Mode string
}

// storeAddressesInput lists pinned guest addresses separated by commas.
type storeAddressesInput struct {
	clispec.InputMarker
	Addresses string
}

// storeStatusOp declares `ops store status`.
func storeStatusOp(f *cli.Factory) clispec.Operation[noInput] {
	return clispec.Operation[noInput]{
		Name:     clispec.Name{Canonical: "status", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    singletonReadSpec(audit.VerbOpsStoreStatus, "store/status"),
		Group:    storeGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Print the product store's cluster status",
		Long: "Print the cluster's status report verbatim, including process count, " +
			"redundancy mode, coordinators, fault tolerance, and backup sessions. " +
			"Validate the distributed store against this observed state, not deployment intent.",
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
		Long: "double stores two copies and tolerates one data-guest failure. " +
			"triple requires all three guests on a three-guest cluster; one guest's " +
			"failure causes an outage. single stores one copy for local or single-guest " +
			"environments. The cluster continues serving requests during background " +
			"replication. Without --mode, the command uses TACK_OPS_FDB_REDUNDANCY_MODE.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[storeRedundancyInput]{
			clispec.StringParam(
				"mode",
				"single, double, or triple (default: this environment's configured mode)",
				"",
				false,
				func(in *storeRedundancyInput, v string) { in.Mode = v },
			),
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
		Long: "Supply an odd number of pinned data-guest addresses from the service " +
			"inventory, separated by commas. The optional port defaults to 4500. " +
			"Coordinators require an odd count for quorum decisions. The cluster " +
			"updates connected clients' writable cluster files without a restart. " +
			"The command refuses container addresses and names. Container addresses " +
			"change on recreation, and site DNS resolves names to the wildcard proxy.",
		Examples: nil,
		Args:     nil,
		Params: storeAddressParams(
			"comma-separated pinned guest addresses, for example " +
				"3d06:bad:b01:210::220,3d06:bad:b01:210::221,3d06:bad:b01:210::222",
		),
		New: newStoreAddressesInput,
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
		Short:    "Exclude store processes after replicating their keys elsewhere",
		Long: "The cluster replicates each excluded process's keys on other " +
			"addresses before the command returns. Stop the processes only after " +
			"the command succeeds. This operation retires the original guest's " +
			"store process during relocation. The command continues waiting while " +
			"an excluded address stores the last copy of a key.",
		Examples: nil,
		Args:     nil,
		Params: storeAddressParams(
			"comma-separated addresses of the processes to take out of service",
		),
		New: newStoreAddressesInput,
		Run: func(ctx context.Context, in storeAddressesInput, sink clispec.ResultSink) error {
			return RunStoreExclude(ctx, f.Cfg, in.Addresses, sink)
		},
	}
}

func storeAddressParams(help string) []clispec.Param[storeAddressesInput] {
	return []clispec.Param[storeAddressesInput]{
		clispec.StringParam(
			"addresses",
			help,
			"",
			true,
			func(input *storeAddressesInput, value string) { input.Addresses = value },
		),
	}
}

func newStoreAddressesInput() storeAddressesInput {
	return storeAddressesInput{InputMarker: clispec.InputMarker{}, Addresses: ""}
}
