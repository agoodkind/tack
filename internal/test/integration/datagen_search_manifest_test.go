package integration

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/datagen"
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
	identities, err := datagen.BootstrapIdentities(t.Context(), cfg, seed, scale)
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
	root.SetArgs([]string{
		"--execute", "--output", "json", "--operator-id", "019dd226-440e-729a-a442-281aaf73ca30",
		"--operator-email", "operator@example.com", "--operator-name", "Search Test",
		"ops", "qa", "datagen", "search", "--prepare-only", "--commit",
		"--seed", strconv.FormatInt(seed, 10), "--corpus", "testdata/search_semantic_corpus.json",
	})
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
	if manifest.OrgID != identities.Workspaces[0].OrgID || len(manifest.Nodes) != 194 || len(manifest.Cases) != 8 {
		t.Fatalf("unexpected fixture manifest: %+v", manifest)
	}
	stores, err := fdbadapter.NewStores(cfg.FDBClusterFile, cfg.FDBTransactionTimeout, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Types) != 3 || len(manifest.Properties) != 6 {
		t.Fatal("manifest omits created metadata identities")
	}
	for _, exported := range manifest.Types {
		stored, err := stores.NodeTypes.Get(t.Context(), manifest.OrgID, exported.ID)
		if err != nil || stored == nil || stored.TypeKey != exported.Key {
			t.Fatalf("read exported node type %s: %v", exported.ID, err)
		}
	}
	for _, exported := range manifest.Properties {
		stored, err := stores.PropertyDefs.Get(t.Context(), manifest.OrgID, exported.ID)
		if err != nil || stored == nil || stored.Name != exported.Key || stored.Search == nil || stored.Search.Include != exported.Included {
			t.Fatalf("read exported projection %s: %v", exported.ID, err)
		}
	}
	byType := make(map[string]map[uuid.UUID]bool)
	for _, exported := range manifest.Nodes {
		stored, err := stores.Nodes.Get(t.Context(), manifest.OrgID, exported.ID)
		if err != nil || stored == nil {
			t.Fatalf("read exported node %s: %v", exported.ID, err)
		}
		if stored.Name != exported.Name || stored.NodeType != exported.NodeType || len(stored.Props) != 2 {
			t.Fatalf("exported node %s differs from stored node", exported.ID)
		}
		var text string
		if err := json.Unmarshal(stored.Props[exported.IncludedKey], &text); err != nil || len(text) != exported.TextBytes {
			t.Fatalf("exported text length for %s differs: %v", exported.ID, err)
		}
		if byType[stored.NodeType] == nil {
			byType[stored.NodeType] = make(map[uuid.UUID]bool)
		}
		if byType[stored.NodeType][exported.ID] {
			t.Fatalf("duplicate exported node %s", exported.ID)
		}
		byType[stored.NodeType][exported.ID] = true
	}
	if len(byType) != 3 {
		t.Fatalf("fixture uses %d types, want three isolated types", len(byType))
	}
	for position, testCase := range manifest.Cases {
		if position < 6 {
			if testCase.Match != "ranked_targets" || testCase.RankLimit != 25 || len(testCase.ExpectedIDs) != 2 || len(byType[testCase.NodeType]) != 162 {
				t.Fatalf("semantic case differs from target ranking contract: %+v", testCase)
			}
		} else {
			if testCase.Match != "exact" || len(testCase.ExpectedIDs) != len(byType[testCase.NodeType]) {
				t.Fatalf("exact case differs from isolated type: %+v", testCase)
			}
		}
		for _, nodeID := range testCase.ExpectedIDs {
			if !byType[testCase.NodeType][nodeID] {
				t.Fatalf("case %q exports node %s outside its type", testCase.Query, nodeID)
			}
		}
	}
	if len(manifest.Cases[6].ExpectedIDs) != 31 || len(manifest.Cases[7].ExpectedIDs) != 1 {
		t.Fatal("continuation and final-page counts differ from the declared workload")
	}
	t.Logf("prepared nodes=%d semantic=%d continuation=%d final_page=%d with disabled public search", len(manifest.Nodes), 162, 31, 1)
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
		{"missing-seed", "local", "--seed greater than 0",[]string{"--prepare-only", "--commit", "--corpus", "testdata/search_semantic_corpus.json"}},
		{"seed-without-preparation", "local", "require --prepare-only",[]string{"--commit", "--seed", seedText}},
		{"unmarked-target", "", "ALLOW_TARGET", []string{"--prepare-only", "--commit", "--seed", seedText, "--corpus", invalidCorpus}},
		{"production-target", "qa", "identifies production", []string{"--prepare-only", "--commit", "--seed", seedText, "--corpus", invalidCorpus}},
		{"invalid-corpus", "local", "approved SHA256", []string{"--prepare-only", "--commit", "--seed", seedText, "--corpus", invalidCorpus}},
	} {
		t.Run(refusal.name, func(t *testing.T) {
			cfg.DatagenAllowTarget = refusal.marker
			cfg.AuditWriterDSN = writerDSN
			if refusal.name == "production-target" {
				cfg.AuditWriterDSN = "postgres://writer@tack.home.goodkind.io:5433/yugabyte"
			}
			output.Reset()
			command := searchCommandRoot(factory)
			command.SetArgs(append([]string{
				"--execute", "--output", "json", "--operator-id", "019dd226-440e-729a-a442-281aaf73ca30",
				"--operator-email", "operator@example.com", "--operator-name", "Search Test",
				"ops", "qa", "datagen", "search",
			}, refusal.arguments...))
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
		})
	}
	cfg.DatagenAllowTarget = "local"
	cfg.AuditWriterDSN = writerDSN
	requirePreparationAudit(t, pool)
}
