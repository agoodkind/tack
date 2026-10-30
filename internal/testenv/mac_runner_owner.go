package testenv

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

var (
	macRunnerName        = regexp.MustCompile(`^tack-mac-runner-[0-9a-f]{24}$`)
	macRunnerContainerID = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func verifiedNativeMacRunner(ctx context.Context, docker *client.Client, id, hostname string) (bool, error) {
	if id == "" {
		return false, nil
	}
	result, err := macObserve(ctx, func(observation context.Context) (client.ContainerInspectResult, error) {
		return docker.ContainerInspect(observation, id, client.ContainerInspectOptions{})
	})
	if err != nil {
		return false, macRunnerFailure("inspect current runner "+id, err)
	}
	inspection := result.Container
	if inspection.Config == nil {
		return false, errors.New("current runner inspection omitted configuration")
	}
	labels := inspection.Config.Labels
	if labels[macRunnerLabel] == "" && labels[macRunnerLabel+".native"] == "" {
		return false, nil
	}
	if err := verifyNativeMacIdentity(ctx, inspection, id, hostname); err != nil {
		return false, macRunnerFailure("verify marked native runner "+id, err)
	}
	if err := verifyNativeMacAdmission(ctx, docker, inspection); err != nil {
		return false, macRunnerFailure("verify native runner admission "+id, err)
	}
	return true, nil
}

func verifyNativeMacIdentity(ctx context.Context, inspection container.InspectResponse, id, hostname string) error {
	configuration := inspection.Config
	labels := configuration.Labels
	name := strings.TrimPrefix(inspection.Name, "/")
	if !macRunnerContainerID.MatchString(id) || inspection.ID != id || configuration.Hostname != hostname {
		return errors.New("current hostname and full runner identity differ")
	}
	if name != labels[macRunnerLabel] || !macRunnerName.MatchString(name) || labels[macRunnerLabel+".native"] != "true" {
		return errors.New("native runner name and labels differ")
	}
	project := labels[macRunnerProjectLabel]
	image := labels[macRunnerLabel+".image"]
	root := labels[macRunnerLabel+".root"]
	if project == "" || labels[macRunnerServiceLabel] != "tests" || !macRunnerImage.MatchString(image) || inspection.Image != image || configuration.Image != image {
		return errors.New("native runner project, service or immutable image differs")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return macRunnerFailure("resolve native runner root "+root, err)
	}
	if !filepath.IsAbs(root) || resolved != root || configuration.WorkingDir != root {
		return errors.New("native runner working directory differs from its resolved absolute root")
	}
	return verifyNativeMacCommand(ctx, inspection, project, image, root)
}

func verifyNativeMacCommand(ctx context.Context, inspection container.InspectResponse, project, image, root string) error {
	configuration := inspection.Config
	command := configuration.Cmd
	if len(command) < 3 || command[0] != "test" || command[len(command)-1] != "./internal/test/integration/..." {
		return errors.New("native runner does not execute the supported Go test package")
	}
	validated, _, err := macRunnerArguments(command[1 : len(command)-1])
	if err != nil {
		return err
	}
	if !slices.Equal(command, validated) || !slices.Equal(configuration.Entrypoint, []string{"/usr/local/go/bin/go"}) {
		return errors.New("native runner Go command or entrypoint differs")
	}
	var prepared macRunnerPreparation
	prepared.root, prepared.options.Image, prepared.command = root, image, validated
	if err := prepared.loadComposeInputs(ctx, project, ""); err != nil {
		return err
	}
	if prepared.project.Name != project {
		return errors.New("native runner loaded Compose project differs")
	}
	return verifyNativeMacMounts(inspection, &prepared)
}

func verifyNativeMacMounts(inspection container.InspectResponse, prepared *macRunnerPreparation) error {
	var execution macRunnerExecution
	execution.prepared = prepared
	execution.cacheVolumes = make(map[string]bool)
	for _, volume := range prepared.service.Volumes {
		if volume.Target == "/root/.cache/go-build" || volume.Target == "/go/pkg/mod" {
			execution.cacheVolumes[volume.Source] = true
		}
	}
	if len(inspection.Mounts) != 5 {
		return errors.New("native runner must have exactly five writable mounts")
	}
	seen := make(map[string]bool)
	for _, point := range inspection.Mounts {
		if seen[point.Destination] || !point.RW {
			return fmt.Errorf("native runner mount %s repeats or is read-only", point.Destination)
		}
		seen[point.Destination] = true
		if point.Destination == "/tmp/tack-test" {
			if point.Type != mount.TypeVolume || !macRunnerAnonymousMount(inspection, point) || execution.cacheVolumes[point.Name] {
				return errors.New("native runner temporary volume is not anonymous")
			}
			continue
		}
		if point.Destination == "/var/run/docker.sock" && point.Type == mount.TypeBind && point.Source == "/var/run/docker.sock" {
			continue
		}
		if err := execution.verifyMount(point); err != nil {
			return macRunnerFailure(fmt.Sprintf("native runner inspected mount source=%q target=%q type=%q", point.Source, point.Destination, point.Type), err)
		}
	}
	return nil
}
