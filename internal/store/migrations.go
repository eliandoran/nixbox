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
	// Optional human-friendly label shown in the UI. Purely cosmetic: the
	// `name` column remains the load-bearing ID (URL key, filesystem dir,
	// systemd/NixOS container identity). Empty means "fall back to name".
	`ALTER TABLE workloads ADD COLUMN display_name TEXT NOT NULL DEFAULT '';`,
	// External flakes declared as inputs of the managed state flake, defined
	// in the Flakes tab. A pure dependency registry: a name (the flake input
	// name, so it obeys nix.ValidateName) and a flake ref, optionally
	// following a shared nixpkgs. Where an input is *used* is a separate
	// concern. There is no revision history; the pending/locked badge is
	// derived from timestamps — applied_at NULL or older than updated_at
	// means the input is not yet locked into the live system.
	`
	CREATE TABLE flake_inputs (
		id              INTEGER PRIMARY KEY,
		name            TEXT NOT NULL UNIQUE,
		url             TEXT NOT NULL,
		follows_nixpkgs INTEGER NOT NULL DEFAULT 0,
		created_at      TIMESTAMP NOT NULL,
		updated_at      TIMESTAMP NOT NULL,
		applied_at      TIMESTAMP
	);
	`,
	// agenix-managed secrets (Secrets tab). Metadata only: the value lives
	// exclusively as age ciphertext on disk (state/secrets/<name>.age, the
	// file Nix reads), never in the database. name doubles as the agenix
	// secret name and the /run/agenix/<name> path, so it obeys
	// nix.ValidateName. owner/group/mode are the decrypted file's identity
	// on the host (group_name because "group" is an SQL keyword). The
	// pending/applied badge derives from applied_at vs updated_at, exactly
	// like flake_inputs. secret_mounts records which workloads the secret
	// is delivered into (bind mount / podman volume, per the workload's
	// type); host-service workloads read the host path directly and need
	// no row here.
	`
	CREATE TABLE secrets (
		id         INTEGER PRIMARY KEY,
		name       TEXT NOT NULL UNIQUE,
		owner      TEXT NOT NULL DEFAULT 'root',
		group_name TEXT NOT NULL DEFAULT 'root',
		mode       TEXT NOT NULL DEFAULT '0400',
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		applied_at TIMESTAMP
	);
	CREATE TABLE secret_mounts (
		secret_id   INTEGER NOT NULL REFERENCES secrets(id) ON DELETE CASCADE,
		workload_id INTEGER NOT NULL REFERENCES workloads(id) ON DELETE CASCADE,
		PRIMARY KEY (secret_id, workload_id)
	);
	`,
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
