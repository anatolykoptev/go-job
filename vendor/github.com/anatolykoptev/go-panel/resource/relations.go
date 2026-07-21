package resource

import (
	"context"
	"fmt"
	"log/slog"
)

// Relation declares a BelongsTo relationship from this Resource's row to another
// resource's row. For P1 only BelongsTo is supported (no Type enum — ADR-1).
//
// ForeignKey is the Column.Key on THIS resource whose cell value carries the
// target resource's primary key. DisplayKey is the key in the target
// resource's FetchRow-returned map whose value is used as the human-readable
// label for the cross-link anchor (e.g. "name").
//
// ResolveLabels is the PRIMARY, recommended resolution path: a batch closure
// that receives all FK ids for the current list page and returns a map of
// id-to-display-value. When nil, the framework falls back to per-id FetchRow
// on the target resource, gated by ADR-3 (target must declare a non-empty
// Scope AND total calls stay at or below FALLBACK_CAP).
//
// Tenant scoping is the consumer's responsibility: ResolveLabels and FetchRow
// both receive ctx and are expected to read tenant.From(ctx) themselves
// (ADR-7), same as Lister.
type Relation struct {
	Resource      string
	ForeignKey    string
	DisplayKey    string
	ResolveLabels func(ctx context.Context, ids []string) (map[string]string, error)
}

// FALLBACK_CAP bounds the total number of FetchRow invocations across all
// relations of a single resolveRelations call when ResolveLabels is nil
// (ADR-3 DoS cap). 50 rows x 1 relation = 50 calls = at cap.
const FALLBACK_CAP = 50

// resolveRelations batch-resolves each BelongsTo Relation declared on r,
// replacing raw-FK cells in rows with XSS-safe CrossLinkCell anchors. It is
// wired into makeListHandler (Phase 3a, resource.go).
//
// rows is mutated in place: matching FK cells are replaced with HTML anchor
// cells (Cell.HTML=true). Non-matching rows/cells are left untouched.
func resolveRelations(ctx context.Context, p *Panel, r *Resource, rows []Row) error {
	if len(r.Relations) == 0 {
		return nil
	}
	fallbackCalls := 0
	for _, rel := range r.Relations {
		resolveOneRelation(ctx, p, r, rows, rel, &fallbackCalls)
	}
	return nil
}

// resolveOneRelation handles a single Relation: locates the FK cell index,
// collects FK ids, resolves labels (batch or fallback), and replaces cells.
func resolveOneRelation(ctx context.Context, p *Panel, r *Resource, rows []Row, rel Relation, fallbackCalls *int) {
	fkIdx := findForeignKeyIndex(r, rel)
	if fkIdx < 0 {
		return
	}
	refs, ids := collectFKRefs(rows, fkIdx)
	if len(refs) == 0 {
		return
	}
	labels := resolveLabels(ctx, p, r, rel, ids, fallbackCalls)
	replaceFKCells(rows, refs, fkIdx, labels, p, rel)
}

// findForeignKeyIndex returns the cell index for rel.ForeignKey on r, or -1.
func findForeignKeyIndex(r *Resource, rel Relation) int {
	for i, col := range r.Sort.Columns {
		if col.Key == rel.ForeignKey {
			return i
		}
	}
	return -1
}

// rowRef pairs a row index with its FK id for cell replacement after resolution.
type rowRef struct {
	rowIdx int
	fkID   string
}

// collectFKRefs walks rows and collects non-empty, non-"0" FK values
// (ADR-10 empty/zero skip) along with their row indices, plus a de-duplicated
// id list for batch lookup.
func collectFKRefs(rows []Row, fkIdx int) (refs []rowRef, ids []string) {
	seen := make(map[string]bool)
	for i := range rows {
		if fkIdx >= len(rows[i].Cells) {
			continue
		}
		fk := rows[i].Cells[fkIdx].Value
		if fk == "" || fk == "0" {
			continue
		}
		refs = append(refs, rowRef{i, fk})
		if !seen[fk] {
			seen[fk] = true
			ids = append(ids, fk)
		}
	}
	return refs, ids
}

// resolveLabels returns id-to-display-value map. PRIMARY path is rel.ResolveLabels
// (batch, ADR-2). Fallback path uses target.FetchRow gated by ADR-3.
func resolveLabels(ctx context.Context, p *Panel, r *Resource, rel Relation, ids []string, fallbackCalls *int) map[string]string {
	if rel.ResolveLabels != nil {
		got, err := rel.ResolveLabels(ctx, ids)
		if err != nil {
			slog.Warn("resource: resolve relation failed", "resource", r.Name, "relation", rel.Resource, "err", err)
			return nil
		}
		return got
	}
	return resolveViaFetchRow(ctx, p, r, rel, ids, fallbackCalls)
}

// resolveViaFetchRow is the ADR-3 gated fallback: target must have non-empty
// Scope (tenant enforcement proof) AND total calls must stay at or below
// FALLBACK_CAP. Returns nil on any gate failure (caller leaves raw FK).
func resolveViaFetchRow(ctx context.Context, p *Panel, r *Resource, rel Relation, ids []string, fallbackCalls *int) map[string]string {
	target := findResource(p, rel.Resource)
	if target == nil {
		slog.Warn("resource: resolve relation failed", "resource", r.Name, "relation", rel.Resource, "err", fmt.Errorf("target resource %q not registered", rel.Resource))
		return nil
	}
	if target.FetchRow == nil {
		slog.Warn("resource: resolve relation failed", "resource", r.Name, "relation", rel.Resource, "err", fmt.Errorf("target resource %q has no FetchRow", rel.Resource))
		return nil
	}
	if target.Scope.Column == "" {
		slog.Warn("resource: resolve relation failed", "resource", r.Name, "relation", rel.Resource, "err", fmt.Errorf("target resource %q has empty Scope (ADR-3 tenant gate)", rel.Resource))
		return nil
	}
	if *fallbackCalls+len(ids) > FALLBACK_CAP {
		slog.Warn("resource: resolve relation failed", "resource", r.Name, "relation", rel.Resource, "err", fmt.Errorf("fallback cap %d exceeded (ADR-3 DoS cap)", FALLBACK_CAP))
		return nil
	}
	labels := make(map[string]string)
	for _, id := range ids {
		row, err := target.FetchRow(ctx, id)
		if err != nil {
			slog.Warn("resource: resolve relation failed", "resource", r.Name, "relation", rel.Resource, "err", err)
			continue
		}
		*fallbackCalls++
		if v, ok := row[rel.DisplayKey]; ok && v != "" {
			labels[id] = v
		}
	}
	return labels
}

// replaceFKCells mutates rows: for each ref with a resolved display value,
// replaces the FK cell with a CrossLinkCell anchor (Cell.HTML=true). Missing
// display values leave the raw FK in place (ADR-9 graceful degradation).
func replaceFKCells(rows []Row, refs []rowRef, fkIdx int, labels map[string]string, p *Panel, rel Relation) {
	for _, ref := range refs {
		disp, ok := labels[ref.fkID]
		if !ok || disp == "" {
			continue
		}
		rows[ref.rowIdx].Cells[fkIdx] = Cell{
			Value: CrossLinkCell(p.basePath, rel.Resource, ref.fkID, disp),
			HTML:  true,
		}
	}
}

// findResource returns a pointer to the registered Resource named name, or
// nil when no such resource is registered. Used by resolveRelations for the
// FetchRow fallback path (ADR-3). Target-existence is checked at runtime
// (first list request), not at Register time, to avoid order-dependency and
// impossible circular registrations (ADR-6).
func findResource(p *Panel, name string) *Resource {
	for i := range p.resources {
		if p.resources[i].Name == name {
			return &p.resources[i]
		}
	}
	return nil
}

// validateRelationsConfig panics at Register time if any Relation.ForeignKey
// does not match a Sort.Columns[].Key on the SAME resource (ADR-6
// self-contained validation). Target-resource-existence is intentionally NOT
// checked here (deferred to runtime — ADR-6).
func validateRelationsConfig(r *Resource) {
	for _, rel := range r.Relations {
		if findForeignKeyIndex(r, rel) < 0 {
			panic(fmt.Sprintf("resource.Register %q: Relation.ForeignKey %q does not match any Sort.Columns key", r.Name, rel.ForeignKey))
		}
	}
}
