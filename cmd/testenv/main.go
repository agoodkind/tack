// Command testenv is a developer tool that starts the engines internal/testenv
// gives tests, for use outside a test binary, and removes them again.
//
//	testenv ledger        start a YugabyteDB ledger, migrate it, print its DSN
//	testenv foundationdb  start a single-node FoundationDB, print its cluster file
//	testenv opensearch    start a TLS OpenSearch engine and print its endpoint,
//	                      username, password, and CA certificate
//	testenv objectstore   start a SeaweedFS object store, create a bucket, and
//	                      print its endpoint, bucket, keys, and container
//	testenv objectstore-stop CONTAINER   stop that object store, as its guest stops
//	testenv objectstore-start CONTAINER  start it again and print its endpoint
//	testenv search-cluster MEMBERS  start MEMBERS OpenSearch members behind a
//	                      Traefik proxy and print the proxy endpoint, login,
//	                      each member's proxy status, and the CA certificate
//	testenv search-cluster-drill MEMBERS  start that cluster, then stop and
//	                      restart each member in turn and print the proxy
//	                      status while each member is down
//	testenv shared-dir    create a directory the Docker daemon sees at the same path, print it
//	testenv down          remove every engine the tool or any test started
//
// An engine the tool starts keeps running after it exits, for the operator to
// use, until `testenv down` removes it. A shared directory stays until the
// operator deletes it. The tool is not an `./server ops`
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
	"search-cluster-drill MEMBERS | shared-dir | down"

func main() {
	code := run(os.Args[1:])
	if code != 0 {
		slog.Error("testenv.exited", slog.String("err", "exit status "+strconv.Itoa(code)))
		os.Exit(code)
	}
}

// run dispatches one subcommand and returns the process exit code.
func run(args []string) int {
	if len(args) == 0 || len(args) > 2 {
		_, _ = fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	command := subcommand(args[0])
	argument := ""
	if len(args) == 2 {
		argument = args[1]
	}
	takesArgument := command == subcommandStopStore || command == subcommandStartStore ||
		command == subcommandSearch || command == subcommandSearchDrill
	if (argument != "") != takesArgument {
		_, _ = fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	slog.Info("testenv.start", slog.String("command", args[0]), slog.String("argument", argument))
	switch command {
	case subcommandLedger:
		return runStep(func(step *cliStep) { _, _ = fmt.Println(testenv.Ledger(step)) })
	case subcommandFoundationDB:
		return runStep(func(step *cliStep) { _, _ = fmt.Println(testenv.FoundationDB(step)) })
	case subcommandOpenSearch:
		return runStep(func(step *cliStep) {
			engine := testenv.OpenSearch(step)
			_, _ = fmt.Println(engine.Endpoint, engine.Username, engine.Password)
			_, _ = fmt.Print(engine.CA)
		})
	case subcommandObjectStore:
		return runStep(func(step *cliStep) {
			store := testenv.ObjectStore(step)
			_, _ = fmt.Println(store.Endpoint, store.Bucket, store.AccessKey, store.SecretKey,
				store.ReadOnlyAccessKey, store.ReadOnlySecretKey, store.Container)
		})
	case subcommandKafka:
		return runStep(func(step *cliStep) { _, _ = fmt.Println(testenv.Kafka(step)) })
	case subcommandStopStore:
		return runStep(func(step *cliStep) { testenv.StopObjectStore(step, argument) })
	case subcommandStartStore:
		return runStep(func(step *cliStep) { _, _ = fmt.Println(testenv.StartObjectStore(step, argument)) })
	case subcommandSearch, subcommandSearchDrill:
		members, err := strconv.Atoi(argument)
		if err != nil || members < 1 {
			_, _ = fmt.Fprintln(os.Stderr, usage)
			return 2
		}
		return runStep(func(step *cliStep) {
			cluster := startSearchCluster(step, members)
			if command == subcommandSearchDrill {
				drillSearchCluster(step, cluster)
			}
		})
	case subcommandSharedDir:
		return runStep(func(step *cliStep) { _, _ = fmt.Println(testenv.SharedDir(step)) })
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
