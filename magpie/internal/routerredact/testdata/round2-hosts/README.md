# Offline host replay inputs

`manifest.json` pins the exact SHA256 of each JSON file replayed by the redaction
tests. Its source paths are relative to the repository root and refer only to
checked-in test data. Shared WorkBuddy 5.3.8 captures live in routercloud's
`testdata/workbuddy/`; the cancel and cancel-only cases use the same request body,
as they did in the original capture set.

Keep JSON fixture files and the manifest encoded as UTF-8 with LF line endings.
The hashes cover the exact committed bytes, so a fixture edit requires updating
its manifest hash after preserving those line endings.

The usage follow-up and Loomy comment inputs are explicitly synthetic. They
exercise protocol field preservation and are not model recognition or real host
acceptance evidence. Request bodies retain their values and types; capture
metadata, headers, logs and machine summaries are not required or included.
