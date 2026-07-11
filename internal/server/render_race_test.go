package server

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestRenderConcurrentDevReload hammers rendering while dev mode re-parses
// templates and reloads catalogs on every request. Before pages/i18n were
// guarded, two requests rebuilding s.pages at once tripped the runtime's
// "concurrent map writes" fatal; the race detector flags the swap races too.
func TestRenderConcurrentDevReload(t *testing.T) {
	// Dev mode serves assets from os.DirFS("web"), relative to cwd; run
	// from the repo root (two levels up from this package) so the reload
	// actually parses templates instead of erroring out before the map.
	t.Chdir("../..")

	s := newTestServer(t)
	s.cfg.Dev = true // force the per-request reload path

	paths := []string{"/", "/system", "/flakes", "/partials/workloads"}

	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, p, nil)
			s.mux.ServeHTTP(httptest.NewRecorder(), req)
		}(paths[i%len(paths)])
	}
	wg.Wait()
}
