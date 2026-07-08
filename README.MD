# ad-status-sender

A lightweight Go agent that runs **on every host**, collects local statuses (systemd units and Docker container groups), and **asynchronously posts** them to an ADCM-compatible endpoint. It also sends a host heartbeat. Supports **1→m** (one unit → many components) and **m↔n** (a component can be reported by multiple rules).

---

## Features

- SQLite-backed rules database with multiple sources: `rules.yaml`, `configs/legacy`, or ADCM.
- **ADCM shared hosts**: a machine shared into several clusters as host duplicates is fully covered — component status is posted per duplicate (ADCM fans out host status server-side, but not component status).
- **Hot reload** of rules:
  - `yaml`: `fsnotify` on `rules.yaml`
  - `legacy`: `fsnotify` on `legacy_dir` (services/docker/hosts)
  - `adcm`: periodic refresh (`rules_refresh_interval`)
- **Hot reload** of config on **SIGHUP**.
- **Status cache** + **forced re-send** at configurable interval (`force_send_after`, default **120s**) so the ADCM doesn’t mark entities as stale.
- **TLS/HTTPS**: custom CA, mTLS (client cert/key), `server_name` override, `insecure_skip_verify`.
- Token from YAML, **token file**, or **systemd credentials**.
- Worker pool, stable HTTP timeouts.

---

## Requirements

- Go **≥ 1.26**
- systemd **with D-Bus available** (for systemd checks)
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
# adcm_user: "admin"        # use with adcm_password instead of token
# adcm_password: "secret"

# rules source: yaml | legacy | adcm
rules_source: "yaml"

# sqlite database (rules source of truth)
rules_db: "/var/lib/ad-status-sender/rules.db"

# path to rules (auto hot-reload, only for rules_source: yaml)
rules_path: "/etc/ad-status-sender/rules.yaml"

# legacy_dir: "/usr/lib/adcm-status-checker"   # only for rules_source: legacy

# rules_refresh_interval: "60s"               # only for rules_source: adcm

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

### Example: legacy source

```yaml
adcm_url: "https://adcm.example.com"
host_id: 7
adcm_user: "admin"
adcm_password: "secret"

rules_source: "legacy"
legacy_dir: "/usr/lib/adcm-status-checker"
rules_db: "/var/lib/ad-status-sender/rules.db"
```

Legacy directory layout:

```
/usr/lib/adcm-status-checker/
  services/
    nginx.service          # contains component_id list
    hbase-regionserver.service
  docker/
    core                   # components + names/labels
  hosts/
    7                      # optional scope (filenames are host IDs)
```

Example contents:

`services/nginx.service`
```
501
502
```

`docker/core` (format A: two blocks with empty line separator)
```
601
602

db
cache
role=frontend
```

`docker/adcc` (format B: first line components, remaining lines selectors)
```
4
adcc_ui
adcc_backend
```

### Example: adcm source

```yaml
adcm_url: "https://adcm.example.com"
host_id: 7
adcm_user: "admin"
adcm_password: "secret"

rules_source: "adcm"
rules_refresh_interval: "60s"
rules_db: "/var/lib/ad-status-sender/rules.db"
```

### ADCM login/password

You can use `adcm_user` + `adcm_password` instead of a token. The agent will obtain a token on startup (if no token is provided) and refresh it on 401 responses.

Systemd credentials are also supported:
- `CREDENTIALS_DIRECTORY/adcm_user`
- `CREDENTIALS_DIRECTORY/adcm_password`

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

- **systemd**: queried via systemd **D-Bus** (`go-systemd/dbus`).  
  Returns **0** if the unit’s `ActiveState == "active"`, otherwise **1** (including “unit not found”).
  
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

## Rules sources

### 1) rules.yaml
Parsed into SQLite. Hot-reloaded on file change.

### 2) configs/legacy
Directory structure:
```
legacy/
  services/   # filename = unit, content = component_id list
  docker/     # component_id list + names/labels
  hosts/      # optional host scope (filenames are host IDs)
```
Changes in these folders trigger a sync into SQLite.

### 3) ADCM
Rules are built from ADCM service configs:
- for each component mapped to the host, the service config is inspected for
  `components.<component>.systemd.service_name`
- this becomes a systemd rule (`<service_name>.service` if missing suffix)

Refresh happens on a timer (`rules_refresh_interval`).

**Shared hosts (host duplicates).** A single machine can be shared into several
clusters as duplicate hosts, each carrying its own components. ADCM fans out
*host* status to duplicates server-side, but **not** component status, so the
agent enumerates the original host plus every duplicate and posts each
duplicate's component status under that duplicate's host id. Set `host_id` to the
**original** host id — duplicates are discovered automatically. A duplicate that
is deleted or forbidden (404/403) is skipped; a transient failure (5xx/network)
aborts the refresh so previously imported rules are preserved rather than wiped.

**Status authentication (two domains).** ADCM authenticates `/api/v2/*` (rule
import) with the rbac token, but `/status/api/v1/*` status POSTs with a separate
shared secret. In `adcm` mode the agent fetches that secret from
`POST /api/v2/adcm/status-checker-token/`, caches it, refetches on `401`, and
falls back to the configured token when the endpoint is absent (older ADCM).

---

## How it works

Each `interval`:
1) Expands `unit_glob` via systemd **D-Bus** (`ListUnitsByPatterns`) and checks each unit’s `ActiveState`.
2) Checks Docker groups (by `names` or `labels`).
3) Sends host heartbeat.

**Hot reload**:
- `rules.yaml` is reloaded via `fsnotify` when `rules_source: yaml`.
- `legacy_dir` is reloaded via `fsnotify` when `rules_source: legacy`.
- `rules_source: adcm` is refreshed by timer.
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

## Tests

Unit tests are fast and need no external services:

```bash
go test ./...
```

Live-ADCM integration tests boot a real ADCM + Postgres via testcontainers
(require Docker) and are gated behind the `adcm_integration` build tag, so the
default `go test ./...` never runs them:

```bash
# shared-host import contract against a released ADCM image
go test -tags adcm_integration -run TestSharedHost ./internal/adcmclient/

# status-checker-token / two-domain status auth — needs an ADCM image that
# ships the endpoint (feature/status-token-api build), selected via ADCM_TC_IMAGE
ADCM_TC_IMAGE=hub.adsw.io/adcm/adcm:feature_status-token-api \
  go test -tags adcm_integration -run TestStatus ./internal/adcmclient/
```

The `TestStatus*` tests skip when `ADCM_TC_IMAGE` is unset (released images lack
the endpoint). Provisioning bundles live under `internal/adcmclient/testdata/`.

---

## Tips

- For self-signed ADCM certs, use `tls.ca_file`.
- For mTLS, set both `tls.cert_file` and `tls.key_file`.
- If Docker label selection finds **no** containers, status is **1** (not OK).
- A component can appear in multiple rules — the agent will post all related statuses.

---
