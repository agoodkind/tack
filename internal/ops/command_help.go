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
	fmt.Fprintln(os.Stderr, "  preview   Preview a targeted repair for one node")
	fmt.Fprintln(os.Stderr, "  apply     Apply a previously previewed repair after explicit confirmation")
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
		fmt.Fprintln(os.Stderr, "usage: ./server ops repair preview --node <uuid> [--class <repair-class>]")
	case "apply":
		fmt.Fprintln(os.Stderr, "usage: ./server ops repair apply --node <uuid> --actor <uuid> --confirm <token> [--class <repair-class>] --yes")
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

func printBatchUsage() {
	fmt.Fprintln(os.Stderr, "usage: ./server ops batch <registered-op>")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "available batch ops:")
	for _, op := range List() {
		if strings.HasPrefix(op.Name, "repair.") {
			continue
		}
		fmt.Fprintf(os.Stderr, "  %-32s %s\n", op.Name, op.Description)
	}
}
