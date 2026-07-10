package store

import "fmt"

// migrations are applied in order; user_version tracks the last applied
// index. Never edit an existing entry — append a new one.
var migrations = []string{
	`
	CREATE TABLE workloads (
		id                  INTEGER PRIMARY KEY,
		name                TEXT NOT NULL UNIQUE,
		type                TEXT NOT NULL DEFAULT 'nixos-container',
		enabled             INTEGER NOT NULL DEFAULT 0,
		created_at          TIMESTAMP NOT NULL,
		updated_at          TIMESTAMP NOT NULL,
		applied_revision_id INTEGER REFERENCES revisions(id)
	);
	CREATE TABLE revisions (
		id          INTEGER PRIMARY KEY,
		workload_id INTEGER NOT NULL REFERENCES workloads(id) ON DELETE CASCADE,
		content     TEXT NOT NULL,
		created_at  TIMESTAMP NOT NULL,
		note        TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX revisions_workload ON revisions(workload_id, id DESC);
	CREATE TABLE jobs (
		id          INTEGER PRIMARY KEY,
		kind        TEXT NOT NULL,
		status      TEXT NOT NULL,
		workload_id INTEGER REFERENCES workloads(id) ON DELETE SET NULL,
		started_at  TIMESTAMP NOT NULL,
		finished_at TIMESTAMP,
		exit_code   INTEGER,
		log_path    TEXT NOT NULL,
		generation  INTEGER
	);
	CREATE TABLE sessions (
		token_hash TEXT PRIMARY KEY,
		created_at TIMESTAMP NOT NULL,
		expires_at TIMESTAMP NOT NULL
	);
	CREATE TABLE settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);
	`,
	// Host firewall ports a workload asks nixbox to open, snapshotted per
	// revision as a canonical "8080/tcp 53/udp" string (see nix.HostPort).
	`ALTER TABLE revisions ADD COLUMN ports TEXT NOT NULL DEFAULT '';`,
}

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
