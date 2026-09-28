package integration

import (
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/testenv"
)

// The audited commands provision these fixed names. Every control test
// deletes them before it starts and after it ends.
const (
	searchControlIndex      = "node-pages-1"
	searchControlAlias      = "node-pages"
	searchControlOtherIndex = "other-index"
)

// searchControl runs the audited search commands against the shared engine.
type searchControl struct {
	fixture testenv.OpenSearchFixture
	adapter *search.Adapter
	client  *opensearchapi.Client
	run     func(args ...string) error
}

func newSearchControl(t *testing.T) searchControl {
	t.Helper()
	fixture := testenv.OpenSearch(t)
	factory := cli.System(searchControlConfig(t, fixture))
	pool, err := pgxpool.New(t.Context(), testenv.Ledger(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	t.Cleanup(factory.CloseAuditOutbox)
	adapter, client := openSearchClientsFor(t, fixture)
	control := searchControl{
		fixture: fixture, adapter: adapter, client: client,
		run: func(args ...string) error {
			root := searchCommandRoot(factory)
			root.SetArgs(append([]string{
				"--execute", "--operator-id", "019dd226-440e-729a-a442-281aaf73ca30",
				"--operator-email", "operator@example.com", "--operator-name", "Search Test",
			}, args...))
			return root.Execute()
		},
	}
	control.reset(t)
	t.Cleanup(func() { control.reset(t) })
	return control
}

// reset deletes the indexes the control tests create; deleting an index
// removes its aliases.
func (control searchControl) reset(t *testing.T) {
	t.Helper()
	deleteNativeIndex(t, control.client, searchControlIndex)
	deleteNativeIndex(t, control.client, searchControlOtherIndex)
}

// provisionFromEmpty deletes the two control indexes and runs the audited
// provision command.
func (control searchControl) provisionFromEmpty(t *testing.T) {
	t.Helper()
	control.reset(t)
	if err := control.run("ops", "search", "provision"); err != nil {
		t.Fatalf("provision: %v", err)
	}
}

// indexUUID returns the OpenSearch UUID of one physical index. Recreating the
// index changes its UUID.
func (control searchControl) indexUUID(t *testing.T, index string) string {
	t.Helper()
	response, err := control.client.Indices.Settings.Get(t.Context(), &opensearchapi.SettingsGetReq{Indices: []string{index}})
	if err != nil {
		t.Fatal(err)
	}
	entry, exists := response.GetIndices()[index]
	if !exists {
		t.Fatalf("OpenSearch index %s settings are absent", index)
	}
	var settings struct {
		Index struct {
			UUID string `json:"uuid"`
		} `json:"index"`
	}
	if err := json.Unmarshal(entry.Settings, &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Index.UUID == "" {
		t.Fatalf("OpenSearch index %s has no UUID", index)
	}
	return settings.Index.UUID
}
