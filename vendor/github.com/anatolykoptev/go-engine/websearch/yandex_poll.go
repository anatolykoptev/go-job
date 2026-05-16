package websearch

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"context"
)

// yandexStartSearch posts an async search request and returns the operation ID.
func yandexStartSearch(
	ctx context.Context, cfg YandexConfig, query, region string,
) (string, error) {
	reqBody := yandexRequest{
		Query: yandexQuery{
			SearchType: "SEARCH_TYPE_RU",
			QueryText:  query,
			FamilyMode: "FAMILY_MODE_NONE",
		},
		SortSpec: yandexSortSpec{
			SortMode:  "SORT_MODE_BY_RELEVANCE",
			SortOrder: "SORT_ORDER_DESC",
		},
		GroupSpec: yandexGroupSpec{
			GroupMode:    "GROUP_MODE_DEEP",
			GroupsOnPage: "10",
			DocsInGroup:  "1",
		},
		MaxPass:  "2",
		Region:   region,
		L10N:     "LOCALIZATION_RU",
		FolderID: cfg.FolderID,
		Page:     "0",
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("yandex marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, yandexAsyncEndpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", fmt.Errorf("yandex new request: %w", err)
	}
	req.Header.Set("Authorization", "Api-Key "+cfg.APIKey)
	req.Header.Set("Content-Type", acceptJSON)

	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed below
	if err != nil {
		return "", fmt.Errorf("yandex http: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("yandex read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("yandex async HTTP %d: %s", resp.StatusCode, string(data))
	}

	var op yandexOperation
	if err := json.Unmarshal(data, &op); err != nil {
		return "", fmt.Errorf("yandex json: %w", err)
	}

	if op.ID == "" {
		return "", errors.New("yandex: empty operation id")
	}

	return op.ID, nil
}

// yandexPollOperation polls until the operation completes or times out.
func yandexPollOperation(
	ctx context.Context, cfg YandexConfig, opID string,
) ([]byte, error) {
	deadline := time.Now().Add(yandexMaxPollWait)
	ticker := time.NewTicker(yandexPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("yandex: operation %s timed out", opID)
			}
		}

		op, err := yandexFetchOperation(ctx, cfg, opID)
		if err != nil {
			return nil, err
		}
		if !op.Done {
			continue
		}
		return yandexExtractResponse(op)
	}
}

// yandexFetchOperation polls a single operation status.
func yandexFetchOperation(
	ctx context.Context, cfg YandexConfig, opID string,
) (*yandexOperation, error) {
	req, err := http.NewRequestWithContext(
		ctx, http.MethodGet, yandexOpEndpoint+opID, nil)
	if err != nil {
		return nil, fmt.Errorf("yandex poll request: %w", err)
	}
	req.Header.Set("Authorization", "Api-Key "+cfg.APIKey)

	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed below
	if err != nil {
		return nil, fmt.Errorf("yandex poll http: %w", err)
	}

	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("yandex poll read: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("yandex poll HTTP %d: %s", resp.StatusCode, string(data))
	}

	var op yandexOperation
	if err := json.Unmarshal(data, &op); err != nil {
		return nil, fmt.Errorf("yandex poll json: %w", err)
	}

	if op.Error != nil {
		return nil, fmt.Errorf("yandex error %d: %s", op.Error.Code, op.Error.Message)
	}

	return &op, nil
}

// yandexExtractResponse extracts XML bytes from a completed operation response.
func yandexExtractResponse(op *yandexOperation) ([]byte, error) {
	if len(op.Response) == 0 {
		return nil, errors.New("yandex: empty response in completed operation")
	}

	// Try JSON-wrapped base64: {"rawData": "base64..."}
	var wrapped struct {
		RawData string `json:"rawData"`
	}
	if err := json.Unmarshal(op.Response, &wrapped); err == nil && wrapped.RawData != "" {
		return base64.StdEncoding.DecodeString(wrapped.RawData)
	}

	// Try direct XML string in JSON.
	var xmlStr string
	if err := json.Unmarshal(op.Response, &xmlStr); err == nil {
		return []byte(xmlStr), nil
	}

	return op.Response, nil
}
