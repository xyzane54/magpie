# API-Router Loomy (`opencode.json`) Host Integration

## Purpose

Connect, synchronize, and restore Loomy's OpenCode configuration
(`opencode.json`) and coordinate multi-model and routing-group (`group/<id>`)
onboarding alongside WorkBuddy through `POST /api/hosts/auto` and `FastChecker`.

## Entry Points

- `internal/routerhost/loomy.go`: `inspectLoomy`, `connectLoomy`,
  `restoreLoomy`, `retireManagedLoomyEntries`, `loomyPaths`
- `internal/routerhost/host.go`: `HostLoomy`, `Options` (`Models []string`),
  `FastChecker`, `Manager.Connect`, `Manager.Restore`
- `internal/routerservice/hosts.go`: `handleHosts`, `handleHostsAuto`,
  `fastCheckApp`
- `internal/routerservice/assets/profiles.js`: `#view-agents` two-row agent UI
  (`loomy` and `workbuddy`)

## Architecture

### Configuration Path Resolution

`loomyPaths` locates Loomy's `opencode.json` in priority order:

1. Explicit override passed to `routerhost.NewManager` (`Config.LoomyDir`)
2. `$OPENCODE_CONFIG_DIR/opencode.json` when `OPENCODE_CONFIG_DIR` is set
3. `$XDG_CONFIG_HOME/opencode/opencode.json` when `XDG_CONFIG_HOME` is set
4. `~/.config/opencode/opencode.json` across Windows, macOS, and Linux

### Multi-Model & Routing-Group Connection (`connectLoomy`)

When connecting Loomy via `POST /api/hosts/auto` (`action: "connect"`) or
`POST /api/hosts/loomy/connect`:

1. **`FastChecker` Non-Blocking Check**:
   - `Manager.Connect` prefers `FastChecker` (`Service.fastCheckApp`) over
     blocking network probes. It verifies locally that the `loomy` `AppProfile`
     and all entries in `Options.Models` (`group/<id>`, `<providerId>/<model>`,
     `local/<id>`) exist in the current store.
2. **Writing `provider.api_router.models`**:
   - Preserves all unrelated top-level keys and other providers in
     `opencode.json`.
   - Upserts `provider.api_router` with `npm: "@ai-sdk/openai-compatible"`,
     `name: "API-Router"`, `options.baseURL: "<baseURL>/v1"`,
     `options.apiKey: "api-router-local"`, and
     `options.headers["X-API-Router-App"]: "<appID>"`.
   - Writes every selected entry from `Options.Models` (`resolvedModels()`) into
     `provider.api_router.models[modelID]` (supporting `group/<id>`,
     `<providerId>/<model>`, and `local/<id>`).
   - Calls `retireManagedLoomyEntries` to purge previously managed model keys in
     `provider.api_router.models` that are no longer selected.
3. **Default Primary Model Assignment**:
   - Sets top-level `model` and `small_model` to `api_router/<models[0]>` (the
     first selected routing group or model).
   - WorkBuddy (`models.json` + `settings.json`) follows the same contract via
     `POST /api/hosts/auto`: all selected groups and models are written to
     `models.json`, stale managed entries are retired, and `models[0]` is set as
     `SelectedModel` in `settings.json`.

### Disconnect and Backup Restoration (`restoreLoomy`)

- Before the first managed connection, `commitTransaction` saves the original
  `opencode.json` bytes (and pre-existing `model` / `small_model` values) in
   the host backup directory (`%LOCALAPPDATA%/api-router/hosts/loomy/`).
- When `POST /api/hosts/auto` is called with
  `{"host": "loomy", "action": "disconnect"}` (or `POST /api/hosts/loomy/restore`),
  `restoreLoomy` restores the backed-up `opencode.json` snapshot or removes
  `provider.api_router` and restores the previous `model` / `small_model`
  fields while preserving any concurrent user edits to other fields.
