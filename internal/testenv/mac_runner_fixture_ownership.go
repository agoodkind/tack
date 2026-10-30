package testenv

import (
	"fmt"
	"maps"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
)

func (execution *macRunnerExecution) fixtureOwner(labels map[string]string) bool {
	return execution.acceptedID != "" && labels[managedLabel] == "true" && labels[runnerLabel] == execution.acceptedID
}

func (execution *macRunnerExecution) preserveFixture(kind, id, reason string, incomplete bool) {
	execution.fixtures.Preserved[kind+":"+id] = macRunnerPreservedResource{Kind: kind, ID: id, Reason: reason, Incomplete: incomplete}
}

func (execution *macRunnerExecution) candidateFixture(kind, id string, labels map[string]string) bool {
	if execution.fixtures.OtherRunners[kind+":"+id] != "" {
		return false
	}
	if labels[managedLabel] != "true" {
		return false
	}
	if macRunnerContainerID.MatchString(labels[runnerLabel]) && labels[runnerLabel] != execution.acceptedID {
		execution.fixtures.OtherRunners[kind+":"+id] = labels[runnerLabel]
		execution.preserveFixture(kind, id, "another runner owns this resource", false)
		return false
	}
	if labels[runnerLabel] == "" && labels[hostLabel] != "" && execution.terminal != nil && execution.terminal.Config != nil && labels[hostLabel] != execution.terminal.Config.Hostname {
		execution.preserveFixture(kind, id, "another host owns this resource", false)
		return false
	}
	return true
}

func (execution *macRunnerExecution) attributeContainer(inspection container.InspectResponse, created, logged bool) {
	id := inspection.ID
	if execution.baseline.Containers[id] || inspection.Config == nil || !macRunnerContainerID.MatchString(id) {
		return
	}
	labels := inspection.Config.Labels
	if !execution.candidateFixture("container", id, labels) {
		return
	}
	name := strings.TrimPrefix(inspection.Name, "/")
	proof := "owner labels"
	if !execution.fixtureOwner(labels) {
		process, valid := labeledProcess(labels)
		generated := strings.HasPrefix(name, "tack-testenv-") && strings.Contains(name, fmt.Sprintf("-%d-", process))
		_, known := execution.fixtures.Processes[labels[processLabel]]
		if labels[runnerLabel] != "" || !created || !logged || !valid || (!known && !generated) {
			execution.preserveFixture("container", id, "managed container lacks exact invocation attribution", true)
			return
		}
		proof = "create event and exact engine-start log with process identity"
	}
	previous := execution.fixtures.Containers[id]
	fixture := macRunnerFixture{
		ID: id, Name: name, Labels: maps.Clone(labels), Proof: proof, Volumes: nil,
		CreateObserved: created || previous.CreateObserved, LogObserved: logged || previous.LogObserved, Absent: previous.Absent,
	}
	for _, point := range inspection.Mounts {
		if point.Type != mount.TypeVolume || execution.baseline.Volumes[point.Name] || execution.cacheVolumes[point.Name] {
			continue
		}
		anonymous := macRunnerAnonymousMount(inspection, point)
		fixture.Volumes = append(fixture.Volumes, macRunnerFixtureVolume{Name: point.Name, Target: point.Destination, Anonymous: anonymous})
		execution.fixtures.Volumes[point.Name] = anonymous
		if !anonymous {
			execution.preserveFixture("volume", point.Name, "fixture uses a new named or unidentified volume", true)
		}
	}
	if len(fixture.Volumes) == 0 {
		fixture.Volumes = previous.Volumes
	}
	execution.fixtures.Containers[id] = fixture
	execution.fixtures.Processes[labels[processLabel]] = macRunnerFixtureProcess{
		Process: labels[processLabel], Run: labels[runLabel], Host: labels[hostLabel], Runner: labels[runnerLabel],
	}
	delete(execution.fixtures.Preserved, "container:"+id)
}

func macRunnerAnonymousMount(inspection container.InspectResponse, point container.MountPoint) bool {
	if !macRunnerContainerID.MatchString(point.Name) || inspection.HostConfig == nil || inspection.Config == nil {
		return false
	}
	for _, binding := range inspection.HostConfig.Binds {
		parts := strings.Split(binding, ":")
		if len(parts) > 1 && parts[1] == point.Destination {
			return false
		}
	}
	for _, request := range inspection.HostConfig.Mounts {
		if request.Target == point.Destination {
			return request.Type == mount.TypeVolume && request.Source == ""
		}
	}
	_, declared := inspection.Config.Volumes[point.Destination]
	return declared
}
