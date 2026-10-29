package service

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"time"

	searchdomain "goodkind.io/tack/internal/domain/search"
)

// classShares divides one worker's time among pending classes in proportion
// to their weights. It tracks weighted time, the worker time each class
// uses divided by its weight.
type classShares struct {
	weights []int
	used    []time.Duration
	ties    []int
	bank    time.Duration
}

// newClassShares requires a positive weight for every scheduled class and
// rejects other classes. bank limits the worker time an empty class can
// use before a running class runs again. offset rotates the
// ScheduledWorkClasses order for classes with equal weighted times.
func newClassShares(weights map[string]int, bank time.Duration, offset int) (*classShares, error) {
	count := len(scheduledClasses)
	shares := &classShares{
		weights: make([]int, count), used: make([]time.Duration, count),
		ties: make([]int, count), bank: bank,
	}
	for position, class := range scheduledClasses {
		weight, exists := weights[string(class)]
		if !exists || weight < 1 {
			return nil, fmt.Errorf("search work class %s requires a positive weight", class)
		}
		shares.weights[position] = weight
		shares.ties[position] = ((position-offset)%count + count) % count
	}
	if len(weights) != count {
		return nil, errors.New("search work class weights name an unknown class")
	}
	return shares, nil
}

// order returns class positions by weighted time, smallest first. Ties use
// the ScheduledWorkClasses order rotated by the worker offset.
func (s *classShares) order() []int {
	positions := make([]int, len(s.used))
	for position := range positions {
		positions[position] = position
	}
	slices.SortFunc(positions, func(first, second int) int {
		if compared := cmp.Compare(s.used[first], s.used[second]); compared != 0 {
			return compared
		}
		return cmp.Compare(s.ties[first], s.ties[second])
	})
	return positions
}

// spend adds duration divided by the class weight to its weighted time and
// returns the new value. Negative durations count as zero. An unknown
// class returns zero without changing any weighted time.
func (s *classShares) spend(class searchdomain.WorkClass, duration time.Duration) time.Duration {
	position := slices.Index(scheduledClasses, class)
	if position < 0 {
		return 0
	}
	s.used[position] += max(duration, 0) / time.Duration(s.weights[position])
	return s.used[position]
}

// limitBank raises the weighted time of empty to at least the weighted
// time of running minus bank divided by empty's weight. It never lowers
// the value. The empty class can use at most bank of worker time before
// the running class runs again.
func (s *classShares) limitBank(empty, running int) {
	floor := s.used[running] - s.bank/time.Duration(s.weights[empty])
	s.used[empty] = max(s.used[empty], floor)
}

// reset sets every class's weighted time to zero.
// claimNext runs reset when no class has claimable work.
func (s *classShares) reset() {
	clear(s.used)
}
