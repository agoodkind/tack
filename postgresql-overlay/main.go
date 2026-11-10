// Command terraform-provider-postgresql serves the cyrilgdn PostgreSQL provider
// built from the pinned source in src, with the patch in role-state.patch.
package main

import (
	"log/slog"

	"github.com/hashicorp/terraform-plugin-sdk/v2/plugin"
	"github.com/terraform-providers/terraform-provider-postgresql/postgresql"
)

func main() {
	slog.Info("provider.started", slog.String("provider", "postgresql"))
	plugin.Serve(&plugin.ServeOpts{ProviderFunc: postgresql.Provider})
}
