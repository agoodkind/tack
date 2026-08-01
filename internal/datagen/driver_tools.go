package datagen

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

const toolsList = "tools/list"

type listToolsRequest struct {
	JSONRPC string       `json:"jsonrpc"`
	ID      string       `json:"id"`
	Method  string       `json:"method"`
	Params  listToolArgs `json:"params"`
}

type listToolArgs struct{}

type listedTool struct {
	Name string `json:"name"`
}

type listToolsResult struct {
	Tools []listedTool `json:"tools"`
}

// ListTools returns the tools registered for one authenticated user.
func (d *Driver) ListTools(ctx context.Context, token string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, loggedError(ctx, "qa datagen: stop before tools/list call", err)
	}
	requestID, err := d.requestID(ctx, token, toolsList, ToolArguments{})
	if err != nil {
		return nil, err
	}
	if d.dryRun {
		slog.InfoContext(
			ctx,
			"qa.datagen.request_planned",
			slog.String("method", toolsList),
		)
		return nil, nil
	}
	body, err := json.Marshal(listToolsRequest{
		JSONRPC: jsonRPCVersion,
		ID:      requestID,
		Method:  toolsList,
		Params:  listToolArgs{},
	})
	if err != nil {
		return nil, loggedError(ctx, "qa datagen: encode tools/list request", err)
	}
	response, err := d.send(ctx, token, body, true)
	if err != nil {
		return nil, err
	}
	payload, err := responsePayload(ctx, toolsList, response)
	if err != nil {
		return nil, err
	}
	resultPayload, responseError, err := decodeRPCEnvelope(payload)
	if err != nil {
		return nil, loggedError(ctx, "qa datagen: decode tools/list response", err)
	}
	if responseError != nil {
		responseError.toolName = toolsList
		return nil, responseError
	}
	var result listToolsResult
	if err := json.Unmarshal(resultPayload, &result); err != nil {
		return nil, loggedError(ctx, "qa datagen: decode tools/list result", err)
	}
	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		if strings.TrimSpace(tool.Name) == "" {
			return nil, fmt.Errorf("qa datagen: tools/list returned an empty tool name")
		}
		names = append(names, tool.Name)
	}
	return names, nil
}
