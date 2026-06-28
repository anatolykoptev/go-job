package shell

import "context"

// GroupsCookie is the cookie persisting which nav groups are collapsed.
// Value is URL-encoded (encodeURIComponent in JS), comma-separated group names.
// Only collapsed group names are stored (non-default); absent = no groups collapsed.
// Server-readable so Layout emits data-collapsed=true at SSR time — no flash.
// Co-located with SidebarCookie because both are chrome-state cookies read by
// chromeStateFrom; chrome_state.go is the canonical home for all cookie names the
// chrome layer owns.
const GroupsCookie = "sb-g"

// ChromeState carries per-request shell (chrome) configuration threaded into
// Layout via context.  It is the single context seam for all per-request
// state the layout renders, replacing the narrower SidebarState.
//
// CollapsedGroups and Profile are zero-valued in Phase 2 and will be
// populated by Phases 4 and 7 respectively.
//
// shell imports neither net/http nor auth; cookie parsing lives in the
// resource layer (resource.chromeStateFrom).
type ChromeState struct {
	Collapsed       bool
	CollapsedGroups map[string]bool
	Profile         ProfileConfig
}

// ProfileConfig carries the per-operator identity shown in the sticky-bottom
// profile block (avatar initial, name, role, settings/logout links).
//
// Zero value is safe: when Name is empty, Layout renders only the bare Logout
// link (backward-compatible with single-user HMACAuth consumers that have no
// named session).
//
// Consumers call Panel.SetProfile to supply static defaults (SettingsURL,
// LogoutURL); the resource layer overlays Name/Role per-request from the live
// session so a multi-user deployment always shows the current operator's
// identity without per-request SetProfile calls.
type ProfileConfig struct {
	// Name is the operator's display name shown in the profile block.
	// Populated per-request from Session.UserID for BcryptTOTPAuth consumers.
	// Empty → bare Logout rendered (zero-value safe, backward-compat).
	Name string

	// Role is the role label shown muted below the name (e.g. "admin", "owner").
	// Populated per-request from Session.Role.
	Role string

	// SettingsURL is the optional settings page URL. When non-empty, a
	// Settings link is rendered above Logout in the profile block.
	SettingsURL string

	// LogoutURL is the logout endpoint. When empty, defaults to /admin/logout.
	LogoutURL string
}

type chromeCtxKey struct{}

// ContextWithChrome stashes ChromeState in ctx for Layout to read.
// Chain after other context decorators (locale.WithLocale, etc.).
func ContextWithChrome(ctx context.Context, s ChromeState) context.Context {
	return context.WithValue(ctx, chromeCtxKey{}, s)
}

// ChromeFromContext returns the threaded ChromeState.
// Returns a zero value (Collapsed=false) when no state was stashed — safe default.
func ChromeFromContext(ctx context.Context) ChromeState {
	if s, ok := ctx.Value(chromeCtxKey{}).(ChromeState); ok {
		return s
	}
	return ChromeState{}
}
