package testenv

import (
	"context"
	"os"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const (
	macRunnerLabel        = "io.goodkind.tack.mac-runner"
	macRunnerProjectLabel = "com.docker.compose.project"
	macRunnerServiceLabel = "com.docker.compose.service"
	macRunnerObservation  = 30 * time.Second
	macRunnerStreamWait   = 10 * time.Second
)

type macRunnerBaseline struct {
	Containers map[string]bool `json:"containers"`
	Networks   map[string]bool `json:"networks"`
	Volumes    map[string]bool `json:"volumes"`
}

type macRunnerStage struct {
	Name  string `json:"name"`
	Error string `json:"error,omitempty"`
}

type macRunnerRefusal struct {
	ID         string                    `json:"id"`
	Attempt    int                       `json:"attempt"`
	Reason     string                    `json:"reason"`
	Inspection container.InspectResponse `json:"inspection"`
}

type macRunnerExecution struct {
	prepared               *macRunnerPreparation
	defaultClient          *client.Client
	rawClient              *client.Client
	hostDaemon             macRunnerDaemon
	rawDaemon              macRunnerDaemon
	name                   string
	labels                 map[string]string
	baseline               macRunnerBaseline
	preserved              macRunnerBaseline
	baselineReady          bool
	networkID              string
	networkCreated         bool
	acceptedID             string
	createdIDs             []string
	temporaryVolumes       map[string]macRunnerTemporaryVolume
	anonymousVolumes       map[string]bool
	cacheVolumes           map[string]bool
	imageEnvironment       []string
	refused                []macRunnerRefusal
	stages                 []macRunnerStage
	operations             []macRunnerOperation
	unknownResources       []string
	fixtures               macRunnerFixtureEvidence
	eventSnapshot          macRunnerEventSnapshot
	observerError          error
	attributionComplete    bool
	fixtureCleanupComplete bool
	evidenceErrors         []error
	runtimeExit            *int
	started                bool
	terminal               *container.InspectResponse
	attach                 *client.ContainerAttachResult
	attachDone             chan error
	attachCancel           context.CancelFunc
	waitOutcome            <-chan macRunnerWaitOutcome
	waitReceiverDone       <-chan error
	waitCancel             context.CancelFunc
	waitEvidence           *macRunnerWaitEvidence
	logFile                *os.File
	eventCancel            context.CancelFunc
	eventDone              chan error
	eventsFile             *os.File
	eventsErrorFile        *os.File
	startedAt              time.Time
}

type macRunnerTemporaryVolume struct {
	Name          string `json:"name,omitempty"`
	IdentityError string `json:"identity_error,omitempty"`
	Absent        bool   `json:"absent"`
}

func (execution *macRunnerExecution) stage(name string, err error) error {
	stage := macRunnerStage{Name: name, Error: ""}
	if err != nil {
		stage.Error = err.Error()
	}
	execution.stages = append(execution.stages, stage)
	return err
}

func macObserve[T macRunnerObservationResult](ctx context.Context, operation func(context.Context) (T, error)) (T, error) {
	observation, cancel := context.WithTimeout(ctx, macRunnerObservation)
	defer cancel()
	return operation(observation)
}
