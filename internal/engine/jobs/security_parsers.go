package jobs

import (
	"encoding/json"
	"fmt"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// --- HackerOne ---

type h1Program struct {
	Name           string `json:"name"`
	Handle         string `json:"handle"`
	URL            string `json:"url"`
	OffersBounties bool   `json:"offers_bounties"`
	ManagedProgram bool   `json:"managed_program"`
	Targets        struct {
		InScope []struct {
			AssetIdentifier string `json:"asset_identifier"`
		} `json:"in_scope"`
	} `json:"targets"`
}

func parseHackerOneData(data []byte) ([]engine.SecurityProgram, error) {
	var raw []h1Program
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("hackerone: parse failed: %w", err)
	}

	programs := make([]engine.SecurityProgram, 0, len(raw))
	for _, r := range raw {
		if r.URL == "" {
			continue
		}

		progType := "vdp"
		if r.OffersBounties {
			progType = "bug_bounty"
		}

		targets := make([]string, 0, len(r.Targets.InScope))
		for _, t := range r.Targets.InScope {
			if t.AssetIdentifier != "" {
				targets = append(targets, t.AssetIdentifier)
			}
		}

		programs = append(programs, engine.SecurityProgram{
			Name:     r.Name,
			Platform: "hackerone",
			URL:      r.URL,
			Targets:  targets,
			Type:     progType,
			Managed:  r.ManagedProgram,
		})
	}

	return programs, nil
}

// --- Bugcrowd ---

type bcProgram struct {
	Name             string `json:"name"`
	URL              string `json:"url"`
	ManagedByBugcrowd bool  `json:"managed_by_bugcrowd"`
	MaxPayout        int    `json:"max_payout"`
	Targets          struct {
		InScope []struct {
			Target string `json:"target"`
		} `json:"in_scope"`
	} `json:"targets"`
}

func parseBugcrowdData(data []byte) ([]engine.SecurityProgram, error) {
	var raw []bcProgram
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("bugcrowd: parse failed: %w", err)
	}

	programs := make([]engine.SecurityProgram, 0, len(raw))
	for _, r := range raw {
		if r.URL == "" {
			continue
		}

		progType := "vdp"
		if r.MaxPayout > 0 {
			progType = "bug_bounty"
		}

		targets := make([]string, 0, len(r.Targets.InScope))
		for _, t := range r.Targets.InScope {
			if t.Target != "" {
				targets = append(targets, t.Target)
			}
		}

		programs = append(programs, engine.SecurityProgram{
			Name:      r.Name,
			Platform:  "bugcrowd",
			URL:       r.URL,
			MaxBounty: formatOptionalUSD(r.MaxPayout),
			Targets:   targets,
			Type:      progType,
			Managed:   r.ManagedByBugcrowd,
		})
	}

	return programs, nil
}

// formatOptionalUSD formats a dollar amount using formatCentsUSD.
// Returns "$0" for zero values.
func formatOptionalUSD(dollars int) string {
	return formatCentsUSD(dollars * 100)
}

