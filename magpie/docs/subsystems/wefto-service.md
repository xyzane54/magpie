# Wefto service lifecycle and management console

[`cmd/wefto`](../../cmd/wefto/main.go) owns the standalone `wefto` / `wefto.exe` command. It starts independently of Magpie GUI, migrations and background jobs. Product names and narrow legacy aliases belong to [`product`](../../internal/product/identity.go).

## Startup and control

[`RunWithModelOptions`](../../internal/routerservice/service.go) initializes the selected layout, acquires the service lock, binds loopback and starts the embedded console. Fresh configuration tries `127.0.0.1:13145`; an occupied address is replaced by another available loopback port. The actual address is persisted and connected WorkBuddy/Loomy endpoints synchronize through the [host transaction owner](wefto-host-config.md). Failure rolls back that transaction and fails startup. Unrelated listeners remain running.

[`routercontrol`](../../internal/routercontrol/control.go) implements background start, status, stop, restart and explicit Windows per-user autostart. `state/router.lock/instance.json` records exact executable, PID, process stamp, instance identity and address. Control validates those against the authenticated instance; another executable owning the layout is not adopted. Only recognized exited/reused-process locks may be archived.

Executable comparisons resolve both existing absolute paths before comparing them. This recognizes Windows 8.3 aliases and macOS symlinked temporary roots returned by OS process queries. A missing or unreadable path does not establish identity; a distinct hard-link path remains another installation. PID creation stamps and authenticated instance checks still apply.

Native Windows supports external process identity and per-user autostart. Linux provides external PID identity for regression runners. Other platforms currently identify only the current foreground process: an external process remains unknown, and its lock must not be archived or its process controlled. The cleanup regression exercises that refusal on those platforms rather than spawning a child that cannot be identified.

The ordinary Windows development entry is [`scripts/start-project.ps1`](../../../scripts/start-project.ps1), also called by `start.cmd`. It uses `magpie/dist/wefto.exe` and ordinary Windows roots, and reports the actual management URL. `-CheckOnly -NoBrowser` is read-only. Its check accepts a whole fresh or legacy layout, never a mixture. Upgrades stop the original executable before switching builds; see the [upgrade guide](../wefto-upgrade.md).

## Management and model requests

[`http.go`](../../internal/routerservice/http.go) enforces loopback remote access, exact Host, same-origin requests and management Bearer authentication. The page gets its private per-run token through `POST /api/management/session`, requiring exact Origin, same-origin Fetch Metadata and a single valid `X-Wefto-UI` marker. The known `X-API-Router-UI` header is accepted at the same boundary; duplicate or conflicting markers are rejected. No CORS access is granted.

Service operations use `X-Wefto-Instance`; legacy `X-API-Router-Instance` remains accepted and either supplied value must match the process stamp. New health responses identify `wefto`. Product clients recognize the known legacy `api-router` name while retaining endpoint and instance/authentication checks.

Principal management routes are `/api/service`, `/api/service/{stop,restart,autostart}`, `/api/cloud-providers`, `/api/apps`, `/api/routing-groups`, `/api/hosts/auto`, `/api/hosts/loomy`, `/api/local-models`, `/api/redaction` and request ledger APIs. Exact allowlists/methods live in `http.go` and each handler. Model traffic uses `/app/<host>/v1/...` and its application's provider credentials; management tokens are never model credentials. See [cloud execution](wefto-cloud.md) and [applications](wefto-apps.md).

## Embedded UI

[`assets/index.html`](../../internal/routerservice/assets/index.html) and product scripts expose application, provider, routing, local-model and usage pages. Titles and header show Wefto / Wefto 织流 with a W favicon. Copy and tutorial translations cover Simplified/Traditional Chinese, English, Japanese and German. Retained Magpie assets provide shared styles/icons without starting its lifecycle.

UI preferences and tutorial state use `wefto.ui` and `wefto.tutorial.v1`. If the new key is absent at the same origin, the page reads its `api-router` predecessor; subsequent saves use the new key. Explicit new state wins. Another browser origin cannot read old localStorage; server credentials and models are unaffected.

Manual tutorial entry records the actual clicked entry button as its focus-return target, including on browsers that do not focus buttons on mouse clicks. Closing or dismissing the guide restores that visible entry after removing modal isolation. Programmatic and first-run entry retain the previously focused element or the ordinary guide entry fallback.

While the guide is open, Tab and Shift+Tab explicitly cycle its visible controls in both directions. This keeps focus in the dialog even when native browser keyboard preferences skip buttons. Keyboard focus may scroll the reading pane normally; entry and exit preserve the page scroll position.

Verification includes management-origin/authentication tests, legacy-header tests, service/control tests, native Windows lifecycle checks and the complete Chromium/WebKit gate in [product regression](../product-regression.md).
