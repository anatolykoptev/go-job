package adminui

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/anatolykoptev/go-kit/admintable"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// huntLister builds a read-only Lister for a single hunt_* table. count + select +
// spec-driven ORDER BY + paging are shared here; the per-table SELECT column list
// (`cols`, author-constant) and row->Row mapping (`scan`) are supplied by each
// resource. `table`/`cols` are author-constant (never user input); request values
// reach SQL only as bind args.
func huntLister(pool *pgxpool.Pool, table, cols string, spec admintable.Spec, scan func(pgx.Rows) (resource.Row, error)) func(context.Context, resource.ListQuery) ([]resource.Row, int, error) {
	return func(ctx context.Context, q resource.ListQuery) ([]resource.Row, int, error) {
		where := "TRUE"
		if w := q.WhereConds; len(w) > 0 {
			where = w
		}
		var total int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE "+where, q.WhereArgs...).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("adminui: count %s: %w", table, err)
		}
		n := len(q.WhereArgs)
		args := append(append([]any{}, q.WhereArgs...), q.Limit, q.Offset)
		query := fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY %s LIMIT $%d OFFSET $%d",
			cols, table, where, spec.OrderBy(q.Sort), n+1, n+2)
		rows, err := pool.Query(ctx, query, args...)
		if err != nil {
			return nil, 0, fmt.Errorf("adminui: list %s: %w", table, err)
		}
		defer rows.Close()
		var out []resource.Row
		for rows.Next() {
			r, err := scan(rows)
			if err != nil {
				return nil, 0, fmt.Errorf("adminui: scan %s: %w", table, err)
			}
			out = append(out, r)
		}
		return out, total, rows.Err()
	}
}

func boolStr(b bool) string {
	if b {
		return "yes"
	}
	return ""
}

// --- bounties ---

var bountiesSpec = admintable.Spec{
	Columns: []admintable.Column{
		{Key: colKeyTitle, Label: "Title", Sortable: true, SQLExpr: colKeyTitle},
		{Key: "org", Label: "Org", Sortable: true, SQLExpr: "org", NullsLast: true},
		{Key: "amount", Label: "Amount", Sortable: true, SQLExpr: "amount_cents", NullsLast: true},
		{Key: colSource, Label: lblSource, Sortable: false},
		{Key: "posted", Label: lblPosted, Sortable: true, SQLExpr: "posted_at", NullsLast: true, TieBreakSQLExpr: "last_seen_at DESC"},
		{Key: keyRecent, Label: lblRecent, Sortable: true, SQLExpr: colLastSeen},
		{Key: colStatus, Label: lblStatus, Sortable: true, SQLExpr: colStatus},
	},
	DefaultKey: keyRecent, DefaultDir: admintable.Desc,
}

var bountiesFilter = admintable.FilterSpec{Filters: []admintable.Filter{
	{Key: keyQ, SQLExprs: []string{colKeyTitle, "org"}, Match: admintable.ILike},
	{Key: colStatus, SQLExpr: colStatus, Match: admintable.Eq, Allowed: []string{"open", "closed"}},
}}

func bountiesResource(pool *pgxpool.Pool) resource.Resource {
	cols := "id, COALESCE(title,''), COALESCE(org,''), amount_cents, COALESCE(currency,''), COALESCE(source,''), posted_at, last_seen_at, COALESCE(status,''), COALESCE(url,'')"
	scan := func(rows pgx.Rows) (resource.Row, error) {
		var id int64
		var title, org, currency, source, status, url string
		var amount *int
		var posted, recent *time.Time
		if err := rows.Scan(&id, &title, &org, &amount, &currency, &source, &posted, &recent, &status, &url); err != nil {
			return resource.Row{}, err
		}
		return resource.Row{ID: strconv.FormatInt(id, 10), Href: url, Cells: []resource.Cell{
			{Value: title}, {Value: org}, {Value: centsStr(amount, currency)}, {Value: source},
			{Value: dateStr(posted)}, {Value: dateStr(recent)}, {Value: status},
		}}, nil
	}
	return resource.Resource{Name: "bounties", Title: "Bounties", Icon: "\U0001F3AF", Group: grpHunt,
		Sort: bountiesSpec, Filter: bountiesFilter, Perms: resource.ReadAny,
		Lister: huntLister(pool, "hunt_bounties", cols, bountiesSpec, scan)}
}

// --- freelance ---

var freelanceSpec = admintable.Spec{
	Columns: []admintable.Column{
		{Key: colKeyTitle, Label: "Title", Sortable: true, SQLExpr: colKeyTitle},
		{Key: colPlatform, Label: lblPlatform, Sortable: true, SQLExpr: colPlatform},
		{Key: "budget", Label: "Budget", Sortable: true, SQLExpr: "COALESCE(budget_max,0)"},
		{Key: "location", Label: "Location", Sortable: false},
		{Key: colSource, Label: lblSource, Sortable: false},
		{Key: "posted", Label: lblPosted, Sortable: true, SQLExpr: "posted_at", NullsLast: true},
		{Key: keyRecent, Label: lblRecent, Sortable: true, SQLExpr: colLastSeen},
		{Key: colStatus, Label: lblStatus, Sortable: true, SQLExpr: "COALESCE(status,'open')"},
	},
	DefaultKey: keyRecent, DefaultDir: admintable.Desc,
}

var freelanceFilter = admintable.FilterSpec{Filters: []admintable.Filter{
	{Key: keyQ, SQLExprs: []string{colKeyTitle}, Match: admintable.ILike},
	{Key: colPlatform, SQLExpr: colPlatform, Match: admintable.Eq, Allowed: []string{"upwork", "toptal", "contra", "wellfound"}},
}}

func freelanceResource(pool *pgxpool.Pool) resource.Resource {
	cols := "id, COALESCE(title,''), COALESCE(platform,''), budget_max, COALESCE(budget_currency,''), COALESCE(location,''), COALESCE(source,''), posted_at, last_seen_at, COALESCE(status,'open'), COALESCE(url,'')"
	scan := func(rows pgx.Rows) (resource.Row, error) {
		var id int64
		var title, platform, cur, location, source, status, url string
		var budget *int
		var posted, recent *time.Time
		if err := rows.Scan(&id, &title, &platform, &budget, &cur, &location, &source, &posted, &recent, &status, &url); err != nil {
			return resource.Row{}, err
		}
		return resource.Row{ID: strconv.FormatInt(id, 10), Href: url, Cells: []resource.Cell{
			{Value: title}, {Value: platform}, {Value: moneyStr(budget, cur)}, {Value: location},
			{Value: source}, {Value: dateStr(posted)}, {Value: dateStr(recent)}, {Value: status},
		}}, nil
	}
	return resource.Resource{Name: "freelance", Title: "Freelance", Icon: "\U0001F4BB", Group: grpHunt,
		Sort: freelanceSpec, Filter: freelanceFilter, Perms: resource.ReadAny,
		Lister: huntLister(pool, "hunt_freelance", cols, freelanceSpec, scan)}
}

// --- security programs ---

var securitySpec = admintable.Spec{
	Columns: []admintable.Column{
		{Key: "program", Label: "Program", Sortable: true, SQLExpr: "name"},
		{Key: colPlatform, Label: lblPlatform, Sortable: true, SQLExpr: colPlatform},
		{Key: "type", Label: "Type", Sortable: true, SQLExpr: "COALESCE(program_type,'')"},
		{Key: "maxbounty", Label: "Max Bounty", Sortable: true, SQLExpr: "COALESCE(max_bounty,0)"},
		{Key: "managed", Label: "Managed", Sortable: true, SQLExpr: "managed"},
		{Key: keyRecent, Label: "Seen", Sortable: true, SQLExpr: colLastSeen},
		{Key: colStatus, Label: lblStatus, Sortable: true, SQLExpr: "COALESCE(status,'open')"},
	},
	DefaultKey: keyRecent, DefaultDir: admintable.Desc,
}

var securityFilter = admintable.FilterSpec{Filters: []admintable.Filter{
	{Key: keyQ, SQLExprs: []string{"name"}, Match: admintable.ILike},
	{Key: colPlatform, SQLExpr: colPlatform, Match: admintable.Eq, Allowed: []string{"hackerone", "bugcrowd", "intigriti", "yeswehack"}},
}}

func securityResource(pool *pgxpool.Pool) resource.Resource {
	cols := "id, COALESCE(name,''), COALESCE(platform,''), COALESCE(program_type,''), max_bounty, COALESCE(managed,false), last_seen_at, COALESCE(status,'open'), COALESCE(url,'')"
	scan := func(rows pgx.Rows) (resource.Row, error) {
		var id int64
		var name, platform, ptype, status, url string
		var managed bool
		var maxb *int
		var recent *time.Time
		if err := rows.Scan(&id, &name, &platform, &ptype, &maxb, &managed, &recent, &status, &url); err != nil {
			return resource.Row{}, err
		}
		return resource.Row{ID: strconv.FormatInt(id, 10), Href: url, Cells: []resource.Cell{
			{Value: name}, {Value: platform}, {Value: ptype}, {Value: intStr(maxb)},
			{Value: boolStr(managed)}, {Value: dateStr(recent)}, {Value: status},
		}}, nil
	}
	return resource.Resource{Name: "security", Title: "Security", Icon: "\U0001F512", Group: grpHunt,
		Sort: securitySpec, Filter: securityFilter, Perms: resource.ReadAny,
		Lister: huntLister(pool, "hunt_security", cols, securitySpec, scan)}
}

// --- audit contests (no status column) ---

var contestsSpec = admintable.Spec{
	Columns: []admintable.Column{
		{Key: "contest", Label: "Contest", Sortable: true, SQLExpr: colKeyTitle},
		{Key: colPlatform, Label: lblPlatform, Sortable: true, SQLExpr: colPlatform},
		{Key: "pool", Label: "Pool", Sortable: true, SQLExpr: "COALESCE(total_pool,0)"},
		{Key: "starts", Label: "Starts", Sortable: true, SQLExpr: "starts_at", NullsLast: true},
		{Key: "ends", Label: "Ends", Sortable: true, SQLExpr: "ends_at", NullsLast: true},
		{Key: keyRecent, Label: "Seen", Sortable: true, SQLExpr: colLastSeen},
	},
	DefaultKey: keyRecent, DefaultDir: admintable.Desc,
}

var contestsFilter = admintable.FilterSpec{Filters: []admintable.Filter{
	{Key: keyQ, SQLExprs: []string{colKeyTitle}, Match: admintable.ILike},
	{Key: colPlatform, SQLExpr: colPlatform, Match: admintable.Eq, Allowed: []string{"code4rena", "sherlock", "cantina", "codehawks", "immunefi"}},
}}

func contestsResource(pool *pgxpool.Pool) resource.Resource {
	cols := "id, COALESCE(title,''), COALESCE(platform,''), total_pool, COALESCE(currency,''), starts_at, ends_at, last_seen_at, COALESCE(url,'')"
	scan := func(rows pgx.Rows) (resource.Row, error) {
		var id int64
		var title, platform, cur, url string
		var pool *int
		var starts, ends, recent *time.Time
		if err := rows.Scan(&id, &title, &platform, &pool, &cur, &starts, &ends, &recent, &url); err != nil {
			return resource.Row{}, err
		}
		return resource.Row{ID: strconv.FormatInt(id, 10), Href: url, Cells: []resource.Cell{
			{Value: title}, {Value: platform}, {Value: moneyStr(pool, cur)},
			{Value: dateStr(starts)}, {Value: dateStr(ends)}, {Value: dateStr(recent)},
		}}, nil
	}
	return resource.Resource{Name: "audit-contests", Title: "Audit Contests", Icon: "\U0001F9FE", Group: grpHunt,
		Sort: contestsSpec, Filter: contestsFilter, Perms: resource.ReadAny,
		Lister: huntLister(pool, "hunt_audit_contests", cols, contestsSpec, scan)}
}

func centsStr(cents *int, cur string) string {
	if cents == nil || *cents == 0 {
		return ""
	}
	return moneyStr(intPtr(*cents/100), cur)
}

func intPtr(i int) *int { return &i }

func moneyStr(v *int, cur string) string {
	if v == nil || *v == 0 {
		return ""
	}
	if cur == "" {
		return strconv.Itoa(*v)
	}
	return strconv.Itoa(*v) + " " + cur
}
