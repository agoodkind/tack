package audit

import "errors"

const readVerdictCode = "check_failed"

// ReadVerdictError marks a completed read when the check of the read data fails.
// The read lifecycle records the final event with outcome failed, code
// check_failed, and the read evidence. It then releases the report and returns
// Err.
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
