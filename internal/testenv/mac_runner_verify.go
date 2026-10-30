package testenv

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
)

func (execution *macRunnerExecution) owned(inspection container.InspectResponse) error {
	if !execution.baselineReady || execution.baseline.Containers[inspection.ID] || inspection.ID == "" {
		return errors.New("runner ownership requires a nonbaseline container ID")
	}
	if inspection.Name != "/"+execution.name || inspection.Config == nil {
		return fmt.Errorf("runner %s does not match generated name %s", inspection.ID, execution.name)
	}
	for key, value := range execution.labels {
		if inspection.Config.Labels[key] != value {
			return fmt.Errorf("runner %s does not match ownership label %s", inspection.ID, key)
		}
	}
	return nil
}

func (execution *macRunnerExecution) verify(inspection container.InspectResponse) error {
	if err := execution.owned(inspection); err != nil {
		return err
	}
	configuration := inspection.Config
	request := execution.request()
	if inspection.Image != execution.prepared.options.Image || configuration.Image != execution.prepared.options.Image {
		return errors.New("runner inspection does not match the immutable image ID")
	}
	if !slices.Equal(configuration.Entrypoint, request.Config.Entrypoint) || !slices.Equal(configuration.Cmd, request.Config.Cmd) || configuration.WorkingDir != request.Config.WorkingDir {
		return errors.New("runner inspection does not match the exact Go command, entrypoint and working directory")
	}
	actualEnvironment := slices.Clone(configuration.Env)
	slices.Sort(actualEnvironment)
	if !slices.Equal(actualEnvironment, execution.expectedEnvironment()) {
		return errors.New("runner inspection does not match pinned-image defaults and prepared environment overrides")
	}
	if inspection.NetworkSettings == nil || len(inspection.NetworkSettings.Networks) != 1 {
		return errors.New("runner must attach only to the loaded default network")
	}
	endpoint := inspection.NetworkSettings.Networks[execution.prepared.project.Networks["default"].Name]
	if endpoint == nil || endpoint.NetworkID != execution.networkID {
		return errors.New("runner default network identity does not match")
	}
	return execution.verifyMounts(inspection)
}

func (execution *macRunnerExecution) verifyMounts(inspection container.InspectResponse) error {
	if len(inspection.Mounts) != 5 {
		return errors.New("runner must have exactly five mount destinations")
	}
	seen := make(map[string]bool)
	socketRewritten := false
	for _, point := range inspection.Mounts {
		if seen[point.Destination] || !point.RW {
			return fmt.Errorf("runner mount %s must be unique and writable", point.Destination)
		}
		seen[point.Destination] = true
		if point.Destination == "/var/run/docker.sock" && point.Type == mount.TypeBind && point.Source == "/var/run/docker.sock" {
			socketRewritten = true
			continue
		}
		if err := execution.verifyMount(point); err != nil {
			return err
		}
	}
	if socketRewritten {
		return errMacRunnerSocketRewrite
	}
	return nil
}

func (execution *macRunnerExecution) verifyMount(point container.MountPoint) error {
	switch point.Destination {
	case execution.prepared.root:
		if point.Type == mount.TypeBind && (point.Source == execution.prepared.root || point.Source == "/host_mnt"+execution.prepared.root) {
			return nil
		}
	case "/var/run/docker.sock":
		if point.Type == mount.TypeBind && point.Source == "/var/run/docker.sock.raw" {
			return nil
		}
	case "/root/.cache/go-build", "/go/pkg/mod":
		for _, volume := range execution.prepared.service.Volumes {
			if volume.Target == point.Destination && point.Type == mount.TypeVolume && point.Name == volume.Source {
				return nil
			}
		}
	case "/tmp/tack-test":
		if err := execution.temporaryVolume(point); err != nil {
			return err
		}
		execution.anonymousVolumes[point.Name] = true
		execution.write("anonymous-volume.txt", macRunnerIdentities(execution.anonymousVolumes))
		return nil
	}
	return fmt.Errorf("runner mount %s has unexpected type or identity", point.Destination)
}

func (execution *macRunnerExecution) temporaryVolume(point container.MountPoint) error {
	if point.Destination != "/tmp/tack-test" || point.Type != mount.TypeVolume || point.Name == "" {
		return errors.New("runner temporary mount must report an anonymous volume identity")
	}
	if execution.baseline.Volumes[point.Name] || execution.cacheVolumes[point.Name] {
		return fmt.Errorf("refuse baseline or cache temporary volume %s", point.Name)
	}
	return nil
}

func (execution *macRunnerExecution) expectedEnvironment() []string {
	values := make(map[string]string)
	for _, value := range execution.imageEnvironment {
		name, contents, _ := strings.Cut(value, "=")
		values[name] = contents
	}
	for name, value := range execution.prepared.service.Environment {
		values[name] = *value
	}
	result := make([]string, 0, len(values))
	for name, value := range values {
		result = append(result, name+"="+value)
	}
	slices.Sort(result)
	return result
}
