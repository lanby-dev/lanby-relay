package relay

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-ping/ping"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

const maxRelayProbeBodyBytes = 1 << 20

func executeCheck(cfg RelayCheckConfig) ResultItem {
	start := time.Now()
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	result := ResultItem{
		MonitorID: cfg.MonitorID,
		Timestamp: start.UTC(),
	}

	switch cfg.Type {
	case "http":
		return executeHTTPCheck(ctx, cfg, start)
	case "tcp":
		return executeTCPCheck(ctx, cfg, start)
	case "dns":
		return executeDNSCheck(ctx, cfg, start)
	case "grpc_health":
		return executeGRPCHealthCheck(ctx, cfg, start)
	case "ping":
		return executePingCheck(ctx, cfg, start)
	default:
		result.DurationMs = time.Since(start).Milliseconds()
		result.Status = "error"
		result.Error = fmt.Sprintf("unsupported check type: %s", cfg.Type)
		result.State = "down"
		return result
	}
}

func executeHTTPCheck(ctx context.Context, cfg RelayCheckConfig, start time.Time) ResultItem {
	result := ResultItem{MonitorID: cfg.MonitorID, Timestamp: start.UTC()}

	method := cfg.Method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, cfg.Target, nil)
	if err != nil {
		result.Status = "error"
		result.Error = err.Error()
		result.State = "down"
		result.DurationMs = time.Since(start).Milliseconds()
		return result
	}
	for k, v := range cfg.Headers {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		req.Header.Set(k, v)
	}

	httpTimeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if httpTimeout <= 0 {
		httpTimeout = 5 * time.Second
	}
	maxRedir := cfg.MaxRedirects
	if maxRedir <= 0 {
		maxRedir = 5
	}
	tlsCfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.IgnoreTLSErrors, //nolint:gosec
	}
	httpClient := &http.Client{
		Timeout: httpTimeout,
		Transport: &http.Transport{
			TLSClientConfig: tlsCfg,
		},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if !cfg.FollowRedirects {
				return http.ErrUseLastResponse
			}
			if len(via) >= maxRedir {
				return fmt.Errorf("stopped after %d redirects", maxRedir)
			}
			return nil
		},
	}

	resp, err := httpClient.Do(req)
	result.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		result.Status = "error"
		result.Error = err.Error()
		result.State = "down"
		return result
	}
	defer func() { _ = resp.Body.Close() }()
	result.StatusCode = resp.StatusCode

	if err := verifyRelayCert(cfg, resp); err != nil {
		result.Status = "fail"
		result.Error = err.Error()
		result.State = "down"
		return result
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRelayProbeBodyBytes))
	if err != nil {
		result.Status = "error"
		result.Error = err.Error()
		result.State = "down"
		return result
	}

	if !httpStatusOK(cfg, resp.StatusCode) {
		result.Status = "fail"
		result.Error = fmt.Sprintf("HTTP %d (outside allowed status rules)", resp.StatusCode)
		result.State = "down"
		return result
	}

	if want := strings.TrimSpace(cfg.HTTPBodyContains); want != "" {
		if !bytes.Contains(body, []byte(want)) {
			result.Status = "fail"
			result.Error = fmt.Sprintf("response body does not contain %q", want)
			result.State = "down"
			return result
		}
	}

	result.Status = "ok"
	result.State = "up"
	return result
}

func httpStatusOK(cfg RelayCheckConfig, status int) bool {
	if len(cfg.SuccessHTTPStatusCodes) > 0 {
		for _, c := range cfg.SuccessHTTPStatusCodes {
			if c == status {
				return true
			}
		}
		return false
	}
	if cfg.ExpectedStatus != 0 {
		return status == cfg.ExpectedStatus
	}
	return status >= 200 && status < 300
}

func verifyRelayCert(cfg RelayCheckConfig, resp *http.Response) error {
	if !cfg.CheckCertExpiry || resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		return nil
	}
	leaf := resp.TLS.PeerCertificates[0]
	now := time.Now()
	if now.After(leaf.NotAfter) {
		return fmt.Errorf("certificate expired on %s", leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	minDays := cfg.CertExpiryMinDays
	if minDays <= 0 {
		minDays = 14
	}
	warnBy := now.AddDate(0, 0, minDays)
	if leaf.NotAfter.Before(warnBy) {
		return fmt.Errorf("certificate expires on %s (within %d days)", leaf.NotAfter.UTC().Format(time.RFC3339), minDays)
	}
	return nil
}

func executeTCPCheck(ctx context.Context, cfg RelayCheckConfig, start time.Time) ResultItem {
	result := ResultItem{MonitorID: cfg.MonitorID, Timestamp: start.UTC()}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", cfg.Target)
	result.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		result.Status = "error"
		result.Error = err.Error()
		result.State = "down"
		return result
	}
	_ = conn.Close()
	result.Status = "ok"
	result.State = "up"
	return result
}

func executeDNSCheck(ctx context.Context, cfg RelayCheckConfig, start time.Time) ResultItem {
	result := ResultItem{MonitorID: cfg.MonitorID, Timestamp: start.UTC()}
	host := strings.TrimSpace(cfg.DNSHost)
	if host == "" {
		host = strings.TrimSpace(cfg.Target)
	}
	if host == "" {
		result.DurationMs = time.Since(start).Milliseconds()
		result.Status = "error"
		result.Error = "dns_host is required"
		result.State = "down"
		return result
	}
	qtype := strings.ToUpper(strings.TrimSpace(cfg.DNSType))
	if qtype == "" {
		qtype = "A"
	}
	expect := strings.TrimSpace(cfg.DNSExpect)

	nsAddr, nerr := normalizeDNSNameserver(cfg.DNSNameserver)
	if nerr != nil {
		result.DurationMs = time.Since(start).Milliseconds()
		result.Status = "error"
		result.Error = nerr.Error()
		result.State = "down"
		return result
	}

	var answers []string
	if nsAddr != "" {
		var err error
		answers, err = queryDNSNameserver(ctx, nsAddr, host, qtype)
		if err != nil {
			result.DurationMs = time.Since(start).Milliseconds()
			result.Status = "error"
			result.Error = err.Error()
			result.State = "down"
			return result
		}
		if len(answers) == 0 {
			result.DurationMs = time.Since(start).Milliseconds()
			result.Status = "fail"
			result.Error = fmt.Sprintf("no %s records for %q", qtype, host)
			result.State = "down"
			return result
		}
	} else {
		r := &net.Resolver{PreferGo: true}
		switch qtype {
		case "A", "AAAA":
			ips, err := r.LookupIPAddr(ctx, host)
			if err != nil {
				result.DurationMs = time.Since(start).Milliseconds()
				result.Status = "error"
				result.Error = err.Error()
				result.State = "down"
				return result
			}
			for _, ip := range ips {
				if qtype == "A" && ip.IP.To4() != nil {
					answers = append(answers, ip.IP.String())
				}
				if qtype == "AAAA" && ip.IP.To4() == nil {
					answers = append(answers, ip.IP.String())
				}
			}
			if len(answers) == 0 {
				result.DurationMs = time.Since(start).Milliseconds()
				result.Status = "fail"
				result.Error = fmt.Sprintf("no %s records for %q", qtype, host)
				result.State = "down"
				return result
			}
		case "CNAME":
			cname, err := r.LookupCNAME(ctx, host)
			if err != nil {
				result.DurationMs = time.Since(start).Milliseconds()
				result.Status = "error"
				result.Error = err.Error()
				result.State = "down"
				return result
			}
			cname = strings.TrimSuffix(cname, ".")
			if cname == "" {
				result.DurationMs = time.Since(start).Milliseconds()
				result.Status = "fail"
				result.Error = fmt.Sprintf("no CNAME for %q", host)
				result.State = "down"
				return result
			}
			answers = []string{cname}
		case "TXT":
			txts, err := r.LookupTXT(ctx, host)
			if err != nil {
				result.DurationMs = time.Since(start).Milliseconds()
				result.Status = "error"
				result.Error = err.Error()
				result.State = "down"
				return result
			}
			if len(txts) == 0 {
				result.DurationMs = time.Since(start).Milliseconds()
				result.Status = "fail"
				result.Error = fmt.Sprintf("no TXT records for %q", host)
				result.State = "down"
				return result
			}
			answers = txts
		case "NS":
			nss, err := r.LookupNS(ctx, host)
			if err != nil {
				result.DurationMs = time.Since(start).Milliseconds()
				result.Status = "error"
				result.Error = err.Error()
				result.State = "down"
				return result
			}
			if len(nss) == 0 {
				result.DurationMs = time.Since(start).Milliseconds()
				result.Status = "fail"
				result.Error = fmt.Sprintf("no NS records for %q", host)
				result.State = "down"
				return result
			}
			for _, ns := range nss {
				answers = append(answers, strings.TrimSuffix(ns.Host, "."))
			}
		default:
			result.DurationMs = time.Since(start).Milliseconds()
			result.Status = "error"
			result.Error = fmt.Sprintf("unsupported dns_type %q", qtype)
			result.State = "down"
			return result
		}
	}

	if expect != "" {
		matched := false
		for _, a := range answers {
			if strings.Contains(a, expect) {
				matched = true
				break
			}
		}
		if !matched {
			result.DurationMs = time.Since(start).Milliseconds()
			result.Status = "fail"
			result.Error = fmt.Sprintf("dns answers for %q do not contain %q", host, expect)
			result.State = "down"
			return result
		}
	}

	result.DurationMs = time.Since(start).Milliseconds()
	result.Status = "ok"
	result.State = "up"
	return result
}

func executeGRPCHealthCheck(ctx context.Context, cfg RelayCheckConfig, start time.Time) ResultItem {
	result := ResultItem{MonitorID: cfg.MonitorID, Timestamp: start.UTC()}
	if cfg.Target == "" {
		result.DurationMs = time.Since(start).Milliseconds()
		result.Status = "error"
		result.Error = "grpc target is required"
		result.State = "down"
		return result
	}

	var opts []grpc.DialOption
	if cfg.GRPTLS {
		host, _, err := net.SplitHostPort(cfg.Target)
		if err != nil {
			host = cfg.Target
		}
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: host,
		})))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	conn, err := grpc.NewClient(cfg.Target, opts...)
	if err != nil {
		result.DurationMs = time.Since(start).Milliseconds()
		result.Status = "error"
		result.Error = err.Error()
		result.State = "down"
		return result
	}
	defer func() { _ = conn.Close() }()

	client := healthpb.NewHealthClient(conn)
	hcResp, err := client.Check(ctx, &healthpb.HealthCheckRequest{Service: cfg.GRPCService})
	result.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		result.Status = "error"
		result.Error = err.Error()
		result.State = "down"
		return result
	}
	if hcResp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		result.Status = "fail"
		result.Error = fmt.Sprintf("gRPC health status %v", hcResp.GetStatus())
		result.State = "down"
		return result
	}
	result.Status = "ok"
	result.State = "up"
	return result
}

// executePingCheck sends ICMP echo requests from the relay host (same role as “ping” in Uptime Kuma
// when the checker runs on your network). Requires raw ICMP capability (e.g. Linux CAP_NET_RAW) or
// an OS that allows unprivileged ping for this process.
func executePingCheck(ctx context.Context, cfg RelayCheckConfig, start time.Time) ResultItem {
	result := ResultItem{MonitorID: cfg.MonitorID, Timestamp: start.UTC()}
	host := strings.TrimSpace(cfg.Target)
	if host == "" {
		result.DurationMs = time.Since(start).Milliseconds()
		result.Status = "error"
		result.Error = "ping target host is required"
		result.State = "down"
		return result
	}

	deadline := time.Duration(cfg.TimeoutSeconds) * time.Second
	if deadline <= 0 {
		deadline = 10 * time.Second
	}

	type outcome struct {
		ok  bool
		msg string
	}
	ch := make(chan outcome, 1)
	go func() {
		var lastErr error
		for _, privileged := range []bool{false, true} {
			pinger, err := ping.NewPinger(host)
			if err != nil {
				ch <- outcome{false, err.Error()}
				return
			}
			pinger.Count = 1
			pinger.Timeout = deadline
			pinger.SetPrivileged(privileged)
			lastErr = pinger.Run()
			if lastErr != nil {
				continue
			}
			stats := pinger.Statistics()
			if stats.PacketsRecv == 0 {
				ch <- outcome{false, "no ICMP reply"}
				return
			}
			ch <- outcome{true, ""}
			return
		}
		if lastErr != nil {
			ch <- outcome{false, lastErr.Error()}
			return
		}
		ch <- outcome{false, "ping failed"}
	}()

	select {
	case <-ctx.Done():
		result.DurationMs = time.Since(start).Milliseconds()
		result.Status = "error"
		result.Error = ctx.Err().Error()
		result.State = "down"
		return result
	case out := <-ch:
		result.DurationMs = time.Since(start).Milliseconds()
		if !out.ok {
			result.Status = "error"
			result.Error = out.msg
			result.State = "down"
			return result
		}
		result.Status = "ok"
		result.State = "up"
		return result
	}
}
