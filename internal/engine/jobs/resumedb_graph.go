package jobs

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// --- AGE Graph Helpers (account-scoped, plan ADR-8) ---
//
// Every resume_graph node and edge carries an `aid` property bound to the
// owning panel_accounts.id. The bound account's UUID is interpolated via
// a.aidStr() — uuid.UUID renders in a fixed canonical format, so no quoting
// risk; readable/writable account scoping is guaranteed by the type system
// (all methods hang off ResumeAccount, never ResumeDB). MERGE keys include
// `aid`, so nodes from another account are unreachable and an account-scoped
// ClearGraph can never delete a neighbour's subgraph.

// UpsertGraphNode merges (label, id) under the bound account: the MERGE key
// includes aid so a same-labelled node belonging to another account is a
// distinct vertex.
func (a *ResumeAccount) UpsertGraphNode(ctx context.Context, label string, id int, props map[string]string) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	conn, err := a.db.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return fmt.Errorf("age setup: %w", err)
	}

	var setParts []string
	for k, v := range props {
		setParts = append(setParts, fmt.Sprintf("n.%s = '%s'", escapeCypher(k), escapeCypher(v)))
	}
	setClause := ""
	if len(setParts) > 0 {
		setClause = "SET " + strings.Join(setParts, ", ")
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MERGE (n:%s {id: %d, aid: '%s'})
			%s
			RETURN n
		$$) AS (n ag_catalog.agtype)`,
		label, id, a.aidStr(), setClause,
	)
	if _, err := conn.Exec(ctx, cypher); err != nil {
		return fmt.Errorf("upsert node %s:%d: %w", label, id, err)
	}
	return nil
}

// UpsertGraphEdge merges an edge between two of the bound account's nodes and
// stamps the edge's aid property; endpoints outside the account do not match.
func (a *ResumeAccount) UpsertGraphEdge(ctx context.Context, fromLabel string, fromID int, edgeLabel string, toLabel string, toID int) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	conn, err := a.db.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return fmt.Errorf("age setup: %w", err)
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (a:%s {id: %d, aid: '%s'}), (b:%s {id: %d, aid: '%s'})
			MERGE (a)-[e:%s]->(b)
			SET e.aid = '%s'
		$$) AS (result ag_catalog.agtype)`,
		fromLabel, fromID, a.aidStr(), toLabel, toID, a.aidStr(), edgeLabel, a.aidStr(),
	)
	if _, err := conn.Exec(ctx, cypher); err != nil {
		return fmt.Errorf("upsert edge %s:%d->%s->%s:%d: %w", fromLabel, fromID, edgeLabel, toLabel, toID, err)
	}
	return nil
}

// ClearGraph deletes only the bound account's subgraph (nodes tagged aid and
// their edges). Another account's graph — and any never-migrated aid-less
// legacy vertices — survive untouched.
func (a *ResumeAccount) ClearGraph(ctx context.Context) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	conn, err := a.db.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return fmt.Errorf("age setup: %w", err)
	}

	cypher := fmt.Sprintf(`SELECT * FROM ag_catalog.cypher('resume_graph', $$
		MATCH (n) WHERE n.aid = '%s' DETACH DELETE n
	$$) AS (result ag_catalog.agtype)`, a.aidStr())
	_, err = conn.Exec(ctx, cypher)
	return err
}

// QueryExperienceIDsBySkill finds the bound account's experience IDs linked to
// a skill name via the graph.
func (a *ResumeAccount) QueryExperienceIDsBySkill(ctx context.Context, skillName string) ([]int, error) {
	conn, err := a.db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return nil, fmt.Errorf("age setup: %w", err)
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (e:Exp {aid: '%s'})-[:USED_SKILL]->(s:Skill {name: '%s', aid: '%s'})
			RETURN e.id
		$$) AS (id ag_catalog.agtype)`, a.aidStr(), escapeCypher(skillName), a.aidStr())

	rows, err := conn.Query(ctx, cypher)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanAGEIntIDs(rows)
}

// QueryProjectIDsBySkill finds the bound account's project IDs linked to a
// skill name via the graph.
func (a *ResumeAccount) QueryProjectIDsBySkill(ctx context.Context, skillName string) ([]int, error) {
	conn, err := a.db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return nil, fmt.Errorf("age setup: %w", err)
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (p:Proj {aid: '%s'})-[:USED_SKILL]->(s:Skill {name: '%s', aid: '%s'})
			RETURN p.id
		$$) AS (id ag_catalog.agtype)`, a.aidStr(), escapeCypher(skillName), a.aidStr())

	rows, err := conn.Query(ctx, cypher)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanAGEIntIDs(rows)
}

// QueryAchievementIDsByExperience finds achievement IDs produced by one of the
// bound account's experiences.
func (a *ResumeAccount) QueryAchievementIDsByExperience(ctx context.Context, expID int) ([]int, error) {
	conn, err := a.db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return nil, fmt.Errorf("age setup: %w", err)
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (e:Exp {id: %d, aid: '%s'})-[:PRODUCED]->(ach:Achv {aid: '%s'})
			RETURN ach.id
		$$) AS (id ag_catalog.agtype)`, expID, a.aidStr(), a.aidStr())

	rows, err := conn.Query(ctx, cypher)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanAGEIntIDs(rows)
}

// --- Extended Graph Queries ---

// QueryExperienceIDsByDomain finds the bound account's experience IDs linked
// to a domain via the graph.
func (a *ResumeAccount) QueryExperienceIDsByDomain(ctx context.Context, domain string) ([]int, error) {
	conn, err := a.db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return nil, fmt.Errorf("age setup: %w", err)
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (e:Exp {aid: '%s'})-[:IN_DOMAIN]->(d:Domain {name: '%s', aid: '%s'})
			RETURN e.id
		$$) AS (id ag_catalog.agtype)`, a.aidStr(), escapeCypher(domain), a.aidStr())

	rows, err := conn.Query(ctx, cypher)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAGEIntIDs(rows)
}

// QueryImpliedSkillIDs returns the bound account's skill IDs reachable via
// 1-hop IMPLIES_SKILL from skillID.
func (a *ResumeAccount) QueryImpliedSkillIDs(ctx context.Context, skillID int) ([]int, error) {
	conn, err := a.db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return nil, fmt.Errorf("age setup: %w", err)
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (s:Skill {id: %d, aid: '%s'})-[:IMPLIES_SKILL]->(t:Skill {aid: '%s'})
			RETURN t.id
		$$) AS (id ag_catalog.agtype)`, skillID, a.aidStr(), a.aidStr())

	rows, err := conn.Query(ctx, cypher)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAGEIntIDs(rows)
}

// QuerySubProjectIDs returns the bound account's project IDs linked to an
// experience via PART_OF.
func (a *ResumeAccount) QuerySubProjectIDs(ctx context.Context, expID int) ([]int, error) {
	conn, err := a.db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return nil, fmt.Errorf("age setup: %w", err)
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (p:Proj {aid: '%s'})-[:PART_OF]->(e:Exp {id: %d, aid: '%s'})
			RETURN p.id
		$$) AS (id ag_catalog.agtype)`, a.aidStr(), expID, a.aidStr())

	rows, err := conn.Query(ctx, cypher)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAGEIntIDs(rows)
}

// TrajectoryEdge represents a career evolution edge.
type TrajectoryEdge struct {
	FromExpID int    `json:"from_exp_id"`
	ToExpID   int    `json:"to_exp_id"`
	FromTitle string `json:"from_title"`
	ToTitle   string `json:"to_title"`
}

// QueryCareerTrajectory returns EVOLVED_TO edges inside the bound account's
// career graph.
func (a *ResumeAccount) QueryCareerTrajectory(ctx context.Context, personID int) ([]TrajectoryEdge, error) {
	conn, err := a.db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return nil, fmt.Errorf("age setup: %w", err)
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (x:Exp {aid: '%s'})-[:EVOLVED_TO]->(y:Exp {aid: '%s'})
			RETURN x.id, y.id, x.title, y.title
		$$) AS (from_id ag_catalog.agtype, to_id ag_catalog.agtype, from_title ag_catalog.agtype, to_title ag_catalog.agtype)`,
		a.aidStr(), a.aidStr())

	rows, err := conn.Query(ctx, cypher)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var edges []TrajectoryEdge
	for rows.Next() {
		var fID, tID, fTitle, tTitle string
		if err := rows.Scan(&fID, &tID, &fTitle, &tTitle); err != nil {
			continue
		}
		var e TrajectoryEdge
		_, _ = fmt.Sscanf(strings.TrimSpace(fID), "%d", &e.FromExpID)
		_, _ = fmt.Sscanf(strings.TrimSpace(tID), "%d", &e.ToExpID)
		e.FromTitle = strings.Trim(strings.TrimSpace(fTitle), `"`)
		e.ToTitle = strings.Trim(strings.TrimSpace(tTitle), `"`)
		edges = append(edges, e)
	}
	return edges, rows.Err()
}

// QuerySkillIDByName returns the skill ID for a given name under a person
// owned by the bound account, or 0 if not found / foreign / Nil account.
func (a *ResumeAccount) QuerySkillIDByName(ctx context.Context, personID int, skillName string) int {
	var id int
	err := a.db.pool.QueryRow(ctx,
		`SELECT s.id FROM resume_skills s
		 WHERE s.person_id = $1 AND LOWER(s.name) = LOWER($2)
		   AND EXISTS (SELECT 1 FROM resume_persons WHERE id = $1 AND account_id = $3)`,
		personID, skillName, a.aid,
	).Scan(&id)
	if err != nil {
		return 0
	}
	return id
}

// CountGraphNodes returns the number of nodes owned by the bound account.
func (a *ResumeAccount) CountGraphNodes(ctx context.Context) (int, error) {
	conn, err := a.db.pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return 0, err
	}

	cypher := fmt.Sprintf(`SELECT * FROM ag_catalog.cypher('resume_graph', $$
		MATCH (n) WHERE n.aid = '%s' RETURN count(n)
	$$) AS (count ag_catalog.agtype)`, a.aidStr())

	var raw string
	if err := conn.QueryRow(ctx, cypher).Scan(&raw); err != nil {
		return 0, err
	}
	var count int
	_, _ = fmt.Sscanf(strings.TrimSpace(raw), "%d", &count)
	return count, nil
}

// CountGraphEdges returns the number of edges owned by the bound account.
func (a *ResumeAccount) CountGraphEdges(ctx context.Context) (int, error) {
	conn, err := a.db.pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return 0, err
	}

	cypher := fmt.Sprintf(`SELECT * FROM ag_catalog.cypher('resume_graph', $$
		MATCH ()-[r]->() WHERE r.aid = '%s' RETURN count(r)
	$$) AS (count ag_catalog.agtype)`, a.aidStr())

	var raw string
	if err := conn.QueryRow(ctx, cypher).Scan(&raw); err != nil {
		return 0, err
	}
	var count int
	_, _ = fmt.Sscanf(strings.TrimSpace(raw), "%d", &count)
	return count, nil
}

// scanAGEIntIDs scans agtype integer results into []int.
func scanAGEIntIDs(rows pgx.Rows) ([]int, error) {
	var ids []int
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		var id int
		if _, err := fmt.Sscanf(strings.TrimSpace(raw), "%d", &id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// escapeCypher escapes a string for safe use in a single-quoted Cypher literal.
// Account ids are NOT passed through here — they are uuid.UUID values rendered
// by aidStr(), whose canonical format cannot inject cypher syntax.
func escapeCypher(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "`", "\\`")
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	return s
}
