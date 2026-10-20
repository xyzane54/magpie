[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$ManifestPath,
    [Parameter(Mandatory = $true)][string]$PackagePath,
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs/API-router')
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
function Assert-PlainPath([string]$Path) {
    if (-not [IO.Path]::IsPathRooted($Path)) { throw 'Manifest, package and installation paths must be absolute.' }
    if ($Path -notmatch '^[A-Za-z]:[\\/]') { throw 'Use a local drive path; UNC and device namespaces are unsupported.' }
    foreach ($component in $Path.Substring(2).Replace('/', '\').Split('\')) {
        if ($component -eq '.' -or $component -eq '..' -or $component.EndsWith('.') -or $component.EndsWith(' ')) { throw 'Dot components and trailing dot/space path aliases are refused.' }
    }
    $cursor = [IO.Path]::GetFullPath($Path)
    while ($cursor) {
        if (Test-Path -LiteralPath $cursor) {
            $item = Get-Item -LiteralPath $cursor -Force
            if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "Links and reparse points are refused: $cursor" }
        }
        $parent = [IO.Path]::GetDirectoryName($cursor)
        if ($parent -eq $cursor) { break }
        $cursor = $parent
    }
}
function Assert-Pin($File, $Pin) {
    if (($File.Length -ne $Pin.size) -or ((Get-FileHash -LiteralPath $File.FullName -Algorithm SHA256).Hash.ToLowerInvariant() -cne $Pin.sha256)) { throw "Size or SHA256 mismatch: $($File.Name)" }
}
if ($env:OS -ne 'Windows_NT' -or ($env:PROCESSOR_ARCHITECTURE -ne 'AMD64' -and $env:PROCESSOR_ARCHITEW6432 -ne 'AMD64')) { throw 'This package supports Windows x64 only.' }
foreach ($path in @($ManifestPath, $PackagePath, $InstallDir)) { Assert-PlainPath $path }
$ManifestPath = [IO.Path]::GetFullPath($ManifestPath); $PackagePath = [IO.Path]::GetFullPath($PackagePath); $InstallDir = [IO.Path]::GetFullPath($InstallDir)
$manifestFile = Get-Item -LiteralPath $ManifestPath
if ($manifestFile.PSIsContainer -or $manifestFile.Length -le 0 -or $manifestFile.Length -gt 65536) { throw 'Invalid manifest file size or type.' }
$m = [IO.File]::ReadAllText($ManifestPath) | ConvertFrom-Json
if ($m.schemaVersion -ne 1 -or $m.product -cne 'api-router' -or $m.version -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?(?:\+[A-Za-z0-9.-]+)?$') { throw 'An explicit fixed api-router schema 1 manifest is required.' }
if ($m.os -cne 'windows' -or $m.architecture -cne 'amd64') { throw 'Wrong package platform; Windows amd64 is required.' }
$names = @('api-router.exe', 'README-windows.md', 'install-windows.ps1', 'uninstall-windows.ps1')
if (@($m.files).Count -ne 4) { throw 'Manifest must contain the four exact managed files.' }
$pins = @{}; [long]$total = 0
foreach ($pin in $m.files) {
    if ($names -cnotcontains $pin.name -or $pins.ContainsKey($pin.name) -or $pin.size -le 0 -or $pin.size -gt 268435456 -or $pin.sha256 -cnotmatch '^[0-9a-f]{64}$') { throw 'Invalid, duplicate or unexpected manifest file.' }
    $pins[$pin.name] = $pin; $total += $pin.size
}
if ($total -gt 268435456 -or $m.package.size -le 0 -or $m.package.size -gt 268435456 -or $m.package.sha256 -cnotmatch '^[0-9a-f]{64}$') { throw 'Package exceeds the size bound or has an invalid hash.' }
$packageFile = Get-Item -LiteralPath $PackagePath
if ($packageFile.PSIsContainer) { throw 'Package must be a regular ZIP file.' }
Assert-Pin $packageFile $m.package
$bootstrapTempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
Assert-PlainPath $bootstrapTempRoot
$stage = Join-Path $bootstrapTempRoot ('api-router-bootstrap-' + [Guid]::NewGuid().ToString('N'))
[IO.Directory]::CreateDirectory($stage) | Out-Null
try {
    Add-Type -AssemblyName System.IO.Compression
    $stream = [IO.File]::OpenRead($PackagePath)
    $archive = New-Object IO.Compression.ZipArchive($stream, [IO.Compression.ZipArchiveMode]::Read, $false)
    try {
        if ($archive.Entries.Count -ne 4) { throw 'ZIP must contain the four exact managed files.' }
        $seen = @{}
        foreach ($entry in $archive.Entries) {
            $unixType = ($entry.ExternalAttributes -shr 16) -band 61440
            if (-not $pins.ContainsKey($entry.FullName) -or $seen.ContainsKey($entry.FullName) -or ($names -cnotcontains $entry.FullName) -or ($unixType -ne 0 -and $unixType -ne 32768) -or (($entry.ExternalAttributes -band 1040) -ne 0)) { throw 'ZIP contains an unexpected path, duplicate, directory, link or reparse point.' }
            $pin = $pins[$entry.FullName]
            if ($entry.Length -ne $pin.size) { throw 'ZIP entry size does not match manifest.' }
            $seen[$entry.FullName] = $true
            $target = Join-Path $stage $entry.FullName
            $input = $entry.Open(); $output = [IO.File]::Open($target, [IO.FileMode]::CreateNew)
            try {
                $buffer = New-Object byte[] 65536; [long]$written = 0
                while (($count = $input.Read($buffer, 0, $buffer.Length)) -gt 0) {
                    $written += $count
                    if ($written -gt $pin.size) { throw 'ZIP entry expands beyond its manifest bound.' }
                    $output.Write($buffer, 0, $count)
                }
                $output.Flush($true)
            } finally { $input.Dispose(); $output.Dispose() }
            Assert-Pin (Get-Item -LiteralPath $target) $pin
        }
    } finally { $archive.Dispose(); $stream.Dispose() }
    $exe = Join-Path $stage 'api-router.exe'
    $reader = New-Object IO.BinaryReader([IO.File]::OpenRead($exe))
    try {
        if ($reader.ReadUInt16() -ne 23117) { throw 'Executable is not Windows PE.' }
        $reader.BaseStream.Position = 60; $offset = $reader.ReadInt32()
        if ($offset -lt 64 -or $offset -gt ($reader.BaseStream.Length - 26)) { throw 'Invalid PE header offset.' }
        $reader.BaseStream.Position = $offset
        if ($reader.ReadUInt32() -ne 17744 -or $reader.ReadUInt16() -ne 34404) { throw 'Executable must be Windows PE amd64.' }
        $reader.BaseStream.Position = $offset + 22; $flags = $reader.ReadUInt16()
        if (($flags -band 2) -eq 0 -or ($flags -band 8192) -ne 0) { throw 'PE payload is not an executable application.' }
    } finally { $reader.Dispose() }
    & $exe install --manifest $ManifestPath --package $PackagePath --install-dir $InstallDir
    if ($LASTEXITCODE -ne 0) { throw 'Verified product installer failed; existing installation was preserved.' }
    Write-Output "Installed into: $InstallDir"
    Write-Output 'Login autostart is not enabled by installation. Choose explicitly with: api-router.exe service autostart enable; disable it with: api-router.exe service autostart disable.'
    Write-Output 'Installation never downloads, enables or loads a model. Start with: api-router.exe service start'
} finally {
    if (Test-Path -LiteralPath $stage) {
        $cleanupTarget = [IO.Path]::GetFullPath($stage)
        if ([IO.Path]::GetDirectoryName($cleanupTarget).TrimEnd('\', '/') -ine $bootstrapTempRoot.TrimEnd('\', '/') -or [IO.Path]::GetFileName($cleanupTarget) -notmatch '^api-router-bootstrap-[0-9a-f]{32}$') { throw "Refusing cleanup outside the expected bootstrap stage: $cleanupTarget" }
        Assert-PlainPath $cleanupTarget
        foreach ($child in (Get-ChildItem -LiteralPath $cleanupTarget -Force)) {
            if ($child.PSIsContainer -or ($child.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "Refusing cleanup of unexpected stage contents: $($child.FullName)" }
        }
        Remove-Item -LiteralPath $cleanupTarget -Recurse -Force
    }
}
