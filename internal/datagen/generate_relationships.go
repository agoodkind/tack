package datagen

import (
	"context"
	"fmt"
)

func (g *Generator) generateRelationships(
	ctx context.Context,
	index int,
	actor Actor,
	workspace WorkspaceIdentity,
	issueReference string,
	parentReference string,
	labels []string,
) error {
	otherActor := workspace.Actors[(index+1)%len(workspace.Actors)]
	relationships := []struct {
		relationType string
		target       string
	}{
		{relationType: "assigned_to", target: actor.UserID.String()},
		{relationType: "labeled_with", target: labels[index%len(labels)]},
		{relationType: "child_of", target: parentReference},
		{relationType: "watches", target: otherActor.UserID.String()},
	}
	for _, relationship := range relationships {
		if err := g.callRelationship(
			ctx,
			actor.Token,
			"tack_add_relationship",
			issueReference,
			relationship.relationType,
			relationship.target,
		); err != nil {
			return err
		}
	}
	if index%6 == 0 {
		if err := g.callRelationship(
			ctx,
			actor.Token,
			"tack_remove_relationship",
			issueReference,
			"watches",
			otherActor.UserID.String(),
		); err != nil {
			return err
		}
		if err := g.callRelationship(
			ctx,
			actor.Token,
			"tack_add_relationship",
			issueReference,
			"watches",
			otherActor.UserID.String(),
		); err != nil {
			return err
		}
	}
	return nil
}

func (g *Generator) callRelationship(
	ctx context.Context,
	token string,
	toolName string,
	source string,
	relationType string,
	target string,
) error {
	_, err := g.driver.Call(ctx, token, toolName, ToolArguments{
		SourceID:     source,
		RelationType: relationType,
		TargetID:     target,
	})
	if err != nil {
		return loggedError(
			ctx,
			fmt.Sprintf(
				"qa datagen: %s %s %s %s",
				toolName,
				source,
				relationType,
				target,
			),
			err,
		)
	}
	return nil
}
