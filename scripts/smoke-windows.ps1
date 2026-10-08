# Exercises the Windows executables exactly as they will be packaged.
# This starts one synthetic server only; it never starts a worker or agent.
param(
    [Parameter(Mandatory = $true)]
    [string]$BinDir,
    [string]$SmokeRoot
)

$ErrorActionPreference = "Stop"

function Assert-That([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw $Message }
}

function Quote-WindowsArgument([string]$Value) {
    $builder = New-Object System.Text.StringBuilder
    [void]$builder.Append('"')
    $slashes = 0
    foreach ($character in $Value.ToCharArray()) {
        if ($character -eq [char]92) {
            $slashes++
            continue
        }
        if ($character -eq [char]34) {
            [void]$builder.Append(('\' * (2 * $slashes + 1)))
            [void]$builder.Append('"')
            $slashes = 0
            continue
        }
        if ($slashes -gt 0) { [void]$builder.Append(('\' * $slashes)) }
        [void]$builder.Append($character)
        $slashes = 0
    }
    if ($slashes -gt 0) { [void]$builder.Append(('\' * (2 * $slashes))) }
    [void]$builder.Append('"')
    return $builder.ToString()
}

function Invoke-PackagedCommand([string]$Executable, [string[]]$Arguments, [string]$InputLine = $null) {
    $quotedArguments = $Arguments | ForEach-Object { Quote-WindowsArgument $_ }
    $start = New-Object System.Diagnostics.ProcessStartInfo
    $start.FileName = $Executable
    $start.Arguments = $quotedArguments -join " "
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardInput = $null -ne $InputLine
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $process = New-Object System.Diagnostics.Process
    $process.StartInfo = $start
    if (-not $process.Start()) { throw "Could not start packaged command" }
    $stdout = $process.StandardOutput.ReadToEndAsync()
    $stderr = $process.StandardError.ReadToEndAsync()
    if ($null -ne $InputLine) {
        $process.StandardInput.WriteLine($InputLine)
        $process.StandardInput.Close()
    }
    $process.WaitForExit()
    $result = [pscustomobject]@{ ExitCode = $process.ExitCode; Stdout = $stdout.Result; Stderr = $stderr.Result }
    $process.Dispose()
    return $result
}

function New-SyntheticPassword {
    $bytes = New-Object byte[] 32
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
    return [Convert]::ToBase64String($bytes)
}

function Wait-ForHealthyServer($Process, [string]$Origin) {
    $health = $null
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    while ([DateTime]::UtcNow -lt $deadline) {
        if ($Process.HasExited) { throw "Packaged server exited before /healthz became ready" }
        try {
            $health = Invoke-RestMethod -Uri "$Origin/healthz" -TimeoutSec 2
            if ($health.ok -eq $true) { break }
        } catch { }
        Start-Sleep -Milliseconds 250
    }
    Assert-That ($null -ne $health -and $health.ok -eq $true) "Packaged server /healthz did not become healthy"
}

function Get-WebResponse([string]$Uri, $Session) {
    if ($null -eq $Session) {
        return Invoke-WebRequest -Uri $Uri -UseBasicParsing -TimeoutSec 8
    }
    return Invoke-WebRequest -Uri $Uri -WebSession $Session -UseBasicParsing -TimeoutSec 8
}

function Test-Frontend([string]$Origin, [string]$Path, $Session, [string]$Label) {
    $response = Get-WebResponse "$Origin$Path" $Session
    Assert-That ([int]$response.StatusCode -eq 200) "$Label returned HTTP $($response.StatusCode)"
    $htmlContentType = [string]$response.Headers["Content-Type"]
    Assert-That ($htmlContentType -match "^text/html") "$Label did not return HTML"
    $html = [string]$response.Content
    Assert-That ($html -notmatch "Фронт не собран") "$Label returned the unbuilt frontend placeholder"

    $assetRefs = [regex]::Matches($html, '(?:src|href)="([^"?#]+\.(?:js|css))')
    $jsCount = 0
    $cssCount = 0
    foreach ($match in $assetRefs) {
        $assetPath = $match.Groups[1].Value
        $assetUri = ([System.Uri]::new([System.Uri]$Origin, $assetPath)).AbsoluteUri
        $asset = Get-WebResponse $assetUri $Session
        Assert-That ([int]$asset.StatusCode -eq 200) "$Label asset failed: $assetPath"
        $contentType = [string]$asset.Headers["Content-Type"]
        Assert-That ($asset.Content.Length -gt 0) "$Label asset is empty: $assetPath"
        if ($assetPath -match '\.js$') {
            Assert-That ($contentType -match '^(text|application)/javascript') "$Label JS MIME is '$contentType'"
            $jsCount++
        } elseif ($assetPath -match '\.css$') {
            Assert-That ($contentType -match '^text/css') "$Label CSS MIME is '$contentType'"
            $cssCount++
        }
    }
    Assert-That ($jsCount -gt 0) "$Label index did not reference a JavaScript bundle"
    Assert-That ($cssCount -gt 0) "$Label index did not reference a CSS bundle"
    return ($jsCount + $cssCount)
}

$resolvedBin = (Resolve-Path -LiteralPath $BinDir).Path
$serverExe = Join-Path $resolvedBin "studlance-server.exe"
$workerExe = Join-Path $resolvedBin "studlance-worker.exe"
Assert-That (Test-Path -LiteralPath $serverExe -PathType Leaf) "Missing studlance-server.exe in package"
Assert-That (Test-Path -LiteralPath $workerExe -PathType Leaf) "Missing studlance-worker.exe in package"

if ([string]::IsNullOrWhiteSpace($SmokeRoot)) {
    $SmokeRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("studlance-smoke-" + [Guid]::NewGuid().ToString("N"))
}
if (Test-Path -LiteralPath $SmokeRoot) { throw "SmokeRoot must be new and empty: $SmokeRoot" }
$SmokeRoot = [System.IO.Path]::GetFullPath($SmokeRoot)
$dataDir = Join-Path $SmokeRoot "data"
New-Item -ItemType Directory -Force -Path $dataDir | Out-Null

$listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
$listener.Start()
$port = ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
$listener.Stop()
$origin = "http://127.0.0.1:$port"
$stdoutPath = Join-Path $SmokeRoot "server.stdout.log"
$stderrPath = Join-Path $SmokeRoot "server.stderr.log"
$restoreStdoutPath = Join-Path $SmokeRoot "restore-server.stdout.log"
$restoreStderrPath = Join-Path $SmokeRoot "restore-server.stderr.log"
$serverProcess = $null
$assetCount = 0

try {
    $migrate = Invoke-PackagedCommand -Executable $serverExe -Arguments @("migrate", "--data", $dataDir)
    if ($migrate.ExitCode -ne 0) { throw "Packaged migrate failed (exit $($migrate.ExitCode))" }

    $password = New-SyntheticPassword
    $clientPassword = New-SyntheticPassword
    $createAdmin = Invoke-PackagedCommand -Executable $serverExe -Arguments @("user", "create", "--email", "smoke-admin@example.test", "--role", "admin", "--name", "Synthetic smoke admin", "--password-stdin", "--data", $dataDir) -InputLine $password
    if ($createAdmin.ExitCode -ne 0) { throw "Synthetic admin setup failed (exit $($createAdmin.ExitCode))" }
    $createClient = Invoke-PackagedCommand -Executable $serverExe -Arguments @("user", "create", "--email", "smoke-client@example.test", "--role", "client", "--name", "Synthetic smoke client", "--password-stdin", "--data", $dataDir) -InputLine $clientPassword
    if ($createClient.ExitCode -ne 0) { throw "Synthetic client setup failed (exit $($createClient.ExitCode))" }

    $blobDir = Join-Path $dataDir "blobs\synthetic-smoke"
    New-Item -ItemType Directory -Force -Path $blobDir | Out-Null
    $blobPath = Join-Path $blobDir "probe.bin"
    [System.IO.File]::WriteAllBytes($blobPath, [System.Text.Encoding]::UTF8.GetBytes("synthetic backup blob bytes`r`n"))
    $expectedBlobHash = (Get-FileHash -LiteralPath $blobPath -Algorithm SHA256).Hash

    $serverArguments = @("serve", "--data", (Quote-WindowsArgument $dataDir), "--addr", (Quote-WindowsArgument "127.0.0.1:$port")) -join " "
    $serverProcess = Start-Process -FilePath $serverExe -ArgumentList $serverArguments -WorkingDirectory $resolvedBin -WindowStyle Hidden -PassThru -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath
    Wait-ForHealthyServer $serverProcess $origin

    foreach ($path in @("/", "/login", "/orders/synthetic-smoke")) {
        $assetCount += Test-Frontend $origin $path $null "client $path"
    }

    $session = $null
    $loginBody = @{ email = "smoke-admin@example.test"; password = $password } | ConvertTo-Json -Compress
    $login = Invoke-WebRequest -Uri "$origin/api/auth/login" -Method Post -Headers @{ Origin = $origin } -ContentType "application/json" -Body $loginBody -SessionVariable session -UseBasicParsing -TimeoutSec 8
    Assert-That ([int]$login.StatusCode -eq 200) "Synthetic admin login failed"
    foreach ($path in @("/admin", "/admin/jobs/synthetic-smoke")) {
        $assetCount += Test-Frontend $origin $path $session "admin $path"
    }

    Stop-Process -Id $serverProcess.Id -Force
    $serverProcess.WaitForExit(5000) | Out-Null
    $serverProcess.Dispose()
    $serverProcess = $null

    $backupPath = Join-Path $SmokeRoot "synthetic-backup.zip"
    $backup = Invoke-PackagedCommand -Executable $serverExe -Arguments @("backup", "--out", $backupPath, "--data", $dataDir)
    if ($backup.ExitCode -ne 0) { throw "Packaged backup failed (exit $($backup.ExitCode))" }

    $restoreData = Join-Path $SmokeRoot "restore data"
    if (Test-Path -LiteralPath $restoreData) { throw "Restore destination must be new and empty" }
    New-Item -ItemType Directory -Path $restoreData | Out-Null
    Expand-Archive -LiteralPath $backupPath -DestinationPath $restoreData
    Assert-That (Test-Path -LiteralPath (Join-Path $restoreData "studlance.db") -PathType Leaf) "Backup restore has no database"
    $restoredBlob = Join-Path $restoreData "blobs\synthetic-smoke\probe.bin"
    Assert-That (Test-Path -LiteralPath $restoredBlob -PathType Leaf) "Backup restore has no synthetic blob"
    $actualBlobHash = (Get-FileHash -LiteralPath $restoredBlob -Algorithm SHA256).Hash
    Assert-That ($actualBlobHash -eq $expectedBlobHash) "Restored blob bytes differ from the original"

    $restoreArguments = @("serve", "--data", (Quote-WindowsArgument $restoreData), "--addr", (Quote-WindowsArgument "127.0.0.1:$port")) -join " "
    $serverProcess = Start-Process -FilePath $serverExe -ArgumentList $restoreArguments -WorkingDirectory $resolvedBin -WindowStyle Hidden -PassThru -RedirectStandardOutput $restoreStdoutPath -RedirectStandardError $restoreStderrPath
    Wait-ForHealthyServer $serverProcess $origin
    $restoredSession = $null
    $restoredLogin = Invoke-WebRequest -Uri "$origin/api/auth/login" -Method Post -Headers @{ Origin = $origin } -ContentType "application/json" -Body $loginBody -SessionVariable restoredSession -UseBasicParsing -TimeoutSec 8
    Assert-That ([int]$restoredLogin.StatusCode -eq 200) "Restored database did not accept the synthetic admin login"
    $restoredAdminPage = Test-Frontend $origin "/admin/jobs/synthetic-smoke" $restoredSession "restored admin deep link"
    $assetCount += $restoredAdminPage

    Write-Host "Windows package smoke passed: migrate/user create/serve, /healthz, client/admin deep links, $assetCount JS/CSS assets with MIME types, backup/restore, restored admin login, and synthetic blob SHA-256. No worker or agent was started."
}
finally {
    if ($null -ne $serverProcess) {
        if (-not $serverProcess.HasExited) {
            Stop-Process -Id $serverProcess.Id -Force
            $serverProcess.WaitForExit(5000) | Out-Null
        }
        $serverProcess.Dispose()
    }
}
