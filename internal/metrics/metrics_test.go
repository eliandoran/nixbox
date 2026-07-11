package metrics

import "testing"

func TestParseLoadavg(t *testing.T) {
	l1, l5, l15 := parseLoadavg("0.52 0.48 0.44 1/512 12345\n")
	if l1 != 0.52 || l5 != 0.48 || l15 != 0.44 {
		t.Fatalf("got %v %v %v", l1, l5, l15)
	}
	if l1, _, _ := parseLoadavg("garbage"); l1 != 0 {
		t.Fatalf("short input should yield zero, got %v", l1)
	}
}

func TestParseMeminfo(t *testing.T) {
	const in = "MemTotal:       16000000 kB\nMemFree:  1000000 kB\nMemAvailable:    4000000 kB\n"
	total, used := parseMeminfo(in)
	if total != 16000000*1024 {
		t.Fatalf("total = %d", total)
	}
	if used != (16000000-4000000)*1024 {
		t.Fatalf("used = %d", used)
	}
}

func TestParseCPUStat(t *testing.T) {
	// user nice system idle iowait irq softirq steal
	const in = "cpu  100 20 30 800 40 5 5 0\ncpu0 ...\n"
	total, busy := parseCPUStat(in)
	if total != 1000 {
		t.Fatalf("total = %d", total)
	}
	// busy excludes idle(800) and iowait(40)
	if busy != 160 {
		t.Fatalf("busy = %d", busy)
	}
}

func TestFormatBytes(t *testing.T) {
	cases := map[uint64]string{
		0:               "0 B",
		512:             "512 B",
		1536:            "1.5 KiB",
		2 * 1024 * 1024: "2.0 MiB",
	}
	for in, want := range cases {
		if got := FormatBytes(in); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestParseCPUStatEdgeCases(t *testing.T) {
	// No aggregate "cpu " line (e.g. truncated read) yields zeros.
	if total, busy := parseCPUStat("cpu0 1 2 3\n"); total != 0 || busy != 0 {
		t.Errorf("no cpu line: %d %d", total, busy)
	}
	// Unparseable fields are skipped, the rest still count.
	total, busy := parseCPUStat("cpu  100 junk 30 800 40\n")
	if total != 970 || busy != 130 {
		t.Errorf("junk field: total=%d busy=%d", total, busy)
	}
}

// TestSample reads the real /proc and statfs — side-effect free by design
// (the package's documented contract), and always present on Linux.
func TestSample(t *testing.T) {
	s := Sample("/")
	if s.MemTotal == 0 || s.MemUsed == 0 {
		t.Errorf("memory not sampled: %+v", s)
	}
	if s.CPUTotal == 0 {
		t.Errorf("cpu not sampled: %+v", s)
	}
	if s.DiskTotal == 0 || s.DiskUsed == 0 || s.DiskUsed > s.DiskTotal {
		t.Errorf("disk not sampled: %+v", s)
	}

	// A missing disk path leaves the disk fields zero instead of failing
	// the sample.
	s = Sample("/no/such/path")
	if s.DiskTotal != 0 || s.DiskUsed != 0 {
		t.Errorf("missing path disk fields: %+v", s)
	}
	if s.MemTotal == 0 {
		t.Error("memory sampling must survive a bad disk path")
	}
}
