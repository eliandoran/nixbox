package server

import (
	"net/http"
	"os"
	"strings"

	"github.com/elian/nixbox/internal/machine"
	"github.com/elian/nixbox/internal/nix"
)

type hostInfo struct {
	Hostname   string
	OSVersion  string
	Generation int64
	StateDir   string
}

type workloadView struct {
	Name    string // ID: used in the item href and active-highlight match
	Display string // friendly label shown to the user (falls back to Name)
	Type    string
	Running bool
	State   string
}

// workloadGroup buckets workloads of the same type under one sidebar heading.
type workloadGroup struct {
	Label     string
	Workloads []workloadView
}

// workloadTypeLabel is the sidebar heading shown for a workload type. An
// unregistered type (stale row) falls back to its raw ID.
func workloadTypeLabel(t string) string {
	if wt, ok := nix.Lookup(t); ok {
		return wt.Label
	}
	return t
}

// groupWorkloads buckets workloads by type, preserving the order each type
// first appears in (so the grouping is stable across polls).
func groupWorkloads(views []workloadView) []workloadGroup {
	var groups []workloadGroup
	index := make(map[string]int)
	for _, v := range views {
		i, ok := index[v.Type]
		if !ok {
			i = len(groups)
			index[v.Type] = i
			groups = append(groups, workloadGroup{Label: workloadTypeLabel(v.Type)})
		}
		groups[i].Workloads = append(groups[i].Workloads, v)
	}
	return groups
}

type dashboardData struct {
	baseData
	Host hostInfo
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	data := dashboardData{baseData: s.base(r, "Dashboard", "dashboard")}
	data.Host = s.hostInfo()
	s.renderPage(w, "dashboard", data)
}

// handleWorkloadList is the HTMX polling target that refreshes the sidebar
// workload list (status dots). The ?active= query param preserves the
// highlight of the workload currently being viewed.
func (s *Server) handleWorkloadList(w http.ResponseWriter, r *http.Request) {
	views, err := s.workloadViews(r)
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	data := baseData{Active: r.URL.Query().Get("active"), WorkloadGroups: groupWorkloads(views)}
	s.render(w, "dashboard", "workload-list", data)
}

func (s *Server) workloadViews(r *http.Request) ([]workloadView, error) {
	workloads, err := s.store.Workloads()
	if err != nil {
		return nil, err
	}
	views := make([]workloadView, 0, len(workloads))
	for _, wl := range workloads {
		v := workloadView{Name: wl.Name, Display: wl.Display(), Type: wl.Type}
		switch {
		case !wl.Enabled:
			v.State = "disabled"
		default:
			st, err := s.machines.Status(r.Context(), workloadType(wl.Type), wl.Name)
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

// enabledWorkloadRefs lists the enabled workloads as machine refs, in
// stored order — the set the metrics stream samples resource usage for.
func (s *Server) enabledWorkloadRefs() ([]machine.Ref, error) {
	workloads, err := s.store.Workloads()
	if err != nil {
		return nil, err
	}
	refs := make([]machine.Ref, 0, len(workloads))
	for _, wl := range workloads {
		if wl.Enabled {
			refs = append(refs, machine.Ref{Type: workloadType(wl.Type), Name: wl.Name})
		}
	}
	return refs, nil
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
