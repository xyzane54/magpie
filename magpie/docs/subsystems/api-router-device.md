# API-router device detection and model recommendation

[`internal/routerdevice`](../../internal/routerdevice/device.go) owns T3.1's
read-only device observations, hardware tier recommendation and device check.
It has no CLI/HTTP registration, startup hook, cache, polling, installer, network
client or process runner. Constructing `Native()` does no detection. Existing
Router startup, requests, configuration and redaction gates do not call it.

## Consumer contract

`New(Provider)` injects a complete provider. [`Probes`](../../internal/routerdevice/probes.go)
replaces individual system, memory, adapter, disk and backend reads; it also
allows Linux fixtures to run on Windows/macOS without probing the runner.
`ProviderFunc` is the small integration seam for T3.2/T3.4 test doubles.

```go
// Paths come from dirs.Layout; no host config or credentials are inspected.
detector := routerdevice.Native()
report := detector.Detect(ctx, layout.Models, routerdevice.Requirements{
    Tier1_7BBytes: manifest.Small.InstallationBytes,
    Tier4BBytes:   manifest.Large.InstallationBytes,
})
// Display report.Recommendation and report.Detection separately.
// A later, explicit user action owns download, installation and enablement.
```

The example's manifest is T3.2-owned, not a manifest implementation in T3.1.
Disk requirements include model/runtime files and temporary installation space
on the measured volume. There is no guessed default: a zero/missing requirement
leaves the device check unknown. If a runtime will be placed on another volume,
T3.2 must check that destination separately before installation.

| State | Owner and meaning |
| --- | --- |
| `Report.Recommendation` | Primary recommended `Tier`, maximum allowed `MaxTier` (`Allows(target)`), and reason; does not imply installability |
| `Report.Detection` | Known minimum RAM, available physical RAM (`>= 2.5 GiB` when known), CPU logical threads (`>= 4` on CPU-only hosts when known), supported system/architecture, required host backend and sufficient destination disk; never model health |
| `ModelState.Installed` | Separate installer-owned `Fact[bool]`; verified files, not inferred by detection |
| `ModelState.Enabled` | Separate configuration/runtime-owned `Fact[bool]`; explicit user choice, not inferred by recommendation or installation |

`Fact.Value == nil` serializes as `null` and always means unknown. Measured disk
zero remains known zero. Invalid physical RAM zero is unknown. A per-probe error
discards values returned alongside it; a provider-level error or cancellation
discards the entire snapshot. This includes errors with an empty message.
Reports own their pointers/slices, so editing a report does not change later
observations. Providers used concurrently must themselves be concurrency-safe.

`Recommend` and `Evaluate` are pure functions. `Detect` calls the provider once
per explicit invocation, never reuses stale success, and accepts only an absolute
model destination. Native disk probes inspect the nearest existing directory
without creating it; denied reads, files and dangling symlinks remain errors.

## Fixed recommendation and check rules

Thresholds use exact bytes with `GiB = 1 << 30` and `HalfGiB = GiB / 2` for the
product's GB RAM/VRAM labels; there is no floating-point rounding before comparison.

1. Unknown/invalid RAM → `unknown`; known RAM below 8 GiB → `none` (`memory_below_8_gib`),
   including machines with large GPUs. The below-8 rule takes precedence.
2. When no GPU accelerator backend (`metal`, `cuda`, `vulkan`) is confirmed available
   and known `System.LogicalCPUs < 4` → `none` (`cpu_threads_below_4`).
3. When `AvailableMemoryBytes` is known and `< 2.5 GiB` → `none` (`available_memory_below_2_5_gib`).
4. Confirmed discrete adapter with at least 6 GiB VRAM (when `< 16 GiB` RAM, or when
   a matching `cuda`/`vulkan` backend is confirmed available) → `Tier: 4B, MaxTier: 4B`
   (`discrete_vram_at_least_6_gib`), or `Tier: 1.7B, MaxTier: 1.7B` (`available_memory_limited_to_1_7b`)
   if known available memory is `< 4.5 GiB`.
5. Apple Silicon / unified memory with a confirmed `metal` backend and `>= 16 GiB` RAM →
   `Tier: 4B, MaxTier: 4B` (`metal_unified_memory_at_least_16_gib`) when available memory
   is unknown or `>= 4.5 GiB`, else `Tier: 1.7B, MaxTier: 1.7B`.
6. System RAM at least 24 GiB (`HighMemoryBytes`) →
   - If CPU-only and known `LogicalCPUs < 8`: `Tier: 1.7B, MaxTier: 1.7B` (`cpu_threads_limited_to_1_7b`).
   - If known available memory `< 4.5 GiB`: `Tier: 1.7B, MaxTier: 1.7B` (`available_memory_limited_to_1_7b`).
   - If known available memory `< 6.5 GiB`: `Tier: 1.7B, MaxTier: 4B` (`available_memory_prefer_1_7b_allow_4b`).
   - Otherwise: `Tier: 4B, MaxTier: 4B` (`memory_at_least_24_gib`).
7. Mainstream 16 GiB to below 24 GiB RAM (`MainstreamMemoryBytes`) → recommends `Tier: 1.7B`
   by default to avoid severe memory pressure and slow CPU inference on 16 GB machines:
   - If `(hasGPUAccelerator || LogicalCPUs unknown or >= 8)` and `(AvailableMemoryBytes unknown or >= 4.5 GiB)`:
     `Tier: 1.7B, MaxTier: 4B` (`memory_16_gib_prefer_1_7b_allow_4b`), allowing explicit 4B selection/enablement.
   - Otherwise: `Tier: 1.7B, MaxTier: 1.7B` (`cpu_threads_limited_to_1_7b` or `available_memory_limited_to_1_7b`).
8. Remaining known RAM from 8 GiB to below 16 GiB → `Tier: 1.7B, MaxTier: 1.7B` (`memory_at_least_8_gib`).

When `Evaluate` inspects destination disk space and `Recommendation.EffectiveMaxTier() == 4B`
while `Recommendation.Tier == 1.7B`, if disk free space covers `Tier1_7BBytes` but is below
`Tier4BBytes`, `Evaluate` caps `Report.Recommendation.MaxTier` to `1.7B` so `1.7B` remains
installable while `4B` is blocked.

Integrated/unified/shared memory, unknown adapter type and software/remote
adapters cannot supply discrete VRAM. Memory is not summed across GPUs. Unknown
GPU data cannot upgrade 8–16 GiB RAM.

An unknown/unsupported architecture, unknown/unavailable required backend,
unknown disk/manifest requirement, insufficient disk, known available memory
below 2.5 GiB, or CPU-only logical threads below 4 prevents `CheckPassed`.
Known failures dominate unknowns, and all check reason codes are retained.
The recommendation remains visible even when a device check cannot pass.

Windows x64 and Linux x64 accept a conservative CPU host backend. A VRAM-based
4B recommendation additionally needs a confirmed CUDA/Vulkan backend with the
same nonempty `AdapterID` as a qualifying adapter; another GPU or CPU does not
prove that memory usable. macOS arm64 requires a confirmed Metal host backend.
Native Windows/Linux optional accelerator backends currently remain unknown:
they are never guessed from DLL/library files. T3.2 can inject separately
validated backend evidence; T3.4 must still verify its exact runtime artifact.

No result grants permission to download, enables a local capability or changes
redaction. In particular, model unavailability while redaction is enabled must
still block at the later request gate; a recommendation is never a bypass.

## Native platform boundaries

Windows 10/11 x64 and macOS 14+ arm64 are the required first targets. Other
architectures return an unsupported check; missing Windows/macOS versions stay unknown.

| Platform | Native observations | Limits |
| --- | --- | --- |
| Windows | Installed physical RAM (`GetPhysicallyInstalledSystemMemory` / `GlobalMemoryStatusEx`), real-time available physical RAM (`GlobalMemoryStatusEx.AvailPhys`), native architecture, OS version, read-only CPU name and logical thread count, caller-available disk; DXGI adapters plus D3D12 architecture query | Only non-UMA, non-software/remote adapters count as discrete. Failed D3D12 classification stays unknown even if DXGI returns a large reserved segment. Optional CUDA/Vulkan evidence remains unknown. |
| macOS | `hw.memsize`, OS/CPU sysctl, process architecture, available filesystem blocks; Apple silicon unified GPU | Unified memory is never discrete VRAM. A cgo build queries `MTLCreateSystemDefaultDevice` without launching a runtime. A no-cgo build reports Metal unknown and does not pass the Metal check. |
| Linux | Sysinfo RAM with checked units, real-time available RAM (`/proc/meminfo` `MemAvailable:` with sysinfo fallback), uname/architecture, logical CPUs and caller-available filesystem blocks | GPU/CPU model details not read are unknown. Injectable fixtures validate the contract separately. Ubuntu/kernel/driver and physical CPU/GPU acceptance remain later native validation. |

The default product build is no-cgo and this task does not change that build or
connect detection to the service. Before connecting native Metal detection to a
macOS product build, T3.2/integration must use the cgo provider (system Metal and
Foundation frameworks) or supply equivalently validated backend evidence. A
no-cgo cross build proves compatibility only and cannot be recorded as Metal
acceptance. No Linux release work is included.

## Verification

Tests use `testenv.Main`, temporary HOME/USERPROFILE and data directories, and
fixture providers. They do not run native hardware queries, models or upstream
requests. `make test-router` includes this package. From `magpie/`:

```sh
gofmt -l internal/routerdevice
go test -race -count=20 ./internal/routerdevice
go vet ./internal/routerdevice
make test-router
```

Coverage includes 7.9/8/16 GiB RAM, 5.9/6 GiB VRAM, UMA/shared/unknown GPU,
multiple GPUs, failed/empty-error reads with residual values, unknown system,
architecture/backend, backend-to-adapter matching, disk below/at threshold,
missing requirements, cancellation, repeated/concurrent detection and detached
reports. Filesystem fixtures cover absent model directories and dangling
symlinks. A production import guard covers all Go platform files against
network/process/product lifecycle dependencies; the Metal bridge is also
reviewed for read-only behavior. Real hardware acceptance and cross compilation
are recorded separately in the T3.1 delivery record (historical development record; not distributed).
