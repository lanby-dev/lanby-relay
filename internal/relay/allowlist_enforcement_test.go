package relay

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func mustAllowList(t *testing.T, raw string) AllowList {
	t.Helper()
	a, err := ParseAllowList(raw)
	if err != nil {
		t.Fatalf("parse allowlist %q: %v", raw, err)
	}
	return a
}

// hitCounter starts a server that counts requests. It listens on 127.0.0.1 only,
// so it is reachable as both "127.0.0.1" and "localhost" — letting tests use an
// allowlist of "127.0.0.1" to tell an allowed host from a blocked one.
func hitCounter(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func redirectTo(t *testing.T, location string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, location, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func localhostURL(srv *httptest.Server) string {
	return strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
}

func TestExecuteCheck_HTTP_RedirectToBlockedHostIsNotFollowed(t *testing.T) {
	blocked, blockedHits := hitCounter(t)
	start := redirectTo(t, localhostURL(blocked)) // "localhost" is not in the allowlist

	res := executeCheck(RelayCheckConfig{
		MonitorID:       "m1",
		Type:            "http",
		Target:          start.URL,
		FollowRedirects: true,
		TimeoutSeconds:  3,
	}, mustAllowList(t, "127.0.0.1"))

	if blockedHits.Load() != 0 {
		t.Fatalf("redirect to non-allowlisted host was followed (%d hits)", blockedHits.Load())
	}
	if res.Status == "ok" {
		t.Fatalf("expected non-ok result, got %+v", res)
	}
	if !strings.Contains(res.Error, "ALLOWED_PROBE_HOSTS") {
		t.Fatalf("expected error to mention ALLOWED_PROBE_HOSTS, got %q", res.Error)
	}
}

func TestExecuteCheck_HTTP_RedirectToAllowedHostIsFollowed(t *testing.T) {
	dest, destHits := hitCounter(t)
	start := redirectTo(t, dest.URL)

	res := executeCheck(RelayCheckConfig{
		MonitorID:       "m1",
		Type:            "http",
		Target:          start.URL,
		FollowRedirects: true,
		TimeoutSeconds:  3,
	}, mustAllowList(t, "127.0.0.1"))

	if res.Status != "ok" || destHits.Load() != 1 {
		t.Fatalf("expected redirect followed (status ok, 1 hit), got %+v hits=%d", res, destHits.Load())
	}
}

func TestRunRelayURLTests_TargetOutsideAllowListIsNotProbed(t *testing.T) {
	srv, hits := hitCounter(t)

	results := runRelayURLTests([]RelayURLTest{{ID: "t1", URL: localhostURL(srv)}}, mustAllowList(t, "127.0.0.1"))

	if hits.Load() != 0 {
		t.Fatalf("non-allowlisted URL test was probed (%d hits)", hits.Load())
	}
	if len(results) != 1 || results[0].Reachable || !strings.Contains(results[0].Error, "ALLOWED_PROBE_HOSTS") {
		t.Fatalf("expected unreachable result mentioning ALLOWED_PROBE_HOSTS, got %+v", results)
	}
}

func TestRunRelayURLTests_TargetInsideAllowListIsProbed(t *testing.T) {
	srv, hits := hitCounter(t)

	results := runRelayURLTests([]RelayURLTest{{ID: "t1", URL: srv.URL}}, mustAllowList(t, "127.0.0.1"))

	if hits.Load() != 1 || len(results) != 1 || !results[0].Reachable {
		t.Fatalf("expected allowlisted URL test to be probed, got %+v hits=%d", results, hits.Load())
	}
}

func TestRunRelayURLTests_RedirectToBlockedHostIsNotFollowed(t *testing.T) {
	blocked, blockedHits := hitCounter(t)
	start := redirectTo(t, localhostURL(blocked))

	results := runRelayURLTests([]RelayURLTest{{ID: "t1", URL: start.URL}}, mustAllowList(t, "127.0.0.1"))

	if blockedHits.Load() != 0 {
		t.Fatalf("redirect to non-allowlisted host was followed (%d hits)", blockedHits.Load())
	}
	if len(results) != 1 || results[0].Reachable {
		t.Fatalf("expected unreachable result, got %+v", results)
	}
}

func TestExecuteCheck_DNS_NameserverOutsideAllowListIsNotQueried(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	defer func() { _ = pc.Close() }()
	var packets atomic.Int32
	go func() {
		buf := make([]byte, 512)
		for {
			if _, _, err := pc.ReadFrom(buf); err != nil {
				return
			}
			packets.Add(1)
		}
	}()

	res := executeCheck(RelayCheckConfig{
		MonitorID:      "m-dns",
		Type:           "dns",
		Target:         "example.com",
		DNSHost:        "example.com",
		DNSNameserver:  pc.LocalAddr().String(),
		TimeoutSeconds: 2,
	}, mustAllowList(t, "192.168.0.0/16"))

	time.Sleep(50 * time.Millisecond) // let the listener goroutine observe any packet
	if packets.Load() != 0 {
		t.Fatalf("query was sent to non-allowlisted nameserver (%d packets)", packets.Load())
	}
	if res.Status != "error" || !strings.Contains(res.Error, "ALLOWED_PROBE_HOSTS") {
		t.Fatalf("expected error mentioning ALLOWED_PROBE_HOSTS, got %+v", res)
	}
}
