#requires -Version 5.1
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '..\start-project.ps1')

function Assert-True($Value, [string]$Message) { if (-not $Value) { throw $Message } }
function Assert-Fails([scriptblock]$Action, [string]$Pattern) {
    try { & $Action } catch {
        if ($_.Exception.Message -notmatch $Pattern) { throw }
        return
    }
    throw "Expected failure: $Pattern"
}

$routerTestRoot = Join-Path ([IO.Path]::GetTempPath()) ('router-start-test-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $routerTestRoot | Out-Null
try {
    $routerTestExecutable = Join-Path $routerTestRoot 'api-router.exe'
    [IO.File]::WriteAllText($routerTestExecutable, 'not executed: native CLI is replaced by a test double')
    $routerTestState = Join-Path $routerTestRoot 'state'
    New-Item -ItemType Directory -Path $routerTestState | Out-Null
    $routerTestAppData = Join-Path $routerTestRoot 'roaming'
    $routerTestLocalAppData = Join-Path $routerTestRoot 'local'
    New-Item -ItemType Directory -Path (Join-Path $routerTestAppData 'api-router') | Out-Null
    [IO.File]::WriteAllText((Join-Path $routerTestAppData 'api-router\config.json'), '{"listen":{"address":"127.0.0.1:13145"}}')
    $routerTestHost = Join-Path $routerTestRoot 'workbuddy'
    New-Item -ItemType Directory -Path $routerTestHost | Out-Null
    $routerTestModels = Join-Path $routerTestHost 'models.json'
    $routerTestApps = @{apps = @(@{id = 'workbuddy'; configured = $true; cloud = @{model = 'fixture-model'}})}
    [IO.File]::WriteAllText($routerTestModels, '[{"id":"fixture-model","url":"http://127.0.0.1:13145/app/workbuddy/v1"}]')
    Assert-WorkBuddyRouterAddress $routerTestHost $routerTestApps 'http://127.0.0.1:13145/'
    [IO.File]::WriteAllText($routerTestModels, '{"models":[{"id":"fixture-model","url":"http://127.0.0.1:13145/app/workbuddy/v1"}]}')
    Assert-WorkBuddyRouterAddress $routerTestHost $routerTestApps 'http://127.0.0.1:13145/'
    Write-Host 'PASS: both WorkBuddy configuration shapes match the live endpoint'

    [IO.File]::WriteAllText($routerTestModels, '[{"id":"fixture-model","url":"http://127.0.0.1:58116/app/workbuddy/v1"}]')
    $routerTestBefore = [IO.File]::ReadAllText($routerTestModels)
    Assert-Fails { Assert-WorkBuddyRouterAddress $routerTestHost $routerTestApps 'http://127.0.0.1:13145/' } 'endpoint differs'
    Assert-True ([IO.File]::ReadAllText($routerTestModels) -eq $routerTestBefore) 'Endpoint check rewrote host configuration'
    Write-Host 'PASS: a stale WorkBuddy port fails visibly and preserves configuration'

    $routerTestTokenFile = Join-Path $routerTestRoot 'token'
    [IO.File]::WriteAllText($routerTestTokenFile, 'fixture-token')
    $routerTestCommands = New-Object 'Collections.Generic.List[string]'
    function Invoke-ApiRouterCommand {
        param([string]$Executable, [string[]]$Arguments)
        Assert-True ($env:APPDATA -eq $routerTestAppData -and $env:LOCALAPPDATA -eq $routerTestLocalAppData) 'Startup inherited a test instance environment'
        $routerTestCommands.Add(($Arguments -join ' '))
        if ($Arguments[0] -eq 'dirs') {
            return @{config = (Join-Path $routerTestAppData 'api-router'); data = (Join-Path $routerTestLocalAppData 'api-router'); state = $routerTestState}
        }
        return @{running = $true; executable = $routerTestExecutable; pid = 123; version = 'fixture'; managementURL = 'http://127.0.0.1:13145/'; tokenFile = $routerTestTokenFile}
    }
    function Invoke-RestMethod {
        param($Uri, $TimeoutSec, $Proxy, $Headers)
        if ($Uri.EndsWith('/healthz')) { return @{service = 'api-router'; status = 'ok'; pid = 123} }
        Assert-True ($Uri -eq 'http://127.0.0.1:13145/api/apps') 'Token sent to an unexpected endpoint'
        Assert-True ($Headers.Authorization -eq 'Bearer fixture-token') 'Missing management authorization'
        return @{apps = @()}
    }
    $routerTestSavedAppData = $env:APPDATA
    $routerTestSavedLocalAppData = $env:LOCALAPPDATA
    try {
        $env:APPDATA = 'unrelated-test-instance-roaming'
        $env:LOCALAPPDATA = 'unrelated-test-instance-local'
        Start-ApiRouterProject -Executable $routerTestExecutable -AppData $routerTestAppData -LocalAppData $routerTestLocalAppData -WorkBuddyRoot $routerTestHost -CheckOnly -NoBrowser
        Assert-True (($routerTestCommands -join ',') -eq 'dirs,service status') 'Read-only startup check started a service'
        Assert-True ($env:APPDATA -eq 'unrelated-test-instance-roaming' -and $env:LOCALAPPDATA -eq 'unrelated-test-instance-local') 'Caller environment was not restored'
        $routerTestCommands.Clear()
        Start-ApiRouterProject -Executable $routerTestExecutable -AppData $routerTestAppData -LocalAppData $routerTestLocalAppData -WorkBuddyRoot $routerTestHost -NoBrowser
        Assert-True (($routerTestCommands -join ',') -eq 'dirs,service start') 'Startup did not use the native service start command'
    } finally {
        $env:APPDATA = $routerTestSavedAppData
        $env:LOCALAPPDATA = $routerTestSavedLocalAppData
    }
    Write-Host 'PASS: startup pins storage roots, uses the native lifecycle and restores the caller environment'

} finally {
    # The only recursive cleanup target is the literal, verified test directory.
    if ($routerTestRoot -and [IO.Path]::GetFullPath($routerTestRoot).StartsWith([IO.Path]::GetTempPath(), [StringComparison]::OrdinalIgnoreCase) -and
        (Split-Path -Leaf $routerTestRoot).StartsWith('router-start-test-')) {
        Remove-Item -LiteralPath $routerTestRoot -Recurse -Force
    }
}
