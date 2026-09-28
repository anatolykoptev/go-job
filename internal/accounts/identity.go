// identity.go — the ONE fail-closed account-identity seam (plan ADR-1).
// Every consumer that needs "which account is acting" calls AccountFrom;
// nothing reads auth.SessionFrom / sdkauth.TokenInfoFromContext directly for
// identity, and go-panel's tenant accessor is BANNED as an identity source
// (its global 'spb' default is fail-open — the Makefile grep-gate enforces
// the ban in internal/accounts, internal/adminui, and main.go).
package accounts

import (
	"context"

	panelauth "github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go-panel/tenant"
	"github.com/google/uuid"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
)

// AccountFrom resolves the acting account UUID from ctx:
//
//   - web path — the session BcryptTOTPAuth.Require stamped
//     (auth.SessionFrom); Session.UserID is the panel_accounts UUID;
//   - MCP path — the bearer TokenInfo RequireBearerToken stamped
//     (sdkauth.TokenInfoFromContext); TokenInfo.UserID is the same UUID,
//     minted by KeyStore.Verifier from the key's account_id.
//
// Fail-closed on every axis: missing identity, empty or malformed UserID, and
// uuid.Nil all deny with (uuid.Nil, false). A present-but-invalid session
// UserID denies outright — it never falls through to the bearer leg; a ctx
// carrying a broken identity is not allowed to shop for a second one.
func AccountFrom(ctx context.Context) (uuid.UUID, bool) {
	sess, _ := panelauth.SessionFrom(ctx)
	return accountFromIdentity(sess, sdkauth.TokenInfoFromContext(ctx))
}

// accountFromIdentity is the pure seam behind AccountFrom — split out so the
// deny matrix (malformed UserIDs on BOTH legs) is unit-testable; the ctx
// keys that stamp *Session/*TokenInfo are unexported in both SDKs.
func accountFromIdentity(sess *panelauth.Session, ti *sdkauth.TokenInfo) (uuid.UUID, bool) {
	if sess != nil {
		return parseAccountID(sess.UserID)
	}
	if ti != nil {
		return parseAccountID(ti.UserID)
	}
	return uuid.Nil, false
}

func parseAccountID(raw string) (uuid.UUID, bool) {
	if raw == "" {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, false
	}
	return id, true
}

// MCPTenantResolver is panelmcp's Config.TenantResolver (ADR-16): it pins the
// per-call tenant to the verified bearer's account UUID via AccountFrom —
// TokenInfo.UserID → Tenant{CitySlug}. It is wired UNCONDITIONALLY, including
// when bearerAuth is nil: with no verified identity in ctx it returns
// (zero Tenant, false), and upstream callTenant denies the call — the
// no-DB/no-auth shape stays fail-closed rather than falling back to the
// global 'spb' tenant. The deny contract is pinned upstream over the real
// transport by go-panel's TestListTool_TenantResolverDenies.
func MCPTenantResolver(ctx context.Context) (tenant.Tenant, bool) {
	id, ok := AccountFrom(ctx)
	if !ok {
		return tenant.Tenant{}, false
	}
	return tenant.Tenant{CitySlug: id.String()}, true
}
