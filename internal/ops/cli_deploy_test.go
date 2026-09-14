package ops_test

import "testing"

// TestDeployVerifyIsReachableFromTheCommandLine pins that the digest check
// still renders under ops deploy after the laptop deploy family was removed,
// with its one flag and nothing shadowing the global --execute.
func TestDeployVerifyIsReachableFromTheCommandLine(t *testing.T) {
	verify := findCommand(t, "ops", "deploy", "verify")
	if verify.LocalNonPersistentFlags().Lookup("execute") != nil {
		t.Fatal("deploy verify declares its own --execute, which shadows the audit gate")
	}
	tag := verify.Flags().Lookup("tag")
	if tag == nil || tag.DefValue != "" {
		t.Fatalf("deploy verify --tag = %v, want an empty default", tag)
	}
	parent := findCommand(t, "ops", "deploy")
	for _, gone := range []string{"build", "push", "pull", "up"} {
		if next, _, err := parent.Find([]string{gone}); err == nil && next != parent {
			t.Fatalf("ops deploy %s still renders; the laptop deploy family was removed", gone)
		}
	}
}
