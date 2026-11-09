// Package cli provides shared dependencies and global flags for the operator
// command line. Commands use Factory for configuration and input and output
// streams instead of package globals or direct process stream access.
package cli

import (
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
)

// The global --output flag selects an output format.
const (
	FormatText = "text"
	FormatJSON = "json"
)

// Factory provides dependencies for commands. Cfg contains the loaded server
// configuration. In, Out, and Err supply the process streams, and tests can
// replace them. RegisterGlobalFlags binds output to the persistent --output
// flag.
type Factory struct {
	Cfg *config.Config
	In  io.Reader
	Out io.Writer
	Err io.Writer

	output                   *string
	operatorID               *string                      `exhaustruct:"optional"`
	operatorEmail            *string                      `exhaustruct:"optional"`
	operatorName             *string                      `exhaustruct:"optional"`
	operatorService          *string                      `exhaustruct:"optional"`
	operatorSession          *string                      `exhaustruct:"optional"`
	deployCommit             *string                      `exhaustruct:"optional"`
	execute                  *bool                        `exhaustruct:"optional"`
	operatorSource           audit.OperatorIdentitySource `exhaustruct:"optional"`
	auditOutbox              audit.OutboxWriter           `exhaustruct:"optional"`
	auditOutboxCloser        auditOutboxCloser            `exhaustruct:"optional"`
	auditInfrastructureProbe audit.InfrastructureProbe    `exhaustruct:"optional"`
	auditReads               audit.ReadLifecycle          `exhaustruct:"optional"`
}

type auditOutboxCloser interface {
	Close()
}

// System creates a Factory with the process input and output streams.
//
// The caller supplies the server configuration. Tests can replace the streams
// after construction to capture command output.
//
//	factory := System(cfg)
//	factory.Out = &output
func System(cfg *config.Config) *Factory {
	return &Factory{Cfg: cfg, In: os.Stdin, Out: os.Stdout, Err: os.Stderr, output: nil}
}

// OutputFormat returns the selected output format.
//
// Only an explicit JSON selection returns FormatJSON. An unset or other value
// returns FormatText.
//
//	factory := &Factory{}
//	format := factory.OutputFormat() // FormatText
func (f *Factory) OutputFormat() string {
	if f.output != nil && *f.output == FormatJSON {
		return FormatJSON
	}
	return FormatText
}

// Operator returns the raw operator identity flag values.
//
// The return values follow the flag order below. An unset flag returns an
// empty string, and this method does not trim whitespace.
//
//	operatorID, email, name := factory.Operator()
func (f *Factory) Operator() (string, string, string) {
	return stringValue(f.operatorID), stringValue(f.operatorEmail), stringValue(f.operatorName)
}

// OperatorService returns the service name from --operator-service.
//
// An unset service flag returns an empty string, as it does for a human
// operator. This accessor does not trim whitespace or select the identity
// source.
//
//	serviceName := factory.OperatorService()
//	serviceName = strings.TrimSpace(serviceName)
func (f *Factory) OperatorService() string {
	return stringValue(f.operatorService)
}

// OperatorSession returns the agent session identifier from --operator-session.
//
// An unset session flag returns an empty string. The identity source validates
// whether the selected identity permits or requires a session.
//
//	sessionID := strings.TrimSpace(factory.OperatorSession())
func (f *Factory) OperatorSession() string {
	return stringValue(f.operatorSession)
}

// DeployCommit returns the deployment commit after trimming whitespace.
//
// An unset deployment flag returns an empty string. The accessor preserves
// whitespace within the supplied commit or branch value.
//
//	factory := &Factory{}
//	commit := factory.DeployCommit() // An unset flag returns an empty string.
func (f *Factory) DeployCommit() string {
	return strings.TrimSpace(stringValue(f.deployCommit))
}

// Execute reports whether the operator selected --execute.
//
// An unset flag returns false. Command execution uses this selection as the
// action gate.
//
//	factory := &Factory{}
//	execute := factory.Execute() // false
func (f *Factory) Execute() bool {
	return f.execute != nil && *f.execute
}

// SetOperatorIdentitySource stores the identity source for the root command.
//
// The root command installs the source before executing commands. Identity
// resolution can then read the flags after Cobra parses them.
//
//	factory.SetOperatorIdentitySource(NewOperatorSource(factory))
func (f *Factory) SetOperatorIdentitySource(source audit.OperatorIdentitySource) {
	f.operatorSource = source
}

// OperatorIdentitySource returns the identity source for the root command.
//
// The accessor returns nil until setup supplies a source. Calling this method
// does not resolve an operator or parse flags.
//
//	factory.SetOperatorIdentitySource(NewOperatorSource(factory))
//	source := factory.OperatorIdentitySource()
//	principal, err := source.Resolve(ctx)
func (f *Factory) OperatorIdentitySource() audit.OperatorIdentitySource {
	return f.operatorSource
}

// SetAuditOutbox stores the outbox for audited commands.
//
// If the outbox implements Close, the factory saves that method for
// CloseAuditOutbox. Otherwise, it clears the previous closer.
//
//	factory.SetAuditOutbox(outbox)
//	defer factory.CloseAuditOutbox()
func (f *Factory) SetAuditOutbox(outbox audit.OutboxWriter) {
	f.auditOutbox = outbox
	closer, ok := outbox.(auditOutboxCloser)
	if ok {
		f.auditOutboxCloser = closer
	} else {
		f.auditOutboxCloser = nil
	}
}

// AuditOutbox returns the outbox for audited commands.
//
// The accessor returns nil until setup supplies a writer. Retrieving the
// writer does not record an audit event.
//
//	factory.SetAuditOutbox(writer)
//	outbox := factory.AuditOutbox() // writer
func (f *Factory) AuditOutbox() audit.OutboxWriter {
	return f.auditOutbox
}

// SetAuditInfrastructureProbe stores the audit infrastructure probe.
//
// Only commands that create their own audit infrastructure may use this probe
// to check which recording dependencies already exist.
//
//	factory.SetAuditInfrastructureProbe(probe)
//	state, err := factory.AuditInfrastructureProbe()(ctx)
func (f *Factory) SetAuditInfrastructureProbe(probe audit.InfrastructureProbe) {
	f.auditInfrastructureProbe = probe
}

// AuditInfrastructureProbe returns the audit infrastructure probe.
//
// The accessor returns the probe that SetAuditInfrastructureProbe stored for
// commands that create their own audit infrastructure. It does not run the
// probe.
//
//	probe := factory.AuditInfrastructureProbe()
//	state, err := probe(ctx)
func (f *Factory) AuditInfrastructureProbe() audit.InfrastructureProbe {
	return f.auditInfrastructureProbe
}

// SetAuditReads stores the lifecycle store that records operator reads.
//
// Command setup supplies the store alongside the audit outbox. This method
// does not begin or finalize a read.
//
//	factory.SetAuditReads(audit.NewPoolReadLifecycle(pool))
func (f *Factory) SetAuditReads(reads audit.ReadLifecycle) {
	f.auditReads = reads
}

// AuditReads returns the lifecycle store that records operator reads.
//
// The accessor returns nil until command setup supplies a store. Retrieving
// the store does not begin or finalize a read.
//
//	factory.SetAuditReads(store)
//	reads := factory.AuditReads() // store
func (f *Factory) AuditReads() audit.ReadLifecycle {
	return f.auditReads
}

// CloseAuditOutbox releases the database pool for audited commands.
//
// The method calls the closer that SetAuditOutbox saved. It does not act when
// the writer lacks Close.
//
//	factory.SetAuditOutbox(outbox)
//	defer factory.CloseAuditOutbox()
func (f *Factory) CloseAuditOutbox() {
	if f.auditOutboxCloser != nil {
		f.auditOutboxCloser.Close()
	}
}

// RegisterGlobalFlags installs persistent global flags on root.
//
// The factory saves each flag pointer. Its accessors read the operator's
// selections after Cobra parses the flags for a command.
//
//	factory.RegisterGlobalFlags(root)
//	root.SetArgs(args)
//	err := root.Execute()
func (f *Factory) RegisterGlobalFlags(root *cobra.Command) {
	f.output = root.PersistentFlags().String(
		"output", FormatText, "output format: text or json")
	f.operatorID = root.PersistentFlags().String(
		"operator-id", "", "operator UUID")
	f.operatorEmail = root.PersistentFlags().String(
		"operator-email", "", "operator email")
	f.operatorName = root.PersistentFlags().String(
		"operator-name", "", "operator name")
	f.operatorService = root.PersistentFlags().String(
		"operator-service", "",
		"service name for a non-human operator identity; with --operator-id, the service acts for that operator")
	f.operatorSession = root.PersistentFlags().String(
		"operator-session", "", "agent session identifier; valid only with --operator-service")
	f.deployCommit = root.PersistentFlags().String(
		"deploy-commit", "", "commit or branch being deployed, supplied by the deploy playbook")
	f.execute = root.PersistentFlags().Bool(
		"execute", false, "execute the command instead of printing its dry-run")
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
