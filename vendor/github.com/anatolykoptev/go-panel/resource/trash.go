package resource

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/anatolykoptev/go-panel/csrf"
	"github.com/anatolykoptev/go-panel/shell"
	"github.com/anatolykoptev/go-panel/tenant"
)

// trashNavID is the nav item's ID and the URL segment the page mounts on.
const trashNavID = "trash"

// trashNavGroup is the sidebar heading the Trash link sits under. It is its own
// group because the page belongs to the panel, not to any one resource's
// subject area.
const trashNavGroup = "System"

// trashPageSize caps how many of one resource's deleted rows a section renders.
// Nothing is ever purged, so a resource's trash only grows; the cap bounds the
// page and the section prints the true total beside what it drew.
const trashPageSize = 50

// hasTrash reports whether any registered resource opted into the trash.
func (p *Panel) hasTrash() bool {
	for _, r := range p.resources {
		if r.TrashLister != nil {
			return true
		}
	}
	return false
}

// mountTrash mounts the panel-wide Trash page and its nav item — but only when
// at least one resource opted in, so a panel that never soft-deletes gains
// neither a route nor a sidebar entry and is bit-identical to before.
//
// Called from finalize rather than from Register: the page fans out over EVERY
// registered resource, and Register cannot know which call is the last one.
func (p *Panel) mountTrash() {
	if !p.hasTrash() {
		return
	}
	// Both a resource and a MountPage'd page can already own this suffix, and
	// neither collision is loud on its own: net/http rejects only a
	// byte-identical pattern, so MountPage's "trash/{$}" and this bare "/trash"
	// coexist happily while the consumer's page quietly stops answering the URL
	// they published. Fail at startup, where it is one rename.
	for _, r := range p.resources {
		if r.Name == trashNavID {
			panic(fmt.Sprintf(
				"resource: resource %q collides with the panel-wide Trash page at %s/%s — rename the resource",
				r.Name, p.basePath, trashNavID))
		}
	}
	for _, path := range p.pagePaths {
		if path == trashNavID {
			panic(fmt.Sprintf(
				"resource: a MountPage path or alias %q collides with the panel-wide Trash page at %s/%s — rename the page",
				path, p.basePath, trashNavID))
		}
	}
	// The page itself is open to any authenticated operator; its CONTENTS are
	// filtered per resource in trashResourcesFor. Gating the page on a delete
	// permission is what Payload shipped and spent eight months un-picking
	// (v3.78.0): being allowed to delete and being allowed to see what was
	// deleted are different questions, and fusing them is easy to do once and
	// expensive to undo.
	p.mux.HandleFunc("GET "+p.basePath+"/"+trashNavID, p.guard("", p.handleTrash))
	// Through addNavLink, the same insertion a resource gets: it creates the
	// heading or reuses an existing one. The heading matters because
	// toNavGroups files a Group-less item under the group that registered LAST,
	// so a bare append would render this panel-wide page as one more resource
	// inside whichever subject area came last.
	addNavLink(p, shell.NavItem{
		ID:    trashNavID,
		Label: "Trash",
		Icon:  "🗑",
		URL:   p.basePath + "/" + trashNavID,
		// Hidden when this operator can reach none of the opted-in resources.
		// Cosmetic only, like every other nav Visible — the route stays reachable
		// and renders an empty page, which is the correct answer to "show me my
		// trash" when none of it is yours. Cheap by construction: role checks,
		// never a query.
		Visible: func(ctx context.Context) bool { return len(p.trashResourcesFor(ctx)) > 0 },
	}, trashNavGroup)
}

// trashResourcesFor returns the opted-in resources this operator may see,
// in registration order.
func (p *Panel) trashResourcesFor(ctx context.Context) []Resource {
	// Type-assert once; nil is safe, because validateRoleConfig has already
	// panicked at Register for any resource carrying a RequiredRole without a
	// RoleAuthenticator behind it.
	ra, _ := p.auth.(RoleAuthenticator)

	var out []Resource
	for _, r := range p.resources {
		if r.TrashLister == nil {
			continue
		}
		if r.RequiredRole != "" && (ra == nil || !ra.HasRole(ctx, r.RequiredRole)) {
			continue
		}
		if r.Visible != nil && !r.Visible(ctx) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// handleTrash renders the panel-wide Trash page.
//
// One resource's lister failing must not blank the page, but it must not
// silently shrink it either: the failure becomes a section that says it could
// not be read. A resource that just disappears from the page is
// indistinguishable from one whose trash is empty, and the row the operator
// came looking for is exactly the one they would then give up on.
func (p *Panel) handleTrash(w http.ResponseWriter, req *http.Request) {
	shell.SecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	ctx := req.Context()

	d := trashPageData{
		BasePath: p.basePath,
		// One session-bound token covers every section. The restore route each
		// button posts to still runs that resource's own guard, so this is a
		// forgery check and never an authorisation.
		CSRFToken: csrf.Issue(p.csrfKey, p.sessionValue(req), csrf.DefaultTTL),
	}
	for _, r := range p.trashResourcesFor(ctx) {
		rows, total, err := r.TrashLister(ctx, ListQuery{
			Tenant: tenant.From(ctx),
			Limit:  trashPageSize,
		})
		if err != nil {
			slog.ErrorContext(ctx, "resource: trash lister failed", "resource", r.Name, "err", err)
			d.Sections = append(d.Sections, trashSection{Resource: r, Err: true})
			continue
		}
		if total == 0 && len(rows) == 0 {
			continue // nothing deleted here; no empty table to scroll past
		}
		// max, not total: a consumer that returns len(rows) as its total (or 0
		// beside a full page) would otherwise render a heading that says 0 above
		// rows the operator can see, or claim to show more than it has.
		//
		// But max() alone would render that contradiction as a perfectly
		// plausible page, and the contradiction has a specific likely cause: a
		// tenant-scoped COUNT beside an unscoped SELECT — exactly what
		// TrashLister's doc warns about. Smoothing it over silently is the
		// failure this whole page is built to refuse, so say so.
		if total < len(rows) {
			slog.WarnContext(ctx, "resource: trash lister reported fewer total rows than it returned",
				"resource", r.Name, "total", total, "rows", len(rows))
		}
		d.Sections = append(d.Sections, trashSection{Resource: r, Rows: rows, Total: max(total, len(rows))})
	}

	layoutComp := shell.Layout(p.title, p.activeNav(ctx, trashNavID), trashPageContent(d))
	renderCtx := shell.ContextWithChrome(ctx, p.chromeStateFrom(req))
	if err := layoutComp.Render(renderCtx, w); err != nil {
		slog.ErrorContext(ctx, "resource: render trash page", "err", err)
		http.Error(w, "render failed", http.StatusInternalServerError)
	}
}
