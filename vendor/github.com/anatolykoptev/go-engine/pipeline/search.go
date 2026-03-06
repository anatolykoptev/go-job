package pipeline

import (
	"context"
	"log/slog"
	"sync"

	"github.com/anatolykoptev/go-engine/llm"
	"github.com/anatolykoptev/go-engine/sources"
)

// searchSources fans out Search calls to all sources concurrently.
// Source errors are logged and skipped — partial results are returned.
func (p *Pipeline) searchSources(ctx context.Context, query string) []sources.Result {
	if len(p.sources) == 0 {
		return nil
	}

	type sourceOut struct {
		results []sources.Result
	}

	ch := make(chan sourceOut, len(p.sources))
	var wg sync.WaitGroup

	for _, src := range p.sources {
		wg.Add(1)
		go func(s sources.Source) {
			defer wg.Done()
			res, err := s.Search(ctx, sources.Query{Text: query})
			if err != nil {
				slog.WarnContext(ctx, "source search failed",
					"source", s.Name(),
					"err", err,
				)
				ch <- sourceOut{}
				return
			}
			ch <- sourceOut{results: res}
		}(src)
	}

	wg.Wait()
	close(ch)

	var all []sources.Result
	for out := range ch {
		all = append(all, out.results...)
	}
	return all
}

// buildOutput assembles the SearchOutput from LLM output and source results.
func (p *Pipeline) buildOutput(query string, out *llm.StructuredOutput, srcResults []sources.Result) *SearchOutput {
	searchOut := BuildSearchOutput(query, out, srcResults)
	return &searchOut
}
