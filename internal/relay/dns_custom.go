package relay

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
)

func normalizeDNSNameserver(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if host, port, err := net.SplitHostPort(raw); err == nil {
		if host == "" || port == "" {
			return "", fmt.Errorf("invalid dns_nameserver %q", raw)
		}
		return net.JoinHostPort(host, port), nil
	}
	if ip := net.ParseIP(raw); ip != nil {
		return net.JoinHostPort(ip.String(), "53"), nil
	}
	if strings.Contains(raw, ":") {
		return "", fmt.Errorf("invalid dns_nameserver %q (use [IPv6]:port for IPv6)", raw)
	}
	return net.JoinHostPort(raw, "53"), nil
}

func queryDNSNameserver(ctx context.Context, nameserver, qname, qtype string) ([]string, error) {
	if nameserver == "" {
		return nil, fmt.Errorf("nameserver is required")
	}
	qt := strings.ToUpper(strings.TrimSpace(qtype))
	if qt == "" {
		qt = "A"
	}
	var rrType uint16
	switch qt {
	case "A":
		rrType = dns.TypeA
	case "AAAA":
		rrType = dns.TypeAAAA
	case "CNAME":
		rrType = dns.TypeCNAME
	case "TXT":
		rrType = dns.TypeTXT
	case "NS":
		rrType = dns.TypeNS
	default:
		return nil, fmt.Errorf("unsupported dns_type %q", qtype)
	}

	deadline, ok := ctx.Deadline()
	timeout := 5 * time.Second
	if ok {
		timeout = time.Until(deadline)
		if timeout < time.Second {
			timeout = time.Second
		}
	}

	client := &dns.Client{Net: "udp", Timeout: timeout}
	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(qname), rrType)
	msg.RecursionDesired = true

	resp, _, err := client.ExchangeContext(ctx, msg, nameserver)
	if err != nil {
		return nil, err
	}
	if resp.Rcode != dns.RcodeSuccess {
		return nil, fmt.Errorf("dns: %s", dns.RcodeToString[resp.Rcode])
	}

	return collectDNSAnswers(resp, qt), nil
}

func collectDNSAnswers(resp *dns.Msg, qtype string) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}

	sections := [][]dns.RR{resp.Answer, resp.Extra}
	for _, sec := range sections {
		for _, rr := range sec {
			switch qtype {
			case "A":
				if v, ok := rr.(*dns.A); ok {
					add(v.A.String())
				}
			case "AAAA":
				if v, ok := rr.(*dns.AAAA); ok {
					add(v.AAAA.String())
				}
			case "CNAME":
				if v, ok := rr.(*dns.CNAME); ok {
					add(strings.TrimSuffix(v.Target, "."))
				}
			case "TXT":
				if v, ok := rr.(*dns.TXT); ok {
					add(strings.Join(v.Txt, ""))
				}
			case "NS":
				if v, ok := rr.(*dns.NS); ok {
					add(strings.TrimSuffix(v.Ns, "."))
				}
			}
		}
	}
	return out
}
