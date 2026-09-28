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

// oversizeSpec drives the /admin/oversize table. payload and sample JSONB cols
// are intentionally excluded from all SELECTs (potentially very large).
var oversizeSpec = admintable.Spec{
	Columns: []admintable.Column{
		{Key: "tool", Label: "Tool", Sortable: true, SQLExpr: colToolName},
		{Key: "items", Label: "Items", Sortable: true, SQLExpr: "item_count"},
		{Key: "size", Label: "Size (B)", Sortable: true, SQLExpr: "size_bytes"},
		{Key: "hash", Label: "Query Hash", Sortable: false},
		{Key: "created", Label: "Created", Sortable: true, SQLExpr: "created_at"},
	},
	DefaultKey: "created",
	DefaultDir: admintable.Desc,
}

var oversizeFilter = admintable.FilterSpec{Filters: []admintable.Filter{
	{Key: colToolName, SQLExpr: colToolName, Match: admintable.Eq},
}}

const (
	colToolName        = "tool_name"
	tblOversize        = "oversize_responses"
	grpSystem          = "System"
	oversizeSelectCols = "id, tool_name, item_count, size_bytes, query_hash, created_at"
)

func oversizeResource(pool *pgxpool.Pool, acctOf accountResolver) resource.Resource {
	scan := func(rows pgx.Rows) (resource.Row, error) {
		var id int64
		var toolName, queryHash string
		var itemCount, sizeBytes int
		var createdAt time.Time
		if err := rows.Scan(&id, &toolName, &itemCount, &sizeBytes, &queryHash, &createdAt); err != nil {
			return resource.Row{}, err
		}
		return resource.Row{
			ID: strconv.FormatInt(id, 10),
			Cells: []resource.Cell{
				{Value: toolName},
				{Value: strconv.Itoa(itemCount)},
				{Value: strconv.Itoa(sizeBytes)},
				{Value: queryHash},
				{Value: createdAt.Format("2006-01-02 15:04")},
			},
		}, nil
	}
	return resource.Resource{
		Name:   "oversize",
		Title:  "Oversize",
		Icon:   "\U0001F4E6",
		Group:  grpSystem,
		Sort:   oversizeSpec,
		Filter: oversizeFilter,
		Lister: oversizeLister(pool, acctOf, scan),
	}
}

// oversizeLister is the account-scoped variant of huntLister for
// oversize_responses (plan ADR-10): every count/select carries
// `account_id = $N` so a foreign account's spills are invisible — a missed
// account resolution binds uuid.Nil and simply matches nothing (fail-closed).
func oversizeLister(pool *pgxpool.Pool, acctOf accountResolver, scan func(pgx.Rows) (resource.Row, error)) func(context.Context, resource.ListQuery) ([]resource.Row, int, error) {
	return func(ctx context.Context, q resource.ListQuery) ([]resource.Row, int, error) {
		where := "TRUE"
		if w := q.WhereConds; len(w) > 0 {
			where = w
		}
		aid, _ := acctOf(ctx)
		n := len(q.WhereArgs)
		countArgs := append(append([]any{}, q.WhereArgs...), aid)
		var total int
		if err := pool.QueryRow(ctx,
			fmt.Sprintf("SELECT count(*) FROM %s WHERE %s AND account_id = $%d", tblOversize, where, n+1),
			countArgs...,
		).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("adminui: count oversize: %w", err)
		}
		args := append(append([]any{}, q.WhereArgs...), aid, q.Limit, q.Offset)
		query := fmt.Sprintf("SELECT %s FROM %s WHERE %s AND account_id = $%d ORDER BY %s LIMIT $%d OFFSET $%d",
			oversizeSelectCols, tblOversize, where, n+1, oversizeSpec.OrderBy(q.Sort), n+2, n+3)
		rows, err := pool.Query(ctx, query, args...)
		if err != nil {
			return nil, 0, fmt.Errorf("adminui: list oversize: %w", err)
		}
		defer rows.Close()
		var out []resource.Row
		for rows.Next() {
			r, err := scan(rows)
			if err != nil {
				return nil, 0, fmt.Errorf("adminui: scan oversize: %w", err)
			}
			out = append(out, r)
		}
		return out, total, rows.Err()
	}
}

// oversizeQuerySQL returns the SELECT query string used by oversizeLister for
// oversize_responses. Exposed for tests to assert payload/sample are absent.
func oversizeQuerySQL(where, orderBy string, limitPos, offsetPos int) string {
	return fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY %s LIMIT $%d OFFSET $%d",
		oversizeSelectCols, tblOversize, where, orderBy, limitPos, offsetPos)
}
