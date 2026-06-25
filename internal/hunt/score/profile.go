// Package score provides the ScoringProfile struct and loader for the
// fit-scoring layer (Phase 2 of the hunt fit-scoring plan).
//
// Import-cycle guard: this package imports only stdlib, pgx, and go-kit/env.
// It does NOT import internal/engine/jobs because engine/jobs already imports
// internal/hunt — importing engine/jobs here would create:
//
//	internal/hunt/score → internal/engine/jobs → internal/hunt  (CYCLE)
//
// The resume_profile tables are queried directly via *pgxpool.Pool, which is
// the same pool passed to hunt.NewStore. The caller (huntworker) owns the
// pool and injects it here.
package score

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ScoringProfile is the compact, structured profile the LLM scorer needs to
// evaluate fit against a job posting. It is intentionally minimal — only the
// fields the scorer and gate logic actually consume.
//
// Loaded from the resume_profile DB (primary), a JSON file pointed to by
// HUNT_SCORE_PROFILE_PATH (fallback), or disabled (nil return, no error).
type ScoringProfile struct {
	// Seniority is the operator's target level (e.g. "Staff", "Principal").
	Seniority string `json:"seniority"`

	// TargetDomains are the tech domains the operator wants to work in.
	// Marketing/event/media domains are filtered out at load time.
	TargetDomains []string `json:"target_domains"`

	// CoreSkills are tech skills (category ∈ programming_language|database|devops|other)
	// at level advanced|expert. These are the primary matching signals.
	CoreSkills []string `json:"core_skills"`

	// AdjacentSkills are tech skills at level intermediate. Lower weight than CoreSkills.
	AdjacentSkills []string `json:"adjacent_skills"`

	// CompFloorUSD is the minimum acceptable total compensation in USD.
	CompFloorUSD int `json:"comp_floor_usd"`

	// Locations are preferred work locations (e.g. "San Francisco Bay Area", "Remote (US)").
	Locations []string `json:"locations"`

	// WorkAuth describes authorization status (e.g. "US authorized, no sponsorship").
	WorkAuth string `json:"work_auth"`

	// AvoidSignals are lowercase substrings of job titles/descriptions that indicate
	// a role the operator wants to avoid (e.g. "sales", "marketing", "event").
	AvoidSignals []string `json:"avoid_signals"`
}

// Default env-based values used when the corresponding HUNT_SCORE_* var is unset or empty.
const (
	defaultSeniority = "Staff"
	defaultCompFloor = 250000
	defaultWorkAuth  = "US authorized, no sponsorship"
)

var defaultLocations = []string{"San Francisco Bay Area", "Remote (US)"}
var defaultAvoidSignals = []string{"sales", "marketing", "event", "management", "recruiting"}

// techSkillCategories is the allowlist of resume_skills.category values that
// count as tech skills for scoring. methodology and soft_skill are excluded
// (they pollute the tech fit signal with noise like "Agile", "Public Speaking").
var techSkillCategories = map[string]bool{
	"programming_language": true,
	"database":             true,
	"devops":               true,
	"other":                true,
}

// coreLevels are the resume_skills.level values that map to CoreSkills.
var coreLevels = map[string]bool{
	"advanced": true,
	"expert":   true,
}

// noiseDomains is the blocklist of domain name substrings (lowercased) that
// pollute the TargetDomains set with marketing/event/media noise.
var noiseDomains = []string{
	"media",
	"digital marketing",
	"event production",
	"event management",
	"marketing",
}

// LoadProfile loads a ScoringProfile using the following precedence:
//
//  1. PRIMARY: read the latest resume_profile from PostgreSQL (resume_persons,
//     resume_skills, resume_domains). Requires pool to be non-nil and to have rows.
//     ENV vars override the computed fields (see below).
//  2. FALLBACK: if pool is nil OR DB has no person rows, AND HUNT_SCORE_PROFILE_PATH
//     is set, load the JSON file at that path. ENV vars still override.
//  3. DISABLED: if neither source is available, return (nil, nil) and log a
//     WARN. Callers MUST treat nil profile as "scoring disabled, gate open".
//     This NEVER errors — a missing profile should not break the ingest pipeline.
//
// ENV overrides (all take precedence over DB/file if non-empty):
//
//   - HUNT_SCORE_SENIORITY      (default: "Staff")
//   - HUNT_SCORE_COMP_FLOOR     (default: 250000, USD int)
//   - HUNT_SCORE_LOCATIONS      (default: "San Francisco Bay Area,Remote (US)")
//   - HUNT_SCORE_WORK_AUTH      (default: "US authorized, no sponsorship")
//   - HUNT_SCORE_AVOID          (default: "sales,marketing,event,management,recruiting")
func LoadProfile(ctx context.Context, pool *pgxpool.Pool) (*ScoringProfile, error) {
	var prof *ScoringProfile

	// --- PRIMARY: DB ---
	if pool != nil {
		p, err := loadFromDB(ctx, pool)
		if err != nil {
			slog.WarnContext(ctx, "fit scoring: DB load error, trying fallback",
				slog.Any("error", err))
		} else {
			prof = p
		}
	}

	// --- FALLBACK: JSON file ---
	if prof == nil {
		path := os.Getenv("HUNT_SCORE_PROFILE_PATH")
		if path != "" {
			p, err := loadFromFile(path)
			if err != nil {
				return nil, fmt.Errorf("score: load profile from %q: %w", path, err)
			}
			prof = p
		}
	}

	// --- DISABLED ---
	if prof == nil {
		slog.WarnContext(ctx, "fit scoring disabled: no profile",
			slog.String("hint", "populate resume_profile via master_resume_build or set HUNT_SCORE_PROFILE_PATH"))
		return nil, nil
	}

	// Apply ENV overrides on top of whatever was loaded.
	applyEnvOverrides(prof)

	return prof, nil
}

// loadFromDB queries resume_persons, resume_skills, and resume_domains to build
// a ScoringProfile. Returns nil (not error) when no person row exists.
func loadFromDB(ctx context.Context, pool *pgxpool.Pool) (*ScoringProfile, error) {
	// Find the latest person.
	var personID int
	err := pool.QueryRow(ctx,
		`SELECT id FROM resume_persons ORDER BY id DESC LIMIT 1`,
	).Scan(&personID)
	if err != nil {
		// pgx.ErrNoRows: no person yet — not an error, just no data.
		return nil, nil //nolint:nilerr
	}
	if personID == 0 {
		return nil, nil
	}

	prof := &ScoringProfile{}

	// Load skills — filter by category and level.
	rows, err := pool.Query(ctx,
		`SELECT name, category, level FROM resume_skills
		 WHERE person_id = $1 AND category IS NOT NULL AND level IS NOT NULL`,
		personID,
	)
	if err != nil {
		return nil, fmt.Errorf("query resume_skills: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name, category, level string
		if err := rows.Scan(&name, &category, &level); err != nil {
			continue
		}
		if !techSkillCategories[category] {
			continue // exclude methodology, soft_skill
		}
		if isNoiseSkillName(name) {
			continue // name-level noise guard (e.g. "Event Production" in category=other)
		}
		if coreLevels[level] {
			prof.CoreSkills = append(prof.CoreSkills, name)
		} else if level == "intermediate" {
			prof.AdjacentSkills = append(prof.AdjacentSkills, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate resume_skills: %w", err)
	}

	// Load domains — filter noise.
	drows, err := pool.Query(ctx,
		`SELECT name FROM resume_domains WHERE person_id = $1`,
		personID,
	)
	if err != nil {
		return nil, fmt.Errorf("query resume_domains: %w", err)
	}
	defer drows.Close()

	for drows.Next() {
		var name string
		if err := drows.Scan(&name); err != nil {
			continue
		}
		if !isNoiseDomain(name) {
			prof.TargetDomains = append(prof.TargetDomains, name)
		}
	}
	if err := drows.Err(); err != nil {
		return nil, fmt.Errorf("iterate resume_domains: %w", err)
	}

	// If we got neither skills nor domains, treat as "no usable profile".
	if len(prof.CoreSkills) == 0 && len(prof.AdjacentSkills) == 0 && len(prof.TargetDomains) == 0 {
		return nil, nil
	}

	return prof, nil
}

// loadFromFile reads a JSON ScoringProfile from path.
func loadFromFile(path string) (*ScoringProfile, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is operator-controlled via HUNT_SCORE_PROFILE_PATH env var
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	var prof ScoringProfile
	if err := json.Unmarshal(data, &prof); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	return &prof, nil
}

// applyEnvOverrides replaces profile fields with values from HUNT_SCORE_* env vars
// when those vars are set and non-empty. This lets the operator tune scoring
// without re-populating the DB.
func applyEnvOverrides(prof *ScoringProfile) {
	if v := os.Getenv("HUNT_SCORE_SENIORITY"); v != "" {
		prof.Seniority = v
	} else if prof.Seniority == "" {
		prof.Seniority = defaultSeniority
	}

	if v := os.Getenv("HUNT_SCORE_COMP_FLOOR"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			prof.CompFloorUSD = n
		}
	} else if prof.CompFloorUSD == 0 {
		prof.CompFloorUSD = defaultCompFloor
	}

	if v := os.Getenv("HUNT_SCORE_LOCATIONS"); v != "" {
		prof.Locations = splitTrimmed(v)
	} else if len(prof.Locations) == 0 {
		prof.Locations = append([]string(nil), defaultLocations...)
	}

	if v := os.Getenv("HUNT_SCORE_WORK_AUTH"); v != "" {
		prof.WorkAuth = v
	} else if prof.WorkAuth == "" {
		prof.WorkAuth = defaultWorkAuth
	}

	if v := os.Getenv("HUNT_SCORE_AVOID"); v != "" {
		prof.AvoidSignals = splitTrimmed(v)
	} else if len(prof.AvoidSignals) == 0 {
		prof.AvoidSignals = append([]string(nil), defaultAvoidSignals...)
	}
}

// splitTrimmed splits a comma-separated string and trims spaces.
func splitTrimmed(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// isNoiseDomain returns true for domain names that contain marketing/event/media
// noise substrings (case-insensitive match).
func isNoiseDomain(name string) bool {
	lower := strings.ToLower(name)
	for _, noise := range noiseDomains {
		if strings.Contains(lower, noise) {
			return true
		}
	}
	return false
}

// isNoiseSkillName returns true for skill names that match noise keywords
// (event, marketing, media) regardless of category. This catches skills like
// "Event Production" that happen to be in category="other".
var noiseSkillKeywords = []string{"event", "marketing", "guerrilla", "festival", "media production"}

func isNoiseSkillName(name string) bool {
	lower := strings.ToLower(name)
	for _, kw := range noiseSkillKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
