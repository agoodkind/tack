package testenv

import (
	"time"

	"github.com/compose-spec/compose-go/v2/types"
)

// MacTestOptions selects a checkout, an existing image, and integration tests.
type MacTestOptions struct {
	Root              string
	Image             string
	EvidenceDirectory string
	TestArguments     []string
}

// MacTestResult reports the test status and the evidence for one invocation.
type MacTestResult struct {
	ExitCode          int
	EvidenceDirectory string
	RunnerID          string
}

// macRunnerPreparation stores inputs and source evidence for one invocation.
type macRunnerPreparation struct {
	options MacTestOptions
	root    string
	source  macRunnerSource
	project *types.Project
	service types.ServiceConfig
	command []string
	timeout time.Duration
}

type macRunnerSource struct {
	revision string
	dirty    string
	paths    []string
	manifest []macRunnerSourceHash
}

type macRunnerSourceHash struct {
	path   string
	digest [32]byte
}

type macRunnerInvocation struct {
	Root     string                         `json:"root"`
	Project  string                         `json:"project"`
	Service  types.ServiceConfig            `json:"service"`
	Networks map[string]types.NetworkConfig `json:"networks"`
}
