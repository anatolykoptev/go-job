package adminui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/anatolykoptev/go-kit/admintable"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/jackc/pgx/v5/pgxpool"
)

// jobsSpec drives the /admin/jobs table sort/columns. Cell order in the Lister
// MUST match Columns order. Ported from go-nerv's retired jobsSortSpec.
var jobsSpec = admintable.Spec{
	Columns: []admintable.Column{
		{Key: "fit", Label: "Fit", Sortable: true, SQLExpr: "fit_score", NullsLast: true, TieBreakSQLExpr: "last_seen_at DESC"},
		{Key: "title", Label: "Title", Sortable: true, SQLExpr: "title"},
		{Key: "company", Label: "Company", Sortable: true, SQLExpr: "company", NullsLast: true},
		{Key: "rec", Label: "Rec", Sortable: true, SQLExpr: "recommendation_rank", NullsLast: true},
		{Key: "status", Label: "Status", Sortable: true, SQLExpr: "status"},
		{Key: "posted", Label: "Posted", Sortable: true, SQLExpr: "posted_at", NullsLast: true, TieBreakSQLExpr: "last_seen_at DESC"},
		{Key: "recent", Label: "Recent", Sortable: true, SQLExpr: "last_seen_at"},
		{Key: "location", Label: "Location", Sortable: false},
		{Key: "salary", Label: "Salary", Sortable: false},
		{Key: "source", Label: "Source", Sortable: false},
	},
	DefaultKey: "fit",
	DefaultDir: admintable.Desc,
}

func jobsResource(pool *pgxpool.Pool) resource.Resource {
	return resource.Resource{
		Name:   "jobs",
		Title:  "Jobs",
		Icon:   "\U0001F4BC",
		Group:  "Hunt",
		Sort:   jobsSpec,
		Perms:  resource.ReadAny,
		Lister: jobsLister(pool),
	}
}

func jobsLister(pool *pgxpool.Pool) func(context.Context, resource.ListQuery) ([]resource.Row, int, error) {
	return func(ctx context.Context, q resource.ListQuery) ([]resource.Row, int, error) {
		where := "TRUE"
		if strings.TrimSpace(q.WhereConds) != "" {
			where = q.WhereConds
		}
		var total int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM hunt_jobs WHERE "+where, q.WhereArgs...).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("adminui: count jobs: %w", err)
		}
		n := len(q.WhereArgs)
		args := append(append([]any{}, q.WhereArgs...), q.Limit, q.Offset)
		query := fmt.Sprintf(`
			SELECT id, COALESCE(title,''), COALESCE(company,''), COALESCE(status,''),
			       fit_score, recommendation_rank, posted_at, last_seen_at,
			       COALESCE(location,''), salary_min, salary_max, COALESCE(source,''), COALESCE(url,'')
			  FROM hunt_jobs WHERE %s ORDER BY %s LIMIT $%d OFFSET $%d`,
			where, jobsSpec.OrderBy(q.Sort), n+1, n+2)
		rows, err := pool.Query(ctx, query, args...)
		if err != nil {
			return nil, 0, fmt.Errorf("adminui: list jobs: %w", err)
		}
		defer rows.Close()

		var out []resource.Row
		for rows.Next() {
			var (
				id                       int64
				title, company, status   string
				location, source, url    string
				fit, recRank, sMin, sMax *int
				posted, recent           *time.Time
			)
			if err := rows.Scan(&id, &title, &company, &status, &fit, &recRank, &posted, &recent, &location, &sMin, &sMax, &source, &url); err != nil {
				return nil, 0, fmt.Errorf("adminui: scan job: %w", err)
			}
			out = append(out, resource.Row{
				ID:   strconv.FormatInt(id, 10),
				Href: url,
				Cells: []resource.Cell{
					{Value: intStr(fit)},
					{Value: title},
					{Value: company},
					{Value: intStr(recRank)},
					{Value: status},
					{Value: dateStr(posted)},
					{Value: dateStr(recent)},
					{Value: location},
					{Value: salaryStr(sMin, sMax)},
					{Value: source},
				},
			})
		}
		return out, total, rows.Err()
	}
}

func intStr(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

func dateStr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02")
}

func salaryStr(lo, hi *int) string {
	switch {
	case lo == nil && hi == nil:
		return ""
	case lo != nil && hi != nil:
		return fmt.Sprintf("%d–%d", *lo, *hi)
	case lo != nil:
		return fmt.Sprintf("%d+", *lo)
	default:
		return fmt.Sprintf("≤%d", *hi)
	}
}
