package server

import (
	"net/http"
	"os"
	"strings"

	"github.com/elian/nixbox/internal/nix"
)

type hostInfo struct {
	Hostname   string
	OSVersion  string
	Generation int64
	StateDir   string
}

type workloadView struct {
	Name    string
	Type    string
	Running bool
	State   string
}

type dashboardData struct {
	baseData
	Host      hostInfo
	Workloads []workloadView
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	data := dashboardData{baseData: s.base("Dashboard", "dashboard")}
	data.Host = s.hostInfo()

	views, err := s.workloadViews(r)
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	data.Workloads = views
	s.renderPage(w, "dashboard", data)
}

// handleWorkloadCards is the HTMX polling target refreshing status dots.
func (s *Server) handleWorkloadCards(w http.ResponseWriter, r *http.Request) {
	views, err := s.workloadViews(r)
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	s.render(w, "dashboard", "workload-cards", dashboardData{Workloads: views})
}

func (s *Server) workloadViews(r *http.Request) ([]workloadView, error) {
	workloads, err := s.store.Workloads()
	if err != nil {
		return nil, err
	}
	views := make([]workloadView, 0, len(workloads))
	for _, wl := range workloads {
		v := workloadView{Name: wl.Name, Type: wl.Type}
		switch {
		case !wl.Enabled:
			v.State = "disabled"
		default:
			st, err := s.machines.Status(r.Context(), wl.Name)
			if err != nil {
				v.State = "status unavailable"
			} else {
				v.Running = st.Running()
				v.State = st.ActiveState + " (" + st.SubState + ")"
			}
		}
		views = append(views, v)
	}
	return views, nil
}

func (s *Server) hostInfo() hostInfo {
	info := hostInfo{StateDir: s.cfg.StateDir}
	info.Hostname, _ = os.Hostname()
	info.OSVersion = osPrettyName()
	if gen, err := nix.CurrentGeneration(); err == nil {
		info.Generation = gen
	}
	return info
}

func osPrettyName() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "unknown"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return "unknown"
}
