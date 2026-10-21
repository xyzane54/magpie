# Wefto storage and runtime state

[`dirs.Layout`](../../internal/dirs/dirs.go) resolves the product paths once at startup. [`product`](../../internal/product/identity.go) owns new names and explicit legacy identifiers. Resolution is read-only; `Layout.Ensure` creates writable directories with private modes. Product storage is separate from Magpie settings, accounts and plugin migrations.

## Layout and rename compatibility

| Platform | Configuration | Durable data | Cache | State and logs |
| --- | --- | --- | --- | --- |
| Windows | `%APPDATA%/wefto` | `%LOCALAPPDATA%/wefto` | data/cache | data/state, data/logs |
| macOS | `~/Library/Application Support/wefto` | same as configuration | `~/Library/Caches/wefto` | data/state, `~/Library/Logs/wefto` |
| Linux | `$XDG_CONFIG_HOME/wefto` or `~/.config/wefto` | `$XDG_DATA_HOME/wefto` or `~/.local/share/wefto` | `$XDG_CACHE_HOME/wefto` or `~/.cache/wefto` | `$XDG_STATE_HOME/wefto` or `~/.local/state/wefto`; logs beneath state |

Models and inference runtimes live in `data/models` and `data/runtime`. Configuration and credentials are `config/config.json` and `config/credentials.json`. `state/router.lock` is the single-instance directory; `instance.json` and the private `management-token` live inside. `Layout.Install` is the executable directory, separate from writable state. Environment directory overrides must be absolute.

`Resolve` checks the equivalent `api-router` layout using `Lstat`. If only legacy storage exists, it returns that entire layout in place, including models, cache, state and logs. It never creates new configuration while retaining credentials or backups elsewhere. If both layouts exist, or an observed root is unreadable, a link or a non-directory, resolution fails for explicit reconciliation. No automatic copying, moving, merging or deletion occurs. See the [upgrade guide](../wefto-upgrade.md).

## Configuration and secrets

[`routerconfig.Store`](../../internal/routerconfig/config.go) owns schema-1 configuration with a nonempty instance identity and listen, apps, providers, redaction and runtime objects. Unknown JSON fields round-trip. Updates retain previous `.bak` files and write atomically through [`steady`](../../internal/steady); a private configuration lock serializes transactions.

Provider records contain credential references and previews; API keys remain in `credentials.json`. Application Profiles hold ordered model/group references, with the first synchronized to legacy primary-model fields. Provider deletion rejects references from saved applications or groups. Model/runtime artifacts and request ledger records remain in their existing owners' paths; renaming does not rewrite their contents.

## Shared file replacement

[`steady.Rename` and `WriteFileContext`](../../internal/steady/steady.go) publish staged state files beside their destination. On Windows these sibling replacements use [`os.Root.Rename`](../../internal/steady/rename_windows.go), which requests handle-based POSIX replacement and owns the filesystem fallback. On supporting filesystems such as NTFS, a reader that permits deletion can finish reading the previous generation while new readers open the complete replacement. A reader that denies replacement is still waited out by the existing bounded retry. Moves between different directory paths retain `os.Rename` behavior; the staged file is flushed before publication and a failed write removes its staging file.

[`ReadFile` / `ReadThere`](../../internal/steady/read_windows.go) open Windows files with read, write and delete sharing, so reads of the previous generation cooperate with that replacement. Each read owns and closes one handle; ordinary Win32 resolution still follows links outside the parent, and long absolute and relative paths retain extended-path support. Sharing flags do not grant read access: a handle that denies readers still produces a read-sharing error after the bounded retry.

[`Stat` / `StatOnce`](../../internal/steady/stat_windows.go) query metadata from a handle with attribute-only access and the same replacement sharing. A read-data restriction does not prevent metadata lookup. `filememo` uses `StatOnce` for its known-absent fast path, so that path retains a single lookup without reverting to the path-attribute API. A newly created file is detected after a cached absence; true deletion still returns `fs.ErrNotExist`.

[`filememo.Read`](../../internal/filememo/filememo.go) continues to report genuine deletion as `fs.ErrNotExist` and other read failures as errors. The replacement fix does not return a cached account list on failure or parse a missing file as empty. The existing bounded read retries remain for replacement by older or external writers.

## Service and host recovery

[`routercontrol`](../../internal/routercontrol/control.go) verifies the recorded executable path, process stamp, instance identity and authenticated service before control operations. [`routerservice`](../../internal/routerservice/service.go) acquires the instance lock, binds a loopback listener and persists the actual address. Unrelated listeners remain running; another available port is selected and established host connections synchronize transactionally.

Recognized records are archived only after their process is proven exited or its PID reused. Live and unknown locks remain intact. Legacy storage keeps the old Windows autostart entry name so an existing entry can be inspected; entries belonging to another executable are never adopted or overwritten.

Host recovery records retain the original root, first snapshot, checksum and declared adapter contract. Exact frozen version-1 Codex/Loomy contracts remain readable by the version-2 adapters; arbitrary versions or fields are rejected. Host transactions acquire both new and legacy writer locks. Details belong to [host configuration](wefto-host-config.md) and [Loomy](wefto-loomy.md).

Verification: `go test ./internal/dirs ./internal/routerconfig ./internal/routerhost ./internal/routercontrol ./internal/steady ./internal/filememo`, then the [complete product gate](../product-regression.md). Rename tests cover all platform layouts, retained credentials/models/recovery sentinels, dual-layout conflicts, unknown roots and legacy host restore/reconnect. Native Windows replacement tests keep an old reader open through both publication paths, reject missing sources and read-only destinations without losing the previous generation, and exercise concurrent replacement under `-race`.
