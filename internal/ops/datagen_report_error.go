package ops

// datagenReportError omits the underlying cause text from its own error string.
// Unwrap returns the cause for errors.Is matching.
type datagenReportError struct {
	report string
	cause  error
}

func (failure datagenReportError) Error() string {
	return "generated data report " + failure.report + " failed"
}

func (failure datagenReportError) Unwrap() error {
	return failure.cause
}
