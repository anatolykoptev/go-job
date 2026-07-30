package jobs

// resume_profile_sync.go — re-derives the structured-profile resume_vectors
// rows (experiences, projects, achievements) from their current DB state.
//
// Invariant: after any profile mutation completes, the derived vector rows for
// the affected entity reflect the new content — created on insert, updated on
// change, removed on delete. Derived rows carry source='profile' and the entity
// id as ref_id, distinguishing them from source='agent' manual memories.
//
// Manual source='agent' rows are never touched: the sync only lists, upserts
// and deletes source='profile' rows scoped to the derived mem_types.
//
// Reuse: the single write path is UpsertVectorWithSource (same SQL + content-
// hash dedup as manual adds). No side table, no second write path.
//
// Embedding failure degrades, not aborts: a profile update still persists when
// the embedder is unreachable — the derived row is stored with a NULL embedding
// for a later backfill, and SyncProfileVectors never returns an embed error.
//
// No-op on unchanged: rows whose derived content matches the existing row are
// skipped before upsert, so re-running the sync with unchanged data produces no
// duplicate rows and no updated_at churn.

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
)

// derivedMemTypes is the set of mem_types SyncProfileVectors owns. enrich_project
// is intentionally excluded — it is managed by resume_enrich, not the profile
// sync, so the sync never deletes or rewrites enrich_project rows.
var derivedMemTypes = []string{memTypeResumeExp, memTypeResumeProj, memTypeResumeAchv}

// derivedEntry is a desired derived vector row computed from an entity.
type derivedEntry struct {
	memType string
	refID   int64
	content string
}

// SyncProfileVectors re-derives the resume_vectors rows for the structured
// profile entities (experiences, projects, achievements) from their current DB
// state and upserts them with source='profile' and the entity id as ref_id.
// Orphaned derived rows (entity deleted) are removed. Manual source='agent'
// rows are never touched.
//
// Embedding failure degrades: the row is upserted with a NULL embedding for a
// later backfill; the sync never aborts on an embedder outage. Re-running with
// unchanged content is a no-op (unchanged rows are skipped before upsert, so
// updated_at is not churned).
//
// Returns an error only on structural DB read/write failures (not embed
// failures). Callers that have already persisted a profile mutation should
// treat a non-nil error as best-effort (log + continue) — the mutation itself
// is not rolled back.
func SyncProfileVectors(ctx context.Context, personID int) error {
	db := GetResumeDB()
	if db == nil {
		// No vector store configured — nothing to sync. The profile mutation
		// itself is unaffected.
		return nil
	}

	desired, err := buildDerivedEntries(ctx, db, personID)
	if err != nil {
		return err
	}

	// Index existing source='profile' derived rows by (mem_type, ref_id) →
	// content + count, so unchanged single rows can be skipped (no-op, no
	// updated_at churn) while duplicate stale rows are always reconciled.
	existing, err := db.ListDerivedVectors(ctx, derivedMemTypes)
	if err != nil {
		return err
	}
	type existingDerived struct {
		content string
		count   int
	}
	existingRows := make(map[string]existingDerived, len(existing))
	for _, r := range existing {
		if r.RefID == nil {
			continue
		}
		key := derivedKey(r.MemType, *r.RefID)
		e := existingRows[key]
		e.content = r.Content
		e.count++
		existingRows[key] = e
	}

	// keepIDs[memType] = the entity ids that still exist → used for orphan delete.
	keepIDs := map[string][]int64{
		memTypeResumeExp:  nil,
		memTypeResumeProj: nil,
		memTypeResumeAchv: nil,
	}

	for _, e := range desired {
		keepIDs[e.memType] = append(keepIDs[e.memType], e.refID)

		key := derivedKey(e.memType, e.refID)
		if prev, ok := existingRows[key]; ok && prev.count == 1 && prev.content == e.content {
			// Unchanged content, single row — skip the upsert entirely so
			// updated_at is not churned and an embedder outage cannot degrade
			// a good row.
			continue
		}

		// Content changed, new, or duplicate stale rows exist — reconcile by
		// identity (mem_type, ref_id), not content hash. Delete all
		// source='profile' rows for this identity first, then insert the
		// current content. Delete-then-insert (not update-in-place) because it
		// also cleans up pre-existing duplicate rows that update-in-place would
		// leave behind. A failed insert leaves a gap that the next sync fills;
		// stale content (the alternative) serves wrong search results.
		if err := db.DeleteDerivedVectorByID(ctx, e.memType, e.refID); err != nil {
			slog.Warn("profile_sync: stale derived delete failed",
				slog.String("mem_type", e.memType), slog.Int64("ref_id", e.refID), slog.Any("error", err))
			continue
		}

		// Embedding failure degrades: embedPassage returns (nil, backendFTS) on
		// any embedder error / dim mismatch / non-finite vector, so the row is
		// stored FTS-only (embedding NULL) for a later backfill.
		embedding, _ := embedPassage(ctx, db, e.content, "profile_sync")
		if _, err := db.UpsertVectorWithSource(ctx, e.content, e.memType, &e.refID, embedding, sourceProfile); err != nil {
			slog.Warn("profile_sync: upsert derived vector failed",
				slog.String("mem_type", e.memType), slog.Int64("ref_id", e.refID), slog.Any("error", err))
			// best-effort: continue syncing the rest
		}
	}

	// Remove orphaned derived rows (entity deleted). Scoped to source='profile'
	// AND each derived mem_type — manual source='agent' rows and other
	// consumers' mem_types (enrich_project) are never deleted.
	for _, mt := range derivedMemTypes {
		if err := db.DeleteDerivedVectorsNotIn(ctx, mt, keepIDs[mt]); err != nil {
			slog.Warn("profile_sync: orphan delete failed",
				slog.String("mem_type", mt), slog.Any("error", err))
		}
	}
	return nil
}

// buildDerivedEntries reads the structured profile entities and computes the
// derived vector content for each, reusing the same formatters as master_resume
// so a full rebuild and an incremental sync produce identical text.
func buildDerivedEntries(ctx context.Context, db *ResumeDB, personID int) ([]derivedEntry, error) {
	var entries []derivedEntry

	exps, err := db.GetAllExperiences(ctx, personID)
	if err != nil {
		return nil, err
	}
	for _, exp := range exps {
		// GetAllExperiences returns the extended domain field, so the synced
		// experience text is byte-identical to what master_resume builds from
		// the parsed resume (formatExperienceTextExtended with exp.Domain) —
		// same content_hash, so the ON CONFLICT dedup converges instead of
		// inserting a duplicate row for the same ref_id.
		entries = append(entries, derivedEntry{
			memType: memTypeResumeExp,
			refID:   int64(exp.ID),
			content: formatExperienceTextExtended(exp.Title, exp.Company, exp.StartDate, exp.EndDate, exp.Description, exp.Highlights, exp.Domain),
		})
	}

	projs, err := db.GetAllProjects(ctx, personID)
	if err != nil {
		return nil, err
	}
	for _, proj := range projs {
		entries = append(entries, derivedEntry{
			memType: memTypeResumeProj,
			refID:   int64(proj.ID),
			content: formatProjectText(proj.Name, proj.Description, proj.Tech, proj.Highlights),
		})
	}

	achs, err := db.GetAllAchievements(ctx, personID)
	if err != nil {
		return nil, err
	}
	for _, ach := range achs {
		entries = append(entries, derivedEntry{
			memType: memTypeResumeAchv,
			refID:   int64(ach.ID),
			content: ach.Text,
		})
	}
	return entries, nil
}

func derivedKey(memType string, refID int64) string {
	return memType + ":" + strconv.FormatInt(refID, 10)
}

// ResumeProfileSyncResult is the output of the resume_profile_sync MCP tool.
type ResumeProfileSyncResult struct {
	PersonID int    `json:"person_id"`
	Synced   int    `json:"synced"`
	Orphaned int    `json:"orphaned_removed"`
	Total    int    `json:"total_derived"`
	Status   string `json:"status"`
}

// SyncProfileVectorsReported is SyncProfileVectors exposed through the existing
// MCP tool surface (resume_profile_sync) for a full re-sync / embedding backfill
// without re-running the LLM-based master_resume_build. It re-derives the
// structured-profile vector rows from the current entity state and reports
// counts. Embedding failure degrades (NULL embedding) and is not counted as an
// error — run this after an embedder outage to backfill.
func SyncProfileVectorsReported(ctx context.Context, personID int) (*ResumeProfileSyncResult, error) {
	db := GetResumeDB()
	if db == nil {
		return nil, errors.New("resume DB not configured (set DATABASE_URL)")
	}

	before, err := db.ListDerivedVectors(ctx, derivedMemTypes)
	if err != nil {
		return nil, err
	}
	beforeCount := make(map[string]int, len(derivedMemTypes))
	for _, r := range before {
		beforeCount[r.MemType]++
	}

	if err := SyncProfileVectors(ctx, personID); err != nil {
		return nil, err
	}

	after, err := db.ListDerivedVectors(ctx, derivedMemTypes)
	if err != nil {
		return nil, err
	}
	afterByMemType := make(map[string]int, len(derivedMemTypes))
	total := 0
	for _, r := range after {
		afterByMemType[r.MemType]++
		total++
	}
	orphaned := 0
	for _, mt := range derivedMemTypes {
		if d := beforeCount[mt] - afterByMemType[mt]; d > 0 {
			orphaned += d
		}
	}

	return &ResumeProfileSyncResult{
		PersonID: personID,
		Synced:   total,
		Orphaned: orphaned,
		Total:    total,
		Status:   "synced",
	}, nil
}
