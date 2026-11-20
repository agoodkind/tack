package integration

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
)

// TestDatagenSearchPreparationExportsStoredIsolatedSets executes the guarded
// command with real SQL and FoundationDB while public search is disabled.
func TestDatagenSearchPreparationExportsStoredIsolatedSets(t *testing.T) {
	cfg := harnessConfig(t)
	cfg.DatagenAllowTarget = "local"
	cfg.SearchPublicEnabled = false
	seed := nextHarnessSeed()
	scale, err := datagen.ParseScale("small")
	if err != nil {
		t.Fatal(err)
	}
	identities, err := datagen.BootstrapIdentities(
		WithTestOperation(t, t.Context()),
		cfg,
		seed,
		scale,
	)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	factory := cli.System(cfg)
	factory.Out = &output
	pool, err := pgxpool.New(t.Context(), cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	t.Cleanup(factory.CloseAuditOutbox)
	root := searchCommandRoot(factory)
	root.SetArgs(
		searchPreparationArguments([]string{
			"--prepare-only",
			"--commit",
			"--seed",
			strconv.FormatInt(seed, 10),
			"--corpus",
			"testdata/search_semantic_corpus.json",
		}),
	)
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("prepare fixture: %v; output=%s", err, output.String())
	}
	var envelope struct {
		Result struct {
			Verified bool                   `json:"verified"`
			Manifest datagen.SearchManifest `json:"manifest"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatalf("decode fixture result: %v; output=%s", err, output.String())
	}
	manifest := envelope.Result.Manifest
	if envelope.Result.Verified || cfg.SearchPublicEnabled {
		t.Fatal("preparation claimed search acceptance or enabled public search")
	}
	if manifest.OrgID != identities.Workspaces[0].OrgID || len(
		manifest.Nodes,
	) != 194 || len(manifest.Cases) != 8 {
		t.Fatalf("unexpected fixture manifest: %+v", manifest)
	}
	stores, err := fdbadapter.NewStores(cfg.FDBClusterFile, cfg.FDBTransactionTimeout, nil)
	if err != nil {
		t.Fatal(err)
	}
	requireStoredSearchManifest(
		t,
		stores,
		manifest,
		node.WorkspaceID(manifest.OrgID, identities.Workspaces[0].Slug),
	)
	typesBefore, err := stores.NodeTypes.List(t.Context(), manifest.OrgID)
	if err != nil {
		t.Fatal(err)
	}
	propertiesBefore, err := stores.PropertyDefs.List(t.Context(), manifest.OrgID)
	if err != nil {
		t.Fatal(err)
	}
	invalidCorpus := filepath.Join(t.TempDir(), "invalid-corpus.json")
	if err := os.WriteFile(invalidCorpus, []byte("[]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seedText := strconv.FormatInt(seed, 10)
	writerDSN := cfg.AuditWriterDSN
	for _, refusal := range []struct {
		name, marker, message string
		arguments             []string
	}{
		{
			"missing-seed",
			"local",
			"--seed greater than 0",
			[]string{
				"--prepare-only",
				"--commit",
				"--corpus",
				"testdata/search_semantic_corpus.json",
			},
		},
		{
			"seed-without-preparation",
			"local",
			"require --prepare-only",
			[]string{"--commit", "--seed", seedText},
		},
		{
			"unmarked-target",
			"",
			"ALLOW_TARGET",
			[]string{"--prepare-only", "--commit", "--seed", seedText, "--corpus", invalidCorpus},
		},
		{
			"production-target",
			"qa",
			"identifies production",
			[]string{"--prepare-only", "--commit", "--seed", seedText, "--corpus", invalidCorpus},
		},
		{
			"invalid-corpus",
			"local",
			"approved SHA256",
			[]string{"--prepare-only", "--commit", "--seed", seedText, "--corpus", invalidCorpus},
		},
	} {
		t.Run(
			refusal.name,
			func(t *testing.T) {
				cfg.DatagenAllowTarget = refusal.marker
				cfg.AuditWriterDSN = writerDSN
				if refusal.name == "production-target" {
					cfg.AuditWriterDSN = "postgres://writer@tack.home.goodkind.io:5433/yugabyte"
				}
				output.Reset()
				command := searchCommandRoot(factory)
				command.SetArgs(searchPreparationArguments(refusal.arguments))
				err := command.ExecuteContext(t.Context())
				if err == nil || !strings.Contains(err.Error(), refusal.message) {
					t.Fatalf("preparation refusal differs: %v", err)
				}
				typesAfter, err := stores.NodeTypes.List(t.Context(), manifest.OrgID)
				if err != nil || len(typesAfter) != len(typesBefore) {
					t.Fatalf("refused preparation changed node types: %v", err)
				}
				propertiesAfter, err := stores.PropertyDefs.List(t.Context(), manifest.OrgID)
				if err != nil || len(propertiesAfter) != len(propertiesBefore) {
					t.Fatalf("refused preparation changed projections: %v", err)
				}
			},
		)
	}
	cfg.DatagenAllowTarget = "local"
	cfg.AuditWriterDSN = writerDSN
	requirePreparationAudit(t, pool)
}
