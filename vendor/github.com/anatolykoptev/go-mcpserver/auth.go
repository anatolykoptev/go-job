package mcpserver

import (
	"context"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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
	// ToolFilter controls per-tool access based on token info.
	// Return true to allow, false to hide/deny.
	// Called for tools/list (filtering) and tools/call (enforcement).
	// Nil = all tools allowed.
	ToolFilter func(ctx context.Context, toolName string, info *TokenInfo) bool
}

// ProtectedResourceMetadata re-exports oauthex type so consumers
// don't need to import oauthex directly.
type ProtectedResourceMetadata = oauthex.ProtectedResourceMetadata

// TokenInfo re-exports auth.TokenInfo for consumer convenience.
type TokenInfo = auth.TokenInfo

// TokenInfoFromContext retrieves token info set by bearer auth middleware.
var TokenInfoFromContext = auth.TokenInfoFromContext

// StaticTokenVerifier returns a [auth.TokenVerifier] that accepts a single
// pre-shared token. Useful for internal services that don't need full OAuth.
func StaticTokenVerifier(token string) auth.TokenVerifier {
	return func(_ context.Context, t string, _ *http.Request) (*auth.TokenInfo, error) {
		if t != token {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{Expiration: time.Now().Add(time.Hour)}, nil
	}
}

// toolFilterMiddleware returns MCP middleware that filters tools based on
// token info. On tools/list it removes denied tools; on tools/call it
// rejects calls to denied tools with an error result.
func toolFilterMiddleware(filter func(context.Context, string, *TokenInfo) bool) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			info := tokenInfoFromRequest(req)
			switch method {
			case "tools/list":
				return filterToolList(ctx, method, req, next, filter, info)
			case "tools/call":
				return filterToolCall(ctx, method, req, next, filter, info)
			}
			return next(ctx, method, req)
		}
	}
}

func filterToolList(
	ctx context.Context, method string, req mcp.Request,
	next mcp.MethodHandler, filter func(context.Context, string, *TokenInfo) bool, info *TokenInfo,
) (mcp.Result, error) {
	result, err := next(ctx, method, req)
	if err != nil || result == nil {
		return result, err
	}
	lr := result.(*mcp.ListToolsResult)
	filtered := make([]*mcp.Tool, 0, len(lr.Tools))
	for _, t := range lr.Tools {
		if filter(ctx, t.Name, info) {
			filtered = append(filtered, t)
		}
	}
	lr.Tools = filtered
	return lr, nil
}

func filterToolCall(
	ctx context.Context, method string, req mcp.Request,
	next mcp.MethodHandler, filter func(context.Context, string, *TokenInfo) bool, info *TokenInfo,
) (mcp.Result, error) {
	name := req.GetParams().(*mcp.CallToolParamsRaw).Name
	if !filter(ctx, name, info) {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "tool not permitted: " + name}},
		}, nil
	}
	return next(ctx, method, req)
}

func tokenInfoFromRequest(req mcp.Request) *TokenInfo {
	if extra := req.GetExtra(); extra != nil {
		return extra.TokenInfo
	}
	return nil
}
