package ops_test

import "testing"

// TestLedgerNodeCommandsRegisterExecutionAndWaitFlags checks the rendered
// deploy commands and wait defaults. Local flags must not override --execute.
func TestLedgerNodeCommandsRegisterExecutionAndWaitFlags(t *testing.T) {
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
