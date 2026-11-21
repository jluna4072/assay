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

// SchemaVersion is the database layout this build creates and reads.
// Version 2 added run baselines and the evidence blob store; version 3,
// work runs and the worker that made each attempt; version 4, the events
// of work runs, so another process can follow one.
const SchemaVersion = 4

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
	// WAL lets "harness show --follow" read while "harness work" writes.
	if _, err := s.db.Exec("PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000; PRAGMA journal_mode = WAL;"); err != nil {
		return err
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > SchemaVersion {
		return fmt.Errorf("unsupported database schema version %d (this harness supports up to %d); upgrade harness", version, SchemaVersion)
	}
	if version == SchemaVersion {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
CREATE TABLE IF NOT EXISTS drafts (id TEXT PRIMARY KEY, payload TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS versions (
 contract_id TEXT NOT NULL REFERENCES drafts(id), version INTEGER NOT NULL,
 payload TEXT NOT NULL, PRIMARY KEY(contract_id, version));
CREATE TABLE IF NOT EXISTS runs (
 id TEXT PRIMARY KEY, contract_id TEXT NOT NULL, version INTEGER NOT NULL,
 project_dir TEXT NOT NULL, status TEXT NOT NULL, created_at TEXT NOT NULL,
 baseline TEXT NOT NULL DEFAULT '', work TEXT NOT NULL DEFAULT '',
 FOREIGN KEY(contract_id, version) REFERENCES versions(contract_id, version));
CREATE TABLE IF NOT EXISTS attempts (
 run_id TEXT NOT NULL REFERENCES runs(id), number INTEGER NOT NULL,
 started_at TEXT NOT NULL, completed_at TEXT NOT NULL, status TEXT NOT NULL,
 worker TEXT NOT NULL DEFAULT '',
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
CREATE TABLE IF NOT EXISTS blobs (hash TEXT PRIMARY KEY, data BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS events (
 run_id TEXT NOT NULL REFERENCES runs(id), seq INTEGER NOT NULL, payload TEXT NOT NULL,
 PRIMARY KEY(run_id, seq));
CREATE TRIGGER IF NOT EXISTS blobs_no_update BEFORE UPDATE ON blobs
BEGIN SELECT RAISE(ABORT, 'evidence is immutable'); END;
`)
	if err != nil {
		return err
	}
	// CREATE TABLE IF NOT EXISTS above leaves existing tables as they were,
	// so add the columns later versions introduced.
	if version >= 1 && version < 2 {
		if _, err := tx.Exec("ALTER TABLE runs ADD COLUMN baseline TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("upgrade schema to version 2: %w", err)
		}
	}
	if version >= 1 && version < 3 {
		for _, statement := range []string{
			"ALTER TABLE runs ADD COLUMN work TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE attempts ADD COLUMN worker TEXT NOT NULL DEFAULT ''",
		} {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("upgrade schema to version 3: %w", err)
			}
		}
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
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

// optionalJSON encodes v, or "" for nil.
func optionalJSON[T any](v *T) (string, error) {
	if v == nil {
		return "", nil
	}
	data, err := json.Marshal(v)
	return string(data), err
}

// decodeOptional decodes data into a new T, or returns nil for "".
func decodeOptional[T any](data string) (*T, error) {
	if data == "" {
		return nil, nil
	}
	v := new(T)
	return v, json.Unmarshal([]byte(data), v)
}

func (s *SQLite) CreateRun(r backend.Run) error {
	work, err := optionalJSON(r.Work)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO runs(id,contract_id,version,project_dir,baseline,status,created_at,work) VALUES(?,?,?,?,?,?,?,?)", r.ID, r.ContractID, r.Version, r.ProjectDir, r.Baseline, r.Status, r.CreatedAt.Format(time.RFC3339Nano), work)
	return err
}

// UpdateWork replaces a work run's Work record.
func (s *SQLite) UpdateWork(id string, work backend.Work) error {
	data, err := json.Marshal(work)
	if err != nil {
		return err
	}
	result, err := s.db.Exec("UPDATE runs SET work=? WHERE id=?", string(data), id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return backend.ErrNotFound
	}
	return nil
}

func scanRun(row interface{ Scan(...any) error }) (r backend.Run, err error) {
	var created, work string
	err = row.Scan(&r.ID, &r.ContractID, &r.Version, &r.ProjectDir, &r.Baseline, &r.Status, &created, &work)
	if err != nil {
		return r, notFound(err)
	}
	r.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return r, err
	}
	r.Work, err = decodeOptional[backend.Work](work)
	r.Attempts = []backend.Attempt{}
	return
}
func (s *SQLite) GetRun(id string) (backend.Run, error) {
	run := backend.Run{Attempts: []backend.Attempt{}}
	// Read status and history in one statement so concurrent appends cannot mix snapshots.
	rows, err := s.db.Query(`SELECT r.id,r.contract_id,r.version,r.project_dir,r.baseline,r.status,r.created_at,r.work,
a.number,a.started_at,a.completed_at,a.status,a.worker,rr.payload
FROM runs r LEFT JOIN attempts a ON r.id=a.run_id
LEFT JOIN rule_results rr ON a.run_id=rr.run_id AND a.number=rr.attempt_number
WHERE r.id=? ORDER BY a.number,rr.position`, id)
	if err != nil {
		return run, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		found = true
		var number sql.NullInt64
		var created, work string
		var started, completed, status, worker, payload sql.NullString
		if err := rows.Scan(&run.ID, &run.ContractID, &run.Version, &run.ProjectDir, &run.Baseline, &run.Status, &created, &work, &number, &started, &completed, &status, &worker, &payload); err != nil {
			return run, err
		}
		run.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return run, err
		}
		if run.Work, err = decodeOptional[backend.Work](work); err != nil {
			return run, err
		}
		if !number.Valid {
			continue
		}
		if len(run.Attempts) == 0 || run.Attempts[len(run.Attempts)-1].Number != int(number.Int64) {
			start, err := time.Parse(time.RFC3339Nano, started.String)
			if err != nil {
				return run, err
			}
			end, err := time.Parse(time.RFC3339Nano, completed.String)
			if err != nil {
				return run, err
			}
			workerRun, err := decodeOptional[backend.WorkerRun](worker.String)
			if err != nil {
				return run, err
			}
			run.Attempts = append(run.Attempts, backend.Attempt{Number: int(number.Int64), StartedAt: start, CompletedAt: end, Status: backend.ResultStatus(status.String), Results: []backend.RuleResult{}, Worker: workerRun})
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
	if err := rows.Err(); err != nil {
		return run, err
	}
	if !found {
		return run, backend.ErrNotFound
	}
	return run, nil
}
func (s *SQLite) ListRuns() ([]backend.Run, error) {
	rows, err := s.db.Query("SELECT id,contract_id,version,project_dir,baseline,status,created_at,work FROM runs ORDER BY created_at DESC,id")
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
	worker, err := optionalJSON(attempt.Worker)
	if err != nil {
		return backend.Run{}, err
	}
	if _, err := tx.Exec("INSERT INTO attempts(run_id,number,started_at,completed_at,status,worker) VALUES(?,?,?,?,?,?)", id, number, attempt.StartedAt.Format(time.RFC3339Nano), attempt.CompletedAt.Format(time.RFC3339Nano), attempt.Status, worker); err != nil {
		return backend.Run{}, err
	}
	for hash, data := range attempt.Blobs {
		if _, err := tx.Exec("INSERT OR IGNORE INTO blobs(hash,data) VALUES(?,?)", hash, data); err != nil {
			return backend.Run{}, err
		}
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

// GetBlob returns stored evidence content.
func (s *SQLite) GetBlob(hash string) ([]byte, error) {
	var data []byte
	if err := s.db.QueryRow("SELECT data FROM blobs WHERE hash=?", hash).Scan(&data); err != nil {
		return nil, notFound(err)
	}
	return data, nil
}

// AppendEvent records a work run's event, numbering it after the run's
// earlier events.
func (s *SQLite) AppendEvent(runID string, event backend.Event) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var seq int
	if err := tx.QueryRow("SELECT COALESCE(MAX(seq),0)+1 FROM events WHERE run_id=?", runID).Scan(&seq); err != nil {
		return 0, err
	}
	event.Seq = seq
	payload, err := json.Marshal(event)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec("INSERT INTO events(run_id,seq,payload) VALUES(?,?,?)", runID, seq, string(payload)); err != nil {
		return 0, err
	}
	return seq, tx.Commit()
}

// ListEvents returns a run's events after seq, in order.
func (s *SQLite) ListEvents(runID string, after int) ([]backend.Event, error) {
	rows, err := s.db.Query("SELECT payload FROM events WHERE run_id=? AND seq>? ORDER BY seq", runID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []backend.Event{}
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var event backend.Event
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
