package datagen

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func (g *Generator) probeParentPropertyRefused(
	ctx context.Context,
	workspace WorkspaceIdentity,
	projectReference string,
	issueReference string,
) error {
	encodedReference, err := json.Marshal(issueReference)
	if err != nil {
		return loggedError(ctx, "qa datagen: encode issue reference", err)
	}

	for _, toolName := range []string{"tack_create_issue", "tack_update_issue"} {
		if toolName == "tack_update_issue" && issueReference == "" {
			continue
		}
		for _, value := range []json.RawMessage{
			encodedReference,
			json.RawMessage("null"),
		} {
			properties := newProperties()
			properties["parent_id"] = value
			arguments := ToolArguments{
				WorkspaceReference: workspace.Slug,
				Properties:         properties,
			}
			if toolName == "tack_create_issue" {
				arguments.ProjectReference = projectReference
				arguments.Name = fmt.Sprintf("QA undeclared parent property seed %d", g.seed)
			} else {
				arguments.NodeID = issueReference
			}
			_, err := g.driver.Call(ctx, workspace.Actors[0].Token, toolName, arguments)
			if g.dryRun {
				continue
			}
			if err == nil {
				return fmt.Errorf("qa datagen: %s accepted properties.parent_id", toolName)
			}
			if !strings.Contains(err.Error(), "properties.parent_id is not declared") {
				return loggedError(ctx, "qa datagen: reject undeclared parent property", err)
			}
		}
	}
	return nil
}
