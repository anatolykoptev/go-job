// Package mcpclient provides a thin reusable MCP client over the go-sdk
// StreamableClientTransport.
//
// It centralises the krolik inter-service conventions that were hand-rolled in
// seven places across the fleet:
//   - Accept + SSE framing — delegated entirely to StreamableClientTransport.
//   - Per-call timeout — every Call wraps ctx in context.WithTimeout.
//   - Lazy session + reconnect on transport error — guarded by a single mutex.
//   - Unreachable-tolerant mode — dial/connect errors map to ("", nil).
//   - Fire-and-forget — context.WithoutCancel so the push survives handler return.
//   - Text content concatenation — the 90% path via CallText.
package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const defaultTimeout = 15 * time.Second

// clientImpl is the MCP implementation descriptor sent during Connect.
// Allocated once at package init rather than on every connect call.
var clientImpl = &mcp.Implementation{
	Name:    "go-mcpserver/mcpclient",
	Version: "1.0.0",
}

// ErrUnreachable is returned (or suppressed with WithUnreachableTolerant) when
// the MCP server cannot be reached at the transport level (dial failure, connection
// refused, etc.).
var ErrUnreachable = errors.New("mcpclient: server unreachable")

// ErrToolError is returned when the tool ran but the server set IsError on the
// result. It is always surfaced regardless of WithUnreachableTolerant.
var ErrToolError = errors.New("mcpclient: tool returned error")

// Client is a reusable MCP client for a single remote server.
// It is safe for concurrent use.
type Client struct {
	baseURL    string
	httpClient *http.Client
	bearer     string
	timeout    time.Duration
	tolerant   bool // WithUnreachableTolerant
	reuse      bool // WithSessionReuse

	mu      sync.Mutex
	session *mcp.ClientSession // nil = not connected or dropped
}

// Option configures a Client.
type Option func(*Client)

// WithTimeout sets the per-call context timeout (default 15s).
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.timeout = d }
}

// WithHTTPClient replaces the default *http.Client used by the transport.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}

// WithBearer sets an Authorization: Bearer <token> header on all requests.
// Implemented via a custom http.RoundTripper wrapping the base transport.
func WithBearer(token string) Option {
	return func(c *Client) { c.bearer = token }
}

// WithUnreachableTolerant controls whether dial/transport errors are silenced.
// When true, CallText returns ("", nil) on unreachable; Fire just logs.
// Defaults to false — errors are always returned.
func WithUnreachableTolerant(v bool) Option {
	return func(c *Client) { c.tolerant = v }
}

// WithSessionReuse controls whether a single ClientSession is kept across calls.
// On transport error the session is dropped and the next call reconnects.
// Defaults to true.
func WithSessionReuse(v bool) Option {
	return func(c *Client) { c.reuse = v }
}

// New creates a Client pointing at baseURL.
func New(baseURL string, opts ...Option) *Client {
	c := &Client{
		baseURL: baseURL,
		timeout: defaultTimeout,
		reuse:   true,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// CallText calls the named tool with args and returns the concatenated text
// content from the result. Non-text content parts are ignored. If the server
// returns IsError=true the error is wrapped with ErrToolError.
func (c *Client) CallText(ctx context.Context, tool string, args map[string]any) (string, error) {
	result, err := c.Call(ctx, tool, args)
	if err != nil {
		return "", err
	}
	return textFrom(result), nil
}

// Call calls the named tool and returns the raw *mcp.CallToolResult.
func (c *Client) Call(ctx context.Context, tool string, args map[string]any) (*mcp.CallToolResult, error) {
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	sess, err := c.session_(callCtx)
	if err != nil {
		return nil, c.unreachableErr(err)
	}
	// In non-reuse mode, the session was created solely for this call and is
	// never cached; close it when the call completes so the SSE goroutine and
	// socket are always released.
	if !c.reuse {
		defer func() { _ = sess.Close() }()
	}

	result, err := sess.CallTool(callCtx, &mcp.CallToolParams{
		Name:      tool,
		Arguments: args,
	})
	if err != nil {
		// Transport-level error — drop the session so the next call reconnects.
		c.dropSession()
		return nil, c.unreachableErr(err)
	}
	if result.IsError {
		return result, fmt.Errorf("%w: %s", ErrToolError, textFrom(result))
	}
	return result, nil
}

// Fire calls the named tool in a background goroutine detached from ctx so
// the push survives the caller's handler return. Errors are logged at Warn
// level. The call is still bounded by WithTimeout.
func (c *Client) Fire(ctx context.Context, tool string, args map[string]any) {
	// Detach from the parent context so the goroutine isn't cancelled when the
	// caller's handler returns. Each call still has its own WithTimeout.
	detached := context.WithoutCancel(ctx)
	go func() {
		_, err := c.Call(detached, tool, args)
		if err != nil && (!c.tolerant || !errors.Is(err, ErrUnreachable)) {
			slog.Warn("mcpclient: fire failed",
				slog.String("tool", tool),
				slog.String("url", c.baseURL),
				slog.Any("error", err))
		}
	}()
}

// Close closes the current session if one is open.
func (c *Client) Close() error {
	c.mu.Lock()
	sess := c.session
	c.session = nil
	c.mu.Unlock()

	if sess == nil {
		return nil
	}
	return sess.Close()
}

// session_ returns the current session, creating one if needed.
// single mutex held across Connect: serializes concurrent lazy-init and
// reconnect by design. No double-checked locking — the network round-trip
// under lock is intentional (sessions are not hot-path; correctness over
// micro-contention).
func (c *Client) session_(ctx context.Context) (*mcp.ClientSession, error) {
	if c.reuse {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.session != nil {
			return c.session, nil
		}
	}
	return c.connect(ctx)
}

// connect creates a new ClientSession. Must be called with c.mu held when reuse=true.
func (c *Client) connect(ctx context.Context) (*mcp.ClientSession, error) {
	httpClient := c.httpClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	if c.bearer != "" {
		httpClient = &http.Client{
			Transport: &bearerTransport{
				base:  httpClient.Transport,
				token: c.bearer,
			},
			CheckRedirect: httpClient.CheckRedirect,
			Jar:           httpClient.Jar,
			Timeout:       httpClient.Timeout,
		}
	}

	transport := &mcp.StreamableClientTransport{
		Endpoint:   c.baseURL + "/mcp",
		HTTPClient: httpClient,
	}

	client := mcp.NewClient(clientImpl, nil)

	sess, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, err
	}

	if c.reuse {
		c.session = sess
	}
	return sess, nil
}

// dropSession clears the cached session under lock so the next call reconnects.
func (c *Client) dropSession() {
	if !c.reuse {
		return
	}
	c.mu.Lock()
	sess := c.session
	c.session = nil
	c.mu.Unlock()

	if sess != nil {
		_ = sess.Close()
	}
}

// unreachableErr wraps err as ErrUnreachable and, when tolerant mode is on,
// suppresses it (returns nil).
func (c *Client) unreachableErr(err error) error {
	if err == nil {
		return nil
	}
	// ErrToolError is always surfaced.
	if errors.Is(err, ErrToolError) {
		return err
	}
	wrapped := fmt.Errorf("%w: %w", ErrUnreachable, err)
	if c.tolerant {
		return nil
	}
	return wrapped
}

// textFrom concatenates all TextContent parts from result.
func textFrom(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	var parts []string
	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// bearerTransport injects Authorization: Bearer <token> on every request.
type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}
