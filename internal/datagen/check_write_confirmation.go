package datagen

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

const (
	maxWriteConfirmationBytes = 1024
	issueStateLinePrefix      = "- State: `"
	inProgressStateName       = "In Progress"
	todoStateName             = "Todo"
	stateIDKey                = "state_id"
)

// updateIssueAndCheckConfirmation updates the issue state and QA properties.
// It checks the state change and true checkbox, then repeats the update and
// checks that state_id, qa_checkbox, and qa_text report Unchanged.
// Dry runs send one update without checking its confirmation.
func (g *Generator) updateIssueAndCheckConfirmation(
	ctx context.Context,
	actor Actor,
	workspace WorkspaceIdentity,
	projectIdentifier string,
	issueReference string,
) error {
	properties := newProperties()
	properties.setBool("qa_checkbox", true)
	properties.setString("qa_text", g.content.Comment())
	if g.dryRun {
		_, err := g.updateIssue(ctx, actor, workspace, issueReference, properties)
		return err
	}
	current, err := g.knownIssueState(ctx, actor, workspace, projectIdentifier, issueReference)
	if err != nil {
		return err
	}
	target := projectIdentifier + "::" + inProgressStateName
	if current == target {
		target = projectIdentifier + "::" + todoStateName
	}
	properties.setString(stateIDKey, target)
	first, err := g.updateIssue(ctx, actor, workspace, issueReference, properties)
	if err != nil {
		return err
	}
	if err := checkConfirmation(ctx, issueReference, first,
		[]string{fmt.Sprintf("- Changed: `%s` from `%s` to `%s`", stateIDKey, current, target)},
		[]string{"- Unchanged: `qa_checkbox` is `true`", "- Changed: `qa_checkbox` from `false` to `true`"},
	); err != nil {
		return err
	}
	repeated, err := g.updateIssue(ctx, actor, workspace, issueReference, properties)
	if err != nil {
		return err
	}
	if err := checkConfirmation(ctx, issueReference, repeated,
		[]string{fmt.Sprintf("- Unchanged: `%s` is `%s`", stateIDKey, target)},
		[]string{"- Unchanged: `qa_checkbox` is `true`"},
		[]string{"- Unchanged: `qa_text` is `"},
	); err != nil {
		return err
	}
	slog.InfoContext(ctx, "qa.datagen.write_confirmation_verified",
		slog.String("issue", issueReference),
		slog.String("from_state", current),
		slog.String("to_state", target),
	)
	return nil
}

// knownIssueState returns the issue's Todo or In Progress reference, moving other
// states to Todo to avoid shortened references in the confirmation check.
func (g *Generator) knownIssueState(
	ctx context.Context,
	actor Actor,
	workspace WorkspaceIdentity,
	projectIdentifier string,
	issueReference string,
) (string, error) {
	current, err := g.issueStateReference(ctx, actor, workspace, issueReference)
	if err != nil {
		return "", err
	}
	todo := projectIdentifier + "::" + todoStateName
	if current == todo || current == projectIdentifier+"::"+inProgressStateName {
		return current, nil
	}
	properties := newProperties()
	properties.setString(stateIDKey, todo)
	if _, err := g.updateIssue(ctx, actor, workspace, issueReference, properties); err != nil {
		return "", err
	}
	return todo, nil
}

func (g *Generator) updateIssue(
	ctx context.Context,
	actor Actor,
	workspace WorkspaceIdentity,
	issueReference string,
	properties NodeProperties,
) (Result, error) {
	result, err := g.driver.Call(ctx, actor.Token, "tack_update_issue", ToolArguments{
		WorkspaceReference: workspace.Slug,
		NodeID:             issueReference,
		Properties:         properties,
	})
	if err != nil {
		return Result{}, loggedError(ctx, "qa datagen: update issue", err)
	}
	return result, nil
}

func (g *Generator) issueStateReference(
	ctx context.Context,
	actor Actor,
	workspace WorkspaceIdentity,
	issueReference string,
) (string, error) {
	result, err := g.driver.Call(ctx, actor.Token, "tack_get_issue", ToolArguments{
		WorkspaceReference: workspace.Slug,
		NodeID:             issueReference,
	})
	if err != nil {
		return "", loggedError(ctx, "qa datagen: read issue state before update", err)
	}
	for _, line := range strings.Split(result.Text(), "\n") {
		rest, found := strings.CutPrefix(line, issueStateLinePrefix)
		if !found {
			continue
		}
		reference, _, closed := strings.Cut(rest, "`")
		if closed && reference != "" {
			return reference, nil
		}
	}
	return "", loggedError(ctx, "qa datagen: read issue state before update",
		fmt.Errorf("response for issue %s does not include a readable State reference: %s", issueReference, result.Text()))
}

// checkConfirmation checks the byte limit and requires at least one matching
// line prefix from each group.
func checkConfirmation(ctx context.Context, issueReference string, result Result, wanted ...[]string) error {
	operation := "qa datagen: check update confirmation for " + issueReference
	text := result.Text()
	if len(text) > maxWriteConfirmationBytes {
		return loggedError(ctx, operation,
			fmt.Errorf("the confirmation is %d bytes, above the %d-byte limit: %s", len(text), maxWriteConfirmationBytes, text))
	}
	for _, prefixes := range wanted {
		if !containsAnyLine(text, prefixes) {
			return loggedError(ctx, operation, fmt.Errorf("the confirmation does not include a line starting with any of %q: %s", prefixes, text))
		}
	}
	return nil
}

func containsAnyLine(text string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.Contains(text, "\n"+prefix) {
			return true
		}
	}
	return false
}
