package testenv

type macRunnerOperation struct {
	Operation string `json:"operation"`
	ID        string `json:"id"`
	Error     string `json:"error,omitempty"`
}

func (execution *macRunnerExecution) operation(operation, id string, err error) {
	result := macRunnerOperation{Operation: operation, ID: id, Error: ""}
	if err != nil {
		result.Error = err.Error()
	}
	execution.operations = append(execution.operations, result)
}
