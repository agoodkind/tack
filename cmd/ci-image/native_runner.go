package main

import "fmt"

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
	fmt.Printf("Native image runner: %s\n", machine)
	return nil
}
