package cache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/bugfixes/go-bugfixes/logs"
	"github.com/flags-gg/go-flags/flag"
	_ "modernc.org/sqlite"
	"time"
)

func getDBClient(db *sql.DB, fileName *string) (*sql.DB, error) {
	if db != nil {
		return db, nil
	}

	if fileName != nil {
		db, err := sql.Open("sqlite", fmt.Sprintf("%s?_pragma=busy_timeout=1000&pragma=journal_mode=WAL", *fileName))
		if err != nil {
			return nil, logs.Errorf("failed to open database: %v", err)
		}
		return db, nil
	}

	db, err := sql.Open("sqlite", "/tmp/flags.db?_pragma=busy_timeout=1000&pragma=journal_mode=WAL")
	if err != nil {
		return nil, logs.Errorf("failed to open database: %v", err)
	}
	return db, nil
}

type System struct {
	Context context.Context

	FileName *string
	DB       *sql.DB
}

func NewSystem() *System {
	return &System{
		Context: context.Background(),
	}
}

func (s *System) SetContext(ctx context.Context) {
	s.Context = ctx
}

func (s *System) SetFileName(fileName *string) {
	s.FileName = fileName
}

func (s *System) InitDB() error {
	db, err := getDBClient(s.DB, s.FileName)
	if err != nil {
		return logs.Errorf("failed to get database client: %v", err)
	}
	s.DB = db

	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		if err := db.Close(); err != nil {
			return logs.Errorf("failed to close database: %v", err)
		}
		return logs.Errorf("failed to enable foreign keys: %v", err)
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

func (s *System) deleteAllFlags() error {
	db, err := getDBClient(s.DB, s.FileName)
	if err != nil {
		return logs.Errorf("failed to get database client: %v", err)
	}
	s.DB = db

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
	if _, err := tx.Exec(`DELETE FROM flags`); err != nil {
		return logs.Errorf("failed to delete flags: %v", err)
	}

	return tx.Commit()
}

func (s *System) Refresh(flags []flag.FeatureFlag, intervalAllowed int) error {
	if err := s.deleteAllFlags(); err != nil {
		return err
	}

	db, err := getDBClient(s.DB, s.FileName)
	if err != nil {
		return logs.Errorf("failed to get database client: %v", err)
	}
	s.DB = db

	tx, err := db.Begin()
	if err != nil {
		return logs.Errorf("failed to begin transaction: %v", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO flags (name, enabled, updated_at) VALUES ($1, $2, $3)`)
	if err != nil {
		return logs.Errorf("failed to prepare statement: %v", err)

	}

	now := time.Now().Unix()
	for _, f := range flags {
		if _, err := stmt.Exec(f.Details.Name, f.Enabled, now); err != nil {
			return logs.Errorf("failed to insert flag: %v", err)
		}
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO cache_metadata(key, value) VALUES('next_refresh_time', ?), ('cache_ttl', ?)`, time.Now().Add(time.Duration(intervalAllowed)*time.Second).Unix(), intervalAllowed); err != nil {
		return logs.Errorf("failed to insert cache metadata: %v", err)
	}

	if err := tx.Commit(); err != nil {
		return logs.Errorf("failed to commit transaction: %v", err)
	}

	return nil
}

func (s *System) ShouldRefreshCache() bool {
	db, err := getDBClient(s.DB, s.FileName)
	if err != nil {
		return true
	}
	s.DB = db

	var nextRefreshTime int64
	if err := db.QueryRow(`SELECT CAST(value AS INTEGER) FROM cache_metadata WHERE key = 'next_refresh_time'`).Scan(&nextRefreshTime); err != nil {
		return true
	}

	return time.Now().Unix() > nextRefreshTime
}

func (s *System) Get(name string) (bool, bool) {
	db, err := getDBClient(s.DB, s.FileName)
	if err != nil {
		return false, false
	}
	s.DB = db

	var enabled bool
	if err := db.QueryRow(`SELECT enabled FROM flags WHERE name = $1 AND updated_at > (SELECT CAST(value AS INTEGER) FROM cache_metadata WHERE key = 'cache_ttl')`, name).Scan(&enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, false
		}
		return false, false
	}
	return enabled, true
}

func (s *System) GetAll() ([]flag.FeatureFlag, error) {
	db, err := getDBClient(s.DB, s.FileName)
	if err != nil {
		return nil, logs.Errorf("failed to get database client: %v", err)
	}
	s.DB = db

	defer func() {
		if err := db.Close(); err != nil {
			_ = logs.Errorf("failed to close database: %v", err)
		}
	}()

	var flags []flag.FeatureFlag
	rows, err := db.Query(`SELECT name, enabled FROM flags`)
	if err != nil {
		return nil, logs.Errorf("failed to query database: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			_ = logs.Errorf("failed to close database rows: %v", err)
		}
	}()

	for rows.Next() {
		var name string
		var enabled bool
		if err := rows.Scan(&name, &enabled); err != nil {
			return nil, logs.Errorf("failed to scan database rows: %v", err)
		}

		flags = append(flags, flag.FeatureFlag{
			Enabled: enabled,
			Details: flag.Details{
				Name: name,
			},
		})
	}

	return flags, nil
}
