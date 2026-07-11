package nix

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestRegistryFuncs pins the machine-facing derivations of each registered
// type: unit names, journal selectors, data dirs, and shell argvs.
func TestRegistryFuncs(t *testing.T) {
	container, _ := Lookup(WorkloadTypeContainer)
	oci, _ := Lookup(WorkloadTypeOCI)
	host, _ := Lookup(WorkloadTypeHostService)

	if got := container.UnitName("web"); got != "container@web.service" {
		t.Errorf("container unit = %q", got)
	}
	if got := container.JournalArgs("web", true); !reflect.DeepEqual(got, []string{"-M", "web"}) {
		t.Errorf("container inside journal = %v", got)
	}
	if got := container.JournalArgs("web", false); !reflect.DeepEqual(got, []string{"-u", "container@web.service"}) {
		t.Errorf("container journal = %v", got)
	}
	if got := container.DataDir("web"); got != "/var/lib/nixos-containers/web" {
		t.Errorf("container data dir = %q", got)
	}
	if got := container.ShellArgs("web"); !reflect.DeepEqual(got, []string{"machinectl", "shell", "web"}) {
		t.Errorf("container shell = %v", got)
	}

	if got := oci.UnitName("web"); got != "podman-web.service" {
		t.Errorf("oci unit = %q", got)
	}
	if got := oci.JournalArgs("web", true); !reflect.DeepEqual(got, []string{"-u", "podman-web.service"}) {
		t.Errorf("oci journal (inside ignored) = %v", got)
	}
	if got := oci.DataDir("web"); got != "" {
		t.Errorf("oci data dir = %q, want none", got)
	}
	if got := oci.ShellArgs("web"); !reflect.DeepEqual(got, []string{"podman", "exec", "-it", "web", "/bin/sh"}) {
		t.Errorf("oci shell = %v", got)
	}

	if got := host.UnitName("jellyfin"); got != "jellyfin.service" {
		t.Errorf("host-service unit = %q", got)
	}
	if got := host.JournalArgs("jellyfin", true); !reflect.DeepEqual(got, []string{"-u", "jellyfin.service"}) {
		t.Errorf("host-service journal = %v", got)
	}
	if got := host.DataDir("jellyfin"); got != "" {
		t.Errorf("host-service data dir = %q, want none", got)
	}
	if host.ShellArgs != nil {
		t.Error("host-service must not advertise a shell")
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("duplicate Register did not panic")
		}
	}()
	Register(WorkloadType{ID: WorkloadTypeContainer})
}

func TestTemplateByID(t *testing.T) {
	wt, _ := Lookup(WorkloadTypeContainer)
	if tmpl, ok := wt.TemplateByID("blank"); !ok || tmpl.ID != "blank" {
		t.Errorf("blank template: %v %v", tmpl, ok)
	}
	if _, ok := wt.TemplateByID("no-such"); ok {
		t.Error("unknown template reported found")
	}
}

func TestValidateDisplayName(t *testing.T) {
	for _, ok := range []string{"", "My Web Server", "ümlauts & emoji 🚀", strings.Repeat("x", 60)} {
		if err := ValidateDisplayName(ok); err != nil {
			t.Errorf("%q: unexpected error %v", ok, err)
		}
	}
	for _, bad := range []string{strings.Repeat("x", 61), "a\tb", "a\nb", "a\rb", "a\x00b"} {
		if err := ValidateDisplayName(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestNixAttrName(t *testing.T) {
	if got := nixAttrName("web"); got != "web" {
		t.Errorf("bare name quoted: %q", got)
	}
	// A digit-leading name is a valid workload name but not a bare Nix
	// identifier, so it must be quoted in the index.
	if got := nixAttrName("9lives"); got != `"9lives"` {
		t.Errorf("digit-leading name = %q", got)
	}
}

// TestWriteFileAtomicErrors drives the temp-create and rename failure
// legs of the atomic write.
func TestWriteFileAtomicErrors(t *testing.T) {
	if err := writeFileAtomic(filepath.Join(t.TempDir(), "missing", "f"), nil); err == nil {
		t.Error("nonexistent parent dir: want error")
	}
	// Renaming a file over an existing directory fails.
	dir := t.TempDir()
	target := filepath.Join(dir, "occupied")
	if err := os.MkdirAll(filepath.Join(target, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(target, []byte("x")); err == nil {
		t.Error("rename over non-empty directory: want error")
	}
}

// TestStateFlakePathGuards covers the name validation and error legs of the
// workload file operations and Init.
func TestStateFlakePathGuards(t *testing.T) {
	f := &StateFlake{Dir: t.TempDir()}
	if err := f.Init(); err != nil {
		t.Fatal(err)
	}
	for _, call := range []struct {
		name string
		err  error
	}{
		{"WriteWorkload", f.WriteWorkload("BAD NAME", "{ }")},
		{"RemoveWorkload", f.RemoveWorkload("BAD NAME")},
	} {
		if call.err == nil {
			t.Errorf("%s with invalid name: want error", call.name)
		}
	}
	if _, err := f.ReadWorkload("BAD NAME"); err == nil {
		t.Error("ReadWorkload with invalid name: want error")
	}
	if _, err := f.ReadWorkload("absent"); err == nil {
		t.Error("ReadWorkload of missing workload: want error")
	}

	// A read-only workloads dir fails the write's MkdirAll.
	if err := os.Chmod(filepath.Join(f.Dir, "workloads"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(f.Dir, "workloads"), 0o755) })
	if err := f.WriteWorkload("web", "{ }"); err == nil {
		t.Error("WriteWorkload into read-only dir: want error")
	}

	// Init against a path occupied by a file cannot mkdir.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (&StateFlake{Dir: blocker}).Init(); err == nil {
		t.Error("Init over a file: want error")
	}
}

// TestInitWriteErrors walks Init's write failures one seed file at a time:
// the static modules, then flake.nix, flake.lock, and index.nix (each of
// which only writes when absent).
func TestInitWriteErrors(t *testing.T) {
	restore := func(f *StateFlake) {
		os.Chmod(filepath.Join(f.Dir, "modules"), 0o755)
		os.Chmod(f.Dir, 0o755)
	}

	// Static module write fails in a read-only modules dir.
	f := &StateFlake{Dir: t.TempDir()}
	if err := f.Init(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(f.Dir, "modules"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restore(f) })
	if err := f.Init(); err == nil {
		t.Error("Init with read-only modules dir: want error")
	}
	restore(f)

	// Each root-level seed write fails in a read-only root; pre-seeding
	// the earlier files advances Init to the next leg.
	for _, present := range [][]string{
		nil,                         // flake.nix write fails
		{"flake.nix"},               // flake.lock write fails
		{"flake.nix", "flake.lock"}, // index.nix write fails
	} {
		f := &StateFlake{Dir: t.TempDir()}
		for _, d := range []string{"modules", "workloads", "secrets"} {
			if err := os.MkdirAll(filepath.Join(f.Dir, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range present {
			if err := os.WriteFile(filepath.Join(f.Dir, name), []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		// Static modules land in modules/ (writable); only the root is
		// locked, so Init reaches the root-level seed writes.
		if err := os.Chmod(f.Dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { restore(f) })
		if err := f.Init(); err == nil {
			t.Errorf("Init with read-only root (present: %v): want error", present)
		}
		restore(f)
	}
}

func TestValidateContainerNamePassthrough(t *testing.T) {
	// The shared path-safety rule runs first...
	if err := validateContainerName("BAD NAME"); err == nil {
		t.Error("invalid charset: want error")
	}
	// ...then the nspawn 11-char cap.
	if err := validateContainerName("twelve-chars"); err == nil {
		t.Error("12-char name: want error")
	}
	if err := validateContainerName("elevenchars"); err != nil {
		t.Errorf("11-char name: %v", err)
	}
}
