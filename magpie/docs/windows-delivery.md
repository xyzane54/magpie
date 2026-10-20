# API-router Windows x64 本地安装与管理

本包包含已编译的产品 EXE 和内嵌网页。使用产品无需 Go、Node、Python 或源码，不需要管理员服务。当前仅验证 Windows x64。正式 Release 托管、公开下载 URL、发布者签名和公开分发信任链尚未接入；不要将本地产物宣称为公开发布。SHA256 校验用于检测与明确可信 manifest 不一致的文件，不能证明 manifest 的发布者身份。

## 安装固定版本

从可信来源取得同一固定版本的 `*.zip`、`*.manifest.json`、`SHA256SUMS` 和独立 `install-windows.ps1`。不要执行网络脚本文本，不查询或隐式使用 latest。安装器只接受明确给定的本地包；当前不实现自动升级、自动回滚或其他平台安装。

在 PowerShell 中指定绝对路径（支持空格和中文）：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File 'C:\Downloads\API-router\install-windows.ps1' -ManifestPath 'C:\Downloads\API-router\api-router-VERSION-windows-amd64.manifest.json' -PackagePath 'C:\Downloads\API-router\api-router-VERSION-windows-amd64.zip'
```

把 `VERSION` 替换为实际固定版本。默认程序目录为 `%LOCALAPPDATA%\Programs\API-router`，可用 `-InstallDir 'C:\Users\YOU\Programs\API-router'` 指定独立产品目录。程序目录与配置、数据、模型目录必须分开，安装不会覆盖或初始化这些数据。安装不会启动服务、打开自启、下载、启用或加载任何模型。首次启动也不会下载、启用或加载模型。

包、manifest 和安装目录必须是本机盘符绝对路径；不支持 UNC、设备命名空间、`.` / `..` 组件或尾随点/空格路径别名。安装器验证完整包及所有文件的大小和 SHA256、ZIP 平铺文件白名单、Windows PE x64 架构，拒绝路径穿越、链接与重解析点。验证通过后才发布完整安装目录。同一完整包重复安装返回 reused；同版本文件损坏会明确失败并保留原目录。其他版本不会覆盖现有安装，需按手动替换流程操作。

## 服务与登录自启

```powershell
$router = Join-Path $env:LOCALAPPDATA 'Programs\API-router\api-router.exe'
& $router service start
& $router service status
& $router service restart
& $router service stop
& $router service autostart status
& $router service autostart enable
& $router service autostart disable
```

`start` / `status` 返回实际 `managementURL` 和 `tokenFile` 路径。打开该本地管理地址即可使用，无需点击连接或手动输入令牌；刷新页面和服务重启后会自动恢复管理连接。token 仍在内部保护管理 API，仅保存在页面内存中；命令行客户端需要时仍可读取私有 token 文件，不要分享或贴进日志。停止后网页会断开，用独立 EXE 的 `service start` 重新启动。浏览器关闭不会停止服务。端口被占用时会自动绑定备用回环端口，同步已接入 Router 的 WorkBuddy/Loomy 地址并保存新端口；占用进程继续运行。已运行的宿主可能缓存旧地址，重新打开后加载同步配置。登录自启为每用户选择，不要求管理员权限，启用与关闭入口始终存在。

页面提供的 PowerShell 重新启动命令会保留当前实例的配置与数据目录，执行后恢复终端原有环境；
专用 APPDATA / LOCALAPPDATA 的测试实例不能仅用 EXE 路径在普通终端中重启，否则会读到另一套配置和端口。
源码开发的日常入口为仓库根 `start.cmd`，会额外核对 WorkBuddy 接入地址；
不要用历史验收中的临时端口或已移除的测试目录作为日常启动入口。

模型、脱敏试用与登录自启是独立选择。首次默认不开启试用；如明确选择 1.7B 实验性脱敏，使用：

```powershell
& $router service start --experimental-redaction
# 以后重启和登录自启保留已明确选择的试用参数。
& $router service restart
# 明确关闭试用（先停止当前服务）：
& $router service stop
& $router service start --no-experimental-redaction
```

试用的识别质量未正式验收，不得当作可靠隐私保护；严格校验失败仍阻断出站。试用参数本身不会下载、启用或加载模型。修改选择后核对 `service status`。

设置页提供实际服务状态和自启开关；服务停止前会显示独立重新启动命令。服务管理必须在本机 loopback 并提供管理 token；页面不会允许未经授权的远程控制。

## 简易卸载与宿主恢复

先预览，不会写文件：

```powershell
& $router uninstall preview --install-dir (Split-Path $router)
& $router host restore-preview --help
```

如果仍有通过 Router 接入的宿主，先按预览给出的独立 `host restore-preview` / `host restore` 命令明确恢复。宿主文件被用户修改时，恢复会阻断，必须处理冲突；不要直接覆盖。停止服务不会自动修改宿主配置。卸载遇到尚未解决的恢复记录会给出恢复入口。

```powershell
& $router uninstall execute --install-dir (Split-Path $router)
# 或运行已安装的脚本（默认只预览，-Execute 才执行）：
& (Join-Path (Split-Path $router) 'uninstall-windows.ps1') -Execute
```

卸载停止本产品实例并移除本产品登录自启，只删除 receipt 明确管理的程序文件，不递归删除宽泛目录。默认保留配置、凭证、统计、模型、运行时、宿主恢复备份和日志；保留 `api-router.exe` 及安装 receipt 作为可离线使用的恢复工具。报告会明确列出残留恢复工具路径，这不等于清空全部用户数据。确认所有宿主已恢复且不再需要工具后，可在 EXE 已停止、离开安装目录的 PowerShell 中手动删除列出的 EXE 与 receipt；其他未知文件由用户自行处理。本轮不提供清空用户数据开关。

## 手动版本替换、备份与恢复

1. 使用 `dirs` 查看本人的真实目录；停止服务，关闭登录自启。确认没有活跃或排队请求。保存旧 ZIP、manifest、SHA256SUMS 和安装 EXE，不要依赖临时下载目录。
2. 在服务停止后，将 `dirs` 输出中的 Config 和 Data 目录完整复制到另一个私有备份位置；至少包括配置、凭证、SQLite 及其可能存在的 sidecar、state 和宿主恢复备份。按需保留已有 models/runtime，避免重复复制大文件。备份含凭证，不要上传或共享。
3. 把旧程序目录重命名为明确的备份目录。用新版本的独立安装脚本、对应 manifest 和 ZIP 安装到原程序路径；安装器不修改旧备份或用户数据。启动，核对版本、配置、管理页与模型意图，再按原选择恢复登录自启。
4. 新程序无法工作时，先停止并关闭自启。保留失败现场，把新程序目录移走，再将旧程序目录放回原路径。安装 receipt 绑定原始绝对目录：重命名的旧备份 EXE 仍可作为离线 `host restore-preview` / `host restore` 工具使用，但重新安装、安装复核或卸载原 receipt 时须先把整个目录放回原路径。只有旧版本明确支持当前数据 schema 时才能使用现有数据；不兼容时必须恢复第 2 步的兼容私有备份并接受备份之后的数据不会保留。不要在两版本同时运行时复制或恢复数据库，不承诺直接替换 EXE 即可降级。

所有替换与恢复由用户明确执行，没有后台自动更新或自动回滚引擎。数据 schema 不兼容会明确失败，不会自动降级或静默覆盖。
