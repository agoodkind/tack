package testenv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/clock"
)

const ledgerEvidenceVariable = "TACK_TEST_LEDGER_EVIDENCE_DIRECTORY"

type ledgerReleaseEvidence struct {
	Container    string                  `json:"Container"`
	CapturedAt   time.Time               `json:"CapturedAt"`
	Image        string                  `json:"Image"`
	Running      bool                    `json:"Running"`
	RestartCount int                     `json:"RestartCount"`
	OOMKilled    bool                    `json:"OOMKilled"`
	SQLReady     bool                    `json:"SQLReady"`
	Commands     []ledgerEvidenceCommand `json:"Commands"`
	Failures     []string                `json:"Failures"`
}

type ledgerEvidenceCommand struct {
	Name     string `json:"Name"`
	Output   string `json:"Output"`
	ExitCode int    `json:"ExitCode"`
	Error    string `json:"Error"`
}

// captureLedgerReleaseEvidence captures an owned SQL fixture before removal
// when an operator requests native-platform diagnostics.
func captureLedgerReleaseEvidence(ctx context.Context, containers []string) error {
	directory := os.Getenv(ledgerEvidenceVariable)
	if directory == "" {
		return nil
	}
	cli, err := dockerClient(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()
	var failures []error
	for _, name := range containers {
		if !strings.HasPrefix(name, "tack-testenv-yugabyte-") {
			continue
		}
		if err := captureOwnedLedger(ctx, cli, name, directory); err != nil {
			failures = append(failures, err)
		}
	}
	if err := errors.Join(failures...); err != nil {
		slog.ErrorContext(ctx, "testenv.ledger.evidence_failed", slog.String("err", err.Error()))
		return err
	}
	return nil
}

func captureOwnedLedger(ctx context.Context, cli *client.Client, name, directory string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	directory = filepath.Join(directory, name)
	if !filepath.IsLocal(directory) {
		return fmt.Errorf("SQL evidence directory must be relative to the working directory: %q", directory)
	}
	workingRoot, err := os.OpenRoot(".")
	if err != nil {
		slog.ErrorContext(ctx, "testenv.ledger.evidence_root_failed", slog.String("err", err.Error()))
		return fmt.Errorf("open SQL evidence working directory: %w", err)
	}
	defer func() { _ = workingRoot.Close() }()
	if err := workingRoot.MkdirAll(directory, 0o700); err != nil {
		slog.ErrorContext(ctx, "testenv.ledger.evidence_directory_failed", slog.String("err", err.Error()))
		return fmt.Errorf("create SQL evidence directory: %w", err)
	}
	root, err := workingRoot.OpenRoot(directory)
	if err != nil {
		slog.ErrorContext(ctx, "testenv.ledger.evidence_root_failed", slog.String("err", err.Error()))
		return fmt.Errorf("open SQL evidence directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	evidence := ledgerReleaseEvidence{
		Container: name, CapturedAt: clock.Now().UTC(), Image: "", Running: false,
		RestartCount: 0, OOMKilled: false, SQLReady: false,
		Commands: nil, Failures: nil,
	}
	inspected, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		evidence.Failures = append(evidence.Failures, "inspect: "+err.Error())
	} else {
		evidence.Image = inspected.Container.Image
		evidence.Running = inspected.Container.State.Running
		evidence.RestartCount = inspected.Container.RestartCount
		evidence.OOMKilled = inspected.Container.State.OOMKilled
		if !evidence.Running || evidence.RestartCount != 0 || evidence.OOMKilled {
			evidence.Failures = append(evidence.Failures, "SQL container is not healthy")
		}
	}
	if err := probeLedger(ctx, ledgerState.result); err != nil {
		evidence.Failures = append(evidence.Failures, "SQL query: "+err.Error())
	} else {
		evidence.SQLReady = true
	}
	commands := []struct {
		name string
		args []string
	}{
		{name: "cores", args: []string{"find", "/home/yugabyte/var", "-name", "core", "-o", "-name", "core.*"}},
		{name: "postmaster", args: []string{"cat", "/home/yugabyte/var/data/pg_data_11/postmaster.pid"}},
		{name: "processes", args: []string{"ps", "-eo", "pid,comm"}},
	}
	for _, command := range commands {
		output, exitCode, commandError := execInContainer(ctx, cli, name, command.args)
		result := ledgerEvidenceCommand{Name: command.name, Output: output, ExitCode: exitCode, Error: ""}
		if commandError != nil {
			result.Error = commandError.Error()
		}
		evidence.Commands = append(evidence.Commands, result)
		if commandError != nil || exitCode != 0 {
			evidence.Failures = append(evidence.Failures, command.name+" collection failed")
		}
		if command.name == "cores" && strings.TrimSpace(output) != "" {
			evidence.Failures = append(evidence.Failures, "core files exist")
		}
	}
	if err := copyLedgerBackendLogs(ctx, cli, name, root); err != nil {
		evidence.Failures = append(evidence.Failures, "backend logs: "+err.Error())
	}
	encoded, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		slog.ErrorContext(ctx, "testenv.ledger.evidence_encode_failed", slog.String("err", err.Error()))
		return fmt.Errorf("encode SQL release evidence: %w", err)
	}
	if err := root.WriteFile("release.json", encoded, 0o600); err != nil {
		slog.ErrorContext(ctx, "testenv.ledger.evidence_write_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write SQL release evidence: %w", err)
	}
	if len(evidence.Failures) != 0 {
		slog.ErrorContext(ctx, "testenv.ledger.evidence_incomplete", slog.String("container", name), slog.String("err", strings.Join(evidence.Failures, "; ")))
		return fmt.Errorf("SQL evidence for %s is incomplete: %s", name, strings.Join(evidence.Failures, "; "))
	}
	return nil
}

func copyLedgerBackendLogs(ctx context.Context, cli *client.Client, name string, root *os.Root) error {
	copied, err := cli.CopyFromContainer(ctx, name, client.CopyFromContainerOptions{
		SourcePath: "/home/yugabyte/var/data/yb-data/tserver/logs",
	})
	if err != nil {
		slog.ErrorContext(ctx, "testenv.ledger.backend_copy_failed", slog.String("err", err.Error()))
		return fmt.Errorf("copy SQL backend logs from %s: %w", name, err)
	}
	defer func() { _ = copied.Content.Close() }()
	file, err := root.OpenFile("tserver-logs.tar", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		slog.ErrorContext(ctx, "testenv.ledger.backend_open_failed", slog.String("err", err.Error()))
		return fmt.Errorf("open SQL backend archive: %w", err)
	}
	_, copyError := io.Copy(file, copied.Content)
	if err := errors.Join(copyError, file.Close()); err != nil {
		slog.ErrorContext(ctx, "testenv.ledger.backend_write_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write SQL backend archive: %w", err)
	}
	return nil
}
