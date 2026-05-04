package repair

import (
	searchadapter "goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/config"
	domainsearch "goodkind.io/tack/internal/domain/search"
)

// NewSearcher builds the repair search dependency from the shared config.
func NewSearcher(cfg *config.Config) domainsearch.Searcher {
	if cfg == nil {
		return searchadapter.Noop{}
	}
	client := searchadapter.New(cfg.MeiliURL, cfg.MeiliMasterKey)
	if err := client.EnsureIndex("nodes", []string{"org_id", "node_type"}); err != nil {
		return searchadapter.Noop{}
	}
	return client
}
