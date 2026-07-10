package store

import (
	"path/filepath"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestWorkloadLifecycle(t *testing.T) {
	s := open(t)

	w, err := s.CreateWorkload("web", "nixos-container", "{ }\n")
	if err != nil {
		t.Fatal(err)
	}
	if w.Enabled {
		t.Error("new workload should start disabled")
	}

	rev, err := s.LatestRevision(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rev.Content != "{ }\n" || rev.Note != "created" {
		t.Errorf("unexpected initial revision: %+v", rev)
	}

	if _, err := s.CreateWorkload("web", "nixos-container", ""); err == nil {
		t.Error("duplicate name should fail")
	}

	revID, err := s.SaveRevision(w.ID, "{ autoStart = true; }\n", "edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorkloadEnabled(w.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkApplied(w.ID, revID); err != nil {
		t.Fatal(err)
	}

	got, err := s.WorkloadByName("web")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || !got.AppliedRevisionID.Valid || got.AppliedRevisionID.Int64 != revID {
		t.Errorf("unexpected workload state: %+v", got)
	}

	revs, err := s.Revisions(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 2 {
		t.Errorf("want 2 revisions, got %d", len(revs))
	}

	if err := s.DeleteWorkload(w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WorkloadByName("web"); err != ErrNotFound {
		t.Errorf("want ErrNotFound after delete, got %v", err)
	}
	// Revisions cascade with the workload.
	if revs, _ := s.Revisions(w.ID); len(revs) != 0 {
		t.Errorf("revisions should cascade-delete, got %d", len(revs))
	}
}

func TestJobs(t *testing.T) {
	s := open(t)

	j, err := s.CreateJob(JobApply, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != JobRunning {
		t.Errorf("new job status = %s, want running", j.Status)
	}
	if err := s.SetJobLogPath(j.ID, "/var/lib/nixbox/logs/job-1.log"); err != nil {
		t.Fatal(err)
	}

	running, err := s.RunningJobs()
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 1 {
		t.Fatalf("want 1 running job, got %d", len(running))
	}

	gen := int64(42)
	if err := s.FinishJob(j.ID, JobOK, 0, &gen); err != nil {
		t.Fatal(err)
	}
	got, err := s.JobByID(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != JobOK || !got.Generation.Valid || got.Generation.Int64 != 42 {
		t.Errorf("unexpected finished job: %+v", got)
	}
	if running, _ := s.RunningJobs(); len(running) != 0 {
		t.Error("finished job still reported as running")
	}
}
