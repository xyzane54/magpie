# Generic protocol fixtures

These synthetic JSON/SSE fixtures use the public wire shapes already handled by
the product's three codecs. They contain fake identifiers and credentials only;
they are not captures from Loomy, WorkBuddy or a real vendor. The incoming beta
header shape in `protocol_contract_test.go` comes from the committed Claude Code
2.1.81 capture in `z-steps/host-cloud-acceptance/claude-code-2.1.81/requests.json`.

Each JSON request contains a complete text/tool exchange, an empty tool result,
a large integer and a nested `anyOf` in an ordinary `image` schema property.
Each SSE reply splits one function argument object across events. Tests also
split the wire into seven-byte writes and remove the final successful event to
check transport fragmentation and interrupted completion independently.

Loomy/WorkBuddy fixed-version request shapes remain X04; Loomy's refreshed,
expired and quota-bearing authorization samples remain X05; real target endpoint,
protocol, model, credentials and test quota remain X08. No host-specific private
state or tool bridge is introduced by these fixtures.
