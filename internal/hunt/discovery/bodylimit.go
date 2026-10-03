package discovery

import (
	"errors"
	"fmt"
	"io"
)

var errBodyTruncated = errors.New("discovery: body truncated at read cap")

// readLimitedBody reads one byte beyond limit so that a growing response fails
// at the reader boundary instead of surfacing as a misleading JSON parse error.
func readLimitedBody(r io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("discovery: body exceeds %d-byte read cap: %w", limit, errBodyTruncated)
	}
	return body, nil
}
