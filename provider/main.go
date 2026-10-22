// Command terraform-provider-tack serves the Tack OpenTofu provider over
// plugin protocol 6.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"goodkind.io/tack/provider/internal/tackprovider"
	"goodkind.io/tack/provider/internal/version"
)

const (
	providerAddress  = "tofu.home.arpa/agoodkind/tack"
	protocolVersion6 = 6
)

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers")
	flag.Parse()

	options := providerserver.ServeOpts{
		Address:         providerAddress,
		Debug:           debug,
		ProtocolVersion: protocolVersion6,
	}
	slog.Info("provider.starting", slog.String("address", providerAddress), slog.String("version", version.Version),
		slog.Bool("debug", debug))
	if err := providerserver.Serve(context.Background(), tackprovider.New(version.Version), options); err != nil {
		slog.Error("provider.stopped", slog.String("address", providerAddress), slog.String("err", err.Error()))
		os.Exit(1)
	}
}
