package integration

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/datagen"
	appruntime "goodkind.io/tack/internal/runtime"
)

func auditedDatagenConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := harnessConfig(t)
	cfg.AuditKafkaBrokers = ""
	cfg.AuditWriterDSN = cfg.DatabaseURL
	cfg.AuditAllowUnrecorded = false
	cfg.AuditReadFlushInterval = 10 * time.Millisecond
	return cfg
}

func TestDatagenRerunReusesPublicNodeIDs(t *testing.T) {
	cfg := auditedDatagenConfig(t)
	seed := nextHarnessSeed()
	options := datagen.SeedRunOptions{Scale: "small", Seed: seed, Commit: true}
	first, err := datagen.RunSeed(t.Context(), cfg, options)
	if err != nil {
		t.Fatal(err)
	}
	before := generatedProjectID(t, cfg, seed)
	second, err := datagen.RunSeed(t.Context(), cfg, options)
	if err != nil {
		t.Fatal(err)
	}
	after := generatedProjectID(t, cfg, seed)
	if first.Created == 0 || second.Created != 0 || second.Reused != first.Created+first.Reused || before != after {
		t.Fatalf("rerun changed corpus: first=%+v second=%+v project_before=%s project_after=%s", first, second, before, after)
	}
	t.Logf("rerun reused=%d project=%s", second.Reused, after)
}

func TestDatagenSoakSeedsFreshPublicCorpus(t *testing.T) {
	cfg := auditedDatagenConfig(t)
	seed := nextHarnessSeed()
	result, err := datagen.RunSoak(t.Context(), t.Context(), cfg, datagen.SoakOptions{
		Duration: time.Minute, Rate: 60, MaxOps: 1, Scale: "small", Seed: seed, Commit: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Operations != 1 || result.StopReason != "max_ops" {
		t.Fatalf("fresh soak did not execute its public operation: %+v", result)
	}
	project := generatedProjectID(t, cfg, seed)
	reused, err := datagen.RunSeed(t.Context(), cfg, datagen.SeedRunOptions{Scale: "small", Seed: seed, Commit: true})
	if err != nil {
		t.Fatal(err)
	}
	if reused.Created != 0 || reused.Reused == 0 || reused.Issues != 6 {
		t.Fatalf("fresh soak did not store a reusable complete corpus: %+v", reused)
	}
	t.Logf("fresh soak operations=%d reused=%d project=%s", result.Operations, reused.Reused, project)
}

func generatedProjectID(t *testing.T, cfg *config.Config, seed int64) string {
	t.Helper()
	scale, err := datagen.ParseScale("small")
	if err != nil {
		t.Fatal(err)
	}
	identities, err := datagen.BootstrapIdentities(t.Context(), cfg, seed, scale)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := appruntime.BuildGraph(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close()
	workspace := identities.Workspaces[0]
	result, err := datagen.NewDriver(graph, false, seed).Call(t.Context(), workspace.Actors[0].Token, "tack_get_project", datagen.ToolArguments{
		WorkspaceReference: workspace.Slug, NodeID: "Q0101",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(result.RawID()); err != nil {
		t.Fatal(fmt.Errorf("generated project has no raw UUID: %w", err))
	}
	return result.RawID()
}
