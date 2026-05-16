package search

import (
	"context"

	"github.com/anatolykoptev/go-engine/metrics"
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-engine/websearch"
)

const metricYandexRequests = "yandex_requests"

// YandexConfig holds Yandex Search API v2 credentials.
type YandexConfig struct {
	APIKey       string // Api-Key for Authorization header
	FolderID     string // Yandex Cloud folder ID
	MonthlyLimit int    // max requests per calendar month (0 = unlimited)
}

// SearchYandexAPI queries Yandex Search API v2 (async) and returns results.
// Delegates to websearch.SearchYandexAPI.
func SearchYandexAPI(ctx context.Context, cfg YandexConfig, query, region string, m *metrics.Registry) ([]sources.Result, error) {
	if m != nil {
		m.Incr(metricYandexRequests)
	}
	wcfg := websearch.YandexConfig{
		APIKey:       cfg.APIKey,
		FolderID:     cfg.FolderID,
		MonthlyLimit: cfg.MonthlyLimit,
	}
	ws, err := websearch.SearchYandexAPI(ctx, wcfg, query, region)
	if err != nil {
		return nil, err
	}
	return ws, nil
}

// ParseYandexXML extracts search results from Yandex Search API XML response.
// Delegates to websearch.ParseYandexXML.
func ParseYandexXML(data []byte) ([]sources.Result, error) {
	ws, err := websearch.ParseYandexXML(data)
	return ws, err
}
