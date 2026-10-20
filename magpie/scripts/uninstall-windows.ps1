[CmdletBinding()]
param([string]$InstallDir = $PSScriptRoot, [switch]$Execute)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if (-not [IO.Path]::IsPathRooted($InstallDir)) { throw 'InstallDir must be absolute.' }
$InstallDir = [IO.Path]::GetFullPath($InstallDir)
$exe = Join-Path $InstallDir 'api-router.exe'
$action = 'preview'
if ($Execute) { $action = 'execute' }
& $exe uninstall $action --install-dir $InstallDir
if ($LASTEXITCODE -ne 0) { throw 'Uninstall was blocked. Review the host recovery instructions and resolve any conflicts before retrying.' }
if ($Execute) { Write-Output "User data and offline host recovery tool retained: $exe" }
