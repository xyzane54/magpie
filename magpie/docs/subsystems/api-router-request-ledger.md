# API-router request ledger

[`internal/routerledger`](../../internal/routerledger/types.go) owns the minimal
business-request store. Its only runtime storage dependency is the existing pure
Go `modernc.org/sqlite` driver. It imports no legacy usage, settings, provider,
gateway or session lifecycle. The service opens `state/requests.sqlite` inside
the independent [API-router layout](api-router-storage.md), after acquiring its
existing process lock; that lock must exclude a second database owner.

`Open(path, Options{})` creates a new private regular file only when missing.
An existing zero-length, malformed, incompatible or invalid-row database is an
explicit error; it is never reinitialized or replaced with an empty history.
Symlink database files and a symlink immediate parent are rejected. The service
owns the parent directory policy through `dirs.Ensure`. Database mode is 0600
on POSIX; Windows relies on the user profile ACLs, as other independent storage
does. No database/SQL/file-download management endpoint is provided.

## Schema and data contract

Schema 2 is registered in SQLite `PRAGMA user_version` and in every projected
record's `schemaVersion`. The `requests` table stores immutable indexed
`request_id`, `started_ns`, `app_id`, `operation`, and mutable indexed `status`,
plus a bounded safe JSON projection in `data`. The exact owned table schema,
integrity and all persisted projections are validated before migration or restart
cleanup. Schema 2 adds the separate bounded `calls` table. Unknown tables, views
or triggers are incompatible. A valid schema 1 database upgrades in one SQLite
transaction together with restart interruption and retention: request snapshots
and actual usage survive, while new reference prices and local identities remain
unknown. No internal calls are invented for old requests. Invalid schema 1 rows
are checked before even expired rows can be deleted. Failed migration rolls back
the table, version and record changes; unknown future versions are rejected.

`Metadata` is a request-time snapshot of `requestId`, `appId`, `appName`, the
safe app-relative `path`, `operation`, `providerId` and `model`. Later Profile
renames or target edits do not change historical attribution. Request IDs come
from Router's entry boundary, never a client's correlation header. The store
does not generate identities or re-resolve a target. IDs are bounded literal
identifiers; display values are bounded UTF-8 without controls, URLs, private
query syntax or credential-header prefixes. The caller remains responsible for
passing configured snapshot fields instead of client content or credentials.
Allowed paths are empty or the exact retained `/v1/chat/completions`,
`/v1/responses`, `/v1/messages`, `/v1/messages/count_tokens` endpoints, with a
matching `generate` or `count_tokens` operation. No arbitrary client URL is
stored. Unsupported endpoints and discovery are not generation operations.
`referencePrice` copies the Profile's currency, `million-tokens` unit, separate
input/output prices and version/source at entry. It is nullable and immutable;
later edits cannot reprice retained requests. Only safe bounded version/source
display strings are accepted, so URLs and credential-shaped sources stay out of
the ledger. `Outcome.localModel` is the actual local execution identity, separate
from Metadata's configured cloud target `model`. `Outcome.localCallId` identifies
the exact internal execution that supplied the final local business result;
matching token values are not evidence that two calls are the same. A non-null
reference price must retain all six non-null fields. Missing input/output prices
in an existing projection are corruption, never an implicit zero/free price.

Records include UTC RFC3339 `startedAt` and nullable `endedAt`, total
`durationMs`, `status`, actual `destination`, `attempts`, `outputCommitted`,
`safeErrorCode`, `failureStage`, nullable `usage` and bounded `attemptDetails`.
The HTTP owner supplies `Metadata.AcceptedAt` from its own clock before request
preparation, so start and duration include configuration preparation, ledger
admission, body reading and execution. This internal field is not serialized;
standalone callers may omit it to use Start's clock. Future acceptance times
are rejected. Attempt duration, configured retry delay and actual
retry wait use `durationMs`, `retryDelayMs` and `retryWaitMs`. Incoming executor
nanoseconds must be converted explicitly. Each attempt has a number, start
time, HTTP status, safe codes, output state, retried state and nullable usage.
There are at most two actual attempts. A planned retry does not add an attempt.

Statuses are `processing`, `succeeded`, `failed`, `canceled`, `timed_out` and
`interrupted`. Destination is `none` before dispatch, `cloud` after a real cloud
attempt, `local` for final local execution, or empty when unknown. Output after a failed stream remains a failed
outcome with `outputCommitted=true`. `SafeCode` accepts only the fixed request
and cloud error vocabulary; unknown errors collapse to `router_error` in the
adapter. Store writes reject any unclassified error string. No raw error
message, target object, header, token, response body, prompt or tool content is
accepted by the data structures.

`Outcome.capabilityState` uses its own exact `SafeCapabilityState` vocabulary:
it is a local routing/fallback reason, independent of the terminal cloud error.
Unknown strings collapse to `router_error` in the adapter and raw writes are
rejected. `decision` is empty/local/cloud and judgment timing is nonnegative.

The fixed safe vocabulary also includes the native Responses capability codes
and `cloud_permission_error`; capability rejections have zero attempts and a
`validation` failure stage. Unknown capability-like strings are not accepted
by prefix. A text connection check with no completed text retains
`connection_check_no_text` rather than inventing success.

Each `Usage` field (`inputTokens`, `outputTokens`, `totalTokens`) is a nullable
nonnegative int64. A nil usage object or nil field means unknown; explicit zero
is known. Total is retained only when explicitly reported, never calculated
from incomplete fields. The adapter places final response usage on the business
record once and, if attributing it to an attempt, only on that final attempt.
Unknown failed-attempt usage stays unknown. Summaries do not estimate retry
billing or cache token savings. Nonnegative int64 values are retained exactly
in Go/SQLite; JSON consumers using JavaScript numbers lose exact precision
beyond 2^53-1. An overflowing aggregate is an explicit query failure, never a
wrapped or fabricated number.

## Lifecycle and progress

`Start(Metadata)` commits a processing row with SQLite synchronous FULL before
returning. A duplicate ID returns a conflict without replacing its snapshot.
`Finish(id, Outcome)` commits one terminal outcome. Subsequent terminal updates
are idempotent and preserve the first result. The service should end every
started record on all return paths, and must independently handle a ledger
write failure without appending another protocol envelope to an existing SSE
response. The adapter owns conversion of final errors/statuses and actual
executor results; the store does not run requests or change retry budgets.

Synchronous Observe callbacks may call `Progress(id, ProgressState)` for a
bounded in-memory projection of actual attempts, output, destination and stage.
It has a separate mutex, never performs SQL or waits for the database mutex,
and retains at most the active admitted records. Attempts and committed output
are monotonic. Get/List overlay these fields on processing rows. A successful
Finish removes the active entry, so a late event cannot resurrect a request.
`ErrNotFound` from a late Progress after Finish is benign. Detailed attempts
and usage are persisted at Finish; Observe does not serialize large payloads.

Normal records have `executionKnown=true`. On restart, leftover processing rows
are durably marked `interrupted` with `request_interrupted`, an end timestamp
and `executionKnown=false`; destination becomes empty. The interrupted duration
includes downtime and is not a measurement of upstream execution time. Live
attempt/output progress was memory-only, so the interrupted row's numeric
attempt value and output flag must be displayed as unknown. Restart never
claims that such a request succeeded or that no dispatch occurred.

## Capacity, queries and diagnosis

`RecordCall(Call)` synchronously persists a safe internal terminal observation;
the production completion callback must enqueue independently and never invoke
this method while holding model/scheduler locks. Call IDs are globally unique.
Repeated terminal observations preserve the first result; changing that ID's
request/scope/purpose is a conflict. A business call requires an existing parent
but may arrive after it finishes. A missing or expired parent is never recreated.
Management checks use `scope=check` and an empty request ID and cannot be assigned
to the latest business request. `mode=diagnostic` is accepted only for checks.
`Calls(requestID)` returns the parent's ordered calls; `Calls("")` returns checks.

Each observation contains only purpose, model/runtime identities and fingerprints,
material/prompt/suite versions, mode, start/end, fixed result/error vocabulary,
whether inference actually started, nullable actual usage and nullable measured
phase milliseconds. Calls may also carry validated raw `RuntimeSettings`, its
matching SHA-256 fingerprint, a bounded exact local-tokenizer `contextUsage`
(input/output reserve/window) and a safe integer HTTP status. Older observations
omit these fields; zero raw settings preserve legacy budget semantics. Capacity
counts are not billed usage. A capacity check can reject before inference starts,
retaining its HTTP status and counts while actual generation usage remains null.
A not-started observation cannot contain usage. Combined
queue/load duration is retained without fabricating separate phases. No content,
URL, error message, tool arguments or entity mapping field exists. Call JSON is
capped at 4096 bytes. The database's existing 128 MiB page/file cap still applies.

Defaults cap internal calls at 50,000 globally, 128 per retained parent and 5,000
checks; options allow up to 100,000 / 1,000 / 10,000 respectively. Excess events
fail explicitly rather than evicting deduplication identities while their parent
is retained. Business calls are deleted in the same retention/admission transaction
as their parent; checks expire by call start under the request age policy.
`ReportObservationFailure` increments an atomic, lock-independent failure counter
for dropped/invalid observations. `Status.observationFailures` remains visible
after later writes recover, alongside the existing storage failure diagnosis.

Defaults retain at most 10,000 rows and 30 days by request start time. Options
permit 1..100,000 rows and 1 second..365 days. Cleanup runs at Open and before
Start. It deletes expired terminal records, then oldest terminal records needed
to admit the new row. Active requests are never evicted; an entirely active
full ledger returns `ledger_capacity_exhausted`. Capacity cleanup and admission
share a transaction, so a failed admission cannot partially delete history.
At restart, interrupted rows become terminal and are eligible for retention.
There is no timer, goroutine, remote database, queue or background query.

SQLite is capped at 32,768 pages (128 MiB at the default 4096-byte page size).
Existing files over 128 MiB are rejected. Row JSON is capped at 8192 bytes.
DELETE rollback journals can temporarily require space in addition to the
database; page reuse bounds growth without vacuuming in a request hot path.
Writes and reads use one database connection with a two-second busy timeout.

`List(Query)` returns 50 rows by default, at most 100, in descending immutable
start time then request ID order. `Page.NextCursor` is an opaque keyset cursor;
newer inserts do not shift subsequent pages, including equal timestamps.
Filters are exact bounded app ID, status, operation and half-open `[From, To)`
start times. SQL arguments are parameterized. Invalid limits/cursors/filters
are errors, not silently expanded reads. `Total` counts the complete filter
without cursor/offset. Retention may remove rows between pages.

`Summary(Query{})` counts only `generate` by default; `count_tokens` requires
an explicit operation filter. It reports the retained matching window,
status counts, recorded attempts and per-field known token sums with known
and unknown request counts. Oldest/newest start times explain the retained
range. Recorded attempt totals are durable terminal data; live progress is
available per request in Get/List. Missing failed-attempt usage is excluded
rather than fabricated. Health checks, model discovery, management reads and
other nonbusiness calls never enter this store unless their caller explicitly
misuses Start; entry ownership defines which recognized preflight failures are
recorded.

`Status` reports availability, schema/capacity policy, current safe error code,
historical `failureCount` and `lastErrorCode`. Start/Finish validation errors,
write failures, corrupt query results and unavailable storage remain explicit;
the store never reports persistence success for a failed write. A later
successful durable Start/Finish restores availability while retaining the
failure diagnosis. Reopening starts a new process-local diagnostic counter.
`Close` is idempotent, has no queue to drain and leaves active rows for the
explicit restart interruption policy.

## Validation

[`store_test.go`](../../internal/routerledger/store_test.go) uses `testenv.Main`
and temporary database paths. It checks persistence and snapshot stability,
unknown/zero/partial usage, operation separation, cancellation/timeout/failure
states, one business record with two attempts, concurrent requests and repeated
Finish, keyset ordering and ties, retention/admission, corruption/schema
preservation, symlinks, injected SQLite write failure, fake-secret rejection,
memory-only progress, late events and numeric aggregate overflow. A deadlock
watchdog test holds the database mutex while Progress completes, proving the
Observe path has no dependency on SQL completion. The task delivery record
contains exact commands and integration evidence; ledger tests alone do not
prove product HTTP dispatch or real upstream requests.
