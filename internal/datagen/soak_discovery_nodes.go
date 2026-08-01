package datagen

import "context"

func discoverScopedNodes(
	ctx context.Context,
	driver *Driver,
	token string,
	workspaceReference string,
	projectReference string,
	resolutionScope string,
	singular string,
	plural string,
	definitions map[string]discoveryDefinition,
) ([]soakNode, error) {
	args := ToolArguments{WorkspaceReference: workspaceReference}
	if projectReference != "" {
		args.ProjectReference = projectReference
	}
	list, err := driver.Call(ctx, token, "tack_list_"+plural, args)
	if err != nil {
		loggedDiscoveryListSkip(ctx, singular, projectReference, err)
		return nil, nil
	}
	nodes := make([]soakNode, 0, len(list.collectionItems()))
	for _, item := range list.collectionItems() {
		resolved, err := resolveCollectionItem(
			ctx,
			driver,
			token,
			workspaceReference,
			singular,
			item,
			resolutionScope,
			discoveryDefinitionFor(definitions, item.Name),
		)
		if err != nil {
			continue
		}
		nodes = append(nodes, resolved)
	}
	return nodes, nil
}
