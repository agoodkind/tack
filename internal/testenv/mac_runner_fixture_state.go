package testenv

import (
	"sync"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
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
	NamedVolumes map[string]volume.Volume              `json:"named_disk_volumes"`
	Processes    map[string]macRunnerFixtureProcess    `json:"processes"`
	Preserved    map[string]macRunnerPreservedResource `json:"preserved"`
	OtherRunners map[string]string                     `json:"other_runners"`
}

type macRunnerObservedCreate struct {
	Event     events.Message             `json:"event"`
	Container *container.InspectResponse `json:"container"`
	Network   *network.Inspect           `json:"network"`
	Volume    *volume.Volume             `json:"volume"`
	Error     string                     `json:"error"`
}

type macRunnerEventSnapshot struct {
	sync.Mutex
	creates     []macRunnerObservedCreate
	bytes       int
	runnerID    string
	inspections int
}

func (snapshot *macRunnerEventSnapshot) copy() []macRunnerObservedCreate {
	snapshot.Lock()
	defer snapshot.Unlock()

	return append([]macRunnerObservedCreate(nil), snapshot.creates...)
}

func newMacRunnerFixtures() macRunnerFixtureEvidence {
	return macRunnerFixtureEvidence{
		Containers:   make(map[string]macRunnerFixture),
		Networks:     make(map[string]macRunnerFixture),
		Volumes:      make(map[string]bool),
		Processes:    make(map[string]macRunnerFixtureProcess),
		NamedVolumes: make(map[string]volume.Volume),
		Preserved:    make(map[string]macRunnerPreservedResource),
		OtherRunners: make(map[string]string),
	}
}
