# API-Router Application Profiles, Routing Groups, and Agent Onboarding

## Purpose

Manage per-application routing profiles (`AppProfile`), multi-model and
routing-group bindings (`Models []string`), runtime group scheduling
(`smart_split`, `ordered`, `weighted`, `manual`, and rule matching), and the
streamlined `#view-agents` UI for one-click Loomy and WorkBuddy integration.

## Entry Points

- Configuration (`internal/routerconfig/`):
  - `apps.go`: `AppProfile`, `AppCloudRoute`, `AppLocalRoute`,
    `AppRoutingPolicy`, `ResolvedAppRoute`, `AppPreset`, `AppPresets`
  - `apps_validate.go`: `validateAndNormalizeApp`, `normalizeAndSyncAppModels`,
    `validateAppModelEntry`
  - `apps_save.go`: `Store.SaveAppContext`, `Store.DeleteAppContext`, `AppError`
  - `apps_resolve.go`: `Store.ResolveAppRouteContext`
- Runtime Group & Multi-Model Routing (`internal/routerservice/`):
  - `groups.go`: `handleRoutingGroups`, `handleRoutingGroupByID`,
    `handleRoutingGroupSwitch`
  - `group_routing.go`: `resolveRuntimeGroupRoute`, `executeGroupRoute`,
    `selectSmartSplitOrder`, `selectGroupCandidates`, `matchGroupRule`
  - `routing.go`: `executeRoute`, `resolveAppRoute`, `decideRoute`
  - `apps.go`: `handleApps`, `handleAppByID`, `handleAppCheck`
  - `hosts.go`: `handleHosts`, `handleHostsAuto`
- Frontend UI (`internal/routerservice/assets/`):
  - `profiles.js`: `#view-agents` two-row agent connection list (`loomy` and
    `workbuddy`), multi-select model/group picker (`.field.solo.ag-menu` +
    `.pop.am-pop`), and connection switch (`.lib-switch.ag-conn`)
  - `routing-groups.js` & `routing-groups-editor.js`: `#view-routing` routing
    group list, strategy switcher, and drawer editor

## Architecture

### `AppProfile` Data Model (`Enabled` & `Models []string`)

`routerconfig.AppProfile` is stored at `apps[id]` in `config.json`:

- `Enabled bool`: controls whether the application profile is active (defaults
  to `true` when omitted in JSON).
- `Models []string`: ordered list of target model/group references bound to the
  agent. Each entry supports three formats:
  - `<providerId>/<model>`: explicit cloud provider and model ID
  - `local/<engine>`: installed local inference engine (`local/llama_cpp` or
    `local/mlx`)
  - `group/<id>`: persisted or auto-merged routing group ID from
    `EffectiveGroups`
- **Primary Route Synchronization**: `normalizeAndSyncAppModels` deduplicates
  `Models` and automatically synchronizes `Models[0]` with `Mode`, `ProviderID`,
  `Cloud`, and `Local` so single-model callers and legacy fields stay consistent.
  When `Models[0]` is `group/<id>`, `resolvePrimarySelectionFromGroup` resolves
  the group's first concrete member to populate the fallback fields.
- **Non-Blocking Save & Optional Pricing**: Saving an `AppProfile` (`POST /api/apps`)
  does **not** require `referencePrice` JSON (when omitted or partial, zero/default
  pricing is normalized without error) and does **not** perform blocking
  upstream network probes.

### Routing Groups & Runtime Scheduling (`group_routing.go`)

`GET /api/routing-groups` returns `{ groups, effectiveGroups, catalogModels }`
combining saved groups (`Config.Groups`) with cross-provider auto-merged groups
(`EffectiveGroups`). `POST /api/routing-groups` creates/updates groups,
`DELETE /api/routing-groups/{id}` removes or hides (`Hidden: true`) auto groups,
and `POST /api/routing-groups/switch` switches the active manual member.

When a request arrives (either via `X-API-Router-App` or an explicit `model`
parameter matching `group/<id>` or a group name), `resolveRuntimeGroupRoute` and
`executeGroupRoute` resolve the candidate execution order:

1. **Rule Matching (`matchGroupRule`)**: Evaluates `Group.Rules` against request
   features (`has_sensitive` from redaction detection, `complexity` from
   `RuntimeJudge`, `tokens` estimate, `stream`, `has_tools`, `has_vision`,
   `system`, `model`). A matching rule routes directly to its `Use`/`Target`
   member (`local/*` or `<providerId>/<model>`).
2. **Strategies**:
   - `smart_split` (`StrategySmartSplit`, Cloud/Local Hybrid): Reuses
     `RuntimeJudge` (`judge.Assess`) and redaction scanning (`redactor.Inspect`).
     Simple requests (`ComplexitySimple`) without sensitive content prioritize
     `local/*` members first and fall back to cloud members if local fails;
     complex requests (`ComplexityComplex`) or requests containing sensitive
     terms (which are redacted before cloud dispatch) prioritize cloud members
     first and fall back to remaining group members on failure.
   - `ordered` (`order`): Tries active members sequentially in list order,
     falling back to the next member when an upstream attempt fails.
   - `weighted` (`weight`): Selects the initial member by weighted round-robin
     (`Group.Weights`), then falls back through remaining active members.
   - `manual`: Routes to `Group.Active` (switched via
     `POST /api/routing-groups/switch`), falling back to remaining active
     members if enabled.
3. **Multi-Model `AppProfile.Models` Fallback**: If an `AppProfile` binds
   multiple entries in `Models []string` without a single group wrapper,
   `resolveRuntimeGroupRoute` synthesizes an ordered candidate list across all
   selected groups and models.

### `#view-agents` Frontend (`profiles.js`)

The `#view-agents` view is dedicated to connecting the two supported desktop
coding agents (`loomy` and `workbuddy`):

- Renders two `.row.agent.conn` rows (`data-agent-id="loomy"` and
  `data-agent-id="workbuddy"`).
- Each row provides a multi-select dropdown (`.field.solo.ag-menu` +
  `.pop.am-pop`) with `Routing Groups` (`group/<id>`) pinned at the top,
  followed by cloud provider models (`<providerId>/<model>`) and local models
  (`local/<id>`).
- Toggling `.lib-switch.ag-conn` (or changing the model selection while
  connected) persists the `AppProfile` (`POST /api/apps`) and invokes
  `POST /api/hosts/auto` (`action: "connect"` or `"disconnect"`) to synchronize
  or restore the host configuration files without blocking upstream probes.
