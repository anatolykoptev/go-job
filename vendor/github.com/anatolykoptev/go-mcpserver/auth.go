package mcpserver

import (
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// BearerAuth configures OAuth 2.1 bearer token verification for /mcp.
// Auth wraps the /mcp handler only; /health, /metrics, and metadata
// endpoints remain unauthenticated. For full-server auth, use
// Config.Middleware with auth.RequireBearerToken() directly.
type BearerAuth struct {
	// Verifier validates bearer tokens. Required.
	Verifier auth.TokenVerifier
	// Scopes lists required scopes. Empty = any valid token accepted.
	Scopes []string
	// ResourceMetadataPath is the path for the RFC 9728 metadata endpoint.
	// Default: "/.well-known/oauth-protected-resource" when Metadata is set.
	ResourceMetadataPath string
	// Metadata for RFC 9728 endpoint. Nil = no metadata endpoint.
	Metadata *ProtectedResourceMetadata
}

// ProtectedResourceMetadata re-exports oauthex type so consumers
// don't need to import oauthex directly.
type ProtectedResourceMetadata = oauthex.ProtectedResourceMetadata

// TokenInfo re-exports auth.TokenInfo for consumer convenience.
type TokenInfo = auth.TokenInfo

// TokenInfoFromContext retrieves token info set by bearer auth middleware.
var TokenInfoFromContext = auth.TokenInfoFromContext
