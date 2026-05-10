package ops

import (
	"fmt"
	"os"
	"strings"
)

func printOpsUsage() {
	fmt.Fprintln(os.Stderr, "usage: ./server ops <family|operation> [...]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "families:")
	fmt.Fprintln(os.Stderr, "  inspect   read, find, and query node/storage state")
	fmt.Fprintln(os.Stderr, "  verify    check node consistency and constraints")
	fmt.Fprintln(os.Stderr, "  validate  check repair applicability for a node")
	fmt.Fprintln(os.Stderr, "  repair    preview and apply targeted repairs")
	fmt.Fprintln(os.Stderr, "  batch     run registered batch maintenance operations")
	fmt.Fprintln(os.Stderr, "  audit     compliance audit subcommands (parity, ...)")
	fmt.Fprintln(os.Stderr, "  backup    snapshot, verify, and restore-test the production datastores")
	fmt.Fprintln(os.Stderr, "  deploy    build, push, and roll the production image to CT 117")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "run `./server ops <family> help` for family-specific usage")
	fmt.Fprintln(os.Stderr)
	printLegacyOpList()
}

func printLegacyOpList() {
	fmt.Fprintln(os.Stderr, "registered batch operations:")
	for _, op := range List() {
		if strings.HasPrefix(op.Name, "repair.") {
			continue
		}
		fmt.Fprintf(os.Stderr, "  %-32s %s\n", op.Name, op.Description)
	}
}

func printInspectUsage() {
	fmt.Fprintln(os.Stderr, "usage: ./server ops inspect <read|find|query> [flags]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "subcommands:")
	fmt.Fprintln(os.Stderr, "  read   Read one node, including resolve, view, and primary payloads")
	fmt.Fprintln(os.Stderr, "  find   List node views by org and optional node type")
	fmt.Fprintln(os.Stderr, "  query  Filter node views by indexed property equality")
}

func printInspectCommandUsage(command string) {
	switch strings.TrimSpace(command) {
	case "read":
		fmt.Fprintln(os.Stderr, "usage: ./server ops inspect read --node <uuid> [--records]")
	case "find":
		fmt.Fprintln(os.Stderr, "usage: ./server ops inspect find --org <uuid> [--type <type>] [--limit N]")
	case "query":
		fmt.Fprintln(os.Stderr, "usage: ./server ops inspect query --org <uuid> --property <name> --value <json> [--type <type>] [--limit N]")
	default:
		fmt.Fprintf(os.Stderr, "unknown inspect command: %s\n\n", command)
		printInspectUsage()
		os.Exit(2)
	}
}

func printVerifyUsage() {
	fmt.Fprintln(os.Stderr, "usage: ./server ops verify node --node <uuid> [--class <repair-class>] [--records]")
}

func printValidateUsage() {
	fmt.Fprintln(os.Stderr, "usage: ./server ops validate node --node <uuid> [--class <repair-class>]")
}

func printRepairUsage() {
	fmt.Fprintln(os.Stderr, "usage: ./server ops repair <classes|preview|apply> [flags]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "subcommands:")
	fmt.Fprintln(os.Stderr, "  classes   List supported repair classes")
	fmt.Fprintln(os.Stderr, "  preview   Preview one node with --profile or many nodes with --manifest")
	fmt.Fprintln(os.Stderr, "  apply     Apply a previewed repair after explicit confirmation")
}

// PrintClasses writes the supported repair classes to stderr for CLI help.
func PrintClasses() {
	fmt.Fprintln(os.Stderr, "supported repair classes:")
	for _, info := range RepairClasses() {
		fmt.Fprintf(os.Stderr, "  %-24s %s\n", info.Class, info.Description)
	}
}

func printRepairCommandUsage(command string) {
	switch strings.TrimSpace(command) {
	case "classes":
		fmt.Fprintln(os.Stderr, "usage: ./server ops repair classes")
	case "preview":
		fmt.Fprintln(os.Stderr, "usage: ./server ops repair preview --node <uuid> --profile <profile.json> [--class <repair-class>]")
		fmt.Fprintln(os.Stderr, "       ./server ops repair preview --manifest <manifest.json> [--class <repair-class>]")
	case "apply":
		fmt.Fprintln(os.Stderr, "usage: ./server ops repair apply --node <uuid> --profile <profile.json> --actor <uuid> --confirm <token> [--class <repair-class>] --yes")
		fmt.Fprintln(os.Stderr, "       ./server ops repair apply --manifest <manifest.json> --actor <uuid> [--class <repair-class>] --yes")
	default:
		fmt.Fprintf(os.Stderr, "unknown repair command: %s\n\n", command)
		printRepairUsage()
		os.Exit(2)
	}
}

func printCommandUsage(command string) {
	switch strings.TrimSpace(command) {
	case "inspect read":
		printInspectCommandUsage("read")
	case "inspect find":
		printInspectCommandUsage("find")
	case "inspect query":
		printInspectCommandUsage("query")
	case "verify node":
		printVerifyUsage()
	case "validate node":
		printValidateUsage()
	case "repair preview":
		printRepairCommandUsage("preview")
	case "repair apply":
		printRepairCommandUsage("apply")
	case "repair classes":
		printRepairCommandUsage("classes")
	default:
		fmt.Fprintf(os.Stderr, "unknown ops command: %s\n\n", command)
		printOpsUsage()
		os.Exit(2)
	}
}

func printAuditUsage() {
	fmt.Fprintln(os.Stderr, "usage: ./server ops audit parity")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "subcommands:")
	fmt.Fprintln(os.Stderr, "  parity  Compare audit.events vs audit.events_v2 over a time window")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "parity env vars:")
	fmt.Fprintln(os.Stderr, "  TACK_PARITY_FROM       inclusive lower bound, RFC3339 UTC (required)")
	fmt.Fprintln(os.Stderr, "  TACK_PARITY_TO         exclusive upper bound, RFC3339 UTC (required)")
	fmt.Fprintln(os.Stderr, "  TACK_PARITY_THRESHOLD  matched-fraction floor in [0,1], default 1.0")
}

func printBatchUsage() {
	fmt.Fprintln(os.Stderr, "usage: ./server ops batch <registered-op>")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "address backfill controls:")
	fmt.Fprintln(os.Stderr, "  TACK_BACKFILL_LIMIT       maximum legacy address rows to inspect, default 1000")
	fmt.Fprintln(os.Stderr, "  TACK_BACKFILL_NODE_ID     optional single-node legacy address preview/apply")
	fmt.Fprintln(os.Stderr, "  TACK_BACKFILL_APPLY=true  required only for backfill.addresses.apply")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "available batch ops:")
	for _, op := range List() {
		if strings.HasPrefix(op.Name, "repair.") {
			continue
		}
		fmt.Fprintf(os.Stderr, "  %-32s %s\n", op.Name, op.Description)
	}
}
