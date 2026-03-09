package jobs

import (
	"encoding/json"
	"fmt"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// --- Intigriti ---

type igProgram struct {
	Name   string `json:"name"`
	Handle string `json:"handle"`
	URL    string `json:"url"`
	Min    struct {
		Value    int    `json:"value"`
		Currency string `json:"currency"`
	} `json:"min_bounty"`
	Max struct {
		Value    int    `json:"value"`
		Currency string `json:"currency"`
	} `json:"max_bounty"`
	Targets struct {
		InScope []struct {
			Endpoint string `json:"endpoint"`
		} `json:"in_scope"`
	} `json:"targets"`
}

func parseIntigritiData(data []byte) ([]engine.SecurityProgram, error) {
	var raw []igProgram
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("intigriti: parse failed: %w", err)
	}

	programs := make([]engine.SecurityProgram, 0, len(raw))
	for _, r := range raw {
		if r.URL == "" {
			continue
		}

		targets := make([]string, 0, len(r.Targets.InScope))
		for _, t := range r.Targets.InScope {
			if t.Endpoint != "" {
				targets = append(targets, t.Endpoint)
			}
		}

		progType := "vdp"
		if r.Max.Value > 0 {
			progType = "bug_bounty"
		}

		programs = append(programs, engine.SecurityProgram{
			Name:      r.Name,
			Platform:  "intigriti",
			URL:       r.URL,
			MaxBounty: formatOptionalUSD(r.Max.Value),
			MinBounty: formatOptionalUSD(r.Min.Value),
			Targets:   targets,
			Type:      progType,
		})
	}

	return programs, nil
}

// --- YesWeHack ---

type ywhProgram struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Disabled  bool   `json:"disabled"`
	MinBounty int    `json:"min_bounty"`
	MaxBounty int    `json:"max_bounty"`
	Targets   struct {
		InScope []struct {
			Target string `json:"target"`
		} `json:"in_scope"`
	} `json:"targets"`
}

func parseYesWeHackData(data []byte) ([]engine.SecurityProgram, error) {
	var raw []ywhProgram
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("yeswehack: parse failed: %w", err)
	}

	programs := make([]engine.SecurityProgram, 0, len(raw))
	for _, r := range raw {
		if r.ID == "" || r.Disabled {
			continue
		}

		targets := make([]string, 0, len(r.Targets.InScope))
		for _, t := range r.Targets.InScope {
			if t.Target != "" {
				targets = append(targets, t.Target)
			}
		}

		progType := "vdp"
		if r.MaxBounty > 0 {
			progType = "bug_bounty"
		}

		programs = append(programs, engine.SecurityProgram{
			Name:      r.Name,
			Platform:  "yeswehack",
			URL:       "https://yeswehack.com/programs/" + r.ID,
			MaxBounty: formatOptionalUSD(r.MaxBounty),
			MinBounty: formatOptionalUSD(r.MinBounty),
			Targets:   targets,
			Type:      progType,
		})
	}

	return programs, nil
}
