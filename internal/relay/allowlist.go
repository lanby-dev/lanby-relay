package relay

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// AllowList restricts which probe targets the relay may contact.
// An empty AllowList permits all targets.
type AllowList struct {
	entries []allowEntry
}

type allowEntry struct {
	kind    string     // "exact", "wildcard", "cidr"
	pattern string     // for exact/wildcard
	network *net.IPNet // for cidr
}

// ParseAllowList parses a comma-separated list of allowed probe host patterns.
// Supported forms:
//   - exact hostname or IP: "mynas.local", "192.168.1.1"
//   - wildcard subdomain: "*.home.arpa" (matches foo.home.arpa, not home.arpa itself)
//   - CIDR block: "192.168.0.0/16" (matches IP-literal targets only, not hostnames)
func ParseAllowList(raw string) (AllowList, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return AllowList{}, nil
	}
	var entries []allowEntry
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.Contains(p, "/") {
			_, network, err := net.ParseCIDR(p)
			if err != nil {
				return AllowList{}, fmt.Errorf("invalid CIDR %q in ALLOWED_PROBE_HOSTS: %w", p, err)
			}
			entries = append(entries, allowEntry{kind: "cidr", network: network})
		} else if strings.HasPrefix(p, "*.") {
			entries = append(entries, allowEntry{kind: "wildcard", pattern: strings.ToLower(p[2:])})
		} else {
			entries = append(entries, allowEntry{kind: "exact", pattern: strings.ToLower(p)})
		}
	}
	return AllowList{entries: entries}, nil
}

// Empty reports whether the allowlist is unconfigured (no restriction applies).
func (a AllowList) Empty() bool {
	return len(a.entries) == 0
}

// Allowed reports whether target may be probed. target is the raw check target
// string (e.g. "http://mynas.local:8080/", "192.168.1.1:9090", "myhost.local").
// CIDRs match only IP-literal targets; use hostname patterns for named hosts.
func (a AllowList) Allowed(target string) bool {
	if a.Empty() {
		return true
	}
	host := extractTargetHost(target)
	if host == "" {
		return false
	}
	host = strings.ToLower(host)
	ip := net.ParseIP(host)
	for _, e := range a.entries {
		switch e.kind {
		case "exact":
			if host == e.pattern {
				return true
			}
		case "wildcard":
			if strings.HasSuffix(host, "."+e.pattern) {
				return true
			}
		case "cidr":
			if ip != nil && e.network.Contains(ip) {
				return true
			}
		}
	}
	return false
}

// extractTargetHost pulls the bare hostname or IP from a check target string.
func extractTargetHost(target string) string {
	if strings.Contains(target, "://") {
		u, err := url.Parse(target)
		if err == nil && u.Host != "" {
			host, _, err := net.SplitHostPort(u.Host)
			if err != nil {
				return u.Host
			}
			return host
		}
	}
	host, _, err := net.SplitHostPort(target)
	if err == nil {
		return host
	}
	return target
}
