// Package clispec declares each operator command once as one typed value and
// renders it onto a spf13/cobra command. The declaration carries the name,
// help text, positional arguments, flags, and one work function; the cobra
// adapter decodes a request into the typed input and calls that function. A
// new command is one Operation value with no separate dispatch switch, flag
// parser, or usage string. The package is terminal-only: it renders no MCP
// tool and never touches the consumer MCP surface.
package clispec

import (
	"context"
	"strings"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/cli"
)

// Input constrains per-operation input structs. Embedding InputMarker
// satisfies it, so an input struct names its fields and nothing else.
type Input interface {
	isClispecInput()
}

// InputMarker embeds into an input struct to satisfy Input.
type InputMarker struct{}

func (InputMarker) isClispecInput() {}

// Name carries the canonical command identity and derives the terminal verb.
// CLIOverride lets a command read as a short verb under a parent, for example
// "read" under "inspect" rather than the dash-spelled canonical.
type Name struct {
	Canonical   string
	CLIOverride string
}

// CLI returns the terminal command word.
func (n Name) CLI() string {
	if n.CLIOverride != "" {
		return n.CLIOverride
	}
	return strings.ReplaceAll(n.Canonical, "_", "-")
}

// Group is a parent command that one or more operations attach under.
// Operations sharing one *Group value render as subcommands of one parent.
// Parent nests a group under another, so a chain renders as nested parents.
type Group struct {
	Use   string
	Short string
	Long  string
	Parent *Group
}

// Operation is one declared command. I is the input struct both the flags and
// the work function share. New returns a zeroed input with defaults applied.
// Run is the single work function; it writes results through the sink and
// never sees cobra types. Aliases register alternate terminal spellings.
type Operation[I Input] struct {
	Name     Name
	Group    *Group
	Aliases  []string
	Hidden   bool
	Short    string
	Long     string
	Examples []string
	Args     []Arg[I]
	Params   []Param[I]
	New      func() I
	Run      func(ctx context.Context, in I, sink ResultSink) error
}

// renderable is the type-erased view of an Operation. The type parameter stays
// captured in the methods and never escapes as any.
type renderable interface {
	group() *Group
	cobraCommand(f *cli.Factory) *cobra.Command
}

func (op Operation[I]) group() *Group { return op.Group }

// Registry is the ordered list of every command. It holds full operations and
// hand-written terminal commands that carry their own wiring (such as the
// server start command, which needs no typed input model).
type Registry struct {
	ops         []renderable
	handwritten []HandwrittenCommand
}

// HandwrittenCommand wraps a constructor so a bespoke command lives in the
// same registry as the spec operations. Group attaches it under a parent; a
// nil Group renders it at the top level.
type HandwrittenCommand struct {
	Group *Group
	Build func(f *cli.Factory) *cobra.Command
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// Register adds one operation. It is a free function because a Go method
// cannot introduce its own type parameter.
func Register[I Input](r *Registry, op Operation[I]) {
	r.ops = append(r.ops, op)
}

// AddHandwritten adds one bespoke command.
func (r *Registry) AddHandwritten(h HandwrittenCommand) {
	r.handwritten = append(r.handwritten, h)
}
