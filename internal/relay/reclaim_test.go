package relay

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// When the platform rejects the relay credential, the relay re-claims and carries on.
// Results buffered before the 401 must reach the platform exactly once.
func TestRunner_ReclaimAfter401DoesNotDuplicateResults(t *testing.T) {
	var (
		mu         sync.Mutex
		oldSyncs   int
		newResults []time.Time
	)
	enc := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }

	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/relays/r1/sync": // old identity
			mu.Lock()
			oldSyncs++
			first := oldSyncs == 1
			mu.Unlock()
			if !first {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			enc(w, SyncResponse{
				ConfigETag:                "v1",
				ConfigPollIntervalSeconds: 30,
				Config: &SyncConfigPayload{Checks: []RelayCheckConfig{{
					MonitorID: "m1", Type: "tcp", Target: "127.0.0.1:1", // refused: fails fast, enters high-touch
					IntervalSeconds: 1, TimeoutSeconds: 1,
				}}},
			})
		case "/api/v1/relays/claim/request":
			enc(w, ClaimResponse{RelayID: "r2", ClaimCode: "ABCD", ExpiresAt: time.Now().Add(time.Minute)})
		case "/api/v1/relays/claim/r2/status":
			enc(w, ClaimStatusResponse{Status: "claimed", RelaySecret: "new", ConfigPollIntervalSeconds: 30})
		case "/api/v1/relays/r2/sync": // new identity
			var body struct {
				Results []ResultItem `json:"results"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			for _, res := range body.Results {
				newResults = append(newResults, res.Timestamp)
			}
			mu.Unlock()
			enc(w, SyncResponse{ConfigUnchanged: true, ConfigETag: "v2", ConfigPollIntervalSeconds: 30})
		default:
			http.NotFound(w, r)
		}
	}))
	defer platform.Close()

	cfg := Config{
		PlatformURL:        platform.URL,
		IdentityPath:       filepath.Join(t.TempDir(), "identity.json"),
		RelayVersion:       "test",
		DefaultPollSeconds: 30,
	}
	runner := NewRunner(slog.New(slog.NewTextHandler(io.Discard, nil)), cfg, NewClient(platform.URL))
	if err := runner.saveIdentity(Identity{RelayID: "r1", RelaySecret: "old", PlatformURL: platform.URL}); err != nil {
		t.Fatalf("save identity: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Start(ctx) }()

	// Wait until the new identity has received at least two distinct probe results.
	distinct := func() int {
		seen := map[time.Time]struct{}{}
		for _, ts := range newResults {
			seen[ts] = struct{}{}
		}
		return len(seen)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := distinct()
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if distinct() < 2 {
		t.Fatalf("new identity never received the buffered and the fresh result, got %d distinct", distinct())
	}
	counts := map[time.Time]int{}
	for _, ts := range newResults {
		counts[ts]++
	}
	for ts, n := range counts {
		if n > 1 {
			var all []string
			for _, x := range newResults {
				all = append(all, x.Format("05.000"))
			}
			t.Fatalf("result %s delivered %d times; all deliveries: %s", ts.Format("05.000"), n, strings.Join(all, ", "))
		}
	}
}
