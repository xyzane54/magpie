# API-router fixed Windows delivery

[`routerdelivery`](../../internal/routerdelivery/delivery.go) owns a local,
explicitly chosen Windows amd64 package manifest and receipt-managed program
files. It does not initialize config, persist model intent, run a service,
touch host settings or download anything. CLI service/host coordination remains
with the [service owner](api-router-service.md); application data follows the
[independent storage layout](api-router-storage.md).

[`package-windows.ps1`](../../scripts/package-windows.ps1) builds only
`cmd/api-router` with CGO disabled, `-trimpath`, disabled VCS stamping and a fixed
version/build ID. The ZIP has a fixed order/date and exactly four flat files:
the EXE, user guide and install/uninstall scripts. The external schema-1
manifest pins product/version/windows/amd64, the ZIP's size/SHA256 and every
member's name/size/SHA256; `SHA256SUMS` covers the ZIP, manifest, and standalone
installer. The bundled user guide includes the original MIT license. Building the
same source tree/version/toolchain yields the same bytes. Dependencies must
already exist in the development Go cache for an offline build.

[`install-windows.ps1`](../../scripts/install-windows.ps1) is an offline
bootstrap requiring Windows PowerShell/.NET, without Go/Node/Python. It verifies
the explicit bounded manifest, whole ZIP, exact members and PE x64 executable
before executing that verified copy's `install` command. The product installer
independently repeats manifest/package/file/PE validation. Only an empty new
directory is published by sibling-stage rename. Healthy exact repeats verify
and reuse the existing files; another package/version or a damaged install
fails without overwriting it. A per-install sibling lock rejects concurrent
install/uninstall. Staging is removed on return; a process crash may leave a
stage or lock requiring explicit inspection, never automatic stale takeover.

The recommended program root is `%LOCALAPPDATA%/Programs/API-router`, separate
from config/data/models/runtime. The CLI passes protected storage roots to
`Install` so ancestor and descendant overlap is refused. Absolute paths are
required; UNC/device namespaces, dot/parent components and Windows path aliases
are refused. Root/drive-level directories are refused, every existing component
is checked for links/reparse points and Windows files reject hard links.
Archive traversal, aliases, duplicates, directories, links, unexpected members,
excess expansion, missing members, wrong sizes, corrupt hashes and non-amd64 PE
payloads fail before installation publication. These checks defend trusted
local inputs from damage and unsafe layouts; they are not an OS access-control
boundary against another process running as the same user and racing filesystem
changes. Packages are published in the personal fork's
[GitHub Releases](https://github.com/xyzane54/magpie/releases) with an
`api-router-v` tag prefix; no publisher code signature is included. The installer
does not contact GitHub or trust a moving latest-release URL.

[`PreviewUninstall` / `Uninstall`](../../internal/routerdelivery/uninstall.go)
require a receipt bound to the exact install root. Preview is read-only. Only
the four exact managed names and receipt can be removed, each is rechecked,
and unknown files/directories remain. No recursive deletion is used. The CLI
stops the owned instance, removes owned autostart and provides offline host
recovery instructions. Its default retains the executable plus receipt as
an offline recovery tool; repeating uninstall tolerates already missing managed
files. User config, credentials, models, runtime, statistics and host backups
are outside the deletion plan. `uninstall-windows.ps1` is a wrapper with a
read-only default and explicit `-Execute` switch.

The [user guide](../windows-delivery.md) describes manual version replacement,
private consistent backups and compatible-data restoration. No updater,
automatic rollback, data purge or non-Windows installer is implemented. Local
fixed artifacts are distributed as Windows x64 previews. Each release has an
explicit source tag and the ZIP, manifest, standalone installer and SHA256SUMS
assets. Publication is a separate manual operation after package verification;
the package script itself never uploads files. Publisher verification and an
automatic updater are not implemented.

Verification from `magpie/`: `go test ./internal/routerdelivery`, then the
complete product gate. Native acceptance includes the actual PowerShell
bootstrap under a PATH without Go/Node, Unicode/spaces, repeat/corruption,
unsafe ZIP/platform rejection and receipt-managed uninstall. Exact candidate,
hashes, platform evidence and limitations are recorded in
`round4-B/delivery.md` (historical development record; not distributed).
