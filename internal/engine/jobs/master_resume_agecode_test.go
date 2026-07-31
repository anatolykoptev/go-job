package jobs

// isAgeMissing decides whether a graph error means "AGE is not installed"
// (tolerated: nothing to rebuild onto) or "a real cypher failure" (surfaced:
// the profile is committed but the graph is stale). Getting that wrong is
// silent — both branches only log — so the classifier needs its own oracle
// rather than being exercised incidentally by a cluster that happens to have
// AGE installed.
//
// It needs no database: the codes are the contract.

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsAgeMissing_ClassifiesByRealSQLSTATE(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			// The code Postgres actually emits for a missing ag_catalog.
			// Captured live from CI's pgvector image, which ships no AGE:
			//   ERROR: schema "ag_catalog" does not exist (SQLSTATE 3F000)
			// The set previously carried an invented "3F001" — class 3F has
			// exactly one member — so this case returned false and an
			// AGE-less cluster was reported as a real cypher failure.
			name: "missing ag_catalog schema (3F000)",
			err:  &pgconn.PgError{Code: "3F000", Message: `schema "ag_catalog" does not exist`},
			want: true,
		},
		{
			name: "cypher function absent (42883)",
			err:  &pgconn.PgError{Code: "42883", Message: "function ag_catalog.cypher(...) does not exist"},
			want: true,
		},
		{
			name: "graph label table absent (42P01)",
			err:  &pgconn.PgError{Code: "42P01", Message: `relation "resume_graph.Skill" does not exist`},
			want: true,
		},
		{
			// The half that matters in the other direction: a genuine cypher
			// failure must NOT be laundered into "AGE absent", or the build
			// upserts new nodes on top of a stale graph and resume_generate
			// silently mixes live and dead ids.
			name: "real cypher syntax error (42601)",
			err:  &pgconn.PgError{Code: "42601", Message: "syntax error at or near \"MATCH\""},
			want: false,
		},
		{
			name: "wrapped 3F000 is still recognised",
			err:  fmt.Errorf("clear graph: %w", &pgconn.PgError{Code: "3F000"}),
			want: true,
		},
		{
			name: "non-pg error is not absence",
			err:  errors.New("context deadline exceeded"),
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAgeMissing(tc.err); got != tc.want {
				t.Errorf("isAgeMissing(%v) = %v, want %v — a misclassified graph error is silent in "+
					"production: both branches only log, and the wrong one hides a stale graph that "+
					"resume_generate reads", tc.err, got, tc.want)
			}
		})
	}
}
