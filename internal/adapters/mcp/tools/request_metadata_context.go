package tools

import "context"

// MCPRequestMetadataFromContext returns the transport identity attached by
// WithMCPRequestMetadata, and false when the context carries none.
func MCPRequestMetadataFromContext(ctx context.Context) (MCPRequestMetadata, bool) {
	metadata, ok := ctx.Value(mcpRequestMetadataKey{}).(MCPRequestMetadata)
	return metadata, ok
}
