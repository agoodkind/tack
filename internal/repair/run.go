package repair

import (
	"fmt"
	"os"

	"goodkind.io/tack/internal/config"
)

// Run dispatches the standalone repair CLI without leaking repair orchestration into cmd/server.
func Run(cfg *config.Config, args []string) {
	if len(args) == 0 {
		PrintUsage()
		return
	}
	if args[0] == "help" {
		if len(args) == 1 {
			PrintUsage()
			return
		}
		PrintCommandUsage(args[1])
		return
	}
	if args[0] == "list" || args[0] == "classes" {
		PrintClasses()
		return
	}
	switch args[0] {
	case "read":
		runRead(cfg, args[1:])
	case "find":
		runFind(cfg, args[1:])
	case "query":
		runQuery(cfg, args[1:])
	case "verify":
		runVerify(cfg, args[1:])
	case "validate":
		runValidate(cfg, args[1:])
	case "preview":
		RunPreview(cfg, args[1:])
	case "apply":
		RunApply(cfg, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown repair command: %s\n\n", args[0])
		PrintUsage()
		os.Exit(2)
	}
}
