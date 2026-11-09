// Package clispec declares each operator command as a typed value and
// renders it as a Cobra command. The declaration supplies the command
// identity, help text, positional arguments, flags, and one work function.
// The Cobra adapter decodes command input and calls the work function.
//
// A new typed command uses one [Operation] value without a separate
// dispatch switch, flag parser, or usage string. The package implements
// the terminal interface. It does not render MCP tools or modify the
// consumer MCP interface.
package clispec

import (
	"context"
	"strings"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
)

// Input constrains the input structs for operations. A struct embeds
// [InputMarker] to satisfy Input without defining its own marker method.
type Input interface {
	isClispecInput()
}

// InputMarker satisfies [Input] when an input struct embeds it.
type InputMarker struct{}

func (InputMarker) isClispecInput() {}

// Name stores the canonical command identity and derives the terminal verb.
// CLIOverride selects a short verb under a parent, such as "read" under
// "inspect", instead of the canonical name with hyphens.
type Name struct {
	Canonical   string
	CLIOverride string `exhaustruct:"optional"`
}

// CLI returns the terminal command word.
//
// The method uses CLIOverride when it is nonempty. Otherwise, it replaces
// each underscore in Canonical with a hyphen.
//
// For example, a canonical name can define the terminal spelling:
//
//	word := (Name{Canonical: "audit_query"}).CLI() // word is "audit-query".
func (n Name) CLI() string {
	if n.CLIOverride != "" {
		return n.CLIOverride
	}
	return strings.ReplaceAll(n.Canonical, "_", "-")
}

// Group defines a parent command for one or more operations. Operations
// that share a *Group value render as subcommands of the same parent.
// Parent defines the enclosing group for nested commands.
type Group struct {
	Use    string
	Short  string
	Long   string `exhaustruct:"optional"`
	Parent *Group `exhaustruct:"optional"`
}

// Operation declares one command. The flags and work function use the
// same input struct I. New returns a zeroed input with defaults applied.
// Run writes results through the sink without using Cobra types. Aliases
// register alternate terminal spellings.
type Operation[I Input] struct {
	Name Name
	// Lifetime uses Permanent for a product command or records the ticket
	// and removal day for a backfill. A backfill renders under a backfill
	// group with the once- prefix. The build fails after its removal day.
	Lifetime Lifetime
	Group    *Group     `exhaustruct:"optional"`
	Audit    audit.Spec `exhaustruct:"optional"`
	Aliases  []string   `exhaustruct:"optional"`
	Hidden   bool       `exhaustruct:"optional"`
	Short    string
	Long     string     `exhaustruct:"optional"`
	Examples []string   `exhaustruct:"optional"`
	Args     []Arg[I]   `exhaustruct:"optional"`
	Params   []Param[I] `exhaustruct:"optional"`
	New      func() I
	// DryRun reports planned work after audit validation of the operator
	// identity. It must not write state. A preview that reads state must save
	// durable admission and final read evidence before printing that state.
	//
	// For an operation outside the read class, the preview path passes the
	// original factory to [RunRecordedPreview]. Its infrastructure fallback
	// can return output without admission or final read evidence. This
	// implementation gap does not waive the recording requirement.
	DryRun func(ctx context.Context, in I, sink ResultSink) error `exhaustruct:"optional"`
	Run    func(ctx context.Context, in I, sink ResultSink) error
}

// renderable provides the non-generic interface to an Operation. Its
// methods retain the concrete input type without exposing Go's any type.
type renderable interface {
	renderParent(backfills backfillGroups) *Group
	auditSpec() audit.Spec
	cobraCommand(f *cli.Factory) *cobra.Command
}

func (op Operation[I]) auditSpec() audit.Spec { return op.Audit }

// Registry stores ordered operations and handwritten terminal commands.
// Handwritten commands define their own setup, such as the server start
// command, which does not need a typed input model.
type Registry struct {
	ops         []renderable
	handwritten []HandwrittenCommand
}

// HandwrittenCommand registers a command constructor alongside typed
// operations. Group selects its parent command. A nil Group renders the
// command at the top level.
type HandwrittenCommand struct {
	Group *Group
	Build func(f *cli.Factory) *cobra.Command
}

// NewRegistry returns an empty registry.
//
// [Register] adds typed operations, and [Registry.AddHandwritten] adds
// command constructors.
//
//   - RenderCobra returns nil before a caller adds any commands.
//   - RenderCobra processes typed operations before handwritten commands,
//     even when a caller adds a handwritten command first.
func NewRegistry() *Registry {
	return &Registry{ops: nil, handwritten: nil}
}

// Register adds one typed operation to r.
//
// Register is a function because a Go method cannot introduce its own
// type parameter. The registry preserves the operation's insertion order.
//
//   - Registering firstOperation before secondOperation renders
//     firstOperation before secondOperation.
//   - Adding a handwritten command before registering firstOperation
//     still renders firstOperation before that handwritten command.
func Register[I Input](r *Registry, op Operation[I]) {
	r.ops = append(r.ops, op)
}

// AddHandwritten adds one handwritten command to r.
//
// The registry preserves insertion order for handwritten commands and
// renders them after the typed operations.
//
//   - Adding firstCommand before secondCommand renders firstCommand
//     before secondCommand.
//   - A nil Group renders the command at the top level. A nonnil Group
//     renders the command under that parent.
func (r *Registry) AddHandwritten(h HandwrittenCommand) {
	r.handwritten = append(r.handwritten, h)
}
