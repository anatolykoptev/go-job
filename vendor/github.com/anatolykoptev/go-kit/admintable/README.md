# admintable

SQL-injection-safe declarative primitives for server-rendered admin list pages:
a sort resolver (`Spec`) and a WHERE-filter builder (`FilterSpec`).

Second go-kit/go-panel admin primitive. `Spec` ported from `go-nerv internal/admin/table`;
`FilterSpec` extracted from a hand-coded pattern duplicated across ≥5 sites
(oxpulse-admin subscriptions/stripe_events/analytics, go-nerv, go-piter).

## Security model

Both primitives share the same invariant: **only author-declared compile-time
constants ever reach SQL — never raw URL parameter bytes.**

### Sort (`Spec` / `OrderBy`)

URL sort and dir parameters are **equality-matched** against a closed set of
declared column keys. The only bytes that ever reach an `ORDER BY` clause are:

- `Column.SQLExpr` — author-declared compile-time constant
- `Column.TieBreakSQLExpr` — author-declared compile-time constant (optional)
- The literal strings `"ASC"`, `"DESC"`, and `" NULLS LAST"`

### Filter (`FilterSpec` / `Where`)

URL parameter values go exclusively into **bind args** (`$N`). The returned
conditions string contains only:

- `Filter.SQLExpr` / `Filter.SQLExprs` — author-declared compile-time constant column expressions
- The literal operators `"= $N"` (Eq), `"= ANY($N::text[])"` (AnyOf),
  or `"ILIKE $N ESCAPE '\'"` (ILike, per column, OR'd)
- The literal conjunctives `" AND "` and `" OR "`

A URL parameter whose key is **not declared** in the `FilterSpec` is silently
ignored — it cannot inject a predicate.  When `Filter.Allowed` is set, a value
not in the enum is treated as if the parameter were absent (safe degrade — no
filter applied, no error).

For `ILike`, the search term is LIKE-escaped (`\`→`\\`, `%`→`\%`, `_`→`\_`)
and wrapped as `%term%` before binding.  The `ESCAPE '\'` clause in the emitted
SQL tells Postgres to honor the escaping, so a user searching for `"50%"` matches
the literal string `"50%"`, not every row.

## Usage

### Sort only

```go
var tableSpec = admintable.Spec{
    Columns: []admintable.Column{
        {Key: "name",    Label: "Name",    Sortable: true,  SQLExpr: "u.name"},
        {Key: "updated", Label: "Updated", Sortable: true,  SQLExpr: "u.updated_at", NullsLast: true},
        {Key: "notes",   Label: "Notes",   Sortable: false},
    },
    DefaultKey: "updated",
    DefaultDir: admintable.Desc,
}

func init() {
    if err := tableSpec.Valid(); err != nil {
        panic(err) // catch misconfiguration at startup, not at query time
    }
}

func handleList(w http.ResponseWriter, r *http.Request) {
    st := tableSpec.Resolve(r.URL.Query().Get("sort"), r.URL.Query().Get("dir"))
    //nolint:gosec // only Spec-owned SQLExpr + literal "ASC"/"DESC" reach SQL
    query := fmt.Sprintf("SELECT ... ORDER BY %s LIMIT $1 OFFSET $2", tableSpec.OrderBy(st))
}
```

### Sort + Filter together

```go
var filterSpec = admintable.FilterSpec{
    Filters: []admintable.Filter{
        // Eq: exact match. Empty value → filter skipped.
        {Key: "status", SQLExpr: "subscription_status", Match: admintable.Eq},
        // Eq + Allowed: only "free" and "pro" accepted; anything else → filter skipped.
        {Key: "plan",   SQLExpr: "plan_id",             Match: admintable.Eq,
         Allowed: []string{"free", "pro"}},
        // AnyOf: ?source=organic&source=referral → col = ANY($N::text[])
        // pgx encodes []string as Postgres text[].
        {Key: "source", SQLExpr: "source",              Match: admintable.AnyOf},
        // ILike: ?q=alice → (name ILIKE $6 ESCAPE '\' OR notes ILIKE $6 ESCAPE '\')
        // One bind arg ("%alice%"), $6 referenced twice. Term is LIKE-escaped.
        {Key: "q",      SQLExprs: []string{"name", "notes"}, Match: admintable.ILike},
    },
}

func init() {
    if err := filterSpec.Valid(); err != nil { panic(err) }
}

func handleList(w http.ResponseWriter, r *http.Request) {
    q  := r.URL.Query()
    st := tableSpec.Resolve(q.Get("sort"), q.Get("dir"))

    // $1/$2 reserved for LIMIT/OFFSET; filter args start at $3.
    conds, filterArgs := filterSpec.Where(q, 3)

    base := "SELECT ... FROM subscriptions"
    if conds != "" {
        //nolint:gosec // only FilterSpec-owned SQLExpr + literal operators reach SQL
        base += " WHERE " + conds
    }
    //nolint:gosec // only Spec-owned SQLExpr + literal "ASC"/"DESC" reach SQL
    base += fmt.Sprintf(" ORDER BY %s LIMIT $1 OFFSET $2", tableSpec.OrderBy(st))

    rows, _ := db.Query(ctx, base, append([]any{limit, offset}, filterArgs...)...)
}
```

## API

### Sort

| Type / Function | Purpose |
|---|---|
| `Column` | Declarative column definition (Key, Label, Sortable, SQLExpr, NullsLast, TieBreakSQLExpr, Width, Align) |
| `Dir` (`Asc` / `Desc`) | Sort direction constants |
| `Spec` | Table contract: Columns + DefaultKey + DefaultDir |
| `Spec.Valid() error` | Startup validation: no sortable cols / bad DefaultKey / duplicate keys |
| `Spec.Resolve(sort, dir string) State` | Parse URL params safely; always returns a valid State |
| `Spec.OrderBy(State) string` | Build the ORDER BY fragment; only author-declared bytes reach SQL |
| `State` | Resolved sort: Key (validated column key) + Dir (Asc or Desc) |

### Filter

| Type / Function | Purpose |
|---|---|
| `Match` (`Eq` / `AnyOf` / `ILike`) | Predicate shape: exact equality, set membership, or case-insensitive substring |
| `Filter` | One WHERE condition: Key, SQLExpr (Eq/AnyOf) or SQLExprs (ILike), Match, optional Allowed |
| `FilterSpec` | Collection of Filters for one list page |
| `FilterSpec.Valid() error` | Startup validation: dup Keys / empty SQLExpr / unknown Match / ILike misuse |
| `FilterSpec.Where(vals url.Values, startArg int) (conds string, args []any)` | Build AND-joined WHERE conditions; URL values → bind args only |

#### `Where` semantics

- Returns `("", nil)` when no filters are active.
- Returned `conds` has no leading `WHERE` or `AND` — the caller composes.
- `startArg` is the next `$N` index; placeholders number sequentially for **active
  filters only** — skipped filters consume no index.
- **Eq**: uses `vals.Get(key)` (first value); empty string → filter skipped.
- **AnyOf**: uses `vals[key]` (all values); empty slice → filter skipped; arg is
  `[]string` (pgx encodes this as Postgres `text[]`).
- **ILike**: uses `vals.Get(key)` (first value); empty string → filter skipped.
  Consumes **exactly one** `$N` index even when multiple columns are declared —
  Postgres allows a placeholder to be referenced multiple times in one query.
  The bound arg is the LIKE-escaped term wrapped as `%term%` (string, not `[]string`).
- **Allowed**: value not in enum → filter skipped (safe degrade, not an error).
  Must NOT be set on an `ILike` filter — `Valid()` returns an error.

#### Field usage by Match

| Match  | `SQLExpr`    | `SQLExprs`   | `Allowed`  |
|--------|-------------|-------------|-----------|
| `Eq`   | required    | must be nil | optional  |
| `AnyOf`| required    | must be nil | optional  |
| `ILike`| must be `""`| ≥1 required | must be nil|

## NullsLast

Set `Column.NullsLast: true` for nullable date/time columns. `OrderBy` emits the
direction keyword **before** `NULLS LAST`, which is the only valid Postgres syntax:

```
"i.updated_at DESC NULLS LAST"   -- correct
"i.updated_at NULLS LAST DESC"   -- SQLSTATE 42601 syntax error
```

## TieBreakSQLExpr

Set `Column.TieBreakSQLExpr` for stable pagination on low-cardinality columns:

```go
{Key: "score", Sortable: true, SQLExpr: "fit_score", NullsLast: true,
 TieBreakSQLExpr: "last_seen_at DESC"}
// → "fit_score DESC NULLS LAST, last_seen_at DESC"
```

## No external dependencies

Sort half: `errors`, `fmt`, `strings`.
Filter half adds: `net/url` (stdlib). No new `go.mod` entries.
