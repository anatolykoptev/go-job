package search

import (
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-stealth/websearch"
)

// wsToSourceResults converts websearch.Result slice to sources.Result slice.
func wsToSourceResults(ws []websearch.Result) []sources.Result {
	if ws == nil {
		return nil
	}
	out := make([]sources.Result, len(ws))
	for i, r := range ws {
		out[i] = sources.Result{
			Title:    r.Title,
			URL:      r.URL,
			Content:  r.Content,
			Score:    r.Score,
			Metadata: r.Metadata,
		}
	}
	return out
}

// sourceToWSResults converts sources.Result slice to websearch.Result slice.
func sourceToWSResults(sr []sources.Result) []websearch.Result {
	if sr == nil {
		return nil
	}
	out := make([]websearch.Result, len(sr))
	for i, r := range sr {
		out[i] = websearch.Result{
			Title:    r.Title,
			URL:      r.URL,
			Content:  r.Content,
			Score:    r.Score,
			Metadata: r.Metadata,
		}
	}
	return out
}
