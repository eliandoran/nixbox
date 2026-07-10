package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/elian/nixbox/internal/jobs"
	"github.com/elian/nixbox/internal/nix"
	"github.com/elian/nixbox/internal/store"
)

type systemData struct {
	baseData
	Busy      bool
	DryRun    bool
	Jobs      []store.Job
	ActiveJob *store.Job
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	data := systemData{
		baseData: s.base("System", "system"),
		Busy:     s.jobs.Busy(),
		DryRun:   s.cfg.DryRun,
	}
	recent, err := s.store.RecentJobs(20)
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	data.Jobs = recent
	for i := range recent {
		if recent[i].Status == store.JobRunning {
			data.ActiveJob = &recent[i]
			break
		}
	}
	s.renderPage(w, "system", data)
}

// handleRebuild starts an apply job and returns the live log fragment.
func (s *Server) handleRebuild(w http.ResponseWriter, r *http.Request) {
	// Regenerate the index from enabled workloads before rebuilding, so
	// the state flake always matches the database.
	if err := s.regenerateIndex(); err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}

	job, err := s.jobs.Start(store.JobApply, nil, func(ctx context.Context, log io.Writer) (jobs.Result, error) {
		code, gen, err := s.pipeline.Rebuild(ctx, log, nix.ModeSwitch)
		return jobs.Result{ExitCode: code, Generation: gen}, err
	})
	if errors.Is(err, jobs.ErrBusy) {
		http.Error(w, "a job is already running", http.StatusConflict)
		return
	}
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	s.render(w, "system", "job-log", job)
}

// handleJobLogFragment re-renders the log pane for a historical job.
func (s *Server) handleJobLogFragment(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad job id", http.StatusBadRequest)
		return
	}
	job, err := s.store.JobByID(id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	s.render(w, "system", "job-log", job)
}

func (s *Server) regenerateIndex() error {
	workloads, err := s.store.Workloads()
	if err != nil {
		return err
	}
	var entries []nix.IndexEntry
	for _, wl := range workloads {
		if wl.Enabled {
			entries = append(entries, nix.IndexEntry{Name: wl.Name, Type: wl.Type})
		}
	}
	return s.flake.WriteIndex(entries)
}
