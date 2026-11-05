package main

import (
	"fmt"
	"os"
)

func verifyNativeRunner() error {
	architecture, err := required("IMAGE_ARCH")
	if err != nil {
		return err
	}
	if architecture != "amd64" && architecture != "arm64" {
		return fmt.Errorf("unsupported image architecture: %s", architecture)
	}
	machine, err := readRunnerMachine()
	if err != nil {
		return err
	}
	if (architecture != "amd64" || machine != "x86_64") && (architecture != "arm64" || machine != "aarch64") {
		return fmt.Errorf("image architecture %s does not match runner %s", architecture, machine)
	}
	if _, err := fmt.Printf("Native image runner: %s\n", machine); err != nil {
		return &os.PathError{Op: "write native runner output", Path: "stdout", Err: err}
	}
	return nil
}
