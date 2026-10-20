# Pinned artifact installation

`Download` requires a confirmed positive byte size, SHA256 and HTTP(S) URL.
It writes `<destination>.part` and atomic `<destination>.part.json` metadata.
Range and If-Range resume persisted bytes after cancellation or a new process.
A full 200 response resets the partial; changed validators or malformed partial
responses reset and retry once; a mid-stream read stall (`StallTimeout`, default
30 s) that already flushed new bytes resumes via Range up to three times.
Truncated bodies remain resumable. Wrong size or digest removes the partial and
never publishes an installed file. A flushed, verified partial is renamed to the
final path; installation readers use `Verify`, not file existence or progress,
as their source of truth.

Every writer holds an OS file lock at `<destination>.lock`; competing calls,
including other processes, return `ErrBusy`. The OS releases ownership on exit;
the empty lock file remains deliberately. No stale lock is guessed or removed.
Disk checks reserve remaining download bytes plus the artifact's expanded bound.
An unavailable disk probe fails closed. Actual write/flush errors also propagate.
Callbacks run synchronously, so callers should publish progress without blocking.

`Extract` first verifies a ZIP or tar.gz against the pinned artifact. It applies
the positive `ExpandedBytes` hard bound, a 10,000-entry ceiling, portable path
checks and regular-file-only extraction. Links, traversal, Windows device/ADS
paths, duplicate/case-colliding entries, unexpected lengths and CRC failures are
rejected. Temporary extraction stays in an adjacent private directory until a
complete tree and its `.router-verified.json` receipt can be renamed atomically.
The receipt records exact file sizes and SHA256 values and binds them to the
archive digest and size. `VerifyDirectory` rehashes files and rejects missing,
modified, extra or linked entries. Verification never creates missing parents.

A verified existing installation is reused. A damaged existing directory fails
closed; the manager must stop users of that installation and explicitly remove
or quarantine it before repair. Extraction does not replace possibly live files.
The receipt protects ordinary corruption and version reuse; it is local state,
not a signature against an adversary able to rewrite both receipt and payload.
The caller owns consent, catalog selection, credentials and lifecycle state.

`ExtractSelected` handles a pinned ZIP whose complete contents are not intended
for redistribution. Its caller supplies the compiled `SelectedFile` allow-list:
exact archive member path, flat output name, positive size and SHA256. All ZIP
member headers, including unselected members, must satisfy the same path,
duplicate/case-collision, regular-file/directory, entry-count and declared
expansion limits. Only selected members are decompressed or written; each must
exist at the exact recorded path and length and pass its pinned digest. The
result is a separate flat extraction tree with a v1 receipt. Reuse calls
`VerifySelectedDirectory`, which checks the complete file set and each compiled
pin independently of the local receipt. A modified receipt cannot authorize a
different selected member, digest, extra file or subdirectory. The caller still
owns redistribution eligibility; selecting a file is not a license decision.

`Assembly` preserves the original base and selected dependency trees and
publishes a separate execution tree. A safe `OverlayPrefix` places the flat
dependency files beside the executable, replacing only the corresponding
planned filenames in that tree. The source and destination directories must be
disjoint. Every `Install` and `Verify`, including reuse of an existing execution
tree, first calls `VerifyDirectory` on **both source trees**. It then derives the
complete expected file plan from those verified source receipts; an output
receipt never defines the accepted source files. Callers with a selected-file
contract must additionally recheck that source using
`VerifySelectedDirectory` and their compiled allow-list.

The execution tree uses `.router-verified.json` receipt **version 2**. It records
the base and overlay archive SHA256, an identity over both fixed artifact
descriptions and the overlay prefix, and the exact sorted output file plan.
Verification compares that receipt to a freshly derived plan, rehashes the
execution files and rejects missing, modified, extra, linked or inconsistent
entries. A valid-looking execution tree cannot compensate for missing or
damaged sources. Assembly reserves the combined expansion bound, writes a
private sibling stage, flushes and verifies copied bytes, and publishes the
complete tree atomically. Existing valid trees are reused; invalid ones fail
closed until their lifecycle owner stops users and explicitly repairs them.
These local receipts retain the ordinary-corruption boundary described above;
they do not establish publisher signatures or replace fixed catalog pins.

Tests use `testenv.Main`, temporary directories and local `httptest` servers.
`TestDownloadResumesAfterChildProcessExit` crosses a real process boundary.
Run from `magpie/`: `go test -tags nogui ./internal/routerdownload`; coordinate
the shared heavy-test lock before `go test -race -tags nogui ./internal/routerdownload`.
Windows evidence is an actual Windows test executable run on the user's PC;
WSL tests and cross-platform vet alone do not establish Windows behavior.
