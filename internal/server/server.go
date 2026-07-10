// Package server implements the nixbox HTTP interface: server-rendered
// pages with HTMX partial swaps and SSE job-log streaming.
package server

import (
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"os"

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

	static, err := fs.Sub(s.assetFS(), "static")
	if err != nil {
		return nil, err
	}
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))

	s.mux.HandleFunc("GET /{$}", s.handleDashboard)
	s.mux.HandleFunc("GET /partials/workloads", s.handleWorkloadList)
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
	s.mux.HandleFunc("POST /system/reboot", s.handleReboot)
	s.mux.HandleFunc("POST /system/poweroff", s.handlePoweroff)
	s.mux.HandleFunc("GET /system/jobs/{id}/log", s.handleJobLogFragment)
	s.mux.HandleFunc("GET /events/jobs/{id}", s.handleJobEvents)
	s.mux.HandleFunc("GET /events/metrics", s.handleMetrics)

	return s, nil
}

func (s *Server) Handler() http.Handler { return s.mux }

// assetFS returns the filesystem backing /static and the templates,
// rooted so that "static" and "templates/..." resolve. In dev mode it
// reads live from ./web on disk (edits show on refresh, no recompile);
// otherwise it uses the binary's embedded FS.
func (s *Server) assetFS() fs.FS {
	if s.cfg.Dev {
		return os.DirFS("web")
	}
	return web.FS
}

// parseTemplates builds one template set per page (layout + page), so
// pages can define the same block names without clashing. In dev mode
// it is re-run on every render so template edits show on a refresh.
func (s *Server) parseTemplates() error {
	pages := []string{"dashboard", "system", "workload", "workload_new"}
	src := s.assetFS()
	s.pages = make(map[string]*template.Template, len(pages))
	for _, name := range pages {
		t, err := template.ParseFS(src, "templates/layout.html", "templates/"+name+".html")
		if err != nil {
			return err
		}
		s.pages[name] = t
	}
	return nil
}

// baseData is embedded in every page's template data. It carries the
// workload list rendered in the persistent sidebar on every page.
type baseData struct {
	Title          string
	Nav            string
	HostAttr       string
	Active         string // name of the currently-viewed workload (sidebar highlight)
	WorkloadGroups []workloadGroup
}

func (s *Server) base(r *http.Request, title, nav string) baseData {
	b := baseData{Title: title, Nav: nav, HostAttr: s.cfg.HostAttr}
	views, err := s.workloadViews(r)
	if err != nil {
		slog.Error("loading sidebar workloads", "err", err)
	}
	b.WorkloadGroups = groupWorkloads(views)
	return b
}

func (s *Server) render(w http.ResponseWriter, page, block string, data any) {
	if s.cfg.Dev {
		if err := s.parseTemplates(); err != nil {
			slog.Error("re-parsing templates", "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
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
