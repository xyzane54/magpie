# API-Router Privacy Redaction and Reversible Restoration

## Purpose

Detect and redact sensitive PII, credentials, and user-defined confidential
terms from outbound cloud requests across all supported wire protocols, and
reversibly restore placeholders in both non-streaming and streaming responses.

## Entry Points

- Redaction engine & wire rewriters (`internal/routerredact/`):
  - `policy.go`: `Config`, `DetectorConfig`, `CustomEntry`, `DefaultConfig`,
    `ValidateConfig`
  - `engine.go`: `Engine`, `NewEngine`, `Session`, `Result`, `Match`
  - `detectors.go`: built-in detectors (`email`, `phone_cn`, `id_card_cn`,
    `bank_card`, `ip_address`, `api_secret`, `jwt_token`, `private_key`)
  - `stream.go`: `StreamRestorer` (chunk-safe placeholder restoration across
    SSE token boundaries)
  - `wire.go`, `wire_parse.go`, `wire_decode.go`: protocol-aware request
    redaction (`RedactWireRequest`), response restoration
    (`RestoreWireResponse`), and SSE stream wrapping (`WrapSSEStream`)
- Service HTTP handlers: `internal/routerservice/redaction.go`
  (`handleRedaction`, `handleRedactionPreview`)
- Frontend UI: `internal/routerservice/assets/redaction.js` (`#view-redaction`)

## Architecture

### Policy Configuration (`routerredact.Config`)

Redaction settings live under `redaction` in `config.json`:

- `Enabled bool`: master switch for outbound cloud redaction.
- `Mode string`: `"placeholder"` (replaces sensitive spans with reversible
  tokens such as `[REDACTED_EMAIL_1]` and restores them in responses) or
  `"mask"` (irreversible masking).
- `RestoreResponses bool`: whether placeholders in upstream cloud responses are
  restored to their original values before returning to the caller.
- `Detectors map[string]DetectorConfig`: per-detector enablement for built-in
  patterns (`email`, `phone_cn`, `id_card_cn`, `bank_card`, `ip_address`,
  `api_secret`, `jwt_token`, `private_key`).
- `CustomEntries []CustomEntry`: user-defined sensitive terms or regex patterns
  with optional replacement aliases and category labels.

### Request Inspection, Smart-Split Signal, and Outbound Redaction

1. **Sensitivity Signal for Routing Groups (`smart_split` & `has_sensitive`)**:
   - During routing-group evaluation (`internal/routerservice/group_routing.go`),
     the redaction engine inspects the request payload.
   - Under `smart_split`, requests containing sensitive matches (`hasSensitive`)
     can be steered according to group rules or redacted before cloud dispatch.
2. **Protocol-Aware Wire Redaction (`wire.go`, `wire_parse.go`)**:
   - `RedactWireRequest` traverses user/system text fields, tool arguments, and
     tool outputs across OpenAI Chat, OpenAI Responses, Anthropic Messages, and
     Gemini Native JSON payloads without corrupting structural fields (such as
     `model`, `role`, tool names, or JSON schema keys).
   - Generates deterministic per-request session placeholders
     (`[REDACTED_<CATEGORY>_<N>]`) and records match counts and categories in
     `routerredact.Result` for `routerobs` telemetry.

### Non-Streaming and Streaming Restoration (`wire_decode.go`, `stream.go`)

- **Non-Streaming (`RestoreWireResponse`)**: Scans assistant message content and
  tool-call arguments in the upstream JSON response and substitutes session
  placeholders back with their original values.
- **Streaming SSE (`StreamRestorer`, `WrapSSEStream`)**: Buffers only the minimal
  trailing prefix needed when a `[` placeholder spans multiple SSE chunks,
  emitting restored text deltas in real time without breaking SSE framing.

## Invariants

- Original sensitive plaintext values exist only in the per-request in-memory
  `Session` during active request execution and are never persisted to disk,
  `routerobs`, or `routerusage`.
- Local engine executions (`local/*`) stay on-device and do not send data to
  external cloud endpoints.
