// Command backfillcheck validates temporary command removal dates.
package main

import (
	"log/slog"
	"os"

	"goodkind.io/tack/internal/command/backfill"
)

func main() {
	code := backfill.Run()
	if code != 0 {
		slog.Error(
			"backfillcheck.failed",
			slog.String("err", "a backfill declaration failed the check"),
			slog.Int("exit", code),
		)
	}

	os.Exit(code)
}
