// Package cli holds the operator CLI's shared dependency factory and global
// flags. The factory carries config and the IO streams every command writes
// through, so commands take a *Factory instead of reaching for package
// globals or the process streams directly.
package cli

import (
	"io"
	"os"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
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

	output         *string
	operatorID     *string                      `exhaustruct:"optional"`
	operatorEmail  *string                      `exhaustruct:"optional"`
	operatorName   *string                      `exhaustruct:"optional"`
	execute        *bool                        `exhaustruct:"optional"`
	operatorSource audit.OperatorIdentitySource `exhaustruct:"optional"`
	auditOutbox    audit.OutboxWriter           `exhaustruct:"optional"`
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

// Operator returns the raw values selected by the operator identity flags.
func (f *Factory) Operator() (string, string, string) {
	return stringValue(f.operatorID), stringValue(f.operatorEmail), stringValue(f.operatorName)
}

// Execute reports whether the operator passed the action gate.
func (f *Factory) Execute() bool {
	return f.execute != nil && *f.execute
}

// SetOperatorIdentitySource stores the source selected for the root command.
func (f *Factory) SetOperatorIdentitySource(source audit.OperatorIdentitySource) {
	f.operatorSource = source
}

// OperatorIdentitySource returns the source selected for the root command.
func (f *Factory) OperatorIdentitySource() audit.OperatorIdentitySource {
	return f.operatorSource
}

// SetAuditOutbox stores the outbox used by audited commands.
func (f *Factory) SetAuditOutbox(outbox audit.OutboxWriter) {
	f.auditOutbox = outbox
}

// AuditOutbox returns the outbox used by audited commands.
func (f *Factory) AuditOutbox() audit.OutboxWriter {
	return f.auditOutbox
}

// RegisterGlobalFlags installs the persistent global flags on root and binds
// them so every command sees the operator's choices through Factory accessors.
func (f *Factory) RegisterGlobalFlags(root *cobra.Command) {
	f.output = root.PersistentFlags().String(
		"output", FormatText, "output format: text or json")
	f.operatorID = root.PersistentFlags().String(
		"operator-id", "", "operator UUID")
	f.operatorEmail = root.PersistentFlags().String(
		"operator-email", "", "operator email")
	f.operatorName = root.PersistentFlags().String(
		"operator-name", "", "operator name")
	f.execute = root.PersistentFlags().Bool(
		"execute", false, "execute the command instead of printing its dry-run")
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
