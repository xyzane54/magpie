# API-Router Request Observation, Usage Rollups, and Routing Console Split

## Purpose

Capture privacy-safe per-request diagnostic records (`routerobs`) and persistent
token/cost usage rollups (`routerusage`) across cloud, local, and routing-group
executions, while separating the `#view-routing` routing-group console from the
`#view-usage` (`#ledgerPane`) request observation ledger.

## Entry Points

- Observation ring buffer: `internal/routerobs/store.go` (`Store`, `NewStore`,
  `Record`, `Filter`, `Summary`, `Detail`)
- Usage ledger & rollups: `internal/routerusage/store.go` (`Store`, `NewStore`,
  `Event`, `Summary`, `Filter`)
- HTTP handlers (`internal/routerservice/`):
  - `observations.go`: `handleObservations`, `handleObservationByID`
  - `usage.go`: `handleUsageSummary`
  - `groups.go`: `handleRoutingGroups` (`/api/routing-groups`),
    `handleRoutingGroupByID` (`/api/routing-groups/{id}`),
    `handleRoutingGroupSwitch` (`/api/routing-groups/switch`)
  - `hosts.go`: `handleHostsAuto` (`/api/hosts/auto`)
- Frontend UI (`internal/routerservice/assets/`):
  - `usage.js` & `observations.js`: `#view-usage` usage metrics, charts, and
    bottom `#ledgerPane` request observation table + `#obsDrawer` diagnostic
    drawer
  - `routing-groups.js` & `routing-groups-editor.js`: `#view-routing` routing
    group cards, strategy switcher, and `#groupDrawer` editor

## Architecture

### Per-Request Observation Ring Buffer (`routerobs.Store`)

`routerobs.Store` maintains a bounded in-memory ring buffer (`DiagnosticRetention`
from `RuntimeSettings`, default `100`, range `[10, 1000]`) of `Record` entries:

- Captures routing metadata: `ID`, `RequestID`, `Timestamp`, `Protocol`
  (`openai_chat`, `openai_responses`, `anthropic_messages`, `gemini_native`),
  `AppID`, `AppName`, `RequestedModel`, `FinalModel`, `ProviderID`, `Engine`,
  `RequestedMode`, `InitialTarget`, `FinalTarget` (`cloud` or `local`),
  `JudgeSource`, `Complexity`, `JudgeReason`, `FallbackPolicy`, `FallbackUsed`,
  `FallbackReason`, `RedactionAction`, `RedactionCount`, `RedactionCategories`,
  `RestoreApplied`, `RestoreCount`, `StatusCode`, `LatencyMs`, `Stream`,
  `ErrorCode`, `ErrorMessage`, `AttemptCount`, and per-attempt `Attempts`.
- Stores **no raw prompt or response text**: only structural metadata, redaction
  counts/categories, and safe diagnostic error summaries are retained.
- When `RuntimeSettings.DiagnosticRetention` is reduced via
  `PUT /api/runtime/settings`, `Store.SetLimit` trims the oldest entries
  immediately.

### Persistent Token & Cost Usage Store (`routerusage.Store`)

`routerusage.Store` persists usage events to `data/usage/events.jsonl` and
computes aggregated `Summary` views (`GET /api/usage/summary`):

- Records token counts (`InputTokens`, `OutputTokens`, `TotalTokens`),
  estimated or reported cost (`Cost`, `Currency`, `PriceSource`), target type
  (`cloud` vs `local`), provider/engine, model, application, and status.
- Supports filtering by time range (`window`: `24h`, `7d`, `30d`, `all`),
  `appId`, `target`, and `providerId`.
- Aggregates totals, cloud-vs-local offload ratios, time-series buckets, and
  per-app / per-model / per-provider breakdowns.

### Separation of `#view-routing` and `#view-usage` (`#ledgerPane`)

The management console separates routing configuration from runtime request
telemetry:

1. **Routing Groups in `#view-routing` (`routing-groups.js`, `routing-groups-editor.js`)**:
   - `#view-routing` is dedicated exclusively to managing routing groups backed
     by `GET/POST /api/routing-groups`, `PUT/DELETE /api/routing-groups/{id}`,
     and `POST /api/routing-groups/switch`.
   - Displays persisted and auto-merged (`Auto: true`) groups, strategy
     selectors (`smart_split`, `ordered`, `weighted`, `manual`), member toggles,
     and the `#groupDrawer` group editor.
   - Agents bind to these routing groups (`group/<id>`) or individual models via
     `#view-agents` (`POST /api/apps` and `POST /api/hosts/auto`).
2. **Observation Ledger in `#view-usage` (`observations.js`, `usage.js`)**:
   - The request observation table (`#obsBody`), filter controls (`#obsSearch`,
     `#obsTargetFilter`, `#obsStatusFilter`, `#obsFallbackFilter`), refresh
     button (`#refreshObsBtn`), and slide-out diagnostic drawer (`#obsDrawer`)
     reside inside `#ledgerPane` at the bottom of `#view-usage`.
   - Selecting any row in `#ledgerPane` fetches `GET /api/observations/{id}` and
     opens `#obsDrawer` showing route decision details, redaction/restore
     counters, token/cost metrics, and the per-attempt execution timeline.

## Invariants

- Raw prompts, completions, and secret credentials are never stored in
  `routerobs.Store` or `routerusage.Store`.
- Observation recording is non-blocking on the gateway hot path and honors
  `RuntimeSettings.DiagnosticRetention`.
