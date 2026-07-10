package nix

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// HostPort is a firewall port a workload asks nixbox to open on the host.
// Shared-namespace containers (no privateNetwork) serve on the host's
// network stack, so their ports must be opened in the *host* firewall;
// nixbox aggregates these declarations into the generated module's
// networking.firewall.allowed{TCP,UDP}Ports.
type HostPort struct {
	Port  int
	Proto string // "tcp" or "udp"
}

// ParseHostPorts pairs the parallel port/proto arrays submitted by the
// workload editor's port rows. Rows with a blank port are skipped (an
// empty editor row), and duplicates are collapsed. The result is sorted
// so identical declarations always serialize identically.
//
// HTML forms serialize same-named inputs in document order, so ports[i]
// and protos[i] belong to the same row as long as every row always
// renders both an input and a select (even when the port is blank).
func ParseHostPorts(ports, protos []string) ([]HostPort, error) {
	var out []HostPort
	seen := map[HostPort]bool{}
	for i, ps := range ports {
		ps = strings.TrimSpace(ps)
		if ps == "" {
			continue
		}
		n, err := strconv.Atoi(ps)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("invalid port %q: must be a number 1-65535", ps)
		}
		proto := "tcp"
		if i < len(protos) && strings.TrimSpace(protos[i]) != "" {
			proto = strings.TrimSpace(protos[i])
		}
		if proto != "tcp" && proto != "udp" {
			return nil, fmt.Errorf("invalid protocol %q: must be tcp or udp", proto)
		}
		hp := HostPort{Port: n, Proto: proto}
		if seen[hp] {
			continue
		}
		seen[hp] = true
		out = append(out, hp)
	}
	sortHostPorts(out)
	return out, nil
}

// FormatHostPorts renders ports to the canonical "8080/tcp 8443/udp"
// string stored in the revisions table. The empty slice yields "".
func FormatHostPorts(ps []HostPort) string {
	var b strings.Builder
	for i, p := range ps {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%d/%s", p.Port, p.Proto)
	}
	return b.String()
}

// DecodeHostPorts parses the canonical stored string back into HostPorts.
// Malformed tokens are skipped so a hand-edited row never breaks a render
// or a rebuild.
func DecodeHostPorts(s string) []HostPort {
	var out []HostPort
	for _, tok := range strings.Fields(s) {
		portStr, proto, ok := strings.Cut(tok, "/")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(portStr)
		if err != nil || n < 1 || n > 65535 || (proto != "tcp" && proto != "udp") {
			continue
		}
		out = append(out, HostPort{Port: n, Proto: proto})
	}
	sortHostPorts(out)
	return out
}

func sortHostPorts(ps []HostPort) {
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].Proto != ps[j].Proto {
			return ps[i].Proto < ps[j].Proto
		}
		return ps[i].Port < ps[j].Port
	})
}
