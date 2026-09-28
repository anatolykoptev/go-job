package adminui

// keys.go — the P6 self-serve /admin/keys surface (bcrypt driver only, behind
// d.selfServe): an account lists its own MCP bearer keys, mints new ones
// under a password step-up, and revokes its own — never another account's.
//
// Identity: every handler resolves the acting account via acctOf
// (accounts.AccountFrom under bcrypt). A miss renders the
// identity-unavailable page — uuid.Nil is NEVER passed to ListKeys (Nil is
// the all-accounts sentinel in admin.go).
//
// Step-up: minting is credential issuance — the same sensitivity class as
// TOTP enrollment — so mint re-verifies the account's CURRENT password
// (auth.VerifyAccountPassword via acctStore.GetByID → email), and a
// per-account LoginLimiter (mint:<accountID>, 5/min) runs BEFORE the bcrypt
// verify: a stolen session must not become an unthrottled password oracle
// (spec gate MEDIUM-1). Revoke needs no step-up — revoking your own key is
// self-limiting.
//
// MountPage/MountAction give the auth guard + CSRF verify + form parsing for
// free; the GET handlers set shell.SecurityHeaders themselves (MountPage does
// not). Role plays no part here — keys are the caller's own regardless of
// role, so no role gate applies.

import (
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go-panel/csrf"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go-panel/shell"
	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/google/uuid"
)

const (
	// mintRateLimit bounds the step-up password verify per account — checked
	// BEFORE bcrypt so a stolen session cannot grind passwords.
	mintRateLimit  = 5
	mintRateWindow = time.Minute

	maxKeyLabelLen = 80
)

// mcpEndpoint is the public MCP endpoint shown in the onboarding block —
// overridable via MCP_PUBLIC_URL for staging/deploys on another host.
func mcpEndpoint() string {
	return envOr("MCP_PUBLIC_URL", "https://mcp.krolik.run/job/mcp")
}

// keysPage serves GET /admin/keys/: the caller's own keys, the mint form,
// and the onboarding block.
func keysPage(p *resource.Panel, ks *accounts.KeyStore, acctOf accountResolver, csrfKey []byte, cookieName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		shell.SecurityHeaders(w)
		aid, ok := acctOf(r.Context())
		if !ok {
			renderKeysUnavailable(w, r, p)
			return
		}
		keys, err := ks.ListKeys(r.Context(), aid)
		if err != nil {
			slog.Error("adminui: keys list", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		renderKeysPage(w, r, p, keys, csrf.Issue(csrfKey, sessionValue(r, cookieName), csrf.DefaultTTL), "")
	}
}

// keysMint serves POST /admin/keys/mint. MountAction already enforced auth +
// CSRF + form parse. Order: identity → input validation → per-account
// limiter → password step-up → Mint → token page rendered IN PLACE (a PRG
// redirect would lose the one-shown plaintext).
func keysMint(p *resource.Panel, acctStore *auth.PgxAccountStore, ks *accounts.KeyStore, acctOf accountResolver, csrfKey []byte, cookieName string, limiter *accounts.LoginLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		shell.SecurityHeaders(w)
		ctx := r.Context()
		aid, ok := acctOf(ctx)
		if !ok {
			renderKeysUnavailable(w, r, p)
			return
		}
		label := strings.TrimSpace(r.FormValue("label"))
		if label == "" || len(label) > maxKeyLabelLen {
			rerenderKeysError(w, r, p, aid, ks, csrfKey, cookieName, "Label is required (max 80 characters).")
			return
		}
		// MEDIUM-1: the limiter runs BEFORE the bcrypt verify — a stolen
		// session must not become an unthrottled password oracle. Fail-closed
		// on limiter error, same convention as login.
		allowed, err := limiter.Allow(ctx, "mint:"+aid.String(), mintRateLimit, mintRateWindow)
		if err != nil {
			slog.Error("adminui: mint limiter error — denying (fail-closed)", "err", err)
			allowed = false
		}
		if !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(mintRateWindow.Seconds()))))
			http.Error(w, "Too many attempts. Please try again later.", http.StatusTooManyRequests)
			return
		}
		acct, err := acctStore.GetByID(ctx, aid.String())
		if err != nil {
			slog.Error("adminui: mint account lookup", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !auth.VerifyAccountPassword(ctx, acctStore, acct.Email, r.FormValue("current_password")) {
			rerenderKeysError(w, r, p, aid, ks, csrfKey, cookieName, "Incorrect password.")
			return
		}
		token, err := ks.Mint(ctx, aid, label)
		if err != nil {
			slog.Error("adminui: mint key", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		renderMintedKey(w, r, p, token)
	}
}

// keysRevoke serves POST /admin/keys/{id}/revoke. RevokeForAccount carries
// the account predicate: a foreign-owned or nonexistent id collapses to the
// same "key not found or already revoked" — no ownership oracle.
func keysRevoke(p *resource.Panel, ks *accounts.KeyStore, acctOf accountResolver, csrfKey []byte, cookieName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		shell.SecurityHeaders(w)
		ctx := r.Context()
		aid, ok := acctOf(ctx)
		if !ok {
			renderKeysUnavailable(w, r, p)
			return
		}
		keyID, err := uuid.Parse(r.PathValue("id"))
		if err == nil {
			err = ks.RevokeForAccount(ctx, aid, keyID)
		}
		if err != nil {
			rerenderKeysError(w, r, p, aid, ks, csrfKey, cookieName, "Key not found or already revoked.")
			return
		}
		http.Redirect(w, r, adminBasePath+"/keys/", http.StatusSeeOther)
	}
}

// renderKeysUnavailable is the hmac-pin/no-identity shape — the same answer
// resumeEmptyHTML gives other account-less surfaces.
func renderKeysUnavailable(w http.ResponseWriter, r *http.Request, p *resource.Panel) {
	if err := p.RenderPageHTML(w, r, "MCP Keys", "keys",
		resumeEmptyHTML("Account identity is unavailable on this session — keys cannot be managed.")); err != nil {
		slog.Error("adminui: render keys unavailable", "err", err)
	}
}

// rerenderKeysError re-renders the keys page with a message line — used for
// step-up failure, label validation, and revoke misses.
func rerenderKeysError(w http.ResponseWriter, r *http.Request, p *resource.Panel, aid uuid.UUID, ks *accounts.KeyStore, csrfKey []byte, cookieName, msg string) {
	keys, err := ks.ListKeys(r.Context(), aid)
	if err != nil {
		slog.Error("adminui: keys re-list", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	renderKeysPage(w, r, p, keys, csrf.Issue(csrfKey, sessionValue(r, cookieName), csrf.DefaultTTL), msg)
}

// renderKeysPage emits the list + mint form. Every form's _csrf token is
// bound to the live session cookie — MountAction verifies against the same
// value (the star.go/job_detail.go bespoke-handler pattern).
func renderKeysPage(w http.ResponseWriter, r *http.Request, p *resource.Panel, keys []accounts.KeyInfo, csrfTok, errMsg string) {
	data := keysPageData{
		BasePath: adminBasePath,
		ErrMsg:   errMsg,
		CSRF:     csrfTok,
		Endpoint: mcpEndpoint(),
		Keys:     make([]keyRow, 0, len(keys)),
	}
	for _, k := range keys {
		data.Keys = append(data.Keys, keyRow{
			ID:         k.ID.String(),
			Prefix:     k.Prefix,
			Label:      k.Label,
			CreatedAt:  k.CreatedAt.Format("2006-01-02 15:04"),
			LastUsedAt: derefTime(k.LastUsedAt),
			RevokedAt:  derefTime(k.RevokedAt),
			Active:     k.RevokedAt == nil,
		})
	}
	var sb strings.Builder
	if err := keysPageTmpl.Execute(&sb, data); err != nil {
		slog.Error("adminui: render keys page", "err", err)
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	if err := p.RenderPageHTML(w, r, "MCP Keys", "keys", sb.String()); err != nil {
		slog.Error("adminui: render keys page", "err", err)
	}
}

// renderMintedKey renders the one-shot token page IN PLACE (200, no
// redirect): the plaintext exists only in this response.
func renderMintedKey(w http.ResponseWriter, r *http.Request, p *resource.Panel, token string) {
	var sb strings.Builder
	if err := mintedKeyTmpl.Execute(&sb, mintedKeyData{Token: token, Endpoint: mcpEndpoint(), BasePath: adminBasePath}); err != nil {
		slog.Error("adminui: render minted key", "err", err)
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	if err := p.RenderPageHTML(w, r, "MCP Keys", "keys", sb.String()); err != nil {
		slog.Error("adminui: render minted key", "err", err)
	}
}

func derefTime(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return t.Format("2006-01-02 15:04")
}

type keyRow struct {
	ID         string
	Prefix     string
	Label      string
	CreatedAt  string
	LastUsedAt string
	RevokedAt  string
	Active     bool
}

type keysPageData struct {
	BasePath string
	ErrMsg   string
	CSRF     string
	Endpoint string
	Keys     []keyRow
}

type mintedKeyData struct {
	Token    string
	Endpoint string
	BasePath string
}

// kj-* classes; .li-pre/.li-code-wrap come from sharedCSS (partials.go) —
// the single-source rule the Makefile fitness gate enforces.
const keysPageTmplSrc = `<style>
  .kj-section{background:var(--bg-surface,#1e293b);border:1px solid var(--border,#334155);border-radius:var(--radius-lg,.75rem);padding:1.25rem 1.5rem;margin-bottom:1.25rem}
  .kj-section h3{font-size:.9375rem;font-weight:700;color:var(--text-primary,#f1f5f9);margin:0 0 .5rem}
  .kj-muted{color:var(--text-secondary,#94a3b8);font-size:.875rem;line-height:1.55}
  .kj-error{margin-bottom:1rem;padding:.5rem .75rem;border-radius:.375rem;background:rgba(239,68,68,.12);border:1px solid rgba(239,68,68,.35);color:#fca5a5;font-size:.8125rem}
  .kj-warn{margin-bottom:1rem;padding:.5rem .75rem;border-radius:.375rem;background:rgba(245,158,11,.12);border:1px solid rgba(245,158,11,.35);color:#fbbf24;font-size:.8125rem}
  .kj-table{width:100%;border-collapse:collapse;font-size:.875rem}
  .kj-table th{text-align:left;padding:.4rem .6rem;color:var(--text-muted,#64748b);font-weight:600;font-size:.8rem;text-transform:uppercase;letter-spacing:.05em;border-bottom:1px solid var(--border,#334155)}
  .kj-table td{padding:.5rem .6rem;color:var(--text-secondary,#94a3b8);border-bottom:1px solid var(--border-subtle,#1e293b)}
  .kj-field{margin-bottom:.75rem}
  .kj-field label{display:block;font-size:.8rem;color:var(--text-secondary,#94a3b8);margin-bottom:.25rem}
  .kj-input{width:100%;background:var(--bg-deep,#0f172a);border:1px solid var(--border,#334155);border-radius:.375rem;padding:.4rem .6rem;color:var(--text-primary,#f1f5f9);font-size:.875rem}
  .kj-btn{padding:.4rem .9rem;border-radius:.375rem;font-size:.8125rem;cursor:pointer;border:none;background:var(--accent,#3b82f6);color:#0f172a;font-weight:600}
  .kj-btn-sm{padding:.25rem .6rem;border-radius:.375rem;font-size:.75rem;cursor:pointer;border:1px solid var(--border,#334155);background:transparent;color:var(--text-secondary,#94a3b8)}
  .kj-btn-sm:hover{color:#fca5a5;border-color:rgba(239,68,68,.35)}
  .kj-empty{color:var(--text-muted,#64748b);font-style:italic;font-size:.875rem}
{{template "sharedCSS" .}}
</style>

<div class="page-header">
  <h2>&#x1F511; MCP Keys</h2>
  <p class="kj-muted">Keys authenticate MCP clients as your account. Only the key prefix is stored or shown — a raw token is visible once, at mint time.</p>
</div>

{{if .ErrMsg}}<div class="kj-error" role="alert">{{.ErrMsg}}</div>{{end}}

<div class="kj-section">
  <h3>Your keys</h3>
  {{if .Keys}}
  <table class="kj-table">
    <thead><tr><th>Prefix</th><th>Label</th><th>Created</th><th>Last used</th><th>Revoked</th><th></th></tr></thead>
    <tbody>
    {{range .Keys}}
      <tr>
        <td><code>{{.Prefix}}</code></td>
        <td>{{.Label}}</td>
        <td>{{.CreatedAt}}</td>
        <td>{{.LastUsedAt}}</td>
        <td>{{.RevokedAt}}</td>
        <td>{{if .Active}}<form method="POST" action="{{$.BasePath}}/keys/{{.ID}}/revoke" style="display:inline"><input type="hidden" name="_csrf" value="{{$.CSRF}}"/><button type="submit" class="kj-btn-sm">Revoke</button></form>{{end}}</td>
      </tr>
    {{end}}
    </tbody>
  </table>
  {{else}}<div class="kj-empty">No keys yet — mint one below.</div>{{end}}
</div>

<div class="kj-section">
  <h3>Mint a new key</h3>
  <form method="POST" action="{{.BasePath}}/keys/mint">
    <input type="hidden" name="_csrf" value="{{.CSRF}}"/>
    <div class="kj-field">
      <label for="label">Label</label>
      <input id="label" name="label" type="text" maxlength="80" required class="kj-input" placeholder="e.g. laptop claude-code"/>
    </div>
    <div class="kj-field">
      <label for="current_password">Current password (required to mint)</label>
      <input id="current_password" name="current_password" type="password" autocomplete="current-password" required class="kj-input"/>
    </div>
    <button type="submit" class="kj-btn">Mint key</button>
  </form>
</div>

<div class="kj-section">
  <h3>Connect a client</h3>
  <p class="kj-muted">Point your MCP client at the endpoint and send the token as a bearer credential:</p>
  <pre class="li-pre">POST {{.Endpoint}}
Authorization: Bearer &lt;token&gt;</pre>
</div>
`

const mintedKeyTmplSrc = `<style>
  .kj-section{background:var(--bg-surface,#1e293b);border:1px solid var(--border,#334155);border-radius:var(--radius-lg,.75rem);padding:1.25rem 1.5rem;margin-bottom:1.25rem}
  .kj-warn{margin-bottom:1rem;padding:.5rem .75rem;border-radius:.375rem;background:rgba(245,158,11,.12);border:1px solid rgba(245,158,11,.35);color:#fbbf24;font-size:.8125rem}
  .kj-muted{color:var(--text-secondary,#94a3b8);font-size:.875rem}
{{template "sharedCSS" .}}
</style>

<div class="page-header"><h2>&#x1F511; Key minted</h2></div>
<div class="kj-warn" role="alert">This token is shown once — store it now. It cannot be recovered.</div>
<div class="kj-section">
  <pre class="li-pre">{{.Token}}</pre>
  <h3 style="font-size:.9375rem;color:var(--text-primary,#f1f5f9)">Connect a client</h3>
  <pre class="li-pre">POST {{.Endpoint}}
Authorization: Bearer {{.Token}}</pre>
  <p><a href="{{.BasePath}}/keys/" style="color:var(--accent,#60a5fa)">Back to keys</a></p>
</div>
`

// sharedPartialsSrc's copyBlock define references the shared funcmap —
// Funcs must be registered before Parse (the linkedin.go pattern).
var keysPageTmpl = template.Must(template.Must(template.New("keys").Funcs(adminuiFuncMap).Parse(sharedPartialsSrc)).Parse(keysPageTmplSrc))
var mintedKeyTmpl = template.Must(template.Must(template.New("minted").Funcs(adminuiFuncMap).Parse(sharedPartialsSrc)).Parse(mintedKeyTmplSrc))
