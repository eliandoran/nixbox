// Package server implements the nixbox HTTP interface: server-rendered
// pages with HTMX partial swaps and SSE job-log streaming.
package server

import (
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/elian/nixbox/internal/config"
	"github.com/elian/nixbox/internal/jobs"
	"github.com/elian/nixbox/internal/machine"
	"github.com/elian/nixbox/internal/nix"
	"github.com/elian/nixbox/internal/store"
	"github.com/elian/nixbox/web"
)

type Server struct {
	cfg      config.Config
	store    *store.Store
	flake    *nix.StateFlake
	jobs     *jobs.Manager
	pipeline *nix.Pipeline
	machines *machine.Manager
	mux      *http.ServeMux
	pages    map[string]*template.Template
}

func New(cfg config.Config, st *store.Store, flake *nix.StateFlake, jm *jobs.Manager,
	pl *nix.Pipeline, mm *machine.Manager) (*Server, error) {

	s := &Server{
		cfg:      cfg,
		store:    st,
		flake:    flake,
		jobs:     jm,
		pipeline: pl,
		machines: mm,
		mux:      http.NewServeMux(),
	}

	if err := s.parseTemplates(); err != nil {
		return nil, err
	}

	static, err := fs.Sub(web.FS, "static")
	if err != nil {
		return nil, err
	}
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))

	s.mux.HandleFunc("GET /{$}", s.handleDashboard)
	s.mux.HandleFunc("GET /partials/workloads", s.handleWorkloadCards)
	s.mux.HandleFunc("GET /workloads/new", s.handleWorkloadNew)
	s.mux.HandleFunc("POST /workloads", s.handleWorkloadCreate)
	s.mux.HandleFunc("GET /workloads/{name}", s.handleWorkloadDetail)
	s.mux.HandleFunc("GET /workloads/{name}/logs", s.handleWorkloadLogs)
	s.mux.HandleFunc("POST /workloads/{name}/save", s.handleWorkloadSave)
	s.mux.HandleFunc("POST /workloads/{name}/validate", s.handleWorkloadValidate)
	s.mux.HandleFunc("POST /workloads/{name}/enable", s.handleWorkloadEnable)
	s.mux.HandleFunc("POST /workloads/{name}/disable", s.handleWorkloadDisable)
	s.mux.HandleFunc("POST /workloads/{name}/apply", s.handleWorkloadApply)
	s.mux.HandleFunc("POST /workloads/{name}/destroy", s.handleWorkloadDestroy)
	s.mux.HandleFunc("POST /workloads/{name}/revisions/{id}/restore", s.handleWorkloadRestore)
	s.mux.HandleFunc("POST /workloads/{name}/{verb}", s.handleWorkloadLifecycle)
	s.mux.HandleFunc("GET /system", s.handleSystem)
	s.mux.HandleFunc("POST /system/rebuild", s.handleRebuild)
	s.mux.HandleFunc("GET /system/jobs/{id}/log", s.handleJobLogFragment)
	s.mux.HandleFunc("GET /events/jobs/{id}", s.handleJobEvents)

	return s, nil
}

func (s *Server) Handler() http.Handler { return s.mux }

// parseTemplates builds one template set per page (layout + page), so
// pages can define the same block names without clashing.
func (s *Server) parseTemplates() error {
	pages := []string{"dashboard", "system", "workload", "workload_new"}
	s.pages = make(map[string]*template.Template, len(pages))
	for _, name := range pages {
		t, err := template.ParseFS(web.FS, "templates/layout.html", "templates/"+name+".html")
		if err != nil {
			return err
		}
		s.pages[name] = t
	}
	return nil
}

// baseData is embedded in every page's template data.
type baseData struct {
	Title    string
	Nav      string
	HostAttr string
}

func (s *Server) base(title, nav string) baseData {
	return baseData{Title: title, Nav: nav, HostAttr: s.cfg.HostAttr}
}

func (s *Server) render(w http.ResponseWriter, page, block string, data any) {
	t, ok := s.pages[page]
	if !ok {
		http.Error(w, "unknown page "+page, http.StatusInternalServerError)
		return
	}
	if err := t.ExecuteTemplate(w, block, data); err != nil {
		slog.Error("rendering template", "page", page, "block", block, "err", err)
	}
}

func (s *Server) renderPage(w http.ResponseWriter, page string, data any) {
	s.render(w, page, "layout", data)
}

func httpError(w http.ResponseWriter, err error, code int) {
	slog.Error("request failed", "err", err)
	http.Error(w, err.Error(), code)
}
