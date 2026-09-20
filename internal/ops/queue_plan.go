package ops

import "errors"

// The replica placement the audit queue's topics take when an operator raises
// their copy count (TACK-409). The planner is arithmetic on broker ids. It
// makes no cluster call, and the placement it produces is checkable on its own.

var (
	// errQueueNoPartitions rejects a plan for a topic with no partitions.
	errQueueNoPartitions = errors.New("partition count must be above zero")
	// errQueueNoReplicas rejects a plan that would place no copy of a partition.
	errQueueNoReplicas = errors.New("replica count must be above zero")
	// errQueueNoBrokerIDs rejects a plan with no broker to place a copy on.
	errQueueNoBrokerIDs = errors.New("no broker ids to place replicas on")
	// errQueueTooFewBrokers rejects a copy count above the broker count. Two
	// copies of one partition on one broker survive that broker's loss no
	// better than one copy does.
	errQueueTooFewBrokers = errors.New("replica count is above the broker count")
	// errQueueDuplicateBroker rejects a broker id listed twice. The planner
	// walks the list positionally. A repeated id would place two copies of one
	// partition on the same broker.
	errQueueDuplicateBroker = errors.New("broker id appears more than once")
)

// PlanReplicaAssignment lays out which brokers hold each partition of a topic.
// Partition p takes the requested number of broker ids starting at index p
// modulo the broker count and walking forward. Leadership then spreads evenly
// across the brokers, and each partition names distinct brokers.
func PlanReplicaAssignment(partitionCount int, brokerIDs []int32, replicas int) ([][]int32, error) {
	brokerCount := len(brokerIDs)
	if partitionCount <= 0 {
		return nil, errQueueNoPartitions
	}
	if replicas <= 0 {
		return nil, errQueueNoReplicas
	}
	if brokerCount == 0 {
		return nil, errQueueNoBrokerIDs
	}
	if replicas > brokerCount {
		return nil, errQueueTooFewBrokers
	}
	if err := rejectDuplicateBrokerIDs(brokerIDs); err != nil {
		return nil, err
	}
	assignment := make([][]int32, 0, partitionCount)
	for partition := range partitionCount {
		placement := make([]int32, 0, replicas)
		for offset := range replicas {
			placement = append(placement, brokerIDs[(partition+offset)%brokerCount])
		}
		assignment = append(assignment, placement)
	}
	return assignment, nil
}

// rejectDuplicateBrokerIDs refuses a broker list holding the same id twice.
func rejectDuplicateBrokerIDs(brokerIDs []int32) error {
	seen := make(map[int32]bool, len(brokerIDs))
	for _, brokerIdentifier := range brokerIDs {
		if seen[brokerIdentifier] {
			return errQueueDuplicateBroker
		}
		seen[brokerIdentifier] = true
	}
	return nil
}
