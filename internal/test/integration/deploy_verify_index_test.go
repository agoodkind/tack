package integration

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

// TestDeployVerifyComparesTheGivenIndexDigest starts a disposable docker:dind
// daemon with the containerd image store. Inside that daemon it pushes a
// multi-platform index image for tack-server and tack-audit-consumer to a
// local registry, pulls each by tag, and creates (without starting) the two
// containers ops deploy verify reads. The audited command passes with the
// pushed index digests and fails when the tack-server digest is the other
// image's index digest.
func TestDeployVerifyComparesTheGivenIndexDigest(t *testing.T) {
	fixture := startDeployFixture(t, testenv.ContainerdDocker)
	requireContainerdImageStore(t, fixture.cli)
	serverDigest, consumerDigest := fixture.pushIndexes(t)
	fixture.createContainers(t)

	logDeployImageReads(t, fixture.cli, "tack-app-1")

	output, err := fixture.verify(t, serverDigest, consumerDigest)
	if err != nil {
		t.Fatalf("deploy verify with the pushed index digests: %v\n%s", err, output)
	}
	t.Logf("matching digests: %s", strings.TrimSpace(output))
	output, err = fixture.verify(t, consumerDigest, consumerDigest)
	if err == nil || !strings.Contains(err.Error(), "descriptor digest differs") || !strings.Contains(err.Error(), "tack-app-1") {
		t.Fatalf("deploy verify with the other index digest for tack-server = %v, want a refusal for tack-app-1\n%s", err, output)
	}
	t.Logf("mismatching digest refused: %v", err)
}

// runOpsDeployVerify runs the audited ops deploy verify command with the two
// digest flags and the real operator outbox.
func runOpsDeployVerify(t *testing.T, cfg *config.Config, ledgerDSN, serverDigest, consumerDigest string) (string, error) {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), ledgerDSN)
	if err != nil {
		t.Fatalf("open the ledger pool: %v", err)
	}
	defer pool.Close()
	factory := cli.System(cfg)
	var output bytes.Buffer
	factory.Out = &output
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	defer factory.CloseAuditOutbox()
	root := searchCommandRoot(factory)
	root.SetContext(t.Context())
	root.SetArgs([]string{
		"--execute", "--operator-id", "019dd226-440e-729a-a442-281aaf73ca30",
		"--operator-email", "operator@example.com", "--operator-name", "Deploy Verify Test",
		"ops", "deploy", "verify", "--tack-server-digest", serverDigest, "--tack-audit-consumer-digest", consumerDigest,
	})
	runErr := root.Execute()
	return output.String(), runErr
}
