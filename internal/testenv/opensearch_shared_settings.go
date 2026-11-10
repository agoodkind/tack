package testenv

const (
	// openSearchFreeSpaceFloor is the minimum free space that ML Commons
	// needs to load and run the search model.
	openSearchFreeSpaceFloor = "1gb"
	// openSearchFreeSpaceKey selects the ML Commons setting that controls
	// the minimum free disk space.
	openSearchFreeSpaceKey = "plugins.ml_commons.disk_free_space_threshold"
)

// openSearchFreeSpaceSettings returns node settings that use the same
// absolute free space floor for all three disk watermarks and ML Commons.
func openSearchFreeSpaceSettings() []string {
	return []string{
		"cluster.routing.allocation.disk.watermark.low=" + openSearchFreeSpaceFloor,
		"cluster.routing.allocation.disk.watermark.high=" + openSearchFreeSpaceFloor,
		"cluster.routing.allocation.disk.watermark.flood_stage=" + openSearchFreeSpaceFloor,
		openSearchFreeSpaceKey + "=" + openSearchFreeSpaceFloor,
	}
}

// sharedOpenSearchNodeSettings returns the shared engine's point in time
// context limit and disk settings.
func sharedOpenSearchNodeSettings() []string {
	return append(
		[]string{"search.max_open_pit_context=" + OpenSearchPointInTimeLimit},
		openSearchFreeSpaceSettings()...,
	)
}
