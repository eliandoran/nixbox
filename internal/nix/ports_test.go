package nix

import (
	"reflect"
	"testing"
)

func TestParseHostPorts(t *testing.T) {
	tests := []struct {
		name   string
		ports  []string
		protos []string
		want   []HostPort
		Err    bool
	}{
		{"empty", nil, nil, nil, false},
		{"blank rows skipped", []string{"", " ", "8080"}, []string{"tcp", "tcp", "tcp"},
			[]HostPort{{8080, "tcp"}}, false},
		{"proto defaults to tcp", []string{"80"}, nil, []HostPort{{80, "tcp"}}, false},
		{"blank proto defaults", []string{"80"}, []string{" "}, []HostPort{{80, "tcp"}}, false},
		{"duplicates collapse", []string{"80", "80"}, []string{"tcp", "tcp"},
			[]HostPort{{80, "tcp"}}, false},
		{"sorted by proto then port", []string{"53", "80", "22"}, []string{"udp", "tcp", "tcp"},
			[]HostPort{{22, "tcp"}, {80, "tcp"}, {53, "udp"}}, false},
		{"not a number", []string{"http"}, []string{"tcp"}, nil, true},
		{"port zero", []string{"0"}, []string{"tcp"}, nil, true},
		{"port too large", []string{"65536"}, []string{"tcp"}, nil, true},
		{"bad proto", []string{"80"}, []string{"icmp"}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseHostPorts(tt.ports, tt.protos)
			if (err != nil) != tt.Err {
				t.Fatalf("err = %v, want error %v", err, tt.Err)
			}
			if !tt.Err && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFormatAndDecodeHostPorts(t *testing.T) {
	ports := []HostPort{{22, "tcp"}, {8080, "tcp"}, {53, "udp"}}
	s := FormatHostPorts(ports)
	if s != "22/tcp 8080/tcp 53/udp" {
		t.Errorf("FormatHostPorts = %q", s)
	}
	if FormatHostPorts(nil) != "" {
		t.Error("empty slice should format to empty string")
	}
	if got := DecodeHostPorts(s); !reflect.DeepEqual(got, ports) {
		t.Errorf("round trip = %v, want %v", got, ports)
	}

	// Malformed tokens are skipped, never fatal: a hand-edited row must not
	// break a render or rebuild.
	got := DecodeHostPorts("80/tcp junk 0/tcp 99999/udp 53/icmp noslash 443/tcp")
	want := []HostPort{{80, "tcp"}, {443, "tcp"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("lenient decode = %v, want %v", got, want)
	}
	if got := DecodeHostPorts(""); got != nil {
		t.Errorf("empty decode = %v", got)
	}
}
