// Command terraform-provider-tack serves the Tack OpenTofu provider over
// plugin protocol 6.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"goodkind.io/tack/internal/tackprovider"
)

const (
	providerAddress  = "tofu.home.arpa/agoodkind/tack"
	protocolVersion6 = 6
)

// version receives its value at link time from the provider-tack Makefile target.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers")
	flag.Parse()

	options := providerserver.ServeOpts{
		Address:         providerAddress,
		Debug:           debug,
		ProtocolVersion: protocolVersion6,
	}
	slog.Info("provider.starting", slog.String("address", providerAddress), slog.String("version", version),
		slog.Bool("debug", debug))
	if err := providerserver.Serve(context.Background(), tackprovider.New(version), options); err != nil {
		slog.Error("provider.stopped", slog.String("address", providerAddress), slog.String("err", err.Error()))
		os.Exit(1)
	}
}
