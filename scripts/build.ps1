# Builds a complete Windows release pair, including both embedded SPAs.
# Usage: powershell -ExecutionPolicy Bypass -File scripts/build.ps1

param([string]$SmokeRoot)

$ErrorActionPreference = "Stop"

$Root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
Set-Location -LiteralPath $Root

$Bin = Join-Path $Root "bin"
New-Item -ItemType Directory -Force -Path $Bin | Out-Null

$NodeVersion = (node --version).Trim()
if ($LASTEXITCODE -ne 0 -or $NodeVersion -notmatch '^v22\.') { throw "Node.js 22 is required (found '$NodeVersion')" }

Push-Location (Join-Path $Root "web")
try {
    $PnpmVersion = (pnpm --version).Trim()
    if ($LASTEXITCODE -ne 0 -or $PnpmVersion -ne "9.15.0") { throw "pnpm 9.15.0 is required (found '$PnpmVersion')" }
    pnpm install --frozen-lockfile
    if ($LASTEXITCODE -ne 0) { throw "pnpm install --frozen-lockfile failed ($LASTEXITCODE)" }
    pnpm build
    if ($LASTEXITCODE -ne 0) { throw "pnpm build failed ($LASTEXITCODE)" }
}
finally {
    Pop-Location
}

$OldGOOS = $env:GOOS
$OldGOARCH = $env:GOARCH
try {
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    go build -o (Join-Path $Bin "studlance-server.exe") ./cmd/server
    if ($LASTEXITCODE -ne 0) { throw "go build server failed ($LASTEXITCODE)" }
    go build -o (Join-Path $Bin "studlance-worker.exe") ./cmd/worker
    if ($LASTEXITCODE -ne 0) { throw "go build worker failed ($LASTEXITCODE)" }
}
finally {
    $env:GOOS = $OldGOOS
    $env:GOARCH = $OldGOARCH
}

if ([string]::IsNullOrWhiteSpace($SmokeRoot)) {
    & (Join-Path $Root "scripts/smoke-windows.ps1") -BinDir $Bin
}
else {
    & (Join-Path $Root "scripts/smoke-windows.ps1") -BinDir $Bin -SmokeRoot $SmokeRoot
}
if ($LASTEXITCODE -ne 0) { throw "Windows packaged smoke failed ($LASTEXITCODE)" }

Write-Host "Complete Windows package built and smoke-tested:"
Write-Host "  $Bin\studlance-server.exe"
Write-Host "  $Bin\studlance-worker.exe"
