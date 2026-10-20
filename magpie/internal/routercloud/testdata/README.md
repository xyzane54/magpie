# Offline host request fixtures

These fixtures include sanitized request bodies from the existing isolated host
captures for WorkBuddy 5.3.8, Codex 0.161.0 and Claude Code 2.1.81. The separate
`workbuddy-usage-fixture.json` is a synthetic usage-metadata request. They exercise
exact protocol fields, tool schemas and complete conversation histories against
local fake upstreams. Replaying them does not establish current host or cloud
service compatibility.

Only the request bodies used by the tests are kept. Capture headers, credential
source records, machine summaries, logs and responses are omitted. Existing
sanitized request values are preserved; any developer login name in captured
tool output is replaced with `fixture-user`. The `body` wrapper and Claude array
order preserve the existing test input shape.

The routerservice WorkBuddy HTTP regressions reuse `workbuddy/` directly so the
protocol and HTTP suites read the same fixtures. No test needs the local
development or acceptance evidence directories.
