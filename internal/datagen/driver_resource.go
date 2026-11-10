package datagen

import (
	"context"
	"encoding/json"
	"log/slog"
)

const (
	resourcesRead     = "resources/read"
	gettingStartedURI = "tack://getting-started"
)

type readResourceParams struct {
	URI string `json:"uri"`
}

type resourceRequest struct {
	JSONRPC string             `json:"jsonrpc"`
	ID      string             `json:"id"`
	Method  string             `json:"method"`
	Params  readResourceParams `json:"params"`
}

// ReadResource reads one MCP resource through the authenticated HTTP handler
// that tool calls also use.
//
// Resource reads exercise the mcp.resource_read audit action. The original
// TACK-340 account recorded that missing generator resource traffic had hidden
// an absent emitter even after the ticket reported completion.
//
//   - A dry run logs the URI and returns a synthetic result.
//   - Other runs send resources/read and decode the response.
func (d *Driver) ReadResource(ctx context.Context, token, uri string) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, loggedError(ctx, "qa datagen: stop before reading "+uri, err)
	}
	d.callCount.Add(1)
	requestID, err := d.requestID(ctx, token, resourcesRead, ToolArguments{})
	if err != nil {
		return Result{}, err
	}
	if d.dryRun {
		slog.InfoContext(ctx, "qa.datagen.resource_read_planned", slog.String("uri", uri))
		return syntheticResult(requestID), nil
	}
	body, err := json.Marshal(resourceRequest{
		JSONRPC: jsonRPCVersion,
		ID:      requestID,
		Method:  resourcesRead,
		Params:  readResourceParams{URI: uri},
	})
	if err != nil {
		return Result{}, loggedError(ctx, "qa datagen: encode "+uri+" read", err)
	}
	response, err := d.send(ctx, token, body, true)
	if err != nil {
		return Result{}, err
	}
	return decodeResponse(ctx, resourcesRead, response)
}
