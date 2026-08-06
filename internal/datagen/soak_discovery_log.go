package datagen

import (
	"context"
	"fmt"
	"log/slog"
)

func loggedDiscoveryListSkip(
	ctx context.Context,
	nodeType string,
	reference string,
	err error,
) {
	loggedDiscoverySkip(ctx, nodeType, collectionItem{Reference: reference}, err)
}

func logIncompleteProjectSkip(
	ctx context.Context,
	item collectionItem,
	stateCount int,
	issueCount int,
) {
	err := fmt.Errorf(
		"qa datagen soak: project has %d states and %d issues",
		stateCount,
		issueCount,
	)
	loggedDiscoverySkip(ctx, "project", item, err)
}

func loggedDiscoverySkip(
	ctx context.Context,
	singular string,
	item collectionItem,
	err error,
) error {
	slog.WarnContext(
		ctx,
		"qa.datagen.soak.discovery_skipped",
		slog.String("node_type", singular),
		slog.String("name", item.Name),
		slog.String("reference", item.Reference),
		slog.String("err", err.Error()),
	)
	return err
}
