package relay

import "testing"

func TestParseAllowList_Empty(t *testing.T) {
	al, err := ParseAllowList("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !al.Empty() {
		t.Fatal("expected empty allowlist")
	}
}

func TestParseAllowList_InvalidCIDR(t *testing.T) {
	_, err := ParseAllowList("not-a-cidr/99")
	if err == nil {
		t.Fatal("expected error for invalid CIDR")
	}
}

func TestAllowList_Allowed(t *testing.T) {
	cases := []struct {
		name    string
		config  string
		target  string
		allowed bool
	}{
		// Empty allowlist permits everything.
		{"empty allows all", "", "http://anything.example.com/", true},

		// Exact hostname matches.
		{"exact match", "mynas.local", "http://mynas.local/", true},
		{"exact match tcp", "mynas.local", "mynas.local:9090", true},
		{"exact match ping", "mynas.local", "mynas.local", true},
		{"exact mismatch", "mynas.local", "http://other.local/", false},

		// Wildcard subdomain matches.
		{"wildcard match", "*.home.arpa", "http://router.home.arpa/", true},
		{"wildcard match deep", "*.home.arpa", "http://nas.home.arpa:8080/health", true},
		{"wildcard apex no match", "*.home.arpa", "http://home.arpa/", false},
		{"wildcard mismatch", "*.home.arpa", "http://router.other.arpa/", false},

		// CIDR matches IP literals.
		{"cidr match", "192.168.0.0/16", "http://192.168.1.100/", true},
		{"cidr match tcp", "192.168.0.0/16", "192.168.1.100:8080", true},
		{"cidr mismatch", "192.168.0.0/16", "http://10.0.0.1/", false},
		{"cidr no match hostname", "192.168.0.0/16", "http://mynas.local/", false},

		// Multiple patterns.
		{"multi: first matches", "mynas.local,10.0.0.0/8", "http://mynas.local/", true},
		{"multi: second matches", "mynas.local,10.0.0.0/8", "http://10.1.2.3/", true},
		{"multi: none match", "mynas.local,10.0.0.0/8", "http://other.local/", false},

		// Case-insensitive hostname matching.
		{"case insensitive", "MyNas.Local", "http://mynas.local/", true},

		// URL with path and query.
		{"url with path", "mynas.local", "http://mynas.local:8080/api/v1/status?foo=bar", true},

		// Plain IP exact match.
		{"exact ip", "192.168.1.50", "192.168.1.50", true},
		{"exact ip mismatch", "192.168.1.50", "192.168.1.51", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			al, err := ParseAllowList(tc.config)
			if err != nil {
				t.Fatalf("ParseAllowList(%q): %v", tc.config, err)
			}
			if got := al.Allowed(tc.target); got != tc.allowed {
				t.Fatalf("Allowed(%q) = %v, want %v (config: %q)", tc.target, got, tc.allowed, tc.config)
			}
		})
	}
}

func TestExtractTargetHost(t *testing.T) {
	cases := []struct {
		target string
		want   string
	}{
		{"http://mynas.local/", "mynas.local"},
		{"https://mynas.local:8443/path", "mynas.local"},
		{"http://192.168.1.1:8080/", "192.168.1.1"},
		{"mynas.local:9090", "mynas.local"},
		{"192.168.1.1:9090", "192.168.1.1"},
		{"mynas.local", "mynas.local"},
		{"192.168.1.1", "192.168.1.1"},
	}
	for _, tc := range cases {
		t.Run(tc.target, func(t *testing.T) {
			if got := extractTargetHost(tc.target); got != tc.want {
				t.Fatalf("extractTargetHost(%q) = %q, want %q", tc.target, got, tc.want)
			}
		})
	}
}
