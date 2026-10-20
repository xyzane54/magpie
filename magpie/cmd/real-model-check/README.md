# Fixed native model diagnostic acceptance

This internal command imports **complete local cache files** through the real
`FileInstaller` and runs the real `Manager`/`NativeFactory`. It leaves the fixed
catalog, production sources and W4/W5 paths unchanged. An exact original URL is
mapped to an owned loopback HTTP file server. Other URLs, redirects and other
network destinations fail. The downloader still checks pinned length/SHA256,
extracts within the original archive budget and creates the normal verified
receipt and manifest. Source files and download partials are never removed.

Run this command under `scripts/heavy-regression-lock.cjs`'s existing shared lock.
Do not acquire that lock twice. Before starting each model, inspect **current**
available RAM while holding the lock and wait for sufficient headroom; do not
terminate other applications. The command also samples fresh available RAM
immediately before each load and refuses to load if that observation fails. It
records memory rather than claiming a model-specific minimum has been proved.
Memory sampling for this acceptance run is implemented for native Windows.

Build once, then invoke the executable sequentially for 1.7B and 4B. Give each
model a dedicated data directory and a separate output directory. For example:

```powershell
go build -o D:/temp/api-router-real-model-check.exe ./cmd/real-model-check
D:/temp/api-router-real-model-check.exe `
  --model spark-x2.5-1.7B `
  --model-file D:/temp/api-router-real-model-cache/1.7B/Spark-X2.5-1.7B-Q4_K_M.gguf `
  --template-file D:/temp/api-router-real-model-cache/1.7B/chat_template.jinja `
  --runtime-archive D:/temp/api-router-w4-real/llama-b10828-bin-win-cpu-x64.zip `
  --data-dir D:/temp/api-router-real-model-data/1.7B `
  --output D:/temp/api-router-real-model-evidence/1.7B/result.json
```

The default cold-load budget is 60 seconds, including the cold re-verification.
Generation has a separate 120-second execution budget. `--load-timeout` and
`--generation-timeout` permit explicit diagnostic reruns; their actual values
are saved in the JSON. A rerun reuses verified destination files, and installation
uses the same downloader and durable resume metadata without clearing partials.

`--prepare-only` creates the dedicated configuration, verifies disabled startup,
close and rejection after close, and never imports or starts a model. Source,
installation, loading and inference assertions remain `not_run`.
`--install-only` imports/verifies complete files and checks runtime `--version`,
then closes without a model load. Neither mode implies an inference pass.

The JSON records every assertion as `pass`, `fail` or `not_run`; separate stage
flags identify complete bytes, installation, load and ordinary inference. It
also records the fixed revisions, SHA256 metadata and full fingerprint, native
version output, verified process spec, arguments reconstructed from the pinned
process contract, PIDs, 250 ms memory observations and sampled peak working set
and private bytes. Contract arguments are identified as such rather than an OS
command-line capture. JSON and SSE response bytes are saved beside the report.

SSE is consumed progressively inside the `Generate` callback, with first content,
finish and `[DONE]` timestamps and event ordinals. This proves the diagnostic
client observed content before consuming terminal events; it does not validate
W4 protocol translation, browser delivery or specialist model quality. The
synthetic tool-history sample checks an assistant tool call and matching tool
result can be consumed; it does not establish general tool-calling compatibility.
Exact synthetic markers, native health, cancellation after first streamed
content, a subsequent ordinary generation, disabled/closed admission and actual
`Process.Done` completion are checked separately.

All three purpose capabilities remain `unknown`. Before loading and after the
diagnostic calls, `Invoke` must reject every purpose with `ErrCapability`, without
calling its consumer or starting a process. No fake acceptance suite is registered.
Disable and close verify `unloaded`, PID 0, no active/queued work and completion of
every owned process's waiter. A deferred close/stop also runs after any error.
The tracking factory refuses a successor until all earlier processes are reaped.

The unit tests use tiny synthetic files and loopback transport only:

```powershell
go test ./cmd/real-model-check
```

These tests do not download or execute real weights. They cover exact URL and
destination restrictions, Range preservation, missing/size/hash rejection through
the real installer, valid formal installation and cleanup after preparation fails.
