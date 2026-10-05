package flags

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestClient_Is(t *testing.T) {
	client := NewClient(WithMemory())
	flag := client.Is("test-flag")

	if flag.Name != "test-flag" {
		t.Errorf("Expected flag name to be 'test-flag', got %s", flag.Name)
	}
	if flag.Client != client {
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
		WithMemory(),
	)

	if client.baseURL != customURL {
		t.Errorf("Expected baseURL to be %s, got %s", customURL, client.baseURL)
	}
	if client.maxRetries != customRetries {
		t.Errorf("Expected maxRetries to be %d, got %d", customRetries, client.maxRetries)
	}
}

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
			}), WithMemory())
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

func TestUnconfiguredAuth_LocalOnly(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("FLAGS_UNCONFIGURED_OVERRIDE", "true")

	for name, auth := range map[string]Auth{
		"empty":           {},
		"missing env id":  {ProjectID: "p", AgentID: "a"},
		"missing project": {AgentID: "a", EnvironmentID: "e"},
	} {
		t.Run(name, func(t *testing.T) {
			client := NewClient(WithBaseURL(server.URL), WithAuth(auth), WithMemory())
			start := time.Now()
			if !client.Is("unconfigured_override").Enabled() {
				t.Error("env override should be honoured without auth")
			}
			if client.Is("unknown-flag").Enabled() {
				t.Error("unknown flag should be false without auth")
			}
			if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
				t.Errorf("checks took %v, want no retry/sleep without auth", elapsed)
			}
		})
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("API called %d times without auth, want 0", n)
	}
}
