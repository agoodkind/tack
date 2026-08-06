package datagen

import "context"

func discoverLabels(
	ctx context.Context,
	session *runSession,
	workspace WorkspaceIdentity,
	workspaceIndex int,
) ([]soakNode, error) {
	actor := workspace.Actors[workspaceIndex%len(workspace.Actors)]
	list, err := session.driver.Call(
		ctx,
		actor.Token,
		"tack_list_labels",
		ToolArguments{WorkspaceReference: workspace.Slug},
	)
	if err != nil {
		loggedDiscoveryListSkip(ctx, "label", workspace.Slug, err)
		return nil, nil
	}
	labels := make([]soakNode, 0, len(list.collectionItems()))
	for _, item := range list.collectionItems() {
		recovery := &discoveryDefinition{
			token: actor.Token,
			arguments: ToolArguments{
				WorkspaceReference: workspace.Slug,
				Name:               item.Name,
			},
		}
		resolved, err := resolveCollectionItem(
			ctx,
			session.driver,
			actor.Token,
			workspace.Slug,
			"label",
			item,
			workspace.Slug,
			recovery,
		)
		if err != nil {
			continue
		}
		labels = append(labels, resolved)
	}
	return labels, nil
}
