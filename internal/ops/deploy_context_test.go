package ops

import (
	"context"
	"strings"
	"testing"

	"goodkind.io/tack/internal/config"
)

// A deploy with no docker context named refuses before it reads the git
// commit or touches a daemon, and the error names the variable (TACK-268).
func TestNewDeployContextRefusesEmptyDockerContext(t *testing.T) {
	cfg := &config.Config{
		DeployMode:          deployModeRegistry,
		DeployRegistry:      "ghcr.io/example/tack",
		DeployDockerContext: "   ",
	}
	dctx, err := newDeployContext(context.Background(), cfg)
	if err == nil {
		t.Fatalf("newDeployContext returned no error; docker context %q", dctx.dockerCtx)
	}
	if !strings.Contains(err.Error(), "TACK_DEPLOY_DOCKER_CONTEXT") {
		t.Fatalf("error %q does not name TACK_DEPLOY_DOCKER_CONTEXT", err.Error())
	}
}
