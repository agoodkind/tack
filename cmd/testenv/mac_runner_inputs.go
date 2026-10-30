package main

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"goodkind.io/tack/internal/testenv"
)

func parseMacOptions(arguments []string) (testenv.MacTestOptions, error) {
	var options testenv.MacTestOptions
	values := make(map[string]string)
	separator := slices.Index(arguments, "--")
	if separator < 0 {
		return options, errors.New("require -- before the Go test flags")
	}
	for index := 0; index < separator; index++ {
		name, value, assigned := strings.Cut(arguments[index], "=")
		if name != "--root" && name != "--image" && name != "--evidence-dir" {
			return options, fmt.Errorf("unsupported option %q", arguments[index])
		}
		if _, duplicate := values[name]; duplicate {
			return options, fmt.Errorf("duplicate option %s", name)
		}
		if !assigned {
			index++
			if index >= separator {
				return options, fmt.Errorf("require a value for %s", name)
			}
			value = arguments[index]
		}
		if value == "" {
			return options, fmt.Errorf("require a nonempty value for %s", name)
		}
		values[name] = value
	}
	options.Root = values["--root"]
	options.Image = values["--image"]
	options.EvidenceDirectory = values["--evidence-dir"]
	options.TestArguments = slices.Clone(arguments[separator+1:])
	if err := options.Validate(); err != nil {
		slog.Error("testenv.run_mac.invalid", slog.String("err", err.Error()))
		return options, fmt.Errorf("validate run-mac inputs: %w", err)
	}
	return options, nil
}
