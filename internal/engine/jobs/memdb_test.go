package jobs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestDeleteByUser_RetriesOn500Deadlock(t *testing.T) {
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":500,"message":"delete by memory_ids failed","data":{"error":"ERROR: deadlock detected (SQLSTATE 40P01)"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":200,"data":null}`))
	}))
	defer srv.Close()

	client := NewMemDBClient(srv.URL, "")
	err := client.DeleteByUser(context.Background(), []string{"mem-id-1", "mem-id-2"})
	if err != nil {
		t.Fatalf("DeleteByUser expected success after retry, got: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected exactly 2 requests (1 failure + 1 success), got %d", got)
	}
}
