#requires -Version 5.1
param([Parameter(Mandatory = $true)][string]$Executable)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '..\start-project.ps1')

$routerTestRoot = Join-Path ([IO.Path]::GetTempPath()) ('router-layout-test-' + [Guid]::NewGuid().ToString('N'))
$routerTestAppData = Join-Path $routerTestRoot 'roaming'
$routerTestLocalAppData = Join-Path $routerTestRoot 'local'
$routerTestHost = Join-Path $routerTestRoot 'workbuddy'
$routerTestSavedAppData = $env:APPDATA
$routerTestSavedLocalAppData = $env:LOCALAPPDATA
$routerTestCleanup = $false
New-Item -ItemType Directory -Path $routerTestRoot | Out-Null
try {
    $env:APPDATA = $routerTestAppData
    $env:LOCALAPPDATA = $routerTestLocalAppData
    $null = & $Executable config init
    if ($LASTEXITCODE -ne 0) { throw 'Isolated config initialization failed.' }
    $routerTestListener = New-Object Net.Sockets.TcpListener([Net.IPAddress]::Loopback, 0)
    $routerTestListener.Start()
    $routerTestPort = $routerTestListener.LocalEndpoint.Port
    $routerTestListener.Stop()
    $routerTestConfigPath = Join-Path $routerTestAppData 'api-router\config.json'
    $routerTestConfig = Get-Content -LiteralPath $routerTestConfigPath -Raw | ConvertFrom-Json
    $routerTestConfig.listen.address = "127.0.0.1:$routerTestPort"
    [IO.File]::WriteAllText($routerTestConfigPath, ($routerTestConfig | ConvertTo-Json -Depth 30))
    $routerTestFirst = Invoke-ApiRouterCommand -Executable $Executable -Arguments @('service', 'start')
    if ($routerTestFirst.address -ne "127.0.0.1:$routerTestPort") { throw 'The isolated service used the wrong port.' }
    $null = Invoke-ApiRouterCommand -Executable $Executable -Arguments @('service', 'stop')
    # Run the page/CLI's copied restart command from a terminal with a different
    # layout. Neither layout is the real user's configuration.
    $env:APPDATA = Join-Path $routerTestRoot 'wrong-roaming'
    $env:LOCALAPPDATA = Join-Path $routerTestRoot 'wrong-local'
    $routerTestRestartText = & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -Command $routerTestFirst.startCommand
    if ($LASTEXITCODE -ne 0) { throw 'Copied restart command failed.' }
    $routerTestRestart = $routerTestRestartText | ConvertFrom-Json
    if ($routerTestRestart.address -ne "127.0.0.1:$routerTestPort" -or -not $routerTestRestart.running) { throw 'Copied restart switched to another configuration/port.' }
    if (Test-Path -LiteralPath $env:APPDATA) { throw 'Copied command initialized the wrong configuration directory.' }
    $routerTestAgainText = & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -Command $routerTestFirst.startCommand
    if ($LASTEXITCODE -ne 0) { throw 'Repeated startup failed.' }
    $routerTestAgain = $routerTestAgainText | ConvertFrom-Json
    if ($routerTestAgain.pid -ne $routerTestRestart.pid) { throw 'Repeated startup created another instance.' }
    Write-Host 'PASS: copied restart keeps the actual port/configuration; repeated start reuses its PID'

    Start-ApiRouterProject -Executable $Executable -AppData $routerTestAppData -LocalAppData $routerTestLocalAppData -WorkBuddyRoot $routerTestHost -NoBrowser -CheckOnly
    $routerTestConfig.listen.address = '127.0.0.1:1'
    [IO.File]::WriteAllText($routerTestConfigPath, ($routerTestConfig | ConvertTo-Json -Depth 30))
    $routerTestMismatch = $false
    try {
        Start-ApiRouterProject -Executable $Executable -AppData $routerTestAppData -LocalAppData $routerTestLocalAppData -WorkBuddyRoot $routerTestHost -NoBrowser -CheckOnly
    } catch {
        if ($_.Exception.Message -notmatch 'saved listen address differs') { throw }
        $routerTestMismatch = $true
    }
    if (-not $routerTestMismatch) { throw 'Saved/running listen address mismatch was not detected.' }
    $routerTestConfig.listen.address = "127.0.0.1:$routerTestPort"
    [IO.File]::WriteAllText($routerTestConfigPath, ($routerTestConfig | ConvertTo-Json -Depth 30))
    Write-Host 'PASS: changed saved listen address cannot be reported as a matching running service'

    $env:APPDATA = $routerTestAppData
    $env:LOCALAPPDATA = $routerTestLocalAppData
    $routerTestProcess = [Diagnostics.Process]::GetProcessById($routerTestRestart.pid)
    # Only the service this test just started is terminated, simulating an
    # uncatchable exit without involving the user's running instance.
    $routerTestProcess.Kill()
    if (-not $routerTestProcess.WaitForExit(5000)) { throw 'Owned test service did not exit.' }
    $routerTestProcess.Dispose()
    Start-ApiRouterProject -Executable $Executable -AppData $routerTestAppData -LocalAppData $routerTestLocalAppData -WorkBuddyRoot $routerTestHost -NoBrowser
    $routerTestRecovered = Invoke-ApiRouterCommand -Executable $Executable -Arguments @('service', 'status')
    $routerTestState = Join-Path $routerTestLocalAppData 'api-router\state'
    $routerTestArchives = @(Get-ChildItem -LiteralPath $routerTestState -Directory -Filter 'router.lock.exited-*')
    if ($routerTestRecovered.address -ne "127.0.0.1:$routerTestPort" -or $routerTestArchives.Count -ne 1) { throw 'Crash recovery changed the port or lost the old lock.' }
    Write-Host 'PASS: forced-exit recovery preserves the old lock and restarts on the same configured port'
} finally {
    try {
        $env:APPDATA = $routerTestAppData
        $env:LOCALAPPDATA = $routerTestLocalAppData
        $null = Invoke-ApiRouterCommand -Executable $Executable -Arguments @('service', 'stop')
        $routerTestCleanup = $true
    } finally {
        $env:APPDATA = $routerTestSavedAppData
        $env:LOCALAPPDATA = $routerTestSavedLocalAppData
    }
    if ($routerTestCleanup -and [IO.Path]::GetFullPath($routerTestRoot).StartsWith([IO.Path]::GetTempPath(), [StringComparison]::OrdinalIgnoreCase) -and
        (Split-Path -Leaf $routerTestRoot).StartsWith('router-layout-test-')) {
        Remove-Item -LiteralPath $routerTestRoot -Recurse -Force
    }
}
