# Running this project

For an ordinary "start the project" request on Windows, run
`powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/start-project.ps1 -NoBrowser`
from the repository root. Use `-CheckOnly -NoBrowser` for a read-only check.
The entry uses `magpie/dist/api-router.exe` and the Windows user's ordinary
configuration/data directories, checks the authenticated service identity and
the configured WorkBuddy endpoint, and reports the actual management URL.
The native service automatically binds another loopback port if the saved port
is occupied, synchronizes already connected WorkBuddy/Loomy endpoints, and
persists the bound address. Always use the returned management URL.

Do not select an EXE or port from historical acceptance records under `z-steps/`,
an unrelated worktree, or a removed temporary directory. Those instances used
separate APPDATA/LOCALAPPDATA and are not the ordinary project startup.
Do not change a host's endpoint merely to match a newly started test instance.
If an endpoint differs, inspect the live service, configuration roots and host
recovery record before applying the documented host transaction.

Native managed startup archives a recognized lock only after the recorded
process is proven exited or its PID reused. An unrelated listener on the old
port stays running; automatic port selection handles it. Unknown/unreadable
locks stay intact. Never kill a port owner to make startup succeed. Keep model,
provider and host settings when switching builds. Follow `magpie/AGENTS.md`
before source changes.
