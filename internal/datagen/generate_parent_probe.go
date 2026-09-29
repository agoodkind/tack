package datagen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// parentRefusalText is part of the error that tack_remove_relationship
// returns when the edge is the source node's only hierarchy parent.
const parentRefusalText = "only hierarchy parent"

// errParentRemoved reports that tack_remove_relationship removed a node's
// only hierarchy parent edge.
var errParentRemoved = errors.New("the only hierarchy parent edge was removed")

// probeParentRemovalRefused calls tack_remove_relationship on the child_of
// edge from issueReference to parentReference, which is the issue's only
// hierarchy parent. The server must refuse the call. A dry run plans the
// call and skips the check.
func (g *Generator) probeParentRemovalRefused(ctx context.Context, token, issueReference, parentReference string) error {
	_, err := g.driver.Call(ctx, token, "tack_remove_relationship", ToolArguments{
		SourceID:     issueReference,
		RelationType: "child_of",
		TargetID:     parentReference,
	})
	if g.dryRun {
		return nil
	}
	message := fmt.Sprintf("qa datagen: parent removal probe for issue %s and parent %s", issueReference, parentReference)
	if err == nil {
		return loggedError(ctx, message, errParentRemoved)
	}
	if !strings.Contains(err.Error(), parentRefusalText) {
		return loggedError(ctx, message, err)
	}
	slog.InfoContext(ctx, "qa.datagen.parent_removal_refused",
		slog.String("issue", issueReference), slog.String("parent", parentReference))
	return nil
}
