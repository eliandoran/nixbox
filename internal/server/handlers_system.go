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
		baseData: s.base(r, "System", "system"),
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
	s.renderPage(w, r, "system", data)
}

// handleRebuild starts an apply job and returns the live log fragment.
func (s *Server) handleRebuild(w http.ResponseWriter, r *http.Request) {
	job, err := s.startApply(nil, nix.ModeSwitch)
	if errors.Is(err, jobs.ErrBusy) {
		http.Error(w, "a job is already running", http.StatusConflict)
		return
	}
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	s.render(w, r, "system", "job-log", job)
}

// powerResult drives the fragment shown after a reboot/poweroff request.
type powerResult struct {
	Action string // "reboot" or "shutdown"
	DryRun bool
}

func (s *Server) handleReboot(w http.ResponseWriter, r *http.Request) {
	s.handlePower(w, r, "reboot")
}

func (s *Server) handlePoweroff(w http.ResponseWriter, r *http.Request) {
	s.handlePower(w, r, "shutdown")
}

// handlePower reboots or shuts down the host. It refuses while a job is
// running (a power cut mid-rebuild risks a half-applied generation) and,
// in dry-run mode, does nothing but still returns the confirmation
// fragment so the flow can be exercised without taking the machine down.
func (s *Server) handlePower(w http.ResponseWriter, r *http.Request, action string) {
	if s.jobs.Busy() {
		http.Error(w, "a job is already running", http.StatusConflict)
		return
	}
	if !s.cfg.DryRun {
		var err error
		switch action {
		case "reboot":
			err = s.machines.Reboot(r.Context())
		case "shutdown":
			err = s.machines.Poweroff(r.Context())
		}
		if err != nil {
			httpError(w, err, http.StatusInternalServerError)
			return
		}
	}
	s.render(w, r, "system", "power-result", powerResult{Action: action, DryRun: s.cfg.DryRun})
}

// startApply regenerates the index and launches a rebuild job. On a
// successful switch it records which revision of each enabled workload
// is now live. workloadID only attributes the job in history; a
// rebuild always applies the whole system.
func (s *Server) startApply(workloadID *int64, mode nix.RebuildMode) (*store.Job, error) {
	if err := s.regenerateIndex(); err != nil {
		return nil, err
	}
	if err := s.regenerateFlake(); err != nil {
		return nil, err
	}

	// Snapshot revisions now: edits made while the rebuild runs must
	// not be marked as applied.
	applied := map[int64]int64{}
	workloads, err := s.store.Workloads()
	if err != nil {
		return nil, err
	}
	for _, wl := range workloads {
		if !wl.Enabled {
			continue
		}
		rev, err := s.store.LatestRevision(wl.ID)
		if err != nil {
			return nil, err
		}
		applied[wl.ID] = rev.ID
	}

	// Snapshot the declared flake inputs: this rebuild locks their current
	// refs into the live system, so mark exactly these applied on success.
	inputs, err := s.store.FlakeInputs()
	if err != nil {
		return nil, err
	}
	var appliedInputs []int64
	for _, in := range inputs {
		appliedInputs = append(appliedInputs, in.ID)
	}

	kind := store.JobApply
	if mode == nix.ModeBuild {
		kind = store.JobValidate
	}
	return s.jobs.Start(kind, workloadID, func(ctx context.Context, log io.Writer) (jobs.Result, error) {
		code, gen, err := s.pipeline.Rebuild(ctx, log, mode)
		if err == nil && code == 0 && mode != nix.ModeBuild {
			for wid, rid := range applied {
				if err := s.store.MarkApplied(wid, rid); err != nil {
					return jobs.Result{ExitCode: code, Generation: gen}, err
				}
			}
			for _, id := range appliedInputs {
				if err := s.store.MarkFlakeInputApplied(id); err != nil {
					return jobs.Result{ExitCode: code, Generation: gen}, err
				}
			}
		}
		return jobs.Result{ExitCode: code, Generation: gen}, err
	})
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
	s.render(w, r, "system", "job-log", job)
}

func (s *Server) regenerateIndex() error {
	workloads, err := s.store.Workloads()
	if err != nil {
		return err
	}
	var entries []nix.IndexEntry
	for _, wl := range workloads {
		if !wl.Enabled {
			continue
		}
		// Host ports live on the latest revision, mirroring how the
		// workload.nix on disk is always the latest saved content.
		rev, err := s.store.LatestRevision(wl.ID)
		if err != nil {
			return err
		}
		entries = append(entries, nix.IndexEntry{
			Name:  wl.Name,
			Type:  wl.Type,
			Ports: nix.DecodeHostPorts(rev.Ports),
		})
	}
	return s.flake.WriteIndex(entries)
}
