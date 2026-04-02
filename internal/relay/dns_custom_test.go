package relay

import "testing"

func TestNormalizeDNSNameserver(t *testing.T) {
	cases := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"8.8.8.8", "8.8.8.8:53", false},
		{"8.8.8.8:5353", "8.8.8.8:5353", false},
		{"  8.8.8.8  ", "8.8.8.8:53", false},
		{"resolver.example.com", "resolver.example.com:53", false},
		{"resolver.example.com:5353", "resolver.example.com:5353", false},
		{"[::1]:53", "[::1]:53", false},
		// Missing port in host:port form — SplitHostPort succeeds but host/port empty, caught by validation.
		{"8.8.8.8:", "", true},
		// Bare IPv6 — treated as an IP and wrapped with port 53.
		{"2001:db8::1", "[2001:db8::1]:53", false},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := normalizeDNSNameserver(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %q, got %q", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for input %q: %v", tc.input, err)
			}
			if tc.want != "" && got != tc.want {
				t.Fatalf("normalizeDNSNameserver(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
