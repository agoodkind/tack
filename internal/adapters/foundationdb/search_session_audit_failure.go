package foundationdb

import (
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// sessionNotStartedError marks a session transaction that returned without an
// attempt because its context had ended. It preserves the text of err.
type sessionNotStartedError struct{ err error }

func (e sessionNotStartedError) Error() string { return e.err.Error() }
func (e sessionNotStartedError) Unwrap() []error {
	return []error{e.err, searchdomain.ErrSessionStepNotStarted}
}
