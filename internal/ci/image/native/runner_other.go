//go:build !linux && !darwin

package native

import "fmt"

func readRunnerMachine() (string, error) {
	return "", fmt.Errorf("native runner verification supports Linux and Darwin only")
}
