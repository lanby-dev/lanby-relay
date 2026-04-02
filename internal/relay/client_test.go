package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewClient_TrimsTrailingSlash(t *testing.T) {
	c := NewClient("http://example.com/")
	if c.baseURL != "http://example.com" {
		t.Fatalf("expected trimmed base URL, got %q", c.baseURL)
	}
}

func TestClient_RequestClaimAndClaimStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/relays/claim/request":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method %s", r.Method)
			}
			var req ClaimRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if req.Hostname != "host-1" {
				t.Fatalf("expected hostname host-1, got %q", req.Hostname)
			}
			_ = json.NewEncoder(w).Encode(ClaimResponse{
				RelayID:             "r1",
				ClaimCode:           "ABCD-1234",
				ExpiresAt:           time.Now().UTC(),
				PollIntervalSeconds: 5,
			})
		case "/api/v1/relays/claim/r1/status":
			if r.Method != http.MethodGet {
				t.Fatalf("unexpected method %s", r.Method)
			}
			_ = json.NewEncoder(w).Encode(ClaimStatusResponse{
				Status:                    "claimed",
				RelaySecret:               "sec",
				ConfigPollIntervalSeconds: 30,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	claim, err := c.RequestClaim(context.Background(), ClaimRequest{
		Hostname:     "host-1",
		OS:           "linux",
		Arch:         "amd64",
		RelayVersion: "1.0.0",
	})
	if err != nil {
		t.Fatalf("request claim: %v", err)
	}
	if claim.RelayID != "r1" {
		t.Fatalf("expected relay id r1, got %q", claim.RelayID)
	}

	status, err := c.ClaimStatus(context.Background(), "r1")
	if err != nil {
		t.Fatalf("claim status: %v", err)
	}
	if status.Status != "claimed" || status.RelaySecret != "sec" {
		t.Fatalf("unexpected claim status response: %+v", status)
	}
}

func TestClient_GetConfig_WithETagAndNotModified(t *testing.T) {
	var sawIfNoneMatch string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/relays/r1/config" {
			http.NotFound(w, r)
			return
		}
		sawIfNoneMatch = r.Header.Get("If-None-Match")
		if sawIfNoneMatch == "v1" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", "v1")
		_ = json.NewEncoder(w).Encode(ConfigResponse{
			ConfigVersion: 1,
			Checks:        []RelayCheckConfig{{MonitorID: "m1", Type: "http", Target: "http://svc"}},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	id := Identity{RelayID: "r1", RelaySecret: "secret"}

	cfg, etag, notModified, err := c.GetConfig(context.Background(), id, "")
	if err != nil {
		t.Fatalf("first get config: %v", err)
	}
	if notModified {
		t.Fatalf("expected modified response on first fetch")
	}
	if etag != "v1" || cfg == nil || len(cfg.Checks) != 1 {
		t.Fatalf("unexpected first config response: etag=%q cfg=%+v", etag, cfg)
	}

	cfg, etag, notModified, err = c.GetConfig(context.Background(), id, "v1")
	if err != nil {
		t.Fatalf("second get config: %v", err)
	}
	if !notModified || cfg != nil || etag != "v1" {
		t.Fatalf("expected 304 behavior, got cfg=%v etag=%q notModified=%v", cfg, etag, notModified)
	}
	if sawIfNoneMatch != "v1" {
		t.Fatalf("expected If-None-Match header v1, got %q", sawIfNoneMatch)
	}
}

func TestClient_doJSON_ErrorStatusIncludesPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	err := c.doJSON(context.Background(), http.MethodPost, "/test/path", "token", map[string]string{"x": "y"}, nil)
	if err == nil {
		t.Fatal("expected error for non-2xx status")
	}
	if !strings.Contains(err.Error(), "/test/path") {
		t.Fatalf("expected error to include path, got %q", err.Error())
	}
}
