#requires -Version 5.1
param([Parameter(Mandatory = $true)][string]$Executable)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '..\start-project.ps1')

function Assert-PortTest($Value, [string]$Message) { if (-not $Value) { throw $Message } }
function Invoke-PortTestAPI($Status, [string]$Path, $Body) {
    $routerTestToken = ([IO.File]::ReadAllText($Status.tokenFile)).Trim()
    Invoke-RestMethod -Uri ($Status.managementURL + $Path) -Method Post -ContentType 'application/json' `
        -Headers @{Authorization = 'Bearer ' + $routerTestToken} -Body ($Body | ConvertTo-Json -Depth 15) -TimeoutSec 10 -Proxy $null
}

$routerTestRoot = Join-Path ([IO.Path]::GetTempPath()) ('router-auto-port-test-' + [Guid]::NewGuid().ToString('N'))
$routerTestSavedEnvironment = @{}
$routerTestListeners = New-Object 'Collections.Generic.List[Net.Sockets.TcpListener]'
$routerTestCleanup = $false
$routerTestPassed = $false
New-Item -ItemType Directory -Path $routerTestRoot | Out-Null
foreach ($routerTestKey in @('APPDATA', 'LOCALAPPDATA', 'USERPROFILE', 'WORKBUDDY_CONFIG_DIR', 'LOOMY_CONFIG_DIR')) {
    $routerTestSavedEnvironment[$routerTestKey] = [Environment]::GetEnvironmentVariable($routerTestKey, 'Process')
}
try {
    $env:APPDATA = Join-Path $routerTestRoot 'roaming'
    $env:LOCALAPPDATA = Join-Path $routerTestRoot 'local'
    $env:USERPROFILE = Join-Path $routerTestRoot 'home'
    $env:WORKBUDDY_CONFIG_DIR = Join-Path $env:USERPROFILE '.workbuddy'
    $env:LOOMY_CONFIG_DIR = Join-Path $routerTestRoot 'loomy\opencode'
    $null = & $Executable config init
    if ($LASTEXITCODE -ne 0) { throw 'Isolated config initialization failed.' }
    $routerTestReserve = New-Object Net.Sockets.TcpListener([Net.IPAddress]::Loopback, 0)
    $routerTestReserve.Start()
    $routerTestOriginalPort = $routerTestReserve.LocalEndpoint.Port
    $routerTestReserve.Stop()
    $routerTestConfigPath = Join-Path $env:APPDATA 'api-router\config.json'
    $routerTestConfig = Get-Content -LiteralPath $routerTestConfigPath -Raw | ConvertFrom-Json
    $routerTestConfig.listen.address = "127.0.0.1:$routerTestOriginalPort"
    [IO.File]::WriteAllText($routerTestConfigPath, ($routerTestConfig | ConvertTo-Json -Depth 30))
    $routerTestInitial = Invoke-ApiRouterCommand -Executable $Executable -Arguments @('service', 'start')
    $routerTestProvider = Invoke-PortTestAPI $routerTestInitial 'api/cloud-providers' @{name = 'local fixture'; protocol = 'chat'; baseURL = 'http://127.0.0.1:1/v1'; proxyMode = 'direct'; apiKey = 'fixture-key-never-sent'}
    foreach ($routerTestHost in @('workbuddy', 'loomy')) {
        $null = Invoke-PortTestAPI $routerTestInitial 'api/apps' @{id = $routerTestHost; revision = 0; name = $routerTestHost; cloud = @{providerId = $routerTestProvider.provider.id; model = 'fixture-model'}; credentialMode = 'stored'; redactionEnabled = $false; referencePrice = $null}
    }
    New-Item -ItemType Directory -Path $env:WORKBUDDY_CONFIG_DIR, $env:LOOMY_CONFIG_DIR -Force | Out-Null
    $routerTestWorkBuddyPath = Join-Path $env:WORKBUDDY_CONFIG_DIR 'models.json'
    $routerTestLoomyPath = Join-Path $env:LOOMY_CONFIG_DIR 'opencode.json'
    $routerTestWorkBuddy = @(@{id = 'fixture-model'; name = 'API Router'; url = $routerTestInitial.managementURL.TrimEnd('/') + '/app/workbuddy/v1'; apiKey = 'api-router-local-placeholder'; supportsToolCall = $true; useCustomProtocol = $false; later = 'keep'}) | ConvertTo-Json -Depth 10
    # Force the desktop's array shape even with a single model.
    if (-not $routerTestWorkBuddy.TrimStart().StartsWith('[')) { $routerTestWorkBuddy = '[' + $routerTestWorkBuddy + ']' }
    [IO.File]::WriteAllText($routerTestWorkBuddyPath, $routerTestWorkBuddy)
    [IO.File]::WriteAllText((Join-Path $env:WORKBUDDY_CONFIG_DIR 'settings.json'), '{"model":"custom-local:fixture-model","theme":"dark"}')
    $routerTestLoomy = @{provider = @{api_router = @{npm = '@ai-sdk/openai-compatible'; name = 'API Router'; options = @{baseURL = $routerTestInitial.managementURL.TrimEnd('/') + '/app/loomy/v1'; apiKey = 'api-router-local-placeholder'; useSessionAuth = $false; timeout = 1200000}; models = @{'fixture-model' = @{name = 'fixture-model'; tool_call = $true}}}}; model = 'api_router/fixture-model'; later = 'keep'} | ConvertTo-Json -Depth 15
    [IO.File]::WriteAllText($routerTestLoomyPath, $routerTestLoomy)
    $routerTestCredentialsPath = Join-Path $env:APPDATA 'api-router\credentials.json'
    $routerTestCredentialsBefore = (Get-FileHash -LiteralPath $routerTestCredentialsPath).Hash
    $null = Invoke-ApiRouterCommand -Executable $Executable -Arguments @('service', 'stop')
    $routerTestOccupied = New-Object Net.Sockets.TcpListener([Net.IPAddress]::Loopback, $routerTestOriginalPort)
    $routerTestOccupied.Start()
    $routerTestListeners.Add($routerTestOccupied)
    $routerTestStarted = Invoke-ApiRouterCommand -Executable $Executable -Arguments @('service', 'start')
    Assert-PortTest ($routerTestStarted.running -and $routerTestStarted.address -ne "127.0.0.1:$routerTestOriginalPort") 'An unrelated listener prevented automatic port fallback.'
    Assert-PortTest ($routerTestStarted.previousAddress -eq "127.0.0.1:$routerTestOriginalPort") 'The original occupied address was lost.'
    foreach ($routerTestHost in @('workbuddy', 'loomy')) {
        $routerTestPath = if ($routerTestHost -eq 'workbuddy') { $routerTestWorkBuddyPath } else { $routerTestLoomyPath }
        $routerTestRaw = [IO.File]::ReadAllText($routerTestPath)
        Assert-PortTest ($routerTestRaw.Contains($routerTestStarted.managementURL.TrimEnd('/') + "/app/$routerTestHost/v1")) "$routerTestHost did not follow the fallback port."
        Assert-PortTest ($routerTestRaw.Contains('keep')) "$routerTestHost lost an unrelated field."
    }
    $routerTestConfig = Get-Content -LiteralPath $routerTestConfigPath -Raw | ConvertFrom-Json
    Assert-PortTest ($routerTestConfig.listen.address -eq $routerTestStarted.address) 'The actual bound address was not saved.'
    $routerTestAgain = Invoke-ApiRouterCommand -Executable $Executable -Arguments @('service', 'start')
    Assert-PortTest ($routerTestAgain.pid -eq $routerTestStarted.pid) 'Repeated startup created another process.'
    Write-Host 'PASS: occupied port selects a bound replacement and synchronizes WorkBuddy and Loomy before readiness'

    $routerTestOccupied.Stop()
    $routerTestRestarted = Invoke-ApiRouterCommand -Executable $Executable -Arguments @('service', 'restart')
    Assert-PortTest ($routerTestRestarted.address -eq $routerTestStarted.address) 'Restart returned to the former default port.'
    Write-Host 'PASS: saved replacement port remains stable after the original port is released'

    $routerTestProcess = [Diagnostics.Process]::GetProcessById($routerTestRestarted.pid)
    $routerTestProcess.Kill()
    Assert-PortTest ($routerTestProcess.WaitForExit(5000)) 'Owned test service did not exit.'
    $routerTestProcess.Dispose()
    $routerTestReplacementPort = ([Uri]$routerTestRestarted.managementURL).Port
    $routerTestSecondOccupant = New-Object Net.Sockets.TcpListener([Net.IPAddress]::Loopback, $routerTestReplacementPort)
    $routerTestSecondOccupant.Start()
    $routerTestListeners.Add($routerTestSecondOccupant)
    $routerTestRecovered = Invoke-ApiRouterCommand -Executable $Executable -Arguments @('service', 'start')
    Assert-PortTest ($routerTestRecovered.running -and $routerTestRecovered.address -ne $routerTestRestarted.address) 'An exited lock plus an occupied old port blocked native startup.'
    foreach ($routerTestHost in @('workbuddy', 'loomy')) {
        $routerTestPath = if ($routerTestHost -eq 'workbuddy') { $routerTestWorkBuddyPath } else { $routerTestLoomyPath }
        Assert-PortTest ([IO.File]::ReadAllText($routerTestPath).Contains($routerTestRecovered.managementURL.TrimEnd('/') + "/app/$routerTestHost/v1")) "$routerTestHost did not follow a second fallback."
    }
    Assert-PortTest ((Get-FileHash -LiteralPath $routerTestCredentialsPath).Hash -eq $routerTestCredentialsBefore) 'Port migration changed saved credentials.'
    Write-Host 'PASS: native crash recovery leaves the other listener intact and synchronizes a second fallback without changing credentials'
    $routerTestPassed = $true
} finally {
    foreach ($routerTestListener in $routerTestListeners) { $routerTestListener.Stop() }
    try {
        $null = Invoke-ApiRouterCommand -Executable $Executable -Arguments @('service', 'stop')
        $routerTestCleanup = $true
    } finally {
        foreach ($routerTestKey in $routerTestSavedEnvironment.Keys) {
            [Environment]::SetEnvironmentVariable($routerTestKey, $routerTestSavedEnvironment[$routerTestKey], 'Process')
        }
    }
    if ($routerTestCleanup -and $routerTestPassed -and [IO.Path]::GetFullPath($routerTestRoot).StartsWith([IO.Path]::GetTempPath(), [StringComparison]::OrdinalIgnoreCase) -and
        (Split-Path -Leaf $routerTestRoot).StartsWith('router-auto-port-test-')) {
        Remove-Item -LiteralPath $routerTestRoot -Recurse -Force
    } elseif (-not $routerTestPassed) {
        Write-Host "Failed test evidence retained: $routerTestRoot"
    }
}
