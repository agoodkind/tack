//go:build runtimeaudit

package foundationdb

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
)

type sessionObservation struct {
	reads int
	bytes int64
}

type sessionObserver struct {
	report       func(operation string, sessionID uuid.UUID, bytes int64, reads int)
	mutex        sync.Mutex
	transactions map[fdb.ReadTransaction]sessionObservation
}

var activeSessionObserver atomic.Pointer[sessionObserver]

// SetSessionObserver sets a test function that receives measurements for
// each committed session transaction.
//
// The function receives the approximate transaction size and the count of
// session value, search record, retirement fence, and child range reads. A
// nil function clears the observer.
//
//   - A successful tracked transaction reports its measurements once.
//   - A failed transaction removes its observation without reporting it.
func SetSessionObserver(
	report func(operation string, sessionID uuid.UUID, bytes int64, reads int),
) {
	if report == nil {
		activeSessionObserver.Store(nil)
		return
	}
	activeSessionObserver.Store(&sessionObserver{
		report:       report,
		mutex:        sync.Mutex{},
		transactions: map[fdb.ReadTransaction]sessionObservation{},
	})
}

func countSessionRead(tr fdb.ReadTransaction) {
	observer := activeSessionObserver.Load()
	if observer == nil {
		return
	}
	observer.mutex.Lock()
	defer observer.mutex.Unlock()
	if observation, tracked := observer.transactions[tr]; tracked {
		observation.reads++
		observer.transactions[tr] = observation
	}
}

func applySession(tr fdb.Transaction, apply func(fdb.Transaction) error) error {
	observer := activeSessionObserver.Load()
	if observer == nil {
		return apply(tr)
	}
	observer.mutex.Lock()
	observer.transactions[tr] = sessionObservation{reads: 0, bytes: 0}
	observer.mutex.Unlock()
	if err := apply(tr); err != nil {
		return err
	}
	size, err := tr.GetApproximateSize().Get()
	if err != nil {
		return fmt.Errorf("read the approximate size of the session transaction: %w", err)
	}
	observer.mutex.Lock()
	defer observer.mutex.Unlock()
	observation := observer.transactions[tr]
	observation.bytes = size
	observer.transactions[tr] = observation
	return nil
}

func reportSession(tr fdb.Transaction, operation string, sessionID uuid.UUID, err error) {
	observer := activeSessionObserver.Load()
	if observer == nil {
		return
	}
	observation, tracked := observer.remove(tr)
	if tracked && err == nil {
		observer.report(operation, sessionID, observation.bytes, observation.reads)
	}
}

func forgetSession(tr fdb.Transaction) {
	observer := activeSessionObserver.Load()
	if observer != nil {
		observer.remove(tr)
	}
}

// PendingSessionObservations returns the number of session transactions that
// the test observer tracks and has not yet reported or removed.
//
// The count includes observations until reportSession or forgetSession
// removes them. The function reads the count under the observer's mutex.
//
//   - An unset observer returns zero.
//   - After SetSessionObserver installs a replacement, this count excludes
//     transactions that only the previous observer tracked.
func PendingSessionObservations() int {
	observer := activeSessionObserver.Load()
	if observer == nil {
		return 0
	}
	observer.mutex.Lock()
	defer observer.mutex.Unlock()
	return len(observer.transactions)
}

func (o *sessionObserver) remove(tr fdb.ReadTransaction) (sessionObservation, bool) {
	o.mutex.Lock()
	defer o.mutex.Unlock()
	observation, tracked := o.transactions[tr]
	delete(o.transactions, tr)
	return observation, tracked
}
