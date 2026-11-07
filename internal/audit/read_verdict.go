package audit

import "errors"

const readVerdictCode = "check_failed"

// ReadVerdictError placeholder.
type ReadVerdictError struct {
	Err error
}

// Error placeholder.
func (v *ReadVerdictError) Error() string {
	return v.Err.Error()
}

// Unwrap placeholder.
func (v *ReadVerdictError) Unwrap() error {
	return v.Err
}

// IsReadVerdict placeholder.
func IsReadVerdict(err error) bool {
	var verdict *ReadVerdictError
	return errors.As(err, &verdict)
}
