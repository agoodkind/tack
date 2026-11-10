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

// managedProbeSequence sets the sequence value for probes that attempt to
// write a property that the service manages.
const managedProbeSequence = 999

// errManagedPropertyAccepted reports that a create or update call accepted a
// value for a property that the node service writes itself.
var errManagedPropertyAccepted = errors.New(
	"the server accepted a value for a service-managed property",
)

// managedProbe specifies the tool, the property that the service manages,
// and the arguments for one create or update probe.
type managedProbe struct {
	toolName  string
	property  string
	arguments ToolArguments
}

// probeManagedPropertiesRefused calls tack_create_issue in the project and
// tack_update_issue on issueReference. Each tool receives one probe with
// scope_id and another with sequence in the properties. The server must
// refuse each call with the error for a property that the service manages.
// A dry run plans the calls and skips the checks. A project without a
// generated issue skips the update calls.
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
		message := fmt.Sprintf(
			"qa datagen: %s probe with properties.%s",
			probe.toolName,
			probe.property,
		)
		if err == nil {
			return loggedError(ctx, message, errManagedPropertyAccepted)
		}
		if !strings.Contains(
			err.Error(),
			managedRefusalText,
		) || !strings.Contains(err.Error(), "properties."+probe.property) {
			return loggedError(ctx, message, err)
		}
	}
	slog.InfoContext(
		ctx,
		"qa.datagen.managed_properties_refused",
		slog.String("project", projectIdentifier),
		slog.Int("probes", len(probes)),
		slog.Bool("dry_run", g.dryRun),
	)
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
		"scope_id": func(properties NodeProperties) {
			properties.setString("scope_id", projectReference)
		},
		"sequence": func(properties NodeProperties) {
			properties.setInt("sequence", managedProbeSequence)
		},
	}
	probes := make([]managedProbe, 0, 2*len(values))
	for _, property := range []string{"scope_id", "sequence"} {
		createProperties := newProperties()
		values[property](createProperties)
		name := fmt.Sprintf("QA managed %s probe seed %d", property, g.seed)
		probes = append(
			probes,
			managedProbe{
				toolName: "tack_create_issue",
				property: property,
				arguments: ToolArguments{
					WorkspaceReference: workspace.Slug,
					ProjectReference:   projectIdentifier,
					Name:               name,
					Properties:         createProperties,
				},
			},
		)
		if issueReference == "" {
			continue
		}
		updateProperties := newProperties()
		values[property](updateProperties)
		probes = append(
			probes,
			managedProbe{
				toolName: "tack_update_issue",
				property: property,
				arguments: ToolArguments{
					WorkspaceReference: workspace.Slug,
					NodeID:             issueReference,
					Properties:         updateProperties,
				},
			},
		)
	}
	return probes
}
