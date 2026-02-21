package jobserver

import (
	"context"
	"errors"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerResumeEnrich(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "resume_enrich",
		Description: "Interactively enrich your master resume. Use action='start' to get enrichment questions about gaps (missing metrics, hidden skills, unclear roles). Use action='answer' with your answers to apply enrichments to the knowledge graph.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.ResumeEnrichInput) (*mcp.CallToolResult, *jobs.ResumeEnrichResult, error) {
		if input.Action == "" {
			return nil, nil, errors.New("action is required ('start' or 'answer')")
		}

		var answers []jobs.AnswerPair
		for _, a := range input.Answers {
			answers = append(answers, jobs.AnswerPair{
				QuestionID: a.QuestionID,
				Answer:     a.Answer,
			})
		}

		result, err := jobs.EnrichResume(ctx, input.Action, answers)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}
