package testenvcli

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"goodkind.io/tack/internal/testenv"
)

var (
	errTestFlagsSeparator = errors.New("require -- before the Go test flags")
	errUnsupportedOption  = errors.New("unsupported option")
	errDuplicateOption    = errors.New("duplicate option")
	errMissingOptionValue = errors.New("require a value for")
	errEmptyOptionValue   = errors.New("require a nonempty value for")
)

func parseMacOptions(arguments []string) (testenv.MacTestOptions, error) {
	var options testenv.MacTestOptions
	values := make(map[string]string)
	separator := slices.Index(arguments, "--")
	if separator < 0 {
		return options, errTestFlagsSeparator
	}

	for index := 0; index < separator; index++ {
		name, value, assigned := strings.Cut(arguments[index], "=")
		if name != "--root" && name != "--image" && name != "--evidence-dir" {
			return options, fmt.Errorf("%w %q", errUnsupportedOption, arguments[index])
		}

		if _, duplicate := values[name]; duplicate {
			return options, fmt.Errorf("%w %s", errDuplicateOption, name)
		}

		if !assigned {
			index++
			if index >= separator {
				return options, fmt.Errorf("%w %s", errMissingOptionValue, name)
			}

			value = arguments[index]
		}

		if value == "" {
			return options, fmt.Errorf("%w %s", errEmptyOptionValue, name)
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
