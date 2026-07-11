package server

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/elian/nixbox/internal/jobs"
	"github.com/elian/nixbox/internal/nix"
	"github.com/elian/nixbox/internal/store"
)

type flakeListData struct {
	baseData
	Inputs []store.FlakeInput
	Busy   bool
	DryRun bool
	Flash  string
	Error  string
}

func (s *Server) handleFlakeList(w http.ResponseWriter, r *http.Request) {
	inputs, err := s.store.FlakeInputs()
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	s.renderPage(w, r, "flakes", flakeListData{
		baseData: s.base(r, s.t(r, "nav.flakes"), "flakes"),
		Inputs:   inputs,
		Busy:     s.jobs.Busy(),
		DryRun:   s.cfg.DryRun,
		Flash:    r.URL.Query().Get("flash"),
		Error:    r.URL.Query().Get("error"),
	})
}

func (s *Server) handleFlakeCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	ref := strings.TrimSpace(r.FormValue("url"))
	follows := r.FormValue("follows_nixpkgs") == "on"

	fail := func(msg string) {
		http.Redirect(w, r, "/flakes?error="+url.QueryEscape(msg), http.StatusSeeOther)
	}
	if err := nix.ValidateName(name); err != nil {
		fail(err.Error())
		return
	}
	if ref == "" {
		fail(s.t(r, "err.ref-required"))
		return
	}
	if _, err := s.store.FlakeInputByName(name); err == nil {
		fail(s.t(r, "err.input-exists", name))
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	if _, err := s.store.CreateFlakeInput(name, ref, follows); err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/flakes?flash="+url.QueryEscape(s.t(r, "flash.input-added")), http.StatusSeeOther)
}

func (s *Server) handleFlakeSave(w http.ResponseWriter, r *http.Request) {
	in, ok := s.lookupFlakeInput(w, r)
	if !ok {
		return
	}
	ref := strings.TrimSpace(r.FormValue("url"))
	follows := r.FormValue("follows_nixpkgs") == "on"
	if ref == "" {
		http.Redirect(w, r, "/flakes?error="+url.QueryEscape(s.t(r, "err.ref-required")), http.StatusSeeOther)
		return
	}
	if err := s.store.UpdateFlakeInput(in.ID, ref, follows); err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/flakes?flash="+url.QueryEscape(s.t(r, "flash.input-saved")), http.StatusSeeOther)
}

// handleFlakeDelete drops the input. flake.nix is regenerated (without it)
// on the next apply; no rebuild is forced here because nothing references
// the input yet, so removing it can't break the current system.
func (s *Server) handleFlakeDelete(w http.ResponseWriter, r *http.Request) {
	in, ok := s.lookupFlakeInput(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteFlakeInput(in.ID); err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/flakes?flash="+url.QueryEscape(s.t(r, "flash.input-removed")), http.StatusSeeOther)
}

// handleFlakeApply regenerates flake.nix from the declared inputs and
// rebuilds. The pipeline locks the state flake first, so this is where the
// inputs are fetched and pinned into flake.lock.
func (s *Server) handleFlakeApply(w http.ResponseWriter, r *http.Request) {
	mode := nix.ModeSwitch
	if r.FormValue("mode") == "build" {
		mode = nix.ModeBuild
	}
	job, err := s.startApply(nil, mode)
	if errors.Is(err, jobs.ErrBusy) {
		http.Error(w, s.t(r, "err.busy"), http.StatusConflict)
		return
	}
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	s.render(w, r, "flakes", "job-log", job)
}

// regenerateFlake rewrites flake.nix from the declared inputs. It mirrors
// regenerateIndex: called at apply time so on-disk always reflects the
// last apply, and a manual rebuild uses the last-applied inputs.
func (s *Server) regenerateFlake() error {
	rows, err := s.store.FlakeInputs()
	if err != nil {
		return err
	}
	inputs := make([]nix.FlakeInput, 0, len(rows))
	for _, in := range rows {
		inputs = append(inputs, nix.FlakeInput{
			Name:           in.Name,
			URL:            in.URL,
			FollowsNixpkgs: in.FollowsNixpkgs,
		})
	}
	return s.flake.WriteFlake(inputs)
}

func (s *Server) lookupFlakeInput(w http.ResponseWriter, r *http.Request) (*store.FlakeInput, bool) {
	name := r.PathValue("name")
	if err := nix.ValidateName(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil, false
	}
	in, err := s.store.FlakeInputByName(name)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return nil, false
	}
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return nil, false
	}
	return in, true
}
