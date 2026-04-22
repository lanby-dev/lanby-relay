# lanby-relay

The relay agent for [Lanby](https://lanby.dev) — runs in your network and reports check results to the Lanby platform.

Deploy a relay when you need to monitor internal services that aren't reachable from the internet.

## How it works

1. Start the relay — it registers itself with Lanby and prints a claim code
2. Enter the claim code in the Lanby console under **Relays**
3. Assign the relay to monitors in the monitor creation form

The relay polls for its configuration, runs checks on schedule, and reports results back to the platform. Its identity is persisted locally so it survives restarts without needing to be re-claimed.

## Running with Docker

```sh
docker run -d \
  --name lanby-relay \
  --restart unless-stopped \
  -v lanby-relay-data:/data \
  -e IDENTITY_PATH=/data/identity.json \
  ghcr.io/lanby-dev/lanby-relay:latest
```

## Running with Docker Compose

```yaml
services:
  lanby-relay:
    image: ghcr.io/lanby-dev/lanby-relay:latest
    restart: unless-stopped
    environment:
      IDENTITY_PATH: /data/identity.json
    volumes:
      - relay-data:/data

volumes:
  relay-data:
```

## Configuration

All configuration is via environment variables. Defaults work for most deployments.

| Variable | Default | Description |
|---|---|---|
| `PLATFORM_URL` | `https://api.lanby.dev` | Lanby API base URL |
| `IDENTITY_PATH` | `./identity.json` | Path to persist relay identity |
| `AGENT_VERSION` | `0.1.0` | Version string reported to the platform |
| `CONFIG_POLL_SECONDS` | `30` | How often to poll for config changes |
| `ALLOWED_PROBE_HOSTS` | *(unset — all hosts permitted)* | Comma-separated allowlist of hosts the relay may probe. See below. |

### ALLOWED_PROBE_HOSTS

When set, the relay only executes probes whose target matches at least one entry. Targets that don't match are skipped and logged as a warning. If unset, all targets are permitted.

Supported pattern forms:

| Pattern | Example | Matches |
|---|---|---|
| Exact hostname or IP | `mynas.local` | `mynas.local` only |
| Wildcard subdomain | `*.home.arpa` | `foo.home.arpa`, not `home.arpa` itself |
| CIDR block | `192.168.0.0/16` | IP-literal targets in that range |

Multiple patterns are comma-separated:

```yaml
environment:
  ALLOWED_PROBE_HOSTS: "*.local,*.home.arpa,192.168.0.0/16,10.0.0.0/8"
```

The relay refuses to start if any entry is malformed (e.g. an invalid CIDR). CIDRs match only IP-literal targets — use hostname patterns for named hosts.

## Supported check types

- HTTP/HTTPS
- TCP
- Ping (ICMP)
- DNS
- gRPC health

## License

MIT
