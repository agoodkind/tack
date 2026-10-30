package testenv

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type macRunnerSummary struct {
	Name                   string                              `json:"name"`
	RunnerID               string                              `json:"runner_id,omitempty"`
	RuntimeExit            *int                                `json:"runtime_exit,omitempty"`
	ExitCode               int                                 `json:"exit_code"`
	Interrupted            bool                                `json:"interrupted"`
	Started                bool                                `json:"started"`
	AttachComplete         bool                                `json:"attach_complete"`
	FinalLogsComplete      bool                                `json:"final_logs_complete"`
	EventsComplete         bool                                `json:"events_complete"`
	Error                  string                              `json:"error,omitempty"`
	Stages                 []macRunnerStage                    `json:"stages"`
	CreatedIDs             []string                            `json:"created_ids"`
	AnonymousVolumes       map[string]bool                     `json:"anonymous_volumes"`
	TemporaryVolumes       map[string]macRunnerTemporaryVolume `json:"temporary_volumes"`
	Wait                   *macRunnerWaitEvidence              `json:"wait,omitempty"`
	Preserved              macRunnerBaseline                   `json:"preserved"`
	Baseline               macRunnerBaseline                   `json:"baseline"`
	Operations             []macRunnerOperation                `json:"operations"`
	UnknownResources       []string                            `json:"unknown_resources"`
	CleanupComplete        bool                                `json:"outer_cleanup_complete"`
	Caches                 map[string]bool                     `json:"caches"`
	NetworkID              string                              `json:"network_id,omitempty"`
	NetworkCreated         bool                                `json:"network_created"`
	InnerAttribution       string                              `json:"inner_attribution"`
	Fixtures               macRunnerFixtureEvidence            `json:"fixtures"`
	AttributionComplete    bool                                `json:"fixture_attribution_complete"`
	FixtureCleanupComplete bool                                `json:"fixture_cleanup_complete"`
	FullCleanupComplete    bool                                `json:"full_cleanup_complete"`
}

func (execution *macRunnerExecution) write(name, contents string) {
	slog.Debug("testenv.run_mac.evidence_write", slog.String("artifact", name))
	path := filepath.Join(execution.prepared.options.EvidenceDirectory, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		execution.evidenceErrors = append(execution.evidenceErrors, fmt.Errorf("write evidence %s: %w", path, err))
	}
}

func writeMacRunnerJSON[T macRunnerEvidenceValue](execution *macRunnerExecution, name string, value T) {
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		execution.evidenceErrors = append(execution.evidenceErrors, fmt.Errorf("encode evidence %s: %w", name, err))
		return
	}
	execution.write(name, string(contents)+"\n")
}

func (execution *macRunnerExecution) open(name string) (*os.File, error) {
	path := filepath.Join(execution.prepared.options.EvidenceDirectory, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, macRunnerFailure("open evidence "+path, err)
	}
	return file, nil
}

func (execution *macRunnerExecution) status(name string, err error) {
	code := 0
	if err != nil {
		code = 1
		execution.write(strings.TrimSuffix(name, ".exit")+".error", err.Error()+"\n")
	}
	execution.write(name, strconv.Itoa(code)+"\n")
}

func (execution *macRunnerExecution) summarize(code int, interrupted bool, err error) error {
	if execution.runtimeExit != nil {
		execution.write("runtime.exit", strconv.Itoa(*execution.runtimeExit)+"\n")
	}
	execution.write("wrapper.exit", strconv.Itoa(code)+"\n")
	if err != nil {
		execution.write("wrapper.error", err.Error()+"\n")
	}
	if artifactErr := macRunnerErrors("write result artifacts", execution.evidenceErrors...); artifactErr != nil {
		err = macRunnerErrors("record final result", err, artifactErr)
		if !interrupted {
			code = 1
		}
		execution.write("wrapper.exit", strconv.Itoa(code)+"\n")
	}
	summary := macRunnerSummary{
		Name: execution.name, RunnerID: execution.runnerID(), RuntimeExit: execution.runtimeExit,
		ExitCode: code, Interrupted: interrupted, Started: execution.started,
		AttachComplete: false, FinalLogsComplete: false, EventsComplete: false, Error: "",
		Stages: execution.stages, CreatedIDs: execution.createdIDs,
		AnonymousVolumes: execution.anonymousVolumes, Preserved: execution.preserved, Baseline: execution.baseline,
		TemporaryVolumes: execution.temporaryVolumes, Wait: execution.waitEvidence,
		Operations: execution.operations, CleanupComplete: false,
		UnknownResources: execution.unknownResources,
		Caches:           execution.cacheVolumes, NetworkID: execution.networkID,
		NetworkCreated: execution.networkCreated, InnerAttribution: "incomplete",
		Fixtures: execution.fixtures, AttributionComplete: execution.attributionComplete,
		FixtureCleanupComplete: execution.fixtureCleanupComplete,
		FullCleanupComplete:    false,
	}
	for _, stage := range execution.stages {
		if stage.Name == "outer cleanup" && stage.Error == "" {
			summary.CleanupComplete = true
		}
		if stage.Name == "attach completion" && stage.Error == "" {
			summary.AttachComplete = true
		}
		if stage.Name == "final log retrieval" && stage.Error == "" {
			summary.FinalLogsComplete = true
		}
		if stage.Name == "event completion" && stage.Error == "" {
			summary.EventsComplete = true
		}
	}
	summary.CleanupComplete = summary.CleanupComplete && summary.EventsComplete && len(summary.UnknownResources) == 0
	if summary.AttributionComplete {
		summary.InnerAttribution = "verified"
	}
	if execution.waitOutcome != nil {
		summary.CleanupComplete = summary.CleanupComplete && execution.waitEvidence != nil && execution.waitEvidence.ReceiverComplete
	}
	if err != nil {
		summary.Error = err.Error()
	}
	summary.FullCleanupComplete = summary.CleanupComplete && summary.FixtureCleanupComplete
	writeMacRunnerJSON(execution, "result.json", summary)
	return macRunnerErrors("write final evidence", execution.evidenceErrors...)
}

func (execution *macRunnerExecution) runnerID() string {
	if execution.acceptedID != "" {
		return execution.acceptedID
	}
	if len(execution.createdIDs) != 0 {
		return execution.createdIDs[len(execution.createdIDs)-1]
	}
	return ""
}

func macRunnerErrors(operation string, failures ...error) error {
	err := errors.Join(failures...)
	if err != nil {
		slog.Error("testenv.run_mac.failed", slog.String("operation", operation), slog.String("err", err.Error()))
	}
	return err
}
