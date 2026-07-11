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
