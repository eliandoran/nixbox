package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/elian/nixbox/internal/jobs"
	"github.com/elian/nixbox/internal/nix"
	"github.com/elian/nixbox/internal/store"
)

type workloadDetail struct {
	baseData
	Workload  *store.Workload
	Content   string
	Ports     []nix.HostPort // host firewall ports from the latest revision
	State     string         // draft | pending | applied
	Status    string         // systemd state, e.g. "active (running)"
	Running   bool
	Revisions []store.Revision
	Busy      bool
	Flash     string
	Error     string
}

type newWorkloadData struct {
	baseData
	Templates []nix.Template
	Error     string
	Name      string
}

func (s *Server) handleWorkloadNew(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, "workload_new", newWorkloadData{
		baseData:  s.base(r, "New container", "dashboard"),
		Templates: nix.Templates,
	})
}

func (s *Server) handleWorkloadCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	tmplID := r.FormValue("template")

	fail := func(msg string) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.renderPage(w, "workload_new", newWorkloadData{
			baseData:  s.base(r, "New container", "dashboard"),
			Templates: nix.Templates,
			Error:     msg,
			Name:      name,
		})
	}

	if err := nix.ValidateName(name); err != nil {
		fail(err.Error())
		return
	}
	tmpl, ok := nix.TemplateByID(tmplID)
	if !ok {
		fail("unknown template")
		return
	}
	if _, err := s.store.WorkloadByName(name); err == nil {
		fail(fmt.Sprintf("a workload named %q already exists", name))
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		httpError(w, err, http.StatusInternalServerError)
		return
	}

	if err := s.flake.WriteWorkload(name, tmpl.Content); err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	if _, err := s.store.CreateWorkload(name, nix.WorkloadTypeContainer, tmpl.Content, nix.FormatHostPorts(tmpl.Ports)); err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/workloads/"+name, http.StatusSeeOther)
}

func (s *Server) handleWorkloadDetail(w http.ResponseWriter, r *http.Request) {
	wl, ok := s.lookupWorkload(w, r)
	if !ok {
		return
	}
	data, err := s.workloadDetailData(r, wl)
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	data.Flash = r.URL.Query().Get("flash")
	s.renderPage(w, "workload", data)
}

func (s *Server) workloadDetailData(r *http.Request, wl *store.Workload) (workloadDetail, error) {
	data := workloadDetail{
		baseData: s.base(r, wl.Name, "dashboard"),
		Workload: wl,
		Busy:     s.jobs.Busy(),
	}
	data.Active = wl.Name

	content, err := s.flake.ReadWorkload(wl.Name)
	if err != nil {
		return data, fmt.Errorf("reading workload file: %w", err)
	}
	data.Content = content

	latest, err := s.store.LatestRevision(wl.ID)
	if err != nil {
		return data, err
	}
	data.Ports = nix.DecodeHostPorts(latest.Ports)
	switch {
	case !wl.AppliedRevisionID.Valid:
		data.State = "draft"
	case wl.AppliedRevisionID.Int64 != latest.ID:
		data.State = "pending"
	default:
		data.State = "applied"
	}

	if st, err := s.machines.Status(r.Context(), wl.Name); err == nil {
		data.Running = st.Running()
		data.Status = st.ActiveState + " (" + st.SubState + ")"
	} else {
		data.Status = "unavailable"
	}

	data.Revisions, err = s.store.Revisions(wl.ID)
	return data, err
}

// handleWorkloadSave stores the textarea content as a new revision and
// runs a syntax check. Content that fails the check is still saved
// (drafts are allowed); the error is surfaced inline.
func (s *Server) handleWorkloadSave(w http.ResponseWriter, r *http.Request) {
	wl, ok := s.lookupWorkload(w, r)
	if !ok {
		return
	}
	content := r.FormValue("content")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	// Textareas submit CRLF line endings; normalize before they reach
	// disk or the parser.
	content = strings.ReplaceAll(content, "\r\n", "\n")

	// Host ports arrive as parallel port/proto arrays, one pair per row.
	// Unlike Nix content (drafts are allowed), a malformed port is a hard
	// error since it feeds the host firewall directly — reject the whole
	// save so content and ports stay in one atomic revision.
	ports, err := nix.ParseHostPorts(r.Form["port"], r.Form["proto"])
	if err != nil {
		s.render(w, "workload", "save-result", map[string]string{"Error": err.Error()})
		return
	}

	if err := s.flake.WriteWorkload(wl.Name, content); err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	if _, err := s.store.SaveRevision(wl.ID, content, nix.FormatHostPorts(ports), "edit"); err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}

	if err := nix.CheckSyntax(r.Context(), s.workloadFile(wl.Name)); err != nil {
		s.render(w, "workload", "save-result", map[string]string{"Error": err.Error()})
		return
	}
	s.render(w, "workload", "save-result", map[string]string{"Flash": "Saved."})
}

// handleWorkloadValidate runs the quick eval check on the saved file.
func (s *Server) handleWorkloadValidate(w http.ResponseWriter, r *http.Request) {
	wl, ok := s.lookupWorkload(w, r)
	if !ok {
		return
	}
	if err := nix.CheckSyntax(r.Context(), s.workloadFile(wl.Name)); err != nil {
		s.render(w, "workload", "save-result", map[string]string{"Error": err.Error()})
		return
	}
	if err := nix.CheckEval(r.Context(), s.workloadFile(wl.Name)); err != nil {
		s.render(w, "workload", "save-result", map[string]string{"Error": err.Error()})
		return
	}
	s.render(w, "workload", "save-result", map[string]string{"Flash": "Expression parses and evaluates."})
}

func (s *Server) handleWorkloadEnable(w http.ResponseWriter, r *http.Request) {
	s.setEnabled(w, r, true)
}

func (s *Server) handleWorkloadDisable(w http.ResponseWriter, r *http.Request) {
	s.setEnabled(w, r, false)
}

func (s *Server) setEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	wl, ok := s.lookupWorkload(w, r)
	if !ok {
		return
	}
	if err := s.store.SetWorkloadEnabled(wl.ID, enabled); err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	flash := "Enabled. Apply to make it live."
	if !enabled {
		flash = "Disabled. Apply to remove it from the system."
	}
	http.Redirect(w, r, "/workloads/"+wl.Name+"?flash="+flash, http.StatusSeeOther)
}

// handleWorkloadApply rebuilds the system (attributed to this workload
// in job history) and returns the live log fragment.
func (s *Server) handleWorkloadApply(w http.ResponseWriter, r *http.Request) {
	wl, ok := s.lookupWorkload(w, r)
	if !ok {
		return
	}
	mode := nix.ModeSwitch
	if r.FormValue("mode") == "build" {
		mode = nix.ModeBuild
	}
	job, err := s.startApply(&wl.ID, mode)
	if errors.Is(err, jobs.ErrBusy) {
		http.Error(w, "a job is already running", http.StatusConflict)
		return
	}
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	s.render(w, "workload", "job-log", job)
}

func (s *Server) handleWorkloadRestore(w http.ResponseWriter, r *http.Request) {
	wl, ok := s.lookupWorkload(w, r)
	if !ok {
		return
	}
	revID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad revision id", http.StatusBadRequest)
		return
	}
	revs, err := s.store.Revisions(wl.ID)
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	var target *store.Revision
	for i := range revs {
		if revs[i].ID == revID {
			target = &revs[i]
			break
		}
	}
	if target == nil {
		http.NotFound(w, r)
		return
	}
	if err := s.flake.WriteWorkload(wl.Name, target.Content); err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	note := fmt.Sprintf("restore of #%d", revID)
	if _, err := s.store.SaveRevision(wl.ID, target.Content, target.Ports, note); err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/workloads/"+wl.Name+"?flash=Restored revision "+strconv.FormatInt(revID, 10)+".", http.StatusSeeOther)
}

// handleWorkloadDestroy disables the workload and rebuilds without it;
// on success it deletes the workload (and optionally its data
// directory). The system is only touched after the user re-types the
// workload name.
func (s *Server) handleWorkloadDestroy(w http.ResponseWriter, r *http.Request) {
	wl, ok := s.lookupWorkload(w, r)
	if !ok {
		return
	}
	if r.FormValue("confirm") != wl.Name {
		http.Error(w, "confirmation name does not match", http.StatusUnprocessableEntity)
		return
	}
	deleteData := r.FormValue("delete_data") == "on"

	if err := s.store.SetWorkloadEnabled(wl.ID, false); err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	if err := s.regenerateIndex(); err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}

	name, id := wl.Name, wl.ID
	job, err := s.jobs.Start(store.JobApply, &wl.ID, func(ctx context.Context, log io.Writer) (jobs.Result, error) {
		fmt.Fprintf(log, "==> destroying container %s\n", name)
		if _, err := s.pipeline.Runner.Stream(ctx, log, "systemctl", "stop", "container@"+name+".service"); err != nil {
			fmt.Fprintf(log, "note: stop failed (container may not be running): %v\n", err)
		}
		code, gen, err := s.pipeline.Rebuild(ctx, log, nix.ModeSwitch)
		if err != nil || code != 0 {
			fmt.Fprintf(log, "rebuild failed; container kept (disabled)\n")
			return jobs.Result{ExitCode: code, Generation: gen}, err
		}
		if err := s.store.DeleteWorkload(id); err != nil {
			return jobs.Result{ExitCode: -1, Generation: gen}, err
		}
		if err := s.flake.RemoveWorkload(name); err != nil {
			return jobs.Result{ExitCode: -1, Generation: gen}, err
		}
		if deleteData {
			fmt.Fprintf(log, "==> deleting container data\n")
			if _, err := s.pipeline.Runner.Stream(ctx, log, "rm", "-rf", "--", "/var/lib/nixos-containers/"+name); err != nil {
				return jobs.Result{ExitCode: -1, Generation: gen}, err
			}
		}
		fmt.Fprintf(log, "==> container %s destroyed\n", name)
		return jobs.Result{ExitCode: 0, Generation: gen}, nil
	})
	if errors.Is(err, jobs.ErrBusy) {
		http.Error(w, "a job is already running", http.StatusConflict)
		return
	}
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	s.render(w, "workload", "job-log", job)
}

func (s *Server) handleWorkloadLifecycle(w http.ResponseWriter, r *http.Request) {
	wl, ok := s.lookupWorkload(w, r)
	if !ok {
		return
	}
	var err error
	switch r.PathValue("verb") {
	case "start":
		err = s.machines.Start(r.Context(), wl.Name)
	case "stop":
		err = s.machines.Stop(r.Context(), wl.Name)
	case "restart":
		err = s.machines.Restart(r.Context(), wl.Name)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.render(w, "workload", "save-result", map[string]string{"Error": err.Error()})
		return
	}
	http.Redirect(w, r, "/workloads/"+wl.Name, http.StatusSeeOther)
}

func (s *Server) lookupWorkload(w http.ResponseWriter, r *http.Request) (*store.Workload, bool) {
	name := r.PathValue("name")
	if err := nix.ValidateName(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil, false
	}
	wl, err := s.store.WorkloadByName(name)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return nil, false
	}
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return nil, false
	}
	return wl, true
}

func (s *Server) workloadFile(name string) string {
	return s.cfg.WorkloadsDir() + "/" + name + "/workload.nix"
}
