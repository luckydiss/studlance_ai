# Builds studlance-server.exe and studlance-worker.exe on Windows.
# Usage: powershell -ExecutionPolicy Bypass -File scripts/build.ps1
# Optional: -SkipWeb to skip the frontend build.

param(
    [switch]$SkipWeb
)

$ErrorActionPreference = "Stop"

$Root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
Set-Location -LiteralPath $Root

$Bin = Join-Path $Root "bin"
New-Item -ItemType Directory -Force -Path $Bin | Out-Null

if (-not $SkipWeb) {
    Push-Location (Join-Path $Root "web")
    try {
        pnpm install
        if ($LASTEXITCODE -ne 0) { throw "pnpm install failed" }
        pnpm build
        if ($LASTEXITCODE -ne 0) { throw "pnpm build failed" }
    }
    finally {
        Pop-Location
    }
}

$env:GOOS = "windows"
$env:GOARCH = "amd64"
go build -o (Join-Path $Bin "studlance-server.exe") ./cmd/server
if ($LASTEXITCODE -ne 0) { throw "go build server failed" }
go build -o (Join-Path $Bin "studlance-worker.exe") ./cmd/worker
if ($LASTEXITCODE -ne 0) { throw "go build worker failed" }

Write-Host "Built:"
Write-Host "  $Bin\studlance-server.exe"
Write-Host "  $Bin\studlance-worker.exe"