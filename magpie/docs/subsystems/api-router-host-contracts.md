# API-router host configuration contracts

WorkBuddy 5.3.8 is pinned to the installed Desktop/Agent artifacts, separately
from Magpie's retired subscription provider. Its [`WorkBuddyAdapter`](../../internal/routerhost/workbuddy.go)
accepts strict JSON `models.json` as a root array (Desktop writer) or an object
with `models` (CLI writer), and JSONC `settings.json`. It manages only the
selected raw model ID's `url`, `apiKey`, `supportsToolCall`, `useCustomProtocol`
and `settings.model`. Selection is `custom-local:<raw ID>`; the wire model is
the raw ID. The installed builder appends `chat/completions` to
`/app/workbuddy/v1`. Source hashes, precedence and minimal sanitized samples are
in the installation investigation (historical development record; not distributed).
See [host configuration](api-router-host-config.md#workbuddy-538) for transaction
and offline restoration, and the delivery record (historical development record; not distributed)
for separate automated and real-cloud results.

Current HTTP input boundary (T2.1/T2.2, 2026-10-16): incoming beta headers are
explicitly rejected with zero outbound work. In particular, the historical
Claude captures below contain `anthropic-beta`; earlier simulated CLI acceptance
does not mean those default requests pass the current HTTP boundary. No host
configuration or feature flag is rewritten to hide this limitation. See the
generic protocol and dependency matrix (historical development record; not distributed).

The C adapter in [`routerhost/formats.go`](../../internal/routerhost/formats.go)
plans a change to one explicitly selected host directory. `BuildFiles` only reads
that directory; it never scans other locations, imports cloud settings, changes
credentials in Router, registers plugins, starts processes or migrates Magpie
state. The only upstream reuse is the pure `edit.PatchJSON` setter (with a C-local comment-preserving member deletion helper); the
`internal/agent` registry and lifecycle are deliberately excluded. Backups,
operation records, connection checks, writer locking, stale-plan checks, atomic
writes and offline restore CLI belong to the owning host transaction subsystem.

## Pinned format baseline

| Contract | Codex CLI 0.114.0 | Claude Code 2.1.81 |
| --- | --- | --- |
| User config | `$CODEX_HOME/config.toml`, default `~/.codex/config.toml` | `$CLAUDE_CONFIG_DIR/settings.json`, default `~/.claude/settings.json` |
| Explicit test root | `Options.ConfigRoot` is the selected Codex directory itself | `Options.ConfigRoot` is the selected Claude directory itself |
| Files changed | Only `config.toml`; `auth.json` is not touched | Only `settings.json`; `.claude.json` and keychain are not touched |
| Model | Root `model` | `env.ANTHROPIC_MODEL` |
| Router URL written | `model_providers.api_router.base_url = "http://127.0.0.1:8317/app/codex/v1"` | `env.ANTHROPIC_BASE_URL = "http://127.0.0.1:8317/app/claude-code"` |
| Final messages endpoint | Appended `responses`: `/app/codex/v1/responses` | SDK appends `/v1/messages`: `/app/claude-code/v1/messages` |
| Public placeholder | `model_providers.api_router.experimental_bearer_token = "api-router-local-placeholder"` | `env.ANTHROPIC_AUTH_TOKEN = "api-router-local-placeholder"` |
| Protocol selection | Root `model_provider = "api_router"`; provider `name = "API Router"`, `wire_api = "responses"`, `requires_openai_auth = false`, `supports_websockets = false` | Anthropic Messages, selected by Claude Code's normal SDK |

These versions are format baselines, not a claim that either executable has
passed real tool tasks. The adapter accepts an origin or exactly this host's
`/app/<host>/v1` endpoint, strips a trailing slash, and rejects other path prefixes,
query strings, fragments and embedded URL credentials. The CLI transaction layer
further restricts the Router origin to the supported local service address.
The Claude base intentionally omits `/v1`: writing it would duplicate the SDK
path. Ordinary applications require no Router credential. The generator writes
the public value `api-router-local-placeholder` into fields that the host format
expects; users do not manage a secret token or environment variable. Router does
not validate or forward this value. Management tokens are used only to read
`/api/apps`, never in host settings or model probes. Cloud credentials remain in
the application/Profile binding and existing Router provider storage. This adapter
has no provider-selection logic and never copies host cloud keys into Router.

Codex direct bearer configuration is supported but discouraged by the official
reference in favor of `env_key`. This single-file integration uses a public
placeholder. Backups and the host file must remain private because original
settings may contain secrets. No shell/environment credential file is needed. Authentication conflicts in an existing `api_router`
provider (`env_key`, `auth`, headers or query parameters) fail closed; rename that
provider or resolve its overrides explicitly before applying. Other provider
fields and all other providers remain untouched.

## Source verification and precedence

Checked on 2026-10-15 using official OpenAI documentation (the OpenAI Docs skill)
and vendor sources:

- [OpenAI configuration reference](https://developers.openai.com/codex/config-reference)
  and [advanced configuration](https://developers.openai.com/codex/config-advanced)
  document custom providers, direct bearer auth and Responses configuration.
- [Codex 0.114.0 release](https://github.com/openai/codex/releases/tag/rust-v0.114.0)
  fixes the baseline. Its
  [provider declaration](https://github.com/openai/codex/blob/rust-v0.114.0/codex-rs/core/src/model_provider_info.rs)
  explicitly supports `experimental_bearer_token`, `requires_openai_auth` and
  `supports_websockets`. Its
  [URL builder](https://github.com/openai/codex/blob/rust-v0.114.0/codex-rs/codex-api/src/provider.rs)
  appends the resource to the complete base prefix, rather than replacing it.
- [Claude Code 2.1.81 release](https://github.com/anthropics/claude-code/releases/tag/v2.1.81),
  [settings](https://code.claude.com/docs/en/settings),
  [environment reference](https://code.claude.com/docs/en/env-vars) and
  [gateway documentation](https://code.claude.com/docs/en/llm-gateway)
  establish user settings, `env`, the custom endpoint and Bearer token variables.
  The current documentation is rolling; later-version features are not assumed
  to exist in 2.1.81. The fixed
  [official npm archive](https://registry.npmjs.org/@anthropic-ai/claude-code/-/claude-code-2.1.81.tgz)
  was also downloaded and inspected without execution: package.json declares
  2.1.81; cli.js reads the three environment variables, constructs the SDK
  base URL by concatenating the complete prefix with the message resource path,
  and sends Messages to `/v1/messages`. Its SHA256 is
  `875acc67241a94a6220b559712aff0b2c226458507648c308c73c9788bc45b2f`.
  This source inspection establishes the path contract, not runtime acceptance.

Codex CLI flags, selected profiles, project configuration and managed policies can
supersede user defaults. Existing threads can keep their original provider;
restart the host and create a new conversation for validation. The adapter does
not rewrite profiles, trusted-project layers or thread history. Claude Code
managed settings, `--settings`, project/local settings and explicit `--model` can
supersede user settings; shell/environment interactions are field-specific.
`ANTHROPIC_MODEL` and `--model` can override the normal model setting. Existing
alternative provider switches (Bedrock/Vertex/Foundry), shell credentials or
higher-priority settings must be checked by the host operator. They are not
silently cleared. The transaction's Router checks cannot prove what a separately
started host actually loaded; verify the host's effective config and final
request path with an isolated host process before claiming live integration.

## Editing and restoring

TOML is validated with `go-toml` (including duplicate definitions), then scalar
values are located using the parser's syntax tree and spliced in place. Existing
spacing, comments, unknown keys and unrelated tables are retained. Inline provider
objects and dotted provider layouts that cannot safely accommodate required
fields are rejected with a normalization instruction. Malformed files, wrong
field types, symlinks and unreadable paths are refused, never treated as absent.
JSON/JSONC is validated recursively with duplicate-key rejection before using
`edit.PatchJSON` for values and C-local syntax deletion for absent originals; comments beside removed fields remain. A scalar `env` is refused rather than replaced. Empty existing
JSON files are invalid. Pure additions and restored values preserve line endings.
No file is serialized as a whole from an object map.

`RestoreContent` compares the original, applied and current semantic value of
only fields the operation actually changed. Current values already equal to the
original make restoration idempotent. A different later value on any changed
field aborts the complete file plan with a field-name-only conflict; no values
or parse snippets are returned in errors. Later unrelated values and comments
remain. A tool-created empty container is removed only when that is safe;
comments in a container keep it present. If the current file exactly equals the
recorded applied bytes, the complete original bytes are safe to restore. If the
file originally did not exist, only that exact unmodified case yields `nil` for
the transaction layer to remove it; user-added content keeps the file.

## Verification and remaining acceptance

`formats_test.go` uses only `t.TempDir` directories and fake tokens. Run from
`magpie/` with an isolated HOME and pinned Go module/cache locations:

```sh
go test -race ./internal/routerhost
go vet ./internal/routerhost
```

Coverage includes missing/existing config, repeated apply/restore, comments,
CRLF, unknown fields, retained original secrets, later unrelated changes,
conflicting field edits, ambiguous/damaged config, unsupported provider shapes,
symlinks, URL composition and non-disclosure in errors. Filesystem transaction,
backup loss, stale plans and multi-file rollback are tested by the transaction
owner. These fixtures do not run Codex or Claude Code, do not call cloud models,
and do not establish Windows/macOS native filesystem evidence, real streaming,
multi-turn tool tasks, or I1 completion. Effective-setting priority, host auth
selection, long-lived host restart behavior and actual requests still require
isolated fixed-version host integration after A/B are available.

Adapter verification on Linux amd64: isolated-HOME `go test -race -count=1
./internal/routerhost` and `go vet ./internal/routerhost` passed; `gofmt -l` and
`git diff --check` produced no output. A mutation retaining `/v1` in the Claude
base made `TestFormatEndpointContract/claude-code` fail with the duplicated
`/app/claude-code/v1/v1/messages` path; restoring the implementation passed.
No fixed-version host executable or real credential was used.

## Codex 0.161.0 default-feature acceptance

The format-only tests above are distinct from the opt-in fixed executable
acceptance added on 2026-10-16. `scripts/host-acceptance/codex_full_capture.py`
runs Codex 0.161.0 with isolated HOME/CODEX_HOME, a persistent temporary thread,
bounded requests and its own product process. A source credential directory is
copied only when explicitly supplied; the CLI has a public placeholder and only
the product sends the bound upstream credential.

Against product commit `0ea0a933`, real `qwen3.8-flash` Responses acceptance
completed default thinking with three sequential shell calls, namespace subagent
execution and result aggregation, goal creation/read/completion, and real search
followed by a cited assistant message and shell execution. Thinking, multi-agent,
goals and search remained enabled. The goal tools require a persistent thread;
`--ephemeral` causes a host-side rejection and is unsuitable for this test.
The [official Goals guide](https://developers.openai.com/cookbook/examples/codex/using_goals_in_codex)
also describes goals as persisted thread state.

Custom/additional_tools round trips were exercised with the fixed executable and
a simulated upstream, not with the real Qianwen target. Qianwen search returned
real source URLs but empty annotations. A separate search/shell parallel task
failed to cite results after Codex omitted action.sources from its next history;
that failed task is retained and is not covered by the sequential success claim.
The script's real-cloud Profile declares only the verified namespace/web_search
capabilities. The complete scope, exact build hash, request/response captures and
reproduction commands are in the
host delivery (historical development record; not distributed).

The four real-cloud tasks were repeated after the native SSE identity and buffer
guards at final product commit `8dd25043`: three shell calls (4 requests), a real
child with spawn/wait/close and returned tool output (6), goals with explicit
create/get/update results (6), and cited search followed by returned shell output
(2). Every CLI exited zero with the fixed model and namespace/web_search Profile.
Acceptance now checks call IDs and structured lifecycle states; a model that
claimed to read a goal while omitting get_goal was correctly rejected. The failed
attempts and the final successful captures are retained in the delivery above.
## Earlier restricted runtime acceptance on 2026-10-16

This historical run used the host-acceptance branch before the complete Codex
compatibility integration described above. Its restrictions and cloud failures
below apply to that earlier build and target, not to the later native Responses
acceptance. Claude beta-header limitations remain unchanged.

The historical format baselines above remain distinct from executed evidence.
This round ran Codex **0.161.0** at `$HOME/.local/bin/codex` and Claude Code
**2.1.81**, directly through Node 22.23.2 from the existing official package at
`/tmp/api-router-c-format-reference/package/cli.js`. The latter is not installed
on PATH. Both ran on WSL2 Linux x86_64, not native Windows or macOS. Exact source
commits, executable hashes, commands and per-host results are in the
acceptance delivery (historical development record; not distributed).

Both actual CLIs completed four streaming requests through the built product
HTTP entry and native-protocol loopback upstreams. Codex really ran three shell
commands, wrote `42`, verified it and returned the tool output in the next
request. Claude really ran Read, Edit and Bash, repaired a Python function and
returned successive tool results. Cancellation closed the upstream connection.
These are real CLI tasks with simulated model decisions, not real-cloud tool
acceptance. The native body fixtures are replayed by
[`host_acceptance_test.go`](../../internal/routercloud/host_acceptance_test.go):
all JSON values except the bound model must survive, and unsupported protocol
conversions must produce zero outbound attempts.

Codex's tested functional subset explicitly uses `web_search = "disabled"`,
`[features] multi_agent = false` and `goals = false`, leaving shell tools enabled.
`model_reasoning_summary = "none"` does not remove `reasoning`, `include` or
`client_metadata`. The fixed CLI source constructs those fields even with
summary disabled. At that earlier build, default namespace, custom tools and `additional_tools`
were rejected; no generated host configuration silently turns those capabilities
off. Codex 0.161.0 also attaches an `id` to `function_call_output`; validation now
accepts a nonblank string ID and retains its `call_id` and output natively.
Client metadata values must be strings, including JSON encoded as a string.
Nested objects, numbers, booleans and null are refused before an upstream call.
A non-null `previous_response_id` is still unsupported; no history is invented.

Claude's default request includes adaptive thinking, effort, cache hints and
`metadata.user_id`. The separately captured explicit restricted configuration
uses `CLAUDE_CODE_DISABLE_THINKING=1`, `DISABLE_PROMPT_CACHING=1` and
`CLAUDE_CODE_EFFORT_LEVEL=unset`; the actual CLI then omits those reasoning/cache
controls while retaining real tools. It still sends metadata and remains
rejected for Chat translation. This is a recorded restriction, not default
feature support. Opaque simulated thinking signatures survive a native CLI
round trip; no genuine vendor signature or genuine Anthropic account was tested.

The captured Claude CLI sends `anthropic-beta` headers, while the current
executor contract forwards no incoming beta header. Native mock success cannot
establish those features against a real Anthropic service. Supporting or
explicitly rejecting those semantic headers belongs to the integration seam;
see the minimal blockers (historical development record; not distributed).
Neither fixed task invoked count_tokens or model discovery; native count tests
are automated protocol evidence only, and Chat counting is never fabricated.

Using the saved Codex application's actual DeepSeek `deepseek-v4-pro` / `chat`
binding, real product text and streaming returned completed text. With the
bounded model probes both hosts applied successfully, then their actual CLI
requests were explicitly refused for unsupported cross-protocol semantics.
Both restored offline and preserved a later unrelated setting. Neither host passed a real-cloud tool task in that earlier run. A successful apply establishes its limited model
probe, not the separately launched CLI's full protocol compatibility.
