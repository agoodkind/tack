package testenv

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
)

const macRunnerComposeFile = "docker-compose.test.yml"

func (prepared *macRunnerPreparation) loadCompose(ctx context.Context) error {
	path := filepath.Join(prepared.root, macRunnerComposeFile)
	contents, err := os.ReadFile(path)
	if err != nil {
		return macRunnerFailure("read test Compose file "+path, err)
	}
	if err := validateMacRunnerCompose(contents); err != nil {
		return macRunnerFailure("validate tests service in "+path, err)
	}
	environment := types.Mapping{
		"TACK_TEST_ROOT":            prepared.root,
		"TACK_SEARCH_INTEGRATION":   "1",
		"TACK_TEST_SOURCE_REVISION": "",
		"TACK_SEARCH_CLUSTER":       os.Getenv("TACK_SEARCH_CLUSTER"),
	}
	project, err := loader.LoadWithContext(ctx, types.ConfigDetails{
		WorkingDir:  prepared.root,
		ConfigFiles: []types.ConfigFile{{Filename: path, Content: contents}},
		Environment: maps.Clone(environment),
	}, func(options *loader.Options) {
		options.SetProjectName(loader.NormalizeProjectName(filepath.Base(prepared.root)), false)
		if name := os.Getenv("COMPOSE_PROJECT_NAME"); name != "" {
			options.SetProjectName(name, true)
		}
		options.Profiles = []string{"runner"}
		options.SelectedServices = []string{"tests"}
		options.SkipResolveEnvironment = true
		options.SkipResolveLabels = true
		options.SkipExtends = true
		options.SkipInclude = true
	})
	if err != nil {
		return macRunnerFailure("load tests service from "+path, err)
	}
	service, present := project.Services["tests"]
	if !present {
		return fmt.Errorf("%s must define the tests service in profile runner", path)
	}
	if service.WorkingDir != prepared.root {
		return fmt.Errorf("tests working directory must equal checkout root %s", prepared.root)
	}
	if err := prepared.configureMacRunnerVolumes(&service, project); err != nil {
		return err
	}
	if len(service.Environment) != len(environment) || service.Environment["TMPDIR"] == nil || *service.Environment["TMPDIR"] != "/tmp/tack-test" {
		return errors.New("tests environment must define TMPDIR and the supported test inputs")
	}
	for name, value := range environment {
		if name == "TACK_TEST_ROOT" {
			continue
		}
		if service.Environment[name] == nil || *service.Environment[name] != value {
			return fmt.Errorf("tests environment must preserve explicit %s", name)
		}
	}
	service.Build = nil
	service.Image = prepared.options.Image
	service.PullPolicy = "never"
	service.Entrypoint = types.ShellCommand{"/usr/local/go/bin/go"}
	service.Command = slices.Clone(prepared.command)
	prepared.project, prepared.service = project, service
	return nil
}

func (prepared *macRunnerPreparation) configureMacRunnerVolumes(service *types.ServiceConfig, project *types.Project) error {
	if len(service.Volumes) != 5 {
		return errors.New("tests service must define the source, socket, build cache, module cache and temporary volume")
	}
	seen := make(map[string]bool)
	for index := range service.Volumes {
		volume := &service.Volumes[index]
		if seen[volume.Target] || volume.ReadOnly {
			return fmt.Errorf("tests volume %s must be unique and writable", volume.Target)
		}
		seen[volume.Target] = true
		switch volume.Target {
		case prepared.root:
			if volume.Type != types.VolumeTypeBind || volume.Source != prepared.root {
				return errors.New("tests source must bind the checkout at its resolved root")
			}
		case "/var/run/docker.sock":
			if volume.Type != types.VolumeTypeBind || volume.Source != "/var/run/docker.sock" {
				return errors.New("tests socket must bind /var/run/docker.sock")
			}
			volume.Source = "/var/run/docker.sock.raw"
		case "/root/.cache/go-build", "/go/pkg/mod":
			cache, present := project.Volumes[volume.Source]
			if volume.Type != types.VolumeTypeVolume || !present || cache.Name == "" {
				return fmt.Errorf("tests cache %s must use a declared named volume", volume.Target)
			}
			volume.Source = cache.Name
		case "/tmp/tack-test":
			if volume.Type != types.VolumeTypeVolume || volume.Source != "" {
				return errors.New("tests temporary directory must use an anonymous volume")
			}
		default:
			return fmt.Errorf("unsupported tests volume target %s", volume.Target)
		}
	}
	return nil
}
