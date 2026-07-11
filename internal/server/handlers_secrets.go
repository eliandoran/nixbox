package server

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/elian/nixbox/internal/jobs"
	"github.com/elian/nixbox/internal/nix"
	"github.com/elian/nixbox/internal/secret"
	"github.com/elian/nixbox/internal/store"
)

type secretsData struct {
	baseData
	Secrets []store.Secret
	// Targets are the workloads offered as delivery mounts — every
	// workload of a type whose module can deliver a secret. Host
	// services are absent by design: they read /run/agenix/<name> off
	// the host directly.
	Targets []store.Workload
	Busy    bool
	DryRun  bool
	Flash   string
	Error   string
}

func (s *Server) handleSecretList(w http.ResponseWriter, r *http.Request) {
	data, err := s.secretsData(r)
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	data.Flash = r.URL.Query().Get("flash")
	data.Error = r.URL.Query().Get("error")
	s.renderPage(w, r, "secrets", data)
}

func (s *Server) secretsData(r *http.Request) (secretsData, error) {
	data := secretsData{
		baseData: s.base(r, s.t(r, "nav.secrets"), "secrets"),
		Busy:     s.jobs.Busy(),
		DryRun:   s.cfg.DryRun,
	}
	var err error
	if data.Secrets, err = s.store.Secrets(); err != nil {
		return data, err
	}
	workloads, err := s.store.Workloads()
	if err != nil {
		return data, err
	}
	for _, wl := range workloads {
		if wt, ok := nix.Lookup(wl.Type); ok && wt.SupportsSecretMounts {
			data.Targets = append(data.Targets, wl)
		}
	}
	return data, nil
}

// unixNameRe bounds owner/group values: they land in the generated Nix
// (escaped, so this is not an injection guard) and become a chown at
// activation — a typo'd name fails the whole activation, so keep the
// charset to plausible account names.
var unixNameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// modeRe matches an octal file mode as agenix expects it, e.g. "0400".
var modeRe = regexp.MustCompile(`^[0-7]{3,4}$`)

// secretForm is the validated common part of the create and save forms.
type secretForm struct {
	Owner  string
	Group  string
	Mode   string
	Value  string // plaintext; empty on save means "keep current"
	Mounts []int64
}

// parseSecretForm validates the common part of the create and save
// forms; the second return is a localized error message, empty on
// success.
func (s *Server) parseSecretForm(r *http.Request) (secretForm, string) {
	f := secretForm{
		Owner: strings.TrimSpace(r.FormValue("owner")),
		Group: strings.TrimSpace(r.FormValue("group")),
		Mode:  strings.TrimSpace(r.FormValue("mode")),
		// Textareas submit CRLF; normalize but otherwise keep the value
		// byte-exact — secrets are not Nix expressions, a trailing
		// newline (or its absence) may be meaningful.
		Value: strings.ReplaceAll(r.FormValue("value"), "\r\n", "\n"),
	}
	if f.Owner == "" {
		f.Owner = "root"
	}
	if f.Group == "" {
		f.Group = "root"
	}
	if f.Mode == "" {
		f.Mode = "0400"
	}
	if !unixNameRe.MatchString(f.Owner) || !unixNameRe.MatchString(f.Group) {
		return f, s.t(r, "err.bad-owner")
	}
	if !modeRe.MatchString(f.Mode) {
		return f, s.t(r, "err.bad-mode")
	}
	for _, v := range r.Form["mount"] {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return f, s.t(r, "err.bad-mount")
		}
		wl, err := s.store.WorkloadByID(id)
		if err != nil {
			return f, s.t(r, "err.bad-mount")
		}
		if wt, ok := nix.Lookup(wl.Type); !ok || !wt.SupportsSecretMounts {
			return f, s.t(r, "err.bad-mount")
		}
		f.Mounts = append(f.Mounts, id)
	}
	return f, ""
}

// encryptSecret seals the plaintext to the configured recipient (the
// host's SSH host key) and writes the ciphertext into the state flake.
// The plaintext is never persisted anywhere.
func (s *Server) encryptSecret(name, plaintext string) error {
	rec, err := secret.LoadRecipient(s.cfg.AgeRecipient)
	if err != nil {
		return err
	}
	ct, err := secret.Encrypt(rec, []byte(plaintext))
	if err != nil {
		return err
	}
	return s.flake.WriteSecret(name, ct)
}

func (s *Server) handleSecretCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	fail := func(msg string) {
		http.Redirect(w, r, "/secrets?error="+url.QueryEscape(msg), http.StatusSeeOther)
	}
	if err := nix.ValidateName(name); err != nil {
		fail(err.Error())
		return
	}
	f, errMsg := s.parseSecretForm(r)
	if errMsg != "" {
		fail(errMsg)
		return
	}
	if f.Value == "" {
		fail(s.t(r, "err.value-required"))
		return
	}
	if _, err := s.store.SecretByName(name); err == nil {
		fail(s.t(r, "err.secret-exists", name))
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	// Ciphertext first, row second — like workloads, the file Nix reads
	// exists before anything points at it. It stays unreferenced until
	// the next apply regenerates the index.
	if err := s.encryptSecret(name, f.Value); err != nil {
		fail(err.Error())
		return
	}
	if _, err := s.store.CreateSecret(name, f.Owner, f.Group, f.Mode, f.Mounts); err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/secrets?flash="+url.QueryEscape(s.t(r, "flash.secret-added")), http.StatusSeeOther)
}

func (s *Server) handleSecretSave(w http.ResponseWriter, r *http.Request) {
	sec, ok := s.lookupSecret(w, r)
	if !ok {
		return
	}
	f, errMsg := s.parseSecretForm(r)
	if errMsg != "" {
		http.Redirect(w, r, "/secrets?error="+url.QueryEscape(errMsg), http.StatusSeeOther)
		return
	}
	// An empty value keeps the current ciphertext: the UI never shows
	// the plaintext back, so "leave blank to keep" is the edit contract.
	if f.Value != "" {
		if err := s.encryptSecret(sec.Name, f.Value); err != nil {
			http.Redirect(w, r, "/secrets?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
	}
	if err := s.store.UpdateSecret(sec.ID, f.Owner, f.Group, f.Mode, f.Mounts); err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/secrets?flash="+url.QueryEscape(s.t(r, "flash.secret-saved")), http.StatusSeeOther)
}

// handleSecretDelete drops the secret row. The ciphertext file stays on
// disk until the next apply: the on-disk index may still reference it
// (a manual rebuild must keep working), so startApply prunes orphans
// right after regenerating the index.
func (s *Server) handleSecretDelete(w http.ResponseWriter, r *http.Request) {
	sec, ok := s.lookupSecret(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteSecret(sec.ID); err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/secrets?flash="+url.QueryEscape(s.t(r, "flash.secret-removed")), http.StatusSeeOther)
}

// handleSecretApply regenerates and rebuilds — identical to the flakes
// apply, re-rendered against this page's template set.
func (s *Server) handleSecretApply(w http.ResponseWriter, r *http.Request) {
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
	s.render(w, r, "secrets", "job-log", job)
}

// Workload-page secrets pane: the same registry, scoped to one
// workload. Creation here is the barebones flow — name + value with
// default identity, pre-delivered into this workload; identity and
// other deliveries are edited on the Secrets tab.

// secretsWorkload resolves the {name} workload and requires its type to
// deliver secret mounts (the pane isn't rendered otherwise, so a miss
// is a hand-crafted request).
func (s *Server) secretsWorkload(w http.ResponseWriter, r *http.Request) (*store.Workload, bool) {
	wl, ok := s.lookupWorkload(w, r)
	if !ok {
		return nil, false
	}
	if wt := workloadType(wl.Type); !wt.SupportsSecretMounts {
		http.NotFound(w, r)
		return nil, false
	}
	return wl, true
}

func (s *Server) handleWorkloadSecretCreate(w http.ResponseWriter, r *http.Request) {
	wl, ok := s.secretsWorkload(w, r)
	if !ok {
		return
	}
	back := "/workloads/" + wl.Name
	fail := func(msg string) {
		http.Redirect(w, r, back+"?error="+url.QueryEscape(msg), http.StatusSeeOther)
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if err := nix.ValidateName(name); err != nil {
		fail(err.Error())
		return
	}
	f, errMsg := s.parseSecretForm(r) // no identity fields posted → defaults
	if errMsg != "" {
		fail(errMsg)
		return
	}
	if f.Value == "" {
		fail(s.t(r, "err.value-required"))
		return
	}
	if _, err := s.store.SecretByName(name); err == nil {
		fail(s.t(r, "err.secret-exists", name))
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	if err := s.encryptSecret(name, f.Value); err != nil {
		fail(err.Error())
		return
	}
	if _, err := s.store.CreateSecret(name, f.Owner, f.Group, f.Mode, []int64{wl.ID}); err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, back+"?flash="+url.QueryEscape(s.t(r, "flash.secret-added")), http.StatusSeeOther)
}

func (s *Server) handleWorkloadSecretAttach(w http.ResponseWriter, r *http.Request) {
	s.setWorkloadSecretMount(w, r, true)
}

func (s *Server) handleWorkloadSecretDetach(w http.ResponseWriter, r *http.Request) {
	s.setWorkloadSecretMount(w, r, false)
}

func (s *Server) setWorkloadSecretMount(w http.ResponseWriter, r *http.Request, attach bool) {
	wl, ok := s.secretsWorkload(w, r)
	if !ok {
		return
	}
	back := "/workloads/" + wl.Name
	secName := r.FormValue("secret")
	if err := nix.ValidateName(secName); err != nil {
		http.Redirect(w, r, back+"?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	sec, err := s.store.SecretByName(secName)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	flash := s.t(r, "flash.secret-attached")
	if attach {
		err = s.store.AddSecretMount(sec.ID, wl.ID)
	} else {
		err = s.store.RemoveSecretMount(sec.ID, wl.ID)
		flash = s.t(r, "flash.secret-detached")
	}
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, back+"?flash="+url.QueryEscape(flash), http.StatusSeeOther)
}

func (s *Server) lookupSecret(w http.ResponseWriter, r *http.Request) (*store.Secret, bool) {
	name := r.PathValue("name")
	if err := nix.ValidateName(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil, false
	}
	sec, err := s.store.SecretByName(name)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return nil, false
	}
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return nil, false
	}
	return sec, true
}
