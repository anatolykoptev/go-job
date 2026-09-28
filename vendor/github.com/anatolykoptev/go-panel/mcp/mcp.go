// Package mcp auto-exposes go-panel Resources as MCP tools.
//
// Each registered Resource becomes two MCP tools:
//
//   - {resource_name}_list — list rows with optional pagination and sort.
//     Parameters: limit (int, default 50, max 500), offset (int, default 0),
//     sort_key (string, optional — must be one of the Resource's sortable
//     column keys), sort_dir (string, "asc" or "desc", optional).
//     Returns a JSON object: {"rows": [...], "total": N}.
//
//   - {resource_name}_get — get a single row's detail by ID.
//     Only registered when the Resource has a Detailer.
//     Parameters: id (string, required).
//     Returns a JSON array of DetailSection objects.
//
// The MCP server runs on a separate port from the admin panel HTTP server,
// sharing the same Panel instance and its registered Resources. Tenant
// context is set to the global default (single-tenant); multi-tenant MCP
// exposure is a future concern.
//
// Usage:
//
//	panel := resource.New(cfg)
//	resource.Register(panel, myResource)
//	// ... register more resources ...
//	go func() {
//	    if err := panelmcp.Run(ctx, panelmcp.Config{
//	        Panel: panel,
//	        Port:  "8895",
//	    }); err != nil {
//	        slog.Error("mcp server failed", "error", err)
//	    }
//	}()
//	// serve panel.Handler() on the admin port as usual
package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"time"

	"github.com/anatolykoptev/go-mcpserver"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go-panel/tenant"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultListLimit = 50
	maxListLimit     = 500
)

// Config configures the MCP server that exposes panel Resources as MCP tools.
type Config struct {
	// Panel is the admin panel whose Resources are exposed as MCP tools.
	// Required — Run panics if nil.
	Panel *resource.Panel

	// Port is the MCP server port. Defaults to the MCP_PORT env var or "8090".
	Port string

	// BearerAuth gates /mcp. nil = no auth (localhost-only deployments only).
	BearerAuth *mcpserver.BearerAuth

	// TenantResolver, when non-nil, resolves the tenant for EVERY tool call
	// from the call's ctx — the seam a multi-account consumer uses to pin the
	// tenant from the verified bearer identity (mcpserver.TokenInfoFromContext)
	// instead of the fail-open global default that tenant.From(ctx) returns.
	// A resolver returning (zero Tenant, false) DENIES the call fail-closed.
	//
	// nil preserves the legacy single-tenant behavior: tenant.From(ctx), which
	// is the global default whenever nothing stamped a tenant. The tools also
	// stamp the resolved tenant back onto the ctx handed to Listers/Detailers,
	// so detailers reading tenant.From(req.Context()) see the pinned value.
	TenantResolver func(ctx context.Context) (tenant.Tenant, bool)

	// Logger. nil = slog.Default().
	Logger *slog.Logger

	// Context for lifecycle. nil = signal.NotifyContext(SIGINT, SIGTERM).
	Context context.Context
}

// Run starts the MCP server, exposing all registered Resources as MCP tools.
// Blocks until the context is cancelled or SIGINT/SIGTERM is received.
func Run(cfg Config) error {
	if cfg.Panel == nil {
		panic("panelmcp.Run: Config.Panel is required")
	}
	ctx := cfg.Context
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	mcpCfg := mcpserver.Config{
		Name:                       "go-panel",
		Version:                    moduleVersion(),
		Port:                       cfg.Port,
		KeepAlive:                  30 * time.Second,
		SchemaCache:                mcp.NewSchemaCache(),
		DisableLocalhostProtection: true,
		Context:                    ctx,
		Logger:                     logger,
		MCPLogger:                  logger,
		BearerAuth:                 cfg.BearerAuth,
		JSONResponse:               true,
		SessionTimeout:             10 * time.Minute,
	}
	return mcpserver.Serve(&mcp.Implementation{
		Name:    "go-panel",
		Version: moduleVersion(),
	}, mcpCfg, func(s *mcp.Server) {
		registerResourceTools(s, cfg.Panel.Resources(), logger, cfg.TenantResolver)
	})
}

// modulePath is go-panel's own import path — the key its version is filed
// under in a consumer's build info.
const modulePath = "github.com/anatolykoptev/go-panel"

// moduleVersion reports the go-panel release a binary was built against, read
// out of the build info the toolchain stamps from the go.mod requirement.
//
// It exists because the two version fields below it used to hold the literal
// "0.1.0". Nothing referenced that number, so nothing ever went red when it
// drifted, and by v0.22.2 every MCP client had been told go-panel was at 0.1.0
// for twenty-two releases. release-please owns the version; a second copy
// maintained by hand can only ever be wrong.
//
// Three cases, in the order they occur:
//   - go-panel is a dependency: the version comes from Deps, or from Replace
//     when a consumer points the requirement elsewhere (a replace directive is
//     what actually gets built, so it is what gets reported).
//   - go-panel is the main module — its own tests, or `go run ./...` — where
//     the toolchain has no released version to stamp. "(devel)" is the
//     toolchain's own word for that, not an error.
//   - no build info at all, which a normally-built binary never hits.
func moduleVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "(unknown)"
	}
	for _, d := range bi.Deps {
		if d.Path != modulePath {
			continue
		}
		if d.Replace != nil && d.Replace.Version != "" {
			return d.Replace.Version
		}
		if d.Version != "" {
			return d.Version
		}
	}
	if bi.Main.Path == modulePath && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "(devel)"
}

// registerResourceTools creates MCP list/get tools for each Resource.
func registerResourceTools(server *mcp.Server, resources []resource.Resource, logger *slog.Logger, tr func(context.Context) (tenant.Tenant, bool)) {
	for _, r := range resources {
		registerListTool(server, r, logger, tr)
		// EffectiveDetailer returns the hand-written Detailer OR a synthesized
		// auto-Detailer built from Sort.Columns + FetchRow. This keeps MCP's
		// {resource}_get tool in sync with the HTTP detail route (which is
		// mounted whenever Detailer OR FetchRow is non-nil — see resource.Register).
		if resource.EffectiveDetailer(r) != nil {
			registerGetTool(server, r, logger, tr)
		}
	}
}

// callTenant resolves the tenant for one tool call. A nil resolver keeps the
// legacy behavior — tenant.From(ctx), which fails OPEN to the global default
// (fine for the single-tenant deployments panelmcp was built for). A set
// resolver is authoritative: (tenant, true) pins, (_, false) denies fail-closed.
// On a successful resolve the ctx is stamped (tenant.WithTenant) so both the
// Lister's ListQuery.Tenant and anything reading tenant.From(ctx) downstream —
// the detailer's request context included — observe the SAME pinned tenant.
func callTenant(ctx context.Context, tr func(context.Context) (tenant.Tenant, bool)) (context.Context, tenant.Tenant, bool) {
	if tr == nil {
		return ctx, tenant.From(ctx), true
	}
	t, ok := tr(ctx)
	if !ok {
		return ctx, tenant.Tenant{}, false
	}
	return tenant.WithTenant(ctx, t), t, true
}

// --- list tool ---

type listInput struct {
	Limit   int    `json:"limit,omitempty" jsonschema:"max 500 rows per page; default 50"`
	Offset  int    `json:"offset,omitempty" jsonschema:"zero-based offset"`
	SortKey string `json:"sort_key,omitempty" jsonschema:"field name to sort by"`
	SortDir string `json:"sort_dir,omitempty" jsonschema:"asc or desc"`
}

type listOutput struct {
	Resource string    `json:"resource"`
	Rows     []rowJSON `json:"rows"`
	Total    int       `json:"total"`
	Limit    int       `json:"limit"`
	Offset   int       `json:"offset"`
}

type rowJSON struct {
	ID    string     `json:"id"`
	Cells []cellJSON `json:"cells"`
	Href  string     `json:"href,omitempty"`
}

type cellJSON struct {
	Value string `json:"value"`
	HTML  bool   `json:"html"`
}

func registerListTool(server *mcp.Server, r resource.Resource, logger *slog.Logger, tr func(context.Context) (tenant.Tenant, bool)) {
	toolName := r.Name + "_list"
	tool := &mcp.Tool{
		Name:        toolName,
		Description: fmt.Sprintf("List rows from the %s admin resource. Returns rows with cell values and total count.", r.Title),
	}
	mcp.AddTool(server, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, listOutput, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = defaultListLimit
		}
		if limit > maxListLimit {
			limit = maxListLimit
		}
		offset := in.Offset
		if offset < 0 {
			offset = 0
		}
		sortState := r.Sort.Resolve(in.SortKey, in.SortDir)
		lctx, tenantVal, ok := callTenant(ctx, tr)
		if !ok {
			return nil, listOutput{}, fmt.Errorf("%s: tenant resolution denied", toolName)
		}
		rows, total, err := r.Lister(lctx, resource.ListQuery{
			Sort:       sortState,
			WhereConds: "", // no filter for MCP list (future: expose FilterSpec)
			WhereArgs:  nil,
			Tenant:     tenantVal,
			Limit:      limit,
			Offset:     offset,
		})
		if err != nil {
			return nil, listOutput{}, fmt.Errorf("%s: list failed: %w", toolName, err)
		}
		return nil, listOutput{
			Resource: r.Name,
			Rows:     rowsToJSON(rows),
			Total:    total,
			Limit:    limit,
			Offset:   offset,
		}, nil
	})
	logger.Info("mcp tool registered", "tool", toolName, "resource", r.Name)
}

// --- get tool ---

type getInput struct {
	ID string `json:"id" jsonschema:"required"`
}

type getOutput struct {
	Resource string        `json:"resource"`
	ID       string        `json:"id"`
	Sections []sectionJSON `json:"sections"`
}

type sectionJSON struct {
	Title   string     `json:"title,omitempty"`
	Items   []itemJSON `json:"items,omitempty"`
	RawHTML string     `json:"raw_html,omitempty"`
}

type itemJSON struct {
	Label string `json:"label"`
	Value string `json:"value"`
	HTML  bool   `json:"html"`
}

func registerGetTool(server *mcp.Server, r resource.Resource, logger *slog.Logger, tr func(context.Context) (tenant.Tenant, bool)) {
	toolName := r.Name + "_get"
	tool := &mcp.Tool{
		Name:        toolName,
		Description: fmt.Sprintf("Get a single %s row's detail by ID. Returns detail sections with labeled items.", r.Title),
	}
	mcp.AddTool(server, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in getInput) (*mcp.CallToolResult, getOutput, error) {
		if in.ID == "" {
			return nil, getOutput{}, fmt.Errorf("%s: id is required", toolName)
		}
		dctx, tenantVal, ok := callTenant(ctx, tr)
		if !ok {
			return nil, getOutput{}, fmt.Errorf("%s: tenant resolution denied", toolName)
		}
		req, err := http.NewRequestWithContext(tenant.WithTenant(dctx, tenantVal), http.MethodGet, "/", nil)
		if err != nil {
			return nil, getOutput{}, fmt.Errorf("%s: internal request build failed: %w", toolName, err)
		}
		// EffectiveDetailer handles both hand-written Detailer and the
		// FetchRow-backed auto-Detailer (see resource.EffectiveDetailer). The
		// detailer receives dctx — with the resolved tenant stamped on it when a
		// TenantResolver is configured — so tenant.From works identically whether
		// it reads the ctx or the request.
		detailer := resource.EffectiveDetailer(r)
		sections, err := detailer(dctx, req, in.ID)
		if err != nil {
			return nil, getOutput{}, fmt.Errorf("%s: detail failed: %w", toolName, err)
		}
		return nil, getOutput{
			Resource: r.Name,
			ID:       in.ID,
			Sections: sectionsToJSON(sections),
		}, nil
	})
	logger.Info("mcp tool registered", "tool", toolName, "resource", r.Name)
}

// --- JSON helpers ---

func rowsToJSON(rows []resource.Row) []rowJSON {
	out := make([]rowJSON, 0, len(rows))
	for _, row := range rows {
		cells := make([]cellJSON, 0, len(row.Cells))
		for _, c := range row.Cells {
			cells = append(cells, cellJSON{Value: c.Value, HTML: c.HTML})
		}
		out = append(out, rowJSON{ID: row.ID, Cells: cells, Href: row.Href})
	}
	return out
}

func sectionsToJSON(sections []resource.DetailSection) []sectionJSON {
	out := make([]sectionJSON, 0, len(sections))
	for _, s := range sections {
		items := make([]itemJSON, 0, len(s.Items))
		for _, item := range s.Items {
			items = append(items, itemJSON{Label: item.Label, Value: item.Value, HTML: item.HTML})
		}
		out = append(out, sectionJSON{Title: s.Title, Items: items, RawHTML: s.RawHTML})
	}
	return out
}
