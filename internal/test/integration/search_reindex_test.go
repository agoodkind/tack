package integration

import (
	"testing"
	"time"

	searchadapter "goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/ops"
)

func TestSearchReindexIndexesExistingNodes(t *testing.T) {
	env := SetupTestEnv(t)
	registerOpsOrg(t, env)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	env.Ops.Cfg = cfg
	projectID := createTestProject(t, env)
	// env.NodeSvc indexes through the no-op searcher, so Meilisearch never sees
	// this issue until the backfill runs.
	issueID := createTestIssue(t, env, projectID, "Reindexed quarry ticket")
	client := searchadapter.New(cfg.MeiliURL, cfg.MeiliMasterKey)
	// The backfill writes into the index the server configures at startup;
	// configure it here so the org filter below works on a fresh stack.
	if err := client.EnsureIndex("nodes", []string{"org_id", "node_type"}, []string{"name", "props"}); err != nil {
		t.Fatalf("ensure index: %v", err)
	}

	operation, ok := ops.Get("search-reindex")
	if !ok {
		t.Fatal("unknown op: search-reindex")
	}
	if err := operation.Run(env.Ctx, env.Ops); err != nil {
		t.Fatalf("search-reindex: %v", err)
	}

	found := waitFor(t, 10*time.Second, func() bool {
		docs, _, err := client.Search(env.Ctx, "nodes", "quarry", map[string]string{"org_id": env.OrgID.String()})
		if err != nil {
			return false
		}
		for _, doc := range docs {
			if doc.ID == issueID.String() {
				return true
			}
		}
		return false
	})
	if !found {
		t.Fatalf("issue %s not searchable after reindex", issueID)
	}
}
