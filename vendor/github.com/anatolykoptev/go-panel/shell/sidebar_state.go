package shell

import "context"

// SidebarCookie is the cookie persisting the operator's collapse choice.
// Server-readable (vs localStorage) so Layout renders the collapsed class at
// SSR time — no flash-of-expanded-sidebar on first load.
// Mirrors shadcn/ui's server-read sidebar_state cookie pattern.
const SidebarCookie = "sb-c"

// SidebarState is per-request chrome state threaded into Layout via context,
// keeping Layout's public signature frozen (73+ internal callers).
// The cookie is parsed in the resource layer (which holds *http.Request);
// shell stays transport-free (no http import).
type SidebarState struct {
	Collapsed bool
}

type sidebarCtxKey struct{}

// ContextWithSidebar stashes SidebarState in ctx for Layout to read.
// Chain after other context decorators (locale.WithLocale, etc.).
func ContextWithSidebar(ctx context.Context, s SidebarState) context.Context {
	return context.WithValue(ctx, sidebarCtxKey{}, s)
}

// SidebarFromContext returns the threaded SidebarState.
// Returns a zero value (Collapsed=false) when no state was stashed — safe default.
func SidebarFromContext(ctx context.Context) SidebarState {
	if s, ok := ctx.Value(sidebarCtxKey{}).(SidebarState); ok {
		return s
	}
	return SidebarState{}
}
