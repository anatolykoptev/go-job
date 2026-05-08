package jobs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestClearAll_CallsBulkEndpoint(t *testing.T) {
	var deleteAllCalls atomic.Int32
	var searchCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/product/delete_all_memories":
			deleteAllCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"code":200,"data":null}`))
		case "/product/search":
			searchCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"code":200,"data":{"text_mem":[]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := NewMemDBClient(srv.URL, "")
	err := client.ClearAll(context.Background())
	if err != nil {
		t.Fatalf("ClearAll expected success, got: %v", err)
	}
	if got := deleteAllCalls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 call to /product/delete_all_memories, got %d", got)
	}
	if got := searchCalls.Load(); got != 0 {
		t.Fatalf("expected no calls to /product/search, got %d", got)
	}
}

func TestClearAllBySearch_AbortsOnStuckLoop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		switch r.URL.Path {
		case "/product/search":
			// Always return one result so the loop never exits naturally.
			_, _ = w.Write([]byte(`{"code":200,"data":{"text_mem":[{"memories":[{"memory":"test","metadata":{"relativity":0.9,"id":"mem-id-1","info":{}}}]}]}}`))
		case "/product/delete_memory":
			// Always report deleted_count=0 (stuck loop scenario).
			_, _ = w.Write([]byte(`{"code":200,"data":{"deleted_count":0}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := NewMemDBClient(srv.URL, "")
	err := client.ClearAllBySearch(context.Background())
	if err == nil {
		t.Fatal("ClearAllBySearch expected error for stuck loop, got nil")
	}
	if !strings.Contains(err.Error(), "stuck loop") {
		t.Fatalf("expected error to mention 'stuck loop', got: %v", err)
	}
}
