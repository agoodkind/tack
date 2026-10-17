// Package version provides build metadata for terraform-provider-tack.
// Releases set Version to the release tag.
package version

// Commit, Version, Dirty, and BuildTime store metadata that the build stamps
// at link time. An unstamped build keeps the defaults, including Version at
// dev.
var (
	Commit    = "unknown"
	Version   = "dev"
	Dirty     = "unknown"
	BuildTime = "unknown"
)
