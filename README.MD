# ad-status-sender

A lightweight Go agent that runs **on every host**, collects local statuses (systemd units and Docker container groups), and **asynchronously posts** them to an ADCM-compatible endpoint. It also sends a host heartbeat. Supports **1→m** (one unit → many components) and **m↔n** (a component can be reported by multiple rules).

---

## Features

- Single `config.yaml` + single `rules.yaml`.
- **Hot reload** of rules via `fsnotify` (no restart or signals).
- **Hot reload** of config on **SIGHUP**.
- **Status cache** + **forced re-send** at configurable interval (`force_send_after`, default **120s**) so the ADCM doesn’t mark entities as stale.
- **TLS/HTTPS**: custom CA, mTLS (client cert/key), `server_name` override, `insecure_skip_verify`.
- Token from YAML, **token file**, or **systemd credentials**.
- Worker pool, stable HTTP timeouts.

---

## Requirements

- Go **≥ 1.24**
- systemd (for systemd checks)
- Docker (for Docker checks)

---

## Run (manually)

```bash
ad-status-sender -config /etc/ad-status-sender/config.yaml
```

---

## Configuration (`config.yaml`)

```yaml
adcm_url: "https://adcm.example.com"
host_id: 101

# token (prefer file or systemd-credentials)
token_file: "/etc/secure/adcm.token"

# path to rules (auto hot-reload)
rules_path: "/etc/ad-status-sender/rules.yaml"

# intervals & timeouts
interval: "5s"            # how often to probe local system
http_timeout: "5s"        # HTTP client timeout
force_send_after: "120s"  # re-send even if unchanged

# performance
concurrency: 0            # 0 = NumCPU

# log server response bodies (useful for debugging)
log_bodies: false

# TLS (only if adcm_url is https://)
tls:
  ca_file: "/etc/pki/ca-trust/source/anchors/adcm-root.pem"  # optional
  cert_file: "/etc/ad-status-sender/client.crt"               # optional (mTLS)
  key_file: "/etc/ad-status-sender/client.key"                # optional (mTLS)
  server_name: "adcm.internal"                                # optional (SNI/verify override)
  insecure_skip_verify: false                                 
```

> You can put the token directly in YAML (`token:`), but **using `token_file` or systemd credentials is recommended**.

---

## Rules (`rules.yaml`)

Describe what to check locally and which **component_id(s)** to report.

```yaml
systemd:
  - unit: "nginx.service"
    components: ["501","502"]                 # one unit → many components
  - unit_glob: "hbase-regionserver@*.service" # glob expansion
    components: ["202","203"]

docker:
  - name: "webstack"                          # group name (arbitrary)
    components: ["201","202"]
    containers:
      names: ["nginx","redis:cluster-a"]      # explicit container names

  - name: "etl-by-labels"
    components: ["301"]
    containers:
      labels: ["app=etl","stage=prod"]        # label selector
```

### Status semantics

- **systemd**: runs `systemctl is-active -q <unit>`  
  → **0** if active; otherwise the **non-zero exit code** from systemctl.

- **docker**:
  - `names`: **0** if **all** listed containers are `running`, else **1**.
  - `labels`: **0** if it finds **at least one** container by labels **and all found** are `running`, else **1**.

- **host heartbeat**: POST `/status/api/v1/host/{host_id}/` with `{"status":0}` each cycle.

### Guaranteed resends

The agent caches last sent status per key:
- `host:{host_id}`
- `comp:{host_id}:{component_id}`

It sends **only if**:
- status **changed**, or
- at least `force_send_after` elapsed since last send (default 120s).

This prevents the receiver from marking entities stale when nothing changes.

---

## How it works

Each `interval`:
1) Expands `unit_glob` via `systemctl list-units` and checks all matched units.
2) Checks Docker groups (by `names` or `labels`).
3) Sends host heartbeat.

**Hot reload**:
- `rules.yaml` is automatically reloaded via `fsnotify`.
- `config.yaml` is reloaded on **SIGHUP** (e.g., `systemctl reload ad-status-sender`).

HTTP client:
- Connection pool, timeouts, TLS 1.2+, optional custom CA & mTLS.

---

## Logging

Structured logs via `log/slog` to stdout.  
If `log_bodies: true`, the agent logs server response bodies (useful for debugging).

---

## Build

Local (snapshot) build:
```bash
goreleaser release --snapshot --clean
```

---

## Lint

We target strict settings:

```bash
golangci-lint run
```

---

## Tips

- For self-signed ADCM certs, use `tls.ca_file`.
- For mTLS, set both `tls.cert_file` and `tls.key_file`.
- If Docker label selection finds **no** containers, status is **1** (not OK).
- A component can appear in multiple rules — the agent will post all related statuses.

---
