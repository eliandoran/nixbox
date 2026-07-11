package store

import "testing"

func TestFlakeInputLifecycle(t *testing.T) {
	s := open(t)

	in, err := s.CreateFlakeInput("nixflix", "github:owner/nixflix", true)
	if err != nil {
		t.Fatal(err)
	}
	if !in.FollowsNixpkgs {
		t.Error("follows_nixpkgs not persisted")
	}
	if in.Status() != "pending" {
		t.Errorf("new input status = %q, want pending", in.Status())
	}

	if _, err := s.CreateFlakeInput("nixflix", "github:x/y", false); err == nil {
		t.Error("duplicate name should fail")
	}

	if err := s.MarkFlakeInputApplied(in.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.FlakeInputByName("nixflix")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status() != "locked" {
		t.Errorf("status after apply = %q, want locked", got.Status())
	}

	// An edit reopens it as pending.
	if err := s.UpdateFlakeInput(got.ID, "github:owner/nixflix/v2", false); err != nil {
		t.Fatal(err)
	}
	got, _ = s.FlakeInputByName("nixflix")
	if got.Status() != "pending" {
		t.Errorf("status after edit = %q, want pending", got.Status())
	}
	if got.URL != "github:owner/nixflix/v2" || got.FollowsNixpkgs {
		t.Errorf("update not persisted: %+v", got)
	}

	if err := s.DeleteFlakeInput(got.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FlakeInputByName("nixflix"); err != ErrNotFound {
		t.Errorf("want ErrNotFound after delete, got %v", err)
	}
}

func TestFlakeInputsList(t *testing.T) {
	s := open(t)
	if in, err := s.FlakeInputs(); err != nil || len(in) != 0 {
		t.Fatalf("empty FlakeInputs() = %v, %v", in, err)
	}
	if _, err := s.CreateFlakeInput("zeta", "github:o/zeta", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateFlakeInput("alpha", "github:o/alpha", true); err != nil {
		t.Fatal(err)
	}
	in, err := s.FlakeInputs()
	if err != nil {
		t.Fatal(err)
	}
	if len(in) != 2 || in[0].Name != "alpha" || in[1].Name != "zeta" {
		t.Fatalf("FlakeInputs() not name-ordered: %+v", in)
	}
}

// TestFlakeInputsClosedDB and the corrupt-row case cover FlakeInputs' query
// error, the readers' scan error, and the list scan-loop error — mirroring
// the workload/secret fault-injection tests.
func TestFlakeInputsClosedDB(t *testing.T) {
	s := open(t)
	in, err := s.CreateFlakeInput("nixflix", "github:o/nixflix", false)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := s.CreateFlakeInput("x", "y", false); err == nil {
		t.Error("CreateFlakeInput on closed db")
	}
	if _, err := s.FlakeInputByID(in.ID); err == nil {
		t.Error("FlakeInputByID on closed db")
	}
	if _, err := s.FlakeInputByName("nixflix"); err == nil {
		t.Error("FlakeInputByName on closed db")
	}
	if _, err := s.FlakeInputs(); err == nil {
		t.Error("FlakeInputs on closed db")
	}
	if err := s.UpdateFlakeInput(in.ID, "z", false); err == nil {
		t.Error("UpdateFlakeInput on closed db")
	}
	if err := s.MarkFlakeInputApplied(in.ID); err == nil {
		t.Error("MarkFlakeInputApplied on closed db")
	}
	if err := s.DeleteFlakeInput(in.ID); err == nil {
		t.Error("DeleteFlakeInput on closed db")
	}
}

func TestFlakeInputsScanError(t *testing.T) {
	s := open(t)
	if _, err := s.db.Exec(
		`INSERT INTO flake_inputs (name, url, created_at, updated_at) VALUES ('bad', 'x', ?, ?)`,
		corruptTime, corruptTime); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FlakeInputs(); err == nil {
		t.Error("FlakeInputs: want scan error on corrupt timestamp")
	}
}
