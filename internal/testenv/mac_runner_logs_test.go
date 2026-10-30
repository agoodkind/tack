//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

func TestMacRunnerDockerFinalOutput(t *testing.T) {
	options, docker, _ := macDockerFixture(t)
	options.TestArguments = []string{"-run=^TestMacRunnerFinalOutput$", "-count=1", "-timeout=45s", "-tags=macrunnerfixture,runtimeaudit"}
	result, err := testenv.RunMacTests(t.Context(), options)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("final output invocation: result=%+v error=%v", result, err)
	}
	macRuntimeEvidence(t, options, docker)
	output := macRead(t, options.EvidenceDirectory, "runtime.log")
	if strings.Count(output, "MAC_RUNNER_STDOUT\n") != 4096 || strings.Count(output, "MAC_RUNNER_STDERR\n") != 4096 || !strings.Contains(output, "MAC_RUNNER_FINAL_STDOUT\n") || !strings.Contains(output, "MAC_RUNNER_FINAL_STDERR\n") || !strings.Contains(output, "\nPASS\n") {
		t.Fatal("final Docker logs did not contain complete stdout, stderr, cleanup output, and Go test completion")
	}
	attached := macRead(t, options.EvidenceDirectory, "attach.log")
	if !bytes.Equal([]byte(output), []byte(attached)) {
		t.Fatal("final output did not match the separately retained attach output")
	}
	if macRead(t, options.EvidenceDirectory, "final-logs.exit") != "0\n" {
		t.Fatal("final Docker log retrieval did not record success")
	}
	macRequireInvocation(t, options.EvidenceDirectory)
}

func macRequireInvocation(t *testing.T, directory string) {
	t.Helper()
	var invocation struct {
		SchemaVersion int    `json:"schema_version"`
		Name          string `json:"name"`
	}
	if err := json.Unmarshal([]byte(macRead(t, directory, "invocation.json")), &invocation); err != nil {
		t.Fatal(err)
	}
	var summary struct {
		Name              string `json:"name"`
		FinalLogsComplete bool   `json:"final_logs_complete"`
	}
	if err := json.Unmarshal([]byte(macRead(t, directory, "result.json")), &summary); err != nil {
		t.Fatal(err)
	}
	if invocation.SchemaVersion != 1 || !regexp.MustCompile(`^tack-mac-runner-[0-9a-f]{24}$`).MatchString(invocation.Name) || invocation.Name != summary.Name || !summary.FinalLogsComplete {
		t.Fatalf("invocation metadata and final log status: invocation=%+v result=%+v", invocation, summary)
	}
}

func macRequireStageFailure(t *testing.T, directory, artifact, message string) {
	t.Helper()
	if macRead(t, directory, artifact+".exit") != "1\n" {
		t.Fatalf("%s did not record a failure status", artifact)
	}
	details := strings.TrimSpace(macRead(t, directory, artifact+".error"))
	if !strings.Contains(details, message) || !strings.Contains(macRead(t, directory, "wrapper.error"), details) {
		t.Fatalf("%s did not preserve paired and aggregate error details: %s", artifact, details)
	}
	var summary struct {
		Stages []struct {
			Error string `json:"error"`
		} `json:"stages"`
	}
	if err := json.Unmarshal([]byte(macRead(t, directory, "result.json")), &summary); err != nil {
		t.Fatal(err)
	}
	for _, stage := range summary.Stages {
		if strings.Contains(stage.Error, details) {
			return
		}
	}
	t.Fatalf("%s did not preserve its structured stage error", artifact)
}

func macRequireFinalLogFailure(t *testing.T, directory, id string) {
	t.Helper()
	macRequireStageFailure(t, directory, "final-logs", id)
	if !strings.Contains(macRead(t, directory, "attach.log"), "MAC_RUNNER_SIGNAL_READY") {
		t.Fatal("failed final retrieval did not preserve original attach output")
	}
	if macRead(t, directory, "source-hash-verification.exit") != "0\n" || macRead(t, directory, "cleanup.exit") != "0\n" {
		t.Fatal("failed final retrieval prevented source verification or outer cleanup")
	}
	var summary struct {
		FinalLogsComplete bool `json:"final_logs_complete"`
		Attribution       bool `json:"fixture_attribution_complete"`
		Stages            []struct {
			Name  string `json:"name"`
			Error string `json:"error"`
		} `json:"stages"`
	}
	if err := json.Unmarshal([]byte(macRead(t, directory, "result.json")), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.FinalLogsComplete || summary.Attribution {
		t.Fatal("failed final retrieval claimed complete logs or fixture attribution")
	}
	for _, stage := range summary.Stages {
		if stage.Name == "terminal inspection" && strings.Contains(stage.Error, id) {
			return
		}
	}
	t.Fatal("removed runner did not record the final inspection failure")
}
