package ops

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/moby/moby/client"
)

const (
	// ComposeProjectLabel is the Docker Compose label key for a container's
	// project.
	ComposeProjectLabel = "com.docker.compose.project"
	// ComposeServiceLabel is the Docker Compose label key for a container's
	// service.
	ComposeServiceLabel = "com.docker.compose.service"
)

// serviceReplica pairs a container and its Docker Compose service with the
// expected image reference.
type serviceReplica struct {
	Service   string
	Container string
	Image     string
}

// serviceReplicas lists running and stopped containers with Docker Compose
// labels matching project and each target service. It returns an error if
// Docker cannot list the containers or a target service does not have any named
// containers.
func serviceReplicas(ctx context.Context, cli *client.Client, project string, targets []deployVerifyTarget) ([]serviceReplica, error) {
	var replicas []serviceReplica
	for _, target := range targets {
		filters := client.Filters{}.
			Add("label", ComposeProjectLabel+"="+project).
			Add("label", ComposeServiceLabel+"="+target.Service)
		listed, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filters})
		if err != nil {
			slog.ErrorContext(ctx, "ops.deploy.verify.replica_list_failed",
				slog.String("service", target.Service), slog.String("err", err.Error()))
			return nil, fmt.Errorf("list Docker Compose replicas for project %s and service %s: %w", project, target.Service, err)
		}
		names := make([]string, 0, len(listed.Items))
		for _, item := range listed.Items {
			for _, name := range item.Names {
				names = append(names, strings.TrimPrefix(name, "/"))
			}
		}
		if len(names) == 0 {
			err := fmt.Errorf("cannot find any named Docker Compose replicas for project %s and service %s", project, target.Service)
			slog.ErrorContext(ctx, "ops.deploy.verify.replica_missing",
				slog.String("service", target.Service), slog.String("err", err.Error()))
			return nil, err
		}
		slices.Sort(names)
		for _, name := range names {
			replicas = append(replicas, serviceReplica{Service: target.Service, Container: name, Image: target.Image})
		}
	}
	return replicas, nil
}
