// Command server runs the Tack operator CLI and HTTP server.
package main

import (
	"log/slog"
	"os"

	"goodkind.io/tack/internal/servercli"
)

func main() {
	if code := servercli.Run(); code != 0 {
		slog.Error("cli.exited", slog.Int("code", code))
		os.Exit(code)
	}
}
