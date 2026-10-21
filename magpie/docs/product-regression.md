# Wefto 产品统一回归（T9.1.1）

从仓库根运行固定入口（Windows PowerShell、macOS、Linux 均不要求 make）：

```sh
node magpie/scripts/product-regression.cjs
```

等价入口：在 `magpie/` 执行 `npm test` 或 `make test-product`。命令串行运行产品构建、27 个产品/共享包 race、相同 27 个产品/共享包 vet、Chromium 十套页面、WebKit 十套页面；原 24 包/七页面全部保留，新增 Windows 交付、服务控制两包以及 `service-control`、`spark-models`、`tutorial` 三套页面套件。任何失败立即阻断后续步骤并返回 1；缺工具、缺浏览器、缺系统动态库同样失败。仅五步全过才输出 `status: passed, complete: true`。不连接真实云端、模型运行时或宿主。

候选记录包含实际 HEAD 和完整 tracked Git status。WSL读取 Windows 创建的 worktree 时，
以现有 Windows `git.exe` 检查同一完整工作树，避免 Linux Git 跨 DrvFS 重读共享索引超时；
`candidate.gitPlatform` 明确记录这个取证工具平台。测试平台与environment仍分别记录Linux/WSL，
不能据Git工具字段宣称原生Windows测试通过。Git失败仍记unknown，不能转换成clean。

## 干净环境准备

依赖准备和离线回归分开。以下下载只用于准备工具/锁定依赖，不属于测试流量；CI 仅在准备阶段安装依赖和 Linux 浏览器系统库，完整产品回归阶段保持离线。

| 依赖 | 固定值/来源 | 约束 |
| --- | --- | --- |
| Go | `go.mod`: 1.26.8 | 入口精确核对版本，`GOTOOLCHAIN=local`，不自动下载 |
| Node | `.node-version`: 22.23.2 | 本轮 Linux/WSL 基线；也支持 Node 24，实际版本写 summary |
| npm | 随 Node 安装 | 使用 lockfile v3；本轮离线检查使用 npm 11.16.0 CLI |
| Playwright / playwright-core | `package-lock.json`: 1.55.1 | 使用本 checkout 的 `node_modules`，不从 `NODE_PATH` 借用其他版本 |
| Chromium / headless shell | Playwright revision 1193，140.0.7339.186 | 启动记录实际版本及 Playwright 默认可执行路径；无头启动使用同 revision 的 headless shell |
| WebKit | Playwright revision 2203，26.0 | 个别旧 OS 有 revision override，以锁定包 `playwright-core/browsers.json` 为准 |
| FFmpeg | Playwright revision 1011 | 平台 override 同上 |

WSL 上若 Playwright 需要固定宿主平台 revision，使用 `PLAYWRIGHT_HOST_PLATFORM_OVERRIDE`（例如 `ubuntu24.04-x64`）；统一入口会在隔离环境中保留这一显式变量，不继承其他宿主覆盖。临时服务、SQLite 与浏览器缓存必须位于 WSL 本地文件系统，不能放在 `/mnt/c`。
| race C 编译器 | Linux/macOS clang 或 gcc；Windows 可用的 MinGW-w64 gcc | `CGO_ENABLED=1`；缺失会在 race 阶段失败，不降为普通测试 |

在 `magpie/` 下准备：

```sh
# 先按 go.mod / .node-version 安装对应原生平台工具链，并使 go/node/npm 在 PATH。
# 不从真实用户配置读取 npm 凭证；公共依赖不需要登录。
npm ci --ignore-scripts --no-audit --no-fund --cache ./dist/cache/npm --userconfig ./dist/npm-user.npmrc --globalconfig ./dist/npm-global.npmrc
npm run browsers:install
# 仅在你管理的 Linux runner 需要安装系统库时使用（会调用系统包管理器）：
# npm run browsers:install -- --with-deps

# 在替换 HOME 前准备并固定 Go 依赖缓存；只在依赖准备阶段允许下载。
go mod download
npm run test:entry
npm test
```

Windows 在 PowerShell 中可用 `npm.cmd`，不要求改变脚本执行策略。Windows 构建产物自动使用 `.exe`，通过绝对 `WEFTO_BINARY` 传给页面测试。macOS 需要相应系统版本的 Playwright 原生浏览器；WebKit Playwright 不是 Safari 或 WebKitGTK 的实机验收。

没有网络的干净 runner 需要事先放好与 lockfile 匹配的 npm tarball 缓存、Go module 缓存、Go 工具链和本平台浏览器。准备时可使用 `npm ci --offline ...`；缓存不完整会失败。不要通过删除 WebKit 或 race 来让完整入口变绿。Linux 浏览器系统库需在 runner 镜像准备，不在回归时修改真实宿主。

## 缓存与隔离

入口在改 HOME 前以 `GOENV=off` 查询 `go env -json GOVERSION GOPATH GOMODCACHE GOCACHE`，保留这些绝对缓存路径。可事先显式设置 `GOPATH`、`GOMODCACHE`、`GOCACHE`。不读取用户 Go 配置，不继承 GOFLAGS、GOOS、GOARCH、GOROOT、代理、NODE_OPTIONS、NODE_PATH、真实凭证或宿主目录覆盖变量。测试命令强制 `GOPROXY=off`、`GOSUMDB=off`、`GOWORK=off`、`-mod=readonly -buildvcs=false`；依赖不全会报错，不在线补齐。基线/提交 SHA 由交付记录标识，不借 Git safe.directory 改写用户配置。

| 缓存/数据 | 默认路径 | 生命周期 |
| --- | --- | --- |
| Go modules/build | 改 HOME 前的 go env；或显式环境变量 | 保留，路径写入 summary；不要放进临时 HOME |
| npm tarballs | 准备命令的 `magpie/dist/cache/npm` | 保留；回归不调用 npm |
| 浏览器 | `magpie/dist/cache/playwright` | 安装脚本与回归共享同一默认值；可用绝对 `PLAYWRIGHT_BROWSERS_PATH` 同时覆盖两步 |
| 回归 HOME/USERPROFILE、APPDATA/LOCALAPPDATA、XDG 四目录、TMP/TEMP/TMPDIR | `os.tmpdir()/wefto-regression-*/`；绝对路径记录在 summary 的 temporary | 普通成功/失败后仅删除本次 temporary |
| 产品配置、私有假凭证、账本、Go 临时构建和页面服务数据 | 上述临时目录及测试自身 mkdtemp/TestMain | 不读取真实宿主配置；测试清理自行启动的服务 |
| 二进制、日志、截图、summary | `dist/regression/run-*/` | 保留，受 `.gitignore` 排除 |

原 `make clean` 会删除整个 `dist/`，包括默认 npm/浏览器缓存和回归证据；清理前先归档证据。CI 持久化缓存时按 OS/架构区分，不共用不同平台的浏览器目录。

临时数据使用运行系统的本地临时文件系统；源码/日志可以在 Windows worktree，但 WSL 不应把 SQLite 账本与临时服务数据放进 `/mnt/c`。本轮曾在该挂载上遇到页面加载超时和包测试迟迟未结束，已停止本次测试进程并保留原日志，改为原生临时目录后重新验证。若继承的 TMP/TEMP/TMPDIR 已指向挂载盘，启动入口前把它们设为本系统可写的本地临时目录（WSL 通常 `/tmp`）。

所有产品/共享包名称集中在 `scripts/product-regression.cjs` 的 `packages`；原 20 包与 W5/runtime/acceptance 合入后的 24 包全部保留，新增 Windows 交付、服务控制与产品身份包后共为 27 包。`make test-router`、`make vet-router` 复用此入口，避免分叉清单。Go 测试用 `-count=1 -p 1 -timeout 45m -race`，实际执行而非复用测试结果缓存。现有 Go `testenv` 继续隔离宿主 CLI/keychain，产品网络测试使用 httptest/fake upstream。

十套必测页面为 `page`、`provider-presets`、`profile-probe`、`observations`、`local-model`、`loomy-host`、`redaction`、`service-control`、`spark-models`、`tutorial`；入口确认十个文件均存在，并自动纳入新增 `*.test.cjs`，按文件串行。每引擎先真实启动一次浏览器确认依赖，再运行所有页面。测试保留五语言、1600/1100/375px（observations/local-model/redaction 为 1600/375px）、刷新/重载、错误状态与真实产品管理 API；预置供应商地区错误等既有拦截保持不变。

页面服务和 fake upstream 均只监听 `127.0.0.1`，upstream 使用端口 0；页面夹具先申请临时端口再释放给服务，不使用默认 13145。产品在地址被占用时会自动绑定备用回环端口，并以启动输出和保存的实际地址为准，不查杀/接管现有进程。专门的端口迁移测试覆盖占用、双宿主同步、回滚与原生 Windows 重启；页面夹具的预留端口不是长期管理地址。普通断言失败由既有 t.after 关闭本次 browser、service 与 upstream。外部强杀、机器退出或 Ctrl+C 可打断清理与最终摘要，不能把无最终 summary 的运行算通过；不得为清理测试而停止其他会话或用户服务。

## 命令与证据

每次运行打印绝对输出目录；用 `--output=绝对或相对路径` 可指定一个**尚不存在**的目录，拒绝覆盖旧证据。

| 阶段 | 实际命令（包/文件参数以日志为准） | 输出 |
| --- | --- | --- |
| build | `CGO_ENABLED=0 go build -trimpath -ldflags=… -o <本次二进制> ./cmd/wefto` | `build.log`、本次 wefto[.exe] |
| race | `CGO_ENABLED=1 go test -race -count=1 -p 1 -timeout 45m <27个产品/共享包>` | `race.log` |
| vet | `CGO_ENABLED=0 go vet <27个产品/共享包>` | `vet.log` |
| chromium | 浏览器启动探测；`BROWSER=chromium node --test --test-concurrency=1 --test-reporter=tap <十套页面>` | `chromium.log`、`chromium/` 截图 |
| webkit | 同上，`BROWSER=webkit` | `webkit.log`、`webkit/` 截图 |

`summary.json` 从 `running` 开始写入，记录实际 Node/Go、平台/架构/WSL、内核、缓存、27 个产品/共享包、页面文件、每步状态/耗时/日志以及未执行原因。`not_run` 不表示通过；默认执行遇到失败会为后续项写入 `blocked` 原因。中断留下的 `running` 也不表示通过。

定位单步可用 `--only=build`、`--only=race`、`--only=vet`、`--only=chromium`、`--only=webkit`，或逗号组合。浏览器诊断会先重建自己的产品二进制。`make test-router-ui` 等价两引擎诊断。诊断成功状态为 `partial, complete: false`，不能当完整产品通过；失败仍返回 1。全门禁不要传 `--only`。入口本身的离线回归：`npm run test:entry`。

## GitHub Actions

仓库根的 [Wefto CI](../../.github/workflows/wefto-ci.yml) 在推送 `main`、推送 `codex/**` 开发分支或向 `main` 提交 PR 时运行。工作流合入默认分支后，也可在 GitHub 的 Actions → Wefto CI → Run workflow 手动运行并选择分支。PR 检查使用 GitHub 生成的合并结果，验证与当前主干组合后的代码。

三个独立原生 job 分别使用 Ubuntu 24.04 x64、Windows 2025 x64、macOS 15 arm64。每个 job 从 `go.mod`、`.node-version`、`package-lock.json` 准备工具链、Go modules 和 Playwright 浏览器；Windows 显式检查 MinGW-w64 gcc，Linux 在准备阶段安装浏览器系统库。当前不持久化依赖缓存，避免混用不同平台或镜像版本的缓存。

macOS 在运行入口检查和产品回归前，将系统临时目录解析为真实路径并固定 `TMPDIR`、`TMP`、`TEMP`。系统常见的 `/var` → `/private/var` 别名因此不会混入测试布局；产品原有的符号链接/重解析点路径保护仍生效。

准备完成后运行 `npm run test:entry`，再调用同一条完整入口 `npm test -- --output=dist/regression/ci`；构建、27 包 race、27 包 vet、Chromium 和 WebKit 任一步失败都会阻断该 job。最终还检查 summary 的 `passed`、`complete: true`、干净 tracked 工作树和本次 GitHub 候选 SHA；不使用诊断模式或允许失败的矩阵项。

每个平台始终尝试上传 `magpie/dist/regression/` 为 `wefto-regression-<平台>-<运行次数>` artifact，保留 14 天，包含本次二进制、summary、日志和截图。若在依赖准备阶段即失败，还没有回归目录，错误可从 Actions 步骤日志查看。工作流只获得仓库内容读取权限，不发布 Release，不需要模型、供应商或宿主凭证。

## 旧入口和 CI 盘点

| 既有入口 | 当前覆盖/漏项 |
| --- | --- |
| `make` / `make wefto` | 产品构建；不测试 |
| 原 `make test-router` | 原14包race；当前扩至27包并复用隔离入口race诊断，无vet/浏览器 |
| `make test-ui` | 旧 `internal/gui/tests`；**漏掉全部 routerservice 页面**，保留作上游参考 |
| `make test` / `go test ./...` | 包含大量已裁掉的 Magpie 功能；不构建产品、不运行浏览器，非本任务产品门禁 |
| `make build` / 模块根 `go build` | 构建旧 Magpie，不是 Wefto |
| [上游 test.yml](https://github.com/yetone/magpie/blob/707fbb249df755423d3b2b77c6f001dd376d75c4/.github/workflows/test.yml)（上游专属，不随源码快照分发） | 上游根布局、选择性旧功能 race、Windows 主要编译；无产品浏览器 |
| 其他上游 workflows（不随源码快照分发） | docker/release/ui-preview 面向旧 Magpie；preview 独立 Playwright 1.61.1，不作为产品依赖 |

初版盘点时 `git remote -v` 为空，因此先交付可由托管平台调用的统一脚本。当前 GitHub 集成由仓库根的 Wefto CI 提供。上游站点与旧工作流不随当前公开源码快照分发；旧工作流按上游根布局编写，直接复用仍有 go.mod、工作目录及产品覆盖错误。

接入 CI 时应建立三个独立原生 job：Windows x64、macOS arm64、受此 Playwright 支持的 Linux x64 runner（例如 Ubuntu 24.04），每个 job 在准备工具链/依赖后调用**同一条完整命令**，使用 shell 的非零状态阻断；始终归档本次 `magpie/dist/regression/`。缓存键至少包括 OS、架构、Go 1.26.8/go.sum、Node 基线/package-lock.json、Playwright 1.55.1 和系统镜像版本。不要用 ignore-error、允许失败的 WebKit job 或只跑 Chromium 后给双引擎打勾。实际 runner 未配置时记录未执行，不用跨编译或 WSL 替代 Windows/macOS 原生状态。

初版入口证据见 T9.1.1 交付记录（历史开发记录，未随源码分发）；新增运行时20包/六套页面证据及平台边界见 Round2运行时交付（历史开发记录，未随源码分发）。

## 第二轮统一执行与锁

直接执行 `node magpie/scripts/product-regression.cjs`（仓库根）或 `node scripts/product-regression.cjs`（magpie）。CLI 自身原子持有主 checkout 的 `.test-locks/heavy-regression.lock`，对应 `<checkout>/.test-locks/heavy-regression.lock`（Windows 与 WSL 使用同一主 checkout）。所有 worktree 共用；不按年龄/PID抢锁。历史宿主 wrapper代理共享实现，对产品入口不重复加锁。旧 runtime 的临时锁路径仅是历史证据，不再使用。

必测共27包、10套页面：page、provider-presets、profile-probe、observations、local-model、loomy-host、redaction、service-control、spark-models、tutorial，Chromium/WebKit各全跑。原20包/6套页面与24包/7套页面完整保留；缺失任一必测文件失败。summary记录候选SHA、trackedDirty和构建二进制SHA256；诊断 `--only` 不构成完整通过。
