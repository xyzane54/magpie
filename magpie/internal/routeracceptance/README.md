# Fixed local diagnostics through the product

The exclusive `-privacy-diagnostic representative|full` option selects only the
fixed Spark 1.7B synthetic privacy materials. It rejects 4B, arbitrary suite
names and combinations with the business/prepare/install-only modes. This path
uses the service's existing Manager.Generate and strict W5 diagnostic evaluator;
it has no arbitrary prompt, endpoint, capability acceptance or production bypass
option. Representative indices are 0/1/4/3/49/53 of the unchanged 56-case corpus,
including the long-field capacity probe. Both modes run the same private W5
roundtrip. Full mode additionally checks real stream cancellation and a shorter
100ms parent deadline with immediate same-process recovery; this is distinct
from a15-second budget exhaustion and does not prove semantic recall.
Full mode also measures the separately fixed heldout corpus and a private
synthetic real-result/W5/loopback-upstream/JSON+SSE roundtrip composition.

The original10-field person/email/tool/numeric case remains separate from a
four-field email/tool case and a one-field text-only case reusing corpus case4.
Each keeps its own outcome and raw file names. Text-only success cannot stand
in for tool or numeric acceptance; `ToolCheckApplicable` and
`NumberCheckApplicable` explicitly distinguish those checks. The one-field
case requires the actual model's exact annotated EMAIL entity before sending.

The safe report records actual HTTP status, finish reason, returned token
counters, measured transport phases, safe parser classes, indices and metrics.
Queue/load is combined; headers time includes the server's synchronous work;
consume includes evidence I/O and an extra diagnostic parse. These are not
isolated model inference estimates. Raw responses are written only under
`privacy-synthetic-only` or `synthetic-only`, with new evidence-directory
reservation. All capabilities remain unknown. This suite is not wired to a
public management endpoint or the production Pipeline.

`api-router.exe diagnose-local` runs the fixed synthetic suite inside the formal
product executable. It starts the normal `routerservice` HTTP server, which
creates its own `LocalModels` and manager through the production constructor.
Trusted Go callbacks observe that manager and the factory's actual processes;
the command never injects a replacement manager, installer or process factory.

For example, run under the repository's existing shared heavy lock:

```powershell
api-router.exe diagnose-local `
  -model spark-x2.5-1.7B `
  -model-file D:/temp/api-router-real-model-cache/1.7B/Spark-X2.5-1.7B-Q4_K_M.gguf `
  -template-file D:/temp/api-router-real-model-cache/1.7B/chat_template.jinja `
  -runtime-archive D:/temp/api-router-w4-real/llama-b10828-bin-win-cpu-x64.zip `
  -data-dir D:/temp/api-router-native-product/installed/1.7B `
  -output D:/temp/api-router-native-product/evidence/new-run/1.7B/result.json
```

Use a new evidence directory per invocation, and run 1.7B to completion before
4B. Existing reports and raw responses are refused. Downloaded model/template
and runtime bytes can be reused from the earlier verified cache. The command
imports only these original bytes using the real downloader with a strict URL
to owned-loopback transport mapping; it never copies an installation manifest,
extraction receipt or execution directory. The authenticated HTTP `/install`
operation then runs the normal production installer. Windows dependencies are
obtained by that installer from the fixed official artifact source. No Python,
DLL-directory argument, special PATH, registry change or manually copied DLL
tree is used by this product mode.

Selection, enablement, install, load, health and disable use actual authenticated
management HTTP. The loopback server retains the normal peer, exact Host and
Origin checks. The JSON records every operation's status and response without
the management token. The server is canceled and joined for close assertions.
Generation uses the diagnostic `Generate` seam on the very same product manager;
all prompts and tool messages are fixed in code. No arbitrary prompt, URL,
environment or acceptance evidence is accepted from command arguments. No
diagnostic HTTP route is registered. All three business `Invoke` purposes must
remain unknown and reject before and after generation.

Pass `-business-preacceptance` only for a controlled real-model pre-acceptance.
It uses the same Manager's diagnostic `Generate` seam for one fixed judgment
sample, a two-call tool-result round and the W5 fixed 56-case
`EvaluatePrivacyDiagnostic` measurement. It records hashes and aggregate
metrics, never raw prompts or mappings, and does not register a capability or
change the Manager's acceptance state. This is review evidence, not a
production availability switch.

The original assertions retain their identities. Seven additional assertions
check fixed dependency pins, startup without automatic installation, installed
dependency status, `ValidateDependencies` before load, actual loaded Windows
modules, absence of a manual DLL-directory override and repeat installation.
All six dependency files are verified. Windows module observation requires the
three core VC modules and rejects any observed dependency module loaded outside
the verified app-local execution directory or with different bytes. It records
each observed path, hash and pinned version. Optional DLLs need not have been
loaded by the particular synthetic samples.

The product executable hashes itself into the report. Process arguments,
raw JSON/SSE, memory samples, stage timing and process reaping remain separate
evidence. The production load/generation budgets stay 60/120 seconds. A passing
diagnostic does not pass judgment, redaction, general tools or business execution
quality acceptance.

`-prepare-only` exercises the real product service, HTTP status/disable and
shutdown without downloading dependencies or loading a model. `-install-only`
adds explicit installation, verification and version checking without loading a
model. Unexecuted assertions remain `not_run`.

`cmd/real-model-check` retains the earlier standalone command shape and delegates
its fixed suite to this package. The formal product evidence must come from
`api-router.exe diagnose-local`, whose report has `product:true` and HTTP
operation records.
