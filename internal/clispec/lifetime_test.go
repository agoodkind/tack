package clispec_test

import (
	"context"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

type lifetimeInput struct {
	clispec.InputMarker
}

func lifetimeOp(group *clispec.Group, lifetime clispec.Lifetime) clispec.Operation[lifetimeInput] {
	return clispec.Operation[lifetimeInput]{
		Name:     clispec.Name{Canonical: "move-rows"},
		Lifetime: lifetime,
		Group:    group,
		Short:    "Move rows",
		New:      func() lifetimeInput { return lifetimeInput{InputMarker: clispec.InputMarker{}} },
		Run:      func(context.Context, lifetimeInput, clispec.ResultSink) error { return nil },
	}
}

func childNamed(t *testing.T, parent *cobra.Command, name string) *cobra.Command {
	t.Helper()
	for _, child := range parent.Commands() {
		if child.Name() == name {
			return child
		}
	}
	t.Fatalf("%s has no child %q; children: %v", parent.Name(), name, parent.Commands())
	return nil
}

func TestBackfillRendersApartFromProductCommandsWithThePrefix(t *testing.T) {
	audit := &clispec.Group{Use: "audit", Short: "Audit commands"}
	reg := clispec.NewRegistry()
	clispec.Register(reg, lifetimeOp(audit, clispec.Permanent))
	clispec.Register(reg, lifetimeOp(audit, clispec.Lifetime{
		Ticket:   "TACK-461",
		RemoveBy: time.Date(2026, time.October, 19, 0, 0, 0, 0, time.UTC),
	}))

	tops := clispec.RenderCobra(reg, &cli.Factory{Cfg: nil, In: nil, Out: nil, Err: nil})

	if len(tops) != 1 || tops[0].Name() != "audit" {
		t.Fatalf("top-level commands = %v, want only audit", tops)
	}
	childNamed(t, tops[0], "move-rows")
	backfill := childNamed(t, tops[0], "backfill")
	childNamed(t, backfill, clispec.BackfillNamePrefix+"move-rows")
	if len(tops[0].Commands()) != 2 {
		t.Fatalf("audit children = %v, want the product command and the backfill group", tops[0].Commands())
	}
}
