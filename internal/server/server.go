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
	"github.com/elian/nixbox/internal/i18n"
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
	i18n     *i18n.Bundle
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

	if err := s.loadCatalogs(); err != nil {
		return nil, err
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
	s.mux.HandleFunc("GET /partials/workload-fields", s.handleWorkloadFields)
	s.mux.HandleFunc("POST /workloads", s.handleWorkloadCreate)
	s.mux.HandleFunc("GET /workloads/{name}", s.handleWorkloadDetail)
	s.mux.HandleFunc("GET /workloads/{name}/logs", s.handleWorkloadLogs)
	s.mux.HandleFunc("GET /events/workloads/{name}/metrics", s.handleWorkloadMetrics)
	s.mux.HandleFunc("POST /workloads/{name}/save", s.handleWorkloadSave)
	s.mux.HandleFunc("POST /workloads/{name}/validate", s.handleWorkloadValidate)
	s.mux.HandleFunc("POST /workloads/{name}/enable", s.handleWorkloadEnable)
	s.mux.HandleFunc("POST /workloads/{name}/disable", s.handleWorkloadDisable)
	s.mux.HandleFunc("POST /workloads/{name}/apply", s.handleWorkloadApply)
	s.mux.HandleFunc("POST /workloads/{name}/rename", s.handleWorkloadRename)
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

// loadCatalogs loads the UI message catalogs from web/i18n. Like the
// templates, they are reloaded per request in dev mode so edits to
// en.json show on a browser refresh.
func (s *Server) loadCatalogs() error {
	b, err := i18n.Load(s.assetFS(), "i18n")
	if err != nil {
		return err
	}
	s.i18n = b
	return nil
}

// i18nFuncs are the template funcs backed by a localizer: T looks up a
// message by key, lang reports the active locale (e.g. <html lang>).
// They are registered at parse time with the server default and rebound
// to the request's locale in render.
func i18nFuncs(loc *i18n.Localizer) template.FuncMap {
	return template.FuncMap{"T": loc.T, "lang": loc.Lang}
}

// defaultLocalizer resolves the server's configured default locale. Used
// only to satisfy parse-time func binding; requests use s.localizer.
func (s *Server) defaultLocalizer() *i18n.Localizer {
	return s.i18n.Localizer(s.cfg.Lang)
}

// localizer resolves the locale for a request, most-preferred first: an
// explicit ?lang= (handy for testing), a nixbox-lang cookie, the
// browser's Accept-Language, then the server default. The Localizer
// always falls back to English last.
func (s *Server) localizer(r *http.Request) *i18n.Localizer {
	var prefs []string
	if q := r.URL.Query().Get("lang"); q != "" {
		prefs = append(prefs, q)
	}
	if c, err := r.Cookie("nixbox-lang"); err == nil {
		prefs = append(prefs, c.Value)
	}
	prefs = append(prefs, i18n.ParseAcceptLanguage(r.Header.Get("Accept-Language"))...)
	prefs = append(prefs, s.cfg.Lang)
	return s.i18n.Localizer(prefs...)
}

// parseTemplates builds one template set per page (layout + page), so
// pages can define the same block names without clashing. The T/lang
// funcs are bound with the default locale so templates parse; render
// clones and rebinds them per request. In dev mode this is re-run on
// every render so template edits show on a refresh.
func (s *Server) parseTemplates() error {
	pages := []string{"dashboard", "system", "workload", "workload_new"}
	src := s.assetFS()
	funcs := i18nFuncs(s.defaultLocalizer())
	s.pages = make(map[string]*template.Template, len(pages))
	for _, name := range pages {
		t, err := template.New(name).Funcs(funcs).ParseFS(src, "templates/layout.html", "templates/"+name+".html")
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

func (s *Server) render(w http.ResponseWriter, r *http.Request, page, block string, data any) {
	if s.cfg.Dev {
		if err := s.loadCatalogs(); err != nil {
			slog.Error("re-loading catalogs", "err", err)
		}
		if err := s.parseTemplates(); err != nil {
			slog.Error("re-parsing templates", "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	base, ok := s.pages[page]
	if !ok {
		http.Error(w, "unknown page "+page, http.StatusInternalServerError)
		return
	}
	// Clone so binding this request's locale doesn't mutate (and race on)
	// the shared page template. Funcs must be present at parse for the
	// template to compile; here we swap in the request-scoped versions.
	t, err := base.Clone()
	if err != nil {
		slog.Error("cloning template", "page", page, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	t.Funcs(i18nFuncs(s.localizer(r)))
	if err := t.ExecuteTemplate(w, block, data); err != nil {
		slog.Error("rendering template", "page", page, "block", block, "err", err)
	}
}

func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, page string, data any) {
	s.render(w, r, page, "layout", data)
}

func httpError(w http.ResponseWriter, err error, code int) {
	slog.Error("request failed", "err", err)
	http.Error(w, err.Error(), code)
}
