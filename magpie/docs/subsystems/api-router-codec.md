# Shared cloud wire codecs

`internal/cloudcodec` owns the shared Chat Completions, Responses and Anthropic
Messages request IR, parsers, builders, SSE and whole-response adapters, stream
encoders, reply collectors and bounded cloud codec. Its import graph contains
only standard-library packages. It imports no gateway, provider, catalog, settings, plugin, process or
application-store package and has no `init` function.

The cloud executor imports this package directly. `routercloud.Request`,
`Target`, `Result`, `Event` and `ExecutePath` keep their contracts. `Result.Usage`
is now declared as `*cloudcodec.Usage`; `gateway.Usage` remains an alias of that
exact type, so existing users remain assignment-compatible. Unknown usage is
still nil, and known zero remains distinguishable from missing counts.

The extraction moved one implementation instead of making a second protocol
stack. Files prefixed `gateway_` and `provider_` preserve the source ownership of
the moved declarations. The three protocol bodies, JSON parser, schema helpers,
namespace flattening, error-body parsing and IR live here once.
`gateway/cloud_codec.go`, `gateway/wire_compat.go` and `provider/wire_compat.go`
provide compatibility names. Legacy gateway dispatch still handles its other
protocols; its retained three protocols delegate to these same wire codecs.
Methods and the small number of state fields legacy adapters need are exported
at this internal package boundary; no legacy server is constructed by a codec.

The cloud dispatch accepts exactly the three supported cloud protocols. It
preserves native requests except the bound model, rejects lossy translation,
requires complete replies, and prevents successful terminators after a failed
stream. Existing 3×3 JSON/SSE, full tool-message, unknown-usage and malformed-
response tests moved from gateway to this package. Captured host requests remain
in the original fixture directory; referencing fixture bytes does not import or
initialize gateway.

The portable request guard also rejects competing Chat output caps, empty
messages that the IR/parser or builders would omit or merge away, and JSON-object mode
sent to Anthropic (whose legacy builder would replace that API control with a
system instruction). Tools with root `anyOf`, `oneOf` or `allOf` cannot convert
to Anthropic because its legacy builder flattens those constraints. Nested
schema composition and business properties remain data. Native requests retain
these fields; no legacy builder is changed to emulate a missing control.

Responses assistant `phase` has no equivalent in the Chat/Anthropic builders;
conversion returns `capability_message_phase` rather than dropping it. URL
annotations are inspected in typed message content and typed tool-result output
blocks, so both require the declared native search capability. Opaque result
strings and business schemas are never scanned as protocol annotations. Preview
search rejects full-search-only controls (`filters`, `return_token_budget`,
`external_web_access`) rather than accepting restrictions an upstream may ignore.

Native Chat preserves WorkBuddy's validated per-message Agent-name, identity,
trace, reasoning and argument-display strings unchanged. The portable gate
rejects direct protocol metadata with `capability_message_agent` /
`capability_message_metadata`, and reasoning with `capability_reasoning_history`,
because the IR/destination cannot retain them. Same-named business properties
in schemas, arguments or tool-result strings do not trigger the gate. Complete sanitized installed
capture is replayed by the WorkBuddy agent tests; this addition leaves all other
input field/type validation in place.

WorkBuddy tool/assistant `toolResult` objects are also native Chat metadata.
Conversion returns `capability_message_metadata` before dropping any of their
tool-specific business or display values. The guard checks only the message
field; business schema properties, arguments and result strings named
`toolResult` remain ordinary data.

`host_fields.go` is the shared, pure contract for installed WorkBuddy Chat
metadata and the explicit Chat controls emitted by Loomy's compatible SDK and
WorkBuddy's model builder. Cloud input validation and routing normalization use
the same field, role, type, normalized-usage and text/refusal checks. Conversion
uses the same names to refuse host metadata or request options that the portable
IR cannot preserve, before opening an upstream request. Privacy extraction uses
the registry to scan every protected string/key. Registry objects are internal;
callers receive fresh name lists and cannot mutate the contract.

The native option subset is `frequency_penalty`, `presence_penalty`, signed
64-bit integer `seed`, string `user`, low/medium/high `verbosity`, `store:false`,
`prompt_cache_retention` (`in-memory`/`24h`), and Loomy's source-emitted
`promptCacheKey`/`reasoningSummary`. Camel-case controls are preserved literally;
their upstream meaning is not rewritten or inferred. Conversion returns
`capability_request_options`. Remote storage opt-in and arbitrary provider
extension fields remain outside this contract.

WorkBuddy task lifecycle, compact/recovery, hook, manual-tool, subagent/team and
approval metadata has explicit roles and scalar/object types in that registry.
It is retained as host data, never used to choose Router credentials, routing,
permissions, or whether validation may be skipped. Loomy interleaved `reasoning_details` strings, assistant `extra_fields`
objects and typed refusal history are retained natively; conversion refuses
`capability_content_metadata` / `capability_content_refusal` on typed parts.
Empty assistant annotations remain supported; media and unverified annotations
are still rejected. The installed-source matrix and two sanitized literal
Loomy CLI tool-turn captures are in `routercloud/testdata/host-fields/`.

The package does no network, filesystem, subprocess or background work. It uses
`http.ResponseWriter` and `io.Reader` supplied by the executor. The executor
continues to own the exact target, authentication, cancellation, total budget,
retry policy and events. The `idClock`/sequence and regexp/JSON reflection caches
are local codec state, not lifecycle jobs. Existing provider error descriptions
were moved with their pure parser to preserve legacy behavior; the cloud
executor still returns its own safe errors and never exposes upstream bodies.

This boundary alone does not prove product startup behavior. The product
integration must also check the built `cmd/api-router` dependency graph and run
an isolated process with temporary config and ports while observing network and
subprocess activity. No local model, judgment, redaction or host compatibility
claim follows from this extraction.

## Codex native Responses tools and history

The bounded cloud codec preserves the validated original Responses request,
including missing/null/string reasoning fields, item IDs, item order,
namespace declarations/calls, custom freeform input, additional_tools and
search annotations. Native Responses JSON-to-SSE reuses `ResponsesWholeEvents`
with the original objects; native SSE-to-JSON uses `nativeResponseCollector`
and the terminal response with completed output items. Neither direction
round-trips through the portable message IR. If a backend omits terminal
output, complete item events retain the full items in output-index order.
The existing bounded buffering, complete EOF requirement, safe errors and
nullable actual usage still apply.

`ResponseCapabilities` inspects only protocol containers, never tool arguments,
schemas, results or text. `ValidateCapabilities` checks the Profile's explicit
allowlist for namespace, custom, additional_tools and web_search on declarations
and subsequent history. This is a target assertion, not discovery or vendor
certification. The executor maps `CapabilityError` into a safe 400 with zero
outbound attempts. Basic native functions, including Codex goals, require no
enhanced tool capability. Reasoning history requires no additional permission.

Cross-protocol conversion rejects enhanced tools, reasoning configuration or
history, host metadata/include and independent Responses item identity with
specific capability codes. It preserves the existing proven portable subset;
it does not silently flatten namespaces, re-encode arbitrary freeform as JSON,
unseal tool schema markers or discard opaque reasoning/metadata. Current
namespace flattening in the legacy IR is therefore not a product capability.

`TestCodex0161CapturedNativeRequests` uses complete committed fixed-version CLI
captures, including three consecutive function calls, actual namespace calls,
custom execution and goals completion. The native output-mode matrix checks
all fields, IDs, ordering, null/opaque reasoning, parallel namespaces, custom
freeform and URL citations in JSON and SSE. These are deterministic fixture
replays; real product/cloud certification is separately recorded by the product
acceptance owner.

Native output validation binds item IDs/indexes, names, namespaces and call IDs
across added, argument, done and terminal events. Complete terminal snapshots
must agree with completed items; malformed function argument objects fail
closed, while custom input remains arbitrary literal text. Already delivered
stream data cannot be retracted after a later upstream contradiction, so the
codec emits only a safe error and withholds the successful terminal.

For native JSON-to-SSE, `ResponsesWholeEventsEach` preflights exact serialized
SSE bytes against 32 MiB before commitment, then regenerates one event at a time.
This avoids retaining the entire expanded event stream. Original JSON replies
within the input limit remain byte-preserving for JSON clients. The budget is
for input/output wire bytes and retained protocol buffers, not a total Go heap
ceiling. Legacy `ResponsesWholeEvents` collects the same generator for existing
callers; it does not introduce a second protocol implementation.
