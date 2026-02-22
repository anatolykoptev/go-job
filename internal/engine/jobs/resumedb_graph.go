package jobs

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// --- AGE Graph Helpers ---

func (db *ResumeDB) UpsertGraphNode(ctx context.Context, label string, id int, props map[string]string) error {
	conn, err := db.pool.Acquire(ctx)
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
			MERGE (n:%s {id: %d})
			%s
			RETURN n
		$$) AS (n ag_catalog.agtype)`,
		label, id, setClause,
	)
	if _, err := conn.Exec(ctx, cypher); err != nil {
		return fmt.Errorf("upsert node %s:%d: %w", label, id, err)
	}
	return nil
}

func (db *ResumeDB) UpsertGraphEdge(ctx context.Context, fromLabel string, fromID int, edgeLabel string, toLabel string, toID int) error {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return fmt.Errorf("age setup: %w", err)
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (a:%s {id: %d}), (b:%s {id: %d})
			MERGE (a)-[:%s]->(b)
		$$) AS (result ag_catalog.agtype)`,
		fromLabel, fromID, toLabel, toID, edgeLabel,
	)
	if _, err := conn.Exec(ctx, cypher); err != nil {
		return fmt.Errorf("upsert edge %s:%d->%s->%s:%d: %w", fromLabel, fromID, edgeLabel, toLabel, toID, err)
	}
	return nil
}

// ClearGraph removes all nodes and edges from the resume_graph.
func (db *ResumeDB) ClearGraph(ctx context.Context) error {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return fmt.Errorf("age setup: %w", err)
	}

	cypher := `SELECT * FROM ag_catalog.cypher('resume_graph', $$
		MATCH (n) DETACH DELETE n
	$$) AS (result ag_catalog.agtype)`
	_, err = conn.Exec(ctx, cypher)
	return err
}

// QueryExperienceIDsBySkill finds experience IDs linked to a skill name via the graph.
func (db *ResumeDB) QueryExperienceIDsBySkill(ctx context.Context, skillName string) ([]int, error) {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return nil, fmt.Errorf("age setup: %w", err)
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (e:Exp)-[:USED_SKILL]->(s:Skill {name: '%s'})
			RETURN e.id
		$$) AS (id ag_catalog.agtype)`, escapeCypher(skillName))

	rows, err := conn.Query(ctx, cypher)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanAGEIntIDs(rows)
}

// QueryProjectIDsBySkill finds project IDs linked to a skill name via the graph.
func (db *ResumeDB) QueryProjectIDsBySkill(ctx context.Context, skillName string) ([]int, error) {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return nil, fmt.Errorf("age setup: %w", err)
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (p:Proj)-[:USED_SKILL]->(s:Skill {name: '%s'})
			RETURN p.id
		$$) AS (id ag_catalog.agtype)`, escapeCypher(skillName))

	rows, err := conn.Query(ctx, cypher)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanAGEIntIDs(rows)
}

// QueryAchievementIDsByExperience finds achievement IDs produced by an experience.
func (db *ResumeDB) QueryAchievementIDsByExperience(ctx context.Context, expID int) ([]int, error) {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return nil, fmt.Errorf("age setup: %w", err)
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (e:Exp {id: %d})-[:PRODUCED]->(a:Achv)
			RETURN a.id
		$$) AS (id ag_catalog.agtype)`, expID)

	rows, err := conn.Query(ctx, cypher)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanAGEIntIDs(rows)
}

// --- Extended Graph Queries ---

// QueryExperienceIDsByDomain finds experience IDs linked to a domain via the graph.
func (db *ResumeDB) QueryExperienceIDsByDomain(ctx context.Context, domain string) ([]int, error) {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return nil, fmt.Errorf("age setup: %w", err)
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (e:Exp)-[:IN_DOMAIN]->(d:Domain {name: '%s'})
			RETURN e.id
		$$) AS (id ag_catalog.agtype)`, escapeCypher(domain))

	rows, err := conn.Query(ctx, cypher)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAGEIntIDs(rows)
}

// QueryImpliedSkillIDs returns skill IDs reachable via 1-hop IMPLIES_SKILL from skillID.
func (db *ResumeDB) QueryImpliedSkillIDs(ctx context.Context, skillID int) ([]int, error) {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return nil, fmt.Errorf("age setup: %w", err)
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (s:Skill {id: %d})-[:IMPLIES_SKILL]->(t:Skill)
			RETURN t.id
		$$) AS (id ag_catalog.agtype)`, skillID)

	rows, err := conn.Query(ctx, cypher)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAGEIntIDs(rows)
}

// QuerySubProjectIDs returns project IDs linked to an experience via PART_OF.
func (db *ResumeDB) QuerySubProjectIDs(ctx context.Context, expID int) ([]int, error) {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return nil, fmt.Errorf("age setup: %w", err)
	}

	cypher := fmt.Sprintf(`
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (p:Proj)-[:PART_OF]->(e:Exp {id: %d})
			RETURN p.id
		$$) AS (id ag_catalog.agtype)`, expID)

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

// QueryCareerTrajectory returns EVOLVED_TO edges for a person's career graph.
func (db *ResumeDB) QueryCareerTrajectory(ctx context.Context, personID int) ([]TrajectoryEdge, error) {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return nil, fmt.Errorf("age setup: %w", err)
	}

	cypher := `
		SELECT * FROM ag_catalog.cypher('resume_graph', $$
			MATCH (a:Exp)-[:EVOLVED_TO]->(b:Exp)
			RETURN a.id, b.id, a.title, b.title
		$$) AS (from_id ag_catalog.agtype, to_id ag_catalog.agtype, from_title ag_catalog.agtype, to_title ag_catalog.agtype)`

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

// QuerySkillIDByName returns the skill ID for a given name, or 0 if not found.
func (db *ResumeDB) QuerySkillIDByName(ctx context.Context, personID int, skillName string) int {
	var id int
	err := db.pool.QueryRow(ctx,
		`SELECT id FROM resume_skills WHERE person_id = $1 AND LOWER(name) = LOWER($2)`,
		personID, skillName,
	).Scan(&id)
	if err != nil {
		return 0
	}
	return id
}

// CountGraphNodes returns the total number of nodes in the resume graph.
func (db *ResumeDB) CountGraphNodes(ctx context.Context) (int, error) {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return 0, err
	}

	cypher := `SELECT * FROM ag_catalog.cypher('resume_graph', $$
		MATCH (n) RETURN count(n)
	$$) AS (count ag_catalog.agtype)`

	var raw string
	if err := conn.QueryRow(ctx, cypher).Scan(&raw); err != nil {
		return 0, err
	}
	var count int
	_, _ = fmt.Sscanf(strings.TrimSpace(raw), "%d", &count)
	return count, nil
}

// CountGraphEdges returns the total number of edges in the resume graph.
func (db *ResumeDB) CountGraphEdges(ctx context.Context) (int, error) {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, ageSetup); err != nil {
		return 0, err
	}

	cypher := `SELECT * FROM ag_catalog.cypher('resume_graph', $$
		MATCH ()-[r]->() RETURN count(r)
	$$) AS (count ag_catalog.agtype)`

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
