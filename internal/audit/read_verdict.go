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
//
// Callers must set Err to the failed check before constructing the verdict.
//
// Example:
//
//	verdict := &ReadVerdictError{Err: errors.New("signer check failed")}
//	verdict.Error() == "signer check failed"
func (v *ReadVerdictError) Error() string {
	return v.Err.Error()
}

// Unwrap returns Err.
//
// The standard errors package uses this method to inspect the failed check.
//
// Example:
//
//	errors.Is(&ReadVerdictError{Err: checkError}, checkError) == true
func (v *ReadVerdictError) Unwrap() error {
	return v.Err
}

// IsReadVerdict reports whether err or an error it wraps is a ReadVerdictError.
//
// A true result permits the caller to print the completed read after its final
// audit event commits, even though the command returns a failed check.
//
// Example:
//
//	IsReadVerdict(&ReadVerdictError{Err: checkError}) == true
func IsReadVerdict(err error) bool {
	var verdict *ReadVerdictError
	return errors.As(err, &verdict)
}
