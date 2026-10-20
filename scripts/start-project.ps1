#requires -Version 5.1
param([switch]$NoBrowser, [switch]$CheckOnly)

$ErrorActionPreference = 'Stop'

function Invoke-ApiRouterCommand {
    param([string]$Executable, [string[]]$Arguments)
    $routerOutput = & $Executable @Arguments 2>&1
    if ($LASTEXITCODE -ne 0) { throw ($routerOutput -join [Environment]::NewLine) }
    return ($routerOutput -join [Environment]::NewLine) | ConvertFrom-Json
}

function Assert-RouterPlainPath {
    param([string]$Path)
    $routerItemPath = [IO.Path]::GetFullPath($Path)
    while ($routerItemPath) {
        if (Test-Path -LiteralPath $routerItemPath) {
            if ((Get-Item -LiteralPath $routerItemPath -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) {
                throw "Router path crosses a link or reparse point: $routerItemPath"
            }
        }
        $routerParent = Split-Path -Parent $routerItemPath
        if ($routerParent -eq $routerItemPath) { break }
        $routerItemPath = $routerParent
    }
}

function Assert-WorkBuddyRouterAddress {
    param([string]$Root, [object]$Apps, [string]$Origin)
    $routerApp = @($Apps.apps | Where-Object { $_.id -eq 'workbuddy' -and $_.configured })
    if (-not $routerApp.Count) { return }
    $routerModelsPath = Join-Path $Root 'models.json'
    if (-not (Test-Path -LiteralPath $routerModelsPath -PathType Leaf)) {
        throw 'WorkBuddy Profile is saved but its host configuration is missing. Connect it from the Applications page.'
    }
    $routerModels = Get-Content -LiteralPath $routerModelsPath -Raw | ConvertFrom-Json
    if ($routerModels -isnot [Array]) { $routerModels = $routerModels.models }
    $routerTarget = @()
    if ($routerApp[0].cloud.model) {
        $routerTarget = @($routerModels | Where-Object id -eq $routerApp[0].cloud.model)
    }
    if (-not $routerTarget.Count -and $routerApp[0].models -and @($routerApp[0].models).Count) {
        $routerTarget = @($routerModels | Where-Object id -eq ([string]@($routerApp[0].models)[0]))
    }
    $routerExpected = $Origin.TrimEnd('/') + '/app/workbuddy/v1'
    if ($routerTarget.Count -ne 1 -or ([string]$routerTarget[0].url).TrimEnd('/') -ne $routerExpected) {
        throw "WorkBuddy endpoint differs from this service. Expected: $routerExpected . Reconnect the WorkBuddy Profile from the Applications page, then fully restart WorkBuddy."
    }
}

function Start-ApiRouterProject {
    param([string]$Executable, [string]$AppData, [string]$LocalAppData, [string]$WorkBuddyRoot,
          [switch]$NoBrowser, [switch]$CheckOnly)
    if (-not (Test-Path -LiteralPath $Executable -PathType Leaf)) {
        throw 'Build the project first: go -C magpie build -trimpath -o dist/api-router.exe ./cmd/api-router'
    }
    $routerSavedAppData = $env:APPDATA
    $routerSavedLocalAppData = $env:LOCALAPPDATA
    try {
        # Use the ordinary user's layout even when launched from a terminal
        # that previously selected an isolated acceptance/test instance.
        $env:APPDATA = $AppData
        $env:LOCALAPPDATA = $LocalAppData
        $routerLayout = Invoke-ApiRouterCommand -Executable $Executable -Arguments @('dirs')
        if ($routerLayout.config -ne (Join-Path $AppData 'api-router') -or
            $routerLayout.data -ne (Join-Path $LocalAppData 'api-router')) { throw 'Unexpected Router storage layout.' }
        $routerAction = if ($CheckOnly) { 'status' } else { 'start' }
        $routerStatus = Invoke-ApiRouterCommand -Executable $Executable -Arguments @('service', $routerAction)
        if (-not $routerStatus.running) { throw 'Router is stopped. Run start.cmd to start it.' }
        if ($routerStatus.executable -ne $Executable) { throw 'Another executable owns this layout. Use its own service stop command before switching builds.' }
        $routerSavedConfig = Get-Content -LiteralPath (Join-Path $routerLayout.config 'config.json') -Raw | ConvertFrom-Json
        if ($routerStatus.managementURL -ne ('http://' + $routerSavedConfig.listen.address + '/')) {
            throw 'The saved listen address differs from the running service. Restart this same instance before reconnecting a host.'
        }
        $routerHealth = Invoke-RestMethod -Uri ($routerStatus.managementURL.TrimEnd('/') + '/healthz') -TimeoutSec 5 -Proxy $null
        if ($routerHealth.service -ne 'api-router' -or $routerHealth.status -ne 'ok' -or
            $routerHealth.pid -ne $routerStatus.pid) { throw 'The endpoint belongs to another service instance.' }
        # Read the token only after the native CLI and health endpoint establish
        # the exact live instance. Never send it to a configured upstream URL.
        $routerToken = ([IO.File]::ReadAllText($routerStatus.tokenFile)).Trim()
        $routerApps = Invoke-RestMethod -Uri ($routerStatus.managementURL.TrimEnd('/') + '/api/apps') -Headers @{Authorization = 'Bearer ' + $routerToken} -TimeoutSec 5 -Proxy $null
        Assert-WorkBuddyRouterAddress $WorkBuddyRoot $routerApps $routerStatus.managementURL
        if ($routerStatus.previousAddress -and $routerStatus.previousAddress -ne $routerStatus.address) {
            Write-Host "The previous port was unavailable. Connected application addresses have been synchronized. Restart open applications to load the updated address."
        }
        Write-Host "API-router $($routerStatus.version): $($routerStatus.managementURL) (PID $($routerStatus.pid))"
        Write-Host "Config: $($routerLayout.config)"
        if (-not $NoBrowser -and -not $CheckOnly) { Start-Process $routerStatus.managementURL | Out-Null }
    } finally {
        $env:APPDATA = $routerSavedAppData
        $env:LOCALAPPDATA = $routerSavedLocalAppData
    }
}

if ($MyInvocation.InvocationName -ne '.') {
    $routerProjectRoot = Split-Path -Parent $PSScriptRoot
    $routerMutex = New-Object Threading.Mutex($false, 'Local\API-router-project-start')
    $routerAcquired = $false
    try {
        try { $routerAcquired = $routerMutex.WaitOne(15000) }
        catch [Threading.AbandonedMutexException] { $routerAcquired = $true }
        if (-not $routerAcquired) { throw 'Another project startup is in progress.' }
        Start-ApiRouterProject -Executable (Join-Path $routerProjectRoot 'magpie\dist\api-router.exe') `
            -AppData ([Environment]::GetFolderPath('ApplicationData')) `
            -LocalAppData ([Environment]::GetFolderPath('LocalApplicationData')) `
            -WorkBuddyRoot (Join-Path ([Environment]::GetFolderPath('UserProfile')) '.workbuddy') `
            -NoBrowser:$NoBrowser -CheckOnly:$CheckOnly
    } catch {
        [Console]::Error.WriteLine($_.Exception.Message)
        exit 1
    } finally {
        if ($routerAcquired) { $routerMutex.ReleaseMutex() }
        $routerMutex.Dispose()
    }
}
