package flags

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestFeatureFlags_Memory(t *testing.T) {
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
	}), WithMemory())

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

func TestLocalFeatureFlags_Memory(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := `{
			"intervalAllowed": 60,
			"secretMenu": {"sequence": ["b","b","b"]},
			"flags": [
				{"enabled": true, "details": {"name": "enabled-flag", "id": "1"}},
				{"enabled": false, "details": {"name": "disabled-flag", "id": "2"}},
				{"enabled": true, "details": {"name": "local-flag", "id": "3"}},
				{"enabled": false, "details": {"name": "local-override-flag", "id": "4"}}
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
	}), WithMemory())

	// set the local flag to false
	if err := os.Setenv("FLAGS_LOCAL_FLAG", "false"); err != nil {
		t.Error(err)
	}

	// set the local override flag to true
	if err := os.Setenv("FLAGS_LOCAL_OVERRIDE_FLAG", "true"); err != nil {
		t.Error(err)
	}

	if err := os.Setenv("FLAGS_LOCAL_SPACE_FLAG", "true"); err != nil {
		t.Error(err)
	}

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
		{
			name:     "local flag returns false",
			flagName: "local-flag",
			want:     false,
		},
		{
			name:     "local override flag returns true",
			flagName: "local-override-flag",
			want:     true,
		},
		{
			name:     "local space flag returns true",
			flagName: "local space flag",
			want:     true,
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

func TestConcurrentAccess_Memory(t *testing.T) {
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
	}), WithMemory())

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
