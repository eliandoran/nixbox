package store

import (
	"database/sql"
	"errors"
	"time"
)

// FlakeInput is one external flake declared as an input of the managed
// state flake (Flakes tab). It is a pure dependency: a name, a flake ref,
// and whether it follows a shared nixpkgs. Where the input gets used is
// tracked elsewhere.
type FlakeInput struct {
	ID             int64
	Name           string // flake input name; referenced as inputs.<Name>
	URL            string // flake reference, e.g. "github:owner/repo"
	FollowsNixpkgs bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
	// AppliedAt is when this input's current ref was last locked into the
	// live system by a successful rebuild. NULL until first apply.
	AppliedAt sql.NullTime
}

// Status is the pending/locked badge: an input is pending until a rebuild
// locks its current ref into the system; an edit reopens it.
func (f *FlakeInput) Status() string {
	if f.AppliedAt.Valid && !f.AppliedAt.Time.Before(f.UpdatedAt) {
		return "locked"
	}
	return "pending"
}

func (s *Store) CreateFlakeInput(name, url string, followsNixpkgs bool) (*FlakeInput, error) {
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO flake_inputs (name, url, follows_nixpkgs, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		name, url, followsNixpkgs, now, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.FlakeInputByID(id)
}

func (s *Store) FlakeInputByID(id int64) (*FlakeInput, error) {
	return scanFlakeInput(s.db.QueryRow(flakeInputCols+` WHERE id = ?`, id))
}

func (s *Store) FlakeInputByName(name string) (*FlakeInput, error) {
	return scanFlakeInput(s.db.QueryRow(flakeInputCols+` WHERE name = ?`, name))
}

func (s *Store) FlakeInputs() ([]FlakeInput, error) {
	rows, err := s.db.Query(flakeInputCols + ` ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FlakeInput
	for rows.Next() {
		in, err := scanFlakeInput(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *in)
	}
	return out, rows.Err()
}

// UpdateFlakeInput changes an input's ref/follows and bumps updated_at,
// which flips the badge to pending until the next apply re-locks it.
func (s *Store) UpdateFlakeInput(id int64, url string, followsNixpkgs bool) error {
	_, err := s.db.Exec(`UPDATE flake_inputs SET url = ?, follows_nixpkgs = ?, updated_at = ? WHERE id = ?`,
		url, followsNixpkgs, time.Now().UTC(), id)
	return err
}

// MarkFlakeInputApplied records that the input's current ref is now locked
// into the live system.
func (s *Store) MarkFlakeInputApplied(id int64) error {
	_, err := s.db.Exec(`UPDATE flake_inputs SET applied_at = ? WHERE id = ?`, time.Now().UTC(), id)
	return err
}

func (s *Store) DeleteFlakeInput(id int64) error {
	_, err := s.db.Exec(`DELETE FROM flake_inputs WHERE id = ?`, id)
	return err
}

const flakeInputCols = `SELECT id, name, url, follows_nixpkgs, created_at, updated_at, applied_at FROM flake_inputs`

func scanFlakeInput(r rowScanner) (*FlakeInput, error) {
	var in FlakeInput
	err := r.Scan(&in.ID, &in.Name, &in.URL, &in.FollowsNixpkgs, &in.CreatedAt, &in.UpdatedAt, &in.AppliedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &in, nil
}
