package manifest

// environmentError reports a missing or empty environment variable.
type environmentError struct {
	key string
	err error
}

// Error returns the operation, the variable key, and the cause.
func (failure *environmentError) Error() string {
	return "read manifest input " + failure.key + ": " + failure.err.Error()
}

// Unwrap returns the cause so callers can match it with errors.Is.
func (failure *environmentError) Unwrap() error {
	return failure.err
}
