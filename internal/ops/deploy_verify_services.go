// deploy_verify_services.go selects the Docker Compose services that
// `ops deploy verify` checks. A guest that runs the full stack runs app and
// audit-consumer replicas. An application-only guest runs only the app
// service, and the selection app checks that service alone.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/tack/internal/config"
)

const (
	appService           = "app"
	auditConsumerService = "audit-consumer"
)

// deployVerifyImages maps each checked service to its image name under the
// deploy registry.
var deployVerifyImages = map[string]string{
	appService:           "tack-server",
	auditConsumerService: "tack-audit-consumer",
}

// deployVerifyTarget pairs a Docker Compose service with its expected image
// reference.
type deployVerifyTarget struct {
	Service string
	Image   string
}

// parseDeployVerifyServices returns the services that the --service value
// selects, in check order. An empty value selects app and audit-consumer, and
// the value app selects only app. The command refuses every other value
// before it reads the daemon.
func parseDeployVerifyServices(ctx context.Context, selection string) ([]string, error) {
	switch selection {
	case "":
		return []string{appService, auditConsumerService}, nil
	case appService:
		return []string{appService}, nil
	default:
		err := fmt.Errorf("ops deploy verify: --service %q is not %s; omit --service to check %s and %s",
			selection, appService, appService, auditConsumerService)
		slog.ErrorContext(ctx, "ops.deploy.verify.service_invalid", slog.String("err", err.Error()))
		return nil, err
	}
}

// deployVerifyTargets returns each of services with its image reference under
// cfg.DeployRegistry. It trims whitespace from the tags and uses explicitTag
// when it is not empty, or cfg.DeployImageTag otherwise.
func deployVerifyTargets(cfg *config.Config, explicitTag string, services []string) []deployVerifyTarget {
	tag := strings.TrimSpace(explicitTag)
	if tag == "" {
		tag = strings.TrimSpace(cfg.DeployImageTag)
	}
	registry := strings.TrimSuffix(strings.TrimSpace(cfg.DeployRegistry), "/")
	targets := make([]deployVerifyTarget, 0, len(services))
	for _, service := range services {
		targets = append(targets, deployVerifyTarget{Service: service, Image: registry + "/" + deployVerifyImages[service] + ":" + tag})
	}
	return targets
}
