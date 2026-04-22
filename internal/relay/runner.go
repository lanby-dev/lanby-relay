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

const maxPendingProbeResults = 500

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

	for {
		st, err := r.client.ClaimStatus(ctx, claim.RelayID)
		if err != nil {
			r.log.Warn("claim status failed", "error", err)
			select {
			case <-ctx.Done():
				return Identity{}, ctx.Err()
			case <-time.After(800 * time.Millisecond):
			}
			continue
		}
		switch st.Status {
		case "expired":
			return Identity{}, fmt.Errorf("relay claim code expired before completion")
		case "claimed":
			if st.RelaySecret == "" {
				return Identity{}, fmt.Errorf("claimed relay missing secret")
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
		default:
			// pending — server ended long-poll window; loop immediately for another wait
		}
	}
}

func probeSuccess(status string) bool {
	return status == "ok" || status == "degraded"
}

func clampRelayPollSeconds(sec int) int {
	if sec < 5 {
		return 5
	}
	if sec > 600 {
		return 600
	}
	return sec
}

func syncTickFromChecks(checks []RelayCheckConfig, fallback int) int {
	if fallback <= 0 {
		fallback = 15
	}
	if len(checks) == 0 {
		return clampRelayPollSeconds(fallback)
	}
	minSec := int(^uint(0) >> 1)
	for _, c := range checks {
		sec := c.IntervalSeconds
		if sec <= 0 {
			sec = 30
		}
		if sec < minSec {
			minSec = sec
		}
	}
	return clampRelayPollSeconds(minSec)
}

func (r *Runner) runLoop(ctx context.Context, id Identity) error {
	pollSeconds := id.ConfigPollIntervalSeconds
	if pollSeconds <= 0 {
		pollSeconds = r.cfg.DefaultPollSeconds
	}
	pollSeconds = clampRelayPollSeconds(pollSeconds)
	syncTicker := time.NewTicker(time.Duration(pollSeconds) * time.Second)
	probeTicker := time.NewTicker(1 * time.Second)
	defer syncTicker.Stop()
	defer probeTicker.Stop()

	var (
		etag     string
		mu       sync.RWMutex
		checks   []RelayCheckConfig
		nextRuns = map[string]time.Time{}
	)

	var bufMu sync.Mutex
	pendingProbe := make([]ResultItem, 0, 32)
	pendingTests := make([]RelayURLTestResult, 0, 8)
	lastOutcome := map[string]string{}
	stateByMonitor := map[string]string{}
	highTouch := false

	updateOutcomesAndHighTouch := func(batch []ResultItem, cur []RelayCheckConfig) {
		bufMu.Lock()
		defer bufMu.Unlock()
		for _, res := range batch {
			lastOutcome[res.MonitorID] = res.Status
			if !probeSuccess(res.Status) {
				highTouch = true
			}
		}
		if !highTouch {
			return
		}
		if len(cur) == 0 {
			highTouch = false
			return
		}
		allGreen := true
		for _, c := range cur {
			st, ok := lastOutcome[c.MonitorID]
			if !ok || !probeSuccess(st) {
				allGreen = false
				break
			}
		}
		if allGreen {
			highTouch = false
		}
	}

	resetSyncTicker := func() {
		p := pollSeconds
		if p <= 0 {
			p = r.cfg.DefaultPollSeconds
		}
		p = clampRelayPollSeconds(p)
		syncTicker.Reset(time.Duration(p) * time.Second)
	}

	persistSyncInterval := func() {
		id.ConfigPollIntervalSeconds = pollSeconds
		if err := r.saveIdentity(id); err != nil {
			r.log.Warn("save identity after interval update failed", "error", err)
		}
	}

	recomputePollFromChecks := func(cur []RelayCheckConfig, serverFallback int) {
		next := syncTickFromChecks(cur, serverFallback)
		if next != pollSeconds {
			pollSeconds = next
			resetSyncTicker()
			persistSyncInterval()
		}
	}

	appendPending := func(batch []ResultItem) {
		bufMu.Lock()
		pendingProbe = append(pendingProbe, batch...)
		if len(pendingProbe) > maxPendingProbeResults {
			pendingProbe = pendingProbe[len(pendingProbe)-maxPendingProbeResults:]
		}
		bufMu.Unlock()
	}

	var syncOnce func()

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
			// Switch to recovery interval when the server-reported state is down/degraded.
			// Uses stateByMonitor (updated from check_states on every sync) so the trigger
			// matches platform behaviour: both paths use monitor DB state, not raw outcomes.
			if c.RecoveryIntervalSeconds > 0 {
				st := stateByMonitor[c.MonitorID]
				if st == "down" || st == "degraded" {
					intervalSec = c.RecoveryIntervalSeconds
				}
			}
			nextRuns[c.MonitorID] = now.Add(time.Duration(intervalSec) * time.Second)

			if !r.cfg.AllowedProbeHosts.Allowed(c.Target) {
				r.log.Warn("probe target blocked by ALLOWED_PROBE_HOSTS",
					"monitor_id", c.MonitorID,
					"target", c.Target,
				)
				continue
			}

			r.log.Debug("running probe",
				"monitor_id", c.MonitorID,
				"type", c.Type,
				"target", c.Target,
				"interval_seconds", intervalSec,
			)
			res := executeCheck(c)
			res.State, res.StateChanged = "", false
			if probeSuccess(res.Status) {
				r.log.Debug("probe result",
					"monitor_id", c.MonitorID,
					"status", res.Status,
					"duration_ms", res.DurationMs,
				)
			} else {
				r.log.Warn("probe result",
					"monitor_id", c.MonitorID,
					"type", c.Type,
					"target", c.Target,
					"status", res.Status,
					"duration_ms", res.DurationMs,
					"status_code", res.StatusCode,
					"error", res.Error,
				)
			}
			results = append(results, res)
		}
		if len(results) == 0 {
			return
		}
		updateOutcomesAndHighTouch(results, current)
		appendPending(results)

		bufMu.Lock()
		doImmediate := highTouch
		bufMu.Unlock()
		if doImmediate {
			syncOnce()
		}
	}

	syncOnce = func() {
		applyConfig := func(cfg *ConfigResponse, nextETag string, serverPollFallback int) {
			recomputePollFromChecks(cfg.Checks, serverPollFallback)
			updatedChecks := cfg.Checks
			seen := map[string]struct{}{}
			for _, c := range updatedChecks {
				seen[c.MonitorID] = struct{}{}
				if _, ok := nextRuns[c.MonitorID]; !ok {
					nextRuns[c.MonitorID] = time.Now().UTC()
				}
			}
			for monitorID := range nextRuns {
				if _, ok := seen[monitorID]; !ok {
					delete(nextRuns, monitorID)
				}
			}
			mu.Lock()
			checks = updatedChecks
			mu.Unlock()
			etag = nextETag
			if len(cfg.Tests) > 0 {
				testResults := runRelayURLTests(cfg.Tests)
				bufMu.Lock()
				pendingTests = append(pendingTests, testResults...)
				bufMu.Unlock()
			}
		}

		var extra *SyncExtra
		bufMu.Lock()
		if len(pendingProbe) > 0 || len(pendingTests) > 0 {
			ts := time.Now().UTC()
			extra = &SyncExtra{
				Results:        append([]ResultItem(nil), pendingProbe...),
				RelayTimestamp: ts,
				TestResults:    append([]RelayURLTestResult(nil), pendingTests...),
			}
			pendingProbe = pendingProbe[:0]
			pendingTests = pendingTests[:0]
		}
		bufMu.Unlock()

		resp, err := r.client.Sync(ctx, id, etag, r.cfg.RelayVersion, extra)
		if err != nil {
			if extra != nil {
				bufMu.Lock()
				pendingProbe = append(extra.Results, pendingProbe...)
				pendingTests = append(extra.TestResults, pendingTests...)
				bufMu.Unlock()
			}
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
				pollSeconds = id.ConfigPollIntervalSeconds
				if pollSeconds <= 0 {
					pollSeconds = r.cfg.DefaultPollSeconds
				}
				pollSeconds = clampRelayPollSeconds(pollSeconds)
				resetSyncTicker()
				resp, err = r.client.Sync(ctx, id, "", r.cfg.RelayVersion, extra)
				if err != nil {
					r.log.Warn("immediate sync after re-claim failed", "error", err)
					return
				}
			} else {
				r.log.Warn("sync failed", "error", err)
				return
			}
		}
		serverFallback := resp.ConfigPollIntervalSeconds
		if resp.ConfigPollIntervalSeconds > 0 && resp.ConfigPollIntervalSeconds != pollSeconds {
			pollSeconds = clampRelayPollSeconds(resp.ConfigPollIntervalSeconds)
			resetSyncTicker()
			persistSyncInterval()
		}
		etag = resp.ConfigETag

		// Apply server-reported states. On down/degraded transition, reset nextRuns
		// so the next probeTicker tick triggers an immediate re-check — matching
		// the platform's behaviour of adjusting the Temporal Schedule immediately.
		for monitorID, newState := range resp.CheckStates {
			prev := stateByMonitor[monitorID]
			stateByMonitor[monitorID] = newState
			if prev != newState && (newState == "down" || newState == "degraded") {
				nextRuns[monitorID] = time.Time{}
			}
		}
		// Remove states for monitors no longer in this relay's check list.
		if len(resp.CheckStates) > 0 {
			mu.RLock()
			cur := checks
			mu.RUnlock()
			seen := make(map[string]struct{}, len(cur))
			for _, c := range cur {
				seen[c.MonitorID] = struct{}{}
			}
			for id := range stateByMonitor {
				if _, ok := seen[id]; !ok {
					delete(stateByMonitor, id)
				}
			}
		}

		if !resp.ConfigUnchanged && resp.Config != nil {
			cfg := &ConfigResponse{
				ConfigVersion:             resp.Config.ConfigVersion,
				ConfigPollIntervalSeconds: resp.ConfigPollIntervalSeconds,
				Checks:                    resp.Config.Checks,
				Tests:                     resp.Config.Tests,
			}
			applyConfig(cfg, etag, serverFallback)
			// Config just applied; URL tests may have appended pendingTests — sync again in-loop.
			bufMu.Lock()
			needFlush := len(pendingTests) > 0
			bufMu.Unlock()
			if needFlush {
				syncOnce()
			}
		}
	}

	syncOnce()

	for {
		select {
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			var extra *SyncExtra
			bufMu.Lock()
			if len(pendingProbe) > 0 || len(pendingTests) > 0 {
				ts := time.Now().UTC()
				extra = &SyncExtra{
					Results:        append([]ResultItem(nil), pendingProbe...),
					RelayTimestamp: ts,
					TestResults:    append([]RelayURLTestResult(nil), pendingTests...),
				}
			}
			bufMu.Unlock()
			if extra != nil {
				if _, err := r.client.Sync(flushCtx, id, etag, r.cfg.RelayVersion, extra); err != nil {
					r.log.Warn("shutdown sync flush failed", "error", err)
				}
			}
			cancel()
			return nil
		case <-syncTicker.C:
			syncOnce()
		case <-probeTicker.C:
			runDueChecks(time.Now().UTC())
		}
	}
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
