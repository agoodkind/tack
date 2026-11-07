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

// AsReadVerdict placeholder.
func AsReadVerdict(err error) (*ReadVerdictError, bool) {
	var verdict *ReadVerdictError
	if errors.As(err, &verdict) {
		return verdict, true
	}
	return nil, false
}
