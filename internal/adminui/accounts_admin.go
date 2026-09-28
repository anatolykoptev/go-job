package adminui

// accounts_admin.go — the P6 operator activation surface (bcrypt driver only,
// behind d.selfServe): GET /admin/accounts lists every panel_accounts row and
// POST accounts/{id}/activate|deactivate flips `active`.
//
// Role gate — IN-HANDLER, never RequiredRole: a RequiredRole literal is
// doubly illegal here — the Makefile:47 grep-gate fails it AND it panics
// under AUTH_DRIVER=hmac (HMACAuth is not a RoleAuthenticator,
// resource.go:982). Every handler opens with
//
//	sess, ok := auth.SessionFrom(r.Context())
//	if !ok || sess.Role != "admin" { 403; return }
//
// This is the repo's first cross-account surface — justified because account
// administration IS the operator role's job (vs ADR-14's data-plane
// isolation); a 'user' sees only a 403 wall. The sidebar nav item hides
// cosmetically via NavItem.Visible; enforcement lives here.
//
// Self-lockout: deactivate refuses when the caller targets its own account
// (400). Last-admin wipeout via deactivate-other stays reachable — the CLI
// (gojob-admin account activate) is the recovery path; acceptable at this
// fleet size, noted in the spec rather than gated.

import (
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strings"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go-panel/csrf"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go-panel/shell"
	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// sessionIsAdmin is the single role-gate predicate every handler here opens
// with — the sanctioned replacement for the banned RequiredRole.
func sessionIsAdmin(r *http.Request) bool {
	sess, ok := auth.SessionFrom(r.Context())
	return ok && sess.Role == "admin"
}

// accountsPage serves GET /admin/accounts/ — the operator's account table.
// ListAccountRows (not acctStore.ListAccounts) because the page shows
// created_at, which the framework projection omits.
func accountsPage(p *resource.Panel, pool *pgxpool.Pool, csrfKey []byte, cookieName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		shell.SecurityHeaders(w)
		if !sessionIsAdmin(r) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		rows, err := accounts.ListAccountRows(r.Context(), pool)
		if err != nil {
			slog.Error("adminui: accounts list", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		data := accountsPageData{
			BasePath: adminBasePath,
			CSRF:     csrf.Issue(csrfKey, sessionValue(r, cookieName), csrf.DefaultTTL),
		}
		for _, a := range rows {
			data.Rows = append(data.Rows, accountRowView{
				ID:        a.ID.String(),
				Email:     a.Email,
				Name:      a.Name,
				Role:      a.Role,
				Active:    a.Active,
				CreatedAt: a.CreatedAt.Format("2006-01-02 15:04"),
			})
		}
		var sb strings.Builder
		if err := accountsPageTmpl.Execute(&sb, data); err != nil {
			slog.Error("adminui: render accounts page", "err", err)
			http.Error(w, "render error", http.StatusInternalServerError)
			return
		}
		if err := p.RenderPageHTML(w, r, "Accounts", "accounts", sb.String()); err != nil {
			slog.Error("adminui: render accounts page", "err", err)
		}
	}
}

// accountSetActive returns the MountAction handler for
// accounts/{id}/activate (activate=true) and .../deactivate (false).
func accountSetActive(acctStore *auth.PgxAccountStore, activate bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sessionIsAdmin(r) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		id := r.PathValue("id")
		keyID, err := uuid.Parse(id)
		if err != nil {
			http.Error(w, "bad account id", http.StatusBadRequest)
			return
		}
		// Self-lockout guard: an admin must not deactivate its own account
		// through the UI (CLI is the intentional recovery/edge path).
		sess, _ := auth.SessionFrom(r.Context())
		if !activate && sess.UserID == keyID.String() {
			http.Error(w, "cannot deactivate your own account", http.StatusBadRequest)
			return
		}
		if err := acctStore.SetActive(r.Context(), keyID.String(), activate); err != nil {
			if errors.Is(err, auth.ErrAccountNotFound) {
				http.Error(w, "account not found", http.StatusNotFound)
				return
			}
			slog.Error("adminui: set active", "id", id, "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, adminBasePath+"/accounts/", http.StatusSeeOther)
	}
}

type accountRowView struct {
	ID        string
	Email     string
	Name      string
	Role      string
	Active    bool
	CreatedAt string
}

type accountsPageData struct {
	BasePath string
	CSRF     string
	Rows     []accountRowView
}

const accountsPageTmplSrc = `<style>
  .ka-section{background:var(--bg-surface,#1e293b);border:1px solid var(--border,#334155);border-radius:var(--radius-lg,.75rem);padding:1.25rem 1.5rem;margin-bottom:1.25rem}
  .ka-table{width:100%;border-collapse:collapse;font-size:.875rem}
  .ka-table th{text-align:left;padding:.4rem .6rem;color:var(--text-muted,#64748b);font-weight:600;font-size:.8rem;text-transform:uppercase;letter-spacing:.05em;border-bottom:1px solid var(--border,#334155)}
  .ka-table td{padding:.5rem .6rem;color:var(--text-secondary,#94a3b8);border-bottom:1px solid var(--border-subtle,#1e293b)}
  .ka-pill{display:inline-block;border-radius:9999px;padding:.1rem .6rem;font-size:.75rem;border:1px solid var(--border,#334155)}
  .ka-on{color:#34d399;border-color:rgba(52,211,153,.4)}
  .ka-off{color:#fbbf24;border-color:rgba(245,158,11,.4)}
  .ka-btn{padding:.25rem .6rem;border-radius:.375rem;font-size:.75rem;cursor:pointer;border:1px solid var(--border,#334155);background:transparent;color:var(--text-secondary,#94a3b8)}
  .ka-btn:hover{color:var(--text-primary,#f1f5f9)}
  .ka-empty{color:var(--text-muted,#64748b);font-style:italic;font-size:.875rem}
</style>

<div class="page-header">
  <h2>&#x1F465; Accounts</h2>
  <p style="color:var(--text-secondary,#94a3b8);font-size:.875rem">Pending registrations sign in only after activation. Deactivating an account kills its sessions and bearer keys on the next request.</p>
</div>

<div class="ka-section">
{{if .Rows}}
<table class="ka-table">
  <thead><tr><th>Email</th><th>Name</th><th>Role</th><th>Status</th><th>Created</th><th></th></tr></thead>
  <tbody>
  {{range .Rows}}
    <tr>
      <td>{{.Email}}</td>
      <td>{{.Name}}</td>
      <td>{{.Role}}</td>
      <td>{{if .Active}}<span class="ka-pill ka-on">active</span>{{else}}<span class="ka-pill ka-off">pending/inactive</span>{{end}}</td>
      <td>{{.CreatedAt}}</td>
      <td>
        {{if .Active}}
        <form method="POST" action="{{$.BasePath}}/accounts/{{.ID}}/deactivate" style="display:inline"><input type="hidden" name="_csrf" value="{{$.CSRF}}"/><button type="submit" class="ka-btn">Deactivate</button></form>
        {{else}}
        <form method="POST" action="{{$.BasePath}}/accounts/{{.ID}}/activate" style="display:inline"><input type="hidden" name="_csrf" value="{{$.CSRF}}"/><button type="submit" class="ka-btn">Activate</button></form>
        {{end}}
      </td>
    </tr>
  {{end}}
  </tbody>
</table>
{{else}}<div class="ka-empty">No accounts.</div>{{end}}
</div>
`

var accountsPageTmpl = template.Must(template.New("accounts").Parse(accountsPageTmplSrc))
