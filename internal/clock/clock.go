// Package clock permits direct wall-time reads only within this package.
// Other packages must call [Now] and [Since] instead of time.Now and
// time.Since. Tests can replace the active [Clock] without changing
// production callers. The staticcheck-extra time_now_outside_clock
// analyzer checks this restriction to keep production callers testable.
package clock

import "time"

// Clock defines wall-time reads. Tests can supply a fake implementation
// without changing production callers.
type Clock interface {
	Now() time.Time
	Since(t time.Time) time.Duration
}

// Wall implements the production clock with the standard time package.
// Only Wall may call time.Now and time.Since directly in this repository.
type Wall struct{}

// Now returns the current wall time.
//
// It calls the standard time package directly.
//
//	instant := (Wall{}).Now()
func (Wall) Now() time.Time { return time.Now() }

// Since returns the duration between t and the current time.
//
// It calls time.Since, which uses a monotonic clock, a clock for measuring
// elapsed time, when t includes that reading. Otherwise, it uses wall time.
//
//	started := (Wall{}).Now()
//	elapsed := (Wall{}).Since(started)
func (Wall) Since(t time.Time) time.Duration { return time.Since(t) }

// current stores the process-wide clock that the package-level Now and
// Since functions use. Production callers must use those functions.
var current Clock = Wall{}

// Now returns the current wall time from the active clock.
//
// The active clock defaults to Wall in production.
//
//	instant := Now()
func Now() time.Time { return current.Now() }

// Since returns the duration since t from the active clock.
//
// The active Clock computes the duration; production uses Wall.
//
//	started := Now()
//	elapsed := Since(started)
func Since(t time.Time) time.Duration { return current.Since(t) }
