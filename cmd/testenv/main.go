// Command testenv is a developer tool that starts the engines internal/testenv
// gives tests, for use outside a test binary, and removes them again.
//
//	testenv ledger        start a YugabyteDB ledger, migrate it, print its DSN
//	testenv foundationdb  start a single-node FoundationDB, print its cluster file
//	testenv down          remove every engine the tool or any test started
//
// An engine the tool starts keeps running after it exits, for the operator to
// use, until `testenv down` removes it. The tool is not an `./server ops`
// command: it touches no environment's stores, only throwaway local engines.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"goodkind.io/tack/internal/testenv"
)

// subcommand names one of the tool's commands.
type subcommand string

const (
	subcommandLedger       subcommand = "ledger"
	subcommandFoundationDB subcommand = "foundationdb"
	subcommandDown         subcommand = "down"
)

const usage = "usage: testenv ledger | foundationdb | down"

func main() {
	code := run(os.Args[1:])
	if code != 0 {
		slog.Error("testenv.exited", slog.String("err", "exit status "+strconv.Itoa(code)))
		os.Exit(code)
	}
}

// run dispatches one subcommand and returns the process exit code.
func run(args []string) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	slog.Info("testenv.start", slog.String("command", args[0]))
	switch subcommand(args[0]) {
	case subcommandLedger:
		return runStep(func(step *cliStep) { _, _ = fmt.Println(testenv.Ledger(step)) })
	case subcommandFoundationDB:
		return runStep(func(step *cliStep) { _, _ = fmt.Println(testenv.FoundationDB(step)) })
	case subcommandDown:
		return runStep(func(step *cliStep) {
			testenv.RequireDocker(step)
			removed, err := testenv.Down(step.Context())
			if err != nil {
				step.fail(err)
			}
			slog.Info("testenv.down.done", slog.Int("removed", removed))
		})
	default:
		_, _ = fmt.Fprintln(os.Stderr, usage)
		return 2
	}
}
