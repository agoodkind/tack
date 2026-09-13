package ops_test

import "testing"

// TestLedgerNodeCommandsAreReachableFromTheCommandLine pins that the deploy can
// run both halves of the ledger restart through the rendered tree, under the
// group and names the configs playbook calls, with the wait's windows as flags
// and nothing shadowing the global --execute the audit choke-point reads.
func TestLedgerNodeCommandsAreReachableFromTheCommandLine(t *testing.T) {
	prepare := findCommand(t, "ops", "ledger", "node-prepare")
	if prepare.LocalNonPersistentFlags().Lookup("execute") != nil {
		t.Fatal("node-prepare declares its own --execute, which shadows the audit gate")
	}

	wait := findCommand(t, "ops", "ledger", "node-wait")
	stall := wait.Flags().Lookup("stall")
	if stall == nil || stall.DefValue != "5m0s" {
		t.Fatalf("node-wait --stall = %v, want a 5m0s default", stall)
	}
	poll := wait.Flags().Lookup("poll")
	if poll == nil || poll.DefValue != "10s" {
		t.Fatalf("node-wait --poll = %v, want a 10s default", poll)
	}
}
