// Package jobs runs long operations (rebuilds, flake updates) one at a
// time, streaming their output to a per-job log file that the SSE layer
// tails for the browser.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/elian/nixbox/internal/store"
)

// ErrBusy is returned when a job is already running. nixbox serializes
// jobs because they all contend for the same system profile.
var ErrBusy = errors.New("another job is already running")

// Result is what a job function reports on completion.
type Result struct {
	ExitCode int
	// Generation is the system generation the job produced, if any.
	Generation *int64
}

// Func performs the actual work of a job, writing progress to log.
type Func func(ctx context.Context, log io.Writer) (Result, error)

type Manager struct {
	store   *store.Store
	logsDir string
	busy    atomic.Bool
}

func NewManager(st *store.Store, logsDir string) (*Manager, error) {
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return nil, err
	}
	return &Manager{store: st, logsDir: logsDir}, nil
}

// RecoverStale marks jobs left "running" by a previous process as
// failed. (M2 will reattach to systemd-run units instead.)
func (m *Manager) RecoverStale() error {
	running, err := m.store.RunningJobs()
	if err != nil {
		return err
	}
	for _, j := range running {
		if err := m.store.FinishJob(j.ID, store.JobFailed, -1, nil); err != nil {
			return err
		}
		slog.Warn("marked stale job as failed", "job", j.ID, "kind", j.Kind)
	}
	return nil
}

// Start launches fn as a new job of the given kind. It returns ErrBusy
// if a job is already running. The job executes in the background; its
// completion is recorded in the store.
func (m *Manager) Start(kind store.JobKind, workloadID *int64, fn Func) (*store.Job, error) {
	if !m.busy.CompareAndSwap(false, true) {
		return nil, ErrBusy
	}

	// The log path embeds the job ID, which we only have after insert;
	// create the row with a placeholder-free deterministic path scheme.
	job, err := m.store.CreateJob(kind, workloadID, "")
	if err != nil {
		m.busy.Store(false)
		return nil, err
	}
	logPath := filepath.Join(m.logsDir, fmt.Sprintf("job-%d.log", job.ID))
	if err := m.store.SetJobLogPath(job.ID, logPath); err != nil {
		m.busy.Store(false)
		return nil, err
	}
	job.LogPath = logPath

	go func() {
		defer m.busy.Store(false)

		logFile, err := os.Create(logPath)
		if err != nil {
			slog.Error("creating job log", "job", job.ID, "err", err)
			m.store.FinishJob(job.ID, store.JobFailed, -1, nil)
			return
		}
		defer logFile.Close()

		res, err := fn(context.Background(), &syncWriter{f: logFile})
		status := store.JobOK
		if err != nil {
			fmt.Fprintf(logFile, "\nerror: %v\n", err)
			status = store.JobFailed
			if res.ExitCode == 0 {
				res.ExitCode = -1
			}
		} else if res.ExitCode != 0 {
			status = store.JobFailed
		}
		if err := m.store.FinishJob(job.ID, status, res.ExitCode, res.Generation); err != nil {
			slog.Error("finishing job", "job", job.ID, "err", err)
		}
	}()

	return job, nil
}

// Busy reports whether a job is currently running.
func (m *Manager) Busy() bool { return m.busy.Load() }

// syncWriter flushes each write to disk so SSE tailers see output
// promptly.
type syncWriter struct{ f *os.File }

func (w *syncWriter) Write(p []byte) (int, error) {
	n, err := w.f.Write(p)
	if err == nil {
		w.f.Sync()
	}
	return n, err
}
