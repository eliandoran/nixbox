package store

import (
	"database/sql"
	"errors"
	"time"
)

// Secret is one agenix-managed secret (Secrets tab). The row is metadata
// only — the value exists solely as age ciphertext on disk in the state
// flake (state/secrets/<Name>.age), so deleting nixbox.db loses which
// workloads a secret is mounted into but never the secret itself.
type Secret struct {
	ID    int64
	Name  string // agenix secret name; the decrypted path is /run/agenix/<Name>
	Owner string // owner of the decrypted file on the host
	Group string // group of the decrypted file on the host
	Mode  string // mode of the decrypted file, e.g. "0400"
	// WorkloadIDs are the workloads this secret is mounted into,
	// populated by Secrets()/SecretByName from secret_mounts.
	WorkloadIDs []int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// AppliedAt is when the secret's current value/metadata was last built
	// into the live system by a successful rebuild. NULL until first apply.
	AppliedAt sql.NullTime
}

// Status is the pending/applied badge: a secret is pending until a
// rebuild deploys its current state; any edit reopens it.
func (s *Secret) Status() string {
	if s.AppliedAt.Valid && !s.AppliedAt.Time.Before(s.UpdatedAt) {
		return "applied"
	}
	return "pending"
}

// MountedInto reports whether the secret is mounted into the workload.
func (s *Secret) MountedInto(workloadID int64) bool {
	for _, id := range s.WorkloadIDs {
		if id == workloadID {
			return true
		}
	}
	return false
}

// CreateSecret inserts a secret's metadata row and its workload mounts in
// one transaction. The caller is responsible for having written the
// ciphertext file first, mirroring how workloads write workload.nix
// before their row.
func (s *Store) CreateSecret(name, owner, group, mode string, workloadIDs []int64) (*Secret, error) {
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT INTO secrets (name, owner, group_name, mode, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		name, owner, group, mode, now, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if err := replaceMounts(tx, id, workloadIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.SecretByID(id)
}

// UpdateSecret replaces a secret's metadata and mounts and bumps
// updated_at, flipping the badge to pending until the next apply. A
// value change is invisible here: the ciphertext lives on disk and is
// rewritten by the caller.
func (s *Store) UpdateSecret(id int64, owner, group, mode string, workloadIDs []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`UPDATE secrets SET owner = ?, group_name = ?, mode = ?, updated_at = ? WHERE id = ?`,
		owner, group, mode, time.Now().UTC(), id); err != nil {
		return err
	}
	if err := replaceMounts(tx, id, workloadIDs); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceMounts(tx *sql.Tx, secretID int64, workloadIDs []int64) error {
	if _, err := tx.Exec(`DELETE FROM secret_mounts WHERE secret_id = ?`, secretID); err != nil {
		return err
	}
	for _, wid := range workloadIDs {
		if _, err := tx.Exec(
			`INSERT INTO secret_mounts (secret_id, workload_id) VALUES (?, ?)`, secretID, wid); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SecretByID(id int64) (*Secret, error) {
	return s.loadMounts(scanSecret(s.db.QueryRow(secretCols+` WHERE id = ?`, id)))
}

func (s *Store) SecretByName(name string) (*Secret, error) {
	return s.loadMounts(scanSecret(s.db.QueryRow(secretCols+` WHERE name = ?`, name)))
}

func (s *Store) Secrets() ([]Secret, error) {
	rows, err := s.db.Query(secretCols + ` ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Secret
	for rows.Next() {
		sec, err := scanSecret(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if _, err := s.loadMounts(&out[i], nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// MarkSecretApplied records that the secret's current state is now built
// into the live system.
func (s *Store) MarkSecretApplied(id int64) error {
	_, err := s.db.Exec(`UPDATE secrets SET applied_at = ? WHERE id = ?`, time.Now().UTC(), id)
	return err
}

// TouchSecret bumps updated_at after a value re-encryption that changes
// no metadata, so the badge still flips to pending.
func (s *Store) TouchSecret(id int64) error {
	_, err := s.db.Exec(`UPDATE secrets SET updated_at = ? WHERE id = ?`, time.Now().UTC(), id)
	return err
}

// DeleteSecret drops the secret row; secret_mounts rows cascade. The
// caller removes the ciphertext file after regenerating the index, so
// the flake never references a missing file.
func (s *Store) DeleteSecret(id int64) error {
	_, err := s.db.Exec(`DELETE FROM secrets WHERE id = ?`, id)
	return err
}

func (s *Store) loadMounts(sec *Secret, err error) (*Secret, error) {
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		`SELECT workload_id FROM secret_mounts WHERE secret_id = ? ORDER BY workload_id`, sec.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var wid int64
		if err := rows.Scan(&wid); err != nil {
			return nil, err
		}
		sec.WorkloadIDs = append(sec.WorkloadIDs, wid)
	}
	return sec, rows.Err()
}

const secretCols = `SELECT id, name, owner, group_name, mode, created_at, updated_at, applied_at FROM secrets`

func scanSecret(r rowScanner) (*Secret, error) {
	var sec Secret
	err := r.Scan(&sec.ID, &sec.Name, &sec.Owner, &sec.Group, &sec.Mode, &sec.CreatedAt, &sec.UpdatedAt, &sec.AppliedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sec, nil
}
