// buffered_room.go admits read-class events to the buffered recorder's queue.
// The capacity covers the events waiting in the queue and each event that the
// flusher retains after a failed delivery until a later delivery succeeds.
// During an outage the retained events fill the capacity, and Record sends new
// events to the overflow instead of growing memory (TACK-583).

package audit

import "sync"

// bufferRoom sends an event to the queue only while the queued events and
// the retained events together are fewer than the queue's capacity. Every
// send runs under mu after that check and never blocks.
type bufferRoom struct {
	mu       sync.Mutex
	queue    chan Event
	retained int
	waiting  int
	opened   chan struct{}
}

func newBufferRoom(queue chan Event) *bufferRoom {
	return &bufferRoom{
		mu:       sync.Mutex{},
		queue:    queue,
		retained: 0,
		waiting:  0,
		opened:   make(chan struct{}),
	}
}

// tryAdmit sends ev to the queue and reports whether the queue had room.
func (r *bufferRoom) tryAdmit(ev Event) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.hasRoom() {
		return false
	}
	r.queue <- ev
	return true
}

// admission is the result of one admitOrWait call.
type admission int

const (
	// admitted means admitOrWait sent the event to the queue.
	admitted admission = iota
	// refusedRetained means the queue was full while the flusher retained
	// undelivered events, so room opens only after a delivery succeeds.
	refusedRetained
	// mustWait means the queue was full of queued events. The caller waits
	// on the returned channel and then calls stopWaiting.
	mustWait
)

// admitOrWait sends ev to the queue when it has room. When the queue is full
// and the flusher retains events, it refuses ev. Otherwise it registers the
// caller as a waiter and returns the channel that closes when room opens.
func (r *bufferRoom) admitOrWait(ev Event) (admission, <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hasRoom() {
		r.queue <- ev
		return admitted, nil
	}
	if r.retained > 0 {
		return refusedRetained, nil
	}
	r.waiting++
	return mustWait, r.opened
}

func (r *bufferRoom) stopWaiting() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.waiting--
}

// dequeued wakes the waiters after the flusher takes an event from the queue.
func (r *bufferRoom) dequeued() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.wake()
}

// setRetained replaces the count of events the flusher retains.
func (r *bufferRoom) setRetained(count int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.retained = count
	r.wake()
}

// hasRoom reports whether one more event fits. The caller must lock mu.
func (r *bufferRoom) hasRoom() bool {
	return len(r.queue)+r.retained < cap(r.queue)
}

// wake releases every waiter when the queue has room. The caller must lock mu.
func (r *bufferRoom) wake() {
	if r.waiting == 0 || !r.hasRoom() {
		return
	}
	close(r.opened)
	r.opened = make(chan struct{})
}
