package server

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"golang.org/x/crypto/ssh"

	"github.com/elian/nixbox/internal/config"
	"github.com/elian/nixbox/internal/jobs"
	"github.com/elian/nixbox/internal/machine"
	"github.com/elian/nixbox/internal/nix"
	"github.com/elian/nixbox/internal/run"
	"github.com/elian/nixbox/internal/store"
)

// newTestServer wires a full Server against a temp state dir, a dry-run
// runner, and a generated age recipient — the same shape as serve()
// in main, minus the listener.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()

	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "key.pub")
	if err := os.WriteFile(keyPath, ssh.MarshalAuthorizedKey(sshPub), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		Listen:       "127.0.0.1:0",
		StateDir:     dir,
		HostFlake:    dir,
		HostAttr:     "test",
		AgeRecipient: keyPath,
		DryRun:       true,
		Lang:         "en",
	}
	flake := &nix.StateFlake{Dir: cfg.StateFlakeDir()}
	if err := flake.Init(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	jm, err := jobs.NewManager(st, cfg.LogsDir())
	if err != nil {
		t.Fatal(err)
	}
	pl := &nix.Pipeline{
		Runner: run.DryRun{}, HostFlake: cfg.HostFlake, HostAttr: cfg.HostAttr,
		StateInputName: "nixbox-state", DryRun: true,
	}
	srv, err := New(cfg, st, flake, jm, pl, &machine.Manager{Runner: run.DryRun{}})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// addWorkload creates a workload row + file directly, bypassing the
// create form (not under test here).
func addWorkload(t *testing.T, s *Server, name, typ string, enabled bool) *store.Workload {
	t.Helper()
	if err := s.flake.WriteWorkload(name, "{ }\n"); err != nil {
		t.Fatal(err)
	}
	wl, err := s.store.CreateWorkload(name, "", typ, "{ }\n", "")
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		if err := s.store.SetWorkloadEnabled(wl.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	return wl
}

func post(t *testing.T, s *Server, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

func get(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// wantRedirect asserts a 303 and returns the decoded flash/error query
// values of the Location.
func wantRedirect(t *testing.T, w *httptest.ResponseRecorder) (flash, errMsg string) {
	t.Helper()
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body: %s)", w.Code, w.Body.String())
	}
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return loc.Query().Get("flash"), loc.Query().Get("error")
}

func TestSecretsPageAndCreate(t *testing.T) {
	s := newTestServer(t)
	web := addWorkload(t, s, "web", nix.WorkloadTypeContainer, true)
	addWorkload(t, s, "jelly", nix.WorkloadTypeHostService, false)

	// Empty list renders.
	if w := get(t, s, "/secrets"); w.Code != http.StatusOK {
		t.Fatalf("list: %d", w.Code)
	}

	// Create with explicit identity and a mount.
	w := post(t, s, "/secrets", url.Values{
		"name": {"db-pass"}, "value": {"hunter2\r\nline2"},
		"owner": {"nginx"}, "group": {"nginx"}, "mode": {"0440"},
		"mount": {"1"},
	})
	if flash, errMsg := wantRedirect(t, w); flash == "" || errMsg != "" {
		t.Fatalf("create: flash=%q err=%q", flash, errMsg)
	}
	sec, err := s.store.SecretByName("db-pass")
	if err != nil {
		t.Fatal(err)
	}
	if sec.Owner != "nginx" || sec.Mode != "0440" || !sec.MountedInto(web.ID) {
		t.Errorf("created secret: %+v", sec)
	}
	ct, err := os.ReadFile(filepath.Join(s.cfg.StateFlakeDir(), "secrets", "db-pass.age"))
	if err != nil || len(ct) == 0 {
		t.Fatalf("ciphertext missing: %v", err)
	}
	if strings.Contains(string(ct), "hunter2") {
		t.Error("plaintext leaked into ciphertext file")
	}

	// Defaults kick in when identity fields are empty.
	if flash, _ := wantRedirect(t, post(t, s, "/secrets", url.Values{
		"name": {"plain"}, "value": {"v"},
	})); flash == "" {
		t.Fatal("default-identity create failed")
	}
	if sec, _ := s.store.SecretByName("plain"); sec.Owner != "root" || sec.Group != "root" || sec.Mode != "0400" {
		t.Errorf("defaults: %+v", sec)
	}

	// The list shows both, with the workload as a mount target.
	body := get(t, s, "/secrets").Body.String()
	for _, want := range []string{"db-pass", "plain", "web"} {
		if !strings.Contains(body, want) {
			t.Errorf("list missing %q", want)
		}
	}
	// Host services are not offered as targets (jelly is workload #2;
	// its name still shows in the sidebar, so check the checkbox).
	if !strings.Contains(body, `name="mount" value="1"`) {
		t.Error("container missing from mount targets")
	}
	if strings.Contains(body, `name="mount" value="2"`) {
		t.Error("host-service offered as mount target")
	}

	// Validation failures redirect with an error.
	for name, form := range map[string]url.Values{
		"bad name":      {"name": {"NOPE"}, "value": {"v"}},
		"missing value": {"name": {"ok"}, "value": {""}},
		"duplicate":     {"name": {"db-pass"}, "value": {"v"}},
		"bad owner":     {"name": {"ok"}, "value": {"v"}, "owner": {"Bad User"}},
		"bad mode":      {"name": {"ok"}, "value": {"v"}, "mode": {"999"}},
		"junk mount":    {"name": {"ok"}, "value": {"v"}, "mount": {"zzz"}},
		"ghost mount":   {"name": {"ok"}, "value": {"v"}, "mount": {"9999"}},
		"host mount":    {"name": {"ok"}, "value": {"v"}, "mount": {"2"}}, // jelly: unsupported type
	} {
		if _, errMsg := wantRedirect(t, post(t, s, "/secrets", form)); errMsg == "" {
			t.Errorf("%s: expected error redirect", name)
		}
	}
}

func TestSecretSaveAndDelete(t *testing.T) {
	s := newTestServer(t)
	addWorkload(t, s, "web", nix.WorkloadTypeContainer, true)
	if _, errMsg := wantRedirect(t, post(t, s, "/secrets", url.Values{
		"name": {"tok"}, "value": {"v1"},
	})); errMsg != "" {
		t.Fatal(errMsg)
	}
	ctPath := filepath.Join(s.cfg.StateFlakeDir(), "secrets", "tok.age")
	ct1, _ := os.ReadFile(ctPath)

	// Metadata-only save keeps the ciphertext, changes the row.
	if _, errMsg := wantRedirect(t, post(t, s, "/secrets/tok/save", url.Values{
		"owner": {"nginx"}, "group": {"nginx"}, "mode": {"0440"}, "mount": {"1"},
	})); errMsg != "" {
		t.Fatal(errMsg)
	}
	ct2, _ := os.ReadFile(ctPath)
	if string(ct1) != string(ct2) {
		t.Error("metadata-only save rewrote ciphertext")
	}
	sec, _ := s.store.SecretByName("tok")
	if sec.Owner != "nginx" || len(sec.WorkloadIDs) != 1 {
		t.Errorf("after save: %+v", sec)
	}

	// A new value re-encrypts.
	if _, errMsg := wantRedirect(t, post(t, s, "/secrets/tok/save", url.Values{"value": {"v2"}})); errMsg != "" {
		t.Fatal(errMsg)
	}
	ct3, _ := os.ReadFile(ctPath)
	if string(ct2) == string(ct3) {
		t.Error("value save did not rewrite ciphertext")
	}

	// Bad form redirects with error; unknown/invalid names 404/400.
	if _, errMsg := wantRedirect(t, post(t, s, "/secrets/tok/save", url.Values{"mode": {"bad"}})); errMsg == "" {
		t.Error("expected form error")
	}
	if w := post(t, s, "/secrets/ghost/save", nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown secret: %d", w.Code)
	}
	if w := post(t, s, "/secrets/NOPE/save", nil); w.Code != http.StatusBadRequest {
		t.Errorf("invalid name: %d", w.Code)
	}

	// Delete drops the row but keeps the ciphertext until the next apply.
	if flash, _ := wantRedirect(t, post(t, s, "/secrets/tok/delete", nil)); flash == "" {
		t.Error("delete flash missing")
	}
	if _, err := s.store.SecretByName("tok"); err != store.ErrNotFound {
		t.Errorf("row survived delete: %v", err)
	}
	if _, err := os.Stat(ctPath); err != nil {
		t.Errorf("ciphertext pruned before apply: %v", err)
	}
	if w := post(t, s, "/secrets/tok/delete", nil); w.Code != http.StatusNotFound {
		t.Errorf("double delete: %d", w.Code)
	}
}

func TestSecretApplyPipeline(t *testing.T) {
	s := newTestServer(t)
	web := addWorkload(t, s, "web", nix.WorkloadTypeContainer, true)
	if _, errMsg := wantRedirect(t, post(t, s, "/secrets", url.Values{
		"name": {"tok"}, "value": {"v"}, "mount": {"1"},
	})); errMsg != "" {
		t.Fatal(errMsg)
	}
	_ = web

	// Busy: hold the single job slot, apply must 409.
	release := make(chan struct{})
	if _, err := s.jobs.Start(store.JobApply, nil, func(context.Context, io.Writer) (jobs.Result, error) {
		<-release
		return jobs.Result{ExitCode: 0}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if w := post(t, s, "/secrets/apply", nil); w.Code != http.StatusConflict {
		t.Errorf("busy apply: %d", w.Code)
	}
	close(release)
	waitIdle(t, s)

	// Mounts into a disabled workload and one of an unregistered type
	// must be skipped by the index (delivery stays dormant).
	dis := addWorkload(t, s, "dis", nix.WorkloadTypeContainer, false)
	ghost := addWorkload(t, s, "ghost", "microvm", true)
	sec, err := s.store.SecretByName("tok")
	if err != nil {
		t.Fatal(err)
	}
	for _, wl := range []*store.Workload{dis, ghost} {
		if err := s.store.AddSecretMount(sec.ID, wl.ID); err != nil {
			t.Fatal(err)
		}
	}

	// A real (dry-run) apply regenerates index+flake with the secret,
	// marks it applied, and leaves the ciphertext in place.
	if w := post(t, s, "/secrets/apply", nil); w.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", w.Code, w.Body.String())
	}
	waitIdle(t, s)
	index, _ := os.ReadFile(filepath.Join(s.cfg.StateFlakeDir(), "index.nix"))
	if !strings.Contains(string(index), "tok") || !strings.Contains(string(index), `containers = [ "web" ]`) {
		t.Errorf("index missing secret/mount:\n%s", index)
	}
	for _, absent := range []string{"dis", "ghost"} {
		if strings.Contains(string(index), `"`+absent+`"`) {
			t.Errorf("dormant mount %q leaked into index:\n%s", absent, index)
		}
	}
	flake, _ := os.ReadFile(filepath.Join(s.cfg.StateFlakeDir(), "flake.nix"))
	if !strings.Contains(string(flake), "agenix") {
		t.Errorf("flake missing agenix input:\n%s", flake)
	}
	sec, _ = s.store.SecretByName("tok")
	if sec.Status() != "applied" {
		t.Errorf("status after apply: %s", sec.Status())
	}

	// Build mode also passes (validate job kind).
	if w := post(t, s, "/secrets/apply", url.Values{"mode": {"build"}}); w.Code != http.StatusOK {
		t.Errorf("build apply: %d", w.Code)
	}
	waitIdle(t, s)

	// Deleting and applying prunes the orphan and drops agenix again.
	if _, errMsg := wantRedirect(t, post(t, s, "/secrets/tok/delete", nil)); errMsg != "" {
		t.Fatal(errMsg)
	}
	if w := post(t, s, "/secrets/apply", nil); w.Code != http.StatusOK {
		t.Fatalf("apply after delete: %d", w.Code)
	}
	waitIdle(t, s)
	if _, err := os.Stat(filepath.Join(s.cfg.StateFlakeDir(), "secrets", "tok.age")); !os.IsNotExist(err) {
		t.Errorf("orphan not pruned: %v", err)
	}
	flake, _ = os.ReadFile(filepath.Join(s.cfg.StateFlakeDir(), "flake.nix"))
	if strings.Contains(string(flake), "agenix") {
		t.Errorf("agenix survived last-secret delete:\n%s", flake)
	}
}

func TestWorkloadSecretsPane(t *testing.T) {
	s := newTestServer(t)
	web := addWorkload(t, s, "web", nix.WorkloadTypeContainer, true)
	addWorkload(t, s, "jelly", nix.WorkloadTypeHostService, false)

	// Pane create: encrypted, pre-mounted to this workload.
	if flash, errMsg := wantRedirect(t, post(t, s, "/workloads/web/secrets", url.Values{
		"name": {"tok"}, "value": {"v"},
	})); flash == "" || errMsg != "" {
		t.Fatalf("pane create: %q %q", flash, errMsg)
	}
	sec, err := s.store.SecretByName("tok")
	if err != nil {
		t.Fatal(err)
	}
	if !sec.MountedInto(web.ID) || sec.Owner != "root" {
		t.Errorf("pane-created secret: %+v", sec)
	}

	// Detail page shows the table row and, once another secret exists,
	// the attach dropdown.
	if _, errMsg := wantRedirect(t, post(t, s, "/secrets", url.Values{"name": {"other"}, "value": {"v"}})); errMsg != "" {
		t.Fatal(errMsg)
	}
	body := get(t, s, "/workloads/web").Body.String()
	for _, want := range []string{"/run/agenix/tok", `option value="other"`} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page missing %q", want)
		}
	}
	// Host-service page renders no pane.
	if b := get(t, s, "/workloads/jelly").Body.String(); strings.Contains(b, "secrets/attach") {
		t.Error("pane rendered for host-service")
	}

	// Attach and detach round-trip.
	if flash, _ := wantRedirect(t, post(t, s, "/workloads/web/secrets/attach", url.Values{"secret": {"other"}})); flash == "" {
		t.Error("attach flash missing")
	}
	if sec, _ := s.store.SecretByName("other"); !sec.MountedInto(web.ID) {
		t.Error("attach did not mount")
	}
	if flash, _ := wantRedirect(t, post(t, s, "/workloads/web/secrets/detach", url.Values{"secret": {"other"}})); flash == "" {
		t.Error("detach flash missing")
	}
	if sec, _ := s.store.SecretByName("other"); sec.MountedInto(web.ID) {
		t.Error("detach did not unmount")
	}

	// Guards and validation.
	if w := post(t, s, "/workloads/jelly/secrets", url.Values{"name": {"x"}, "value": {"v"}}); w.Code != http.StatusNotFound {
		t.Errorf("host-service create guard: %d", w.Code)
	}
	if w := post(t, s, "/workloads/jelly/secrets/attach", url.Values{"secret": {"tok"}}); w.Code != http.StatusNotFound {
		t.Errorf("host-service attach guard: %d", w.Code)
	}
	if w := post(t, s, "/workloads/ghost/secrets", url.Values{"name": {"x"}, "value": {"v"}}); w.Code != http.StatusNotFound {
		t.Errorf("unknown workload: %d", w.Code)
	}
	if w := post(t, s, "/workloads/NOPE/secrets", nil); w.Code != http.StatusBadRequest {
		t.Errorf("invalid workload name: %d", w.Code)
	}
	for name, form := range map[string]url.Values{
		"bad name":      {"name": {"NOPE"}, "value": {"v"}},
		"missing value": {"name": {"ok"}, "value": {""}},
		"duplicate":     {"name": {"tok"}, "value": {"v"}},
		"bad owner":     {"name": {"ok"}, "value": {"v"}, "owner": {"Bad"}},
	} {
		if _, errMsg := wantRedirect(t, post(t, s, "/workloads/web/secrets", form)); errMsg == "" {
			t.Errorf("%s: expected error redirect", name)
		}
	}
	if w := post(t, s, "/workloads/web/secrets/attach", url.Values{"secret": {"ghost"}}); w.Code != http.StatusNotFound {
		t.Errorf("attach unknown secret: %d", w.Code)
	}
	if _, errMsg := wantRedirect(t, post(t, s, "/workloads/web/secrets/attach", url.Values{"secret": {"NOPE"}})); errMsg == "" {
		t.Error("attach invalid name: expected error redirect")
	}
}

// failRecipient loads fine but cannot wrap a file key.
type failRecipient struct{}

func (failRecipient) Wrap([]byte) ([]*age.Stanza, error) {
	return nil, errors.New("wrap failed")
}

// TestSecretEncryptFailure: an unusable recipient key surfaces as an
// inline error on create and save, through both entry points.
func TestSecretEncryptFailure(t *testing.T) {
	s := newTestServer(t)
	addWorkload(t, s, "web", nix.WorkloadTypeContainer, true)
	if _, errMsg := wantRedirect(t, post(t, s, "/secrets", url.Values{"name": {"tok"}, "value": {"v"}})); errMsg != "" {
		t.Fatal(errMsg)
	}
	s.cfg.AgeRecipient = filepath.Join(t.TempDir(), "missing.pub")

	if _, errMsg := wantRedirect(t, post(t, s, "/secrets", url.Values{"name": {"new"}, "value": {"v"}})); errMsg == "" {
		t.Error("tab create: expected recipient error")
	}

	// A recipient that loads but cannot encrypt: LoadRecipient validates
	// keys at parse time, so this leg is only reachable through the
	// injection seam.
	s.loadRecipient = func(string) (age.Recipient, error) { return failRecipient{}, nil }
	if _, errMsg := wantRedirect(t, post(t, s, "/secrets", url.Values{"name": {"new"}, "value": {"v"}})); errMsg == "" {
		t.Error("tab create: expected encrypt error from unusable recipient")
	}
	s.loadRecipient = func(string) (age.Recipient, error) { return nil, errors.New("no recipient") }
	if _, errMsg := wantRedirect(t, post(t, s, "/secrets/tok/save", url.Values{"value": {"v2"}})); errMsg == "" {
		t.Error("save: expected recipient error")
	}
	if _, errMsg := wantRedirect(t, post(t, s, "/workloads/web/secrets", url.Values{"name": {"new2"}, "value": {"v"}})); errMsg == "" {
		t.Error("pane create: expected recipient error")
	}
}

// TestSecretStoreFailures faults the database to reach the 500 paths.
func TestSecretStoreFailures(t *testing.T) {
	// Workloads() failing after Secrets() succeeds (list page).
	s := newTestServer(t)
	if _, err := s.store.SecretByName("x"); err != store.ErrNotFound {
		t.Fatal(err)
	}
	dropTable(t, s, "workloads")
	if w := get(t, s, "/secrets"); w.Code != http.StatusInternalServerError {
		t.Errorf("list with dropped workloads: %d", w.Code)
	}

	// Secrets() failing (list page + lookups).
	s = newTestServer(t)
	addWorkload(t, s, "web", nix.WorkloadTypeContainer, true)
	if _, errMsg := wantRedirect(t, post(t, s, "/secrets", url.Values{"name": {"tok"}, "value": {"v"}})); errMsg != "" {
		t.Fatal(errMsg)
	}
	dropTable(t, s, "secret_mounts") // faults every mounts lookup
	if w := get(t, s, "/secrets"); w.Code != http.StatusInternalServerError {
		t.Errorf("list with dropped mounts: %d", w.Code)
	}
	// lookupSecret's non-NotFound branch (save/delete/attach).
	if w := post(t, s, "/secrets/tok/save", url.Values{"mode": {"0400"}}); w.Code != http.StatusInternalServerError {
		t.Errorf("save with dropped mounts: %d", w.Code)
	}
	if w := post(t, s, "/secrets/tok/delete", nil); w.Code != http.StatusInternalServerError {
		t.Errorf("delete with dropped mounts: %d", w.Code)
	}
	if w := post(t, s, "/workloads/web/secrets/attach", url.Values{"secret": {"tok"}}); w.Code != http.StatusInternalServerError {
		t.Errorf("attach with dropped mounts: %d", w.Code)
	}
	// Creation paths: SecretByName errors non-NotFound.
	if w := post(t, s, "/secrets", url.Values{"name": {"new"}, "value": {"v"}}); w.Code != http.StatusInternalServerError {
		t.Errorf("tab create with dropped mounts: %d", w.Code)
	}
	if w := post(t, s, "/workloads/web/secrets", url.Values{"name": {"new"}, "value": {"v"}}); w.Code != http.StatusInternalServerError {
		t.Errorf("pane create with dropped mounts: %d", w.Code)
	}
	// Detail page: workloadDetailData's Secrets() error.
	if w := get(t, s, "/workloads/web"); w.Code != http.StatusInternalServerError {
		t.Errorf("detail with dropped mounts: %d", w.Code)
	}
	// Apply: regenerateIndex's secrets query fails before the job starts.
	if w := post(t, s, "/secrets/apply", nil); w.Code != http.StatusInternalServerError {
		t.Errorf("apply with dropped mounts: %d", w.Code)
	}
	// Creation's duplicate check erroring outright (secrets table gone),
	// through both entry points.
	s = newTestServer(t)
	addWorkload(t, s, "web", nix.WorkloadTypeContainer, true)
	dropTable(t, s, "secrets")
	if w := post(t, s, "/secrets", url.Values{"name": {"x"}, "value": {"v"}}); w.Code != http.StatusInternalServerError {
		t.Errorf("create with dropped secrets: %d", w.Code)
	}
	if w := post(t, s, "/workloads/web/secrets", url.Values{"name": {"x"}, "value": {"v"}}); w.Code != http.StatusInternalServerError {
		t.Errorf("pane create with dropped secrets: %d", w.Code)
	}

	// Write-denied tables: lookups succeed, the mutation itself fails.
	s = newTestServer(t)
	addWorkload(t, s, "web", nix.WorkloadTypeContainer, true)
	if _, errMsg := wantRedirect(t, post(t, s, "/secrets", url.Values{"name": {"tok"}, "value": {"v"}})); errMsg != "" {
		t.Fatal(errMsg)
	}
	denyWrites(t, s, "secrets")
	if w := post(t, s, "/secrets/tok/save", url.Values{"mode": {"0400"}}); w.Code != http.StatusInternalServerError {
		t.Errorf("save with denied secrets writes: %d", w.Code)
	}
	if w := post(t, s, "/secrets/tok/delete", nil); w.Code != http.StatusInternalServerError {
		t.Errorf("delete with denied secrets writes: %d", w.Code)
	}
	// Pane create: the insert into secrets fails after encryption.
	if w := post(t, s, "/workloads/web/secrets", url.Values{"name": {"new"}, "value": {"v"}}); w.Code != http.StatusInternalServerError {
		t.Errorf("pane create with denied secrets writes: %d", w.Code)
	}

	// Mount writes denied: attach/detach lookups pass, the flip fails.
	s = newTestServer(t)
	addWorkload(t, s, "web", nix.WorkloadTypeContainer, true)
	if _, errMsg := wantRedirect(t, post(t, s, "/secrets", url.Values{"name": {"tok"}, "value": {"v"}})); errMsg != "" {
		t.Fatal(errMsg)
	}
	if _, errMsg := wantRedirect(t, post(t, s, "/secrets", url.Values{"name": {"tok2"}, "value": {"v"}, "mount": {"1"}})); errMsg != "" {
		t.Fatal(errMsg)
	}
	denyWrites(t, s, "secret_mounts")
	if w := post(t, s, "/workloads/web/secrets/attach", url.Values{"secret": {"tok"}}); w.Code != http.StatusInternalServerError {
		t.Errorf("attach with denied mount writes: %d", w.Code)
	}
	if w := post(t, s, "/workloads/web/secrets/detach", url.Values{"secret": {"tok2"}}); w.Code != http.StatusInternalServerError {
		t.Errorf("detach with denied mount writes: %d", w.Code)
	}
}

// dropTable faults the schema through a second connection to the same
// database file (the store's own handle is unexported here).
func dropTable(t *testing.T, s *Server, table string) {
	t.Helper()
	execSQL(t, s, `DROP TABLE `+table)
}

// denyWrites installs RAISE triggers so reads on the table keep working
// while any insert/update/delete fails — the fault that reaches the
// error paths after a successful lookup.
func denyWrites(t *testing.T, s *Server, table string) {
	t.Helper()
	for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
		execSQL(t, s, `CREATE TRIGGER deny_`+op+`_`+table+` BEFORE `+op+` ON `+table+
			` BEGIN SELECT RAISE(ABORT, 'denied'); END`)
	}
}

func execSQL(t *testing.T, s *Server, stmt string) {
	t.Helper()
	db, err := sql.Open("sqlite", s.cfg.DBPath()+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(stmt); err != nil {
		t.Fatal(err)
	}
}

// waitIdle blocks until the job manager finishes the running job.
func waitIdle(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for s.jobs.Busy() {
		if time.Now().After(deadline) {
			t.Fatal("job never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
