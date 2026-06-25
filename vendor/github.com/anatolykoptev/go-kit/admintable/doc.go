// Package admintable provides SQL-injection-safe declarative primitives for
// server-rendered admin list pages: a sort resolver ([Spec]) and a WHERE-filter
// builder ([FilterSpec]).
//
// # Security model
//
// Both primitives share the same invariant: only author-declared compile-time
// constants ever reach SQL — never raw URL parameter bytes.
//
//   - [Spec]: only [Column.SQLExpr] and [Column.TieBreakSQLExpr] reach ORDER BY.
//     URL sort and dir parameters are equality-matched against a closed set of
//     declared keys and are NEVER interpolated.
//
//   - [FilterSpec]: only [Filter.SQLExpr] / [Filter.SQLExprs] values, the literal
//     operators "= $N" / "= ANY($N::text[])" / "ILIKE $N ESCAPE '\'", and the
//     literal conjunctives " AND " / " OR " ever appear in the returned WHERE
//     conditions string. URL parameter values go exclusively into bind args —
//     NEVER into the conditions string.  For [ILike] filters, metacharacters in
//     the search term (%, _, \) are escaped before binding; the ESCAPE '\' clause
//     in the emitted SQL tells Postgres to honor that escaping.
//
// Callers that pass [Spec.OrderBy] output to fmt.Sprintf should annotate:
//
//	//nolint:gosec // only Spec-owned SQLExpr + literal "ASC"/"DESC" + optional
//	// literal " NULLS LAST" reach SQL; URL params are equality-matched against
//	// a closed set, never interpolated.
//
// Callers that compose the [FilterSpec.Where] conds string should annotate:
//
//	//nolint:gosec // only FilterSpec-owned SQLExpr/SQLExprs + literal operators + $N
//	// placeholders reach SQL; URL values are bind args, never interpolated.
//
// # Startup validation
//
// Each [Spec] should call [Spec.Valid] and each [FilterSpec] should call
// [FilterSpec.Valid] at package-init or program startup to catch
// misconfiguration early rather than at query time.
//
// # Typical usage (sort + filter together)
//
//	var tableSpec = admintable.Spec{
//	    Columns: []admintable.Column{
//	        {Key: "name",    Label: "Name",    Sortable: true,  SQLExpr: "u.name"},
//	        {Key: "updated", Label: "Updated", Sortable: true,  SQLExpr: "u.updated_at", NullsLast: true},
//	        {Key: "notes",   Label: "Notes",   Sortable: false},
//	    },
//	    DefaultKey: "updated",
//	    DefaultDir: admintable.Desc,
//	}
//
//	var filterSpec = admintable.FilterSpec{
//	    Filters: []admintable.Filter{
//	        {Key: "status", SQLExpr: "subscription_status", Match: admintable.Eq},
//	        {Key: "plan",   SQLExpr: "plan_id",             Match: admintable.Eq,    Allowed: []string{"free", "pro"}},
//	        {Key: "source", SQLExpr: "source",              Match: admintable.AnyOf},
//	        // ILike: case-insensitive substring search across two columns, one bind.
//	        // ?q=alice → (name ILIKE $6 ESCAPE '\' OR notes ILIKE $6 ESCAPE '\')
//	        // with bound value "%alice%" — one arg, $6 referenced twice.
//	        {Key: "q",      SQLExprs: []string{"name", "notes"}, Match: admintable.ILike},
//	    },
//	}
//
//	func init() {
//	    if err := tableSpec.Valid(); err != nil { panic(err) }
//	    if err := filterSpec.Valid(); err != nil { panic(err) }
//	}
//
//	func handleList(w http.ResponseWriter, r *http.Request) {
//	    q := r.URL.Query()
//	    st := tableSpec.Resolve(q.Get("sort"), q.Get("dir"))
//
//	    conds, filterArgs := filterSpec.Where(q, 3) // $1/$2 = LIMIT/OFFSET
//
//	    baseQuery := "SELECT ... FROM subscriptions"
//	    if conds != "" {
//	        //nolint:gosec // only FilterSpec-owned SQLExpr + literal operators reach SQL
//	        baseQuery += " WHERE " + conds
//	    }
//	    //nolint:gosec // only Spec-owned SQLExpr + literal "ASC"/"DESC" reach SQL
//	    baseQuery += fmt.Sprintf(" ORDER BY %s LIMIT $1 OFFSET $2", tableSpec.OrderBy(st))
//
//	    // args order matches the $N order: $1/$2 first, then the filter binds.
//	    rows, err := db.Query(ctx, baseQuery, append([]any{limit, offset}, filterArgs...)...)
//	    // ...
//	}
package admintable
