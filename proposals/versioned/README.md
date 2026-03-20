# Proposal: Versioned

## Goal / Problem

Subnet nodes need to run multiple binary versions concurrently behind a single endpoint. Cosmovisor handles this for chain nodes but is fragile, single-version-only, and not production-grade.

We need a lightweight version manager that:
- Polls an oracle for which versions to run
- Downloads, verifies, and runs binaries on fixed ports
- Reverse-proxies traffic to the right version (keeps version routing internal, avoids pushing version awareness into nginx/ingress config)
- Works identically in containers and on bare metal

Two components: `oracled` (part of decentralized-api private API) and `versiond` (runs on each node, manages binary lifecycle).


## Proposal

### Oracle (`oracled`)

Part of decentralized-api's private API. Stores version config as JSON on disk. Optionally serves binaries directly. Versions can be added/removed via PUT/DELETE at runtime without restarting decentralized-api.

Endpoints:

```
GET  /versions                 -> version config (see format below)
PUT  /versions/{name}          -> add or update a version (admin)
DELETE /versions/{name}        -> remove a version (admin)
GET  /binaries/{name}          -> serve binary file (optional, for direct download)
```

Shares existing private API auth.

#### Version Config Format

```json
{
  "versions": [
    {
      "name": "v0.2.11",
      "binary": "https://github.com/gonka-ai/gonka/releases/download/release%2Fv0.2.11/decentralized-api-amd64.zip?checksum=sha256:e574c3d86189daf325cc7008603ee8e952efb028afda5bcd4a154dcd334192d4",
      "port": 9001
    },
    {
      "name": "v0.2.12",
      "binary": "https://oracle.internal/binaries/v0.2.12",
      "sha256": "a1b2c3d4e5f6...",
      "port": 9002
    }
  ]
}
```

Checksum resolution:
- If `sha256` field is present, use it
- Otherwise, parse `?checksum=sha256:...` from the URL query string
- If neither is present, reject the version config on PUT

This supports two download modes:
- External URL (GitHub releases, S3, etc.) with checksum in URL or field
- Direct from oracle (`/binaries/{name}`) with checksum in field

Binary format: zip archive containing a single executable. versiond extracts and marks executable after download.


### Version Manager (`versiond`)

Single Go binary. Manages child processes + built-in reverse proxy.

Responsibilities:
- Poll oracle `GET /versions` every 30s (configurable via `VERSIOND_POLL_INTERVAL`)
- Download and verify new binaries
- Start/stop child processes
- Reverse-proxy incoming traffic with streaming support
- Forward signals to children on shutdown

#### Directory Layout

```
/opt/versiond/
  bin/
    v0.2.11/subnet        # extracted binary
    v0.2.12/subnet
  data/
    v0.2.11/              # version-specific data dir, passed as --data-dir to child
    v0.2.12/
```

#### Reconciliation Loop

Every poll cycle, versiond compares oracle state against local state:

```
for each version in oracle response:
  if binary not downloaded:
    download, verify sha256, extract to bin/{version}/
    if hash mismatch: log error, skip, keep existing versions running
  if process not running:
    start process on configured port with data dir

for each running process not in oracle response:
  send SIGTERM, wait 5s, SIGKILL
```

Hash verification failure never stops existing versions. The node continues serving with whatever versions are healthy.

#### Reverse Proxy

versiond listens on a single port (default :8080, configurable via `VERSIOND_LISTEN_ADDR`). Routes by path prefix:

```
/v0.2.11/*  ->  localhost:9001
/v0.2.12/*  ->  localhost:9002
```

The prefix is stripped before forwarding. A request to `versiond:8080/v0.2.11/chat/completions` hits `localhost:9001/chat/completions`.

Why internal proxy instead of external nginx routing: version set changes dynamically based on oracle state. Pushing version awareness into nginx/ingress config means syncing two systems. Keeping routing inside versiond means one component owns the full lifecycle -- download, run, route, stop. External infra only sees a single port.

Streaming and SSE: `httputil.ReverseProxy` with `FlushInterval: -1` flushes every write immediately. This is required for `/chat/completions` with `stream: true` (SSE). No buffering, no additional config. Works out of the box for SSE and chunked transfer encoding.

Routing table updates: the poll goroutine builds a new immutable route map and swaps it via `atomic.Value`. Request handlers load the current map with zero lock contention. In-flight requests continue on the old map, new requests use the updated one.

`GET /healthz` returns aggregate health: list of versions, their ports, and process status (running/stopped/starting).

#### Signal Handling and PID 1

In containers, use `tini` as PID 1 for zombie reaping. versiond handles signal forwarding to children:

```dockerfile
FROM alpine:3.20
RUN apk add --no-cache tini
COPY versiond /usr/bin/versiond
ENTRYPOINT ["tini", "--"]
CMD ["versiond"]
```

On SIGTERM/SIGINT:
1. Stop accepting new connections
2. Send SIGTERM to all children
3. Wait up to 10s for graceful shutdown
4. SIGKILL remaining children
5. Exit

#### Logging

Child processes tag their own log output via `SUBNET_LOG_PREFIX` env var set by versiond (e.g. `SUBNET_LOG_PREFIX=v0.2.11`). The subnet binary prepends this to each log line. Zero overhead in versiond -- child stdout/stderr connect directly to versiond's stdout/stderr. No interleaving issues because each line is already tagged at the source.

#### Configuration

All via environment variables:

| Variable | Default | Description |
|---|---|---|
| VERSIOND_ORACLE_URL | (required) | Oracle endpoint, e.g. `https://oracle.internal/versions` |
| VERSIOND_LISTEN_ADDR | :8080 | Proxy listen address |
| VERSIOND_POLL_INTERVAL | 30s | Oracle poll interval |
| VERSIOND_BIN_DIR | /opt/versiond/bin | Binary storage |
| VERSIOND_DATA_DIR | /opt/versiond/data | Per-version data directories |
| VERSIOND_BINARY_NAME | subnet | Expected binary name inside zip |


## Implementation

`versiond` lives in `/subnet/`. `oracled` is part of decentralized-api's private API.

```
/subnet/
  cmd/
    versiond/main.go

/decentralized-api/
  internal/
    oracled/
      handler.go       -- HTTP handlers, registers on private API router
      store.go         -- JSON file read/write with flock
```

### oracled

Part of decentralized-api's private API. Enabled via config flag (`oracled.enabled: true`). Shares existing private API auth.

- `internal/oracled/handler.go` -- HTTP handlers, config CRUD, registers routes on the private API router
- `internal/oracled/store.go` -- JSON file read/write with flock
- Config file path from `oracled.config_path` in api config (default: `/etc/oracled/versions.json`)
- Binary storage dir from `oracled.binary_dir` in api config (default: `/var/lib/oracled/binaries/`)
- Versions can be added/removed via PUT/DELETE at runtime without restarting decentralized-api

### versiond

- `internal/versiond/manager.go` -- reconciliation loop, process lifecycle
- `internal/versiond/downloader.go` -- download, checksum verify, extract
- `internal/versiond/proxy.go` -- reverse proxy with atomic route table, FlushInterval: -1 for SSE
- `internal/versiond/health.go` -- health endpoint

### Process management

Child processes are managed via `os/exec`. Each version gets a goroutine that:
1. Starts the binary with `--data-dir /opt/versiond/data/{version} --port {port}`
2. Sets `SUBNET_LOG_PREFIX={version}` env var on the child
3. Monitors for exit
4. Restarts on crash (exponential backoff: 1s, 2s, 4s, ... max 60s)
5. Resets backoff on 60s of clean running

### Build

```makefile
# in /subnet/Makefile
versiond:
	go build -o build/versiond ./cmd/versiond
```

### Bare metal usage

```ini
# /etc/systemd/system/versiond.service
[Unit]
Description=Subnet Version Manager

[Service]
ExecStart=/usr/bin/versiond
Environment=VERSIOND_ORACLE_URL=https://oracle.internal/versions
Restart=always

[Install]
WantedBy=multi-user.target
```
