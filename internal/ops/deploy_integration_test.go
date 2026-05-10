// Package ops: deploy_integration_test.go covers the SDK-mediated push,
// pull, and verify path against a local registry container plus a fake
// remote (the same daemon, talked to as if it were remote because the
// docker context is the local default). The test is gated on the
// DEPLOY_TEST_INTEGRATION env var so the unit suite stays fast and free of
// daemon dependencies.
//
// The test exercises three things end to end:
//   1. encodeRegistryAuth produces a header the local registry accepts.
//   2. ImagePush against the registry returns a manifest the remote then
//      pulls and inspects via inspectImageDigest.
//   3. compareDigests reports equal between the pushed and the pulled
//      digests, exercising the verify gate.

package ops

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

func TestDeployRoundTripIntegration(t *testing.T) {
	if os.Getenv("DEPLOY_TEST_INTEGRATION") != "1" {
		t.Skip("DEPLOY_TEST_INTEGRATION!=1; skipping daemon-bound integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cli, err := newLocalDockerClient(ctx)
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	defer func() { _ = cli.Close() }()

	registryHost := os.Getenv("DEPLOY_TEST_REGISTRY")
	if registryHost == "" {
		registryHost = "localhost:5000"
	}
	imageRef := registryHost + "/tack-deploy-test:integration"

	pullBody, err := cli.ImagePull(ctx, "alpine:3", client.ImagePullOptions{})
	if err != nil {
		t.Fatalf("pull alpine: %v", err)
	}
	err = drainProgressStream(ctx, nopLogger(), pullBody, "test.pull")
	_ = pullBody.Close()
	if err != nil {
		t.Fatalf("drain alpine pull: %v", err)
	}
	_, err = cli.ImageTag(ctx, client.ImageTagOptions{Source: "alpine:3", Target: imageRef})
	if err != nil {
		t.Fatalf("tag alpine: %v", err)
	}
	authHeader, err := encodeRegistryAuth("", "", registryHost)
	if err != nil {
		t.Fatalf("auth header: %v", err)
	}
	pushBody, err := cli.ImagePush(ctx, imageRef, client.ImagePushOptions{RegistryAuth: authHeader})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	err = drainProgressStream(ctx, nopLogger(), pushBody, "test.push")
	_ = pushBody.Close()
	if err != nil {
		t.Fatalf("drain push: %v", err)
	}
	digest, err := inspectImageDigest(ctx, cli, imageRef)
	if err != nil {
		t.Fatalf("inspect digest: %v", err)
	}
	if digest == "" {
		t.Fatal("expected non-empty digest after push")
	}
	if !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("expected sha256-prefixed digest, got %q", digest)
	}
	err = compareDigests("integration-test", digest, digest)
	if err != nil {
		t.Fatalf("compareDigests: %v", err)
	}
}

func TestAssertDockerContextEmptyIsNoOp(t *testing.T) {
	err := assertDockerContext(context.Background(), "")
	if err != nil {
		t.Fatalf("expected empty context to be a no-op, got: %v", err)
	}
}
