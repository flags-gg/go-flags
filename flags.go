package flags

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bugfixes/go-bugfixes/logs"
	"net/http"
	"sync"
	"time"

	_ "github.com/ncruces/go-sqlite3"
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
	maxRetries   int
	mutex        *sync.RWMutex
	circuitState CircuitState
	auth         Auth
	db           *sql.DB
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

func NewClient(opts ...Option) (*Client, error) {
	db, err := sql.Open("sqlite3", "/tmp/flags.db")
	if err != nil {
		return nil, logs.Errorf("failed to open database: %v", err)
	}
	if err := initDB(db); err != nil {
		defer func() {
			if err := db.Close(); err != nil {
				_ = logs.Errorf("failed to close database: %v", err)
			}
		}()
		return nil, logs.Errorf("failed to initialize database: %v", err)
	}

	client := &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		maxRetries: maxRetries,
	}

	for _, opt := range opts {
		opt(client)
	}
	return client, nil
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

func initDB(db *sql.DB) error {
	_, err := db.Exec(`
    CREATE TABLE IF NOT EXISTS flags (
        name TEXT PRIMARY KEY,
        enabled BOOLEAN NOT NULL DEFAULT FALSE,
        updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
    )`)
	if err != nil {
		return logs.Errorf("failed to create flags table: %v", err)
	}

	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS cache_metadata (
		key TEXT PRIMARY KEY,
		value TEXT,
	)`)
	if err != nil {
		return logs.Errorf("failed to create cache_metadata table: %v", err)
	}
	return nil
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
	var nextRefreshTime time.Time
	if err := c.db.QueryRow(`SELECT value FROM cache_metadata WHERE key = 'next_refresh_time'`).Scan(&nextRefreshTime); err != nil {
		return true
	}

	refreshTime, err := time.Parse(time.RFC3339, nextRefreshTime.String())
	if err != nil {
		return true
	}

	return time.Now().After(refreshTime)
}

func (c *Client) checkCache(name string) (bool, bool) {
	var enabled bool
	if err := c.db.QueryRow(`SELECT enabled FROM flags WHERE name = $1`, name).Scan(&enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, false
		}
		return false, true
	}

	return enabled, true
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

	if err != nil || apiResp == nil {
		return
	}

	tx, err := c.db.Begin()
	if err != nil {
		return
	}
	defer func() {
		if err := tx.Rollback(); err != nil {
			_ = logs.Errorf("failed to rollback transaction: %v", err)
		}
	}()

	if _, err := tx.Exec(`DELETE FROM flags`); err != nil {
		return
	}

	stmt, err := tx.Prepare(`INSERT INTO flags (name, enabled, updated_at) VALUES ($1, $2, $3)`)
	if err != nil {
		return
	}
	defer func() {
		if err := stmt.Close(); err != nil {
			_ = logs.Errorf("failed to close statement: %v", err)
		}
	}()
	now := time.Now()
	for _, flag := range apiResp.Flags {
		if _, err := stmt.Exec(flag.Details.Name, flag.Enabled, now); err != nil {
			return
		}
	}

	nextRefreshTime := time.Now().Add(time.Duration(apiResp.IntervalAllowed) * time.Second)
	if _, err := tx.Exec(`INSERT INTO cache_metadata (key, value) VALUES ('next_refresh_time', $1)`, nextRefreshTime.Format(time.RFC3339)); err != nil {
		return
	}

	if err := tx.Commit(); err != nil {
		return
	}
}

func (c *Client) fetchFlags() (*ApiResponse, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf("%s/flags", c.baseURL), nil)
	if err != nil {
		return nil, err
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
		return nil, logs.Errorf("failed to make request: %v", err)
	}
	defer func() {
		if resp != nil && resp.Body != nil {
			if err := resp.Body.Close(); err != nil {
				_ = logs.Errorf("failed to close response body: %v", err)
			}
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, logs.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var apiResp ApiResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, logs.Errorf("failed to decode response: %v", err)
	}
	return &apiResp, nil
}
