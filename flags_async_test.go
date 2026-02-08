package flags

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackgroundRefresh_FetchesFlags(t *testing.T) {
	var fetchCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCount.Add(1)
		response := `{
			"intervalAllowed": 1,
			"flags": [
				{"enabled": true, "details": {"name": "bg-flag", "id": "1"}}
			]
		}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintln(w, response)
	}))
	defer server.Close()

	client := NewClient(
		WithBaseURL(server.URL),
		WithAuth(Auth{
			ProjectID:     "test-project",
			AgentID:       "test-agent",
			EnvironmentID: "test-environment",
		}),
		WithMemory(),
		WithBackgroundRefresh(),
	)
	defer client.Close()

	// Wait for initial fetch
	time.Sleep(200 * time.Millisecond)

	got := client.Is("bg-flag").Enabled()
	if !got {
		t.Error("Expected bg-flag to be enabled after background fetch")
	}

	if fetchCount.Load() < 1 {
		t.Error("Expected at least one fetch from background goroutine")
	}
}

func TestBackgroundRefresh_PeriodicRefresh(t *testing.T) {
	var fetchCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCount.Add(1)
		response := `{
			"intervalAllowed": 1,
			"flags": [
				{"enabled": true, "details": {"name": "periodic-flag", "id": "1"}}
			]
		}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintln(w, response)
	}))
	defer server.Close()

	client := NewClient(
		WithBaseURL(server.URL),
		WithAuth(Auth{
			ProjectID:     "test-project",
			AgentID:       "test-agent",
			EnvironmentID: "test-environment",
		}),
		WithMemory(),
		WithBackgroundRefresh(),
	)
	defer client.Close()

	// Wait for initial fetch + at least one periodic refresh (interval=1s)
	time.Sleep(2500 * time.Millisecond)

	count := fetchCount.Load()
	if count < 2 {
		t.Errorf("Expected at least 2 fetches (initial + periodic), got %d", count)
	}
}

func TestBackgroundRefresh_Close(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := `{
			"intervalAllowed": 60,
			"flags": [
				{"enabled": true, "details": {"name": "close-flag", "id": "1"}}
			]
		}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintln(w, response)
	}))
	defer server.Close()

	client := NewClient(
		WithBaseURL(server.URL),
		WithAuth(Auth{
			ProjectID:     "test-project",
			AgentID:       "test-agent",
			EnvironmentID: "test-environment",
		}),
		WithMemory(),
		WithBackgroundRefresh(),
	)

	// Close should return without hanging
	done := make(chan struct{})
	go func() {
		client.Close()
		close(done)
	}()

	select {
	case <-done:
		// good
	case <-time.After(5 * time.Second):
		t.Fatal("Close() did not return within 5 seconds")
	}
}

func TestBackgroundRefresh_CloseWithoutBackground(t *testing.T) {
	// Close on a non-background client should be a no-op
	client := NewClient(WithMemory())
	client.Close() // should not panic
}

func TestBackgroundRefresh_EnabledDoesNotBlock(t *testing.T) {
	// Server with a slow response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		response := `{
			"intervalAllowed": 60,
			"flags": [
				{"enabled": true, "details": {"name": "slow-flag", "id": "1"}}
			]
		}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintln(w, response)
	}))
	defer server.Close()

	client := NewClient(
		WithBaseURL(server.URL),
		WithAuth(Auth{
			ProjectID:     "test-project",
			AgentID:       "test-agent",
			EnvironmentID: "test-environment",
		}),
		WithMemory(),
		WithBackgroundRefresh(),
	)
	defer client.Close()

	// Call Enabled immediately — should return fast (not block on fetch)
	start := time.Now()
	result := client.Is("slow-flag").Enabled()
	elapsed := time.Since(start)

	// Should return quickly since background mode doesn't block
	if elapsed > 100*time.Millisecond {
		t.Errorf("Enabled() took %v, expected it to return without blocking", elapsed)
	}

	// Cache is empty so result should be false
	if result {
		t.Error("Expected false before background fetch completes")
	}

	// Wait for background fetch to complete
	time.Sleep(800 * time.Millisecond)

	// Now it should be in the cache
	got := client.Is("slow-flag").Enabled()
	if !got {
		t.Error("Expected slow-flag to be enabled after background fetch completes")
	}
}

func TestErrorHandler_CalledOnFetchError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	var errorReceived atomic.Bool
	client := NewClient(
		WithBaseURL(server.URL),
		WithAuth(Auth{
			ProjectID:     "test-project",
			AgentID:       "test-agent",
			EnvironmentID: "test-environment",
		}),
		WithMemory(),
		WithBackgroundRefresh(),
		WithMaxRetries(1),
		WithErrorHandler(func(err error) {
			errorReceived.Store(true)
		}),
	)
	defer client.Close()

	// Wait for the background goroutine to attempt fetch and fail
	time.Sleep(2 * time.Second)

	if !errorReceived.Load() {
		t.Error("Expected error handler to be called on fetch failure")
	}
}

func TestRefreshDeduplication(t *testing.T) {
	var fetchCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCount.Add(1)
		time.Sleep(200 * time.Millisecond) // Simulate slow response
		response := `{
			"intervalAllowed": 60,
			"flags": [
				{"enabled": true, "details": {"name": "dedup-flag", "id": "1"}}
			]
		}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintln(w, response)
	}))
	defer server.Close()

	client := NewClient(
		WithBaseURL(server.URL),
		WithAuth(Auth{
			ProjectID:     "test-project",
			AgentID:       "test-agent",
			EnvironmentID: "test-environment",
		}),
		WithMemory(),
	)

	// Fire multiple concurrent flag checks that all trigger refetch
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client.Is("dedup-flag").Enabled()
		}()
	}
	wg.Wait()

	count := fetchCount.Load()
	if count > 3 {
		t.Errorf("Expected at most 3 fetches due to deduplication, got %d", count)
	}
}

func TestGetMultiple(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := `{
			"intervalAllowed": 60,
			"flags": [
				{"enabled": true, "details": {"name": "flag-a", "id": "1"}},
				{"enabled": false, "details": {"name": "flag-b", "id": "2"}},
				{"enabled": true, "details": {"name": "flag-c", "id": "3"}}
			]
		}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintln(w, response)
	}))
	defer server.Close()

	client := NewClient(
		WithBaseURL(server.URL),
		WithAuth(Auth{
			ProjectID:     "test-project",
			AgentID:       "test-agent",
			EnvironmentID: "test-environment",
		}),
		WithMemory(),
	)

	result := client.GetMultiple("flag-a", "flag-b", "flag-c", "flag-missing")

	if !result["flag-a"] {
		t.Error("Expected flag-a to be enabled")
	}
	if result["flag-b"] {
		t.Error("Expected flag-b to be disabled")
	}
	if !result["flag-c"] {
		t.Error("Expected flag-c to be enabled")
	}
	if result["flag-missing"] {
		t.Error("Expected flag-missing to be false")
	}
}

func TestAllEnabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := `{
			"intervalAllowed": 60,
			"flags": [
				{"enabled": true, "details": {"name": "all-a", "id": "1"}},
				{"enabled": true, "details": {"name": "all-b", "id": "2"}},
				{"enabled": false, "details": {"name": "all-c", "id": "3"}}
			]
		}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintln(w, response)
	}))
	defer server.Close()

	client := NewClient(
		WithBaseURL(server.URL),
		WithAuth(Auth{
			ProjectID:     "test-project",
			AgentID:       "test-agent",
			EnvironmentID: "test-environment",
		}),
		WithMemory(),
	)

	if !client.AllEnabled("all-a", "all-b") {
		t.Error("Expected AllEnabled to return true when all flags are enabled")
	}
	if client.AllEnabled("all-a", "all-c") {
		t.Error("Expected AllEnabled to return false when one flag is disabled")
	}
}

func TestAnyEnabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := `{
			"intervalAllowed": 60,
			"flags": [
				{"enabled": true, "details": {"name": "any-a", "id": "1"}},
				{"enabled": false, "details": {"name": "any-b", "id": "2"}},
				{"enabled": false, "details": {"name": "any-c", "id": "3"}}
			]
		}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintln(w, response)
	}))
	defer server.Close()

	client := NewClient(
		WithBaseURL(server.URL),
		WithAuth(Auth{
			ProjectID:     "test-project",
			AgentID:       "test-agent",
			EnvironmentID: "test-environment",
		}),
		WithMemory(),
	)

	if !client.AnyEnabled("any-a", "any-b") {
		t.Error("Expected AnyEnabled to return true when at least one flag is enabled")
	}
	if client.AnyEnabled("any-b", "any-c") {
		t.Error("Expected AnyEnabled to return false when no flags are enabled")
	}
}

func TestGetMultiple_WithLocalOverrides(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := `{
			"intervalAllowed": 60,
			"flags": [
				{"enabled": true, "details": {"name": "remote-flag", "id": "1"}}
			]
		}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintln(w, response)
	}))
	defer server.Close()

	if err := os.Setenv("FLAGS_LOCAL_BATCH", "true"); err != nil {
		t.Fatal(err)
	}
	defer os.Unsetenv("FLAGS_LOCAL_BATCH")

	client := NewClient(
		WithBaseURL(server.URL),
		WithAuth(Auth{
			ProjectID:     "test-project",
			AgentID:       "test-agent",
			EnvironmentID: "test-environment",
		}),
		WithMemory(),
	)

	result := client.GetMultiple("remote-flag", "local-batch")

	if !result["remote-flag"] {
		t.Error("Expected remote-flag to be enabled")
	}
	if !result["local-batch"] {
		t.Error("Expected local-batch to be enabled from env override")
	}
}

func TestBackgroundRefresh_UpdatesInterval(t *testing.T) {
	callCount := atomic.Int32{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		response := `{
			"intervalAllowed": 30,
			"flags": [
				{"enabled": true, "details": {"name": "interval-flag", "id": "1"}}
			]
		}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintln(w, response)
	}))
	defer server.Close()

	client := NewClient(
		WithBaseURL(server.URL),
		WithAuth(Auth{
			ProjectID:     "test-project",
			AgentID:       "test-agent",
			EnvironmentID: "test-environment",
		}),
		WithMemory(),
		WithBackgroundRefresh(),
	)
	defer client.Close()

	// Wait for initial fetch
	time.Sleep(200 * time.Millisecond)

	interval := client.refreshInterval.Load()
	if interval != 30 {
		t.Errorf("Expected refresh interval to be updated to 30, got %d", interval)
	}
}
