package server

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/elian/nixbox/internal/store"
)

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
