package tools

import "context"

// MCPRequestMetadataFromContext returns the transport identity from
// WithMCPRequestMetadata. It returns false when the context omits that
// identity.
func MCPRequestMetadataFromContext(ctx context.Context) (MCPRequestMetadata, bool) {
	metadata, ok := ctx.Value(mcpRequestMetadataKey{}).(MCPRequestMetadata)
	return metadata, ok
}
