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

type Client struct {
	baseURL string
	http    *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Client) RequestClaim(ctx context.Context, req ClaimRequest) (*ClaimResponse, error) {
	var out ClaimResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/relays/claim/request", "", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ClaimStatus(ctx context.Context, relayID string) (*ClaimStatusResponse, error) {
	var out ClaimStatusResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/relays/claim/"+relayID+"/status", "", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetConfig(ctx context.Context, id Identity, etag string) (*ConfigResponse, string, bool, error) {
	url := c.baseURL + "/api/v1/relays/" + id.RelayID + "/config"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", false, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+id.RelaySecret)
	if etag != "" {
		httpReq.Header.Set("If-None-Match", etag)
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, "", false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, true, nil
	}
	if resp.StatusCode >= 300 {
		return nil, "", false, fmt.Errorf("config request failed: %s", resp.Status)
	}
	var out ConfigResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, "", false, err
	}
	return &out, resp.Header.Get("ETag"), false, nil
}

func (c *Client) PushResults(ctx context.Context, id Identity, results []ResultItem, version string) error {
	payload := map[string]any{
		"results":         results,
		"relay_version":   version,
		"relay_timestamp": time.Now().UTC(),
	}
	return c.doJSON(ctx, http.MethodPost, "/api/v1/relays/"+id.RelayID+"/results", id.RelaySecret, payload, nil)
}

func (c *Client) Heartbeat(ctx context.Context, id Identity, version string) error {
	payload := map[string]any{"relay_version": version}
	return c.doJSON(ctx, http.MethodPost, "/api/v1/relays/"+id.RelayID+"/heartbeat", id.RelaySecret, payload, nil)
}

func (c *Client) SubmitTestResults(ctx context.Context, id Identity, results []RelayURLTestResult) error {
	payload := map[string]any{"results": results}
	return c.doJSON(ctx, http.MethodPost, "/api/v1/relays/"+id.RelayID+"/tests/results", id.RelaySecret, payload, nil)
}

func (c *Client) doJSON(ctx context.Context, method, path, bearer string, in any, out any) error {
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
	resp, err := c.http.Do(req)
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
