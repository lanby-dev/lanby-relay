package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	claimStatusWaitSec = 45
	httpTimeout        = 60 * time.Second
	claimHTTPTimeout   = (claimStatusWaitSec + 20) * time.Second
)

type Client struct {
	baseURL string
	http    *http.Client
	claim   *http.Client
}

// SyncExtra is optional probe / URL-test data sent with POST .../sync.
type SyncExtra struct {
	Results        []ResultItem
	RelayTimestamp time.Time
	TestResults    []RelayURLTestResult
}

func NewClient(baseURL string) *Client {
	base := strings.TrimRight(baseURL, "/")
	return &Client{
		baseURL: base,
		http:    &http.Client{Timeout: httpTimeout},
		claim:   &http.Client{Timeout: claimHTTPTimeout},
	}
}

func (c *Client) RequestClaim(ctx context.Context, req ClaimRequest) (*ClaimResponse, error) {
	var out ClaimResponse
	if err := c.doJSON(ctx, c.http, http.MethodPost, "/api/v1/relays/claim/request", "", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ClaimStatus(ctx context.Context, relayID string) (*ClaimStatusResponse, error) {
	path := fmt.Sprintf("/api/v1/relays/claim/%s/status?wait=%d", relayID, claimStatusWaitSec)
	var out ClaimStatusResponse
	if err := c.doJSON(ctx, c.claim, http.MethodGet, path, "", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Sync(ctx context.Context, id Identity, etag, relayVersion string, extra *SyncExtra) (*SyncResponse, error) {
	payload := map[string]any{"relay_version": relayVersion}
	if etag != "" {
		payload["config_etag"] = etag
	}
	if extra != nil {
		if len(extra.Results) > 0 {
			payload["results"] = extra.Results
			ts := extra.RelayTimestamp
			if ts.IsZero() {
				ts = time.Now().UTC()
			}
			payload["relay_timestamp"] = ts
		}
		if len(extra.TestResults) > 0 {
			payload["test_results"] = extra.TestResults
		}
	}
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return nil, err
	}
	url := c.baseURL + "/api/v1/relays/" + id.RelayID + "/sync"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+id.RelaySecret)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("sync request failed: 401 Unauthorized")
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("sync request failed: %s", resp.Status)
	}
	var out SyncResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) doJSON(ctx context.Context, hc *http.Client, method, path, bearer string, in any, out any) error {
	var body bytes.Buffer
	if in != nil {
		if err := json.NewEncoder(&body).Encode(in); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s failed: %s", method, path, resp.Status)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return err
		}
	}
	return nil
}
