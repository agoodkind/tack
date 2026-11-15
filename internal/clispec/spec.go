// Package clispec defines typed operator commands and renders terminal commands without registering MCP tools.
package clispec

import (
	"context"
	"strings"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
)

// Input requires an operation input struct to embed InputMarker.
type Input interface {
	isClispecInput()
}

// InputMarker implements Input when an input struct embeds it.
type InputMarker struct{}

func (InputMarker) isClispecInput() {}

// Name defines the canonical command identity and its terminal verb.
// CLIOverride replaces the derived verb, for example "read" under "inspect".
type Name struct {
	Canonical   string
	CLIOverride string `exhaustruct:"optional"`
}

// CLI returns the terminal command word.
func (n Name) CLI() string {
	if n.CLIOverride != "" {
		return n.CLIOverride
	}

	return strings.ReplaceAll(n.Canonical, "_", "-")
}

// Group defines a parent command for operations that share its pointer.
// Parent defines the next ancestor in a chain of nested commands.
type Group struct {
	Use    string
	Short  string
	Long   string `exhaustruct:"optional"`
	Parent *Group `exhaustruct:"optional"`
}

// Operation defines one command. OperationInput is the input struct that flag
// bindings and the work function use. New returns an input with defaults.
// Run writes results through the sink and does not depend on Cobra types.
// Aliases register alternate terminal spellings.
type Operation[OperationInput Input] struct {
	Name Name
	// Lifetime specifies Permanent for a product command or a ticket and
	// removal date for a backfill. Backfill commands use the once- prefix
	// under a backfill group. The build fails after the removal date.
	Lifetime Lifetime
	Group    *Group     `exhaustruct:"optional"`
	Audit    audit.Spec `exhaustruct:"optional"`
	Aliases  []string   `exhaustruct:"optional"`
	Hidden   bool       `exhaustruct:"optional"`
	Short    string
	Long     string                  `exhaustruct:"optional"`
	Examples []string                `exhaustruct:"optional"`
	Args     []Arg[OperationInput]   `exhaustruct:"optional"`
	Params   []Param[OperationInput] `exhaustruct:"optional"`
	New      func() OperationInput
	// DryRun reports planned work after the shared audit check validates the
	// operator identity. It must not write state. The check records each dry
	// run as an operator read before printing the preview.
	DryRun func(ctx context.Context, in OperationInput, sink ResultSink) error `exhaustruct:"optional"`
	Run    func(ctx context.Context, in OperationInput, sink ResultSink) error
}

// renderable provides non-generic methods for rendering an Operation.
type renderable interface {
	renderParent(backfills backfillGroups) *Group
	auditSpec() audit.Spec
	cobraCommand(f *cli.Factory) *cobra.Command
}

func (op Operation[OperationInput]) auditSpec() audit.Spec { return op.Audit }

// Registry stores operations and handwritten terminal command constructors.
type Registry struct {
	ops         []renderable
	handwritten []HandwrittenCommand
}

// HandwrittenCommand registers a command constructor without a typed input.
// Group specifies its parent. A nil Group registers a top-level command.
type HandwrittenCommand struct {
	Group *Group
	Build func(f *cli.Factory) *cobra.Command
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{ops: nil, handwritten: nil}
}

// Register adds one operation.
func Register[OperationInput Input](r *Registry, op Operation[OperationInput]) {
	r.ops = append(r.ops, op)
}

// AddHandwritten adds one handwritten command constructor.
func (r *Registry) AddHandwritten(h HandwrittenCommand) {
	r.handwritten = append(r.handwritten, h)
}
