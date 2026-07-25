package datagen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
)

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

func decodeRPCEnvelope(payload []byte) (json.RawMessage, *rpcError, error) {
	var envelope rpcEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, nil, err
	}
	if envelope.JSONRPC != jsonRPCVersion {
		return nil, nil, fmt.Errorf(
			"jsonrpc = %q, want %q",
			envelope.JSONRPC,
			jsonRPCVersion,
		)
	}
	if len(envelope.ID) == 0 {
		return nil, nil, fmt.Errorf("JSON-RPC response id is required")
	}
	hasResult := len(envelope.Result) > 0
	hasError := len(envelope.Error) > 0
	if hasResult == hasError {
		return nil, nil, fmt.Errorf("JSON-RPC response must contain exactly one of result or error")
	}
	if !hasError {
		return envelope.Result, nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(envelope.Error), []byte("null")) {
		return nil, nil, fmt.Errorf("JSON-RPC error must be an object")
	}
	var responseError rpcError
	if err := json.Unmarshal(envelope.Error, &responseError); err != nil {
		slog.Error("qa.datagen.failed", slog.String("err", err.Error()))
		return nil, nil, fmt.Errorf("decode JSON-RPC error: %w", err)
	}
	return nil, &responseError, nil
}
