package jobs

import (
	"encoding/json"
	"fmt"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// --- Federacy ---

type federacyProgram struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	OffersAwards bool  `json:"offers_awards"`
	URL         string `json:"url"`
	Targets     struct {
		InScope []struct {
			Type   string `json:"type"`
			Target string `json:"target"`
		} `json:"in_scope"`
	} `json:"targets"`
}

func parseFederacyData(data []byte) ([]engine.SecurityProgram, error) {
	var raw []federacyProgram
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("federacy: parse failed: %w", err)
	}

	programs := make([]engine.SecurityProgram, 0, len(raw))
	for _, r := range raw {
		if r.URL == "" {
			continue
		}

		targets := make([]string, 0, len(r.Targets.InScope))
		for _, t := range r.Targets.InScope {
			if t.Target != "" {
				targets = append(targets, t.Target)
			}
		}

		// Federacy programs with offers_awards=false are VDPs (no bounty).
		progType := progTypeVDP
		if r.OffersAwards {
			progType = progTypeBugBounty
		}

		programs = append(programs, engine.SecurityProgram{
			Name:     r.Name,
			Platform: "federacy",
			URL:      r.URL,
			Targets:  targets,
			Type:     progType,
		})
	}

	return programs, nil
}
