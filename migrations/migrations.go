// Package migrations embeds all goose SQL files for use by the postgres adapter.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
