// Package cli holds the operator CLI's shared dependency factory and the
// global output flag. The factory carries config and the IO streams every
// command writes through, so commands take a *Factory instead of reaching for
// package globals or the process streams directly.
package cli

import (
	"io"
	"os"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/config"
)

// Output formats selectable with the global --output flag.
const (
	FormatText = "text"
	FormatJSON = "json"
)

// Factory bundles the dependencies a command needs. Cfg is the loaded server
// config; In, Out, and Err are the process streams (overridable in tests);
// output is bound to the persistent --output flag by RegisterGlobalFlags.
type Factory struct {
	Cfg *config.Config
	In  io.Reader
	Out io.Writer
	Err io.Writer

	output *string
}

// System builds a Factory wired to the process streams.
func System(cfg *config.Config) *Factory {
	return &Factory{Cfg: cfg, In: os.Stdin, Out: os.Stdout, Err: os.Stderr, output: nil}
}

// OutputFormat reports the selected output format, defaulting to text.
func (f *Factory) OutputFormat() string {
	if f.output != nil && *f.output == FormatJSON {
		return FormatJSON
	}
	return FormatText
}

// RegisterGlobalFlags installs the persistent --output flag on root and binds
// it so every command sees the operator's choice through OutputFormat.
func (f *Factory) RegisterGlobalFlags(root *cobra.Command) {
	f.output = root.PersistentFlags().String(
		"output", FormatText, "output format: text or json")
}
