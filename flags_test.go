package flags

import "testing"

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
