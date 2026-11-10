//go:build pgoverlay

package pgoverlay_test

import (
	"testing"

	"goodkind.io/tack/internal/testenv"
)

func (space *workspace) run(
	t *testing.T,
	variables map[string]string,
	args ...string,
) (string, int) {
	t.Helper()
	environment, command := space.tofuCommand(variables, args)
	output, code := space.host.Run(t, environment, command...)
	space.transcript.WriteString(output)
	return output, code
}

// start runs tofu in the container without waiting for it to end.
func (space *workspace) start(
	t *testing.T,
	variables map[string]string,
	args ...string,
) *testenv.TofuProcess {
	t.Helper()
	environment, command := space.tofuCommand(variables, args)
	return space.host.Start(t, environment, command...)
}

// wait returns the output and exit code of a process that start returned.
func (space *workspace) wait(t *testing.T, process *testenv.TofuProcess) (string, int) {
	t.Helper()
	output, code := process.Wait(t)
	space.transcript.WriteString(output)
	return output, code
}

func (space *workspace) tofuCommand(
	variables map[string]string,
	args []string,
) ([]string, []string) {
	environment := []string{
		"HOME=" + space.host.Directory,
		"TF_CLI_CONFIG_FILE=" + space.cliConfigPath(),
		"TF_IN_AUTOMATION=1",
		"TF_INPUT=0",
	}
	for name, value := range variables {
		environment = append(environment, "TF_VAR_"+name+"="+value)
	}
	command := append([]string{"tofu", "-chdir=" + space.host.Directory}, args...)
	return environment, command
}
