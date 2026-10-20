# Installed host field audit, 2026-10-19

`installed-contract.json` contains 51 single-feature reproductions derived from
WorkBuddy 5.3.8's installed `codebuddy.js` constructors/serializers and Loomy
0.9.39's hash-matched OpenCode source map. WorkBuddy messages were produced by
the installed `itemsToMessages` and content normalization functions, using
synthetic values shaped by the observed producers. These entries are source
reproductions, not literal wire captures.

The two `loomy-installed-cli-turn-*` entries are sanitized literal HTTP requests from the installed
Loomy OpenCode executable. It ran with a fresh HOME/config/workspace, disabled
default/external plugins, updates, external skills and model fetching. A local
mock provider requested a read of a synthetic file, then returned the final
marker. Runtime prompt strings, tool argument values, results and identities
were replaced consistently; protocol fields, schemas and JSON types remain.
No live host task, login or credential was used.

The installed OpenCode executable and the executable beside the source map
both have SHA256
`e3d5f71df3fffa86ab698a36ef7c0cb36e3d69edb0efc33f83a62626a0fec3fb`.
The compatible SDK is bundled in the source map; the default integration uses
`@ai-sdk/openai-compatible`, with no assumption about the meaning of arbitrary
Provider options. `promptCacheKey` and `reasoningSummary` retain their literal
source-emitted spelling on native Chat.

Regression checks cover field/type/role boundaries, routing normalization,
native request preservation, zero outbound work for lossy conversion, metadata
privacy scanning, and both application HTTP routes with JSON/SSE and routing
enabled/disabled/unavailable. These checks do not establish media support,
every upstream vendor's support for every control, or future host versions.
