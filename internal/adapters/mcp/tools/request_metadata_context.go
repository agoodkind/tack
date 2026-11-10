package tools

import "context"

// MCPRequestMetadataFromContext returns the transport identity that
// WithMCPRequestMetadata stored in ctx.
//
// The function returns true as its second result for a stored value, even
// when both identity fields are empty. It returns zero metadata and false
// when the context does not contain that value.
//
//   - After storing RequestID "request-7" and SessionID "session-3", the
//     returned metadata preserves each field's value.
func MCPRequestMetadataFromContext(ctx context.Context) (MCPRequestMetadata, bool) {
	metadata, ok := ctx.Value(mcpRequestMetadataKey{}).(MCPRequestMetadata)
	return metadata, ok
}
