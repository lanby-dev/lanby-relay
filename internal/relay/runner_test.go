package relay

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEvaluateStateTransition_Hysteresis(t *testing.T) {
	stateByMonitor := map[string]string{}
	consecutiveFailures := map[string]int{}
	consecutiveSuccesses := map[string]int{}
	monitorID := "m1"

	state, changed := evaluateStateTransition(monitorID, "fail", stateByMonitor, consecutiveFailures, consecutiveSuccesses)
	if state != "degraded" || !changed {
		t.Fatalf("first fail should transition to degraded, got state=%s changed=%v", state, changed)
	}
	state, _ = evaluateStateTransition(monitorID, "fail", stateByMonitor, consecutiveFailures, consecutiveSuccesses)
	if state != "degraded" {
		t.Fatalf("second fail should remain degraded, got %s", state)
	}
	state, _ = evaluateStateTransition(monitorID, "fail", stateByMonitor, consecutiveFailures, consecutiveSuccesses)
	if state != "down" {
		t.Fatalf("third fail should transition to down, got %s", state)
	}

	// One success from down should still be degraded due to recovery hysteresis.
	state, _ = evaluateStateTransition(monitorID, "ok", stateByMonitor, consecutiveFailures, consecutiveSuccesses)
	if state != "degraded" {
		t.Fatalf("first recovery success from down should be degraded, got %s", state)
	}
	state, _ = evaluateStateTransition(monitorID, "ok", stateByMonitor, consecutiveFailures, consecutiveSuccesses)
	if state != "up" {
		t.Fatalf("second recovery success should be up, got %s", state)
	}
}

func TestEvaluateStateTransition_DegradedCountsAsSuccess(t *testing.T) {
	stateByMonitor := map[string]string{}
	consecutiveFailures := map[string]int{}
	consecutiveSuccesses := map[string]int{}

	state, _ := evaluateStateTransition("m2", "degraded", stateByMonitor, consecutiveFailures, consecutiveSuccesses)
	if state != "up" {
		t.Fatalf("degraded status input should count as success signal, got %s", state)
	}
}

func TestIsUnauthorized(t *testing.T) {
	if isUnauthorized(nil) {
		t.Fatal("nil error should not be unauthorized")
	}
	if !isUnauthorized(assertErr("request failed: 401 Unauthorized")) {
		t.Fatal("401 error should be unauthorized")
	}
	if isUnauthorized(assertErr("500 server error")) {
		t.Fatal("500 error should not be unauthorized")
	}
}

func TestExecuteCheck_HTTP(t *testing.T) {
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer okSrv.Close()

	res := executeCheck(RelayCheckConfig{
		MonitorID:      "m1",
		Type:           "http",
		Target:         okSrv.URL,
		TimeoutSeconds: 2,
	})
	if res.Status != "ok" || res.State != "up" || res.StatusCode != http.StatusOK {
		t.Fatalf("expected ok/up/200 result, got %+v", res)
	}

	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer failSrv.Close()

	res = executeCheck(RelayCheckConfig{
		MonitorID:      "m2",
		Type:           "http",
		Target:         failSrv.URL,
		ExpectedStatus: http.StatusOK,
		TimeoutSeconds: 2,
	})
	if res.Status != "fail" || res.State != "down" {
		t.Fatalf("expected fail/down result for status mismatch, got %+v", res)
	}
}

func TestExecuteCheck_HTTP_FollowRedirects(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer final.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))
	defer redir.Close()

	noFollow := executeCheck(RelayCheckConfig{
		MonitorID:       "m-302",
		Type:            "http",
		Target:          redir.URL,
		FollowRedirects: false,
		TimeoutSeconds:  3,
	})
	if noFollow.StatusCode != http.StatusFound {
		t.Fatalf("expected 302 without follow redirects, got status %d", noFollow.StatusCode)
	}
	if noFollow.Status != "fail" || noFollow.State != "down" {
		t.Fatalf("expected fail/down for 302 when not following redirects, got %+v", noFollow)
	}

	follow := executeCheck(RelayCheckConfig{
		MonitorID:       "m-follow",
		Type:            "http",
		Target:          redir.URL,
		FollowRedirects: true,
		TimeoutSeconds:  3,
	})
	if follow.Status != "ok" || follow.State != "up" || follow.StatusCode != http.StatusOK {
		t.Fatalf("expected ok/up/200 when following redirects, got %+v", follow)
	}
}

func TestExecuteCheck_Ping_Localhost(t *testing.T) {
	res := executeCheck(RelayCheckConfig{
		MonitorID:      "m-ping",
		Type:           "ping",
		Target:         "127.0.0.1",
		TimeoutSeconds: 5,
	})
	if res.Status == "ok" {
		return
	}
	low := strings.ToLower(res.Error)
	if strings.Contains(low, "socket") || strings.Contains(low, "permission") || strings.Contains(low, "not permitted") || strings.Contains(low, "operation not permitted") {
		t.Skip("ICMP ping not permitted in this environment: ", res.Error)
	}
	t.Fatalf("expected ping ok or skippable permission error, got %+v", res)
}

func TestExecuteCheck_DNS(t *testing.T) {
	res := executeCheck(RelayCheckConfig{
		MonitorID:      "m-dns",
		Type:           "dns",
		Target:         "localhost",
		DNSHost:        "localhost",
		DNSType:        "A",
		TimeoutSeconds: 3,
	})
	if res.Status != "ok" || res.State != "up" {
		t.Fatalf("expected ok/up DNS result, got %+v", res)
	}
}

func TestExecuteCheck_DNS_CustomNameserver(t *testing.T) {
	res := executeCheck(RelayCheckConfig{
		MonitorID:      "m-dns-ns",
		Type:           "dns",
		Target:         "example.com",
		DNSHost:        "example.com",
		DNSType:        "A",
		DNSNameserver:  "8.8.8.8",
		TimeoutSeconds: 10,
	})
	if res.Status != "ok" {
		t.Skip("public resolver check failed (offline or blocked): ", res.Error)
	}
}

func TestExecuteCheck_TCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	res := executeCheck(RelayCheckConfig{
		MonitorID:      "m-tcp",
		Type:           "tcp",
		Target:         ln.Addr().String(),
		TimeoutSeconds: 2,
	})
	if res.Status != "ok" || res.State != "up" {
		t.Fatalf("expected ok/up TCP result, got %+v", res)
	}
}

func TestExecuteCheck_UnsupportedType(t *testing.T) {
	res := executeCheck(RelayCheckConfig{MonitorID: "m3", Type: "dns"})
	if res.Status != "error" || res.State != "down" {
		t.Fatalf("unsupported type should return error/down, got %+v", res)
	}
}

func TestRunRelayURLTests(t *testing.T) {
	reachableSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer reachableSrv.Close()

	tests := []RelayURLTest{
		{ID: "t1", URL: reachableSrv.URL},
		{ID: "t2", URL: "http://127.0.0.1:1"},
	}
	results := runRelayURLTests(tests)
	if len(results) != 2 {
		t.Fatalf("expected 2 test results, got %d", len(results))
	}

	if !results[0].Reachable || results[0].StatusCode != http.StatusNoContent {
		t.Fatalf("expected first URL test to be reachable, got %+v", results[0])
	}
	if results[1].Reachable {
		t.Fatalf("expected second URL test to be unreachable, got %+v", results[1])
	}
}

func TestRunRelayURLTests_IgnoreTLSErrors(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = &tls.Config{
		// Cert is valid for example.test only; client dials 127.0.0.1 — verification fails unless skipped.
		Certificates: []tls.Certificate{mustServerCert(t, []string{"example.test"})},
	}
	srv.StartTLS()
	defer srv.Close()

	u := srv.URL
	if !strings.HasPrefix(u, "https://") {
		t.Fatalf("expected https URL, got %q", u)
	}

	strict := runRelayURLTests([]RelayURLTest{{ID: "t_strict", URL: u}})
	if len(strict) != 1 || strict[0].Reachable {
		t.Fatalf("expected TLS verify failure without ignore_tls_errors, strict result %+v", strict[0])
	}
	low := strings.ToLower(strict[0].Error)
	if !strings.Contains(low, "tls") && !strings.Contains(low, "cert") && !strings.Contains(low, "x509") {
		t.Fatalf("expected TLS-ish error, got %q", strict[0].Error)
	}

	relaxed := runRelayURLTests([]RelayURLTest{{ID: "t_relaxed", URL: u, IgnoreTLSErrors: true}})
	if len(relaxed) != 1 || !relaxed[0].Reachable || relaxed[0].StatusCode != http.StatusOK {
		t.Fatalf("expected reachable OK with ignore_tls_errors, relaxed result %+v", relaxed[0])
	}
}

func mustServerCert(t *testing.T, dnsNames []string) tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{Organization: []string{"lanby-relay-test"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestRunner_SaveAndLoadIdentity(t *testing.T) {
	tmpDir := t.TempDir()
	idPath := filepath.Join(tmpDir, "identity.json")
	r := &Runner{
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		cfg: Config{IdentityPath: idPath},
	}

	in := Identity{
		RelayID:     "r1",
		RelaySecret: "sec",
		ClaimedAt:   time.Now().UTC(),
		PlatformURL: "http://localhost:8080",
	}
	if err := r.saveIdentity(in); err != nil {
		t.Fatalf("save identity: %v", err)
	}

	loaded, err := r.loadIdentity()
	if err != nil {
		t.Fatalf("load identity: %v", err)
	}
	if loaded.RelayID != in.RelayID || loaded.RelaySecret != in.RelaySecret {
		t.Fatalf("loaded identity mismatch: got %+v want %+v", loaded, in)
	}
}

func TestRunner_LoadIdentity_IncompleteFile(t *testing.T) {
	tmpDir := t.TempDir()
	idPath := filepath.Join(tmpDir, "identity.json")
	if err := os.WriteFile(idPath, []byte(`{"relay_id":"r1"}`), 0o600); err != nil {
		t.Fatalf("write identity fixture: %v", err)
	}

	r := &Runner{
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		cfg: Config{IdentityPath: idPath},
	}
	_, err := r.loadIdentity()
	if err == nil {
		t.Fatal("expected error for incomplete identity")
	}
}

func TestHTTPStatusOK(t *testing.T) {
	cases := []struct {
		name   string
		cfg    RelayCheckConfig
		status int
		want   bool
	}{
		{"default 200", RelayCheckConfig{}, 200, true},
		{"default 204", RelayCheckConfig{}, 204, true},
		{"default 301", RelayCheckConfig{}, 301, false},
		{"default 500", RelayCheckConfig{}, 500, false},
		{"expected_status match", RelayCheckConfig{ExpectedStatus: 201}, 201, true},
		{"expected_status mismatch", RelayCheckConfig{ExpectedStatus: 201}, 200, false},
		{"success_codes match", RelayCheckConfig{SuccessHTTPStatusCodes: []int{200, 201}}, 201, true},
		{"success_codes mismatch", RelayCheckConfig{SuccessHTTPStatusCodes: []int{200, 201}}, 204, false},
		{"success_codes overrides expected_status", RelayCheckConfig{SuccessHTTPStatusCodes: []int{201}, ExpectedStatus: 200}, 201, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := httpStatusOK(tc.cfg, tc.status); got != tc.want {
				t.Fatalf("httpStatusOK(%+v, %d) = %v, want %v", tc.cfg, tc.status, got, tc.want)
			}
		})
	}
}

func TestExecuteCheck_HTTP_BodyContains(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello world"))
	}))
	defer srv.Close()

	res := executeCheck(RelayCheckConfig{
		MonitorID:        "m-body",
		Type:             "http",
		Target:           srv.URL,
		HTTPBodyContains: "hello",
		TimeoutSeconds:   2,
	})
	if res.Status != "ok" || res.State != "up" {
		t.Fatalf("expected ok when body contains substring, got %+v", res)
	}

	res = executeCheck(RelayCheckConfig{
		MonitorID:        "m-body-miss",
		Type:             "http",
		Target:           srv.URL,
		HTTPBodyContains: "goodbye",
		TimeoutSeconds:   2,
	})
	if res.Status != "fail" || res.State != "down" {
		t.Fatalf("expected fail when body missing substring, got %+v", res)
	}
}

func TestExecuteCheck_HTTP_SuccessStatusCodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	res := executeCheck(RelayCheckConfig{
		MonitorID:              "m-codes",
		Type:                   "http",
		Target:                 srv.URL,
		SuccessHTTPStatusCodes: []int{202},
		TimeoutSeconds:         2,
	})
	if res.Status != "ok" || res.State != "up" {
		t.Fatalf("expected ok for 202 in success codes, got %+v", res)
	}

	res = executeCheck(RelayCheckConfig{
		MonitorID:              "m-codes-miss",
		Type:                   "http",
		Target:                 srv.URL,
		SuccessHTTPStatusCodes: []int{200},
		TimeoutSeconds:         2,
	})
	if res.Status != "fail" || res.State != "down" {
		t.Fatalf("expected fail for 202 not in success codes, got %+v", res)
	}
}

func TestVerifyRelayCert_Expired(t *testing.T) {
	cert := mustCertWithDates(t, time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour))
	resp := &http.Response{TLS: &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}}
	err := verifyRelayCert(RelayCheckConfig{CheckCertExpiry: true}, resp)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired error, got %v", err)
	}
}

func TestVerifyRelayCert_ExpiringSoon(t *testing.T) {
	cert := mustCertWithDates(t, time.Now().Add(-time.Hour), time.Now().Add(5*24*time.Hour))
	resp := &http.Response{TLS: &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}}

	// Default 14-day window — cert expires in 5 days, should warn.
	err := verifyRelayCert(RelayCheckConfig{CheckCertExpiry: true}, resp)
	if err == nil || !strings.Contains(err.Error(), "expires on") {
		t.Fatalf("expected expiry warning, got %v", err)
	}

	// Custom 3-day window — cert expires in 5 days, should be fine.
	err = verifyRelayCert(RelayCheckConfig{CheckCertExpiry: true, CertExpiryMinDays: 3}, resp)
	if err != nil {
		t.Fatalf("expected no error with 3-day window, got %v", err)
	}
}

func TestVerifyRelayCert_Valid(t *testing.T) {
	cert := mustCertWithDates(t, time.Now().Add(-time.Hour), time.Now().Add(30*24*time.Hour))
	resp := &http.Response{TLS: &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}}
	if err := verifyRelayCert(RelayCheckConfig{CheckCertExpiry: true}, resp); err != nil {
		t.Fatalf("expected no error for valid cert, got %v", err)
	}
}

func TestVerifyRelayCert_Disabled(t *testing.T) {
	cert := mustCertWithDates(t, time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour))
	resp := &http.Response{TLS: &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}}
	if err := verifyRelayCert(RelayCheckConfig{CheckCertExpiry: false}, resp); err != nil {
		t.Fatalf("expected no error when check disabled, got %v", err)
	}
}

func TestVerifyRelayCert_NoTLS(t *testing.T) {
	resp := &http.Response{}
	if err := verifyRelayCert(RelayCheckConfig{CheckCertExpiry: true}, resp); err != nil {
		t.Fatalf("expected no error for non-TLS response, got %v", err)
	}
}

func TestExecuteCheck_DNS_ExpectMatch(t *testing.T) {
	res := executeCheck(RelayCheckConfig{
		MonitorID:      "m-dns-expect",
		Type:           "dns",
		DNSHost:        "localhost",
		DNSType:        "A",
		DNSExpect:      "127",
		TimeoutSeconds: 3,
	})
	if res.Status != "ok" || res.State != "up" {
		t.Fatalf("expected ok when DNS expect matches, got %+v", res)
	}
}

func TestExecuteCheck_DNS_ExpectNoMatch(t *testing.T) {
	res := executeCheck(RelayCheckConfig{
		MonitorID:      "m-dns-expect-miss",
		Type:           "dns",
		DNSHost:        "localhost",
		DNSType:        "A",
		DNSExpect:      "999.999.999.999",
		TimeoutSeconds: 3,
	})
	if res.Status != "fail" || res.State != "down" {
		t.Fatalf("expected fail when DNS expect does not match, got %+v", res)
	}
}

func TestExecuteCheck_DNS_MissingHost(t *testing.T) {
	res := executeCheck(RelayCheckConfig{
		MonitorID:      "m-dns-nohost",
		Type:           "dns",
		TimeoutSeconds: 3,
	})
	if res.Status != "error" || res.State != "down" {
		t.Fatalf("expected error for missing dns_host, got %+v", res)
	}
}

func TestExecuteCheck_DNS_UnsupportedType(t *testing.T) {
	res := executeCheck(RelayCheckConfig{
		MonitorID:      "m-dns-badtype",
		Type:           "dns",
		DNSHost:        "localhost",
		DNSType:        "BOGUS",
		TimeoutSeconds: 3,
	})
	if res.Status != "error" || res.State != "down" {
		t.Fatalf("expected error for unsupported DNS type, got %+v", res)
	}
}

func TestExecuteCheck_TCP_Unreachable(t *testing.T) {
	res := executeCheck(RelayCheckConfig{
		MonitorID:      "m-tcp-fail",
		Type:           "tcp",
		Target:         "127.0.0.1:1",
		TimeoutSeconds: 1,
	})
	if res.Status != "error" || res.State != "down" {
		t.Fatalf("expected error/down for unreachable TCP, got %+v", res)
	}
}

// mustCertWithDates generates a self-signed cert valid between notBefore and notAfter.
func mustCertWithDates(t *testing.T, notBefore, notAfter time.Time) *x509.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{Organization: []string{"test"}},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
