package manifest

// environmentError reports a missing or empty environment variable.
type environmentError struct {
	key string
	err error
}

// Error returns the operation, variable key, and cause.
//
// The message identifies the required manifest input without including
// its value. It appends the original error text after the key.
//
//	message := failure.Error()
func (failure *environmentError) Error() string {
	return "read manifest input " + failure.key + ": " + failure.err.Error()
}

// Unwrap returns the original environment input error.
//
// The returned error lets [errors.Is] match the cause supplied when the
// manifest input failed validation.
//
//	cause := failure.Unwrap()
func (failure *environmentError) Unwrap() error {
	return failure.err
}
