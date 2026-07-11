package jobs

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elian/nixbox/internal/store"
)

func newManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m, err := NewManager(st, filepath.Join(dir, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	return m, st
}

// waitDone polls until the job leaves "running" — jobs finish in a
// background goroutine, so completion is observed through the store.
func waitDone(t *testing.T, st *store.Store, id int64) *store.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, err := st.JobByID(id)
		if err != nil {
			t.Fatal(err)
		}
		if j.Status != store.JobRunning {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("job did not finish within deadline")
	return nil
}

// waitIdle polls until the manager releases its busy flag, which happens
// strictly after the store row is finished.
func waitIdle(t *testing.T, m *Manager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !m.Busy() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("manager still busy after deadline")
}

func TestStartSuccess(t *testing.T) {
	m, st := newManager(t)
	gen := int64(42)
	job, err := m.Start(store.JobApply, nil, func(ctx context.Context, log io.Writer) (Result, error) {
		io.WriteString(log, "building...\n")
		return Result{Generation: &gen}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.LogPath == "" || !strings.Contains(job.LogPath, "job-") {
		t.Errorf("log path not set: %q", job.LogPath)
	}

	done := waitDone(t, st, job.ID)
	if done.Status != store.JobOK || !done.Generation.Valid || done.Generation.Int64 != 42 {
		t.Errorf("unexpected finished job: %+v", done)
	}
	data, err := os.ReadFile(job.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "building...\n" {
		t.Errorf("log contents = %q", data)
	}
	waitIdle(t, m)
}

func TestStartBusy(t *testing.T) {
	m, st := newManager(t)
	release := make(chan struct{})
	job, err := m.Start(store.JobApply, nil, func(ctx context.Context, log io.Writer) (Result, error) {
		<-release
		return Result{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Busy() {
		t.Error("Busy() = false while a job runs")
	}
	if _, err := m.Start(store.JobValidate, nil, nil); !errors.Is(err, ErrBusy) {
		t.Errorf("second Start = %v, want ErrBusy", err)
	}
	close(release)
	waitDone(t, st, job.ID)
	waitIdle(t, m)

	// The slot frees up for the next job.
	job2, err := m.Start(store.JobValidate, nil, func(ctx context.Context, log io.Writer) (Result, error) {
		return Result{}, nil
	})
	if err != nil {
		t.Fatalf("Start after completion: %v", err)
	}
	waitDone(t, st, job2.ID)
}

func TestStartFnError(t *testing.T) {
	m, st := newManager(t)
	job, err := m.Start(store.JobApply, nil, func(ctx context.Context, log io.Writer) (Result, error) {
		return Result{}, errors.New("nix eval exploded")
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitDone(t, st, job.ID)
	// An error with no exit code is normalized to -1.
	if done.Status != store.JobFailed || done.ExitCode.Int64 != -1 {
		t.Errorf("unexpected failed job: %+v", done)
	}
	data, _ := os.ReadFile(job.LogPath)
	if !strings.Contains(string(data), "error: nix eval exploded") {
		t.Errorf("error not appended to log: %q", data)
	}
	waitIdle(t, m)
}

func TestStartFnErrorKeepsExitCode(t *testing.T) {
	m, st := newManager(t)
	job, err := m.Start(store.JobApply, nil, func(ctx context.Context, log io.Writer) (Result, error) {
		return Result{ExitCode: 3}, errors.New("failed with detail")
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitDone(t, st, job.ID)
	if done.Status != store.JobFailed || done.ExitCode.Int64 != 3 {
		t.Errorf("exit code not preserved: %+v", done)
	}
	waitIdle(t, m)
}

func TestStartNonzeroExit(t *testing.T) {
	m, st := newManager(t)
	// No Go error, but the underlying command failed: still a failed job.
	job, err := m.Start(store.JobApply, nil, func(ctx context.Context, log io.Writer) (Result, error) {
		return Result{ExitCode: 2}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitDone(t, st, job.ID)
	if done.Status != store.JobFailed || done.ExitCode.Int64 != 2 {
		t.Errorf("unexpected job: %+v", done)
	}
	waitIdle(t, m)
}

func TestStartWorkloadID(t *testing.T) {
	m, st := newManager(t)
	w, err := st.CreateWorkload("web", "", "nixos-container", "{ }\n", "")
	if err != nil {
		t.Fatal(err)
	}
	job, err := m.Start(store.JobApply, &w.ID, func(ctx context.Context, log io.Writer) (Result, error) {
		return Result{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !job.WorkloadID.Valid || job.WorkloadID.Int64 != w.ID {
		t.Errorf("workload id not recorded: %+v", job)
	}
	waitDone(t, st, job.ID)
	waitIdle(t, m)
}

func TestStartLogCreateError(t *testing.T) {
	m, st := newManager(t)
	// Removing the logs dir after NewManager makes os.Create fail inside
	// the job goroutine; the job must still be marked failed.
	if err := os.RemoveAll(m.logsDir); err != nil {
		t.Fatal(err)
	}
	job, err := m.Start(store.JobApply, nil, func(ctx context.Context, log io.Writer) (Result, error) {
		t.Error("job fn ran despite log create failure")
		return Result{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitDone(t, st, job.ID)
	if done.Status != store.JobFailed || done.ExitCode.Int64 != -1 {
		t.Errorf("unexpected job after log failure: %+v", done)
	}
	waitIdle(t, m)
}

func TestStartCreateJobError(t *testing.T) {
	m, st := newManager(t)
	st.Close()
	if _, err := m.Start(store.JobApply, nil, nil); err == nil {
		t.Fatal("Start with closed store: want error")
	}
	// The busy slot must be released on the error path.
	if m.Busy() {
		t.Error("manager left busy after failed Start")
	}
}

func TestNewManagerError(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// A regular file where the logs dir should go fails MkdirAll.
	blocker := filepath.Join(dir, "logs")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(st, blocker); err == nil {
		t.Fatal("NewManager over a file: want error")
	}
}

func TestRecoverStale(t *testing.T) {
	m, st := newManager(t)
	// Nothing to recover is a no-op.
	if err := m.RecoverStale(); err != nil {
		t.Fatal(err)
	}
	// A job left "running" by a previous process gets failed.
	stale, err := st.CreateJob(store.JobApply, nil, "orphan.log")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RecoverStale(); err != nil {
		t.Fatal(err)
	}
	got, err := st.JobByID(stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.JobFailed || got.ExitCode.Int64 != -1 {
		t.Errorf("stale job not failed: %+v", got)
	}
}

func TestRecoverStaleError(t *testing.T) {
	m, st := newManager(t)
	st.Close()
	if err := m.RecoverStale(); err == nil {
		t.Fatal("RecoverStale with closed store: want error")
	}
}

func TestSyncWriterError(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "log"))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := (&syncWriter{f: f}).Write([]byte("x")); err == nil {
		t.Error("write to closed file: want error")
	}
}
