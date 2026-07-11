package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/elian/nixbox/internal/jobs"
	"github.com/elian/nixbox/internal/nix"
	"github.com/elian/nixbox/internal/store"
)

type workloadDetail struct {
	baseData
	Workload              *store.Workload
	Content               string
	Ports                 []nix.HostPort // host firewall ports from the latest revision
	State                 string         // draft | pending | applied
	Status                string         // systemd state, e.g. "active (running)"
	Running               bool
	DataDir               string // data path deleted on destroy-with-data; "" if the type has none
	SupportsInsideJournal bool
	TerminalEnabled       bool // NIXBOX_TERMINAL is set
	HasShell              bool // this workload type advertises a shell (ShellArgs)
	// Secrets pane (types with SupportsSecretMounts): what is delivered
	// into this workload, and what else exists to attach.
	SupportsSecretMounts bool
	MountedSecrets       []store.Secret
	OtherSecrets         []store.Secret
	Revisions            []store.Revision
	Busy                 bool
	Flash                string
	Error                string
}

type newWorkloadData struct {
	baseData
	Types       []nix.WorkloadType // for the type picker
	Selected    nix.WorkloadType   // currently selected type (drives fields)
	Templates   []nix.Template     // Selected's templates
	Error       string
	Name        string
	DisplayName string
}

func (s *Server) handleWorkloadNew(w http.ResponseWriter, r *http.Request) {
	sel := workloadType(nix.WorkloadTypeContainer) // default selection
	s.renderPage(w, r, "workload_new", newWorkloadData{
		baseData:  s.base(r, s.t(r, "new.title"), "dashboard"),
		Types:     nix.RegisteredTypes(),
		Selected:  sel,
		Templates: sel.Templates,
	})
}

// handleWorkloadFields renders the per-type portion of the create form
// (ID constraints + template picker) for the selected type. The type
// radios swap it in via HTMX so the fields track the chosen type.
func (s *Server) handleWorkloadFields(w http.ResponseWriter, r *http.Request) {
	sel := workloadType(r.URL.Query().Get("type"))
	s.render(w, r, "workload_new", "workload-type-fields", newWorkloadData{
		Selected:  sel,
		Templates: sel.Templates,
	})
}

func (s *Server) handleWorkloadCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	displayName := strings.TrimSpace(r.FormValue("display_name"))
	tmplID := r.FormValue("template")
	wt, typeOK := nix.Lookup(r.FormValue("type"))
	if !typeOK {
		wt = workloadType(nix.WorkloadTypeContainer) // for a coherent re-render
	}

	fail := func(msg string) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.renderPage(w, r, "workload_new", newWorkloadData{
			baseData:    s.base(r, s.t(r, "new.title"), "dashboard"),
			Types:       nix.RegisteredTypes(),
			Selected:    wt,
			Templates:   wt.Templates,
			Error:       msg,
			Name:        name,
			DisplayName: displayName,
		})
	}

	if !typeOK {
		fail(s.t(r, "err.unknown-type"))
		return
	}
	if err := wt.ValidateName(name); err != nil {
		fail(err.Error())
		return
	}
	if err := nix.ValidateDisplayName(displayName); err != nil {
		fail(err.Error())
		return
	}
	tmpl, ok := wt.TemplateByID(tmplID)
	if !ok {
		fail(s.t(r, "err.unknown-template"))
		return
	}
	if _, err := s.store.WorkloadByName(name); err == nil {
		fail(s.t(r, "err.name-exists", name))
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		httpError(w, err, http.StatusInternalServerError)
		return
	}

	if err := s.flake.WriteWorkload(name, tmpl.Content); err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	if _, err := s.store.CreateWorkload(name, displayName, wt.ID, tmpl.Content, nix.FormatHostPorts(tmpl.Ports)); err != nil {
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
	data.Error = r.URL.Query().Get("error")
	s.renderPage(w, r, "workload", data)
}

func (s *Server) workloadDetailData(r *http.Request, wl *store.Workload) (workloadDetail, error) {
	wt := workloadType(wl.Type)
	data := workloadDetail{
		baseData:              s.base(r, wl.Display(), "dashboard"),
		Workload:              wl,
		Busy:                  s.jobs.Busy(),
		DataDir:               wt.DataDir(wl.Name),
		SupportsInsideJournal: wt.SupportsInsideJournal,
		TerminalEnabled:       s.cfg.EnableTerminal,
		HasShell:              wt.ShellArgs != nil,
	}
	data.Active = wl.Name // sidebar highlight matches on the ID

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

	if st, err := s.machines.Status(r.Context(), wt, wl.Name); err == nil {
		data.Running = st.Running()
		data.Status = st.ActiveState + " (" + st.SubState + ")"
	} else {
		data.Status = "unavailable"
	}

	if data.SupportsSecretMounts = wt.SupportsSecretMounts; data.SupportsSecretMounts {
		secrets, err := s.store.Secrets()
		if err != nil {
			return data, err
		}
		for _, sec := range secrets {
			if sec.MountedInto(wl.ID) {
				data.MountedSecrets = append(data.MountedSecrets, sec)
			} else {
				data.OtherSecrets = append(data.OtherSecrets, sec)
			}
		}
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
		s.render(w, r, "workload", "save-result", map[string]string{"Error": err.Error()})
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
		s.render(w, r, "workload", "save-result", map[string]string{"Error": err.Error()})
		return
	}
	s.render(w, r, "workload", "save-result", map[string]string{"Flash": s.t(r, "flash.saved")})
}

// handleWorkloadValidate runs the quick eval check on the saved file.
func (s *Server) handleWorkloadValidate(w http.ResponseWriter, r *http.Request) {
	wl, ok := s.lookupWorkload(w, r)
	if !ok {
		return
	}
	if err := nix.CheckSyntax(r.Context(), s.workloadFile(wl.Name)); err != nil {
		s.render(w, r, "workload", "save-result", map[string]string{"Error": err.Error()})
		return
	}
	if err := nix.CheckEval(r.Context(), s.workloadFile(wl.Name)); err != nil {
		s.render(w, r, "workload", "save-result", map[string]string{"Error": err.Error()})
		return
	}
	s.render(w, r, "workload", "save-result", map[string]string{"Flash": s.t(r, "flash.validated")})
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
	flash := s.t(r, "flash.enabled")
	if !enabled {
		flash = s.t(r, "flash.disabled")
	}
	http.Redirect(w, r, "/workloads/"+wl.Name+"?flash="+url.QueryEscape(flash), http.StatusSeeOther)
}

// handleWorkloadRename sets the optional friendly display name. Metadata
// only — no rebuild — so it redirects straight back with a flash. An
// empty value clears the label and the UI falls back to the ID.
func (s *Server) handleWorkloadRename(w http.ResponseWriter, r *http.Request) {
	wl, ok := s.lookupWorkload(w, r)
	if !ok {
		return
	}
	displayName := strings.TrimSpace(r.FormValue("display_name"))
	if err := nix.ValidateDisplayName(displayName); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if err := s.store.SetWorkloadDisplayName(wl.ID, displayName); err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/workloads/"+wl.Name+"?flash="+url.QueryEscape(s.t(r, "flash.renamed")), http.StatusSeeOther)
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
		http.Error(w, s.t(r, "err.busy"), http.StatusConflict)
		return
	}
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	s.render(w, r, "workload", "job-log", job)
}

func (s *Server) handleWorkloadRestore(w http.ResponseWriter, r *http.Request) {
	wl, ok := s.lookupWorkload(w, r)
	if !ok {
		return
	}
	revID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, s.t(r, "err.bad-revision"), http.StatusBadRequest)
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
	http.Redirect(w, r, "/workloads/"+wl.Name+"?flash="+url.QueryEscape(s.t(r, "flash.restored", revID)), http.StatusSeeOther)
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
		http.Error(w, s.t(r, "err.confirm-mismatch"), http.StatusUnprocessableEntity)
		return
	}
	deleteData := r.FormValue("delete_data") == "on"
	wt := workloadType(wl.Type)

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
		fmt.Fprintf(log, "==> destroying workload %s\n", name)
		if _, err := s.pipeline.Runner.Stream(ctx, log, "systemctl", "stop", wt.UnitName(name)); err != nil {
			fmt.Fprintf(log, "note: stop failed (workload may not be running): %v\n", err)
		}
		code, gen, err := s.pipeline.Rebuild(ctx, log, nix.ModeSwitch)
		if err != nil || code != 0 {
			fmt.Fprintf(log, "rebuild failed; workload kept (disabled)\n")
			return jobs.Result{ExitCode: code, Generation: gen}, err
		}
		if err := s.store.DeleteWorkload(id); err != nil {
			return jobs.Result{ExitCode: -1, Generation: gen}, err
		}
		if err := s.flake.RemoveWorkload(name); err != nil {
			return jobs.Result{ExitCode: -1, Generation: gen}, err
		}
		if dataDir := wt.DataDir(name); deleteData && dataDir != "" {
			fmt.Fprintf(log, "==> deleting container data\n")
			if _, err := s.pipeline.Runner.Stream(ctx, log, "rm", "-rf", "--", dataDir); err != nil {
				return jobs.Result{ExitCode: -1, Generation: gen}, err
			}
		}
		fmt.Fprintf(log, "==> workload %s destroyed\n", name)
		return jobs.Result{ExitCode: 0, Generation: gen}, nil
	})
	if errors.Is(err, jobs.ErrBusy) {
		http.Error(w, s.t(r, "err.busy"), http.StatusConflict)
		return
	}
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	s.render(w, r, "workload", "job-log", job)
}

func (s *Server) handleWorkloadLifecycle(w http.ResponseWriter, r *http.Request) {
	wl, ok := s.lookupWorkload(w, r)
	if !ok {
		return
	}
	wt := workloadType(wl.Type)
	var err error
	switch r.PathValue("verb") {
	case "start":
		err = s.machines.Start(r.Context(), wt, wl.Name)
	case "stop":
		err = s.machines.Stop(r.Context(), wt, wl.Name)
	case "restart":
		err = s.machines.Restart(r.Context(), wt, wl.Name)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.render(w, r, "workload", "save-result", map[string]string{"Error": err.Error()})
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

// workloadType resolves a stored type string to its descriptor, falling
// back to the container type for an unrecognized value (e.g. a row left
// by a newer build after a downgrade) so the UI keeps working.
func workloadType(typ string) nix.WorkloadType {
	if wt, ok := nix.Lookup(typ); ok {
		return wt
	}
	wt, _ := nix.Lookup(nix.WorkloadTypeContainer)
	return wt
}
