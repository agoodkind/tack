package runtime

import (
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	searchadapter "goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/service"
)

func newConfiguredModelRepair(
	cfg *config.Config,
	stores *fdbadapter.Stores,
	adapter *searchadapter.Adapter,
	source clock.Clock,
	repairSettings config.SearchModelRepairSettings,
) *service.SearchModelRepair {
	return service.NewSearchModelRepair(
		service.SearchModelRepairPorts{
			Engine:  adapter,
			Records: stores.SearchModelRepairs(),
			Index:   stores,
			Audit:   SearchModelRepairAudit(stores),
		},
		source,
		repairSettings,
		service.SearchModelRepairGates{
			Enabled:      repairSettings.Enabled,
			PublicSearch: cfg.SearchPublicEnabled,
		},
	)
}
