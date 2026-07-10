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
		0:              "0 B",
		512:            "512 B",
		1536:           "1.5 KiB",
		2 * 1024 * 1024: "2.0 MiB",
	}
	for in, want := range cases {
		if got := FormatBytes(in); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
