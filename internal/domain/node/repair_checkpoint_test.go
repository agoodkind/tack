package node_test

import (
	"testing"

	"goodkind.io/tack/internal/domain/node"
)

func TestNodeIntegrityRepairPhaseFollows(t *testing.T) {
	cases := []struct {
		phase    node.RepairPhase
		previous node.RepairPhase
		want     bool
	}{
		{phase: node.RepairPhaseExported, previous: "", want: true},
		{phase: node.RepairPhaseCompleted, previous: node.RepairPhaseExported, want: true},
		{phase: node.RepairPhaseExported, previous: node.RepairPhaseCompleted, want: false},
		{phase: node.RepairPhaseCompleted, previous: "unknown", want: false},
		{phase: "unknown", previous: "", want: false},
	}
	for _, testCase := range cases {
		if got := testCase.phase.Follows(testCase.previous); got != testCase.want {
			t.Errorf("%q.Follows(%q) = %t, want %t", testCase.phase, testCase.previous, got, testCase.want)
		}
	}
}
