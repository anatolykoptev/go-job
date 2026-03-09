package jobs

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

type bountyhubResponse struct {
	Data        []bountyhubIssue `json:"data"`
	HasNextPage bool             `json:"hasNextPage"`
}

type bountyhubIssue struct {
	ID                 string `json:"id"`
	Title              string `json:"title"`
	HTMLURL            string `json:"htmlURL"`
	Language           string `json:"language"`
	RepositoryFullName string `json:"repositoryFullName"`
	IssueNumber        int    `json:"issueNumber"`
	IssueState         string `json:"issueState"`
	TotalAmount        string `json:"totalAmount"`
	Solved             bool   `json:"solved"`
	Claimed            bool   `json:"claimed"`
	CreatedAt          string `json:"createdAt"`
}

func parseBountyHubResponse(data []byte) ([]engine.BountyListing, bool, error) {
	var resp bountyhubResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, false, fmt.Errorf("bountyhub: JSON parse failed: %w", err)
	}

	bounties := make([]engine.BountyListing, 0, len(resp.Data))
	for _, issue := range resp.Data {
		if issue.HTMLURL == "" || issue.Solved || issue.Claimed {
			continue
		}

		amount := formatBountyHubAmount(issue.TotalAmount)

		var skills []string
		if issue.Language != "" {
			skills = []string{issue.Language}
		}

		bounties = append(bounties, engine.BountyListing{
			Title:    issue.Title,
			Org:      issue.RepositoryFullName,
			URL:      issue.HTMLURL,
			Amount:   amount,
			Currency: "USD",
			Skills:   skills,
			Source:   "bountyhub",
			IssueNum: "#" + strconv.Itoa(issue.IssueNumber),
			Posted:   issue.CreatedAt,
		})
	}

	return bounties, resp.HasNextPage, nil
}

func formatBountyHubAmount(amount string) string {
	parts := strings.Split(amount, ".")
	dollars, err := strconv.Atoi(parts[0])
	if err != nil || dollars <= 0 {
		return "$0"
	}
	return formatCentsUSD(dollars * 100)
}
