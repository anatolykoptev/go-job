package jobs

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgconn"
)

// graphBuffer collects the AGE graph mutations a master_resume_build would
// issue, in call order, WITHOUT executing any of them. The buffer is replayed
// (replayGraphAfterCommit) only after the relational profile transaction
// commits, so a rolled-back build never touches the graph (rebuild-then-swap).
// Counters are derived from the buffer, not a live AGE count.
type graphBuffer struct {
	nodes []graphNodeOp
	edges []graphEdgeOp
}

type graphNodeOp struct {
	label string
	id    int
	props map[string]string
}

type graphEdgeOp struct {
	fromLabel string
	fromID    int
	edgeLabel string
	toLabel   string
	toID      int
}

func newGraphBuffer() *graphBuffer { return &graphBuffer{} }

func (b *graphBuffer) addNode(label string, id int, props map[string]string) {
	b.nodes = append(b.nodes, graphNodeOp{label: label, id: id, props: props})
}

func (b *graphBuffer) addEdge(fromLabel string, fromID int, edgeLabel, toLabel string, toID int) {
	b.edges = append(b.edges, graphEdgeOp{
		fromLabel: fromLabel, fromID: fromID,
		edgeLabel: edgeLabel, toLabel: toLabel, toID: toID,
	})
}

func (b *graphBuffer) nodeCount() int { return len(b.nodes) }
func (b *graphBuffer) edgeCount() int { return len(b.edges) }

// guardLatestPersonID is the destructive-consent guard's read. It wraps
// GetLatestPersonIDChecked with the test-injection seam (masterResumeGuardHook,
// nil in production) so F4 can force the guard's error path. On a destructive
// surface the zero value is the safe one: an error REFUSES the build.
func (db *ResumeDB) guardLatestPersonID(ctx context.Context) (exists bool, id int, err error) {
	if h := masterResumeGuardHook; h != nil {
		if e := h(); e != nil {
			return false, 0, e
		}
	}
	return db.GetLatestPersonIDChecked(ctx)
}

// replayGraphAfterCommit runs the rebuild-then-swap graph phase AFTER the
// relational profile has committed. It clears the graph, then replays the
// buffered node/edge ops in call order. It distinguishes:
//   - AGE/graph absent (isAgeMissing): tolerated — there is nothing to swap
//     onto (e.g. the test cluster has no AGE). A WARN is logged; no replay.
//   - a real cypher error on clear: surfaced — the profile is committed and
//     correct but the graph is stale. A WARN names that state. The call does
//     not report success as though the graph were rebuilt, and the stale graph
//     is not silently upserted onto (which would mix live and dead ids).
//   - a replay op error: the profile is committed and correct but the graph is
//     partially stale. A WARN names that state; the call does not fail (the
//     profile is the source of truth and resume_generate degrades gracefully).
func replayGraphAfterCommit(ctx context.Context, db *ResumeDB, buf *graphBuffer) {
	if masterResumeGraphOpRecorder != nil {
		masterResumeGraphOpRecorder("clear")
	}
	if err := db.ClearGraph(ctx); err != nil {
		if isAgeMissing(err) {
			slog.Warn("master_resume_build: AGE graph absent — graph rebuild skipped (profile committed; graph stays as-is)",
				slog.Any("error", err))
			return
		}
		slog.Warn("master_resume_build: graph clear failed after commit — profile is committed and correct but the GRAPH IS STALE (resume_generate may return a degraded resume)",
			slog.Any("error", err))
		return
	}
	for _, n := range buf.nodes {
		if masterResumeGraphOpRecorder != nil {
			masterResumeGraphOpRecorder("node")
		}
		if err := db.UpsertGraphNode(ctx, n.label, n.id, n.props); err != nil {
			if isAgeMissing(err) {
				slog.Warn("master_resume_build: AGE graph absent during replay — graph rebuild aborted (profile committed; graph partially stale)",
					slog.Any("error", err))
				return
			}
			slog.Warn("master_resume_build: graph node replay failed — profile committed but GRAPH IS STALE",
				slog.String("label", n.label), slog.Int("id", n.id), slog.Any("error", err))
		}
	}
	for _, e := range buf.edges {
		if masterResumeGraphOpRecorder != nil {
			masterResumeGraphOpRecorder("edge")
		}
		if err := db.UpsertGraphEdge(ctx, e.fromLabel, e.fromID, e.edgeLabel, e.toLabel, e.toID); err != nil {
			if isAgeMissing(err) {
				slog.Warn("master_resume_build: AGE graph absent during replay — graph rebuild aborted (profile committed; graph partially stale)",
					slog.Any("error", err))
				return
			}
			slog.Warn("master_resume_build: graph edge replay failed — profile committed but GRAPH IS STALE",
				slog.String("edge", e.edgeLabel), slog.Any("error", err))
		}
	}
}

// isAgeMissing reports whether err is a Postgres error indicating the AGE
// extension or the resume_graph relation is not present (the environment has
// not installed AGE), as opposed to a real cypher statement error. The cypher
// entry point is ag_catalog.cypher; when AGE is absent the call fails with an
// undefined-function / undefined-schema / undefined-object code, and a missing
// graph surfaces as undefined-table. A real cypher error (syntax, constraint,
// etc.) has a different code and is NOT matched here, so the caller keeps
// fail-closed on it.
func isAgeMissing(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "42883", // undefined_function
		"42704", // undefined_object
		// 3F000 invalid_schema_name — this is what Postgres returns for a missing
		// ag_catalog, i.e. "AGE is not installed". Class 3F has no other member;
		// the 3F001 that stood here was invented and never matched, so an
		// AGE-less cluster was misclassified as a real cypher failure.
		"3F000",
		"42P01": // undefined_table
		return true
	}
	return false
}
