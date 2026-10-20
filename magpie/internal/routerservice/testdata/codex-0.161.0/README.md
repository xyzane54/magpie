# Codex native history fixtures

The `verified-final-*` request bodies come from the existing isolated Codex
0.161.0 host captures for tools, multiple tools, goals and web search. The tests
replay them through the current product HTTP stack against a local fake upstream;
they are regression inputs, not evidence of a new remote acceptance run.

Each file retains only the `body` used by the tests. Capture headers, credential
source records, machine summaries and logs are omitted. Developer login names in
captured tool output are replaced with `fixture-user`; the protocol fields, tool
schemas and conversation history are otherwise preserved.
