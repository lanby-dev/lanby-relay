package relay

import "time"

type Identity struct {
	RelayID                   string    `json:"relay_id"`
	RelaySecret               string    `json:"relay_secret"`
	ClaimedAt                 time.Time `json:"claimed_at"`
	PlatformURL               string    `json:"platform_url"`
	ConfigPollIntervalSeconds int       `json:"config_poll_interval_seconds,omitempty"`
}

type ClaimRequest struct {
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	RelayVersion string `json:"relay_version"`
}

type ClaimResponse struct {
	RelayID             string    `json:"relay_id"`
	ClaimCode           string    `json:"claim_code"`
	ExpiresAt           time.Time `json:"expires_at"`
	PollIntervalSeconds int       `json:"poll_interval_seconds"`
}

type ClaimStatusResponse struct {
	Status                    string `json:"status"`
	RelaySecret               string `json:"relay_secret"`
	ConfigPollIntervalSeconds int    `json:"config_poll_interval_seconds"`
}

type RelayCheckConfig struct {
	MonitorID              string            `json:"monitor_id"`
	Name                   string            `json:"name"`
	Type                   string            `json:"type"`
	Target                 string            `json:"target"`
	IntervalSeconds        int               `json:"interval_seconds"`
	TimeoutSeconds         int               `json:"timeout_seconds"`
	Method                 string            `json:"method,omitempty"`
	ExpectedStatus         int               `json:"expected_status,omitempty"`
	SuccessHTTPStatusCodes []int             `json:"success_http_status_codes,omitempty"`
	HTTPBodyContains       string            `json:"http_body_contains,omitempty"`
	CheckCertExpiry        bool              `json:"check_cert_expiry,omitempty"`
	CertExpiryMinDays      int               `json:"cert_expiry_min_days,omitempty"`
	IgnoreTLSErrors        bool              `json:"ignore_tls_errors,omitempty"`
	FollowRedirects        bool              `json:"follow_redirects,omitempty"`
	MaxRedirects           int               `json:"max_redirects,omitempty"`
	Headers                map[string]string `json:"headers,omitempty"`
	DNSHost                string            `json:"dns_host,omitempty"`
	DNSType                string            `json:"dns_type,omitempty"`
	DNSExpect              string            `json:"dns_expect,omitempty"`
	DNSNameserver          string            `json:"dns_nameserver,omitempty"`
	GRPCService            string            `json:"grpc_service,omitempty"`
	GRPTLS                 bool              `json:"grpc_tls,omitempty"`
	Retries                int               `json:"retries,omitempty"`
	RecoverySuccesses      int               `json:"recovery_successes,omitempty"`
	SlowThresholdMs        int               `json:"slow_threshold_ms,omitempty"`
}

type ConfigResponse struct {
	ConfigVersion             int64              `json:"config_version"`
	ConfigPollIntervalSeconds int                `json:"config_poll_interval_seconds"`
	Checks                    []RelayCheckConfig `json:"checks"`
	Tests                     []RelayURLTest     `json:"tests"`
}

// SyncResponse is returned by POST /api/v1/relays/{id}/sync.
type SyncResponse struct {
	ConfigUnchanged           bool               `json:"config_unchanged"`
	ConfigETag                string             `json:"config_etag"`
	ConfigPollIntervalSeconds int                `json:"config_poll_interval_seconds"`
	Config                    *SyncConfigPayload `json:"config,omitempty"`
}

type SyncConfigPayload struct {
	ConfigVersion int64              `json:"config_version"`
	Checks        []RelayCheckConfig `json:"checks"`
	Tests         []RelayURLTest     `json:"tests"`
}

type ResultItem struct {
	MonitorID    string    `json:"monitor_id"`
	Timestamp    time.Time `json:"timestamp"`
	Status       string    `json:"status"`
	DurationMs   int64     `json:"duration_ms"`
	StatusCode   int       `json:"status_code,omitempty"`
	Error        string    `json:"error,omitempty"`
	State        string    `json:"state,omitempty"`
	StateChanged bool      `json:"state_changed,omitempty"`
}

type RelayURLTest struct {
	ID              string `json:"id"`
	RelayID         string `json:"relay_id"`
	URL             string `json:"url"`
	Status          string `json:"status"`
	IgnoreTLSErrors bool   `json:"ignore_tls_errors,omitempty"`
}

type RelayURLTestResult struct {
	TestID     string `json:"test_id"`
	Reachable  bool   `json:"reachable"`
	StatusCode int    `json:"status_code,omitempty"`
	LatencyMs  int64  `json:"latency_ms,omitempty"`
	Error      string `json:"error,omitempty"`
}
