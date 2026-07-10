// Package store persists nixbox metadata in SQLite. The Nix expressions
// themselves live as files in the state flake; this database holds
// everything around them: workload metadata, revision history, jobs,
// sessions, and settings.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	// SQLite handles one writer at a time; a single connection avoids
	// SQLITE_BUSY without needing retry loops.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating database: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

var ErrNotFound = errors.New("not found")

type Workload struct {
	ID                int64
	Name              string
	Type              string
	Enabled           bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
	AppliedRevisionID sql.NullInt64
}

type Revision struct {
	ID         int64
	WorkloadID int64
	Content    string
	CreatedAt  time.Time
	Note       string
}

type JobKind string

const (
	JobApply       JobKind = "apply"
	JobFlakeUpdate JobKind = "flake-update"
	JobRollback    JobKind = "rollback"
	JobValidate    JobKind = "validate"
)

type JobStatus string

const (
	JobRunning JobStatus = "running"
	JobOK      JobStatus = "ok"
	JobFailed  JobStatus = "failed"
)

type Job struct {
	ID         int64
	Kind       JobKind
	Status     JobStatus
	WorkloadID sql.NullInt64
	StartedAt  time.Time
	FinishedAt sql.NullTime
	ExitCode   sql.NullInt64
	LogPath    string
	Generation sql.NullInt64
}

func (s *Store) CreateWorkload(name, typ, content string) (*Workload, error) {
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO workloads (name, type, enabled, created_at, updated_at) VALUES (?, ?, 0, ?, ?)`,
		name, typ, now, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`INSERT INTO revisions (workload_id, content, created_at, note) VALUES (?, ?, ?, 'created')`,
		id, content, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.WorkloadByID(id)
}

func (s *Store) WorkloadByID(id int64) (*Workload, error) {
	return scanWorkload(s.db.QueryRow(
		`SELECT id, name, type, enabled, created_at, updated_at, applied_revision_id
		 FROM workloads WHERE id = ?`, id))
}

func (s *Store) WorkloadByName(name string) (*Workload, error) {
	return scanWorkload(s.db.QueryRow(
		`SELECT id, name, type, enabled, created_at, updated_at, applied_revision_id
		 FROM workloads WHERE name = ?`, name))
}

func (s *Store) Workloads() ([]Workload, error) {
	rows, err := s.db.Query(
		`SELECT id, name, type, enabled, created_at, updated_at, applied_revision_id
		 FROM workloads ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Workload
	for rows.Next() {
		w, err := scanWorkload(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

func (s *Store) SetWorkloadEnabled(id int64, enabled bool) error {
	_, err := s.db.Exec(`UPDATE workloads SET enabled = ?, updated_at = ? WHERE id = ?`,
		enabled, time.Now().UTC(), id)
	return err
}

func (s *Store) DeleteWorkload(id int64) error {
	_, err := s.db.Exec(`DELETE FROM workloads WHERE id = ?`, id)
	return err
}

// SaveRevision records a new content snapshot for a workload.
func (s *Store) SaveRevision(workloadID int64, content, note string) (int64, error) {
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT INTO revisions (workload_id, content, created_at, note) VALUES (?, ?, ?, ?)`,
		workloadID, content, now, note)
	if err != nil {
		return 0, err
	}
	revID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE workloads SET updated_at = ? WHERE id = ?`, now, workloadID); err != nil {
		return 0, err
	}
	return revID, tx.Commit()
}

func (s *Store) LatestRevision(workloadID int64) (*Revision, error) {
	return scanRevision(s.db.QueryRow(
		`SELECT id, workload_id, content, created_at, note FROM revisions
		 WHERE workload_id = ? ORDER BY id DESC LIMIT 1`, workloadID))
}

func (s *Store) Revisions(workloadID int64) ([]Revision, error) {
	rows, err := s.db.Query(
		`SELECT id, workload_id, content, created_at, note FROM revisions
		 WHERE workload_id = ? ORDER BY id DESC`, workloadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Revision
	for rows.Next() {
		r, err := scanRevision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// MarkApplied records which revision of each enabled workload is now live.
func (s *Store) MarkApplied(workloadID, revisionID int64) error {
	_, err := s.db.Exec(`UPDATE workloads SET applied_revision_id = ? WHERE id = ?`,
		revisionID, workloadID)
	return err
}

func (s *Store) CreateJob(kind JobKind, workloadID *int64, logPath string) (*Job, error) {
	var wid any
	if workloadID != nil {
		wid = *workloadID
	}
	res, err := s.db.Exec(
		`INSERT INTO jobs (kind, status, workload_id, started_at, log_path) VALUES (?, ?, ?, ?, ?)`,
		kind, JobRunning, wid, time.Now().UTC(), logPath)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.JobByID(id)
}

func (s *Store) SetJobLogPath(id int64, logPath string) error {
	_, err := s.db.Exec(`UPDATE jobs SET log_path = ? WHERE id = ?`, logPath, id)
	return err
}

func (s *Store) FinishJob(id int64, status JobStatus, exitCode int, generation *int64) error {
	var gen any
	if generation != nil {
		gen = *generation
	}
	_, err := s.db.Exec(
		`UPDATE jobs SET status = ?, finished_at = ?, exit_code = ?, generation = ? WHERE id = ?`,
		status, time.Now().UTC(), exitCode, gen, id)
	return err
}

func (s *Store) JobByID(id int64) (*Job, error) {
	return scanJob(s.db.QueryRow(
		`SELECT id, kind, status, workload_id, started_at, finished_at, exit_code, log_path, generation
		 FROM jobs WHERE id = ?`, id))
}

func (s *Store) RecentJobs(limit int) ([]Job, error) {
	rows, err := s.db.Query(
		`SELECT id, kind, status, workload_id, started_at, finished_at, exit_code, log_path, generation
		 FROM jobs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// RunningJobs returns jobs still marked running, e.g. to reattach after
// a service restart.
func (s *Store) RunningJobs() ([]Job, error) {
	rows, err := s.db.Query(
		`SELECT id, kind, status, workload_id, started_at, finished_at, exit_code, log_path, generation
		 FROM jobs WHERE status = ? ORDER BY id`, JobRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

type rowScanner interface{ Scan(dest ...any) error }

func scanWorkload(r rowScanner) (*Workload, error) {
	var w Workload
	err := r.Scan(&w.ID, &w.Name, &w.Type, &w.Enabled, &w.CreatedAt, &w.UpdatedAt, &w.AppliedRevisionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &w, nil
}

func scanRevision(r rowScanner) (*Revision, error) {
	var rev Revision
	err := r.Scan(&rev.ID, &rev.WorkloadID, &rev.Content, &rev.CreatedAt, &rev.Note)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &rev, nil
}

func scanJob(r rowScanner) (*Job, error) {
	var j Job
	err := r.Scan(&j.ID, &j.Kind, &j.Status, &j.WorkloadID, &j.StartedAt, &j.FinishedAt, &j.ExitCode, &j.LogPath, &j.Generation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}
