package testenv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

var macRunnerImage = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Validate checks the command inputs without creating files or Docker resources.
func (options MacTestOptions) Validate() error {
	if !filepath.IsAbs(options.Root) {
		return errors.New("require an absolute checkout root")
	}
	if !filepath.IsAbs(options.EvidenceDirectory) {
		return errors.New("require a new absolute evidence directory")
	}
	if !macRunnerImage.MatchString(options.Image) {
		return errors.New("require an immutable local image ID: sha256 followed by 64 lowercase hexadecimal digits")
	}
	_, _, err := macRunnerArguments(options.TestArguments)
	return err
}

// RunMacTests prepares a local macOS integration test invocation.
func RunMacTests(ctx context.Context, options MacTestOptions) (MacTestResult, error) {
	result := MacTestResult{ExitCode: 1, EvidenceDirectory: options.EvidenceDirectory, RunnerID: ""}
	if err := options.Validate(); err != nil {
		return result, err
	}
	if runtime.GOOS != "darwin" {
		return result, errors.New("run-mac requires local macOS Docker Desktop")
	}
	prepared, err := prepareMacRunner(ctx, options)
	if err != nil {
		return result, err
	}
	result.EvidenceDirectory = prepared.options.EvidenceDirectory
	err = errors.New("run-mac execution is not implemented; preparation completed without creating Docker resources")
	slog.ErrorContext(ctx, "testenv.run_mac.execution_unavailable", slog.String("err", err.Error()))
	return result, errors.Join(ctx.Err(), err)
}

func prepareMacRunner(ctx context.Context, options MacTestOptions) (*macRunnerPreparation, error) {
	if err := ctx.Err(); err != nil {
		return nil, macRunnerFailure("prepare macOS test runner", err)
	}
	options.TestArguments = slices.Clone(options.TestArguments)
	command, timeout, err := macRunnerArguments(options.TestArguments)
	if err != nil {
		return nil, err
	}
	root, err := macRunnerRoot(options.Root)
	if err != nil {
		return nil, err
	}
	var prepared macRunnerPreparation
	prepared.options, prepared.root, prepared.command, prepared.timeout = options, root, command, timeout
	if err := prepared.loadCompose(ctx); err != nil {
		return nil, err
	}
	prepared.options.EvidenceDirectory, err = createMacRunnerEvidence(root, options.EvidenceDirectory)
	if err != nil {
		return nil, err
	}
	prepared.source, err = captureMacRunnerSource(root, prepared.options.EvidenceDirectory)
	if err != nil {
		return nil, err
	}
	prepared.service.Environment["TACK_TEST_SOURCE_REVISION"] = &prepared.source.revision
	if err := prepared.writeSourceEvidence(); err != nil {
		return nil, err
	}
	if err := prepared.verifySources(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, macRunnerFailure("complete macOS runner preparation", err)
	}
	return &prepared, nil
}

func macRunnerArguments(arguments []string) ([]string, time.Duration, error) {
	values := make(map[string]string)
	for index := 0; index < len(arguments); index++ {
		name, value, assigned := strings.Cut(arguments[index], "=")
		name = strings.TrimPrefix(strings.TrimPrefix(name, "-"), "-")
		if !strings.HasPrefix(arguments[index], "-") || !slices.Contains([]string{"run", "count", "timeout", "tags", "skip", "v"}, name) {
			return nil, 0, fmt.Errorf("unsupported Go test argument %q", arguments[index])
		}
		if _, duplicate := values[name]; duplicate {
			return nil, 0, fmt.Errorf("duplicate Go test flag -%s", name)
		}
		if !assigned && name == "v" && (index+1 == len(arguments) || strings.HasPrefix(arguments[index+1], "-")) {
			value, assigned = "true", true
		}
		if !assigned {
			index++
			if index >= len(arguments) {
				return nil, 0, fmt.Errorf("require a value for -%s", name)
			}
			value = arguments[index]
		}
		if (name == "skip" || name == "tags") && value == "" {
			return nil, 0, fmt.Errorf("require a nonempty value for -%s", name)
		}
		values[name] = value
	}
	count, err := strconv.Atoi(values["count"])
	if err != nil || count <= 0 {
		return nil, 0, errors.New("require a positive -count")
	}
	timeout, err := time.ParseDuration(values["timeout"])
	if err != nil || timeout <= 0 {
		return nil, 0, errors.New("require a positive Go duration for -timeout")
	}
	if values["run"] == "" {
		return nil, 0, errors.New("require one nonempty -run expression")
	}
	if value, present := values["v"]; present {
		verbose, parseErr := strconv.ParseBool(value)
		if parseErr != nil || !verbose {
			return nil, 0, errors.New("-v must enable verbose output")
		}
	}
	command := []string{"test", "-tags=" + macRunnerTags(values["tags"]), "-run", values["run"], "-count=" + values["count"], "-timeout=" + values["timeout"], "-v"}
	if skip, present := values["skip"]; present {
		command = append(command, "-skip", skip)
	}
	return append(command, "./internal/test/integration/..."), timeout, nil
}

func macRunnerTags(value string) string {
	tags := []string{"fdb"}
	for _, tag := range strings.FieldsFunc(value, func(character rune) bool { return character == ',' || character == ' ' }) {
		if !slices.Contains(tags, tag) {
			tags = append(tags, tag)
		}
	}
	return strings.Join(tags, ",")
}
