package flags

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/bugfixes/go-bugfixes/logs"
	"github.com/flags-gg/go-flags/cache"
	"github.com/flags-gg/go-flags/flag"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	baseURL    = "https://api.flags.gg"
	maxRetries = 3
)

type Auth struct {
	ProjectID     string
	AgentID       string
	EnvironmentID string
}

type Flag struct {
	Name   string
	Client *Client
}

type Client struct {
	baseURL      string
	httpClient   *http.Client
	Cache        *cache.System
	maxRetries   int
	mutex        *sync.RWMutex
	circuitState CircuitState
	auth         Auth
}

type CircuitState struct {
	isOpen       bool
	failureCount int
	lastFailure  time.Time
}

type ApiResponse struct {
	IntervalAllowed int                `json:"intervalAllowed"`
	Flags           []flag.FeatureFlag `json:"flags"`
}
type Option func(*Client)

func NewClient(opts ...Option) *Client {
	c := cache.NewSystem()

	client := &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		Cache:      c,
		maxRetries: maxRetries,
		mutex:      &sync.RWMutex{},
		circuitState: CircuitState{
			isOpen:       false,
			failureCount: 0,
		},
	}

	for _, opt := range opts {
		opt(client)
	}
	c.SetContext(context.Background())
	if err := c.InitDB(); err != nil {
		_ = logs.Errorf("failed to initialize database: %v", err)
		return nil
	}

	return client
}

func WithBaseURL(baseURL string) Option {
	return func(c *Client) {
		c.baseURL = baseURL
	}
}
func WithMaxRetries(maxRetries int) Option {
	return func(c *Client) {
		c.maxRetries = maxRetries
	}
}
func WithAuth(auth Auth) Option {
	return func(c *Client) {
		c.auth = auth
	}
}
func SetFileName(fileName *string) Option {
	return func(c *Client) {
		c.Cache.SetFileName(fileName)
	}
}

func (c *Client) Is(name string) *Flag {
	return &Flag{
		Name:   name,
		Client: c,
	}
}

// List get all flags rather than just the one for the flag itself
func (c *Client) List() ([]flag.FeatureFlag, error) {
	flags, err := c.Cache.GetAll()
	if err != nil {
		return nil, err
	}

	return flags, nil
}

// Enabled flag specific
func (f *Flag) Enabled() bool {
	return f.Client.isEnabled(f.Name)
}

func (c *Client) isEnabled(name string) bool {
	if c.Cache.ShouldRefreshCache() {
		if err := c.refetch(); err != nil {
			_ = logs.Errorf("failed to refetch flags: %v", err)
			return false
		}
	}

	// check local
	localFlags := buildLocal()
	for lname, enabled := range localFlags {
		if lname == name {
			return enabled
		}
	}

	// check cache
	enabled, exists := c.Cache.Get(name)
	if !exists {
		return false
	}
	return enabled
}

func (c *Client) fetchFlags() (*ApiResponse, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf("%s/flags", c.baseURL), nil)
	if err != nil {
		return nil, logs.Errorf("failed to build request %v", err)
	}
	req.Header.Set("User-Agent", "Flags-Go")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	if c.auth.ProjectID == "" {
		return nil, logs.Error("project ID is required")
	}
	if c.auth.AgentID == "" {
		return nil, logs.Error("agent ID is required")
	}
	if c.auth.EnvironmentID == "" {
		return nil, logs.Error("environment ID is required")
	}

	req.Header.Set("X-Project-ID", c.auth.ProjectID)
	req.Header.Set("X-Agent-ID", c.auth.AgentID)
	req.Header.Set("X-Environment-ID", c.auth.EnvironmentID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, logs.Errorf("failed to execute request: %v", err)
	}
	defer func() {
		if resp != nil && resp.Body != nil {
			if err := resp.Body.Close(); err != nil {
				_ = logs.Errorf("error closing response body: %v", err)
			}
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, logs.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var apiResp ApiResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, logs.Errorf("failed to decode body %v", err)
	}
	return &apiResp, nil
}

func (c *Client) refetch() error {
	if c.circuitState.isOpen {
		if time.Since(c.circuitState.lastFailure) < 10*time.Second {
			return nil
		}
		c.circuitState.isOpen = false
		c.circuitState.failureCount = 0
	}

	var apiResp *ApiResponse
	var err error
	for retry := 0; retry < c.maxRetries; retry++ {
		apiResp, err = c.fetchFlags()
		if err == nil {
			c.circuitState.failureCount = 0
			break
		}

		c.circuitState.failureCount++
		if c.circuitState.failureCount >= c.maxRetries {
			c.circuitState.isOpen = true
			c.circuitState.lastFailure = time.Now()
			return nil
		}

		time.Sleep(time.Duration(retry+1) * time.Second)
	}

	if err != nil || apiResp == nil {
		return logs.Errorf("failed to fetch flags: %v", err)
	}

	if err := c.Cache.Refresh(apiResp.Flags, apiResp.IntervalAllowed); err != nil {
		return logs.Errorf("failed to set cache: %v", err)
	}

	return nil
}

func buildLocal() map[string]bool {
	col := make(map[string]bool)
	for _, e := range os.Environ() {
		pair := strings.SplitN(e, "=", 2)
		if len(pair) != 2 {
			continue
		}

		key, val := pair[0], pair[1]
		if !strings.HasPrefix(key, "FLAGS_") {
			continue
		}

		colKey := strings.ToLower(strings.TrimPrefix(key, "FLAGS_"))
		col[colKey] = val == "true"

		// replace _ with -
		colKeyUnderscore := strings.ReplaceAll(colKey, "_", "-")
		col[colKeyUnderscore] = val == "true"

		// replace _ with <space>
		colKeySpace := strings.ReplaceAll(colKey, "_", " ")
		col[colKeySpace] = val == "true"
	}

	return col
}
