# API-Router Service Lifecycle, Loopback Auth, and Management Console

## Purpose

Host the standalone local HTTP service (`cmd/api-router`), loopback listener,
management token authentication, runtime settings endpoints, routing-group and
host-connection endpoints, and the embedded single-page management console.

## Entry Points

- Binary entry: `cmd/api-router/main.go`
- Service lifecycle: `internal/routerservice/service.go` (`Config`, `Service`,
  `New`, `Start`, `Shutdown`, `Handler`, `BaseURL`, `AdminToken`)
- Listener & port binding: `internal/routerservice/listen.go`
- HTTP routes & middleware: `internal/routerservice/routes.go`
- Routing groups API: `internal/routerservice/groups.go`
  (`handleRoutingGroups`, `handleRoutingGroupByID`, `handleRoutingGroupSwitch`)
- Host auto-connection API: `internal/routerservice/hosts.go`
  (`handleHosts`, `handleHostsAuto`)
- Embedded web console (`internal/routerservice/assets/`):
  - `index.html`, `app.js`, `provider.js`, `profiles.js`,
    `routing-groups.js`, `routing-groups-editor.js`, `observations.js`,
    `models.js`, `redaction.js`, `usage.js`

## Architecture

### Startup Sequence and Automatic Loopback Port Selection

`Service.Start` initializes state and binds the loopback listener without killing
unrelated processes:

1. Ensures `APIRouterLayout` directories exist and initializes `config.json`,
   `credentials.json`, and `models/manifest.json`.
2. Loads runtime settings, redaction policy, local engine manager, usage store,
   and observation ring buffer.
3. Acquires `run/api-router.lock` via `routerlock.Manager`. If an identical
   instance is already live, startup aborts cleanly (`StatusAlreadyRunning`).
4. Binds the configured loopback port (`127.0.0.1:<port>`, default `18470`). If
    occupied by an unrelated listener, `listenLoopback` automatically binds an
   ephemeral loopback port, persists the newly bound port to `config.json`, and
   calls `routerhost.SyncConnectedHostPorts` so already connected Loomy and
   WorkBuddy configurations follow the new port.

### Loopback Isolation and Admin Token Authentication

- All requests must originate from a loopback remote address (`isLoopbackRemote`).
- Management API routes (`/api/*`) require either the session cookie or
  `Authorization: Bearer <adminToken>` / `X-API-Router-Admin-Token` matching the
  per-run random token (`s.adminToken`).
- Gateway data-plane routes (`/v1/chat/completions`, `/v1/responses`,
  `/v1/messages`, `/v1/models`, `/v1beta/models/*`) accept loopback agent traffic
  without requiring the management admin token.

### Management HTTP API Surface

- `GET /healthz`, `GET /api/status`: service identity, bound address, version,
  PID, and runtime status.
- `GET/PUT /api/runtime/settings`: read or update `RuntimeSettings`.
- `GET/POST /api/cloud-providers`, `DELETE /api/cloud-providers/{id}`,
  `POST /api/cloud-providers/{id}/check`, `POST /api/cloud-providers/{id}/models`:
  cloud provider CRUD, model discovery, and `localModels` listing.
- `GET/POST /api/routing-groups`, `PUT/DELETE /api/routing-groups/{id}`,
  `POST /api/routing-groups/switch`: list persisted and auto-merged routing
  groups (`EffectiveGroups`), create/update/delete or hide groups, and switch
  the active target for `manual` groups.
- `GET/POST /api/apps`, `DELETE /api/apps/{id}`, `POST /api/apps/{id}/check`:
  manage `AppProfile` records (`Enabled`, `Models []string`).
- `GET /api/hosts`, `POST /api/hosts/{host}/{action}`, `POST /api/hosts/auto`:
  inspect host status (`loomy`, `workbuddy`), run one-click multi-model /
  routing-group connection (`action: "connect"`) using `FastChecker`
  non-blocking validation, or restore host backups (`action: "disconnect"`).
- `GET /api/local-models`, `POST /api/local-models/{engine}/{action}`: local
  engine lifecycle (`llama_cpp`, `mlx`).
- `GET/PUT /api/redaction`, `POST /api/redaction/preview`: privacy redaction
  configuration and live preview.
- `GET /api/usage/summary`, `GET /api/observations`, `GET /api/observations/{id}`:
  token/cost rollups and per-request diagnostic ledger records.

### Management Console Views (`assets/`)

The single-page console (`index.html`) organizes workflows into six views:

- `#view-agents` (`profiles.js`): two-row agent onboarding (`loomy`,
  `workbuddy`) with `.field.solo.ag-menu` + `.pop.am-pop` multi-select model &
  routing-group dropdown and `.lib-switch.ag-conn` connection switch.
- `#view-routing` (`routing-groups.js`, `routing-groups-editor.js`): dedicated
  to routing group cards (persisted and `Auto` merged groups), strategy badges
  (`smart_split`, `ordered`, `weighted`, `manual`), member toggles, and the
  routing-group editor drawer (`#groupDrawer`).
- `#view-providers` (`provider.js`): cloud provider cards, preset/discovery
  model selector, and the bottom `#localProvidersSection` read-only configured
  local models section.
- `#view-local` (`models.js`): on-device engine (`llama_cpp`, `mlx`) download,
  start/stop, and health verification.
- `#view-redaction` (`redaction.js`): built-in PII detectors, custom sensitive
  words, and redaction preview.
- `#view-usage` (`usage.js`, `observations.js`): token/cost usage charts and
  breakdowns at the top, with the request observation ledger and diagnostic
  drawer (`#ledgerPane`, `#obsBody`, `#obsDrawer`) integrated at the bottom.
