package datagen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// managedRefusalText is part of the error that the node service returns when
// a request sets a property that the service writes itself.
const managedRefusalText = "a request cannot set them"

// managedProbeSequence is the sequence value that the managed-property
// probes send.
const managedProbeSequence = 999

// errManagedPropertyAccepted reports that a create or update call accepted a
// value for a property that the node service writes itself.
var errManagedPropertyAccepted = errors.New("the server accepted a value for a service-managed property")

// managedProbe is the tool name, the service-managed property, and the
// arguments of one create or update probe.
type managedProbe struct {
	toolName  string
	property  string
	arguments ToolArguments
}

// probeManagedPropertiesRefused calls tack_create_issue in the project and
// tack_update_issue on issueReference, once with scope_id and once with
// sequence in the properties. The server must refuse each call with the
// service-managed property error. A dry run plans the calls and skips the
// checks. A project without a generated issue skips the update calls.
func (g *Generator) probeManagedPropertiesRefused(
	ctx context.Context,
	workspace WorkspaceIdentity,
	projectIdentifier string,
	projectReference string,
	issueReference string,
) error {
	actor := workspace.Actors[0]
	probes := g.managedProbes(workspace, projectIdentifier, projectReference, issueReference)
	for _, probe := range probes {
		_, err := g.driver.Call(ctx, actor.Token, probe.toolName, probe.arguments)
		if g.dryRun {
			continue
		}
		message := fmt.Sprintf("qa datagen: %s probe with properties.%s", probe.toolName, probe.property)
		if err == nil {
			return loggedError(ctx, message, errManagedPropertyAccepted)
		}
		if !strings.Contains(err.Error(), managedRefusalText) || !strings.Contains(err.Error(), "properties."+probe.property) {
			return loggedError(ctx, message, err)
		}
	}
	slog.InfoContext(ctx, "qa.datagen.managed_properties_refused",
		slog.String("project", projectIdentifier), slog.Int("probes", len(probes)), slog.Bool("dry_run", g.dryRun))
	return nil
}

// managedProbes returns the arguments of each create and update probe that
// probeManagedPropertiesRefused sends.
func (g *Generator) managedProbes(
	workspace WorkspaceIdentity,
	projectIdentifier string,
	projectReference string,
	issueReference string,
) []managedProbe {
	values := map[string]func(NodeProperties){
		"scope_id": func(properties NodeProperties) { properties.setString("scope_id", projectReference) },
		"sequence": func(properties NodeProperties) { properties.setInt("sequence", managedProbeSequence) },
	}
	probes := make([]managedProbe, 0, 2*len(values))
	for _, property := range []string{"scope_id", "sequence"} {
		createProperties := newProperties()
		values[property](createProperties)
		name := fmt.Sprintf("QA managed %s probe seed %d", property, g.seed)
		probes = append(probes, managedProbe{toolName: "tack_create_issue", property: property, arguments: ToolArguments{
			WorkspaceReference: workspace.Slug, ProjectReference: projectIdentifier, Name: name, Properties: createProperties,
		}})
		if issueReference == "" {
			continue
		}
		updateProperties := newProperties()
		values[property](updateProperties)
		probes = append(probes, managedProbe{toolName: "tack_update_issue", property: property, arguments: ToolArguments{
			WorkspaceReference: workspace.Slug, NodeID: issueReference, Properties: updateProperties,
		}})
	}
	return probes
}
