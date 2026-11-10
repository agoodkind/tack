package ops

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
)

// The coordinator list an operator types becomes the word list in the fdbcli
// command that replaces the cluster's coordinators. A wrong entry there points
// every client at an address that answers nothing, and the store is then
// reachable only by editing each guest's cluster file by hand. These tests
// enter through the exported commands and assert the refusal an operator sees,
// which the command returns before any container starts.

// storeCommandFixture builds the config and the output sink the exported store
// commands take. Both are the production types the CLI hands them.
func storeCommandFixture(t *testing.T) (*config.Config, clispec.ResultSink) {
	t.Helper()
	factory := &cli.Factory{Cfg: nil, In: nil, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	return new(config.Config), clispec.NewCLISink(factory)
}

func TestStoreSetCoordinatorsRefusesAName(t *testing.T) {
	cfg, sink := storeCommandFixture(t)
	err := RunStoreSetCoordinators(context.Background(), cfg, "fdb", sink)
	if err == nil {
		t.Fatal("a name was accepted; site DNS answers every name as the proxy")
	}
	if !strings.Contains(err.Error(), "pinned addresses") {
		t.Fatalf("the refusal said %q, which does not say why a name is refused", err)
	}
}

func TestStoreSetCoordinatorsRefusesAnEvenCount(t *testing.T) {
	cfg, sink := storeCommandFixture(t)
	err := RunStoreSetCoordinators(context.Background(),
		cfg, "3d06:bad:b01:210::220,3d06:bad:b01:210::221", sink)
	if err == nil {
		t.Fatal("an even coordinator count was accepted; a quorum needs an odd one")
	}
	if !strings.Contains(err.Error(), "quorum") {
		t.Fatalf("the refusal said %q, which does not say why an even count is refused", err)
	}
}

func TestStoreSetCoordinatorsRefusesAnEmptyList(t *testing.T) {
	cfg, sink := storeCommandFixture(t)
	if err := RunStoreSetCoordinators(context.Background(), cfg, "  ,  ", sink); err == nil {
		t.Fatal("an empty coordinator list was accepted")
	}
}

func TestStoreExcludeRefusesAnEmptyList(t *testing.T) {
	cfg, sink := storeCommandFixture(t)
	if err := RunStoreExclude(context.Background(), cfg, "  ,  ", sink); err == nil {
		t.Fatal("an empty exclude list was accepted")
	}
}

// The redundancy word reaches fdbcli as part of a command. The command refuses
// anything outside the three modes before it gets there.
func TestStoreSetRedundancyRefusesAWordOutsideTheThreeModes(t *testing.T) {
	for _, mode := range []string{"double ssd", "new double", "DOUBLE"} {
		cfg, sink := storeCommandFixture(t)
		if err := RunStoreSetRedundancy(context.Background(), cfg, mode, sink); err == nil {
			t.Fatalf("mode %q was accepted; it reaches fdbcli as a command", mode)
		}
	}
}

// Without a mode on the command line the command reads
// TACK_OPS_FDB_REDUNDANCY_MODE. An environment that declared none is refused
// rather than configured with a mode nobody named.
func TestStoreSetRedundancyRefusesAnUndeclaredMode(t *testing.T) {
	cfg, sink := storeCommandFixture(t)
	if err := RunStoreSetRedundancy(context.Background(), cfg, "", sink); err == nil {
		t.Fatal("an empty configured mode was accepted")
	}
}
