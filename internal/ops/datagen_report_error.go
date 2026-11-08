package ops

// datagenReportError preserves the cause for errors.Is without recording
// database messages in either the report event or the enclosing command.
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
