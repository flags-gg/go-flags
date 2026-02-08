package flags

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bugfixes/go-bugfixes/logs"
	"github.com/flags-gg/go-flags/cache"
	"github.com/flags-gg/go-flags/flag"
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

	// Async support
	backgroundRefresh bool
	cancel            context.CancelFunc
	refreshInProgress atomic.Bool
	refreshInterval   atomic.Int64
	onError           func(error)
	bgDone            chan struct{}
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
	if !c.IsMemory {
		c.CacheSystem = cache.NewSQLLite(c.FileName)
	}

	if err := c.CacheSystem.Init(); err != nil {
		_ = logs.Errorf("failed to initialize database: %v", err)
		return nil
	}

	client.refreshInterval.Store(60) // default interval

	if client.backgroundRefresh {
		ctx, cancel := context.WithCancel(context.Background())
		client.cancel = cancel
		client.bgDone = make(chan struct{})
		go client.startBackgroundRefresh(ctx)
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
func WithMemory() Option {
	return func(c *Client) {
		c.Cache.NewMemory()
	}
}
func WithBackgroundRefresh() Option {
	return func(c *Client) {
		c.backgroundRefresh = true
	}
}
func WithErrorHandler(fn func(error)) Option {
	return func(c *Client) {
		c.onError = fn
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
	flags, err := c.Cache.CacheSystem.GetAll()
	if err != nil {
		return nil, err
	}

	return flags, nil
}

// Close stops the background refresh goroutine and waits for it to finish.
func (c *Client) Close() {
	if c.cancel != nil {
		c.cancel()
		<-c.bgDone
	}
}

func (c *Client) startBackgroundRefresh(ctx context.Context) {
	defer close(c.bgDone)

	// Initial fetch
	if err := c.refetch(); err != nil {
		c.reportError(err)
	}

	interval := time.Duration(c.refreshInterval.Load()) * time.Second
	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := c.refetch(); err != nil {
				c.reportError(err)
			}
			interval = time.Duration(c.refreshInterval.Load()) * time.Second
			timer.Reset(interval)
		}
	}
}

func (c *Client) reportError(err error) {
	if err != nil && c.onError != nil {
		c.onError(err)
	}
}

// Enabled flag specific
func (f *Flag) Enabled() bool {
	return f.Client.isEnabled(f.Name)
}

func (c *Client) isEnabled(name string) bool {
	name = strings.ToLower(name) // force to lowercase

	// In background mode, the goroutine handles refreshes — never block here
	if !c.backgroundRefresh && c.Cache.CacheSystem.ShouldRefreshCache() {
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
	enabled, exists := c.Cache.CacheSystem.Get(name)
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
	// Deduplication: only one goroutine refreshes at a time
	if !c.refreshInProgress.CompareAndSwap(false, true) {
		return nil
	}
	defer c.refreshInProgress.Store(false)

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
			c.reportError(fmt.Errorf("circuit breaker opened after %d failures: %w", c.circuitState.failureCount, err))
			return nil
		}

		time.Sleep(time.Duration(retry+1) * time.Second)
	}

	if err != nil || apiResp == nil {
		return logs.Errorf("failed to fetch flags: %v", err)
	}

	if apiResp.IntervalAllowed > 0 {
		c.refreshInterval.Store(int64(apiResp.IntervalAllowed))
	}

	var flags []flag.FeatureFlag
	for _, f := range apiResp.Flags {
		ff := flag.FeatureFlag{
			Enabled: f.Enabled,
			Details: flag.Details{
				Name: strings.ToLower(f.Details.Name),
				ID:   f.Details.ID,
			},
		}
		flags = append(flags, ff)
	}

	if err := c.Cache.CacheSystem.Refresh(flags, apiResp.IntervalAllowed); err != nil {
		return logs.Errorf("failed to set cache: %v", err)
	}

	return nil
}

// GetMultiple returns a map of flag names to their enabled status.
func (c *Client) GetMultiple(names ...string) map[string]bool {
	result := make(map[string]bool, len(names))

	// Single refresh check for all flags
	if !c.backgroundRefresh && c.Cache.CacheSystem.ShouldRefreshCache() {
		if err := c.refetch(); err != nil {
			_ = logs.Errorf("failed to refetch flags: %v", err)
		}
	}

	localFlags := buildLocal()
	for _, name := range names {
		name = strings.ToLower(name)

		if enabled, ok := localFlags[name]; ok {
			result[name] = enabled
			continue
		}

		enabled, exists := c.Cache.CacheSystem.Get(name)
		if exists {
			result[name] = enabled
		} else {
			result[name] = false
		}
	}

	return result
}

// AllEnabled returns true if all named flags are enabled.
func (c *Client) AllEnabled(names ...string) bool {
	flags := c.GetMultiple(names...)
	for _, enabled := range flags {
		if !enabled {
			return false
		}
	}
	return true
}

// AnyEnabled returns true if any named flag is enabled.
func (c *Client) AnyEnabled(names ...string) bool {
	flags := c.GetMultiple(names...)
	for _, enabled := range flags {
		if enabled {
			return true
		}
	}
	return false
}

func buildLocal() map[string]bool {
	col := make(map[string]bool, len(os.Environ()))
	for _, e := range os.Environ() {
		pair := strings.SplitN(e, "=", 2)
		if len(pair) != 2 {
			continue
		}

		key, val := pair[0], pair[1]
		if !strings.HasPrefix(key, "FLAGS_") {
			continue
		}

		value := val == "true"

		colKey := strings.ToLower(strings.TrimPrefix(key, "FLAGS_"))
		col[colKey] = value
		col[strings.ReplaceAll(colKey, "_", "-")] = value
		col[strings.ReplaceAll(colKey, "_", " ")] = value
	}

	return col
}
