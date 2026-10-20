# API-router execution observation adapter

[`routerobserve`](../../internal/routerobserve/observe.go) binds a Router-generated
request ID and an immutable safe app/profile snapshot to one external operation.
It imports no legacy gateway or usage subsystem. It does not read host sessions,
record bodies, alter execution budgets, send upstream requests or initiate retries.

The HTTP owner calls `Begin(store, routerledger.Metadata{...})` before reading the
admitted request body. Only registered applications and canonical POST generation
or `count_tokens` operations are admitted. Management, health and model-list
requests stay outside this ledger. Explicit management connection checks do not
enter the business ledger. An external generation endpoint remains a business
request: the service never guesses probe identity from client headers or merges
it with a later host request. The ledger never initiates probes. Metadata comes from the
URL-bound app configuration, never client-selected model fields or headers.
Application name, provider ID, cloud model and safe Profile reference price are
request-time snapshots. An unsafe legacy price description is omitted, with an
observation-failure diagnostic, while retaining the business record. Preflight
failure after admission remains a record with zero attempts and destination
`none`; outbound dispatch changes the destination to `cloud`.

```go
watch, ledgerErr := routerobserve.Begin(ledger, routerledger.Metadata{
    RequestID: state.ID, AppID: state.AppID, AppName: state.Target.Profile.Name,
    Operation: "generate", Path: "/v1/responses", AcceptedAt: acceptedAt,
    ProviderID: state.Target.Profile.Cloud.ProviderID,
    Model: state.Target.Profile.Cloud.Model,
})
// Diagnose ledgerErr separately using a fixed safe code. An unavailable ledger
// must not create a second model response or change cloud execution behavior.
defer watch.End("interrupted", "request_interrupted", "interrupted")
executor.Observe = watch.Observe
result, err := executor.ExecutePath(state.Context, writer, "/v1/responses", request)
finishErr := watch.Finish(result, err)
// Diagnose finishErr separately. Never append an envelope after committed output.
```

The snippet is a wiring contract; the product HTTP owner supplies its actual
configuration field names. If preparation fails before `State` exists, use the
Router-generated request ID and the already loaded registered app/profile snapshot,
then `End` with a fixed classified code, or `Reject` with a typed safe
`routerrequest.Error`. All return paths after a successful `Begin` must end it.
A deferred `End("interrupted", ...)` is a fallback: the first finish wins and
subsequent calls return the same persistence result. Nil watches are safe no-ops.
A failed `Begin` returns no watch and its error; no persistence success is claimed.

`Observe(routercloud.Event)` takes a short mutex and keeps at most two attempts in
memory. It performs no serialization, disk access, queries or goroutine creation.
When the sink implements `Progress`, the callback sends only a bounded safe
progress projection to the store's separate memory lock, outside the adapter lock.
Management reads can therefore see actual dispatch count and first output while a
stream is still active. The product writer's `output_committed` notification is
handled immediately, before `attempt_finished`; late progress cannot resurrect a
completed record. Unexpected progress errors are surfaced by `Finish`, while the
expected not-found result from a late event is ignored.
`Finish` projects the final `Result` then writes outside that mutex. Concurrent
finish calls wait for the first durable outcome. Storage errors return to the HTTP
owner; they are not swallowed or written to the response. Events are a fallback
for early ending; the executor's final `AttemptDetails` are authoritative.
Durations convert from executor nanoseconds to ledger milliseconds. A planned
retry becomes `Retried` only when another attempt actually starts. Cancellation
while waiting retains one actual attempt and failure stage `retry_wait`.

Failures distinguish failed, canceled and timed_out. A response failure after
output is committed remains failed with stage `response`. Successful generation
is `succeeded`; an unfinished handler may end as `interrupted`. The adapter reads
only allowlisted typed error codes, including context cancellation/deadline. Raw
errors and unknown codes collapse to `router_error`; messages are never read.
No target, URL, header, credential, session or content object is accepted by the
adapter interface or copied into an outcome.

`cloudcodec.ReportedUsage` and `routercloud.Result.ReportedUsage` retain raw
upstream `input_tokens`/`prompt_tokens`, `output_tokens`/`completion_tokens` and
explicit `total_tokens`, each as nullable `*int64`. This is separate from the
compatibility `Result.Usage` IR, which normalizes cache tokens and requires both
primary fields. Partial reported usage survives, explicit zero remains known,
and absent totals are never derived. Later streaming counts replace earlier ones,
including an explicit zero. Cache subdivisions are not collected in this ledger;
reported input is never added to a cache subtotal. Unknown and ill-typed/negative
counts stay unknown. The parser only examines protocol usage envelopes and never
content/tool objects.

The business record and the final actual attempt own final response usage; earlier
failed attempts retain unknown usage. No final usage is copied to every retry and
no per-attempt values are summed into another business total. Partial actual usage
reported before a failed stream remains available. A retried, uncommitted read
failure resets its usage before dispatching the second attempt. Auxiliary
`count_tokens` operations keep their operation type and have no generation usage.
Two external tool-loop turns create two records; sessions are never merged.

Loopback tests in [`observe_test.go`](../../internal/routerobserve/observe_test.go)
exercise the actual executor, persistent store and mock HTTP upstream for success,
retry, canceled retry, zero-attempt rejection, timeout, partial-output failure,
usage presence, immutable snapshots, secret exclusion and auxiliary counting.
Concurrent tests deliberately block persistence and verify the synchronous callback
still returns, and duplicate finishes keep a single durable result.
[`usage_presence_test.go`](../../internal/routercloud/usage_presence_test.go)
checks all three protocols, raw input before IR cache normalization, partial/missing
counts, explicit zero and streamed count replacement. These are local mocked
upstream checks; they do not establish real cloud account behavior.

## Local completion contract and bounded consumption

Round 4 consumes [`LocalCallObservation`](../../internal/routerruntime/local_observation.go),
the A contract from `54997a0b2e3b93384b91b5b219f090e78e633f98` (locally cherry-picked
as `6d1836df`). Only that contract was imported, without A's business changes.
[`Calls`](../../internal/routerobserve/calls.go) implements its observer directly.
The callback copies nullable fields into a 256-event queue and returns without
SQL or waiting for a database/model lock. Its single worker persists terminal
events; queue overflow and every failed write increment the atomic
`ledger.observationFailures`. Store failures also retain their safe diagnosis.
Accepted events drain after handlers and Manager shutdown, before ledger close.
This is bounded best-effort observation; it is not a durable model-call queue.

[`observation_context.go`](../../internal/routerruntime/observation_context.go)
provides the cycle-free producer seam `LocalCallObserverFromContext(ctx)`.
The HTTP boundary binds that observer to Router's existing ID through
`WithLocalObserver`; checks bind `Check=true` with no business ID. Cross-request
IDs are rejected rather than assigned to a nearby request. A's Manager producer
must publish its completed safe events to this context observer. The contract-only
baseline does not yet do so: actual Manager producer/consumer integration remains
an explicit A/C integration task, not proven by fixtures.

The execution completion also captures actual model identity, `localCallId` and
reported usage in memory. Local final results can carry actual usage without
cloud attempts. A non-nil `Result.ReportedUsage` stays authoritative; otherwise the
same execution event supplies local actual usage. Totals remain nullable. Final
cloud fallback never inherits failed local usage into business results. The exact
`localCallId` marks business execution usage so it is not also overhead, including
partial-output failures; failures never count as savings. Events retain model and
runtime fingerprints, prompt/material/suite versions and formal/experimental/check
mode, without body or error text. Timings persist measured whole milliseconds
(fractional milliseconds are truncated); combined queue/load is not split.

`core_statistics_test.go` exercises synthetic contract producers through actual
HTTP routing, SQLite and management APIs, plus unsafe legacy price handling.
`routerobserve` tests cover concurrent duplicates, delayed events, overflow,
write failure visibility and callbacks while persistence is blocked. These tests
do not run a model or establish real A/C producer wiring.
