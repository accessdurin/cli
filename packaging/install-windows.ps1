param(
    [Parameter(Mandatory = $true)][ValidatePattern('^\d+\.\d+\.\d+$')][string]$Version,
    [string]$ArchivePath,
    [string]$ChecksumPath
)
$ErrorActionPreference = 'Stop'
if (-not [Environment]::Is64BitOperatingSystem) { throw 'Durin CLI requires 64-bit Windows.' }
$asset = "durin-$Version-windows-x64.zip"
$base = "https://github.com/accessdurin/cli/releases/download/v$Version"
$temporary = Join-Path ([IO.Path]::GetTempPath()) ("durin-cli-" + [Guid]::NewGuid().ToString('N'))
$root = Join-Path $env:LOCALAPPDATA 'DurinCLI'
$target = Join-Path $root "versions\$Version"
New-Item -ItemType Directory -Path $temporary | Out-Null
try {
    if (-not $ArchivePath) {
        $ArchivePath = Join-Path $temporary $asset
        Invoke-WebRequest "$base/$asset" -OutFile $ArchivePath
    }
    if (-not $ChecksumPath) {
        $ChecksumPath = Join-Path $temporary 'SHA256SUMS'
        Invoke-WebRequest "$base/SHA256SUMS" -OutFile $ChecksumPath
    }
    $lines = @(Get-Content -LiteralPath $ChecksumPath | Where-Object { $_ -match ("^[a-f0-9]{64}  " + [Regex]::Escape($asset) + '$') })
    if ($lines.Count -ne 1) { throw 'Missing or ambiguous release checksum.' }
    $expected = $lines[0].Substring(0, 64)
    if ((Get-FileHash -LiteralPath $ArchivePath -Algorithm SHA256).Hash.ToLowerInvariant() -ne $expected) {
        throw 'The release archive checksum does not match.'
    }
    $expanded = Join-Path $temporary 'expanded'
    Expand-Archive -LiteralPath $ArchivePath -DestinationPath $expanded
    $node = Join-Path $expanded 'runtime\node.exe'
    $cli = Join-Path $expanded 'dist\cli.js'
    & $node $cli --help | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'The packaged CLI could not start.' }
    if (Test-Path -LiteralPath $target) { throw 'This version is already installed. No files were replaced.' }
    New-Item -ItemType Directory -Path (Split-Path $target) -Force | Out-Null
    Move-Item -LiteralPath $expanded -Destination $target
    $bin = Join-Path $root 'bin'
    New-Item -ItemType Directory -Path $bin -Force | Out-Null
    $shim = "@echo off`r`n`"$target\runtime\node.exe`" `"$target\dist\cli.js`" %*`r`n"
    $stagedShim = Join-Path $bin ('durin-' + [Guid]::NewGuid().ToString('N') + '.cmd')
    [IO.File]::WriteAllText($stagedShim, $shim, (New-Object Text.UTF8Encoding($false)))
    Move-Item -LiteralPath $stagedShim -Destination (Join-Path $bin 'durin.cmd') -Force
    $currentPath = [string][Environment]::GetEnvironmentVariable('Path', 'User')
    if (@($currentPath -split ';') -notcontains $bin) {
        [Environment]::SetEnvironmentVariable('Path', (($currentPath.TrimEnd(';') + ';' + $bin).TrimStart(';')), 'User')
    }
    Write-Host "Durin CLI $Version installed. Open a new terminal and run durin."
    Write-Host 'Existing organization profiles and previous installed versions were retained.'
} finally {
    Remove-Item -LiteralPath $temporary -Recurse -Force
}
