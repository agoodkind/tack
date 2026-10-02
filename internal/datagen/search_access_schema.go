package datagen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	toolsList      = "tools/list"
	searchToolName = "tack_search"
	// entryArgumentSuffix ends the name of the entry point argument that
	// tack_search requires. The prefix comes from the caller's metadata.
	entryArgumentSuffix = "_reference"
	// maxToolListPages bounds one tools/list traversal.
	maxToolListPages = 20
)

type listToolsParams struct {
	Cursor string `json:"cursor,omitempty"`
}

type listToolsRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Method  string          `json:"method"`
	Params  listToolsParams `json:"params"`
}

type listedToolSchema struct {
	Required []string `json:"required"`
}

type listedTool struct {
	Name        string           `json:"name"`
	InputSchema listedToolSchema `json:"inputSchema"`
}

type listToolsResult struct {
	Tools      []listedTool `json:"tools"`
	NextCursor string       `json:"nextCursor"`
}

type searchCallParams struct {
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments"`
}

type searchCallRequest struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      string           `json:"id"`
	Method  string           `json:"method"`
	Params  searchCallParams `json:"params"`
}

// searchEntryArgument reads the tack_search input schema that tools/list
// returns for token and returns its one required entry argument.
func (d *Driver) searchEntryArgument(ctx context.Context, token string) (string, error) {
	cursor := ""
	for range maxToolListPages {
		page, err := d.listTools(ctx, token, cursor)
		if err != nil {
			return "", err
		}
		for _, tool := range page.Tools {
			if tool.Name == searchToolName {
				return requiredEntryArgument(ctx, tool.InputSchema.Required)
			}
		}
		if page.NextCursor == "" {
			return "", loggedError(ctx, "qa datagen: read the tack_search schema", errors.New("tools/list returned no tack_search tool"))
		}
		cursor = page.NextCursor
	}
	return "", loggedError(ctx, "qa datagen: read the tack_search schema",
		fmt.Errorf("tools/list did not finish within %d pages", maxToolListPages))
}

// requiredEntryArgument returns the one required argument that ends with
// entryArgumentSuffix.
func requiredEntryArgument(ctx context.Context, required []string) (string, error) {
	found := make([]string, 0, 1)
	for _, name := range required {
		if strings.HasSuffix(name, entryArgumentSuffix) {
			found = append(found, name)
		}
	}
	if len(found) != 1 {
		return "", loggedError(ctx, "qa datagen: read the tack_search entry argument",
			fmt.Errorf("required arguments %v contain %d entry arguments, want 1", required, len(found)))
	}
	return found[0], nil
}

// listTools reads one tools/list page through the authenticated MCP session.
func (d *Driver) listTools(ctx context.Context, token, cursor string) (listToolsResult, error) {
	requestID, err := d.requestID(ctx, token, toolsList+":"+cursor, ToolArguments{})
	if err != nil {
		return listToolsResult{}, err
	}
	body, err := json.Marshal(listToolsRequest{
		JSONRPC: jsonRPCVersion, ID: requestID, Method: toolsList, Params: listToolsParams{Cursor: cursor},
	})
	if err != nil {
		return listToolsResult{}, loggedError(ctx, "qa datagen: encode tools/list request", err)
	}
	response, err := d.send(ctx, token, body, true)
	if err != nil {
		return listToolsResult{}, err
	}
	payload, err := responsePayload(ctx, toolsList, response)
	if err != nil {
		return listToolsResult{}, err
	}
	resultPayload, responseError, err := decodeRPCEnvelope(payload)
	if err != nil {
		return listToolsResult{}, loggedError(ctx, "qa datagen: decode tools/list response", err)
	}
	if responseError != nil {
		responseError.toolName = toolsList
		return listToolsResult{}, responseError
	}
	var result listToolsResult
	if err := json.Unmarshal(resultPayload, &result); err != nil {
		return listToolsResult{}, loggedError(ctx, "qa datagen: decode tools/list result", err)
	}
	return result, nil
}

// callSearchArguments calls tack_search with exactly arguments through the
// authenticated MCP session.
func (d *Driver) callSearchArguments(ctx context.Context, token string, arguments map[string]string) (Result, error) {
	identity, err := json.Marshal(arguments)
	if err != nil {
		return Result{}, loggedError(ctx, "qa datagen: encode tack_search arguments", err)
	}
	requestID, err := d.requestID(ctx, token, searchToolName+":"+string(identity), ToolArguments{})
	if err != nil {
		return Result{}, err
	}
	body, err := json.Marshal(searchCallRequest{
		JSONRPC: jsonRPCVersion, ID: requestID, Method: toolsCall,
		Params: searchCallParams{Name: searchToolName, Arguments: arguments},
	})
	if err != nil {
		return Result{}, loggedError(ctx, "qa datagen: encode tack_search request", err)
	}
	response, err := d.send(ctx, token, body, true)
	if err != nil {
		return Result{}, err
	}
	return decodeResponse(ctx, searchToolName, response)
}
