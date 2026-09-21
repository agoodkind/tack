package ops_test

import (
	"testing"
)

// queueLeafNames are the five commands `ops queue` must expose. A leaf that is
// declared but never registered leaves an operator with no way to run it, which
// is the failure the reference repair shipped with.
var queueLeafNames = []string{
	"status",
	"set-replication",
	"replication-progress",
	"set-min-insync",
	"clear-throttle",
}

// TestQueueLeavesAreReachableFromTheCommandLine walks the rendered tree the
// server builds.
func TestQueueLeavesAreReachableFromTheCommandLine(t *testing.T) {
	for _, leaf := range queueLeafNames {
		command := findCommand(t, "ops", "queue", leaf)
		if command.Name() != leaf {
			t.Fatalf("resolved %q to command %q", leaf, command.Name())
		}
	}
}

// TestQueueSetReplicationDeclaresItsFlags pins the three inputs the copy-count
// change reads.
func TestQueueSetReplicationDeclaresItsFlags(t *testing.T) {
	command := findCommand(t, "ops", "queue", "set-replication")
	for _, flagName := range []string{"topic", "replicas", "throttle-bytes"} {
		if command.Flags().Lookup(flagName) == nil {
			t.Errorf("ops queue set-replication has no --%s", flagName)
		}
	}
}

// TestQueueSetMinInsyncDeclaresItsFlags pins the two inputs the acks change
// reads.
func TestQueueSetMinInsyncDeclaresItsFlags(t *testing.T) {
	command := findCommand(t, "ops", "queue", "set-min-insync")
	for _, flagName := range []string{"topic", "replicas"} {
		if command.Flags().Lookup(flagName) == nil {
			t.Errorf("ops queue set-min-insync has no --%s", flagName)
		}
	}
}

// TestNoQueueLeafShadowsTheExecuteFlag closes the gate a local --execute would
// open. Cobra binds the local flag, the audit choke-point keeps reading false,
// and the command prints its dry run forever.
func TestNoQueueLeafShadowsTheExecuteFlag(t *testing.T) {
	for _, leaf := range queueLeafNames {
		command := findCommand(t, "ops", "queue", leaf)
		if command.LocalNonPersistentFlags().Lookup("execute") != nil {
			t.Errorf("%s declares its own --execute", command.CommandPath())
		}
	}
}
