package flags

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClient_Is(t *testing.T) {
	client := NewClient()
	flag := client.Is("test-flag")

	if flag.Name != "test-flag" {
		t.Errorf("Expected flag name to be 'test-flag', got %s", flag.Name)
	}
	if flag.client != client {
		t.Error("Expected flag client to be set correctly")
	}
}

func TestNewClientWithOptions(t *testing.T) {
	customURL := "https://custom.flags.gg"
	customRetries := 5

	client := NewClient(
		WithBaseURL(customURL),
		WithMaxRetries(customRetries),
		WithAuth(Auth{
			ProjectID:     "test-project",
			AgentID:       "test-agent",
			EnvironmentID: "test-environment",
		}),
	)

	if client.baseURL != customURL {
		t.Errorf("Expected baseURL to be %s, got %s", customURL, client.baseURL)
	}
	if client.maxRetries != customRetries {
		t.Errorf("Expected maxRetries to be %d, got %d", customRetries, client.maxRetries)
	}
}

func TestFeatureFlags(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := `{
			"intervalAllowed": 60,
			"secretMenu": {"sequence": ["b","b","b"]},
			"flags": [
				{"enabled": true, "details": {"name": "enabled-flag", "id": "1"}},
				{"enabled": false, "details": {"name": "disabled-flag", "id": "2"}}
			]
		}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintln(w, response)
	}))
	defer server.Close()

	client := NewClient(WithBaseURL(server.URL), WithAuth(Auth{
		ProjectID:     "test-project",
		AgentID:       "test-agent",
		EnvironmentID: "test-environment",
	}))

	tests := []struct {
		name     string
		flagName string
		want     bool
	}{
		{
			name:     "enabled flag returns true",
			flagName: "enabled-flag",
			want:     true,
		},
		{
			name:     "disabled flag returns false",
			flagName: "disabled-flag",
			want:     false,
		},
		{
			name:     "non-existent flag returns false",
			flagName: "non-existent",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := client.Is(tt.flagName).Enabled()
			if got != tt.want {
				t.Errorf("Flag %s: got %v, want %v", tt.flagName, got, tt.want)
			}
		})
	}
}

//func TestCaching(t *testing.T) {
//	callCount := 0
//	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
//		callCount++
//		response := fmt.Sprintf(`{
//			"intervalAllowed": 2,
//			"secretMenu": {"sequence": ["b"]},
//			"flags": [{"enabled": true, "details": {"name": "test-flag", "id": "%d"}}]
//		}`, callCount)
//		w.Header().Set("Content-Type", "application/json")
//		_, _ = fmt.Fprintln(w, response)
//	}))
//	defer server.Close()
//
//	client := NewClient(WithBaseURL(server.URL), WithAuth(Auth{
//		ProjectID:     "test-project",
//		AgentID:       "test-agent",
//		EnvironmentID: "test-environment",
//	}))
//
//	// First call should hit the server
//	r1 := client.Is("test-flag").Enabled()
//	initialCallCount := callCount
//	if !r1 {
//		t.Errorf("Expected first call to return true, got false")
//	}
//	if initialCallCount != 1 {
//		t.Errorf("Expected 1 server call, got %d", initialCallCount)
//	}
//
//	// Second immediate call should use cache
//	r2 := client.Is("test-flag").Enabled()
//	if !r2 {
//		t.Errorf("Expected second call to return true, got false")
//	}
//	if callCount != initialCallCount {
//		t.Errorf("Expected cache hit (still 1 call), got %d calls", callCount)
//	}
//
//	// Wait for cache to expire (intervalAllowed is 1 second)
//	time.Sleep(3 * time.Second)
//
//	// This call should hit the server again
//	r3 := client.Is("test-flag").Enabled()
//	if !r3 {
//		t.Errorf("Expected third call to return true, got false")
//	}
//	if callCount <= initialCallCount {
//		t.Errorf("Expected cache hit (still %d call), got %d calls", initialCallCount, callCount)
//	}
//}

//func TestCircuitBreaker(t *testing.T) {
//	failures := 0
//	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
//		failures++
//		w.WriteHeader(http.StatusInternalServerError)
//	}))
//	defer server.Close()
//
//	client := NewClient(
//		WithBaseURL(server.URL),
//		WithMaxRetries(3),
//		WithAuth(Auth{
//			ProjectID:     "test-project",
//			AgentID:       "test-agent",
//			EnvironmentID: "test-environment",
//		}),
//	)
//
//	// First attempt should trigger circuit breaker
//	result := client.Is("any-flag").Enabled()
//	if result != false {
//		t.Error("Expected false when circuit breaker is triggered")
//	}
//	if failures != 3 {
//		t.Errorf("Expected 3 failures before circuit breaker, got %d", failures)
//	}
//
//	// Immediate retry should not hit the server due to open circuit
//	initialFailures := failures
//	client.Is("any-flag").Enabled()
//	if failures != initialFailures {
//		t.Error("Circuit breaker failed to prevent requests")
//	}
//
//	// Wait for circuit to reset
//	time.Sleep(11 * time.Second)
//	client.Is("any-flag").Enabled()
//	if failures <= initialFailures {
//		t.Error("Circuit breaker failed to reset after timeout")
//	}
//}

func TestErrorHandling(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   string
	}{
		{
			name:       "invalid JSON",
			statusCode: http.StatusOK,
			response:   `{"invalid json"}`,
		},
		{
			name:       "server error",
			statusCode: http.StatusInternalServerError,
			response:   "",
		},
		{
			name:       "network timeout",
			statusCode: http.StatusOK,
			response:   "", // Will trigger timeout
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.name == "network timeout" {
					time.Sleep(2 * time.Second)
				}
				w.WriteHeader(tt.statusCode)
				_, _ = fmt.Fprintln(w, tt.response)
			}))
			defer server.Close()

			client := NewClient(WithBaseURL(server.URL), WithAuth(Auth{
				ProjectID:     "test-project",
				AgentID:       "test-agent",
				EnvironmentID: "test-environment",
			}))
			if tt.name == "network timeout" {
				client.httpClient.Timeout = 1 * time.Second
			}

			result := client.Is("test-flag").Enabled()
			if result != false {
				t.Error("Expected false for error condition")
			}
		})
	}
}

func TestConcurrentAccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := `{
			"intervalAllowed": 60,
			"secretMenu": {"sequence": ["b"]},
			"flags": [{"enabled": true, "details": {"name": "test-flag", "id": "1"}}]
		}`
		time.Sleep(100 * time.Millisecond) // Add some delay to increase chance of race conditions
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintln(w, response)
	}))
	defer server.Close()

	client := NewClient(WithBaseURL(server.URL), WithAuth(Auth{
		ProjectID:     "test-project",
		AgentID:       "test-agent",
		EnvironmentID: "test-environment",
	}))

	// Run concurrent flag checks
	concurrentRequests := 10
	done := make(chan bool)
	for i := 0; i < concurrentRequests; i++ {
		go func() {
			client.Is("test-flag").Enabled()
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < concurrentRequests; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Timeout waiting for concurrent requests")
		}
	}
}
