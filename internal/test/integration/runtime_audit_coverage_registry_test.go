//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	mcpadapter "goodkind.io/tack/internal/adapters/mcp"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/ops"
)

// registeredOperations returns sorted, unique operation keys from the real
// command tree, recorded MCP methods, MCP discovery responses, and runtime HTTP
// route table.
func registeredOperations(t *testing.T, env *coverageEnv) []string {
	t.Helper()
	var keys []string
	for path := range env.commandClasses {
		keys = append(keys, path)
	}
	for _, method := range mcpadapter.RecordedMethods() {
		keys = append(keys, "mcp method "+method)
	}
	for _, name := range listedNames(t, env, "tools/list", "tools", "name") {
		keys = append(keys, "mcp tool "+name)
	}
	for _, name := range listedNames(t, env, "prompts/list", "prompts", "name") {
		keys = append(keys, "mcp prompt "+name)
	}
	for _, uri := range listedNames(t, env, "resources/list", "resources", "uri") {
		keys = append(keys, "mcp resource "+uri)
	}
	for _, route := range env.server.graph.RouteTable() {
		keys = append(keys, "http route "+route.Pattern)
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

// commandClasses maps each ops command with a RunE function to its audit class
// annotation. It fails if the command tree does not contain any runnable
// commands.
func commandClasses(t *testing.T) map[string]string {
	t.Helper()
	factory := &cli.Factory{Cfg: nil, In: nil, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	registry := clispec.NewRegistry()
	ops.RegisterCommands(registry, factory)
	classes := map[string]string{}
	var walk func(command *cobra.Command, path []string)
	walk = func(command *cobra.Command, path []string) {
		if command.RunE != nil {
			classes["command "+strings.Join(path, " ")] = command.Annotations[clispec.AuditClassAnnotation]
		}
		for _, child := range command.Commands() {
			walk(child, append(slices.Clone(path), child.Name()))
		}
	}
	for _, command := range clispec.RenderCobra(registry, factory) {
		walk(command, []string{command.Name()})
	}
	if len(classes) == 0 {
		t.Fatal("The ops command tree did not contain any commands with RunE functions; coverage requires runnable commands.")
	}
	return classes
}

// listedNames calls an MCP discovery method and returns each entry's nonempty
// string value for the requested field. It fails if the response, collection,
// or field cannot decode as expected.
func listedNames(t *testing.T, env *coverageEnv, method, collection, field string) []string {
	t.Helper()
	response := env.server.rpc(t, newRequestID(t, "coverage-list"), method, map[string]any{})
	var decoded struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(response.body, &decoded); err != nil {
		t.Fatalf("The test could not decode the expected MCP discovery data from %s: %v\n%s", method, err, response.body)
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(decoded.Result[collection], &entries); err != nil {
		t.Fatalf("The test could not decode the expected MCP discovery data from %s: %v\n%s", method, err, response.body)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		var name string
		if err := json.Unmarshal(entry[field], &name); err != nil || name == "" {
			t.Fatalf("The discovery response did not contain a nonempty string for the requested field; the method, field value, and decoding error follow: %s: %s %v", method, entry[field], err)
		}
		names = append(names, name)
	}
	return names
}
