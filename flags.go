package flags

import (
	"encoding/json"
	"fmt"
	"github.com/bugfixes/go-bugfixes/logs"
	"net/http"
	"sync"
	"time"
)

const (
	baseURL    = "https://api.flags.gg/v1"
	maxRetries = 3
)

type Auth struct {
	ProjectID     string
	AgentID       string
	EnvironmentID string
}

type Flag struct {
	Name   string
	client *Client
}

type Client struct {
	baseURL      string
	httpClient   *http.Client
	cache        *Cache
	maxRetries   int
	mutex        *sync.RWMutex
	circuitState CircuitState
	auth         Auth
}

type Cache struct {
	flags           map[string]bool
	nextRefreshTime time.Time
	mutex           sync.RWMutex
}

type CircuitState struct {
	isOpen       bool
	failureCount int
	lastFailure  time.Time
}

type ApiResponse struct {
	IntervalAllowed int           `json:"intervalAllowed"`
	SecretMenu      SecretMenu    `json:"secretMenu"`
	Flags           []FeatureFlag `json:"flags"`
}
type FlagDetails struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}
type SecretMenu struct {
	Sequence []string `json:"sequence"`
}
type FeatureFlag struct {
	Enabled bool        `json:"enabled"`
	Details FlagDetails `json:"details"`
}

type Option func(*Client)

func NewClient(opts ...Option) *Client {
	client := &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		cache: &Cache{
			flags:           make(map[string]bool),
			nextRefreshTime: time.Now(),
		},
		maxRetries: maxRetries,
	}

	for _, opt := range opts {
		opt(client)
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

func (c *Client) Is(name string) *Flag {
	return &Flag{
		Name:   name,
		client: c,
	}
}

func (f *Flag) Enabled() bool {
	return f.client.isEnabled(f.Name)
}

func (c *Client) isEnabled(name string) bool {
	if c.shouldRefreshCache() {
		c.refreshCache()
	}

	enabled, exists := c.checkCache(name)
	if !exists {
		return false
	}
	return enabled
}

func (c *Client) shouldRefreshCache() bool {
	c.cache.mutex.RLock()
	defer c.cache.mutex.RUnlock()
	return time.Now().After(c.cache.nextRefreshTime)
}

func (c *Client) checkCache(name string) (bool, bool) {
	c.cache.mutex.RLock()
	defer c.cache.mutex.RUnlock()
	enabled, exists := c.cache.flags[name]
	return enabled, exists
}

func (c *Client) refreshCache() {
	if c.circuitState.isOpen {
		if time.Since(c.circuitState.lastFailure) < time.Minute {
			return
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
			return
		}

		time.Sleep(time.Duration(retry+1) * time.Second)
	}

	if err != nil {
		return
	}

	c.cache.mutex.Lock()
	defer c.cache.mutex.Unlock()

	newFlags := make(map[string]bool)
	if apiResp == nil {
		return
	}
	for _, flag := range apiResp.Flags {
		newFlags[flag.Details.Name] = flag.Enabled
	}

	c.cache.flags = newFlags
	c.cache.nextRefreshTime = time.Now().Add(time.Duration(apiResp.IntervalAllowed) * time.Second)
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
