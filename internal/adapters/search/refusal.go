package search

type aliasRejectedError struct {
	alias    string
	index    string
	response string
}

func (err *aliasRejectedError) Error() string {
	return "set OpenSearch alias " + err.alias + " to " + err.index + ": " + err.response
}

type aliasUnacknowledgedError struct {
	alias string
	index string
}

func (err *aliasUnacknowledgedError) Error() string {
	return "set OpenSearch alias " + err.alias + " to " + err.index + ": update was not acknowledged"
}
