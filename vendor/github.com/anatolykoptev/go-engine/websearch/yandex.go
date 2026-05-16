package websearch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"
)

const (
	yandexAsyncEndpoint = "https://searchapi.api.cloud.yandex.net/v2/web/searchAsync"
	yandexOpEndpoint    = "https://operation.api.cloud.yandex.net/operations/"
	yandexPollInterval  = 500 * time.Millisecond
	yandexMaxPollWait   = 10 * time.Second
	yandexDefaultRegion = "213" // Moscow
)

// YandexConfig holds Yandex Search API v2 credentials.
type YandexConfig struct {
	APIKey       string // Api-Key for Authorization header
	FolderID     string // Yandex Cloud folder ID
	MonthlyLimit int    // max requests per calendar month (0 = unlimited)
}

// yandexMonthlyCounter tracks requests per calendar month.
var yandexMonthlyCounter struct {
	count atomic.Int64
	month atomic.Int32 // current month (1-12)
}

// yandexCheckLimit returns true if the request is within the monthly limit.
// Resets counter on month change.
func yandexCheckLimit(limit int) bool {
	if limit <= 0 {
		return true
	}
	now := time.Now()
	currentMonth := int32(now.Month()) //nolint:gosec // Month() returns 1-12, safe for int32
	if stored := yandexMonthlyCounter.month.Load(); stored != currentMonth {
		yandexMonthlyCounter.month.CompareAndSwap(stored, currentMonth)
		yandexMonthlyCounter.count.Store(0)
	}
	return yandexMonthlyCounter.count.Add(1) <= int64(limit)
}

// SearchYandexAPI queries Yandex Search API v2 (async) and returns results.
func SearchYandexAPI(
	ctx context.Context, cfg YandexConfig, query, region string,
) ([]Result, error) {
	if cfg.APIKey == "" || cfg.FolderID == "" {
		return nil, errors.New("yandex: api key and folder_id required")
	}

	if !yandexCheckLimit(cfg.MonthlyLimit) {
		slog.Warn("yandex: monthly limit reached", slog.Int("limit", cfg.MonthlyLimit))
		return nil, nil //nolint:nilnil // over budget, skip silently
	}

	if region == "" {
		region = yandexDefaultRegion
	}

	// 1. Start async search operation.
	opID, err := yandexStartSearch(ctx, cfg, query, region)
	if err != nil {
		return nil, fmt.Errorf("yandex start: %w", err)
	}

	// 2. Poll for completion.
	xmlData, err := yandexPollOperation(ctx, cfg, opID)
	if err != nil {
		return nil, fmt.Errorf("yandex poll: %w", err)
	}

	// 3. Parse XML response.
	results, err := ParseYandexXML(xmlData)
	if err != nil {
		return nil, fmt.Errorf("yandex parse: %w", err)
	}

	slog.Debug("yandex api results",
		slog.Int("count", len(results)),
		slog.String("query", query))
	return results, nil
}
