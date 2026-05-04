package repair

import (
	"fmt"
	"os"
	"strings"
)

func PrintUsage() {
	fmt.Fprintln(os.Stderr, "usage: ./server repair <command> [flags]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "inspection commands:")
	fmt.Fprintln(os.Stderr, "  read       Read one node, including resolve, view, and primary payloads")
	fmt.Fprintln(os.Stderr, "  find       List node views by org and optional node type")
	fmt.Fprintln(os.Stderr, "  query      Filter node views by indexed property equality")
	fmt.Fprintln(os.Stderr, "  verify     Check raw record-family consistency for one node")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "repair commands:")
	fmt.Fprintln(os.Stderr, "  classes    List supported repair classes")
	fmt.Fprintln(os.Stderr, "  validate   Check whether a repair class is applicable to one node")
	fmt.Fprintln(os.Stderr, "  preview   Preview stray-alias repair for one node, showing the exact repair plan and confirmation token")
	fmt.Fprintln(os.Stderr, "  apply      Apply a previously previewed repair after explicit confirmation")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "run `./server repair help <command>` for command-specific usage")
}

func PrintClasses() {
	fmt.Fprintln(os.Stderr, "supported repair classes:")
	for _, info := range RepairClasses() {
		fmt.Fprintf(os.Stderr, "  %-24s %s\n", info.Class, info.Description)
	}
}

func PrintCommandUsage(command string) {
	switch strings.TrimSpace(command) {
	case "read":
		fmt.Fprintln(os.Stderr, "usage: ./server repair read --node <uuid>")
	case "find":
		fmt.Fprintln(os.Stderr, "usage: ./server repair find --org <uuid> [--type <type>] [--limit N]")
	case "query":
		fmt.Fprintln(os.Stderr, "usage: ./server repair query --org <uuid> --property <name> --value <json> [--type <type>] [--limit N]")
	case "verify":
		fmt.Fprintln(os.Stderr, "usage: ./server repair verify --node <uuid> [--class <repair-class>] [--records]")
	case "validate":
		fmt.Fprintln(os.Stderr, "usage: ./server repair validate --node <uuid> [--class <repair-class>]")
	case "preview":
		fmt.Fprintln(os.Stderr, "usage: ./server repair preview --node <uuid> [--class <repair-class>]")
	case "apply":
		fmt.Fprintln(os.Stderr, "usage: ./server repair apply --node <uuid> --actor <uuid> --confirm <token> [--class <repair-class>] --yes")
	case "classes", "list":
		fmt.Fprintln(os.Stderr, "usage: ./server repair classes")
	default:
		fmt.Fprintf(os.Stderr, "unknown repair command: %s\n\n", command)
		PrintUsage()
		os.Exit(2)
	}
}
