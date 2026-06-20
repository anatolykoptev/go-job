package pipeline

import (
	"context"
	"log/slog"
	"sync"
	"time"

	kitmetrics "github.com/anatolykoptev/go-kit/metrics"

	"github.com/anatolykoptev/go-engine/llm"
	"github.com/anatolykoptev/go-engine/metrics"
	"github.com/anatolykoptev/go-engine/sources"
)

// pipelineSourceResult holds the outcome of one source goroutine in the
// pipeline fan-out.
type pipelineSourceResult struct {
	name    string
	results []sources.Result
	err     error
	dur     time.Duration
}

// runPipelineSourceWithTimeout executes src.Search inside a goroutine that is
// capped by srcCtx. It sends to ch once Search returns or srcCtx is cancelled
// (whichever comes first), so a stalled source cannot block the fan-out beyond
// its per-source deadline.
//
// Goroutine lifetime note: the inner goroutine may outlive srcCtx cancellation
// if src.Search does not observe context cancellation promptly (e.g. TCP stall
// inside the source). It is bounded by whatever HTTP timeout the source uses
// and cannot run indefinitely. done MUST remain buffered (cap >= 1) so that
// when this function returns on the srcCtx.Done branch the inner goroutine can
// still send without blocking forever.
func runPipelineSourceWithTimeout(srcCtx context.Context, src sources.Source, query string, ch chan<- pipelineSourceResult) {
	start := time.Now()
	type fnResult struct {
		res []sources.Result
		err error
	}
	done := make(chan fnResult, 1)
	go func() {
		res, err := src.Search(srcCtx, sources.Query{Text: query})
		done <- fnResult{res, err}
	}()

	var res []sources.Result
	var err error
	select {
	case r := <-done:
		res, err = r.res, r.err
	case <-srcCtx.Done():
		err = srcCtx.Err()
	}
	ch <- pipelineSourceResult{name: src.Name(), results: res, err: err, dur: time.Since(start)}
}

// searchSources fans out Search calls to all sources concurrently.
// Each source is capped by a per-source timeout; in-flight sources are
// cancelled once earlyReturnAt results have been collected. Source errors
// are logged and skipped — partial results are returned.
func (p *Pipeline) searchSources(ctx context.Context, query string) []sources.Result {
	if len(p.sources) == 0 {
		return nil
	}

	perSrc := p.perSourceTimeout
	if perSrc == 0 {
		perSrc = defaultPipelinePerSourceTimeout
	}
	earlyAt := p.earlyReturnAt
	if earlyAt == 0 {
		earlyAt = defaultPipelineEarlyReturnAt
	}

	// allDoneCtx is cancelled when enough results accumulate (early-return path).
	allDoneCtx, allDoneCancel := context.WithCancel(ctx)
	defer allDoneCancel()

	ch := make(chan pipelineSourceResult, len(p.sources))
	var wg sync.WaitGroup

	for _, src := range p.sources {
		wg.Add(1)
		go func(s sources.Source) {
			defer wg.Done()
			srcCtx, srcCancel := context.WithTimeout(allDoneCtx, perSrc)
			defer srcCancel()
			runPipelineSourceWithTimeout(srcCtx, s, query, ch)
		}(src)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	return collectPipelineResults(ch, p.metrics, earlyAt, allDoneCancel)
}

// metricPipelineSourceResult is the per-source fan-out outcome counter for the
// pipeline path. Encoded as name{source=<label>,outcome=ok|fail} so the
// go-kit/metrics Prometheus bridge surfaces it as
// go_search_source_result_total{source="yep",outcome="fail"}.
//
// Uses the same metric name as search/direct.go recordSourceResult so that
// both the pipeline fan-out and the direct fan-out share a single alertable
// counter. A source failing 100% is invisible if a sibling silently covers it;
// this counter makes that failure rate alertable.
const metricPipelineSourceResult = "go_search_source_result_total"

// recordPipelineSourceResult increments the per-source outcome counter. Nil-safe.
func recordPipelineSourceResult(m *metrics.Registry, source, outcome string) {
	if m == nil {
		return
	}
	m.Incr(kitmetrics.Label(metricPipelineSourceResult, "source", source, "outcome", outcome))
}

// collectPipelineResults drains ch, emitting metrics and triggering early-return
// cancellation once earlyAt results are collected.
func collectPipelineResults(ch <-chan pipelineSourceResult, m *metrics.Registry, earlyAt int, cancel context.CancelFunc) []sources.Result {
	var all []sources.Result
	var cancelled bool
	for r := range ch {
		if m != nil {
			m.ObserveSeconds(
				kitmetrics.Label("go_search_search_source_duration_seconds", "source", r.name),
				r.dur,
			)
		}
		if r.err != nil {
			recordPipelineSourceResult(m, r.name, "fail")
			slog.Warn("source search failed",
				slog.String("source", r.name),
				slog.Any("err", r.err),
			)
			continue
		}
		recordPipelineSourceResult(m, r.name, "ok")
		slog.Info("pipeline source results", slog.String("source", r.name), slog.Int("count", len(r.results)))
		all = append(all, r.results...)
		if !cancelled && len(all) >= earlyAt {
			cancel()
			cancelled = true
		}
	}
	return all
}

// buildOutput assembles the SearchOutput from LLM output and source results.
func (p *Pipeline) buildOutput(query string, out *llm.StructuredOutput, srcResults []sources.Result) *SearchOutput {
	searchOut := BuildSearchOutput(query, out, srcResults)
	return &searchOut
}
