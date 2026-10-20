# Local model installation and runtime

`routermanifest`, `routerdownload`, `routerprocess` and `routerruntime` implement
T3.2–T3.8. The [management adapter](../../internal/routerservice/local_models.go)
owns HTTP/UI integration. Host adapters, provider credentials, routing decisions
and entity replacement algorithms are outside this subsystem.

## Source of truth and files

[`routermanifest.Fixed`](../../internal/routermanifest/manifest.go) is a detached,
compiled catalog: llama.cpp **b10828**, CPU Windows x64/Linux x64 and Metal macOS
arm64 metadata; Spark-X2.5 **1.7B / 4B Q4_K_M**, exact revisions, lengths,
SHA256, fixed template, license and disk reservation. Runtime/weight metadata
was checked on 2026-10-17; downloading and hashing the actual release bytes and
native inference remain separate acceptance steps. CUDA/Vulkan artifacts are
not selected: the existing device provider cannot yet establish their backend
availability. The legacy serving context is 8192 tokens. Explicit runtime
settings support 8K, 16K, 32K and 64K windows; the selected window is a serving
limit, not an assertion of validated 1M-context/tool compatibility. Archive expansion
has a conservative 256 MiB hard safety limit; model installation reserves two
model copies plus 576 MiB for runtime, staging and margin.

The two published models are **public execution validation weights**, not the
final judgment/redaction artifacts. Their metadata does not establish any of
the three purpose-specific acceptance results. Mirror metadata and fixed hashes
are recorded in catalog evidence (historical development record; not distributed).

`config.json.runtime.enabled/selected` records user intent through the existing
configuration coordinator; unknown fields are preserved. Missing fields mean
disabled/no selection. Invalid values or unknown persisted selection fail closed.
Only explicit `Enable`/`Disable` changes enablement. Startup verifies selected
files but makes no model/network/process call. Persisted enablement permits later
on-demand loading; startup itself never loads or resumes downloads.

### Configurable context and budgets

[`routerconfig.RuntimeSettings`](../../internal/routerconfig/runtime.go) owns the
complete `runtime.settings` object. Context is one of 8192/16384/32768/65536; time
fields use integer seconds. An absent or zero object retains legacy behavior,
including one shared five-second judgment queue/inference deadline. Explicit
settings split judgment waiting from inference. The long-context preset is an
explicit test configuration, not a replacement for the legacy defaults:

| Setting | Legacy default | Long-context test preset |
| --- | --- | --- |
| Serving context | 8192 tokens | 65536 tokens (64K) |
| Judgment | 5 s shared queue and inference | 15 s queue, then 30 s inference |
| Execution | 8 s queue, then 120 s generation | 30 s queue, then 300 s generation |
| Cold loading | 60 s | 90 s |
| Whole business request | 120 s | 600 s |
| Cloud fallback reserve | No separate reserve | 60 s within the same request budget |

These are configurable limits, not measured performance guarantees. In
particular, 64K requires loading, resource and latency checks on the actual
computer; model metadata alone does not prove that it fits available memory.

[`Manager.UpdateSettings`](../../internal/routerruntime/settings.go) stops local
admission, drains and reaps its one owned child, then saves through the existing
configuration coordinator. Any changed setting invalidates capability evidence;
changing context also changes the model fingerprint used in runtime bindings.
Existing weight/template downloads remain reusable. The management action
explicitly reloads an enabled installed model and reports a failed load without
claiming the saved settings are running.
Status exposes saved settings and resolved effective targets; the latter are not
proof that a process is loaded. Requests bind a settings snapshot, and local
observation captures its identity under the same Manager lock. An older snapshot
cannot silently invoke or claim the identity of a replacement runtime.

Privacy specifications also bind the effective context and current model
fingerprint. A context change retires the previous pipeline cache epoch while
existing prepared response snapshots retain their own lifetime. A stale privacy
binding fails closed. This updates runtime identity only; the
[privacy input/output limits](api-router-redaction.md), experimental 30-second
policy and independent quality gates remain in force.

With explicit settings,
[`context.go`](../../internal/routerruntime/context.go) checks the final local
chat request inside the same scheduler slot, using the pinned server's
`/v1/chat/completions/input_tokens` endpoint. That endpoint uses the generation
template/tokenizer, including tools and template arguments. Positive caller
output limits are preserved and reserved. When no output limit is supplied, the
remaining context becomes the output ceiling, with at least 32 free positions;
the runtime does not silently shift away input history to continue generating.
Uncountable or oversized input has a distinct failure and can use the guarded
cloud fallback. Raw input and upstream error bodies are not written to ordinary
request records.

The explicit judgment path uses a versioned compact view, preserves the original
execution envelope, constrains output to local/cloud JSON with at most 32 output
tokens, and checks a 2048-token judgment input ceiling with that local tokenizer.
Fixed host instructions retain their source order in bounded summaries; tool
descriptions are bounded too. Duplicate history representations are removed
without dropping tool-call/result associations. A view that cannot
retain its detected restrictions or fit the token ceiling is reported as such
and falls back to cloud. This does not establish routing quality acceptance.

The business request deadline covers the entire operation. Local stages are
bounded by the remaining shared budget less the cloud reserve; cloud fallback
uses the original deadline and never starts a new request budget. A shorter
caller deadline or cancellation wins. A shorter management connection-check
deadline remains independent of the business-request preset. Output already
committed to a client still prevents fallback.

Downloads live under `Layout.Models/<artifact-id>/` and
`Layout.Runtime/<runtime-id>-archive/`; extraction is under
`Layout.Runtime/<runtime-id>/`. `.part` plus `.part.json` survive cancellation or
Router restart. Resume is an explicit install action and uses Range/If-Range;
an ignoring server restarts from byte zero. Resource changes, malformed ranges,
short/overlong responses and bad SHA are handled by the
[downloader](../../internal/routerdownload/README.md). OS file locks reject
duplicate writers across processes. Final files are flushed, exactly verified
and atomically renamed. Extraction rejects links/traversal/Windows aliases and
is bounded before atomic directory publication.

Installation is proved by exact model/template hashes and reverified extraction
receipts, **never** by file existence, progress, config or `models/manifest.json`.
The latter is written after verification: schema 1 for runtimes without native
dependency metadata, schema 2 for the Windows app-local dependency contract below.
A cold load verifies again; corruption clears installed state and prevents launch. An explicit repair
quarantines a damaged extraction as `.invalid-<timestamp>` while no process uses
it, then publishes a fresh verified tree. Quarantined files are retained for
diagnosis and can consume disk until explicitly removed.

## Windows app-local dependencies

The Windows x64 CPU runtime additionally pins six release VC DLLs from an
official Microsoft VSIX in
[`windows_dependencies.go`](../../internal/routermanifest/windows_dependencies.go).
The fixed DLL version **14.50.35719.0** identifies this reviewed payload; it is
not a measured minimum version for b10828 or approval of other VC versions.
The VSIX's actual size is **3,313,422 bytes** and its SHA256 matches the official
catalog value; the catalog's size field instead says 3,325,839. The exact URLs,
per-file pins, source Authenticode observations, licensing conditions and this
size discrepancy are recorded in the
dependency design (historical development record; not distributed).
The VSIX OPC signature was not independently verified. Production verification
uses the compiled archive/file pins, PE architecture and fixed file versions;
it does not perform an online signature or revocation check at each launch.

Explicit installation downloads the VSIX through the same resumable downloader.
[`ExtractSelected`](../../internal/routerdownload/selected.go) checks all ZIP
headers and the total expansion bound, then writes only the six exact release
members into `Layout.Runtime/msvc-x64-14.50.35719.0/` with a v1 receipt. Debug
payloads remain inside the cached VSIX. A separate
[`Assembly`](../../internal/routerdownload/assembly.go) preserves the original
b10828 v1 extraction and creates
`Layout.Runtime/windows-amd64-cpu-b10828-msvc-x64-14.50.35719.0/`, placing the
selected DLLs directly beside the executable. Its v2 receipt must equal the
complete plan freshly derived from both verified source trees; all output
files are rehashed. Fixed per-file dependency pins are checked independently of
local receipts, and the completed model installation writes a schema-2 record.

[`ValidateDependencies`](../../internal/routerprocess/dependency.go) runs during
installation verification and immediately before native launch. It rejects
missing or altered files, wrong x64 DLL architecture/version, unsafe placement
and Windows reparse points/hard links. The owned child's working directory is
the verified executable directory; its loader environment uses system API paths
and excludes inherited loader overrides. No system VC install, System32 change,
global PATH change or diagnostic Python wrapper is required. App-local copies
remain the product's responsibility to update under the applicable Microsoft
redistribution license; system VC updates do not update these private copies.

An older installation lacking these files reports not installed and requires an
explicit install/repair. Startup, status reads and enable never download,
assemble or migrate it. Valid old GGUF/template/runtime caches are reused.
Explicit repair preserves damaged source/execution trees as `.invalid-*` before
replacement while no model uses them. Disable, idle unload and close release
processes without deleting files; this change adds no uninstall endpoint or
automatic cache/quarantine cleanup.

Management status adds `dependencies` with state, fixed ID/version, error and
recovery. A dependency set reports `not_required`, `unknown`, `preparing`,
`ready`, `canceled` or a specific failure such as `missing`, `corrupt`,
`architecture`, `version`, `layout` or `failed`; recovery is
`install_or_repair` when needed. Management errors preserve stable
`local_model_dependency_*` codes. These fields remain separate from installation,
health and capability acceptance. The full dependency metadata participates in
the existing capability fingerprint. W4/W5's `Invoke`/diagnostic interfaces and
the owned-process lifecycle signatures are unchanged.

## Lifetime and concurrency

[`Manager`](../../internal/routerruntime/manager.go) owns one selected model and
one process shared across applications and purposes. `NativeFactory` starts an
owned loopback child with `--parallel 1 --cache-reuse 256 --jinja`, the selected
serving context passed through `--ctx-size`,
verified model/template paths and CPU GPU-offload=0 when appropriate. JSON
`cache_prompt:true` enables the pinned server's prefix-cache path. This is a
configuration assertion only; actual KV-cache benefit is **not measured**.
See the [process contract](../../internal/routerprocess/README.md).

Intent, installation (`not_installed/downloading/installed/canceled/failed`),
runtime and capability samples are separate fields. Runtime transitions are:

- `unloaded → loading → ready → busy → idle`; shared cold starts use one worker.
- Verification/start/health/load timeout or crash → `fault`; children remain
  owned until termination is confirmed. Failed cleanup cannot spawn a successor.
- `idle/ready → unloading → unloaded` after 15 minutes without **any** active,
  queued or loading occupancy. Intent remains enabled; the next request loads.
- Explicit disable first stops admission and invalidates old waiters, persists
  disabled intent, cancels installation/loading, rejects queued requests, drains
  active generation and releases the child. Later requests cannot reload it.
- Router close stops admission and drains before process cleanup, joins owned
  workers and retains durable enable intent for a future explicitly used service.
  Windows uses a kill-on-close Job assigned before the child resumes; abrupt
  parent death cannot leave a task-owned executing child. Other native platforms
  remain separately unverified.

There is one execution slot and FIFO waiting capacity **16**, shared by all
three purposes. During cold loading all waiting calls count against 16. A
single canceled cold waiter does not cancel another waiter's shared load.
Disabled/full/canceled/fault are distinct errors. Timers and `Now` are injectable.
An active child's crash returns `ErrFault`, not caller cancellation. Caller
cancellation/deadlines, explicit shutdown epochs and purpose deadlines retain
their own errors, so W4 can apply its pre-output fallback/cancel policy safely.
`Sweep` and intent transitions serialize so unload/disable/re-enable cannot
overlap two live processes. No blocking network/process operation holds the
status mutex.

Cold loading has its own budget; the judgment and execution queue budgets begin
only after it succeeds. Legacy and explicit settings use the limits in
[Configurable context and budgets](#configurable-context-and-budgets). Redaction
retains its separate **15 s** shared queue/inference budget. A caller's shorter
deadline always bounds the operation. Disable's management drain budget is 120 s;
Router shutdown permits 5 s for HTTP and then bounded model cleanup. Expired drain
cancels active work, initiates owned-process termination and records a fault.

The explicit experimental-redaction startup option changes that Manager's
default redaction budget to **30 s**, capped at 30 s. A shorter configured or
caller budget remains in effect. Its dedicated invoker also places cold loading
under the same request deadline; the experimental extractor owns the earlier
deadline shared by every batch. The ordinary startup default remains 15 s.

An owned process requested to stop by Disable, Close or idle Sweep is an
expected exit. Its monitor must not publish `process_exited` while Stop is
completing. This distinction is bound to the same process immediately before
Stop; merely draining work does not make a crash expected. Unexpected exit
still faults the runtime and cancels active work. A failed Stop or expired drain
retains its separate fault. If the child already exited before Stop, that fault
is recorded before ownership is cleared even when its monitor is delayed.
Successful lifecycle diagnostics additionally check
that the unloaded status has no residual error.

## Health, samples and W4/W5

GET `/health` must be HTTP 200 with parsed `status:"ok"`. Health does not pass a
capability. Judgment, execution and redaction independently begin `unknown`.
`CheckCapability(Check{Purpose,Suite,Run})` executes a registered sample validator
under the same scheduler; success requires a versioned suite and nonempty sample
evidence. The callback receives `(ctx, loopbackEndpoint)` and must use the given
context and validate actual output. It must not recursively call `Use/Generate`.
No acceptance callback or claimed evidence is accepted from HTTP clients.

Each result binds the full runtime/model/template fingerprint, suite and evidence.
Changing selection resets conclusions; restart resets them to unknown. `state`
is the sample result; `ready` is effective enable/installation/runtime gating.
An untested model never inherits results from a different revision.

The API-router 1.7B and 4B routing and execution checks use the same experimental
trial checks. They retain `state:"unknown"` and a separate `trialEvidence`;
they become usable only after an authenticated user calls
`/api/local-model/trial/enable` following a current `/check`. The resulting
`trialEnabled`/`trialReady` fields are independent of formal quality and of
the redaction trial. `/trial/disable` persists the explicit shutdown. Changing
the selected model or its bound runtime resets the evidence and makes a saved
trial intent inactive until the check is run again.

W4/W5 use `Manager.Invoke(ctx,purpose,chatJSON,consumeResponse)` after registering
their independent checks. `Invoke` enforces acceptance before loading and again
when taking the execution slot. `Generate` is the diagnostic/acceptance seam;
`Use` is the lower scheduler seam. `consumeResponse` must consume SSE/body inside
the callback and honor the response context; returning cancels the upstream and
releases the slot. No response or body may escape for later reading. Judgment
and redaction force `chat_template_kwargs.enable_thinking=false`; execution
preserves that setting and the complete messages/tools semantics. W4 owns prompt,
tool-argument compatibility, output validation and fallback. W5 owns entity
extraction, replacement/restoration and fail-closed cloud gating. Existing
`routerrequest` redaction-unavailable rejection is unchanged: disabling a runtime
does not clear any application's redaction intent or send original text to cloud.

`Options.ExperimentalRedaction` is a trusted startup choice, false by default,
with no management HTTP setter. Only that choice permits
`NewExperimentalRedactionInvoker(fingerprint,suite)`. The immutable handle
admits Redaction for either enabled fixed Spark-X2.5 1.7B or 4B selection and exact
current artifact fingerprint, with a complete `privacy-trial-v1:` suite plus
64 lowercase hexadecimal digits. `VerifyBinding` checks the experimental
extractor's exact fingerprint and suite on every request. Disabled, closing,
stopping, different-purpose/model/binding and incomplete-suite calls reject.

This handle is an explicit trial admission, not sample acceptance. It never
changes `Status.Capabilities`, calls `CheckCapability`, or fabricates evidence;
unknown capabilities remain unknown and ordinary `Require`/`Invoke` still
require their real acceptance. Its `Invoke` uses the same private generation
transport, single Manager/process, FIFO queue and execution slot. Trial binding
is checked again after obtaining the slot, and response consumption, caller
cancellation and disable use the same cleanup path. There is no recursive
Generate/Invoke call while holding the slot.

The W4 service adapter supplies a versioned `RuntimeJudge` over the Judgment
capability and a Manager-backed Execution invoker. The judge accepts only
`local` or `cloud`, and Manager adds `enable_thinking:false` for judgment. A
normal health check or ordinary answer never marks a capability available;
without a registered acceptance suite the runtime stays unknown and requests
use the guarded cloud path. The configured load, judgment and execution budgets
are independent of this capability admission; larger limits do not grant
acceptance or trial authorization.

## Management and validation

Trusted in-process callers can attach `WithGenerationDiagnostics` to one
Generate context and read its completed snapshot. It measures scheduler wait
plus load, request-to-headers, consumption and total wall time, with actual HTTP
status. These phases are not token estimates or isolated inference measurements.
HTTP failures retain a safe typed status; response bytes are retained only for
that explicit diagnostic context. Ordinary error formatting/JSON omits them.
No management HTTP option enables this seam, changes capability acceptance, or
chooses a model endpoint. Fixed synthetic privacy CLI evidence keeps raw
responses in a separate diagnostic directory.

All routes inherit exact local Host/peer/origin checks, management Bearer auth,
no-store and bounded input. Encoded aliases and arbitrary URLs/paths are rejected.

| Route under `/api/local-model` | Method / behavior |
| --- | --- |
| base | GET/HEAD: status and fixed catalog; no enable/install/load |
| `/settings` | GET/HEAD: same status/catalog with saved and effective settings; POST: a complete integer-only `RuntimeSettings`, drain/save and reload an enabled installed model without downloading |
| `/detect` | POST: existing `routerdevice` report and recommendation |
| `/select` | POST `{model:<fixed-id>}`: disabled-only selection |
| `/enable` | POST: device checks bound to exact selection; persists enable only |
| `/install` | POST: recheck both destination volumes; start/resume install job |
| `/cancel` | POST: cancel and join installer; keep partials |
| `/load` | POST: explicit load under separate budget; no capability conclusion |
| `/health` | POST: process-only health; may be retried after health failure |
| `/check` | POST: binds the shared server-owned judgment and execution suites to the current 1.7B/4B selection; no client-supplied evidence |
| `/trial/enable` | POST: after current checks, persist explicit experimental judgment/execution authorization |
| `/trial/disable` | POST: persist explicit experimental routing/execution shutdown |
| `/disable` | POST: persist disabled, drain and release; Router keeps listening |

The original-style **Local model** page is at `/?view=local-model`. It reads on
navigation/connect/manual refresh and polls transient install/load states only
while that page is open. All five languages and desktop/mobile layouts are
covered. Closing the page stops no Router or model process.

Runtime settings use the app's 8K/16K/32K/64K buttons and separate numeric
second budgets. The long-context test preset only fills the form; Save and
reload is the explicit mutation. Invalid, missing, duplicate, unknown or null
fields in a settings POST are rejected before any runtime change. A disabled
model stays disabled.
Saved targets are visible after a load failure; an unloaded/unhealthy process
is labeled as not loaded instead of claiming its target window is active.
Each built-in capability check derives its suite from the current runtime
identity and prompt version; after a settings change, users must check again before
experimental admission resumes. Saved trial authorization remains the user's
independent choice and is not quality evidence. API status remains
available while the explicit settings operation drains and reloads.

Available physical memory measured around loading is an observation, not a
model-process peak-memory measurement.

When the selected model has no verified installation (`not_installed`, `canceled`
or `failed`), the authenticated local-model page requests one device probe per
page session. This read-only hardware probe neither selects/enables a model nor
downloads or loads it; refreshing status does not repeat a failed probe. A manual
Detect device action retries it. The recommendation appears above the model
choices, which show the full names **Spark-X2.5-1.7B** and **Spark-X2.5-4B**.
Unknown or failed checks are distinguished from a passed recommendation. Both
tiers share the existing pinned download, resumable installation, dependency
repair and runtime lifecycle. Enablement and installation retain the device
check via `Recommendation.Allows(tier)`: a `MaxTier: 1.7B` recommendation cannot
enable 4B, while a 16 GB machine with `Tier: 1.7B, MaxTier: 4B` recommends 1.7B
by default and still permits explicit 4B selection and enablement when available
memory, CPU threads and 4B disk space suffice. Capability requests use the selected model ID;
their suites bind its current fingerprint, including after a selection change.
Opening 4B support does not establish native 4B inference or quality acceptance.

Core tests use controlled time, loopback download servers and the executable
fake; no real weights/network are needed. `product-regression.cjs` includes all
26 integrated product packages/commands and requires ten UI suites, including
Loomy, local-model, redaction, service-control, spark-models and tutorial. Run `node scripts/product-regression.cjs`; the CLI owns the common
heavy-test lock across Windows and WSL worktrees. The round-two integration also
uses the installed native Windows Go 1.26.8 for package tests and vet. Native
race lacks a C compiler; WSL race/vet/browser evidence is never Windows evidence.
The delivery (historical development record; not distributed) records exact results
and external model/acceptance gaps.

The internal [`real-model-check`](../../cmd/real-model-check/README.md) command
imports explicitly supplied local cache files through a loopback-only transport
while preserving the fixed artifact identities. It uses the same
`FileInstaller`, `Manager` and `NativeFactory`, including byte verification,
extraction receipts and installation records. Diagnostic `Generate` samples do
not register capability suites or make business `Invoke` available. Its
per-model data directory, raw responses and individual lifecycle assertions are
separate from the user's service data. The companion `model-fetch` acquisition
command keeps resumable prefixes and optionally caches bounded HTTP ranges;
only the production downloader's final fixed size/SHA256 check publishes the
complete file. These commands are internal acceptance tools, not management
HTTP local-path import endpoints or new production download sources.
Run these checks under the existing shared heavy lock. Evidence describes only
the exact native platform and samples actually run.

Round 3's service layer owns the same Manager for W4 judgment/execution and the
W5 production redactor. Selection changes reset capability fingerprints and
redaction cache epochs; existing prepared snapshots remain independent. The
Windows final2 EXE passed the 30-item native lifecycle/dependency diagnostic
for both fixed Spark tiers. This proves runtime/install behavior only; the
diagnostic deliberately leaves all three business capabilities unknown.

The local Manager also publishes the safe, nonblocking [`LocalCallObservation`](api-router-runtime-observation.md)
completion contract for Router business calls and explicit checks. It preserves actual nullable usage and measured
queue/load timing without recording prompts, responses, tools, entities, URLs or credentials; ledger persistence remains
owned by the statistics subsystem.
