These request bodies are extracted without filtering from the committed
`codex/host-cloud-acceptance` commit
`d1a0ec082ada2ec1e7e13fd8aa615e71d2fbcfe4`, under
`magpie/scripts/host-acceptance/evidence/codex-0.161.0/<mode>/request-1.json`.
Only the HTTP capture envelope was removed; the body is complete. They contain
isolated synthetic task context and public placeholder authentication, no keys.
The fixture names preserve the original capture modes. Replay proves native
wire preservation, not support by any particular remote model or provider.

The `executed-*` request bodies come from committed `d5586b1e`, under
`z-steps/codex-full-compat/captures/direct-{custom,multi,goals}/request-N.json`.
These combine real Codex CLI tools/scheduling with a deterministic mock upstream;
they are not remote-provider acceptance evidence. `direct-tools-2/3/4` retain
the complete second through fourth requests from the earlier committed capture.
