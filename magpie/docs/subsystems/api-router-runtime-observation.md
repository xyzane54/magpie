# Local model observation contract

`routerruntime.LocalCallObservation` is the producer contract for the local
model ledger. One record describes one internal Manager call and has a unique
`call_id`; a Router business request supplies `request_id` and may produce a
judgment, execution and redaction record. Management checks and diagnostics set
`check:true` and leave `request_id` empty, so they cannot be counted as business
requests or savings.

The record carries only server-owned model/runtime identity and the versioned
prompt/material/suite labels, timing, a fixed safe outcome/error code and an
`attempted` bit. `attempted` becomes true only after the generation HTTP request
is sent to the local model; a tokenizer capacity probe alone does not count as
started inference. Queue and cold-load time is the measured combined
`queue_load_ms`; an unmeasured phase stays absent. Token fields reuse
`cloudcodec.ReportedUsage`: nil means the model omitted that count and an
explicit zero remains zero. No character count, estimate, prompt, response,
tool argument, entity, URL, credential or raw exception may enter the record.

`LocalCallObserver` is a best-effort completion hook. The Manager calls it only
after releasing scheduler state and never uses a callback error to change a
model result. A production sink must enqueue a detached copy without blocking;
its own persistence error must be surfaced through an explicit statistics
health/status field owned by the ledger subsystem. This package does not create
storage, request rows or a statistics page.

The fixed purposes are `judgment`, `execution` and `redaction`. Modes are
`formal`, `experimental` and `diagnostic`; experimental admission never changes
formal capability acceptance. Producers must preserve actual start/end times,
nullable measured phases and the final outcome for canceled, rejected and
failed calls, including calls that never reached the model.

`Settings` is the request's saved raw runtime-settings snapshot (zero means the
legacy shared judgment budget), with `SettingsFingerprint` derived from that
same snapshot. `ContextUsage` is an optional exact local tokenizer count plus
output reserve and effective window; absence remains unknown, and a rejected
input may exceed its window. `HTTPStatus` is zero/unknown or a safe integer
status. These fields support capacity/budget diagnostics without retaining the
request or local HTTP error response body. Dedicated safe stage error codes
preserve judgment/execution queue and inference timeouts independently from a
later successful cloud result.

Settings and model/runtime identity are captured together at the call boundary.
A request whose snapshot no longer matches the Manager cannot borrow the newer
runtime's identity; unavailable identity remains unknown. This binding is
separate from the [privacy specification and cache binding](api-router-runtime.md#configurable-context-and-budgets).

