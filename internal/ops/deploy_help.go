// Package ops: deploy_help.go owns the deploy family usage banner so
// command_help.go stays focused on the rest of the families. Listed verbs
// match the dispatcher in deploy.go.

package ops

import (
	"fmt"
	"os"
)

func printDeployUsage() {
	fmt.Fprintln(os.Stderr, "usage: ./server ops deploy [build|push|pull|up|verify]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "subcommands (default flow runs every step in order):")
	fmt.Fprintln(os.Stderr, "  build   compile the tack-server image locally and tag it")
	fmt.Fprintln(os.Stderr, "  push    ship the image to the configured registry, or save+load over context in offline mode")
	fmt.Fprintln(os.Stderr, "  pull    fetch the new image on the remote (no-op in offline mode)")
	fmt.Fprintln(os.Stderr, "  up      run docker compose up -d on the remote")
	fmt.Fprintln(os.Stderr, "  verify  assert the running containers' image digest matches what was pushed")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "env vars:")
	fmt.Fprintln(os.Stderr, "  TACK_DEPLOY_REGISTRY            image repository (default ghcr.io/agoodkind/tack)")
	fmt.Fprintln(os.Stderr, "  TACK_DEPLOY_MODE                registry|offline (default registry)")
	fmt.Fprintln(os.Stderr, "  TACK_DEPLOY_DOCKER_CONTEXT      docker context for remote ops (default tack)")
	fmt.Fprintln(os.Stderr, "  TACK_DEPLOY_REMOTE_COMPOSE_FILE compose path on the remote (default /root/tack/docker-compose.yml)")
	fmt.Fprintln(os.Stderr, "  TACK_DEPLOY_TIMEOUT_SECONDS     overall deploy timeout (default 600)")
	fmt.Fprintln(os.Stderr, "  GHCR_USERNAME, GHCR_TOKEN       required when mode is registry")
}
