package ops_test

import (
	"slices"
	"testing"

	"goodkind.io/tack/internal/ops"
)

// TestQueuePlanSpreadsSixPartitions pins the exact placement for a
// case small enough to read. Partition 0 starts at the first broker and each
// later partition starts one broker further along.
func TestQueuePlanSpreadsSixPartitions(t *testing.T) {
	assignment, err := ops.PlanReplicaAssignment(6, []int32{1, 2, 3}, 3)
	if err != nil {
		t.Fatalf("PlanReplicaAssignment: %v", err)
	}
	want := [][]int32{
		{1, 2, 3},
		{2, 3, 1},
		{3, 1, 2},
		{1, 2, 3},
		{2, 3, 1},
		{3, 1, 2},
	}
	if len(assignment) != len(want) {
		t.Fatalf("partition count = %d, want %d", len(assignment), len(want))
	}
	for partition := range want {
		if !slices.Equal(assignment[partition], want[partition]) {
			t.Errorf("partition %d = %v, want %v", partition, assignment[partition], want[partition])
		}
	}
}

// TestQueuePlanGivesEveryPartitionDistinctBrokers covers the audit
// topic at its production width. Two copies of one partition on one broker
// would survive that broker's loss no better than one copy.
func TestQueuePlanGivesEveryPartitionDistinctBrokers(t *testing.T) {
	const partitionCount = 256
	const replicaCount = 3
	assignment, err := ops.PlanReplicaAssignment(partitionCount, []int32{11, 12, 13}, replicaCount)
	if err != nil {
		t.Fatalf("PlanReplicaAssignment: %v", err)
	}
	if len(assignment) != partitionCount {
		t.Fatalf("partition count = %d, want %d", len(assignment), partitionCount)
	}
	for partition, placement := range assignment {
		if len(placement) != replicaCount {
			t.Fatalf("partition %d has %d copies, want %d", partition, len(placement), replicaCount)
		}
		seen := map[int32]bool{}
		for _, brokerIdentifier := range placement {
			if seen[brokerIdentifier] {
				t.Fatalf("partition %d places two copies on broker %d", partition, brokerIdentifier)
			}
			seen[brokerIdentifier] = true
		}
	}
}

// TestQueuePlanBalancesLeadership checks the property the modulo
// start exists for: one broker leading far more partitions than another takes
// the produce traffic of the whole topic onto itself.
func TestQueuePlanBalancesLeadership(t *testing.T) {
	brokerIDs := []int32{1, 2, 3}
	assignment, err := ops.PlanReplicaAssignment(256, brokerIDs, 2)
	if err != nil {
		t.Fatalf("PlanReplicaAssignment: %v", err)
	}
	leaderCounts := map[int32]int{}
	for _, placement := range assignment {
		leaderCounts[placement[0]]++
	}
	smallest := len(assignment)
	largest := 0
	for _, brokerIdentifier := range brokerIDs {
		count := leaderCounts[brokerIdentifier]
		if count < smallest {
			smallest = count
		}
		if count > largest {
			largest = count
		}
	}
	if largest-smallest > 1 {
		t.Fatalf("leadership counts span %d to %d, which is more than one apart", smallest, largest)
	}
}

// TestQueuePlanRefusals covers every input the planner rejects. A
// plan built from any of them would place copies the cluster cannot hold.
func TestQueuePlanRefusals(t *testing.T) {
	cases := []struct {
		name           string
		partitionCount int
		brokerIDs      []int32
		replicas       int
	}{
		{name: "no partitions", partitionCount: 0, brokerIDs: []int32{1, 2, 3}, replicas: 2},
		{name: "negative partitions", partitionCount: -4, brokerIDs: []int32{1, 2, 3}, replicas: 2},
		{name: "no replicas", partitionCount: 6, brokerIDs: []int32{1, 2, 3}, replicas: 0},
		{name: "negative replicas", partitionCount: 6, brokerIDs: []int32{1, 2, 3}, replicas: -1},
		{name: "no brokers", partitionCount: 6, brokerIDs: nil, replicas: 1},
		{name: "more replicas than brokers", partitionCount: 6, brokerIDs: []int32{1, 2}, replicas: 3},
		{name: "repeated broker id", partitionCount: 6, brokerIDs: []int32{1, 2, 2}, replicas: 2},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			assignment, err := ops.PlanReplicaAssignment(
				testCase.partitionCount, testCase.brokerIDs, testCase.replicas,
			)
			if err == nil {
				t.Fatalf("no error for %s; plan = %v", testCase.name, assignment)
			}
			if assignment != nil {
				t.Fatalf("a refused plan returned %v, want none", assignment)
			}
		})
	}
}
