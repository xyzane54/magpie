# WorkBuddy request fixtures

`workbuddy-websearch-tool-result.request.json` is a reduced reproduction of the
2026-10-19 WorkBuddy 5.3.8 WebSearch continuation that Router rejected locally
with HTTP 400 / `unsupported_input`. The installed CLI's `itemsToMessages` and
text normalization functions reconstructed its three messages from the saved
session. String business content and identities were replaced consistently;
message fields, null content, tool linkage, usage numbers and object shapes
were retained. The session did not record the request's system prompt or root
tool definitions; those are not claimed as captured here.

The failing tool message includes the installed processor's `toolResult`
object alongside ordinary `content`. Its content/title/renderer fields are
retained in this fixture. Source inspection also showed that the ExitPlanMode
processor can attach this object to assistant function calls, and tool-specific
objects can carry additional business fields.

This fixture and mock regressions prove the checked serialization behavior,
not execution of WorkBuddy's actual search service or general host compatibility.
