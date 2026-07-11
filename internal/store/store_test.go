package store

import (
	"path/filepath"
	"testing"
)

// corruptTime is a timestamp string SQLite stores verbatim but the driver
// cannot scan back into a time.Time, used to fault list-scan loops.
const corruptTime = "not-a-time"

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

	w, err := s.CreateWorkload("web", "My Web Server", "nixos-container", "{ }\n", "8080/tcp")
	if err != nil {
		t.Fatal(err)
	}
	if w.Enabled {
		t.Error("new workload should start disabled")
	}
	if w.DisplayName != "My Web Server" || w.Display() != "My Web Server" {
		t.Errorf("display name not persisted: %q", w.DisplayName)
	}

	rev, err := s.LatestRevision(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rev.Content != "{ }\n" || rev.Note != "created" || rev.Ports != "8080/tcp" {
		t.Errorf("unexpected initial revision: %+v", rev)
	}

	if _, err := s.CreateWorkload("web", "", "nixos-container", "", ""); err == nil {
		t.Error("duplicate name should fail")
	}

	revID, err := s.SaveRevision(w.ID, "{ autoStart = true; }\n", "443/tcp", "edit")
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

func TestWorkloadsList(t *testing.T) {
	s := open(t)
	if ws, err := s.Workloads(); err != nil || len(ws) != 0 {
		t.Fatalf("empty Workloads() = %v, %v", ws, err)
	}
	if _, err := s.CreateWorkload("beta", "", "nixos-container", "{ }\n", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateWorkload("alpha", "Alpha", "oci-container", "{ }\n", ""); err != nil {
		t.Fatal(err)
	}
	ws, err := s.Workloads()
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 2 || ws[0].Name != "alpha" || ws[1].Name != "beta" {
		t.Fatalf("Workloads() not name-ordered: %+v", ws)
	}
	// Display(): friendly label when set, else the ID.
	if ws[0].Display() != "Alpha" || ws[1].Display() != "beta" {
		t.Errorf("Display fallback: %q, %q", ws[0].Display(), ws[1].Display())
	}
}

func TestSetWorkloadDisplayName(t *testing.T) {
	s := open(t)
	w, err := s.CreateWorkload("web", "Web", "nixos-container", "{ }\n", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorkloadDisplayName(w.ID, "Renamed"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.WorkloadByID(w.ID); got.Display() != "Renamed" {
		t.Errorf("Display() = %q, want Renamed", got.Display())
	}
	// Clearing it falls back to the name.
	if err := s.SetWorkloadDisplayName(w.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.WorkloadByID(w.ID); got.Display() != "web" {
		t.Errorf("cleared Display() = %q, want web", got.Display())
	}
}

func TestRecentJobs(t *testing.T) {
	s := open(t)
	if jobs, err := s.RecentJobs(10); err != nil || len(jobs) != 0 {
		t.Fatalf("empty RecentJobs() = %v, %v", jobs, err)
	}
	w, err := s.CreateWorkload("web", "", "nixos-container", "{ }\n", "")
	if err != nil {
		t.Fatal(err)
	}
	// A job bound to a workload exercises CreateJob's non-nil branch.
	j1, err := s.CreateJob(JobApply, &w.ID, "log1")
	if err != nil {
		t.Fatal(err)
	}
	j2, err := s.CreateJob(JobValidate, nil, "log2")
	if err != nil {
		t.Fatal(err)
	}
	// A nil generation exercises FinishJob's else branch.
	if err := s.FinishJob(j2.ID, JobFailed, 1, nil); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.RecentJobs(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || jobs[0].ID != j2.ID || jobs[1].ID != j1.ID {
		t.Fatalf("RecentJobs() not id-desc: %+v", jobs)
	}
	if !jobs[1].WorkloadID.Valid || jobs[1].WorkloadID.Int64 != w.ID {
		t.Errorf("workload id not persisted: %+v", jobs[1])
	}
	if got, _ := s.RecentJobs(1); len(got) != 1 {
		t.Errorf("limit not honored: got %d", len(got))
	}
}

// TestScanNotFound covers the sql.ErrNoRows -> ErrNotFound branch of the
// revision and job scanners (the workload and flake scanners hit it via
// their own lifecycle tests).
func TestScanNotFound(t *testing.T) {
	s := open(t)
	if _, err := s.LatestRevision(9999); err != ErrNotFound {
		t.Errorf("LatestRevision(missing) = %v, want ErrNotFound", err)
	}
	if _, err := s.JobByID(9999); err != ErrNotFound {
		t.Errorf("JobByID(missing) = %v, want ErrNotFound", err)
	}
}

// TestStoreClosedDB drives every workload/revision/job reader and writer
// over a closed handle so their Query/Exec/Scan error returns run.
func TestStoreClosedDB(t *testing.T) {
	s := open(t)
	w, err := s.CreateWorkload("web", "", "nixos-container", "{ }\n", "")
	if err != nil {
		t.Fatal(err)
	}
	rev, err := s.LatestRevision(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.CreateJob(JobApply, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	checks := []struct {
		name string
		err  error
	}{
		{"migrate", s.migrate()},
		{"CreateWorkload", second(s.CreateWorkload("x", "", "nixos-container", "", ""))},
		{"WorkloadByID", second(s.WorkloadByID(w.ID))},
		{"WorkloadByName", second(s.WorkloadByName("web"))},
		{"Workloads", second(s.Workloads())},
		{"SetWorkloadEnabled", s.SetWorkloadEnabled(w.ID, true)},
		{"SetWorkloadDisplayName", s.SetWorkloadDisplayName(w.ID, "y")},
		{"DeleteWorkload", s.DeleteWorkload(w.ID)},
		{"SaveRevision", second(s.SaveRevision(w.ID, "c", "", "n"))},
		{"LatestRevision", second(s.LatestRevision(w.ID))},
		{"Revisions", second(s.Revisions(w.ID))},
		{"MarkApplied", s.MarkApplied(w.ID, rev.ID)},
		{"CreateJob", second(s.CreateJob(JobApply, nil, ""))},
		{"SetJobLogPath", s.SetJobLogPath(j.ID, "p")},
		{"FinishJob", s.FinishJob(j.ID, JobOK, 0, nil)},
		{"JobByID", second(s.JobByID(j.ID))},
		{"RecentJobs", second(s.RecentJobs(10))},
		{"RunningJobs", second(s.RunningJobs())},
	}
	for _, c := range checks {
		if c.err == nil {
			t.Errorf("%s on closed db: want error, got nil", c.name)
		}
	}
}

// TestListScanErrors faults each list query's scan loop with a row whose
// timestamp column holds junk SQLite stores but cannot scan back.
func TestListScanErrors(t *testing.T) {
	s := open(t)
	w, err := s.CreateWorkload("web", "", "nixos-container", "{ }\n", "")
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		if _, err := s.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO workloads (name, type, enabled, created_at, updated_at) VALUES ('bad', 'nixos-container', 0, ?, ?)`, corruptTime, corruptTime)
	exec(`INSERT INTO revisions (workload_id, content, ports, created_at, note) VALUES (?, '', '', ?, '')`, w.ID, corruptTime)
	exec(`INSERT INTO jobs (kind, status, started_at, log_path) VALUES ('apply', 'running', ?, '')`, corruptTime)

	if _, err := s.Workloads(); err == nil {
		t.Error("Workloads: want scan error")
	}
	if _, err := s.Revisions(w.ID); err == nil {
		t.Error("Revisions: want scan error")
	}
	if _, err := s.RecentJobs(10); err == nil {
		t.Error("RecentJobs: want scan error")
	}
	if _, err := s.RunningJobs(); err == nil {
		t.Error("RunningJobs: want scan error")
	}
}

// TestWriteInnerErrors faults the second statement inside the create/save
// transactions (revision insert) by dropping the revisions table.
func TestWriteInnerErrors(t *testing.T) {
	s := open(t)
	if _, err := s.db.Exec(`DROP TABLE revisions`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateWorkload("web", "", "nixos-container", "{ }\n", ""); err == nil {
		t.Error("CreateWorkload: want revision-insert error")
	}

	s = open(t)
	w, err := s.CreateWorkload("web", "", "nixos-container", "{ }\n", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE revisions`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveRevision(w.ID, "c", "", "n"); err == nil {
		t.Error("SaveRevision: want revision-insert error")
	}
}

// TestMigrateError makes a migration fail: Open must surface it (rolled
// back, index reported) rather than leaving a half-migrated database.
func TestMigrateError(t *testing.T) {
	orig := migrations
	migrations = append(append([]string{}, orig...), `THIS IS NOT VALID SQL;`)
	defer func() { migrations = orig }()

	if _, err := Open(filepath.Join(t.TempDir(), "bad.db")); err == nil {
		t.Fatal("expected migration error, got nil")
	}
}

// second discards a two-value return's first value, keeping the error for
// closed-db assertions.
func second[T any](_ T, err error) error { return err }
