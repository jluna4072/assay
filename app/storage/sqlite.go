package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"harness/backend"
	_ "modernc.org/sqlite"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

type SQLite struct{ db *sql.DB }

var _ backend.RunRepository = (*SQLite)(nil)

func Open(path string) (*SQLite, error) {
	if path == "" {
		return nil, fmt.Errorf("database path is required")
	}
	dsn := path
	if path != ":memory:" {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
			return nil, err
		}
		file, err := os.OpenFile(absolute, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
		dsn = (&url.URL{Scheme: "file", Path: absolute}).String()
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A single connection keeps connection-local pragmas active and serializes writers.
	db.SetMaxOpenConns(1)
	s := &SQLite{db: db}
	if err := s.initialize(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *SQLite) Close() error { return s.db.Close() }
func (s *SQLite) initialize() error {
	if _, err := s.db.Exec("PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000;"); err != nil {
		return err
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return fmt.Errorf("unsupported database schema version %d", version)
	}
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS drafts (id TEXT PRIMARY KEY, payload TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS versions (
 contract_id TEXT NOT NULL REFERENCES drafts(id), version INTEGER NOT NULL,
 payload TEXT NOT NULL, PRIMARY KEY(contract_id, version));
CREATE TABLE IF NOT EXISTS runs (
 id TEXT PRIMARY KEY, contract_id TEXT NOT NULL, version INTEGER NOT NULL,
 project_dir TEXT NOT NULL, status TEXT NOT NULL, created_at TEXT NOT NULL,
 FOREIGN KEY(contract_id, version) REFERENCES versions(contract_id, version));
CREATE TABLE IF NOT EXISTS attempts (
 run_id TEXT NOT NULL REFERENCES runs(id), number INTEGER NOT NULL,
 started_at TEXT NOT NULL, completed_at TEXT NOT NULL, status TEXT NOT NULL,
 PRIMARY KEY(run_id, number));
CREATE TABLE IF NOT EXISTS rule_results (
 run_id TEXT NOT NULL, attempt_number INTEGER NOT NULL, position INTEGER NOT NULL,
 rule_id TEXT NOT NULL, payload TEXT NOT NULL,
 PRIMARY KEY(run_id, attempt_number, position), UNIQUE(run_id, attempt_number, rule_id),
 FOREIGN KEY(run_id, attempt_number) REFERENCES attempts(run_id, number));
CREATE TRIGGER IF NOT EXISTS versions_no_update BEFORE UPDATE ON versions
BEGIN SELECT RAISE(ABORT, 'published versions are immutable'); END;
CREATE TRIGGER IF NOT EXISTS versions_no_delete BEFORE DELETE ON versions
BEGIN SELECT RAISE(ABORT, 'published versions are immutable'); END;
PRAGMA user_version = 1;
`)
	return err
}
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return backend.ErrNotFound
	}
	return err
}
func (s *SQLite) SaveDraft(c backend.Contract) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO drafts(id,payload) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload", c.ID, string(data))
	return err
}
func (s *SQLite) GetDraft(id string) (c backend.Contract, err error) {
	var data string
	if err = s.db.QueryRow("SELECT payload FROM drafts WHERE id=?", id).Scan(&data); err != nil {
		return c, notFound(err)
	}
	err = json.Unmarshal([]byte(data), &c)
	return
}
func (s *SQLite) ListContracts() ([]backend.Contract, error) {
	rows, err := s.db.Query("SELECT payload FROM drafts ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []backend.Contract{}
	for rows.Next() {
		var data string
		var c backend.Contract
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}
func (s *SQLite) CreateVersion(c backend.Contract, at time.Time) (backend.PublishedContract, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return backend.PublishedContract{}, err
	}
	defer tx.Rollback()
	var next int
	if err := tx.QueryRow("SELECT COALESCE(MAX(version),0)+1 FROM versions WHERE contract_id=?", c.ID).Scan(&next); err != nil {
		return backend.PublishedContract{}, err
	}
	published := backend.PublishedContract{Contract: c, Version: next, PublishedAt: at}
	data, err := json.Marshal(published)
	if err != nil {
		return backend.PublishedContract{}, err
	}
	if _, err := tx.Exec("INSERT INTO versions(contract_id,version,payload) VALUES(?,?,?)", c.ID, next, string(data)); err != nil {
		return backend.PublishedContract{}, err
	}
	if err := tx.Commit(); err != nil {
		return backend.PublishedContract{}, err
	}
	var snapshot backend.PublishedContract
	err = json.Unmarshal(data, &snapshot)
	return snapshot, err
}
func (s *SQLite) GetVersion(id string, version int) (v backend.PublishedContract, err error) {
	var data string
	if err = s.db.QueryRow("SELECT payload FROM versions WHERE contract_id=? AND version=?", id, version).Scan(&data); err != nil {
		return v, notFound(err)
	}
	err = json.Unmarshal([]byte(data), &v)
	return
}
func (s *SQLite) ListVersions(id string) ([]backend.PublishedContract, error) {
	rows, err := s.db.Query("SELECT payload FROM versions WHERE contract_id=? ORDER BY version", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []backend.PublishedContract{}
	for rows.Next() {
		var data string
		var v backend.PublishedContract
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(data), &v); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}
func (s *SQLite) CreateRun(r backend.Run) error {
	_, err := s.db.Exec("INSERT INTO runs(id,contract_id,version,project_dir,status,created_at) VALUES(?,?,?,?,?,?)", r.ID, r.ContractID, r.Version, r.ProjectDir, r.Status, r.CreatedAt.Format(time.RFC3339Nano))
	return err
}
func scanRun(row interface{ Scan(...any) error }) (r backend.Run, err error) {
	var created string
	err = row.Scan(&r.ID, &r.ContractID, &r.Version, &r.ProjectDir, &r.Status, &created)
	if err != nil {
		return r, notFound(err)
	}
	r.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	r.Attempts = []backend.Attempt{}
	return
}
func (s *SQLite) GetRun(id string) (backend.Run, error) {
	run, err := scanRun(s.db.QueryRow("SELECT id,contract_id,version,project_dir,status,created_at FROM runs WHERE id=?", id))
	if err != nil {
		return run, err
	}
	rows, err := s.db.Query(`SELECT a.number,a.started_at,a.completed_at,a.status,r.payload
FROM attempts a LEFT JOIN rule_results r ON a.run_id=r.run_id AND a.number=r.attempt_number
WHERE a.run_id=? ORDER BY a.number,r.position`, id)
	if err != nil {
		return run, err
	}
	defer rows.Close()
	for rows.Next() {
		var number int
		var started, completed string
		var status backend.ResultStatus
		var payload sql.NullString
		if err := rows.Scan(&number, &started, &completed, &status, &payload); err != nil {
			return run, err
		}
		if len(run.Attempts) == 0 || run.Attempts[len(run.Attempts)-1].Number != number {
			start, err := time.Parse(time.RFC3339Nano, started)
			if err != nil {
				return run, err
			}
			end, err := time.Parse(time.RFC3339Nano, completed)
			if err != nil {
				return run, err
			}
			run.Attempts = append(run.Attempts, backend.Attempt{Number: number, StartedAt: start, CompletedAt: end, Status: status, Results: []backend.RuleResult{}})
		}
		if payload.Valid {
			var result backend.RuleResult
			if err := json.Unmarshal([]byte(payload.String), &result); err != nil {
				return run, err
			}
			i := len(run.Attempts) - 1
			run.Attempts[i].Results = append(run.Attempts[i].Results, result)
		}
	}
	return run, rows.Err()
}
func (s *SQLite) ListRuns() ([]backend.Run, error) {
	rows, err := s.db.Query("SELECT id,contract_id,version,project_dir,status,created_at FROM runs ORDER BY created_at DESC,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []backend.Run{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, run)
	}
	return result, rows.Err()
}
func (s *SQLite) AppendAttempt(id string, attempt backend.Attempt) (backend.Run, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return backend.Run{}, err
	}
	defer tx.Rollback()
	var exists string
	if err := tx.QueryRow("SELECT id FROM runs WHERE id=?", id).Scan(&exists); err != nil {
		return backend.Run{}, notFound(err)
	}
	var number int
	if err := tx.QueryRow("SELECT COALESCE(MAX(number),0)+1 FROM attempts WHERE run_id=?", id).Scan(&number); err != nil {
		return backend.Run{}, err
	}
	if _, err := tx.Exec("INSERT INTO attempts(run_id,number,started_at,completed_at,status) VALUES(?,?,?,?,?)", id, number, attempt.StartedAt.Format(time.RFC3339Nano), attempt.CompletedAt.Format(time.RFC3339Nano), attempt.Status); err != nil {
		return backend.Run{}, err
	}
	for position, result := range attempt.Results {
		payload, err := json.Marshal(result)
		if err != nil {
			return backend.Run{}, err
		}
		if _, err := tx.Exec("INSERT INTO rule_results(run_id,attempt_number,position,rule_id,payload) VALUES(?,?,?,?,?)", id, number, position, result.RuleID, string(payload)); err != nil {
			return backend.Run{}, err
		}
	}
	if _, err := tx.Exec("UPDATE runs SET status=? WHERE id=?", attempt.Status, id); err != nil {
		return backend.Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return backend.Run{}, err
	}
	return s.GetRun(id)
}
