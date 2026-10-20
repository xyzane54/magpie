[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Version,
    [Parameter(Mandatory = $true)][string]$OutputDir,
    [string]$GoExe = 'go'
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if ($Version -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?(?:\+[A-Za-z0-9.-]+)?$') { throw 'An explicit fixed semantic version is required.' }
if (-not [IO.Path]::IsPathRooted($OutputDir)) { throw 'OutputDir must be absolute.' }
$OutputDir = [IO.Path]::GetFullPath($OutputDir)
[IO.Directory]::CreateDirectory($OutputDir) | Out-Null
$base = "api-router-$Version-windows-amd64"
$zipPath = Join-Path $OutputDir "$base.zip"
$manifestPath = Join-Path $OutputDir "$base.manifest.json"
foreach ($target in @($zipPath, $manifestPath, (Join-Path $OutputDir 'SHA256SUMS'))) {
    if (Test-Path -LiteralPath $target) { throw "Refusing to overwrite an existing artifact: $target" }
}
$stage = Join-Path $OutputDir ('.package-stage-' + [Guid]::NewGuid().ToString('N'))
[IO.Directory]::CreateDirectory($stage) | Out-Null
$savedGOOS = $env:GOOS; $savedGOARCH = $env:GOARCH; $savedCGO = $env:CGO_ENABLED
try {
    $env:GOOS = 'windows'; $env:GOARCH = 'amd64'; $env:CGO_ENABLED = '0'
    $module = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
    & $GoExe -C $module build -trimpath -buildvcs=false -ldflags "-X main.version=$Version -buildid=" -o (Join-Path $stage 'api-router.exe') ./cmd/api-router
    if ($LASTEXITCODE -ne 0) { throw 'Windows product build failed.' }
    Copy-Item -LiteralPath (Join-Path $module 'docs/windows-delivery.md') -Destination (Join-Path $stage 'README-windows.md')
    foreach ($script in @('install-windows.ps1', 'uninstall-windows.ps1')) { Copy-Item -LiteralPath (Join-Path $PSScriptRoot $script) -Destination (Join-Path $stage $script) }
    Add-Type -AssemblyName System.IO.Compression
    $fileList = @('api-router.exe', 'README-windows.md', 'install-windows.ps1', 'uninstall-windows.ps1')
    $pins = @()
    $stream = [IO.File]::Open($zipPath, [IO.FileMode]::CreateNew)
    $archive = New-Object IO.Compression.ZipArchive($stream, [IO.Compression.ZipArchiveMode]::Create, $false)
    try {
        foreach ($name in $fileList) {
            $source = Join-Path $stage $name
            $pins += [ordered]@{ name = $name; size = (Get-Item -LiteralPath $source).Length; sha256 = (Get-FileHash -LiteralPath $source -Algorithm SHA256).Hash.ToLowerInvariant() }
            $entry = $archive.CreateEntry($name, [IO.Compression.CompressionLevel]::Optimal)
            $entry.LastWriteTime = [DateTimeOffset]::new(2000, 1, 1, 0, 0, 0, [TimeSpan]::Zero)
            $input = [IO.File]::OpenRead($source); $output = $entry.Open()
            try { $input.CopyTo($output) } finally { $input.Dispose(); $output.Dispose() }
        }
    } finally { $archive.Dispose(); $stream.Dispose() }
    $manifest = [ordered]@{
        schemaVersion = 1; product = 'api-router'; version = $Version; os = 'windows'; architecture = 'amd64'
        package = [ordered]@{ name = "$base.zip"; size = (Get-Item -LiteralPath $zipPath).Length; sha256 = (Get-FileHash -LiteralPath $zipPath -Algorithm SHA256).Hash.ToLowerInvariant() }
        files = $pins
    }
    $utf8 = New-Object Text.UTF8Encoding($false)
    [IO.File]::WriteAllText($manifestPath, ($manifest | ConvertTo-Json -Depth 8) + "`n", $utf8)
    $checksums = foreach ($file in @($zipPath, $manifestPath)) { (Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash.ToLowerInvariant() + '  ' + [IO.Path]::GetFileName($file) }
    [IO.File]::WriteAllText((Join-Path $OutputDir 'SHA256SUMS'), ($checksums -join "`n") + "`n", $utf8)
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'install-windows.ps1') -Destination (Join-Path $OutputDir 'install-windows.ps1')
    Write-Output "Package: $zipPath"
    Write-Output "Manifest: $manifestPath"
    Write-Output 'No upload or public release was performed.'
} finally {
    $env:GOOS = $savedGOOS; $env:GOARCH = $savedGOARCH; $env:CGO_ENABLED = $savedCGO
    if (Test-Path -LiteralPath $stage) {
        $cleanupTarget = [IO.Path]::GetFullPath($stage)
        $cleanupParent = [IO.Path]::GetFullPath((Join-Path $OutputDir '.'))
        if ([IO.Path]::GetDirectoryName($cleanupTarget).TrimEnd('\', '/') -ine $cleanupParent.TrimEnd('\', '/') -or [IO.Path]::GetFileName($cleanupTarget) -notmatch '^\.package-stage-[0-9a-f]{32}$') { throw "Refusing cleanup outside the expected package stage: $cleanupTarget" }
        $cleanupItem = Get-Item -LiteralPath $cleanupTarget -Force
        if (($cleanupItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "Refusing cleanup of a reparse point: $cleanupTarget" }
        foreach ($child in (Get-ChildItem -LiteralPath $cleanupTarget -Force)) {
            if ($child.PSIsContainer -or ($child.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "Refusing cleanup of unexpected stage contents: $($child.FullName)" }
        }
        Remove-Item -LiteralPath $cleanupTarget -Recurse -Force
    }
}
