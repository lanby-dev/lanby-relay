package relay

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Runner struct {
	log    *slog.Logger
	cfg    Config
	client *Client
}

func NewRunner(log *slog.Logger, cfg Config, client *Client) *Runner {
	return &Runner{log: log, cfg: cfg, client: client}
}

func (r *Runner) Start(ctx context.Context) error {
	identity, err := r.loadOrClaim(ctx)
	if err != nil {
		return err
	}
	return r.runLoop(ctx, identity)
}

func (r *Runner) loadOrClaim(ctx context.Context) (Identity, error) {
	if id, err := r.loadIdentity(); err == nil {
		return id, nil
	}
	host, _ := os.Hostname()
	claim, err := r.client.RequestClaim(ctx, ClaimRequest{
		Hostname:     host,
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		RelayVersion: r.cfg.RelayVersion,
	})
	if err != nil {
		return Identity{}, err
	}
	r.log.Info("relay waiting to be claimed", "claim_code", claim.ClaimCode, "expires_at", claim.ExpiresAt)
	ticker := time.NewTicker(time.Duration(claim.PollIntervalSeconds) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return Identity{}, ctx.Err()
		case <-ticker.C:
			st, err := r.client.ClaimStatus(ctx, claim.RelayID)
			if err != nil {
				r.log.Warn("claim status failed", "error", err)
				continue
			}
			if st.Status != "claimed" || st.RelaySecret == "" {
				continue
			}
			id := Identity{
				RelayID:                   claim.RelayID,
				RelaySecret:               st.RelaySecret,
				ClaimedAt:                 time.Now().UTC(),
				PlatformURL:               r.cfg.PlatformURL,
				ConfigPollIntervalSeconds: st.ConfigPollIntervalSeconds,
			}
			if err := r.saveIdentity(id); err != nil {
				return Identity{}, err
			}
			r.log.Info("relay claimed successfully", "relay_id", id.RelayID)
			return id, nil
		}
	}
}

func (r *Runner) runLoop(ctx context.Context, id Identity) error {
	pollSeconds := id.ConfigPollIntervalSeconds
	if pollSeconds <= 0 {
		pollSeconds = r.cfg.DefaultPollSeconds
	}
	configTicker := time.NewTicker(time.Duration(pollSeconds) * time.Second)
	probeTicker := time.NewTicker(1 * time.Second)
	heartbeatTicker := time.NewTicker(60 * time.Second)
	defer configTicker.Stop()
	defer probeTicker.Stop()
	defer heartbeatTicker.Stop()

	var (
		etag                 string
		mu                   sync.RWMutex
		checks               []RelayCheckConfig
		nextRuns             = map[string]time.Time{}
		stateByMonitor       = map[string]string{}
		consecutiveFailures  = map[string]int{}
		consecutiveSuccesses = map[string]int{}
	)

	runDueChecks := func(now time.Time) {
		mu.RLock()
		current := append([]RelayCheckConfig(nil), checks...)
		mu.RUnlock()
		if len(current) == 0 {
			return
		}
		results := make([]ResultItem, 0, len(current))
		for _, c := range current {
			next, ok := nextRuns[c.MonitorID]
			if ok && now.Before(next) {
				continue
			}
			intervalSec := c.IntervalSeconds
			if intervalSec <= 0 {
				intervalSec = 30
			}
			nextRuns[c.MonitorID] = now.Add(time.Duration(intervalSec) * time.Second)

			r.log.Info("running probe",
				"monitor_id", c.MonitorID,
				"type", c.Type,
				"target", c.Target,
				"interval_seconds", intervalSec,
			)
			res := executeCheck(c)
			res.State, res.StateChanged = evaluateStateTransition(
				c.MonitorID,
				res.Status,
				stateByMonitor,
				consecutiveFailures,
				consecutiveSuccesses,
			)
			r.log.Info("probe result",
				"monitor_id", c.MonitorID,
				"type", c.Type,
				"target", c.Target,
				"status", res.Status,
				"state", res.State,
				"duration_ms", res.DurationMs,
				"status_code", res.StatusCode,
				"error", res.Error,
			)
			results = append(results, res)
		}
		if len(results) == 0 {
			return
		}
		if err := r.client.PushResults(ctx, id, results, r.cfg.RelayVersion); err != nil {
			r.log.Warn("push results failed", "error", err)
		}
	}

	pollConfig := func() {
		applyConfig := func(cfg *ConfigResponse, nextETag string) {
			updatedChecks := cfg.Checks
			seen := map[string]struct{}{}
			for _, c := range updatedChecks {
				seen[c.MonitorID] = struct{}{}
				if _, ok := nextRuns[c.MonitorID]; !ok {
					// New checks run immediately on next probe tick.
					nextRuns[c.MonitorID] = time.Now().UTC()
				}
			}
			for monitorID := range nextRuns {
				if _, ok := seen[monitorID]; !ok {
					delete(nextRuns, monitorID)
					delete(stateByMonitor, monitorID)
					delete(consecutiveFailures, monitorID)
					delete(consecutiveSuccesses, monitorID)
				}
			}
			mu.Lock()
			checks = updatedChecks
			mu.Unlock()
			etag = nextETag
			if len(cfg.Tests) > 0 {
				testResults := runRelayURLTests(cfg.Tests)
				if err := r.client.SubmitTestResults(ctx, id, testResults); err != nil {
					r.log.Warn("submit test results failed", "error", err)
				}
			}
		}

		cfg, nextETag, notModified, err := r.client.GetConfig(ctx, id, etag)
		if err != nil {
			if isUnauthorized(err) {
				r.log.Warn("relay credential rejected; deleting local identity and re-claiming", "error", err)
				_ = os.Remove(r.cfg.IdentityPath)
				newID, claimErr := r.loadOrClaim(ctx)
				if claimErr != nil {
					r.log.Warn("re-claim failed", "error", claimErr)
					return
				}
				id = newID
				etag = ""

				newPoll := id.ConfigPollIntervalSeconds
				if newPoll <= 0 {
					newPoll = r.cfg.DefaultPollSeconds
				}
				if newPoll != pollSeconds {
					pollSeconds = newPoll
					configTicker.Reset(time.Duration(pollSeconds) * time.Second)
				}

				// Immediately fetch config for the newly claimed identity so probes
				// do not wait for the next poll interval.
				cfg, nextETag, notModified, err = r.client.GetConfig(ctx, id, "")
				if err != nil {
					r.log.Warn("immediate config fetch after re-claim failed", "error", err)
					return
				}
				if !notModified && cfg != nil {
					applyConfig(cfg, nextETag)
				}
				return
			}
			r.log.Warn("config poll failed", "error", err)
			return
		}
		if !notModified && cfg != nil {
			applyConfig(cfg, nextETag)
		}
	}

	// Fetch immediately on startup instead of waiting for first ticker interval.
	pollConfig()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-configTicker.C:
			pollConfig()
		case <-probeTicker.C:
			runDueChecks(time.Now().UTC())
		case <-heartbeatTicker.C:
			if err := r.client.Heartbeat(ctx, id, r.cfg.RelayVersion); err != nil {
				r.log.Warn("heartbeat failed", "error", err)
			}
		}
	}
}

func evaluateStateTransition(
	monitorID string,
	status string,
	stateByMonitor map[string]string,
	consecutiveFailures map[string]int,
	consecutiveSuccesses map[string]int,
) (string, bool) {
	prev := stateByMonitor[monitorID]
	if prev == "" {
		prev = "unknown"
	}

	isSuccess := status == "ok" || status == "degraded"
	const (
		downAfterFailures = 3
		upAfterSuccesses  = 2
	)

	var next string
	if isSuccess {
		consecutiveSuccesses[monitorID]++
		consecutiveFailures[monitorID] = 0
		if prev == "down" {
			if consecutiveSuccesses[monitorID] >= upAfterSuccesses {
				next = "up"
			} else {
				next = "degraded"
			}
		} else {
			next = "up"
		}
	} else {
		consecutiveFailures[monitorID]++
		consecutiveSuccesses[monitorID] = 0
		if consecutiveFailures[monitorID] >= downAfterFailures {
			next = "down"
		} else {
			next = "degraded"
		}
	}

	stateByMonitor[monitorID] = next
	return next, next != prev
}

func isUnauthorized(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "401")
}

// Ad-hoc URL tests from the UI share this timeout. LAN targets (e.g. routers) may be
// slow to refuse or accept TCP; 10s was too aggressive and conflicted with DefaultClient.
const relayAdHocURLTestTimeout = 30 * time.Second

func runRelayURLTests(tests []RelayURLTest) []RelayURLTestResult {
	redirectPolicy := func(_ *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return http.ErrUseLastResponse
		}
		return nil
	}
	out := make([]RelayURLTestResult, 0, len(tests))
	for _, t := range tests {
		client := &http.Client{
			Timeout:       relayAdHocURLTestTimeout,
			CheckRedirect: redirectPolicy,
		}
		if t.IgnoreTLSErrors {
			client.Transport = &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
			}
		}
		start := time.Now()
		req, err := http.NewRequest(http.MethodGet, t.URL, nil)
		if err != nil {
			out = append(out, RelayURLTestResult{TestID: t.ID, Reachable: false, Error: err.Error()})
			continue
		}
		resp, err := client.Do(req)
		lat := time.Since(start).Milliseconds()
		if err != nil {
			out = append(out, RelayURLTestResult{TestID: t.ID, Reachable: false, Error: err.Error(), LatencyMs: lat})
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8192))
		_ = resp.Body.Close()
		out = append(out, RelayURLTestResult{
			TestID:     t.ID,
			Reachable:  true,
			StatusCode: resp.StatusCode,
			LatencyMs:  lat,
		})
	}
	return out
}

func (r *Runner) loadIdentity() (Identity, error) {
	b, err := os.ReadFile(r.cfg.IdentityPath)
	if err != nil {
		return Identity{}, err
	}
	var id Identity
	if err := json.Unmarshal(b, &id); err != nil {
		return Identity{}, err
	}
	if id.RelayID == "" || id.RelaySecret == "" {
		return Identity{}, fmt.Errorf("identity file is incomplete")
	}
	return id, nil
}

func (r *Runner) saveIdentity(id Identity) error {
	dir := filepath.Dir(r.cfg.IdentityPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.cfg.IdentityPath, b, 0o600)
}
