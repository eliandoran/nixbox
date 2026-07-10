// Package metrics reads host-wide resource figures straight from the
// kernel (/proc and statfs). Every read here is side-effect free, so —
// like reading /etc/os-release or a journal — it runs for real even in
// dry-run mode rather than going through the command runner.
package metrics

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// HostSample is one instantaneous reading of host resource use. CPU is
// reported as raw jiffie counters (busy and total) because a percentage
// only means anything across two samples; callers diff consecutive
// samples to derive load. Memory and disk figures are in bytes.
type HostSample struct {
	Load1, Load5, Load15 float64
	MemTotal, MemUsed    uint64
	DiskTotal, DiskUsed  uint64
	CPUTotal, CPUBusy    uint64
}

// Sample gathers a host reading. diskPath selects the filesystem to
// report (the Nix store is the one that grows on a NixOS box). A missing
// individual source leaves its fields zero rather than failing the whole
// sample — a partial dashboard beats none.
func Sample(diskPath string) HostSample {
	var s HostSample
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		s.Load1, s.Load5, s.Load15 = parseLoadavg(string(b))
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		s.MemTotal, s.MemUsed = parseMeminfo(string(b))
	}
	if b, err := os.ReadFile("/proc/stat"); err == nil {
		s.CPUTotal, s.CPUBusy = parseCPUStat(string(b))
	}
	s.DiskTotal, s.DiskUsed = diskUsage(diskPath)
	return s
}

func parseLoadavg(s string) (l1, l5, l15 float64) {
	f := strings.Fields(s)
	if len(f) < 3 {
		return
	}
	l1, _ = strconv.ParseFloat(f[0], 64)
	l5, _ = strconv.ParseFloat(f[1], 64)
	l15, _ = strconv.ParseFloat(f[2], 64)
	return
}

// parseMeminfo returns total and used memory in bytes. "Used" follows
// the modern convention of total minus MemAvailable (which accounts for
// reclaimable cache), matching what `free` and most dashboards show.
func parseMeminfo(s string) (total, used uint64) {
	var avail uint64
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		kb, _ := strconv.ParseUint(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "kB")), 10, 64)
		switch k {
		case "MemTotal":
			total = kb * 1024
		case "MemAvailable":
			avail = kb * 1024
		}
	}
	if avail <= total {
		used = total - avail
	}
	return
}

// parseCPUStat sums the aggregate "cpu" line into total and busy jiffies.
// Busy is everything except idle and iowait (both count as the CPU
// having nothing to do).
func parseCPUStat(s string) (total, busy uint64) {
	for _, line := range strings.Split(s, "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		for i, f := range strings.Fields(line)[1:] {
			n, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				continue
			}
			total += n
			// Fields after the "cpu" label: user, nice, system,
			// idle(3), iowait(4), irq, softirq, steal, ...
			if i != 3 && i != 4 {
				busy += n
			}
		}
		return
	}
	return
}

func diskUsage(path string) (total, used uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return
	}
	bs := uint64(st.Bsize)
	total = st.Blocks * bs
	used = (st.Blocks - st.Bfree) * bs
	return
}

// FormatBytes renders a byte count in the largest unit under 1024,
// e.g. 1536 → "1.5 KiB". Kept here so the host card and any future
// consumer format sizes identically.
func FormatBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
