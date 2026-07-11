package machine

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/elian/nixbox/internal/nix"
)

// recordingRunner returns fixed output and records every command line.
type recordingRunner struct {
	out   string
	err   error
	calls [][]string
}

func (r *recordingRunner) Output(_ context.Context, name string, args ...string) (string, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return r.out, r.err
}

func (r *recordingRunner) Stream(_ context.Context, _ io.Writer, name string, args ...string) (int, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return 0, r.err
}

func container(t *testing.T) nix.WorkloadType {
	t.Helper()
	wt, ok := nix.Lookup(nix.WorkloadTypeContainer)
	if !ok {
		t.Fatal("container type not registered")
	}
	return wt
}

func TestStatus(t *testing.T) {
	r := &recordingRunner{out: "ActiveState=active\nSubState=running\nJunkLine\n"}
	m := &Manager{Runner: r}
	st, err := m.Status(context.Background(), container(t), "web")
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveState != "active" || st.SubState != "running" || !st.Running() {
		t.Errorf("status = %+v", st)
	}
	want := []string{"systemctl", "show", "container@web.service", "--property=ActiveState,SubState"}
	if !reflect.DeepEqual(r.calls[0], want) {
		t.Errorf("command = %v", r.calls[0])
	}

	// Missing properties default to unknown (not running).
	r.out = "irrelevant"
	if st, _ := m.Status(context.Background(), container(t), "web"); st.ActiveState != "unknown" || st.Running() {
		t.Errorf("defaulted status = %+v", st)
	}

	r.err = errors.New("boom")
	if _, err := m.Status(context.Background(), container(t), "web"); err == nil {
		t.Error("runner failure: want error")
	}
}

func TestUsages(t *testing.T) {
	m := &Manager{Runner: &recordingRunner{}}

	// No refs: no systemctl call at all.
	got, err := m.Usages(context.Background(), nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty refs = %v, %v", got, err)
	}
	if len(m.Runner.(*recordingRunner).calls) != 0 {
		t.Error("systemctl invoked for zero refs")
	}

	r := &recordingRunner{out: "Id=container@web.service\nActiveState=active\nMemoryCurrent=1024\nCPUUsageNSec=5\nTasksCurrent=2\n"}
	m = &Manager{Runner: r}
	got, err = m.Usages(context.Background(), []Ref{{Type: container(t), Name: "web"}})
	if err != nil {
		t.Fatal(err)
	}
	if u := got["web"]; !u.Running || u.MemBytes != 1024 || u.CPUNSec != 5 || u.Tasks != 2 {
		t.Errorf("usage = %+v", u)
	}
	// The queried unit name must appear in the single systemctl call.
	if call := strings.Join(r.calls[0], " "); !strings.Contains(call, "container@web.service") {
		t.Errorf("command = %q", call)
	}

	r.err = errors.New("boom")
	if _, err := m.Usages(context.Background(), []Ref{{Type: container(t), Name: "web"}}); err == nil {
		t.Error("runner failure: want error")
	}

	// A block for a unit nobody asked about (or with no Id at all) is
	// dropped rather than keyed under an empty name.
	r = &recordingRunner{out: "Id=stray.service\nActiveState=active\nnot-a-property\n\nActiveState=active\n"}
	m = &Manager{Runner: r}
	got, err = m.Usages(context.Background(), []Ref{{Type: container(t), Name: "web"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("stray units kept: %v", got)
	}
}

func TestLifecycleVerbs(t *testing.T) {
	r := &recordingRunner{}
	m := &Manager{Runner: r}
	ctx := context.Background()
	wt := container(t)

	for _, tt := range []struct {
		verb string
		call func() error
	}{
		{"start", func() error { return m.Start(ctx, wt, "web") }},
		{"stop", func() error { return m.Stop(ctx, wt, "web") }},
		{"restart", func() error { return m.Restart(ctx, wt, "web") }},
	} {
		if err := tt.call(); err != nil {
			t.Fatalf("%s: %v", tt.verb, err)
		}
		want := []string{"systemctl", tt.verb, "container@web.service"}
		if got := r.calls[len(r.calls)-1]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s command = %v", tt.verb, got)
		}
	}

	r.err = errors.New("boom")
	if err := m.Start(ctx, wt, "web"); err == nil || !strings.Contains(err.Error(), "systemctl start") {
		t.Errorf("failed start = %v", err)
	}
}

func TestPowerVerbs(t *testing.T) {
	r := &recordingRunner{}
	m := &Manager{Runner: r}
	ctx := context.Background()

	if err := m.Reboot(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Poweroff(ctx); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"systemctl", "reboot"}, {"systemctl", "poweroff"}}
	if !reflect.DeepEqual(r.calls, want) {
		t.Errorf("commands = %v", r.calls)
	}

	r.err = errors.New("boom")
	if err := m.Reboot(ctx); err == nil || !strings.Contains(err.Error(), "systemctl reboot") {
		t.Errorf("failed reboot = %v", err)
	}
	if err := m.Poweroff(ctx); err == nil || !strings.Contains(err.Error(), "systemctl poweroff") {
		t.Errorf("failed poweroff = %v", err)
	}
}

func TestJournalCommand(t *testing.T) {
	wt := container(t)
	host := JournalCommand(context.Background(), wt, "web", false)
	args := strings.Join(host.Args, " ")
	for _, want := range []string{"journalctl", "--follow", "-u container@web.service"} {
		if !strings.Contains(args, want) {
			t.Errorf("host journal args missing %q: %q", want, args)
		}
	}
	inside := JournalCommand(context.Background(), wt, "web", true)
	if args := strings.Join(inside.Args, " "); !strings.Contains(args, "-M web") {
		t.Errorf("inside journal args = %q", args)
	}
}
