package audit

import "errors"

const readVerdictCode = "check_failed"

// ReadVerdictError marks a completed read when a check of the read data fails.
// The read lifecycle records a final event with outcome failed, code
// check_failed, and the read evidence. It then returns the payload and Err
// to the caller, which prints the payload.
type ReadVerdictError struct {
	Err error
}

// Error returns the message of Err.
func (v *ReadVerdictError) Error() string {
	return v.Err.Error()
}

// Unwrap returns Err.
func (v *ReadVerdictError) Unwrap() error {
	return v.Err
}

// IsReadVerdict reports whether err or an error it wraps is a ReadVerdictError.
func IsReadVerdict(err error) bool {
	var verdict *ReadVerdictError
	return errors.As(err, &verdict)
}
