package integration

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

const pagedIssueCount = 150

func TestListPageReturnsEveryIssueOnce(t *testing.T) {
	env := SetupTestEnv(t)
	projectID := createTestProject(t, env)
	for i := 0; i < pagedIssueCount; i++ {
		createTestIssue(t, env, projectID, fmt.Sprintf("paged issue %d", i))
	}
	projectRaw, _ := json.Marshal(projectID.String())
	query := node.NodeListQuery{
		OrgID:      env.OrgID,
		NodeType:   "issue",
		ByProperty: &node.PropertyMatch{PropName: "scope_id", Value: projectRaw},
		Limit:      25,
	}

	seen := map[uuid.UUID]bool{}
	pageCount := 0
	for {
		page, err := env.Stores.Views.ListPage(env.Ctx, query)
		if err != nil {
			t.Fatalf("list page %d: %v", pageCount, err)
		}
		pageCount++
		for _, view := range page.Views {
			if seen[view.ID] {
				t.Fatalf("issue %s returned twice", view.ID)
			}
			seen[view.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		query.Cursor = page.NextCursor
	}

	if len(seen) != pagedIssueCount {
		t.Fatalf("saw %d issues, want %d", len(seen), pagedIssueCount)
	}
	if pageCount != 6 {
		t.Fatalf("page count = %d, want 6", pageCount)
	}
}
