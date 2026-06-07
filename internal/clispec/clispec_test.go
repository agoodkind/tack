package clispec_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	oteltrace "go.opentelemetry.io/otel/trace"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

type showInput struct {
	clispec.InputMarker
	ID    string
	Label string
	Org   string
}

func showOp(group *clispec.Group) clispec.Operation[showInput] {
	return clispec.Operation[showInput]{
		Name:  clispec.Name{Canonical: "show"},
		Group: group,
		Short: "Show one id",
		Args: []clispec.Arg[showInput]{
			clispec.StringArg("id", "the id", func(in *showInput, v string) { in.ID = v }),
		},
		Params: []clispec.Param[showInput]{
			clispec.StringParam("label", "a label", "", false, func(in *showInput, v string) { in.Label = v }),
		},
		New: func() showInput { return showInput{} },
		Run: func(ctx context.Context, in showInput, sink clispec.ResultSink) error {
			body, err := json.Marshal(map[string]string{"id": in.ID, "label": in.Label})
			if err != nil {
				return err
			}
			return sink.WriteJSON(ctx, body)
		},
	}
}

func ctxWithSpan() context.Context {
	traceID, _ := oteltrace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	spanID, _ := oteltrace.SpanIDFromHex("0123456789abcdef")
	sc := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: oteltrace.FlagsSampled,
	})
	return oteltrace.ContextWithSpanContext(context.Background(), sc)
}

func execute(t *testing.T, reg *clispec.Registry, f *cli.Factory, args ...string) error {
	t.Helper()
	root := &cobra.Command{Use: "tack", SilenceErrors: true, SilenceUsage: true}
	for _, c := range clispec.RenderCobra(reg, f) {
		root.AddCommand(c)
	}
	root.SetArgs(args)
	root.SetContext(ctxWithSpan())
	return root.Execute()
}

func TestOperationRendersAndRunsWithTraceHeader(t *testing.T) {
	group := &clispec.Group{Use: "demo", Short: "demo group"}
	reg := clispec.NewRegistry()
	clispec.Register(reg, showOp(group))

	out := &bytes.Buffer{}
	f := &cli.Factory{Out: out}
	if err := execute(t, reg, f, "demo", "show", "ABC", "--label", "hi"); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := out.String()
	if !strings.HasPrefix(got, "#meta trace_id=0123456789abcdef0123456789abcdef") {
		t.Fatalf("missing trace header, got:\n%s", got)
	}
	if !strings.Contains(got, `"id": "ABC"`) || !strings.Contains(got, `"label": "hi"`) {
		t.Fatalf("body missing decoded input:\n%s", got)
	}
}

func TestJSONModeWrapsResultUnderMeta(t *testing.T) {
	group := &clispec.Group{Use: "demo", Short: "demo group"}
	reg := clispec.NewRegistry()
	clispec.Register(reg, showOp(group))

	out := &bytes.Buffer{}
	f := &cli.Factory{Out: out}
	root := &cobra.Command{Use: "tack", SilenceErrors: true, SilenceUsage: true}
	f.RegisterGlobalFlags(root)
	for _, c := range clispec.RenderCobra(reg, f) {
		root.AddCommand(c)
	}
	root.SetArgs([]string{"--output", "json", "demo", "show", "ABC"})
	root.SetContext(ctxWithSpan())
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var doc struct {
		Meta   map[string]string `json:"_meta"`
		Result map[string]string `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("output not a json envelope: %v\n%s", err, out.String())
	}
	if doc.Meta["trace_id"] == "" {
		t.Fatalf("_meta.trace_id empty: %s", out.String())
	}
	if doc.Result["id"] != "ABC" {
		t.Fatalf("result.id = %q", doc.Result["id"])
	}
}

func TestRequiredFlagEnforced(t *testing.T) {
	op := showOp(nil)
	op.Params = []clispec.Param[showInput]{
		clispec.StringParam("org", "org id", "", true, func(in *showInput, v string) { in.Org = v }),
	}
	op.Args = nil
	reg := clispec.NewRegistry()
	clispec.Register(reg, op)

	f := &cli.Factory{Out: &bytes.Buffer{}}
	if err := execute(t, reg, f, "show"); err == nil {
		t.Fatal("expected error when required --org is missing")
	}
}

func TestGroupNests(t *testing.T) {
	parent := &clispec.Group{Use: "ops", Short: "ops"}
	child := &clispec.Group{Use: "inspect", Short: "inspect", Parent: parent}
	reg := clispec.NewRegistry()
	clispec.Register(reg, showOp(child))

	tops := clispec.RenderCobra(reg, &cli.Factory{Out: &bytes.Buffer{}})
	if len(tops) != 1 || tops[0].Use != "ops" {
		t.Fatalf("expected single top 'ops', got %v", tops)
	}
	inspect, _, err := tops[0].Find([]string{"inspect", "show"})
	if err != nil || inspect.Name() != "show" {
		t.Fatalf("nested command not found: %v (%v)", inspect, err)
	}
}
