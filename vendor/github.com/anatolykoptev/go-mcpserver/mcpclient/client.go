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
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultTimeout           = 15 * time.Second
	defaultMaxFireConcurrent = 50
)

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

// ErrRejected is returned when the server was reached but refused the request:
// a JSON-RPC error response, or an HTTP 4xx status (413 body too large, 400,
// 401, 403, 404 ...). It is never suppressed by WithUnreachableTolerant -
// retrying or ignoring it would hide a request that can never succeed.
var ErrRejected = errors.New("mcpclient: server rejected request")

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

	maxFireConcurrent int
	fireSem           chan struct{}

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

// WithMaxFireConcurrency sets the maximum number of concurrent Fire()
// goroutines. When the limit is reached, additional Fire() calls are
// dropped with a slog.Warn. Defaults to 50.
func WithMaxFireConcurrency(n int) Option {
	return func(c *Client) { c.maxFireConcurrent = n }
}

// New creates a Client pointing at baseURL.
func New(baseURL string, opts ...Option) *Client {
	c := &Client{
		baseURL:           baseURL,
		timeout:           defaultTimeout,
		reuse:             true,
		maxFireConcurrent: defaultMaxFireConcurrent,
	}
	for _, o := range opts {
		o(c)
	}
	if c.maxFireConcurrent <= 0 {
		c.maxFireConcurrent = defaultMaxFireConcurrent
	}
	c.fireSem = make(chan struct{}, c.maxFireConcurrent)
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
	rec := &statusRecorder{}
	callCtx = context.WithValue(callCtx, statusRecorderKey{}, rec)

	sess, err := c.session_(callCtx)
	if err != nil {
		return nil, c.classifyErr(err, rec)
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
		return nil, c.classifyErr(err, rec)
	}
	if result.IsError {
		return result, fmt.Errorf("%w: %s", ErrToolError, textFrom(result))
	}
	return result, nil
}

// Fire calls the named tool in a background goroutine detached from ctx so
// the push survives the caller's handler return. Errors are logged at Warn
// level. The call is still bounded by WithTimeout. Concurrent Fire() calls
// are bounded by WithMaxFireConcurrency (default 50); when at capacity, the
// call is dropped with a slog.Warn instead of spawning an unbounded goroutine.
func (c *Client) Fire(ctx context.Context, tool string, args map[string]any) {
	// Non-blocking semaphore acquire — drop if at capacity.
	select {
	case c.fireSem <- struct{}{}:
	default:
		slog.Warn("mcpclient: fire dropped — max concurrent fire reached",
			slog.String("tool", tool),
			slog.String("url", c.baseURL),
			slog.Int("max", c.maxFireConcurrent))
		return
	}

	// Detach from the parent context so the goroutine isn't cancelled when the
	// caller's handler returns. Each call still has its own WithTimeout.
	detached := context.WithoutCancel(ctx)
	go func() {
		defer func() { <-c.fireSem }()
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
	// Always wrap the transport: statusTransport records the HTTP status of
	// rejected responses so classifyErr can tell "server said no" (413, 400 ...)
	// from "server unreachable" - the SDK reduces both to a plain error string.
	var rt http.RoundTripper = &statusTransport{base: httpClient.Transport}
	if c.bearer != "" {
		rt = &bearerTransport{base: rt, token: c.bearer}
	}
	httpClient = &http.Client{
		Transport:     rt,
		CheckRedirect: httpClient.CheckRedirect,
		Jar:           httpClient.Jar,
		Timeout:       httpClient.Timeout,
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

// statusRecorderKey carries a per-call *statusRecorder through the request
// context to statusTransport.
type statusRecorderKey struct{}

// statusRecorder remembers the last HTTP status seen for one Call.
type statusRecorder struct{ status atomic.Int32 }

// statusTransport records every response status into the statusRecorder the
// request context carries (if any), then passes the response through untouched.
type statusTransport struct{ base http.RoundTripper }

func (t *statusTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if err == nil {
		if rec, ok := req.Context().Value(statusRecorderKey{}).(*statusRecorder); ok {
			rec.status.Store(int32(resp.StatusCode)) //nolint:gosec // HTTP status fits int32
		}
	}
	return resp, err
}

// rejectedStatus reports whether an HTTP status means the server was reached
// and refused the request. 5xx and 429/408 are transient and stay "unreachable"
// (mirrors the go-sdk's own transient set: 429, 500, 502, 503, 504).
func rejectedStatus(code int) bool {
	if code < http.StatusBadRequest || code >= http.StatusInternalServerError {
		return false
	}
	return code != http.StatusTooManyRequests && code != http.StatusRequestTimeout
}

// classifyErr turns a Connect/CallTool error into ErrRejected (server reached
// and said no: JSON-RPC error or HTTP 4xx) or, for transport-level failures,
// defers to unreachableErr.
func (c *Client) classifyErr(err error, rec *statusRecorder) error {
	if err == nil {
		return nil
	}
	if hasServerRPCError(err) || rejectedStatus(int(rec.status.Load())) {
		return fmt.Errorf("%w: %w", ErrRejected, err)
	}
	return c.unreachableErr(err)
}

// codeRejectedByTransport is the SDK's own sentinel (jsonrpc2.ErrRejected,
// code -32005) that it wraps around transient transport failures such as 503.
// It is a *jsonrpc.Error in the chain but does NOT mean the server answered.
const codeRejectedByTransport = -32005

// hasServerRPCError reports whether err's tree holds a JSON-RPC error the
// server actually sent, ignoring the SDK's transport-rejection sentinel.
func hasServerRPCError(err error) bool {
	if err == nil {
		return false
	}
	if we, ok := err.(*jsonrpc.Error); ok && we.Code != codeRejectedByTransport { //nolint:errorlint // walking the tree manually
		return true
	}
	switch u := err.(type) { //nolint:errorlint // walking the tree manually
	case interface{ Unwrap() []error }:
		for _, e := range u.Unwrap() {
			if hasServerRPCError(e) {
				return true
			}
		}
	case interface{ Unwrap() error }:
		return hasServerRPCError(u.Unwrap())
	}
	return false
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
