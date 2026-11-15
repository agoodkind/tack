package testenvcli

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"goodkind.io/tack/internal/testenv"
)

// subcommand identifies one of the tool's commands.
type subcommand string

const (
	subcommandLedger       subcommand = "ledger"
	subcommandFoundationDB subcommand = "foundationdb"
	subcommandOpenSearch   subcommand = "opensearch"
	subcommandObjectStore  subcommand = "objectstore"
	subcommandKafka        subcommand = "kafka"
	subcommandStopStore    subcommand = "objectstore-stop"
	subcommandStartStore   subcommand = "objectstore-start"
	subcommandSearch       subcommand = "search-cluster"
	subcommandSearchDrill  subcommand = "search-cluster-drill"
	subcommandSharedDir    subcommand = "shared-dir"
	subcommandDown         subcommand = "down"
)

const usage = "usage: testenv ledger | foundationdb | opensearch | objectstore | kafka | " +
	"objectstore-stop CONTAINER | objectstore-start CONTAINER | search-cluster MEMBERS | " +
	"search-cluster-drill MEMBERS | shared-dir | down | " +
	"run-mac --root ROOT --image ID --evidence-dir DIR -- GO_TEST_FLAGS"

const (
	exitUsage       = 2
	exitInterrupted = 130
	maxArguments    = 2
)

// Run dispatches one developer subcommand and returns its process exit code.
//
// It reads arguments after the executable name, preserves successful engines
// for operator use, and releases failed steps.
//
// For example:
//
//	code := testenvcli.Run([]string{"testenv", "foundationdb"})
func Run(args []string) int {
	return run(args[1:])
}

// ExitMessage formats the process exit code for the command's error log.
//
// The entry point logs this message before exiting with a nonzero status.
// The message includes the numeric status without changing that status.
//
// For example:
//
//	message := testenvcli.ExitMessage(2)
func ExitMessage(code int) string {
	return "exit status " + strconv.Itoa(code)
}

func run(args []string) int {
	if len(args) > 0 && args[0] == "run-mac" {
		return runMac(args[1:])
	}

	if len(args) == 0 || len(args) > maxArguments {
		_, _ = fmt.Fprintln(os.Stderr, usage)

		return exitUsage
	}

	command := subcommand(args[0])
	argument := ""
	if len(args) == maxArguments {
		argument = args[1]
	}

	takesArgument := command == subcommandStopStore || command == subcommandStartStore ||
		command == subcommandSearch || command == subcommandSearchDrill
	if (argument != "") != takesArgument {
		_, _ = fmt.Fprintln(os.Stderr, usage)

		return exitUsage
	}

	slog.Info("testenv.start", slog.String("command", args[0]), slog.String("argument", argument))
	switch command {
	case subcommandLedger:
		return runStep(func(step *cliStep) { _, _ = fmt.Fprintln(os.Stdout, testenv.Ledger(step)) })
	case subcommandFoundationDB:
		return runStep(
			func(step *cliStep) { _, _ = fmt.Fprintln(os.Stdout, testenv.FoundationDB(step)) },
		)
	case subcommandOpenSearch:
		return runStep(func(step *cliStep) {
			engine := testenv.OpenSearch(step)
			_, _ = fmt.Fprintln(os.Stdout, engine.Endpoint, engine.Username, engine.Password)
			_, _ = fmt.Fprint(os.Stdout, engine.CA)
		})
	case subcommandObjectStore:
		return runStep(func(step *cliStep) {
			store := testenv.ObjectStore(step)
			_, _ = fmt.Fprintln(
				os.Stdout,
				store.Endpoint,
				store.Bucket,
				store.AccessKey,
				store.SecretKey,
				store.ReadOnlyAccessKey,
				store.ReadOnlySecretKey,
				store.Container,
			)
		})
	case subcommandKafka:
		return runStep(func(step *cliStep) { _, _ = fmt.Fprintln(os.Stdout, testenv.Kafka(step)) })
	case subcommandStopStore:
		return runStep(func(step *cliStep) { testenv.StopObjectStore(step, argument) })
	case subcommandStartStore:
		return runStep(func(step *cliStep) {
			_, _ = fmt.Fprintln(os.Stdout, testenv.StartObjectStore(step, argument))
		})
	case subcommandSearch, subcommandSearchDrill:
		members, err := strconv.Atoi(argument)
		if err != nil || members < 1 {
			_, _ = fmt.Fprintln(os.Stderr, usage)

			return exitUsage
		}

		return runStep(func(step *cliStep) {
			cluster := startSearchCluster(step, members)
			if command == subcommandSearchDrill {
				drillSearchCluster(step, cluster)
			}
		})
	case subcommandSharedDir:
		return runStep(
			func(step *cliStep) { _, _ = fmt.Fprintln(os.Stdout, testenv.SharedDir(step)) },
		)
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

		return exitUsage
	}
}
