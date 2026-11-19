package ops

import (
	"context"
	"fmt"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
)

func preflightParentRelationshipViews(ctx context.Context, nodes *fdbadapter.NodeStore) error {
	cursor := ""

	for {
		next, err := nodes.CheckParentMigrationViews(ctx, cursor)
		if err != nil {
			return parentRelationshipFailure(
				ctx,
				fmt.Sprintf("validate legacy parent views after cursor %q", cursor),
				err,
			)
		}

		if next == "" {
			return nil
		}

		cursor = next
	}
}
