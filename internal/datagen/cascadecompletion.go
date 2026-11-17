package datagen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// notFoundText is part of the error that a get tool returns for a missing
// node.
const notFoundText = "not found"

const (
	cascadeCompletionTimeout = 2 * time.Minute
	cascadePollInterval      = 250 * time.Millisecond
)

// errCascadeChildKept reports that a child of a deleted project still exists.
var errCascadeChildKept = errors.New("a child of the deleted project still exists")

func recordCascadeDelete(
	ctx context.Context,
	proof *CascadeProbeResult,
	deleted Result,
	root string,
) error {
	state := "finished"
	if strings.Contains(deleted.Text(), "- Delete state: running") {
		state = "running"
	} else if !strings.Contains(deleted.Text(), "- Delete state: finished") {
		return loggedError(
			ctx,
			"qa datagen: read cascade deletion state",
			errors.New("delete output has no recognized state"),
		)
	}
	_, suffix, found := strings.Cut(deleted.Text(), "Delete job: `")
	jobID, _, terminated := strings.Cut(suffix, "`")
	if !found || !terminated || jobID == "" {
		return loggedError(
			ctx,
			"qa datagen: read cascade deletion job",
			errors.New("delete output has no job ID"),
		)
	}
	proof.Deletes = append(
		proof.Deletes,
		CascadeDeleteEvidence{RootReference: root, JobID: jobID, InitialState: state},
	)
	return nil
}

// requireCascadeGone polls public reads until a running deletion has removed
// the root and every probe child. After a finished deletion, the first public
// read must already return them as absent.
func (g *Generator) requireCascadeGone(
	ctx context.Context,
	token string,
	workspaceReference string,
	identifier string,
	deleted Result,
	projectID string,
	issueIDs []string,
	commentID string,
) error {
	background := strings.Contains(deleted.Text(), "- Delete state: running")
	if !background && !strings.Contains(deleted.Text(), "- Delete state: finished") {
		return loggedError(
			ctx,
			"qa datagen: read cascade deletion state",
			errors.New("delete output has no recognized state"),
		)
	}
	ctx, cancel := context.WithTimeout(ctx, cascadeCompletionTimeout)
	defer cancel()
	children := []struct {
		tool  string
		rawID string
	}{
		{tool: "tack_get_project", rawID: projectID},
	}
	for _, issueID := range issueIDs {
		children = append(
			children,
			struct{ tool, rawID string }{tool: "tack_get_issue", rawID: issueID},
		)
	}
	if commentID != "" {
		children = append(
			children,
			struct{ tool, rawID string }{tool: "tack_get_comment", rawID: commentID},
		)
	}
	ticker := time.NewTicker(cascadePollInterval)
	defer ticker.Stop()
	for {
		remaining := ""
		for _, child := range children {
			_, err := g.driver.Call(
				ctx,
				token,
				child.tool,
				ToolArguments{WorkspaceReference: workspaceReference, NodeID: child.rawID},
			)
			message := fmt.Sprintf(
				"qa datagen: %s %s after the project delete",
				child.tool,
				child.rawID,
			)
			if err == nil {
				remaining = message
				continue
			}
			if !strings.Contains(err.Error(), notFoundText) {
				return loggedError(ctx, message, err)
			}
		}
		if remaining == "" {
			break
		}
		if !background {
			return loggedError(ctx, remaining, errCascadeChildKept)
		}
		select {
		case <-ctx.Done():
			return loggedError(ctx, remaining, errors.Join(errCascadeChildKept, ctx.Err()))
		case <-ticker.C:
		}
	}
	slog.InfoContext(ctx, "qa.datagen.cascade_delete_verified", slog.String("project", identifier))
	return nil
}
