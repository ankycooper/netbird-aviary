# netbird-aviary 🪺

[![CI](https://github.com/ankycooper/netbird-aviary/actions/workflows/ci.yml/badge.svg)](https://github.com/ankycooper/netbird-aviary/actions/workflows/ci.yml)
[![Build & publish](https://github.com/ankycooper/netbird-aviary/actions/workflows/build.yml/badge.svg)](https://github.com/ankycooper/netbird-aviary/actions/workflows/build.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

Declarative **NetBird Services** from Docker labels. Watches the Docker socket and reconciles labeled containers into reverse-proxy Services on a self-hosted NetBird (v0.72.x) management server — optionally embedding a netbird agent and registering itself as a routing peer so your services don't need to publish host ports at all.

```
        docker labels                 NetBird API
container────────────►  aviary  ────────────────►  NetBird Services
                       (watches            (creates / updates / disables)
                       /var/run/docker.sock)
```

## What it does

- **Declarative Services from labels.** Add `netbird.enable=true` plus a handful of labels to any container — aviary creates the matching NetBird Service, keeps it in sync, and disables it when the container stops.
- **Two routing modes.**
  - `host` — service targets `<docker-host-IP>:<published-port>`. Zero extra setup if you already have a NetBird peer on the host.
  - `network` — service targets `<container-IP-on-docker-network>:<container-port>`. No published ports needed. The controller embeds a netbird agent, registers as a routing peer, and provisions the NetBird Network + Resource + Router automatically.
- **Friendly-name lookups.** Reference NetBird peers / resources / networks / groups by name; the controller resolves them to IDs via the API at startup.
- **Full Service feature coverage.** Auth (SSO, password, PIN, HTTP headers), access control (CrowdSec, allow/block CIDRs, country lists), advanced (pass-host-header, rewrite-redirects), custom upstream headers, request timeouts — all driven by labels.
- **Ephemeral services.** Opt into delete-on-stop with `netbird.ephemeral=true` for short-lived workloads.
- **Idempotent.** Safe to restart. Reconciliation always derives from live Docker + NetBird state — no local state file.

## Quick start

The image is published to `ghcr.io/ankycooper/netbird-aviary` — multi-arch (`linux/amd64`, `linux/arm64`), so it runs on x86, Apple Silicon, and Raspberry Pi.

```sh
# 1. Get the sample compose + env
curl -O https://raw.githubusercontent.com/ankycooper/netbird-aviary/main/docker-compose.example.yml
curl -O https://raw.githubusercontent.com/ankycooper/netbird-aviary/main/.env.example

# 2. Make local copies (gitignored — yours to edit freely)
cp docker-compose.example.yml docker-compose.yml
cp .env.example .env
$EDITOR docker-compose.yml .env

# 3. Pull + bring it up
docker compose pull
docker compose up -d
docker compose logs -f aviary
```

That's it. Aviary watches the docker socket; any new container with `netbird.enable=true` becomes a NetBird Service.

## Configuration

Env vars on the `aviary` container. Required ones in **bold**.

| Env var | What it does |
| --- | --- |
| **`NETBIRD_API_URL`** | e.g. `https://netbird.example.com` (no `/api`) |
| **`NETBIRD_API_TOKEN`** | NetBird PAT (starts with `nbp_`). Generate in NetBird UI → API tokens. |
| `NETBIRD_DEFAULT_TARGET_TYPE` | `subnet` (default), `peer`, `resource`, or `cluster` |
| `NETBIRD_DEFAULT_TARGET_ID` | Explicit ID of the target. Takes precedence over `_NAME`. |
| `NETBIRD_DEFAULT_TARGET_NAME` | Friendly target name (e.g. `Main-net`). Resolved via the NetBird API. |
| `NETBIRD_DEFAULT_NETWORK_ID` | When `target_type=subnet`/`resource`, the Network ID containing the target. |
| `NETBIRD_DEFAULT_NETWORK_NAME` | Friendly Network name (e.g. `Home`). |
| `NETBIRD_DEFAULT_HOST` | Default `target.host` in `host` mode — the docker host's IP on the NetBird overlay. |
| `NETBIRD_DEFAULT_PROTOCOL` | `http` by default. |
| `NETBIRD_DEFAULT_MODE` | NetBird Service mode: `http` (L7, default), `tcp`, `udp`, `tls`. Private services must use `http`. |
| `NETBIRD_DEFAULT_DOMAIN` | Bare-subdomain labels (`netbird.domain=foo`) expand to `foo.<this>`. |
| `NETBIRD_TARGET_MODE` | `host` (default) or `network`. See [Modes](#modes). |
| `NETBIRD_SETUP_KEY` | NetBird setup key. **Set this to enable network mode** (it triggers the embedded-agent bootstrap). |
| `NETBIRD_NETWORK_NAME` | Name aviary uses for the NetBird Network it auto-creates (default: docker network name). |
| `NETBIRD_DOCKER_NETWORK` | Which docker network to use if aviary is attached to several. Auto-detected if only one user-defined network. |
| `NETBIRD_PEER_HOSTNAME` | Hostname the embedded agent registers as (default: `aviary-<docker-network>`). |
| `NETBIRD_AGENT_MANAGEMENT_URL` | Override the management URL the embedded agent connects to (defaults to `NETBIRD_API_URL`). Useful when the controller talks to NetBird over an internal address but the agent should use a public one. |
| `NETBIRD_DISABLE_AGENT` | `true` to skip starting the embedded agent — for users who already run a netbird peer on the docker network. |
| `LABEL_PREFIX` | `netbird` by default. |
| `RECONCILE_INTERVAL` | Periodic resync. `5m` default. |
| `HTTP_TIMEOUT` | NetBird API timeout. `30s` default. |
| `DRY_RUN` | `true` logs payloads without touching the API. |
| `LOG_LEVEL` | `debug`, `info` (default), `warn`, `error`. |

## Label schema

`netbird.enable: "true"` is the master switch — without it the container is ignored.

### Single-service shorthand (one service per container)

```yaml
labels:
  netbird.enable: "true"
  netbird.name: "my-svc"                       # optional; defaults to container name
  netbird.domain: "test"                       # bare subdomain → test.<NETBIRD_DEFAULT_DOMAIN>
  netbird.mode: "http"                         # http | tcp | udp | tls (default from env)
  netbird.listen_port: "443"                   # optional, server may auto-assign
  netbird.proxy_cluster: "<cluster-id>"        # optional
  netbird.ephemeral: "false"                   # if "true", DELETE the service on container stop
  netbird.target_mode: "host"                  # host | network (overrides global)
  netbird.private: "true"                      # NetBird-only access
  netbird.access_groups: "admins,devs"         # groups granted access when private=true

  # Target endpoint
  netbird.port: "80"                           # in host mode: container port, auto-resolved to host port
                                               # in network mode: container's internal port (no resolution)
  netbird.host: "10.0.100.16"                  # optional; defaults from env
  netbird.protocol: "http"                     # default from env

  # Target reference (any of these — id wins over name)
  netbird.target.id:   "<resource-id>"
  netbird.target.name: "Main-net"
  netbird.network:     "Home"                  # network containing the resource
  netbird.network.id:  "<network-id>"

  # Target options
  netbird.target.path: "/api"
  netbird.target.skip_tls_verify: "false"
  netbird.target.request_timeout: "30s"
  netbird.target.path_rewrite: "/v1"
  netbird.target.proxy_protocol: "false"
  netbird.target.session_idle_timeout: "5m"
  netbird.target.direct_upstream: "false"
  netbird.target.headers.X-Forwarded-Host: "myapp.example.com"
  netbird.target.headers.X-Custom: "value"

  # Authentication (any combination)
  netbird.auth.password: "${MY_PASSWORD}"
  netbird.auth.pin: "1234"
  netbird.auth.sso: "true"
  netbird.auth.sso_groups: "admins,engineers"
  netbird.auth.link: "true"
  netbird.auth.header.X-API-Key: "${MY_API_KEY}"

  # Access control
  netbird.access.crowdsec: "enforce"           # off | enforce | observe
  netbird.access.allow_cidrs: "203.0.113.4/32,10.0.0.0/8"
  netbird.access.block_cidrs: "192.168.0.0/16"
  netbird.access.allow_countries: "US,CA"
  netbird.access.block_countries: "RU,CN"

  # Advanced
  netbird.advanced.pass_host_header: "true"
  netbird.advanced.rewrite_redirects: "true"
```

### Multi-service form (multiple services per container)

Namespace each one with `netbird.services.<svc>.*`:

```yaml
labels:
  netbird.enable: "true"

  netbird.services.web.domain: "site.example.com"
  netbird.services.web.port: "80"

  netbird.services.api.domain: "api.example.com"
  netbird.services.api.port: "443"
  netbird.services.api.target.protocol: "https"
  netbird.services.api.auth.sso: "true"
```

Service names default to `<container-name>` (single form) or `<container-name>-<svc>` (multi form). Override with `netbird.[services.<svc>.]name`.

## Modes

The controller has two ways of pointing NetBird at your backends. Pick per-service with `netbird.target_mode`, or set the default with `NETBIRD_TARGET_MODE`.

### `host` mode (default)

The NetBird Service targets the **docker host's IP + the published port** mapped to the container.

- **Use when:** your containers already publish ports and you have a NetBird peer running on the docker host.
- **Setup:** `NETBIRD_DEFAULT_HOST=<host overlay IP>`, plus `NETBIRD_DEFAULT_TARGET_NAME` (or `_ID`) pointing at the peer/resource that fronts that IP.
- **Aviary acts as:** a thin HTTP client — no agent, no caps, no special networking.
- **Port resolution:** `netbird.port: "80"` is treated as the container's *internal* port and auto-translated to the matching host port via Docker's port bindings. So `ports: ["3001:80"]` + `netbird.port: "80"` → NetBird Service points at `<host>:3001`.

### `network` mode

The NetBird Service targets the **container's IP on a shared docker network + its internal port**. No host port publishing needed.

- **Use when:** you want services reachable *only* over the NetBird overlay, not on the host network.
- **Setup:** set `NETBIRD_SETUP_KEY` (one-time secret from NetBird UI → Setup Keys). Attach aviary and your backend containers to the same user-defined docker network. The bootstrap then:
  1. Embeds a netbird agent and registers as peer `aviary-<docker-network>`.
  2. Creates a NetBird Network (default name `Docker`).
  3. Creates a Resource for the docker subnet (e.g. `172.30.0.0/16`).
  4. Sets itself as the Router for that Resource.
- **Requires:** `cap_add: [NET_ADMIN]`, `devices: ["/dev/net/tun"]`, and a persistent volume on `/var/lib/netbird` (already in the sample compose).
- **Port resolution:** `netbird.port: "80"` is the literal container port — no host-binding involved.

The two modes can coexist in one stack. If `NETBIRD_SETUP_KEY` is set, network-mode bootstrap runs regardless of the global default — so you can run with `NETBIRD_TARGET_MODE=host` and opt single containers into network mode via the per-service `netbird.target_mode: "network"` label.

See `docker-compose.example.yml` for both patterns side-by-side.

> **Post-provisioning step:** the auto-created NetBird Resource has no group assignments by default. Open NetBird UI → Networks → your network → the new Resource → attach the groups you want to grant access to, then make sure a policy permits those groups to reach the Resource. Without this, the Resource exists but no peer is authorized to use it.

## Reconciliation model

- **Source of truth:** container labels on the Docker host aviary watches.
- **On start:** lists labeled containers, lists NetBird services, syncs.
- **On Docker events** (`start | stop | die | destroy | update`): triggers a full reconcile.
- **Periodic resync:** every `RECONCILE_INTERVAL` (default 5m) catches missed events / out-of-band edits.
- **Stopped container** → `enabled: false` on the matching Service (PUT). Not deleted, so a restart restores it instantly.
  - **Override:** `netbird.ephemeral=true` makes the Service get **deleted** on stop. Used for short-lived workloads where leftover disabled Services would clutter the UI. The next start re-creates it from scratch.
- **Removed container** (no longer in `docker ps -a`): left alone. NetBird Service stays in last state.
- **Naming join key:** the NetBird `service.name`. Renaming `netbird.name` orphans the old Service.
- **No state file:** every reconcile derives from live Docker + NetBird state. Restarts are safe.

## NetBird API endpoints used

Aviary talks to these (all under your management URL):

| Method | Path | Purpose |
| --- | --- | --- |
| `GET`/`POST`/`PUT`/`DELETE` | `/api/reverse-proxies/services` | The Services this controller manages |
| `GET`/`POST` | `/api/networks`, `/api/networks/{id}/resources`, `/api/networks/{id}/routers` | Network-mode auto-provisioning |
| `GET` | `/api/peers`, `/api/groups`, `/api/reverse-proxies/clusters` | Friendly-name resolution |

## Limitations / by design

- **One target per Service.** The API allows multiple; if you need them, use the multi-service form (one Service per target) or send a PR.
- **No automatic policy creation.** Aviary creates the Resource but not the Policy that grants access to it — you do that step once in the UI per resource group.
- **Auto-detect picks one docker network.** If aviary is attached to several user-defined networks in network mode, set `NETBIRD_DOCKER_NETWORK` to disambiguate.
- **Secret label changes don't propagate.** Aviary's diff ignores `password`/`pin`/`header_auths[].value` (because NetBird redacts them in responses, so we can't tell what's actually stored). Change a secret label → restart the container, or temporarily flip `netbird.enable=false/true`, to force a fresh PUT.
- **Removed containers don't auto-delete Services.** Use `netbird.ephemeral=true` instead, or delete manually in the UI.

## Development

Module path: `github.com/ankycooper/netbird-aviary`. Standard Go layout under `internal/`. To build locally:

```sh
docker build -t netbird-aviary:dev .
```

Or wire a local build into your compose by setting `build: .` on the `aviary` service.

## License

[MIT](LICENSE). NetBird itself is BSD-3-Clause; this project is independent of and not affiliated with NetBird GmbH.
