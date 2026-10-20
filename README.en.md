# API-router

[简体中文](README.md) | [English](README.en.md)

API-router is a local service for routing AI model requests. Manage cloud APIs, local models, and routing policies in one web console, then connect WorkBuddy and Loomy to the same service. It is derived from a fixed Magpie source snapshot; the product entry point is `magpie/cmd/api-router`.

**[Download the Windows x64 preview](https://github.com/xyzane54/magpie/releases/tag/api-router-v0.1.0-beta.1)** · [Browse the API-router source](https://github.com/xyzane54/magpie/tree/codex/api-router)

The current version is **0.1.0-beta.1**, with initial support focused on WorkBuddy and Loomy running on the same machine. A prebuilt package is available for Windows x64. macOS and Linux can be built from source; the other host presets and protocol adapters in this repository do not imply that every client, version, or platform has been verified.

## Download and install on Windows x64

The package includes the executable and embedded web console. Go, Node.js, and Python are not required to use it. Download these four assets from the same release into one local folder:

| File | Purpose |
| --- | --- |
| `api-router-0.1.0-beta.1-windows-amd64.zip` | Executable, web console, Windows guide, and installation scripts |
| `api-router-0.1.0-beta.1-windows-amd64.manifest.json` | File sizes and SHA256 hashes checked by the installer |
| `install-windows.ps1` | Standalone installer |
| `SHA256SUMS` | SHA256 hashes for the ZIP, manifest, and standalone installer |

Open PowerShell in that folder and run:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\install-windows.ps1 -ManifestPath "$PWD\api-router-0.1.0-beta.1-windows-amd64.manifest.json" -PackagePath "$PWD\api-router-0.1.0-beta.1-windows-amd64.zip"

$router = Join-Path $env:LOCALAPPDATA 'Programs\API-router\api-router.exe'
& $router service start
```

The installer verifies the package and every file, then installs into `%LOCALAPPDATA%\Programs\API-router` by default. Open the `managementURL` returned by the start command. Installation does not enable login startup or download a model. This preview is not code-signed and does not include an automatic updater.

For an existing installation, stop the service, retain the previous package, and back up the configuration and data directories reported by `api-router.exe dirs` before replacing the program directory. The installer refuses to overwrite another version. See the [Windows guide](magpie/docs/windows-delivery.md) for replacement, backup, and recovery details.

## First run

1. Open the web console. Add a cloud provider, API key, endpoint, and model, then run its connection check.
2. Optionally create a routing group with cloud or local models and choose its scheduling strategy.
3. Select models or routing groups for WorkBuddy or Loomy on the applications page, enable the connection, and reopen the host to load its configuration.
4. To use local inference, complete device detection, model and runtime installation, and capability checks on the local models page before explicitly enabling it.

Cloud providers and local models can be used independently. Experimental redaction requires a separate, explicit opt-in.

## Features

- **Cloud APIs:** Configure provider credentials, endpoints, protocols, and explicit model lists, with independent application profiles.
- **Routing groups:** Use smart cloud/local routing, ordered, weighted, or manual strategies, and fallback between selected models. An application can bind multiple models and groups.
- **Protocol handling:** Bounded support for OpenAI Chat Completions, OpenAI Responses, and Anthropic Messages, including streaming, limited retries, request records, and reported token usage. Tool use and protocol conversion depend on client input and upstream capabilities.
- **Local models:** Device checks, pinned Spark-X2.5-1.7B / 4B model and runtime downloads, one managed inference process, and context and execution budgets. Local execution requires explicit installation, enablement, and capability checks.
- **Experimental redaction:** Local detection, strict result validation, mapping, and response restoration. Detection quality is not formally accepted; invalid results block cloud dispatch.
- **Host connections:** WorkBuddy and Loomy adapters with configuration previews, backups, and offline recovery commands.
- **Local management:** Embedded web console, first-run guide, service start/stop/restart, optional per-user Windows login startup, and uninstall with a retained recovery tool.

The service first tries `127.0.0.1:13145`. If the port is occupied, it selects another loopback port, saves the actual address, and synchronizes already connected WorkBuddy/Loomy endpoints. Always use the management URL returned by startup. A running host may need to be reopened after an endpoint change. Closing the browser does not stop the service.

The web console connects through a same-origin handshake. Command-line management clients use the private token file reported by startup; this token is not a model API credential. Cloud requests use the provider credentials bound to the application.

## Build from source

Use the Go version specified by [magpie/go.mod](magpie/go.mod), currently **1.26.8**. The executable embeds the web console; a normal product build does not require Node.js or a desktop WebView.

The API-router source lives on the `codex/api-router` branch of this repository. From the repository root, build on Windows:

```powershell
go -C magpie build -trimpath -o dist/api-router.exe ./cmd/api-router
```

For ordinary development startup, run `start.cmd`, or:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/start-project.ps1 -NoBrowser
```

This entry uses `magpie/dist/api-router.exe`, reuses an existing service, checks its identity and configured WorkBuddy endpoint, and reports the actual management URL. Add `-CheckOnly` for a check without startup. Use the ordinary Windows user configuration and data directories for normal startup; historical test instances and ports are not the development entry point.

On macOS or Linux:

```sh
go -C magpie build -trimpath -o dist/api-router ./cmd/api-router
./magpie/dist/api-router serve
```

`make -C magpie api-router` also builds the product. The module-root `go build .`, `make build`, and archived Dockerfile build upstream Magpie instead.

Useful commands:

```sh
api-router version
api-router dirs
api-router config check
api-router healthcheck
api-router service --help
api-router host --help
```

Stop foreground `serve` with Ctrl+C. For an explicit experimental redaction trial:

```sh
api-router serve --experimental-redaction
```

The trial also requires model setup in the console. Its output is not a guarantee of reliable privacy protection; see the [redaction reference](magpie/docs/subsystems/api-router-redaction.md).

## Test and package

The product regression gate requires Go, Node.js 22 or 24, and the pinned Playwright browsers. Follow the [regression guide](magpie/docs/product-regression.md) to provision dependencies, then run from the repository root:

```sh
node magpie/scripts/product-regression.cjs
```

The gate builds the product and runs race tests, vet, and Chromium/WebKit page tests. A failed stage returns a nonzero exit status. These isolated tests use mock upstreams and do not replace validation with real models, hosts, or each native platform.

Build a fixed Windows package in PowerShell:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File magpie/scripts/package-windows.ps1 -Version 0.1.0-beta.1 -OutputDir "$PWD\dist\release\0.1.0-beta.1"
```

The installer accepts explicitly supplied local files. It does not fetch a latest release, update automatically, or roll back data schemas. Preview packages are published in [this repository's Releases](https://github.com/xyzane54/magpie/releases).

## Documentation

- [Service and commands](magpie/docs/subsystems/api-router-service.md)
- [Application profiles and routing](magpie/docs/subsystems/api-router-apps.md), [providers](magpie/docs/subsystems/api-router-providers.md), and [cloud protocols](magpie/docs/subsystems/api-router-cloud.md)
- [Local models and runtime](magpie/docs/subsystems/api-router-runtime.md), [redaction](magpie/docs/subsystems/api-router-redaction.md), and [storage](magpie/docs/subsystems/api-router-storage.md)
- [Host configuration and recovery](magpie/docs/subsystems/api-router-host-config.md), [Loomy integration](magpie/docs/subsystems/api-router-loomy.md), and [Windows delivery](magpie/docs/windows-delivery.md)
- [Subsystem index](magpie/docs/subsystems/README.md)

The `magpie/` directory retains upstream source, tests, and reference material, including capabilities outside API-router's lifecycle. `magpie/README.md` and `magpie/README.zh-CN.md` are archived upstream documents. The upstream site, old release workflows, private development records, and local acceptance artifacts are excluded from the public source snapshot. Historical validation notes do not establish verification of the current release.

## Origin and license

API-router is derived from Magpie commit [`707fbb249df755423d3b2b77c6f001dd376d75c4`](https://github.com/yetone/magpie/commit/707fbb249df755423d3b2b77c6f001dd376d75c4). The original project and Go module path are [`yetone/magpie`](https://github.com/yetone/magpie); API-router releases are managed separately in [`xyzane54/magpie`](https://github.com/xyzane54/magpie).

The root [LICENSE](LICENSE) retains the original MIT license and `Copyright (c) 2026 yetone` notice, matching [magpie/LICENSE](magpie/LICENSE). Third-party components retain their own license terms. Model weights and inference runtimes are downloaded separately from their configured sources and are not bundled with the executable or source snapshot.
