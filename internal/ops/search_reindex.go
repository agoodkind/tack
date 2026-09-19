package ops

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	searchadapter "goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
)

// searchReindexPageSize is the number of views read and indexed per batch.
const searchReindexPageSize = 500

func init() {
	Register(Operation{
		Name:        "search-reindex",
		Audit:       audit.Spec{Verb: string(audit.VerbOpsSearchReindex), Mutates: true},
		Description: "Rebuild the Meilisearch nodes index from FoundationDB views",
		Run:         runSearchReindex,
	})
}

// runSearchReindex indexes every view of every org. Unlike reindex, it returns
// the first error instead of logging and continuing, so an operator sees a
// partial backfill as a failure.
func runSearchReindex(ctx context.Context, env *Env) error {
	searcher := searchadapter.New(env.Cfg.MeiliURL, env.Cfg.MeiliMasterKey)
	if err := searchadapter.EnsureNodesIndex(searcher); err != nil {
		env.Log.ErrorContext(ctx, "search_reindex.ensure_index_failed", slog.String("err", err.Error()))
		return fmt.Errorf("search reindex: ensure nodes index: %w", err)
	}
	orgIDs, err := listOrgIDs(ctx, env)
	if err != nil {
		env.Log.ErrorContext(ctx, "search_reindex.list_orgs_failed", slog.String("err", err.Error()))
		return fmt.Errorf("search reindex: list orgs: %w", err)
	}
	for orgID := range orgIDs {
		if err := reindexOrgSearch(ctx, env, searcher, orgID); err != nil {
			env.Log.ErrorContext(ctx, "search_reindex.org_failed",
				slog.String("org_id", orgID.String()), slog.String("err", err.Error()))
			return fmt.Errorf("search reindex org %s: %w", orgID, err)
		}
	}
	return nil
}

func reindexOrgSearch(ctx context.Context, env *Env, searcher domainsearch.Searcher, orgID uuid.UUID) error {
	defs, err := env.Stores.PropertyDefs.List(ctx, orgID)
	if err != nil {
		env.Log.ErrorContext(ctx, "search_reindex.property_defs_failed", slog.String("err", err.Error()))
		return fmt.Errorf("list property defs: %w", err)
	}
	nodeTypes, err := env.Stores.NodeTypes.List(ctx, orgID)
	if err != nil {
		env.Log.ErrorContext(ctx, "search_reindex.node_types_failed", slog.String("err", err.Error()))
		return fmt.Errorf("list node types: %w", err)
	}
	orgIndexedCount := 0
	for _, nodeType := range nodeTypes {
		indexedCount, err := reindexTypeSearch(ctx, env, searcher, orgID, nodeType.TypeKey, defs)
		if err != nil {
			env.Log.ErrorContext(ctx, "search_reindex.type_failed",
				slog.String("node_type", nodeType.TypeKey), slog.String("err", err.Error()))
			return fmt.Errorf("type %s: %w", nodeType.TypeKey, err)
		}
		env.Log.DebugContext(ctx, "search_reindex.type_done",
			slog.String("org_id", orgID.String()),
			slog.String("node_type", nodeType.TypeKey),
			slog.Int("indexed", indexedCount))
		orgIndexedCount += indexedCount
	}
	env.Log.InfoContext(ctx, "search_reindex.org_done",
		slog.String("org_id", orgID.String()),
		slog.Int("indexed", orgIndexedCount))
	return nil
}

func reindexTypeSearch(ctx context.Context, env *Env, searcher domainsearch.Searcher, orgID uuid.UUID, typeKey string, defs []*node.PropertyDef) (int, error) {
	query := node.NodeListQuery{
		OrgID:            orgID,
		NodeType:         typeKey,
		ByProperty:       nil,
		BySourceRelation: nil,
		ByTargetRelation: nil,
		CreatedAfter:     nil,
		CreatedBefore:    nil,
		PropFilters:      nil,
		Limit:            searchReindexPageSize,
		Cursor:           "",
	}
	indexedCount := 0
	for {
		page, err := env.Stores.Views.ListPage(ctx, query)
		if err != nil {
			env.Log.ErrorContext(ctx, "search_reindex.list_page_failed", slog.String("err", err.Error()))
			return indexedCount, fmt.Errorf("list views after %d: %w", indexedCount, err)
		}
		docs := make([]*domainsearch.NodeDoc, 0, len(page.Views))
		for _, view := range page.Views {
			docs = append(docs, service.SearchDocFromView(view, defs))
		}
		if err := searcher.IndexBatch(ctx, "nodes", docs); err != nil {
			env.Log.ErrorContext(ctx, "search_reindex.index_batch_failed", slog.String("err", err.Error()))
			return indexedCount, fmt.Errorf("index batch after %d: %w", indexedCount, err)
		}
		indexedCount += len(docs)
		if page.NextCursor == "" {
			return indexedCount, nil
		}
		query.Cursor = page.NextCursor
	}
}
