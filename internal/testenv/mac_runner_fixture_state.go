package testenv

import (
	"sync"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/network"
)

type macRunnerFixture struct {
	ID             string                   `json:"id"`
	Name           string                   `json:"name"`
	Labels         map[string]string        `json:"labels"`
	Proof          string                   `json:"proof"`
	CreateObserved bool                     `json:"create_observed"`
	LogObserved    bool                     `json:"log_observed"`
	Volumes        []macRunnerFixtureVolume `json:"volumes"`
	Absent         bool                     `json:"absent"`
}

type macRunnerFixtureVolume struct {
	Name      string `json:"name"`
	Target    string `json:"target"`
	Anonymous bool   `json:"anonymous"`
}

type macRunnerFixtureProcess struct {
	Process string `json:"process"`
	Run     string `json:"run"`
	Host    string `json:"host"`
	Runner  string `json:"runner"`
}

type macRunnerPreservedResource struct {
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	Reason     string `json:"reason"`
	Incomplete bool   `json:"incomplete"`
}

type macRunnerFixtureEvidence struct {
	Containers   map[string]macRunnerFixture           `json:"containers"`
	Networks     map[string]macRunnerFixture           `json:"networks"`
	Volumes      map[string]bool                       `json:"volume_candidates"`
	Processes    map[string]macRunnerFixtureProcess    `json:"processes"`
	Preserved    map[string]macRunnerPreservedResource `json:"preserved"`
	OtherRunners map[string]string                     `json:"other_runners"`
}

type macRunnerObservedCreate struct {
	Event     events.Message
	Container *container.InspectResponse
	Network   *network.Inspect
	Error     string
}

type macRunnerEventSnapshot struct {
	sync.Mutex
	creates []macRunnerObservedCreate
}

func (snapshot *macRunnerEventSnapshot) copy() []macRunnerObservedCreate {
	snapshot.Lock()
	defer snapshot.Unlock()
	return append([]macRunnerObservedCreate(nil), snapshot.creates...)
}

func newMacRunnerFixtures() macRunnerFixtureEvidence {
	return macRunnerFixtureEvidence{
		Containers: make(map[string]macRunnerFixture), Networks: make(map[string]macRunnerFixture),
		Volumes: make(map[string]bool), Processes: make(map[string]macRunnerFixtureProcess),
		Preserved:    make(map[string]macRunnerPreservedResource),
		OtherRunners: make(map[string]string),
	}
}
