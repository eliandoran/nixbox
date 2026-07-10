package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/elian/nixbox/internal/machine"
	"github.com/elian/nixbox/internal/metrics"
	"github.com/elian/nixbox/internal/store"
)

// handleWorkloadLogs streams a container's journal over SSE by piping
// a follow-mode journalctl. The process lives exactly as long as the
// client connection: closing the browser tab cancels the request
// context, which kills journalctl.
func (s *Server) handleWorkloadLogs(w http.ResponseWriter, r *http.Request) {
	wl, ok := s.lookupWorkload(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	inside := r.URL.Query().Get("source") == "container"
	cmd := machine.JournalCommand(r.Context(), wl.Name, inside)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	cmd.Stderr = cmd.Stdout // journalctl errors belong in the stream too
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(w, "event: append\ndata: cannot start journalctl: %v\n\n", err)
		flusher.Flush()
		return
	}
	defer cmd.Wait()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		fmt.Fprintf(w, "event: append\ndata: %s\n\n", scanner.Text())
		flusher.Flush()
	}
}

// metricsSample is the JSON payload pushed on each metrics tick. CPU
// percentages are pointers because they are undefined on the first
// sample (there is no prior reading to diff against) and render as "—".
type metricsSample struct {
	Host       hostMetrics        `json:"host"`
	Containers []containerMetrics `json:"containers"`
}

type hostMetrics struct {
	Load1     float64  `json:"load1"`
	Load5     float64  `json:"load5"`
	Load15    float64  `json:"load15"`
	CPUPct    *float64 `json:"cpuPct"`
	MemUsed   uint64   `json:"memUsed"`
	MemTotal  uint64   `json:"memTotal"`
	DiskUsed  uint64   `json:"diskUsed"`
	DiskTotal uint64   `json:"diskTotal"`
}

type containerMetrics struct {
	Name     string   `json:"name"`
	Running  bool     `json:"running"`
	CPUPct   *float64 `json:"cpuPct"`
	MemBytes uint64   `json:"memBytes"`
	Tasks    uint64   `json:"tasks"`
}

// handleMetrics streams host and per-container resource usage over SSE.
// Each tick reads systemd's accounting and /proc, diffs the cumulative
// CPU counters against the previous tick to derive live percentages, and
// pushes one "sample" JSON event. Per-connection state (the previous
// counters) lives on the stack, so no shared locking is needed.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	// The Nix store is what fills up on a NixOS box; fall back to root
	// where it isn't a distinct path (e.g. the dev machine).
	diskPath := "/nix"
	if _, err := os.Stat(diskPath); err != nil {
		diskPath = "/"
	}

	var (
		prevTime  time.Time
		prevBusy  uint64
		prevTotal uint64
		prevCPU   = map[string]uint64{}
		havePrev  bool
		ticker    = time.NewTicker(2 * time.Second)
	)
	defer ticker.Stop()

	for {
		now := time.Now()
		host := metrics.Sample(diskPath)

		names, err := s.enabledWorkloadNames()
		if err != nil {
			httpError(w, err, http.StatusInternalServerError)
			return
		}
		usages, _ := s.machines.Usages(r.Context(), names) // absent → zeroed below

		sample := metricsSample{Host: hostMetrics{
			Load1: host.Load1, Load5: host.Load5, Load15: host.Load15,
			MemUsed: host.MemUsed, MemTotal: host.MemTotal,
			DiskUsed: host.DiskUsed, DiskTotal: host.DiskTotal,
		}}

		wallNS := now.Sub(prevTime).Nanoseconds()
		if havePrev && host.CPUTotal > prevTotal {
			pct := float64(host.CPUBusy-prevBusy) / float64(host.CPUTotal-prevTotal) * 100
			sample.Host.CPUPct = &pct
		}

		curCPU := make(map[string]uint64, len(names))
		for _, name := range names {
			u := usages[name]
			curCPU[name] = u.CPUNSec
			cm := containerMetrics{
				Name: name, Running: u.Running,
				MemBytes: u.MemBytes, Tasks: u.Tasks,
			}
			if prev, ok := prevCPU[name]; havePrev && ok && wallNS > 0 && u.CPUNSec >= prev {
				pct := float64(u.CPUNSec-prev) / float64(wallNS) * 100
				cm.CPUPct = &pct
			}
			sample.Containers = append(sample.Containers, cm)
		}

		buf, err := json.Marshal(sample)
		if err != nil {
			httpError(w, err, http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "event: sample\ndata: %s\n\n", buf)
		flusher.Flush()

		prevTime, prevBusy, prevTotal, prevCPU, havePrev = now, host.CPUBusy, host.CPUTotal, curCPU, true

		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

// handleJobEvents streams a job's log over SSE by tailing its log file.
// Events: "append" carries one log line; "done" carries the final
// status and ends the stream. Tailing the file (rather than an
// in-memory broker) means reconnects and post-restart views replay the
// full log for free.
func (s *Server) handleJobEvents(w http.ResponseWriter, r *http.Request) {
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

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	var offset int64
	var pending string // partial last line carried between polls
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		// Read anything appended since the last poll.
		if f, err := os.Open(job.LogPath); err == nil {
			if st, err := f.Stat(); err == nil && st.Size() > offset {
				buf := make([]byte, st.Size()-offset)
				if _, err := f.ReadAt(buf, offset); err == nil {
					offset = st.Size()
					pending += string(buf)
					lines := strings.Split(pending, "\n")
					pending = lines[len(lines)-1]
					for _, line := range lines[:len(lines)-1] {
						fmt.Fprintf(w, "event: append\ndata: %s\n\n", line)
					}
					flusher.Flush()
				}
			}
			f.Close()
		}

		current, err := s.store.JobByID(id)
		if err != nil || current.Status != store.JobRunning {
			if pending != "" {
				fmt.Fprintf(w, "event: append\ndata: %s\n\n", pending)
			}
			status := store.JobFailed
			if err == nil {
				status = current.Status
			}
			fmt.Fprintf(w, "event: done\ndata: %s\n\n", status)
			flusher.Flush()
			return
		}

		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}
