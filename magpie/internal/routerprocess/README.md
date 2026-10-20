# Owned local model process

`Factory.Start` starts one child without waiting for health readiness. Its
context owns launch only; the runtime manager owns loading, concurrency,
idle/disable/shutdown policy and calls `Stop`. `Done` closes after the only
waiter has reaped the child. `Stop` is concurrent and idempotent, and initiates
termination even if its own wait context is canceled. No existing process is
searched for or killed.

The fixed contract is llama.cpp **b10828**: loopback host, an allocated port,
one model path, explicit context, one execution slot, Jinja and
`--cache-reuse 256`; CPU explicitly uses zero GPU layers. Extra arguments cannot
override these flags. Runtime artifact and model verification belong to the
installer. These flags were checked against the tag's [server
documentation](https://raw.githubusercontent.com/ggml-org/llama.cpp/b10828/tools/server/README.md)
on 2026-10-17. That document defines prompt caching as enabled by default and
cache reuse as KV shifting; configuring it provides no measured speed claim.
`--chat-template-file` may supply an independently verified artifact.

The Windows production installer supplies `Spec.RuntimeDependencies` from the
fixed catalog and the verified app-local execution tree. `DependencySpec` binds
the dependency set's ID, four-component version and `amd64` architecture to each
DLL's absolute path, name, size, SHA256 and file version. The installer owns the
source and composition receipts; a local receipt cannot replace these catalog
pins. `ValidateDependencies` rechecks the files before launch: unique DLL names
directly beside the executable, regular files without hard links, symlinks or
ancestor reparse points, exact size/hash, AMD64 PE32+ DLL headers and fixed PE
file versions matching the dependency set. The list is bounded to 32 files and
each DLL to 64 MiB. PE metadata is read as bytes, so validation also runs on
non-Windows fixture hosts without loading a library.

Dependency failures expose `DependencyError.Code`:
`local_model_dependency_missing`, `local_model_dependency_corrupt`,
`local_model_dependency_architecture`, `local_model_dependency_version` or
`local_model_dependency_layout`. `Unwrap` preserves the cause; context
cancellation and deadlines retain their original errors. A non-nil dependency
spec can launch only on Windows. A nil spec supports other platform runtimes
and explicit fake engines; the Windows production installer requires the
dependency spec.

For a dependency-bearing launch, the child's working directory is the verified
executable directory. Its environment removes inherited `GGML_BACKEND_PATH` and
`LLAMA_ARG_*` overrides. The child receives `PATH=<System32>;<Windows>`,
`SystemRoot` and `WINDIR` from Windows APIs; an explicit `Spec.Env` override of
these loader keys fails validation. Other environment entries remain available.
This preparation changes only the child command and calls no process-global
`SetDllDirectory` or `AddDllDirectory` API. The parent environment and working
directory retain their values. App-local deployment, acquisition and repair are
described in the [runtime reference](../../docs/subsystems/api-router-runtime.md).

`RuntimeVersion(ctx, spec)` is the narrow internal version probe. It executes
only `--version` with the same dependency checks, child environment, working
directory and OS ownership as `Start`; it bounds combined output to 64 KiB. The
caller supplies the time budget. Cancellation terminates the owned process and
joins its waiter before returning. The caller supplies an already verified
runtime/model spec; the probe does not install artifacts or establish model
capabilities.

`NativeFactory.OnStart` is an optional synchronous observer supplied by trusted
Go code. It receives the same owned `Process` and `Spec` returned to the manager,
after successful launch and its launch-context cancellation check, before the
manager's readiness checks. It must return promptly. Management HTTP requests
cannot configure this observer, and the version probe does not call it.

`Health` requires HTTP 200 and valid JSON `status: ok`, checks child exit and
bounds the response. Both health and caller inference clients use
`NewHTTPClient`, whose transport bypasses proxy settings and refuses redirects.
Health never establishes judgment, execution or redaction capability.

Windows launches suspended, creates and configures a non-inherited kill-on-close
Job, assigns the child and only then resumes it. Failed assignment or resume
terminates/reaps the child and fails the launch. Closing the Router process
closes the job and kills its children even without a shutdown hook. Explicit
Stop terminates the job and joins the waiter. Linux uses `proc.Tree` plus
parent-death SIGKILL. Other platforms use `proc.Tree` for explicit shutdown;
abrupt parent-exit behavior there remains unvalidated.

`cmd/fake-llama-server` uses the same flags, `/health` and
`POST /v1/chat/completions` JSON/OpenAI SSE. `fakeserver.New(Config)` supplies the
same handler to runtime tests. Test controls are explicit:

| Control | Behavior |
| --- | --- |
| `--fake-start-fail` | Exit 22 before bind |
| `--fake-unhealthy` / `--fake-load-delay 1s` | Permanent/timed loading, HTTP 503 |
| `--fake-health-status loading` | HTTP 200 with a non-ready health body |
| `--fake-crash-after 1s` | Abrupt exit 23 |
| Body `fake: {mode: error, status: 429}` | Controlled JSON failure |
| Body `fake: {mode: hang}` | Flush headers, wait for cancellation |
| Body `fake: {mode: delay, delay_ms: 1000}` | Cancellable generation delay |
| Body `fake: {mode: crash}` | Abrupt exit 23 |
| Body `fake: {text: "...", chunks: ["a", "b"], chunk_wait_ms: 5}` | Deterministic ordinary/SSE content |

All completion bodies include a nonempty `messages` array. `X-Fake-Mode` is an
alternative test control. Fixtures are never real model acceptance evidence.

Verification: `go test ./internal/routerprocess/...`,
`go vet ./internal/routerprocess/... ./cmd/fake-llama-server`, and under the
shared heavy-test lock `go test -race ./internal/routerprocess/...`.
Windows' `TestWindowsAbruptParentExitKillsOwnedModel` launches a parent without
an outer process-tree job and waits on the model's native process handle after
killing that parent. General tests execute the test binary as a fake engine,
requiring neither weights nor an installed runtime. Tests use `testenv.Main`
and temporary files/ports. Dependency tests use the inert, 1024-byte
`testenv.PEDLLFixture` to exercise size/hash, PE architecture/version, links and
layout failures. The Windows fake-child probe checks its actual working
directory and environment, and the version probe's cancellation test waits on
the native process handle. These fixtures do not prove that the real CRT or
llama.cpp libraries loaded; native model acceptance records that separately.
