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
//	testenv down          remove operator engines and engines of exited tests
//	testenv run-mac --root ROOT --image ID --evidence-dir DIR -- GO_TEST_FLAGS
//
// Engines keep running until `testenv down` removes them. Shared directories
// remain until the operator deletes them. This tool manages disposable local
// engines, not deployed stores, and does not run as an `./server ops` command.
package main

import (
	"log/slog"
	"os"

	"goodkind.io/tack/internal/testenvcli"
)

func main() {
	code := testenvcli.Run(os.Args)
	if code != 0 {
		slog.Error("testenv.exited", slog.String("err", testenvcli.ExitMessage(code)))
		os.Exit(code)
	}
}
