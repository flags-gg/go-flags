package flags

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bugfixes/go-bugfixes/logs"
	_ "modernc.org/sqlite"
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
	db           *sql.DB
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

func getDBClient(db *sql.DB) (*sql.DB, error) {
	if db != nil {
		return db, nil
	}

	db, err := sql.Open("sqlite", "/tmp/flags.db?_pragma=busy_timeout=1000&pragma=journal_mode=WAL")
	if err != nil {
		return nil, logs.Errorf("failed to open database: %v", err)
	}
	return db, nil
}

func NewClient(opts ...Option) *Client {
	db, err := getDBClient(nil)
	if err != nil {
		return nil
	}

	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		_ = logs.Errorf("failed to enable foreign keys: %v", err)
		if err := db.Close(); err != nil {
			_ = logs.Errorf("failed to close database: %v", err)
		}
		return nil
	}
	if err := initDB(db); err != nil {
		_ = logs.Errorf("failed to initialize database: %v", err)
		if err := db.Close(); err != nil {
			_ = logs.Errorf("failed to close database: %v", err)
		}
		return nil
	}
	// Sleep to allow the database to initialize
	time.Sleep(5 * time.Second)

	client := &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		db:         db,
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

func initDB(db *sql.DB) error {
	db, err := getDBClient(db)
	if err != nil {
		return logs.Errorf("failed to get database client: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		return logs.Errorf("failed to begin transaction: %v", err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil {
			if !errors.Is(err, sql.ErrTxDone) {
				_ = logs.Errorf("failed to rollback transaction: %v", err)
			}
		}
	}()

	if _, err := tx.Exec(`
    CREATE TABLE IF NOT EXISTS flags (
        name TEXT PRIMARY KEY,
        enabled BOOLEAN NOT NULL DEFAULT FALSE,
        updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
    )`); err != nil {
		return logs.Errorf("failed to create flags table: %v", err)
	}

	if _, err := tx.Exec(`
	CREATE TABLE IF NOT EXISTS cache_metadata (
		key TEXT PRIMARY KEY,
		value TEXT
	)`); err != nil {
		return logs.Errorf("failed to create cache_metadata table: %v", err)
	}

	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_flags_updated ON flags(updated_at)`); err != nil {
		return logs.Errorf("failed to create index: %v", err)
	}

	return tx.Commit()
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
	db, err := getDBClient(c.db)
	if err != nil {
		return true
	}

	var nextRefreshTime int64
	if err := db.QueryRow(`SELECT CAST(value AS INTEGER) FROM cache_metadata WHERE key = 'next_refresh_time'`).Scan(&nextRefreshTime); err != nil {
		return true
	}

	return time.Now().Unix() > nextRefreshTime
}

func (c *Client) checkCache(name string) (bool, bool) {
	db, err := getDBClient(c.db)
	if err != nil {
		return false, false
	}

	var enabled bool
	if err := db.QueryRow(`SELECT enabled FROM flags WHERE name = $1 AND updated_at > (SELECT CAST(value AS INTEGER) FROM cache_metadata WHERE key = 'cache_ttl')`, name).Scan(&enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, false
		}
		return false, false
	}
	return enabled, true
}

func (c *Client) refreshCache() {
	db, err := getDBClient(c.db)
	if err != nil {
		_ = logs.Errorf("failed to get database client: %v", err)
		return
	}

	if c.circuitState.isOpen {
		if time.Since(c.circuitState.lastFailure) < 10*time.Second {
			return
		}
		c.circuitState.isOpen = false
		c.circuitState.failureCount = 0
	}

	var apiResp *ApiResponse
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
		_ = logs.Errorf("failed to fetch flags: %v", err)
		return
	}

	tx, err := db.Begin()
	if err != nil {
		_ = logs.Errorf("failed to begin transaction: %v", err)
		return
	}
	defer func() {
		if err := tx.Rollback(); err != nil {
			if !errors.Is(err, sql.ErrTxDone) {
				_ = logs.Errorf("failed to rollback transaction: %v", err)
			}
		}
	}()
	if _, err := tx.Exec(`DELETE FROM flags`); err != nil {
		_ = logs.Errorf("failed to delete flags: %v", err)
		return
	}
	stmt, err := tx.Prepare(`INSERT INTO flags (name, enabled, updated_at) VALUES ($1, $2, $3)`)
	if err != nil {
		_ = logs.Errorf("failed to prepare statement: %v", err)
		return
	}
	defer func() {
		if err := stmt.Close(); err != nil {
			_ = logs.Errorf("failed to close statement: %v", err)
		}
	}()

	now := time.Now().Unix()
	for _, flag := range apiResp.Flags {
		if _, err := stmt.Exec(flag.Details.Name, flag.Enabled, now); err != nil {
			_ = logs.Errorf("failed to insert flag: %v", err)
			return
		}
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO cache_metadata(key, value) VALUES('next_refresh_time', ?), ('cache_ttl', ?)`, time.Now().Add(time.Duration(apiResp.IntervalAllowed)*time.Second).Unix(), apiResp.IntervalAllowed); err != nil {
		_ = logs.Errorf("failed to insert cache metadata: %v", err)
		return
	}

	if err := tx.Commit(); err != nil {
		_ = logs.Errorf("failed to commit transaction: %v", err)
		return
	}
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
