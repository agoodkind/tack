package datagen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"goodkind.io/tack/internal/runtime"
)

const (
	jsonRPCVersion = "2.0"
	toolsCall      = "tools/call"
)

type callParams struct {
	Name      string        `json:"name"`
	Arguments ToolArguments `json:"arguments"`
}

type rpcRequest struct {
	JSONRPC string     `json:"jsonrpc"`
	ID      string     `json:"id"`
	Method  string     `json:"method"`
	Params  callParams `json:"params"`
}

// Driver sends authenticated JSON-RPC requests through the in-process MCP
// handler.
type Driver struct {
	graph     *runtime.Graph
	dryRun    bool
	sessionID string
	callCount atomic.Int64
	tokenMu   sync.RWMutex
	tokenIDs  map[string]string
}

func (d *Driver) registerTokenIdentity(token, identity string) {
	if token == "" || identity == "" {
		return
	}
	d.tokenMu.Lock()
	defer d.tokenMu.Unlock()
	if d.tokenIDs == nil {
		d.tokenIDs = make(map[string]string)
	}
	d.tokenIDs[token] = identity
}

func (d *Driver) requestToken(token string) string {
	d.tokenMu.RLock()
	identity := d.tokenIDs[token]
	d.tokenMu.RUnlock()
	if identity == "" {
		return token
	}
	return identity
}

// NewDriver constructs a request driver with a session ID derived from seed.
//
// A nil graph supports dry runs only. Requests that use the HTTP handler
// require a graph.
//
//	driver := NewDriver(nil, true, 245)
func NewDriver(graph *runtime.Graph, dryRun bool, seed int64) *Driver {
	return &Driver{
		graph: graph, dryRun: dryRun,
		sessionID: fmt.Sprintf("qa-datagen-%d", seed),
	}
}

// Call invokes one MCP tool through the authenticated HTTP handler.
//
// A dry run logs the planned call and returns a synthetic result. Other calls
// encode tools/call and decode the handler's response.
//
//	result, err := driver.Call(ctx, token, "tack_list_workspaces", ToolArguments{})
func (d *Driver) Call(
	ctx context.Context,
	token string,
	toolName string,
	args ToolArguments,
) (Result, error) {
	return d.call(ctx, token, toolName, args)
}

func (d *Driver) call(
	ctx context.Context,
	token string,
	toolName string,
	args ToolArguments,
) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, loggedError(
			ctx,
			"qa datagen: stop before "+toolName+" call",
			err,
		)
	}
	d.callCount.Add(1)
	requestID, err := d.requestID(ctx, token, toolName, args)
	if err != nil {
		return Result{}, err
	}
	if d.dryRun {
		slog.InfoContext(ctx, "qa.datagen.call_planned",
			slog.String("tool", toolName),
			slog.Any("arguments", args),
		)
		return syntheticResult(requestID), nil
	}
	body, err := json.Marshal(rpcRequest{
		JSONRPC: jsonRPCVersion,
		ID:      requestID,
		Method:  toolsCall,
		Params:  callParams{Name: toolName, Arguments: args},
	})
	if err != nil {
		return Result{}, loggedError(ctx, "qa datagen: encode "+toolName+" request", err)
	}
	response, err := d.send(ctx, token, body, true)
	if err != nil {
		return Result{}, err
	}
	return decodeResponse(ctx, toolName, response)
}

// CallCount returns the number of tool calls and resource reads that the driver
// counted, including dry runs.
//
// The driver increments the count after checking context cancellation and
// before building the request. A later request failure still contributes to the
// count.
//
//   - A new driver starts at zero.
//   - A call with an already-canceled context leaves that value at zero.
func (d *Driver) CallCount() int64 {
	return d.callCount.Load()
}

func (d *Driver) requestID(
	ctx context.Context,
	token string,
	toolName string,
	args ToolArguments,
) (string, error) {
	body, err := json.Marshal(args)
	if err != nil {
		return "", loggedError(
			ctx,
			"qa datagen: encode "+toolName+" request identity",
			err,
		)
	}
	requestToken := d.requestToken(token)
	sum := sha256.Sum256(
		append([]byte(d.sessionID+":"+requestToken+":"+toolName+":"), body...),
	)
	return "datagen-" + hex.EncodeToString(sum[:12]), nil
}
