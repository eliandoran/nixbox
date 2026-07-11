package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/elian/nixbox/internal/jobs"
	"github.com/elian/nixbox/internal/nix"
	"github.com/elian/nixbox/internal/store"
)

// holdJob occupies the single job slot until the returned release func is
// called, for exercising the 409-busy guards.
func holdJob(t *testing.T, s *Server) (release func()) {
	t.Helper()
	ch := make(chan struct{})
	if _, err := s.jobs.Start(store.JobApply, nil, func(context.Context, io.Writer) (jobs.Result, error) {
		<-ch
		return jobs.Result{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var once bool
	return func() {
		if !once {
			once = true
			close(ch)
			waitIdle(t, s)
		}
	}
}

func TestDashboardAndSidebar(t *testing.T) {
	s := newTestServer(t)
	addWorkload(t, s, "web", nix.WorkloadTypeContainer, true)
	addWorkload(t, s, "off", nix.WorkloadTypeContainer, false)
	// A stale row of an unregistered type must not break the sidebar
	// (workloadType and workloadTypeLabel fall back).
	addWorkload(t, s, "ghost", "microvm", true)

	w := get(t, s, "/")
	if w.Code != http.StatusOK {
		t.Fatalf("dashboard: %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"web", "off", "ghost", "microvm"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}

	// Sidebar polling partial, with active highlight.
	w = get(t, s, "/partials/workloads?active=web")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "web") {
		t.Fatalf("workload list partial: %d", w.Code)
	}

	// Store failure surfaces as 500 on the partial.
	dropTable(t, s, "workloads")
	if w := get(t, s, "/partials/workloads"); w.Code != http.StatusInternalServerError {
		t.Errorf("partial with dropped workloads: %d", w.Code)
	}
	// The dashboard itself still renders: base() only logs sidebar errors.
	if w := get(t, s, "/"); w.Code != http.StatusOK {
		t.Errorf("dashboard with dropped workloads: %d", w.Code)
	}
}

func TestWorkloadNewAndFields(t *testing.T) {
	s := newTestServer(t)
	if w := get(t, s, "/workloads/new"); w.Code != http.StatusOK {
		t.Fatalf("new page: %d", w.Code)
	}
	// The per-type fields partial, for a picked type and the fallback.
	for _, q := range []string{"?type=" + nix.WorkloadTypeOCI, "?type=bogus", ""} {
		if w := get(t, s, "/partials/workload-fields"+q); w.Code != http.StatusOK {
			t.Errorf("fields partial %q: %d", q, w.Code)
		}
	}
}

func TestWorkloadCreate(t *testing.T) {
	s := newTestServer(t)

	// Happy path redirects to the new workload's page and persists both
	// the row and the on-disk file.
	w := post(t, s, "/workloads", url.Values{
		"name": {"web"}, "display_name": {"My Web"},
		"type": {nix.WorkloadTypeContainer}, "template": {"blank"},
	})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/workloads/web" {
		t.Fatalf("create: %d -> %q", w.Code, w.Header().Get("Location"))
	}
	wl, err := s.store.WorkloadByName("web")
	if err != nil {
		t.Fatal(err)
	}
	if wl.DisplayName != "My Web" || wl.Type != nix.WorkloadTypeContainer {
		t.Errorf("created workload: %+v", wl)
	}
	if _, err := s.flake.ReadWorkload("web"); err != nil {
		t.Errorf("workload.nix not written: %v", err)
	}

	// Validation failures re-render the form with 422.
	for name, form := range map[string]url.Values{
		"unknown type":     {"name": {"ok"}, "type": {"bogus"}, "template": {"blank"}},
		"bad name":         {"name": {"NOPE"}, "type": {nix.WorkloadTypeContainer}, "template": {"blank"}},
		"bad display name": {"name": {"ok"}, "display_name": {strings.Repeat("x", 200)}, "type": {nix.WorkloadTypeContainer}, "template": {"blank"}},
		"unknown template": {"name": {"ok"}, "type": {nix.WorkloadTypeContainer}, "template": {"bogus"}},
		"duplicate":        {"name": {"web"}, "type": {nix.WorkloadTypeContainer}, "template": {"blank"}},
	} {
		if w := post(t, s, "/workloads", form); w.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d, want 422", name, w.Code)
		}
	}

	// An unwritable workloads dir fails the file write.
	if err := os.Chmod(s.cfg.WorkloadsDir(), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(s.cfg.WorkloadsDir(), 0o755) })
	if w := post(t, s, "/workloads", url.Values{
		"name": {"denied"}, "type": {nix.WorkloadTypeContainer}, "template": {"blank"},
	}); w.Code != http.StatusInternalServerError {
		t.Errorf("create with unwritable dir: %d", w.Code)
	}
	os.Chmod(s.cfg.WorkloadsDir(), 0o755)

	// Row insert failing after the file write.
	denyWrites(t, s, "workloads")
	if w := post(t, s, "/workloads", url.Values{
		"name": {"newer"}, "type": {nix.WorkloadTypeContainer}, "template": {"blank"},
	}); w.Code != http.StatusInternalServerError {
		t.Errorf("create with denied row insert: %d", w.Code)
	}

	// The duplicate check erroring outright.
	s2 := newTestServer(t)
	dropTable(t, s2, "workloads")
	if w := post(t, s2, "/workloads", url.Values{
		"name": {"ok"}, "type": {nix.WorkloadTypeContainer}, "template": {"blank"},
	}); w.Code != http.StatusInternalServerError {
		t.Errorf("create with dropped workloads: %d", w.Code)
	}
}

func TestWorkloadSaveAndValidate(t *testing.T) {
	s := newTestServer(t)
	wl := addWorkload(t, s, "web", nix.WorkloadTypeContainer, true)

	// CRLF content without trailing newline is normalized; ports are
	// snapshotted with the revision.
	w := post(t, s, "/workloads/web/save", url.Values{
		"content": {"{ autoStart = true;\r\n}"},
		"port":    {"8080"}, "proto": {"tcp"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	rev, err := s.store.LatestRevision(wl.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rev.Content != "{ autoStart = true;\n}\n" || rev.Ports != "8080/tcp" {
		t.Errorf("saved revision: %q %q", rev.Content, rev.Ports)
	}

	// A syntax error still saves (drafts allowed) but reports inline.
	if w := post(t, s, "/workloads/web/save", url.Values{"content": {"{ oops"}}); w.Code != http.StatusOK {
		t.Fatalf("draft save: %d", w.Code)
	}
	if rev2, _ := s.store.LatestRevision(wl.ID); rev2.ID == rev.ID {
		t.Error("draft with syntax error was not saved")
	}

	// A malformed port rejects the whole save — no new revision.
	before, _ := s.store.LatestRevision(wl.ID)
	if w := post(t, s, "/workloads/web/save", url.Values{
		"content": {"{ }"}, "port": {"nope"}, "proto": {"tcp"},
	}); w.Code != http.StatusOK {
		t.Fatalf("bad-port save: %d", w.Code)
	}
	if after, _ := s.store.LatestRevision(wl.ID); after.ID != before.ID {
		t.Error("bad-port save created a revision")
	}

	// Validate runs the checks against the saved (currently broken) file,
	// then against one that parses but fails eval, then a clean one; all
	// render the result fragment.
	if w := post(t, s, "/workloads/web/validate", nil); w.Code != http.StatusOK {
		t.Fatalf("validate broken: %d", w.Code)
	}
	if w := post(t, s, "/workloads/web/save", url.Values{"content": {`builtins.throw "boom"`}}); w.Code != http.StatusOK {
		t.Fatal("eval-error save")
	}
	if w := post(t, s, "/workloads/web/validate", nil); w.Code != http.StatusOK {
		t.Fatalf("validate eval error: %d", w.Code)
	}
	if w := post(t, s, "/workloads/web/save", url.Values{"content": {"{ }"}}); w.Code != http.StatusOK {
		t.Fatal("clean save")
	}
	if w := post(t, s, "/workloads/web/validate", nil); w.Code != http.StatusOK {
		t.Fatalf("validate clean: %d", w.Code)
	}

	// Guards shared via lookupWorkload.
	if w := post(t, s, "/workloads/ghost/save", url.Values{"content": {"{ }"}}); w.Code != http.StatusNotFound {
		t.Errorf("unknown workload: %d", w.Code)
	}
	if w := post(t, s, "/workloads/NOPE/save", nil); w.Code != http.StatusBadRequest {
		t.Errorf("invalid name: %d", w.Code)
	}

	// Revision insert denied after the file write.
	denyWrites(t, s, "revisions")
	if w := post(t, s, "/workloads/web/save", url.Values{"content": {"{ }"}}); w.Code != http.StatusInternalServerError {
		t.Errorf("save with denied revisions: %d", w.Code)
	}

	// lookupWorkload's non-NotFound store error.
	dropTable(t, s, "workloads")
	if w := post(t, s, "/workloads/web/save", nil); w.Code != http.StatusInternalServerError {
		t.Errorf("save with dropped workloads: %d", w.Code)
	}
}

func TestWorkloadEnableDisableRename(t *testing.T) {
	s := newTestServer(t)
	wl := addWorkload(t, s, "web", nix.WorkloadTypeContainer, false)

	if flash, _ := wantRedirect(t, post(t, s, "/workloads/web/enable", nil)); flash == "" {
		t.Error("enable flash missing")
	}
	if got, _ := s.store.WorkloadByID(wl.ID); !got.Enabled {
		t.Error("enable did not persist")
	}
	if flash, _ := wantRedirect(t, post(t, s, "/workloads/web/disable", nil)); flash == "" {
		t.Error("disable flash missing")
	}
	if got, _ := s.store.WorkloadByID(wl.ID); got.Enabled {
		t.Error("disable did not persist")
	}

	if flash, _ := wantRedirect(t, post(t, s, "/workloads/web/rename", url.Values{
		"display_name": {"Friendly"},
	})); flash == "" {
		t.Error("rename flash missing")
	}
	if got, _ := s.store.WorkloadByID(wl.ID); got.Display() != "Friendly" {
		t.Errorf("rename did not persist: %q", got.Display())
	}
	if w := post(t, s, "/workloads/web/rename", url.Values{
		"display_name": {strings.Repeat("x", 200)},
	}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad display name: %d", w.Code)
	}

	denyWrites(t, s, "workloads")
	if w := post(t, s, "/workloads/web/enable", nil); w.Code != http.StatusInternalServerError {
		t.Errorf("enable with denied writes: %d", w.Code)
	}
	if w := post(t, s, "/workloads/web/rename", url.Values{"display_name": {"x"}}); w.Code != http.StatusInternalServerError {
		t.Errorf("rename with denied writes: %d", w.Code)
	}
}

func TestWorkloadApply(t *testing.T) {
	s := newTestServer(t)
	wl := addWorkload(t, s, "web", nix.WorkloadTypeContainer, true)

	// Build mode runs a validate job and must NOT mark anything applied.
	if w := post(t, s, "/workloads/web/apply", url.Values{"mode": {"build"}}); w.Code != http.StatusOK {
		t.Fatalf("build apply: %d %s", w.Code, w.Body.String())
	}
	waitIdle(t, s)
	if got, _ := s.store.WorkloadByID(wl.ID); got.AppliedRevisionID.Valid {
		t.Error("build mode marked revision applied")
	}

	// Switch mode records the applied revision on success.
	if w := post(t, s, "/workloads/web/apply", nil); w.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", w.Code, w.Body.String())
	}
	waitIdle(t, s)
	got, _ := s.store.WorkloadByID(wl.ID)
	rev, _ := s.store.LatestRevision(wl.ID)
	if !got.AppliedRevisionID.Valid || got.AppliedRevisionID.Int64 != rev.ID {
		t.Errorf("applied revision not recorded: %+v", got)
	}

	// Busy guard.
	release := holdJob(t, s)
	if w := post(t, s, "/workloads/web/apply", nil); w.Code != http.StatusConflict {
		t.Errorf("busy apply: %d", w.Code)
	}
	release()

	// startApply failing before the job launches (regenerateFlake).
	dropTable(t, s, "flake_inputs")
	if w := post(t, s, "/workloads/web/apply", nil); w.Code != http.StatusInternalServerError {
		t.Errorf("apply with dropped flake_inputs: %d", w.Code)
	}
}

func TestWorkloadRestore(t *testing.T) {
	s := newTestServer(t)
	wl := addWorkload(t, s, "web", nix.WorkloadTypeContainer, true)
	if w := post(t, s, "/workloads/web/save", url.Values{"content": {"{ edited = true; }"}}); w.Code != http.StatusOK {
		t.Fatal("save")
	}
	revs, err := s.store.Revisions(wl.ID)
	if err != nil || len(revs) != 2 {
		t.Fatalf("revisions: %v %d", err, len(revs))
	}
	first := revs[len(revs)-1] // oldest: the "created" snapshot

	if flash, _ := wantRedirect(t, post(t, s, "/workloads/web/revisions/"+itoa(first.ID)+"/restore", nil)); flash == "" {
		t.Error("restore flash missing")
	}
	latest, _ := s.store.LatestRevision(wl.ID)
	if latest.Content != first.Content || !strings.Contains(latest.Note, "restore") {
		t.Errorf("restore revision: %+v", latest)
	}
	if content, _ := s.flake.ReadWorkload("web"); content != first.Content {
		t.Error("restore did not rewrite workload.nix")
	}

	if w := post(t, s, "/workloads/web/revisions/zzz/restore", nil); w.Code != http.StatusBadRequest {
		t.Errorf("bad revision id: %d", w.Code)
	}
	if w := post(t, s, "/workloads/web/revisions/9999/restore", nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown revision: %d", w.Code)
	}

	denyWrites(t, s, "revisions")
	if w := post(t, s, "/workloads/web/revisions/"+itoa(first.ID)+"/restore", nil); w.Code != http.StatusInternalServerError {
		t.Errorf("restore with denied revisions: %d", w.Code)
	}
	dropTable(t, s, "revisions")
	if w := post(t, s, "/workloads/web/revisions/"+itoa(first.ID)+"/restore", nil); w.Code != http.StatusInternalServerError {
		t.Errorf("restore with dropped revisions: %d", w.Code)
	}
}

func TestWorkloadDestroy(t *testing.T) {
	s := newTestServer(t)
	addWorkload(t, s, "web", nix.WorkloadTypeContainer, true)

	// The name must be re-typed exactly.
	if w := post(t, s, "/workloads/web/destroy", url.Values{"confirm": {"wrong"}}); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("confirm mismatch: %d", w.Code)
	}

	// Busy guard.
	release := holdJob(t, s)
	if w := post(t, s, "/workloads/web/destroy", url.Values{"confirm": {"web"}}); w.Code != http.StatusConflict {
		t.Errorf("busy destroy: %d", w.Code)
	}
	release()

	// Happy path: dry-run rebuild succeeds, row and file are gone.
	w := post(t, s, "/workloads/web/destroy", url.Values{"confirm": {"web"}, "delete_data": {"on"}})
	if w.Code != http.StatusOK {
		t.Fatalf("destroy: %d %s", w.Code, w.Body.String())
	}
	waitIdle(t, s)
	if _, err := s.store.WorkloadByName("web"); err != store.ErrNotFound {
		t.Errorf("workload row survived destroy: %v", err)
	}
	if _, err := s.flake.ReadWorkload("web"); err == nil {
		t.Error("workload.nix survived destroy")
	}

	// setEnabled failing before the job.
	addWorkload(t, s, "web2", nix.WorkloadTypeContainer, true)
	denyWrites(t, s, "workloads")
	if w := post(t, s, "/workloads/web2/destroy", url.Values{"confirm": {"web2"}}); w.Code != http.StatusInternalServerError {
		t.Errorf("destroy with denied writes: %d", w.Code)
	}
}

// failRunner fails every external command: the systemctl stop gets its
// in-log note, and the rebuild failure keeps the workload.
type failRunner struct{}

func (failRunner) Output(context.Context, string, ...string) (string, error) {
	return "", errors.New("boom")
}

func (failRunner) Stream(context.Context, io.Writer, string, ...string) (int, error) {
	return 1, errors.New("boom")
}

func TestWorkloadDestroyFailures(t *testing.T) {
	// A failed rebuild keeps the workload (disabled) — the fail-safe the
	// handler promises in its log message.
	s := newTestServer(t)
	wl := addWorkload(t, s, "keep", nix.WorkloadTypeContainer, true)
	s.pipeline.Runner = failRunner{}
	if w := post(t, s, "/workloads/keep/destroy", url.Values{"confirm": {"keep"}}); w.Code != http.StatusOK {
		t.Fatalf("destroy: %d", w.Code)
	}
	waitIdle(t, s)
	got, err := s.store.WorkloadByName("keep")
	if err != nil {
		t.Fatalf("workload deleted despite failed rebuild: %v", err)
	}
	if got.Enabled {
		t.Error("workload still enabled after failed destroy")
	}
	if recent, _ := s.store.RecentJobs(1); len(recent) != 1 || recent[0].Status != store.JobFailed {
		t.Errorf("destroy job not failed: %+v", recent)
	}
	_ = wl

	// Row delete failing after a successful rebuild fails the job; the
	// DELETE-only trigger leaves the pre-job disable (an UPDATE) working.
	s = newTestServer(t)
	addWorkload(t, s, "gone", nix.WorkloadTypeContainer, true)
	execSQL(t, s, `CREATE TRIGGER deny_delete_workloads BEFORE DELETE ON workloads
		BEGIN SELECT RAISE(ABORT, 'denied'); END`)
	if w := post(t, s, "/workloads/gone/destroy", url.Values{"confirm": {"gone"}}); w.Code != http.StatusOK {
		t.Fatalf("destroy: %d", w.Code)
	}
	waitIdle(t, s)
	if recent, _ := s.store.RecentJobs(1); len(recent) != 1 || recent[0].Status != store.JobFailed {
		t.Errorf("destroy job not failed on row delete: %+v", recent)
	}

	// File removal failing after the row is gone.
	s = newTestServer(t)
	addWorkload(t, s, "web4", nix.WorkloadTypeContainer, true)
	if err := os.Chmod(s.cfg.WorkloadsDir(), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(s.cfg.WorkloadsDir(), 0o755) })
	if w := post(t, s, "/workloads/web4/destroy", url.Values{"confirm": {"web4"}}); w.Code != http.StatusOK {
		t.Fatalf("destroy: %d", w.Code)
	}
	waitIdle(t, s)
	if recent, _ := s.store.RecentJobs(1); len(recent) != 1 || recent[0].Status != store.JobFailed {
		t.Errorf("destroy job not failed on file removal: %+v", recent)
	}
}

func TestWorkloadLifecycle(t *testing.T) {
	s := newTestServer(t)
	addWorkload(t, s, "web", nix.WorkloadTypeContainer, true)

	for _, verb := range []string{"start", "stop", "restart"} {
		w := post(t, s, "/workloads/web/"+verb, nil)
		if w.Code != http.StatusSeeOther {
			t.Errorf("%s: %d, want 303", verb, w.Code)
		}
	}
	if w := post(t, s, "/workloads/web/frobnicate", nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown verb: %d", w.Code)
	}
}

func TestFlakeHandlers(t *testing.T) {
	s := newTestServer(t)

	// Empty list renders.
	if w := get(t, s, "/flakes"); w.Code != http.StatusOK {
		t.Fatalf("list: %d", w.Code)
	}

	// Create.
	if flash, errMsg := wantRedirect(t, post(t, s, "/flakes", url.Values{
		"name": {"nixflix"}, "url": {"github:o/nixflix"}, "follows_nixpkgs": {"on"},
	})); flash == "" || errMsg != "" {
		t.Fatalf("create: %q %q", flash, errMsg)
	}
	in, err := s.store.FlakeInputByName("nixflix")
	if err != nil {
		t.Fatal(err)
	}
	if !in.FollowsNixpkgs {
		t.Error("follows not persisted")
	}
	if body := get(t, s, "/flakes").Body.String(); !strings.Contains(body, "nixflix") {
		t.Error("list missing input")
	}

	// Validation redirects with errors.
	for name, form := range map[string]url.Values{
		"bad name":  {"name": {"NOPE"}, "url": {"github:x/y"}},
		"empty ref": {"name": {"ok"}, "url": {""}},
		"duplicate": {"name": {"nixflix"}, "url": {"github:x/y"}},
	} {
		if _, errMsg := wantRedirect(t, post(t, s, "/flakes", form)); errMsg == "" {
			t.Errorf("%s: expected error redirect", name)
		}
	}

	// Save: ref updates, badge reopens; empty ref rejected.
	if flash, _ := wantRedirect(t, post(t, s, "/flakes/nixflix/save", url.Values{
		"url": {"github:o/nixflix/v2"},
	})); flash == "" {
		t.Error("save flash missing")
	}
	if in, _ := s.store.FlakeInputByName("nixflix"); in.URL != "github:o/nixflix/v2" || in.FollowsNixpkgs {
		t.Errorf("save not persisted: %+v", in)
	}
	if _, errMsg := wantRedirect(t, post(t, s, "/flakes/nixflix/save", url.Values{"url": {""}})); errMsg == "" {
		t.Error("empty ref save: expected error")
	}
	denyWrites(t, s, "flake_inputs")
	if w := post(t, s, "/flakes/nixflix/save", url.Values{"url": {"x"}}); w.Code != http.StatusInternalServerError {
		t.Errorf("save with denied writes: %d", w.Code)
	}
	allowWrites(t, s, "flake_inputs")

	// Apply regenerates flake.nix with the input and marks it applied.
	if w := post(t, s, "/flakes/apply", nil); w.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", w.Code, w.Body.String())
	}
	waitIdle(t, s)
	flakeNix, _ := os.ReadFile(s.cfg.StateFlakeDir() + "/flake.nix")
	if !strings.Contains(string(flakeNix), "github:o/nixflix/v2") {
		t.Errorf("flake.nix missing input:\n%s", flakeNix)
	}
	if in, _ := s.store.FlakeInputByName("nixflix"); in.Status() != "locked" {
		t.Errorf("input not marked applied: %s", in.Status())
	}

	// Build mode leaves the badge pending.
	wantRedirect(t, post(t, s, "/flakes/nixflix/save", url.Values{"url": {"github:o/v3"}}))
	if w := post(t, s, "/flakes/apply", url.Values{"mode": {"build"}}); w.Code != http.StatusOK {
		t.Fatalf("build apply: %d", w.Code)
	}
	waitIdle(t, s)
	if in, _ := s.store.FlakeInputByName("nixflix"); in.Status() != "pending" {
		t.Errorf("build apply locked the badge: %s", in.Status())
	}

	// Busy guard.
	release := holdJob(t, s)
	if w := post(t, s, "/flakes/apply", nil); w.Code != http.StatusConflict {
		t.Errorf("busy apply: %d", w.Code)
	}
	release()

	// Delete failing on a write-denied table.
	denyWrites(t, s, "flake_inputs")
	if w := post(t, s, "/flakes/nixflix/delete", nil); w.Code != http.StatusInternalServerError {
		t.Errorf("delete with denied writes: %d", w.Code)
	}
	allowWrites(t, s, "flake_inputs")

	// Delete, double delete, lookup guards.
	if flash, _ := wantRedirect(t, post(t, s, "/flakes/nixflix/delete", nil)); flash == "" {
		t.Error("delete flash missing")
	}
	if w := post(t, s, "/flakes/nixflix/delete", nil); w.Code != http.StatusNotFound {
		t.Errorf("double delete: %d", w.Code)
	}
	if w := post(t, s, "/flakes/NOPE/save", nil); w.Code != http.StatusBadRequest {
		t.Errorf("invalid name: %d", w.Code)
	}

	// Store faults: list 500, create's duplicate check 500, lookup 500.
	dropTable(t, s, "flake_inputs")
	if w := get(t, s, "/flakes"); w.Code != http.StatusInternalServerError {
		t.Errorf("list with dropped table: %d", w.Code)
	}
	if w := post(t, s, "/flakes", url.Values{"name": {"ok"}, "url": {"github:x/y"}}); w.Code != http.StatusInternalServerError {
		t.Errorf("create with dropped table: %d", w.Code)
	}
	if w := post(t, s, "/flakes/nixflix/save", url.Values{"url": {"x"}}); w.Code != http.StatusInternalServerError {
		t.Errorf("save with dropped table: %d", w.Code)
	}
	if w := post(t, s, "/flakes/apply", nil); w.Code != http.StatusInternalServerError {
		t.Errorf("apply with dropped table: %d", w.Code)
	}
}

func TestSystemHandlers(t *testing.T) {
	s := newTestServer(t)
	addWorkload(t, s, "web", nix.WorkloadTypeContainer, true)

	// Empty system page.
	if w := get(t, s, "/system"); w.Code != http.StatusOK {
		t.Fatalf("system: %d", w.Code)
	}

	// Rebuild runs to completion and shows in history.
	if w := post(t, s, "/system/rebuild", nil); w.Code != http.StatusOK {
		t.Fatalf("rebuild: %d %s", w.Code, w.Body.String())
	}
	waitIdle(t, s)
	jobsList, err := s.store.RecentJobs(5)
	if err != nil || len(jobsList) != 1 || jobsList[0].Status != store.JobOK {
		t.Fatalf("job history: %v %+v", err, jobsList)
	}

	// Historical log fragment + guards.
	if w := get(t, s, "/system/jobs/"+itoa(jobsList[0].ID)+"/log"); w.Code != http.StatusOK {
		t.Errorf("job log fragment: %d", w.Code)
	}
	if w := get(t, s, "/system/jobs/zzz/log"); w.Code != http.StatusBadRequest {
		t.Errorf("bad job id: %d", w.Code)
	}
	if w := get(t, s, "/system/jobs/9999/log"); w.Code != http.StatusNotFound {
		t.Errorf("unknown job: %d", w.Code)
	}

	// While a job runs: the page shows it as active; rebuild and power
	// are refused.
	release := holdJob(t, s)
	if w := get(t, s, "/system"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "running") {
		t.Errorf("system page during job: %d", w.Code)
	}
	if w := post(t, s, "/system/rebuild", nil); w.Code != http.StatusConflict {
		t.Errorf("busy rebuild: %d", w.Code)
	}
	if w := post(t, s, "/system/reboot", nil); w.Code != http.StatusConflict {
		t.Errorf("busy reboot: %d", w.Code)
	}
	release()

	// Power actions: dry-run renders the fragment without touching the
	// machine; non-dry-run goes through the (dry-run) runner.
	for _, path := range []string{"/system/reboot", "/system/poweroff"} {
		if w := post(t, s, path, nil); w.Code != http.StatusOK {
			t.Errorf("dry-run %s: %d", path, w.Code)
		}
	}
	s.cfg.DryRun = false
	for _, path := range []string{"/system/reboot", "/system/poweroff"} {
		if w := post(t, s, path, nil); w.Code != http.StatusOK {
			t.Errorf("live %s: %d", path, w.Code)
		}
	}
	s.cfg.DryRun = true

	// startApply failing before the job launches.
	dropTable(t, s, "flake_inputs")
	if w := post(t, s, "/system/rebuild", nil); w.Code != http.StatusInternalServerError {
		t.Errorf("rebuild with dropped flake_inputs: %d", w.Code)
	}

	// System page and log fragment store faults.
	dropTable(t, s, "jobs")
	if w := get(t, s, "/system"); w.Code != http.StatusInternalServerError {
		t.Errorf("system with dropped jobs: %d", w.Code)
	}
	if w := get(t, s, "/system/jobs/1/log"); w.Code != http.StatusInternalServerError {
		t.Errorf("log fragment with dropped jobs: %d", w.Code)
	}
}

// allowWrites drops the triggers denyWrites installed.
func allowWrites(t *testing.T, s *Server, table string) {
	t.Helper()
	for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
		execSQL(t, s, `DROP TRIGGER deny_`+op+`_`+table)
	}
}

// stubRunner returns fixed output for every command, making read-only
// systemd queries (Status) succeed with a chosen state.
type stubRunner struct{ out string }

func (r stubRunner) Output(context.Context, string, ...string) (string, error) {
	return r.out, nil
}

func (stubRunner) Stream(context.Context, io.Writer, string, ...string) (int, error) {
	return 0, nil
}

func TestWorkloadStatusBranches(t *testing.T) {
	s := newTestServer(t)
	addWorkload(t, s, "web", nix.WorkloadTypeContainer, true)

	// A running unit shows its systemd state on the detail page and the
	// sidebar; the harness's dry-run runner exercises the unavailable leg.
	s.machines.Runner = stubRunner{out: "ActiveState=active\nSubState=running"}
	if body := get(t, s, "/workloads/web").Body.String(); !strings.Contains(body, "dot-on") {
		t.Error("detail page missing running dot")
	}
	if body := get(t, s, "/partials/workloads").Body.String(); !strings.Contains(body, "dot-on") {
		t.Error("sidebar missing running dot")
	}

	// Lifecycle verb failing surfaces inline, not as a redirect.
	s.machines.Runner = failRunner{}
	if w := post(t, s, "/workloads/web/start", nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "boom") {
		t.Errorf("failed start: %d %s", w.Code, w.Body.String())
	}

	// Power actions failing outside dry-run.
	s.cfg.DryRun = false
	if w := post(t, s, "/system/reboot", nil); w.Code != http.StatusInternalServerError {
		t.Errorf("failed reboot: %d", w.Code)
	}

	// Detail page faults: missing workload.nix, then a dropped revisions
	// table.
	if err := os.RemoveAll(s.cfg.WorkloadsDir() + "/web"); err != nil {
		t.Fatal(err)
	}
	if w := get(t, s, "/workloads/web"); w.Code != http.StatusInternalServerError {
		t.Errorf("detail without workload.nix: %d", w.Code)
	}
	if err := s.flake.WriteWorkload("web", "{ }\n"); err != nil {
		t.Fatal(err)
	}
	dropTable(t, s, "revisions")
	if w := get(t, s, "/workloads/web"); w.Code != http.StatusInternalServerError {
		t.Errorf("detail with dropped revisions: %d", w.Code)
	}
}

// TestApplyMarkFailures denies the post-rebuild bookkeeping writes so the
// job goroutine's error returns run — one table per apply.
func TestApplyMarkFailures(t *testing.T) {
	newCase := func(t *testing.T) *Server {
		s := newTestServer(t)
		addWorkload(t, s, "web", nix.WorkloadTypeContainer, true)
		if _, errMsg := wantRedirect(t, post(t, s, "/flakes", url.Values{
			"name": {"in1"}, "url": {"github:x/y"},
		})); errMsg != "" {
			t.Fatal(errMsg)
		}
		if _, errMsg := wantRedirect(t, post(t, s, "/secrets", url.Values{
			"name": {"tok"}, "value": {"v"},
		})); errMsg != "" {
			t.Fatal(errMsg)
		}
		return s
	}
	for _, table := range []string{"workloads", "flake_inputs", "secrets"} {
		t.Run(table, func(t *testing.T) {
			s := newCase(t)
			denyWrites(t, s, table)
			if w := post(t, s, "/system/rebuild", nil); w.Code != http.StatusOK {
				t.Fatalf("rebuild: %d", w.Code)
			}
			waitIdle(t, s)
			if recent, _ := s.store.RecentJobs(1); len(recent) != 1 || recent[0].Status != store.JobFailed {
				t.Errorf("job with denied %s writes: %+v", table, recent)
			}
		})
	}
}

// TestLocalizerSources covers the cookie leg of the request-locale
// resolution (?lang= and Accept-Language are exercised elsewhere).
func TestLocalizerSources(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/?lang=en", nil)
	req.AddCookie(&http.Cookie{Name: "nixbox-lang", Value: "en"})
	req.Header.Set("Accept-Language", "en;q=0.9")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("dashboard with lang prefs: %d", w.Code)
	}
}

func TestSetLang(t *testing.T) {
	s := newTestServer(t)

	// A known locale sets the cookie and strips ?lang from the referer.
	req := httptest.NewRequest(http.MethodPost, "/lang", strings.NewReader("lang=en"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", "http://example.com/system?lang=ro&x=1")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/system?x=1" {
		t.Errorf("setLang: %d -> %q", w.Code, w.Header().Get("Location"))
	}
	found := false
	for _, c := range w.Result().Cookies() {
		if c.Name == "nixbox-lang" && c.Value == "en" {
			found = true
		}
	}
	if !found {
		t.Error("cookie not set")
	}

	// Unknown locales are ignored; no referer falls back to /.
	w2 := post(t, s, "/lang", url.Values{"lang": {"xx"}})
	if w2.Code != http.StatusSeeOther || w2.Header().Get("Location") != "/" {
		t.Errorf("unknown lang: %d -> %q", w2.Code, w2.Header().Get("Location"))
	}
	if len(w2.Result().Cookies()) != 0 {
		t.Error("cookie set for unknown locale")
	}
}

func TestJobEventsSSE(t *testing.T) {
	s := newTestServer(t)

	// Run a job to completion, then stream it: the whole log replays and
	// the stream closes with a done event.
	job, err := s.jobs.Start(store.JobApply, nil, func(_ context.Context, log io.Writer) (jobs.Result, error) {
		io.WriteString(log, "line one\nline two\ntail-no-newline")
		return jobs.Result{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	w := get(t, s, "/events/jobs/"+itoa(job.ID))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("job events: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	body := w.Body.String()
	for _, want := range []string{
		"event: append\ndata: line one\n\n",
		"data: line two",
		"data: tail-no-newline", // the pending partial line is flushed on done
		"event: done\ndata: ok\n\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing %q in:\n%s", want, body)
		}
	}

	if w := get(t, s, "/events/jobs/zzz"); w.Code != http.StatusBadRequest {
		t.Errorf("bad id: %d", w.Code)
	}
	if w := get(t, s, "/events/jobs/9999"); w.Code != http.StatusNotFound {
		t.Errorf("unknown job: %d", w.Code)
	}
}

// sseGet performs a GET with an already-canceled context: SSE loops run
// exactly one iteration, emit one sample, then exit on ctx.Done.
func sseGet(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

func TestMetricsSSE(t *testing.T) {
	s := newTestServer(t)
	addWorkload(t, s, "web", nix.WorkloadTypeContainer, true)

	w := sseGet(t, s, "/events/metrics")
	if w.Code != http.StatusOK {
		t.Fatalf("metrics: %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "event: sample") || !strings.Contains(body, `"host"`) || !strings.Contains(body, `"web"`) {
		t.Errorf("metrics sample missing fields:\n%s", body)
	}

	w = sseGet(t, s, "/events/workloads/web/metrics")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"web"`) {
		t.Fatalf("workload metrics: %d %s", w.Code, w.Body.String())
	}
	if w := sseGet(t, s, "/events/workloads/ghost/metrics"); w.Code != http.StatusNotFound {
		t.Errorf("unknown workload metrics: %d", w.Code)
	}

	dropTable(t, s, "workloads")
	if w := sseGet(t, s, "/events/metrics"); w.Code != http.StatusInternalServerError {
		t.Errorf("metrics with dropped workloads: %d", w.Code)
	}
}

func TestWorkloadLogsSSE(t *testing.T) {
	s := newTestServer(t)
	addWorkload(t, s, "web", nix.WorkloadTypeContainer, true)

	// A canceled context makes journalctl fail to start, which must be
	// reported in-stream rather than as an HTTP error.
	w := sseGet(t, s, "/workloads/web/logs")
	if w.Code != http.StatusOK {
		t.Fatalf("logs: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "cannot start journalctl") {
		t.Errorf("missing in-stream start error:\n%s", w.Body.String())
	}

	if w := sseGet(t, s, "/workloads/ghost/logs"); w.Code != http.StatusNotFound {
		t.Errorf("unknown workload: %d", w.Code)
	}
	if w := sseGet(t, s, "/workloads/NOPE/logs"); w.Code != http.StatusBadRequest {
		t.Errorf("invalid name: %d", w.Code)
	}
}

func TestTerminalHandlers(t *testing.T) {
	s := newTestServer(t)
	addWorkload(t, s, "web", nix.WorkloadTypeContainer, true)
	addWorkload(t, s, "svc", nix.WorkloadTypeHostService, true)

	// The page always renders; Enabled just gates the client connect.
	if w := get(t, s, "/terminal"); w.Code != http.StatusOK {
		t.Fatalf("terminal page: %d", w.Code)
	}

	// Disabled: both socket endpoints refuse.
	if w := get(t, s, "/terminal/ws"); w.Code != http.StatusForbidden {
		t.Errorf("host terminal while disabled: %d", w.Code)
	}
	if w := get(t, s, "/events/workloads/web/terminal"); w.Code != http.StatusForbidden {
		t.Errorf("workload terminal while disabled: %d", w.Code)
	}

	// A type without ShellArgs is rejected before the enable check.
	if w := get(t, s, "/events/workloads/svc/terminal"); w.Code != http.StatusBadRequest {
		t.Errorf("host-service terminal: %d", w.Code)
	}
	if w := get(t, s, "/events/workloads/ghost/terminal"); w.Code != http.StatusNotFound {
		t.Errorf("unknown workload terminal: %d", w.Code)
	}

	// Enabled but not a WebSocket handshake: Accept writes the failure.
	s.cfg.EnableTerminal = true
	if w := get(t, s, "/terminal/ws"); w.Code == http.StatusOK || w.Code == http.StatusForbidden {
		t.Errorf("non-websocket upgrade: %d", w.Code)
	}
}

func TestRenderGuards(t *testing.T) {
	s := newTestServer(t)

	// Unknown page name is a server bug surfaced as 500.
	w := httptest.NewRecorder()
	s.render(w, httptest.NewRequest(http.MethodGet, "/", nil), "nope", "layout", nil)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("unknown page: %d", w.Code)
	}

	// Dev mode reloads assets from ./web relative to cwd, which does not
	// exist under the test dir: the reload failure surfaces as 500.
	s.cfg.Dev = true
	if w := get(t, s, "/"); w.Code != http.StatusInternalServerError {
		t.Errorf("dev reload without ./web: %d", w.Code)
	}
	s.cfg.Dev = false

	// New with Dev set fails outright for the same reason.
	cfg := s.cfg
	cfg.Dev = true
	if _, err := New(cfg, s.store, s.flake, s.jobs, s.pipeline, s.machines); err == nil {
		t.Error("New in dev mode without ./web: want error")
	}
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }
