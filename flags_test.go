package flags

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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
