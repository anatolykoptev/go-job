package adminui

// password.go — self-serve password change (bcrypt driver only, behind
// d.selfServe). Mounted via MountPage/MountAction like the keys surface:
// auth guard + CSRF verify + form parse come from the framework.
//
// Step-up: changing the password is credential mutation — the same
// sensitivity class as mint/TOTP enrollment — so the change action
// re-verifies the CURRENT password, behind a per-account limiter
// (pw:<accountID>, 5/min) checked BEFORE the bcrypt verify: a stolen
// session must not become an unthrottled password oracle.
//
// On success the existing session stays valid — sessions bind to account
// id+role, not the password hash, so the user is not kicked out of the
// cabinet they just updated the credential from.

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
)

const (
	// pwRateLimit bounds the step-up password verify per account — checked
	// BEFORE bcrypt so a stolen session cannot grind passwords.
	pwRateLimit  = 5
	pwRateWindow = time.Minute
)

// passwordPage serves GET /admin/password/: the change form.
func passwordPage(p *resource.Panel, csrfKey []byte, cookieName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		shell.SecurityHeaders(w)
		renderPasswordPage(w, r, p, csrf.Issue(csrfKey, sessionValue(r, cookieName), csrf.DefaultTTL), "", "")
	}
}

// passwordChange serves POST /admin/password/change. MountAction already
// enforced auth + CSRF + form parse. Order: identity → input validation
// (cheap) → per-account limiter → password step-up → hash → update.
func passwordChange(p *resource.Panel, acctStore *auth.PgxAccountStore, acctOf accountResolver, csrfKey []byte, cookieName string, limiter *accounts.LoginLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		shell.SecurityHeaders(w)
		ctx := r.Context()
		aid, ok := acctOf(ctx)
		if !ok {
			renderKeysUnavailable(w, r, p)
			return
		}
		fail := func(msg string) {
			renderPasswordPage(w, r, p, csrf.Issue(csrfKey, sessionValue(r, cookieName), csrf.DefaultTTL), msg, "")
		}

		newPw := r.FormValue("new_password")
		if len(newPw) < regMinPasswordLen || len(newPw) > regMaxPasswordLen {
			fail("New password must be 10–72 characters.")
			return
		}
		if newPw != r.FormValue("confirm_password") {
			fail("Passwords do not match.")
			return
		}

		// Limiter BEFORE the bcrypt verify — same convention as mint.
		allowed, err := limiter.Allow(ctx, "pw:"+aid.String(), pwRateLimit, pwRateWindow)
		if err != nil {
			slog.Error("adminui: password limiter error — denying (fail-closed)", "err", err)
			allowed = false
		}
		if !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(pwRateWindow.Seconds()))))
			http.Error(w, "Too many attempts. Please try again later.", http.StatusTooManyRequests)
			return
		}

		acct, err := acctStore.GetByID(ctx, aid.String())
		if err != nil {
			slog.Error("adminui: password account lookup", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !auth.VerifyAccountPassword(ctx, acctStore, acct.Email, r.FormValue("current_password")) {
			fail("Incorrect current password.")
			return
		}

		hash, err := auth.HashPassword(newPw)
		if err != nil {
			slog.Error("adminui: password hash", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if err := acctStore.UpdatePasswordHash(ctx, aid.String(), hash); err != nil {
			slog.Error("adminui: password update", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		renderPasswordPage(w, r, p, csrf.Issue(csrfKey, sessionValue(r, cookieName), csrf.DefaultTTL), "", "Password updated.")
	}
}

type passwordPageData struct {
	BasePath string
	ErrMsg   string
	OKMsg    string
	CSRF     string
}

const securityPageTmplSrc = `<style>
  .kj-section{background:var(--bg-surface,#1e293b);border:1px solid var(--border,#334155);border-radius:var(--radius-lg,.75rem);padding:1.25rem 1.5rem;margin-bottom:1.25rem}
  .kj-muted{color:var(--text-secondary,#94a3b8);font-size:.875rem;line-height:1.55}
  .kj-error{margin-bottom:1rem;padding:.5rem .75rem;border-radius:.375rem;background:rgba(239,68,68,.12);border:1px solid rgba(239,68,68,.35);color:#fca5a5;font-size:.8125rem}
  .kj-ok{margin-bottom:1rem;padding:.5rem .75rem;border-radius:.375rem;background:rgba(34,197,94,.12);border:1px solid rgba(34,197,94,.35);color:#86efac;font-size:.8125rem}
  .kj-field{margin-bottom:.75rem}
  .kj-field label{display:block;font-size:.8rem;color:var(--text-secondary,#94a3b8);margin-bottom:.25rem}
  .kj-input{width:100%;background:var(--bg-deep,#0f172a);border:1px solid var(--border,#334155);border-radius:.375rem;padding:.4rem .6rem;color:var(--text-primary,#f1f5f9);font-size:.875rem}
  .kj-btn{padding:.4rem .9rem;border-radius:.375rem;font-size:.8125rem;cursor:pointer;border:none;background:var(--accent,#3b82f6);color:#0f172a;font-weight:600}
{{template "sharedCSS" .}}
</style>

<div class="page-header">
  <h2>&#x1F512; Password</h2>
  <p class="kj-muted">Changing your password does not sign you out of this session — it only changes the credential used at next login.</p>
</div>

{{if .ErrMsg}}<div class="kj-error" role="alert">{{.ErrMsg}}</div>{{end}}
{{if .OKMsg}}<div class="kj-ok" role="status">{{.OKMsg}}</div>{{end}}

<div class="kj-section">
  <h3>Change password</h3>
  <form method="POST" action="{{.BasePath}}/password/change">
    <input type="hidden" name="_csrf" value="{{.CSRF}}"/>
    <div class="kj-field">
      <label for="current_password">Current password</label>
      <input id="current_password" name="current_password" type="password" autocomplete="current-password" required class="kj-input"/>
    </div>
    <div class="kj-field">
      <label for="new_password">New password (10–72 characters)</label>
      <input id="new_password" name="new_password" type="password" autocomplete="new-password" minlength="10" maxlength="72" required class="kj-input"/>
    </div>
    <div class="kj-field">
      <label for="confirm_password">Confirm new password</label>
      <input id="confirm_password" name="confirm_password" type="password" autocomplete="new-password" required class="kj-input"/>
    </div>
    <button type="submit" class="kj-btn">Update password</button>
  </form>
</div>
`

var pwChangePageTmpl = template.Must(template.Must(template.New("password").Funcs(adminuiFuncMap).Parse(sharedPartialsSrc)).Parse(securityPageTmplSrc))

func renderPasswordPage(w http.ResponseWriter, r *http.Request, p *resource.Panel, csrfToken, errMsg, okMsg string) {
	var sb strings.Builder
	if err := pwChangePageTmpl.Execute(&sb, passwordPageData{
		BasePath: adminBasePath,
		ErrMsg:   errMsg,
		OKMsg:    okMsg,
		CSRF:     csrfToken,
	}); err != nil {
		slog.Error("adminui: render password page", "err", err)
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	if err := p.RenderPageHTML(w, r, "Password", "password", sb.String()); err != nil {
		slog.Error("adminui: render password page", "err", err)
	}
}
