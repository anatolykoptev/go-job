// hunt_settings.go — the per-account hunt worker settings surface (plan
// ADR-7). account_hunt_settings replaces the single-row hunt_settings table
// for every account-scoped knob; a missing row means DISABLED (a new account
// never silently arms a hunt — provisioning is explicit).
//
// The worker enumerates ACTIVE panel_accounts LEFT JOIN account_hunt_settings
// once per cycle (ListAccountHuntSettings); the admin resource reads/writes a
// single account's row through the account-bound facade
// (*hunt.AccountStore delegates here with the ForAccount-bound id — the
// unscoped statement shape stays absent from the store API, ADR-15).
package accounts

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AccountHuntSettings is one account's hunt worker configuration — the row
// shape of account_hunt_settings plus HasRow, which distinguishes "row
// absent" (LEFT JOIN miss → disabled, apply nothing) from "row present with
// all-zero columns" (a deliberately configured account). Bool and zero-value
// fields are authoritative ONLY when HasRow is true; env fallbacks apply
// per-field on top (huntworker.mergeAccountSettings), never to the
// Enabled/ScoreEnabled/ScoreFailOpen decisions themselves.
type AccountHuntSettings struct {
	AccountID uuid.UUID
	HasRow    bool
	// Enabled arms this account's ingest queries + scoring + notify. Row
	// absent or enabled=false → the account is skipped entirely.
	Enabled             bool
	Queries             string        // comma-separated role queries
	NotifyChatID        int64         // 0 = no Telegram notify for this account
	NotifyMinFit        int           // 0-100 fit gate, 0 = open
	NotifyMaxAge        time.Duration // recency gate for notify + scorer stale-reject
	ScoreEnabled        bool          // LLM scoring on for this account
	ScoreMinJaccard     int           // pre-filter threshold 0-100
	ScoreMaxLLMPerCycle *int          // NULL = fleet cap only (no sub-cap)
	ScoreFailOpen       bool          // notify with degraded card on LLM error
	UpdatedAt           time.Time
}

// ListAccountHuntSettings enumerates every ACTIVE panel_accounts row LEFT
// JOIN account_hunt_settings — the worker's per-cycle account census
// (ADR-7). Accounts with no settings row come back HasRow=false and are
// skipped by the caller; deactivated accounts are absent entirely (a
// deactivated account must not ingest, score, or notify).
func ListAccountHuntSettings(ctx context.Context, pool *pgxpool.Pool) ([]AccountHuntSettings, error) {
	rows, err := pool.Query(ctx, `
		SELECT a.id,
		       s.account_id IS NOT NULL,
		       COALESCE(s.enabled, false),
		       COALESCE(s.queries, ''),
		       COALESCE(s.notify_chat_id, 0),
		       COALESCE(s.notify_min_fit, 0),
		       COALESCE(s.notify_max_age_seconds, 0),
		       COALESCE(s.score_enabled, false),
		       COALESCE(s.score_min_jaccard, 0),
		       s.score_max_llm_per_cycle,
		       COALESCE(s.score_fail_open, false),
		       s.updated_at
		FROM panel_accounts a
		LEFT JOIN account_hunt_settings s ON s.account_id = a.id
		WHERE a.active
		ORDER BY a.created_at, a.id`)
	if err != nil {
		return nil, fmt.Errorf("accounts: list hunt settings: %w", err)
	}
	defer rows.Close()

	var out []AccountHuntSettings
	for rows.Next() {
		var (
			h         AccountHuntSettings
			maxAgeSec int
			maxLLM    *int
			updatedAt *time.Time
		)
		if err := rows.Scan(
			&h.AccountID, &h.HasRow, &h.Enabled, &h.Queries,
			&h.NotifyChatID, &h.NotifyMinFit, &maxAgeSec,
			&h.ScoreEnabled, &h.ScoreMinJaccard, &maxLLM,
			&h.ScoreFailOpen, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("accounts: list hunt settings scan: %w", err)
		}
		h.NotifyMaxAge = time.Duration(maxAgeSec) * time.Second
		h.ScoreMaxLLMPerCycle = maxLLM
		if updatedAt != nil {
			h.UpdatedAt = *updatedAt
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("accounts: list hunt settings rows: %w", err)
	}
	return out, nil
}

// GetAccountHuntSettings loads one account's settings row. Returns
// (zero-value, false, nil) when the account has no row — callers apply env
// fallbacks on top of the zero value exactly like the legacy GetHuntSettings
// contract. aid is never re-validated here: the caller bound it through
// ForAccount, and account_id is the row key so a foreign row is unreachable.
func GetAccountHuntSettings(ctx context.Context, pool *pgxpool.Pool, aid uuid.UUID) (AccountHuntSettings, bool, error) {
	var (
		h         AccountHuntSettings
		maxAgeSec int
	)
	err := pool.QueryRow(ctx, `
		SELECT enabled, queries, notify_chat_id, notify_min_fit,
		       notify_max_age_seconds, score_enabled, score_min_jaccard,
		       score_max_llm_per_cycle, score_fail_open, updated_at
		FROM account_hunt_settings WHERE account_id = $1`, aid,
	).Scan(
		&h.Enabled, &h.Queries, &h.NotifyChatID, &h.NotifyMinFit,
		&maxAgeSec, &h.ScoreEnabled, &h.ScoreMinJaccard,
		&h.ScoreMaxLLMPerCycle, &h.ScoreFailOpen, &h.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AccountHuntSettings{AccountID: aid}, false, nil
	}
	if err != nil {
		return AccountHuntSettings{}, false, fmt.Errorf("accounts: get hunt settings: %w", err)
	}
	h.AccountID = aid
	h.HasRow = true
	h.NotifyMaxAge = time.Duration(maxAgeSec) * time.Second
	return h, true, nil
}

// SaveAccountHuntSettings upserts one account's settings row. The write is
// keyed by the bound account id — SaveAccountHuntSettings can never create or
// modify a foreign row because s.AccountID is ignored in favour of the
// facade-bound aid.
func SaveAccountHuntSettings(ctx context.Context, pool *pgxpool.Pool, aid uuid.UUID, s AccountHuntSettings) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO account_hunt_settings
			(account_id, enabled, queries, notify_chat_id, notify_min_fit,
			 notify_max_age_seconds, score_enabled, score_min_jaccard,
			 score_max_llm_per_cycle, score_fail_open, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
		ON CONFLICT (account_id) DO UPDATE SET
			enabled                 = EXCLUDED.enabled,
			queries                 = EXCLUDED.queries,
			notify_chat_id          = EXCLUDED.notify_chat_id,
			notify_min_fit          = EXCLUDED.notify_min_fit,
			notify_max_age_seconds  = EXCLUDED.notify_max_age_seconds,
			score_enabled           = EXCLUDED.score_enabled,
			score_min_jaccard       = EXCLUDED.score_min_jaccard,
			score_max_llm_per_cycle = EXCLUDED.score_max_llm_per_cycle,
			score_fail_open         = EXCLUDED.score_fail_open,
			updated_at              = now()`,
		aid, s.Enabled, s.Queries, s.NotifyChatID, s.NotifyMinFit,
		int(s.NotifyMaxAge.Seconds()), s.ScoreEnabled, s.ScoreMinJaccard,
		s.ScoreMaxLLMPerCycle, s.ScoreFailOpen,
	)
	if err != nil {
		return fmt.Errorf("accounts: save hunt settings: %w", err)
	}
	return nil
}
