# API-Router Cloud Protocol Translation and Execution

## Purpose

Normalize incoming agent requests across OpenAI Chat Completions, OpenAI
Responses, Anthropic Messages, and Gemini Native protocols, translate them to
the configured upstream provider's wire format, and adapt streaming or
non-streaming responses back to the caller's protocol.

## Entry Points

- Canonical request normalization (`internal/routerrequest/`):
  - `normalize.go`: `CanonicalRequest`, `Message`, `ContentPart`, `Tool`,
    `ToolCall`, `Protocol`, `NormalizeRequest`
  - `normalize_chat.go`: OpenAI Chat Completions & Responses parsers
  - `normalize_protocols.go`: Anthropic Messages & Gemini Native parsers
  - `normalize_tools.go`: canonical tool definition & tool-call normalizers
- Upstream validation & probing (`internal/routercloud/`):
  - `validate.go`: `ValidateConfig`, `Probe`, `BuildProbeRequest`
  - `validate_chat.go`: OpenAI Chat & Responses request/response validators
  - `validate_protocols.go`: Anthropic & Gemini request/response validators
- Runtime execution & stream adaptation (`internal/routerservice/`):
  - `cloud.go`: `executeCloud`, `buildUpstreamRequest`, `adaptCloudResponse`
  - `models.go`, `models_exec.go`, `models_writer.go`: local & cross-protocol
    streaming SSE / JSON response writers
  - `routing.go`, `group_routing.go`: route resolution and fallback execution

## Architecture

### Canonical Request Model (`routerrequest`)

Incoming requests on `/v1/chat/completions`, `/v1/responses`, `/v1/messages`,
and `/v1beta/models/{model}:{action}` are parsed by `NormalizeRequest` into a
protocol-agnostic `CanonicalRequest`:

- Preserves `Protocol` (caller wire format), `Model`, `Stream`, `Messages`,
  `System`, `Tools`, `ToolChoice`, `MaxTokens`, `Temperature`, `TopP`,
  `ReasoningEffort`, and raw extension fields (`Extra`).
- Normalizes multimodal content parts (`text`, `image_url`, `input_audio`,
  `tool_use`, `tool_result`) and assistant tool calls across all four protocols.
- Estimates prompt token count (`EstimatedInputTokens`) and structural flags
  (`HasTools`, `HasVision`) used by `RuntimeJudge` and routing-group rules
  (`group_routing.go`).

### Outbound Translation and Upstream Execution (`cloud.go`)

When a cloud candidate (`<providerId>/<model>`) is selected by an `AppProfile`
or routing group (`executeRoute` / `executeGroupRoute`):

1. **Redaction Hook**: If redaction is enabled, `routerredact` inspects and
   rewrites sensitive spans in the outbound payload before protocol translation
   and stores the per-request token map for response restoration.
2. **Protocol Translation (`buildUpstreamRequest`)**:
   - If the caller protocol matches `CloudProvider.Protocol` and no cross-format
     rewrites are required, the request body is forwarded with the target
     upstream `model` and redacted messages.
   - When the caller protocol differs from `CloudProvider.Protocol`, the
     canonical messages, system instructions, tools, and stream flags are
     serialized into the upstream provider's target protocol (`openai_chat`,
     `openai_responses`, `anthropic_messages`, or `gemini_native`).
3. **Authentication & Headers**:
   - Injects the resolved `ProviderSecret.APIKey` using `AuthMode` (`bearer`,
     `x_api_key`, `x_goog_api_key`, or `custom_header` with `HeaderName` /
     `HeaderPrefix`) plus any custom `CloudProvider.Headers`.
4. **Response & Stream Adaptation (`adaptCloudResponse`, `models_writer.go`)**:
   - Non-streaming responses are translated back to the caller's wire protocol,
     restored via the redaction session map (`RestoreApplied`), and usage tokens
     are extracted for `routerusage`.
   - Streaming SSE responses (`text/event-stream`) are decoded chunk-by-chunk,
     passed through the streaming redaction restorer, and re-encoded as OpenAI
     Chat chunks, OpenAI Responses events, Anthropic Messages events, or Gemini
     SSE chunks matching the caller's protocol.

## Invariants

- Caller protocol formatting is preserved end-to-end regardless of whether the
  request is served by a matching cloud protocol, a translated cloud protocol,
  or a local inference engine.
- Upstream API keys and custom provider auth headers are never echoed back to
  downstream callers or recorded in observation logs.
