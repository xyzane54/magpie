# API-Router Storage and Runtime State

## Purpose

Own the filesystem layout, configuration documents, credentials store, runtime
settings, routing groups, local model manifest, process lock, and atomic file
persistence used by the standalone API-router service.

## Entry Points

- `internal/dirs/apirouter.go`: `APIRouterLayout`, `NewAPIRouterLayout`,
  `NewAPIRouterLayoutWithHome`
- `internal/routerconfig/config.go`: `SchemaVersion`, `Config`, `Document`,
  `Store`, `NewStore`, `Initialize`, `Load`, `Save`, `LoadConfig`
- `internal/routerconfig/runtime.go`: `RuntimeSettings`, `DefaultRuntimeSettings`,
  `RuntimeSettings.Validate`, `Store.RuntimeSettings`, `Store.SaveRuntimeSettings`
- `internal/routerconfig/providers.go`, `providers_validate.go`,
  `providers_write.go`: `CloudProvider`, `ProviderSecret`,
  `Store.ListCloudProviders`, `Store.SaveCloudProvider`,
  `Store.SaveProviderDiscoveredModels`, `Store.DeleteCloudProvider`
- `internal/routerconfig/groups.go`, `groups_match.go`, `groups_effort.go`,
  `groups_validate.go`, `groups_normalize.go`, `groups_io.go`,
  `groups_store.go`, `groups_store_methods.go`: `Group`, `GroupRule`,
  `EffectiveGroups`, `ResolveGroups`, `ValidateAndNormalizeGroups`,
  `Store.ListGroups`, `Store.SaveGroups`, `Store.EffectiveGroups`
- `internal/routerconfig/apps.go`, `apps_validate.go`, `apps_save.go`,
  `apps_resolve.go`: `AppProfile`, `ResolvedAppRoute`, `Store.ListApps`,
  `Store.SaveApp`, `Store.DeleteApp`, `Store.ResolveAppRoute`
- `internal/routerconfig/models.go`: `ModelManifest`, `ModelRecord`,
  `LoadModelManifest`, `SaveModelManifest`
- `internal/routerlock/lock.go`: `Record`, `Result`, `Manager`, `NewManager`,
  `Acquire`, `Inspect`
- `internal/steady/write.go`: `WriteFile`, `WriteFileContext`, `ReadFileContext`

## Architecture

### Isolated Directory Layout

`dirs.APIRouterLayout` resolves paths that are deliberately separate from Magpie
user state (`dirs.MagpieDir` and `dirs.DataDir`):

- Configuration directory (`ConfigDir`):
  - Windows: `%APPDATA%\api-router`
  - macOS: `~/Library/Application Support/api-router`
  - Linux: `$XDG_CONFIG_HOME/api-router` or `~/.config/api-router`
  - Files: `config.json`, `credentials.json`, `config.lock`
- Data directory (`DataDir`):
  - Windows: `%LOCALAPPDATA%\api-router`
  - macOS: `~/Library/Application Support/api-router/data`
  - Linux: `$XDG_DATA_HOME/api-router` or `~/.local/share/api-router`
  - Paths: `models/`, `models/manifest.json`, `logs/`, `run/`,
    `run/api-router.lock`, `run/archive/`

`APIRouterLayout.EnsureDirs` creates the directory tree with `0700` permissions.

### Configuration Document and Separation of Secrets

`routerconfig.Store` manages `config.json` and `credentials.json` under a single
process/file lock (`config.lock` via `steady.LockFileContext` with a 2-second
default operation budget):

- `config.json` requires `schemaVersion: 1`, a non-empty `instanceId`, and top
  sections `listen`, `apps`, `providers`, `redaction`, and `runtime`, plus
  optional routing-group keys (`groups`, `groupOrder`, `noAutoGroups`).
- Unknown top-level and nested JSON fields are preserved across edits because
  `Document` stores `map[string]json.RawMessage` and sections round-trip raw
  maps before updating specific keys.
- Before overwriting `config.json` or `credentials.json`, the store writes the
  previous bytes to `.bak` sibling files (`config.json.bak`,
  `credentials.json.bak`) and uses `steady.WriteFileContext` (temp file + sync +
  atomic rename).
- `CloudProvider` records in `config.json` never contain plaintext API keys. They
  store `credentialRef` (`cred_<hex>`) and `keyPreview`, along with optional
  `discoveredModels []string` (persisted upstream model IDs cached during
  provider save/probe and updated via `Store.SaveProviderDiscoveredModels`).
  Actual secret values live in `credentials.json` under `providers[credentialRef]`.
  Orphaned credential references are pruned when providers are updated or deleted.

### Routing Groups Persistence and Auto-Merge (`Config.Groups`)

`routerconfig/groups*.go` adapts `internal/provider/group.go` to API-router's
configuration model:

- `Config.Groups []Group` persists user-defined and customized routing groups in
  `config.json` (`groups` array), alongside `GroupOrder []string` and
  `NoAutoGroups bool`.
- Each `Group` supports strategies `smart_split` (`StrategySmartSplit`,
  cloud/local hybrid routing), `ordered` (`order`), `weighted` (`weight`), and
  `manual`, member references (`<providerId>/<model>` or `local/<engine>`),
  condition `Rules []GroupRule`, `Match []string` glob patterns, `Hidden`
  tombstones, and effort levels.
- `EffectiveGroups(cfg, catalogModels)` (`Store.EffectiveGroups`) combines saved
  groups with synthesized cross-provider auto-merged groups (`Auto: true`,
  `ID: AutoGroupID(canonicalModel)`). When two or more distinct providers (or
  `local`) expose the same canonical model across `p.Model`,
  `p.DiscoveredModels`, and `catalogModels`, an auto group is generated unless
  overridden or hidden (`Hidden: true`) in `cfg.Groups`.

### Application Profiles (`AppProfile`)

`routerconfig/apps*.go` persists per-application routing profiles under
`apps[id]` in `config.json`:

- `AppProfile` includes `Enabled bool` (defaulting to `true` when omitted in
  stored JSON) and `Models []string` (ordered list of bound targets supporting
  `<providerId>/<model>`, `local/<id>`, and `group/<id>`).
- `normalizeAndSyncAppModels` deduplicates `Models`, synchronizes the first
  selected entry with legacy `Mode` (`cloud` vs `local`), `ProviderID`/`Cloud`,
  and `Local` fields for backward compatibility, and validates references
  against configured providers, `EffectiveGroups`, and installed local engines.

### Runtime Settings

`RuntimeSettings` lives at `runtime.settings` inside `config.json`. Defaults are
supplied by `DefaultRuntimeSettings()`:

- `RequestBudgetMs`: default `30000`, range `[1000, 300000]`
- `StartupWaitMs`: default `15000`, range `[1000, 120000]`
- `IdleTTLSeconds`: default `600`, range `[30, 86400]`
- `MaxConcurrentGenerations`: default `2`, range `[1, 16]`
- `DefaultRoutingMode`: `"cloud"`, `"local"`, or `"hybrid"` (default `"hybrid"`)
- `DefaultFallbackPolicy`: `"none"` or `"allow_cloud"` (default `"allow_cloud"`)
- `DiagnosticRetention`: default `100`, range `[10, 1000]`

### Local Model Manifest

`LoadModelManifest` and `SaveModelManifest` manage `models/manifest.json`. Each
`ModelRecord` tracks `engine`, `modelId`, `quantization`, `version`, `files`
(`ModelFile` with relative path, size, and SHA-256), `state`, and `updatedAt`.
Path validation rejects absolute paths, parent traversal (`..`), and duplicate
relative paths.

### Process Lock and Stale Lock Archive

`routerlock.Manager` coordinates single-instance startup via
`run/api-router.lock`:

1. `Acquire` takes an advisory lock on `run/api-router.lock.guard`.
2. If no lock file exists, it writes the current `Record` (`instanceId`, `pid`,
   `port`, `startedAt`, `version`, `exePath`, `configPath`, `dataPath`).
3. If a lock exists and `ProcessProbe.SameProcess` confirms the recorded PID is
   still the same live process, `Acquire` returns `Status: StatusAlreadyRunning`
   without mutating the lock.
4. If the recorded process is dead or its PID was reused, the stale lock file is
   moved into `run/archive/api-router-<timestamp>-pid<pid>.lock` before writing
   the new lock record (`Status: StatusRecoveredStaleLock`).
5. Corrupt or unreadable lock files return `ErrUnknownLock` and are never
   deleted automatically.

## Invariants

- API-router state never reads or mutates Magpie's `settings.json`,
  `accounts.json`, or `plugins.json`.
- Plaintext provider API keys never appear in `config.json` or error messages.
- `config.json` and `credentials.json` updates always preserve `.bak` backups
  and unknown JSON properties.
- Provider deletion (`DeleteCloudProvider`) rejects removal when any application
  profile (`ProviderID`, `Cloud.ProviderID`, or `Models`) or active routing
  group (`Members` or `Rules`) references the provider.
