[CmdletBinding()]
param(
    [string[]]$Package = @('./...'),
    [string]$Run,
    [switch]$FreshDatabase,
    [ValidateRange(1, 20)][int]$Repeat = 1,
    [switch]$FailOnUnexpectedSkip,
    [string[]]$AllowedSkipPattern = @(),
    [string]$AdminDsn = $env:ENGRAM_TEST_ADMIN_DSN,
    [string]$PostgresContainer,
    [string]$PostgresImage = 'pgvector/pgvector:pg17',
    [ValidateRange(1, 99)][int]$ConnectionBudget = 20,
    [ValidateSet('Auto', 'Full', 'Targeted')][string]$CoveragePolicy = 'Auto',
    [ValidateRange(1, 120)][int]$TimeoutMinutes = 30,
    [string]$ArtifactRoot = '.agent/reports/evidence/production-ready/release-gates-foundation',
    [string]$RunId,
    [switch]$Help,
    [switch]$SelfTest
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Show-Help {
    @'
run-db-suite.ps1

Runs Go database tests against a fresh disposable PostgreSQL database for every
repetition. Every child exit code is captured independently; a later success
can never mask an earlier failure. Raw stdout/stderr and machine JSON are under:

  .agent/reports/evidence/production-ready/
    release-gates-foundation/<run-id>/

Usage:
  pwsh ./scripts/production-gates/run-db-suite.ps1 \
    -FreshDatabase [-Package ./...] [-Run '<regex>'] [-Repeat 3] \
    [-FailOnUnexpectedSkip] [-AdminDsn <url>] \
    [-PostgresContainer <container>]

Required behavior:
  * -FreshDatabase is mandatory.
  * Each repeat creates a unique `<database>.public` identity.
  * `go test` runs with `-json -p 1 -parallel 1 -count=1`.
  * Missing coverage is fatal. Full `./...` runs enforce >=60% overall and the
    historical package floors. Scoped runs retain mandatory targeted coverage.
  * pg_stat_activity and server headroom are captured before/after tests. Pool
    capacity is bounded by -ConnectionBudget; post-test run-DB sessions must be
    exactly zero before cleanup. Cleanup still terminates/drops after failure.

Options:
  -Help                    Print this help and exit 0.
  -SelfTest                Prove exit aggregation/redaction without PostgreSQL.
  -Package                 Go package patterns; whitespace-delimited input expands.
  -Run                     Go test -run regular expression.
  -FreshDatabase           Required fail-closed release mode.
  -Repeat                  Fresh database repetitions (1..20).
  -FailOnUnexpectedSkip    Fail on non-allowlisted test/package skips.
  -AllowedSkipPattern      Explicit regex allowlist.
  -CoveragePolicy          Auto, Full, or Targeted.
  -ConnectionBudget        App pool cap and required free server headroom.
  -AdminDsn                Admin URL or ENGRAM_TEST_ADMIN_DSN; always redacted.
  -PostgresContainer       Use psql through docker exec; else host psql.

Exit codes:
  0  Every setup/test/parser/coverage/cleanup command passed for every repeat.
  1  Any child/process/assertion/setup/cleanup failure occurred.
'@ | Write-Output
}

function Write-Utf8NoBom {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][AllowEmptyString()][string]$Content)
    $parent = Split-Path -Parent $Path
    if ($parent) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }
    [System.IO.File]::WriteAllText([System.IO.Path]::GetFullPath($Path), $Content, [System.Text.UTF8Encoding]::new($false))
}

function Get-ConnectionInfo {
    param([Parameter(Mandatory)][string]$Dsn)
    try { $uri = [uri]$Dsn } catch { throw "Admin DSN is not a valid URI: $($_.Exception.Message)" }
    if ($uri.Scheme -notin @('postgres', 'postgresql')) { throw "Admin DSN scheme must be postgres or postgresql, got '$($uri.Scheme)'" }
    $parts = $uri.UserInfo -split ':', 2
    $user = if ($parts.Count -ge 1) { [uri]::UnescapeDataString($parts[0]) } else { '' }
    $password = if ($parts.Count -eq 2) { [uri]::UnescapeDataString($parts[1]) } else { '' }
    if ([string]::IsNullOrWhiteSpace($user)) { throw 'Admin DSN must contain a user.' }
    $database = $uri.AbsolutePath.Trim('/'); if ([string]::IsNullOrWhiteSpace($database)) { $database = 'postgres' }
    $sslMode = $null
    foreach ($pair in $uri.Query.TrimStart('?').Split('&', [System.StringSplitOptions]::RemoveEmptyEntries)) {
        $kv = $pair -split '=', 2
        if ([uri]::UnescapeDataString($kv[0]) -eq 'sslmode' -and $kv.Count -eq 2) { $sslMode = [uri]::UnescapeDataString($kv[1]) }
    }
    [pscustomobject]@{
        Uri = $uri; User = $user; Password = $password; Host = $uri.Host
        Port = if ($uri.IsDefaultPort -or $uri.Port -lt 1) { 5432 } else { $uri.Port }
        Database = $database; SslMode = $sslMode; Original = $Dsn
    }
}

function Get-RedactedDsn {
    param([Parameter(Mandatory)][string]$Dsn)
    $connection = Get-ConnectionInfo $Dsn
    $builder = [System.UriBuilder]::new($connection.Uri)
    $builder.UserName = [uri]::EscapeDataString($connection.User); $builder.Password = 'REDACTED'
    return $builder.Uri.AbsoluteUri
}

function New-DatabaseDsn {
    param([Parameter(Mandatory)][string]$Dsn, [Parameter(Mandatory)][string]$Database, [Parameter(Mandatory)][string]$ApplicationName)
    $builder = [System.UriBuilder]::new([uri]$Dsn); $builder.Path = "/$Database"
    $pairs = [System.Collections.Generic.List[string]]::new()
    foreach ($pair in $builder.Query.TrimStart('?').Split('&', [System.StringSplitOptions]::RemoveEmptyEntries)) {
        $key = [uri]::UnescapeDataString(($pair -split '=', 2)[0]); if ($key -ne 'application_name') { $pairs.Add($pair) }
    }
    $pairs.Add('application_name=' + [uri]::EscapeDataString($ApplicationName)); $builder.Query = $pairs -join '&'
    return $builder.Uri.AbsoluteUri
}

function Protect-Text {
    param([string]$Text, [Parameter(Mandatory)]$Connection, [string[]]$SensitiveValues = @())
    if ($null -eq $Text) { return '' }
    $protected = [string]$Text
    foreach ($value in $SensitiveValues) { if ($value) { $protected = $protected.Replace($value, 'REDACTED_DATABASE_DSN') } }
    if ($Connection.Original) { $protected = $protected.Replace($Connection.Original, (Get-RedactedDsn $Connection.Original)) }
    if ($Connection.Password) {
        $protected = $protected.Replace(":" + $Connection.Password + "@", ':REDACTED@')
        $escaped = [regex]::Escape($Connection.Password)
        $protected = [regex]::Replace($protected, "(?i)(password|pwd|PGPASSWORD)(\s*[:=]\s*)$escaped", '$1$2REDACTED')
    }
    return $protected
}

function Get-NormalizedPackages {
    param([string[]]$RawPackages)
    $normalized = [System.Collections.Generic.List[string]]::new()
    foreach ($raw in $RawPackages) { foreach ($item in ([string]$raw -split '\s+')) { if ($item) { $normalized.Add($item) } } }
    if ($normalized.Count -eq 0) { throw 'At least one -Package value is required.' }
    return @($normalized)
}

function Get-EffectiveCoveragePolicy {
    param([string]$Requested, [string[]]$Packages)
    if ($Requested -ne 'Auto') { return $Requested }
    if ($Packages.Count -eq 1 -and $Packages[0] -eq './...') { return 'Full' }
    return 'Targeted'
}

$script:CommandRecords = [System.Collections.Generic.List[object]]::new()

function Invoke-CapturedProcess {
    param(
        [Parameter(Mandatory)][string]$Name, [Parameter(Mandatory)][string]$FilePath,
        [Parameter(Mandatory)][AllowEmptyCollection()][string[]]$ArgumentList, [hashtable]$Environment = @{},
        [Parameter(Mandatory)][string]$StdoutPath, [Parameter(Mandatory)][string]$StderrPath,
        [Parameter(Mandatory)]$Connection, [string[]]$SensitiveValues = @(), [int]$TimeoutSeconds = 1800
    )
    $start = [DateTimeOffset]::UtcNow
    $process = $null; $stdout = ''; $stderr = ''; $timedOut = $false; $exitCode = 127
    try {
        $psi = [System.Diagnostics.ProcessStartInfo]::new()
        $psi.FileName = $FilePath; $psi.UseShellExecute = $false; $psi.RedirectStandardOutput = $true; $psi.RedirectStandardError = $true; $psi.CreateNoWindow = $true
        foreach ($argument in $ArgumentList) { [void]$psi.ArgumentList.Add($argument) }
        foreach ($entry in $Environment.GetEnumerator()) { $psi.Environment[$entry.Key] = [string]$entry.Value }
        $process = [System.Diagnostics.Process]::new(); $process.StartInfo = $psi
        if (-not $process.Start()) { throw "process '$FilePath' did not start" }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync(); $stderrTask = $process.StandardError.ReadToEndAsync()
        $timedOut = -not $process.WaitForExit($TimeoutSeconds * 1000)
        if ($timedOut) { try { $process.Kill($true) } catch { }; $process.WaitForExit() }
        $stdout = $stdoutTask.GetAwaiter().GetResult(); $stderr = $stderrTask.GetAwaiter().GetResult()
        $exitCode = if ($timedOut) { 124 } else { $process.ExitCode }
        if ($timedOut) { $stderr += "`nPROCESS_TIMEOUT after $TimeoutSeconds seconds`n" }
    }
    catch {
        if ($null -ne $process) { try { if (-not $process.HasExited) { $process.Kill($true); $process.WaitForExit() } } catch { } }
        $stderr = "PROCESS_START_OR_CAPTURE_ERROR: $($_.Exception.Message)`n"
        $exitCode = 127
    }
    finally { if ($null -ne $process) { $process.Dispose() } }
    $stdout = Protect-Text $stdout $Connection $SensitiveValues
    $stderr = Protect-Text $stderr $Connection $SensitiveValues
    Write-Utf8NoBom $StdoutPath $stdout; Write-Utf8NoBom $StderrPath $stderr
    $end = [DateTimeOffset]::UtcNow
    $displayArgs = @($ArgumentList | ForEach-Object { Protect-Text $_ $Connection $SensitiveValues })
    $record = [pscustomobject]@{
        name = $Name; executable = $FilePath; arguments = $displayArgs; environment_keys = @($Environment.Keys | Sort-Object); command = (@($FilePath) + $displayArgs) -join ' '
        started_at = $start.ToString('O'); finished_at = $end.ToString('O'); duration_seconds = [math]::Round(($end - $start).TotalSeconds, 3)
        exit_code = $exitCode; timed_out = $timedOut
        stdout = [System.IO.Path]::GetFullPath($StdoutPath); stderr = [System.IO.Path]::GetFullPath($StderrPath)
    }
    $script:CommandRecords.Add($record)
    [pscustomobject]@{ ExitCode = $exitCode; Stdout = $stdout; Stderr = $stderr; Record = $record }
}

function Invoke-Psql {
    param(
        [Parameter(Mandatory)][string]$Name, [Parameter(Mandatory)][string]$Sql,
        [Parameter(Mandatory)][string]$Database, [Parameter(Mandatory)][string]$OutputStem,
        [Parameter(Mandatory)]$Connection, [string]$Container
    )
    if ($Container) {
        return Invoke-CapturedProcess $Name 'docker' @('exec', $Container, 'psql', '-X', '-v', 'ON_ERROR_STOP=1', '-U', $Connection.User, '-d', $Database, '-At', '-F', '|', '-c', $Sql) @{} "$OutputStem.stdout.log" "$OutputStem.stderr.log" $Connection @() 120
    }
    $psql = Get-Command psql -ErrorAction SilentlyContinue
    $psqlPath = if ($null -ne $psql) { $psql.Source } else { 'psql' }
    $environment = @{}; if ($Connection.Password) { $environment.PGPASSWORD = $Connection.Password }; if ($Connection.SslMode) { $environment.PGSSLMODE = $Connection.SslMode }
    return Invoke-CapturedProcess $Name $psqlPath @('-X', '-v', 'ON_ERROR_STOP=1', '-h', $Connection.Host, '-p', [string]$Connection.Port, '-U', $Connection.User, '-d', $Database, '-At', '-F', '|', '-c', $Sql) $environment "$OutputStem.stdout.log" "$OutputStem.stderr.log" $Connection @() 120
}

function Get-IntegerOutput {
    param([Parameter(Mandatory)]$Result, [Parameter(Mandatory)][string]$Name)
    [int]$value = 0
    if ($Result.ExitCode -ne 0) { throw "$Name failed with exit $($Result.ExitCode)" }
    if (-not [int]::TryParse($Result.Stdout.Trim(), [ref]$value)) { throw "$Name returned non-integer '$($Result.Stdout.Trim())'" }
    return $value
}

function Test-RequiredPostgresVersion {
    param([Parameter(Mandatory)][int]$ServerVersionNumber)
    return $ServerVersionNumber -ge 170000 -and $ServerVersionNumber -lt 180000
}

function Test-PostgresImageMatch {
    param([Parameter(Mandatory)][string]$Actual, [Parameter(Mandatory)][string]$Expected)
    return [string]::Equals($Actual, $Expected, [System.StringComparison]::OrdinalIgnoreCase)
}

function Test-ConnectionBudgetFits {
    param([Parameter(Mandatory)][int]$Active, [Parameter(Mandatory)][int]$Budget, [Parameter(Mandatory)][int]$UsableCapacity)
    return ($Active + $Budget) -le $UsableCapacity
}

function Test-NoResidualRunSessions {
    param([Parameter(Mandatory)][int]$SessionCount)
    return $SessionCount -eq 0
}

function Assert-SelfTestCondition { param([bool]$Condition, [string]$Message); if (-not $Condition) { throw "SELFTEST FAIL: $Message" } }

function Invoke-SelfTest {
    $root = Join-Path ([System.IO.Path]::GetTempPath()) ("run-db-suite-" + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $root -Force | Out-Null
    try {
        $connection = Get-ConnectionInfo 'postgres://release_user:s3cr3t@localhost:55432/postgres?sslmode=disable'
        $pwsh = (Get-Command pwsh -ErrorAction Stop).Source
        $failed = Invoke-CapturedProcess 'selftest-failure' $pwsh @('-NoProfile', '-Command', 'exit 7') @{} (Join-Path $root 'failure.stdout.log') (Join-Path $root 'failure.stderr.log') $connection @() 30
        $succeeded = Invoke-CapturedProcess 'selftest-later-success' $pwsh @('-NoProfile', '-Command', 'Write-Output later-success; exit 0') @{} (Join-Path $root 'success.stdout.log') (Join-Path $root 'success.stderr.log') $connection @() 30
        $missingProcess = Invoke-CapturedProcess 'selftest-missing-process' ('engram-missing-process-' + [guid]::NewGuid().ToString('N')) @() @{} (Join-Path $root 'missing.stdout.log') (Join-Path $root 'missing.stderr.log') $connection @() 30
        $aggregateFailed = $failed.ExitCode -ne 0 -or $succeeded.ExitCode -ne 0
        Assert-SelfTestCondition ($failed.ExitCode -eq 7) 'failing child exit code was not captured'
        Assert-SelfTestCondition ($succeeded.ExitCode -eq 0) 'later success did not execute'
        Assert-SelfTestCondition $aggregateFailed 'later success masked the earlier failure'
        Assert-SelfTestCondition ($missingProcess.ExitCode -eq 127 -and $missingProcess.Stderr -match 'PROCESS_START_OR_CAPTURE_ERROR') 'process start failure did not produce captured raw evidence and exit 127'
        $targetDsn = New-DatabaseDsn $connection.Original 'engram_prc_rg_selftest' 'engram-prc-selftest'
        $redacted = Protect-Text "DATABASE_DSN=$targetDsn" $connection @($targetDsn)
        Assert-SelfTestCondition (-not $redacted.Contains('s3cr3t') -and -not $redacted.Contains('engram_prc_rg_selftest')) 'generated DATABASE_DSN was not fully redacted'
        Assert-SelfTestCondition ((Get-EffectiveCoveragePolicy Auto @('./...')) -eq 'Full') 'Auto coverage did not select Full'
        Assert-SelfTestCondition ((Get-EffectiveCoveragePolicy Auto @('./internal/db/gorm')) -eq 'Targeted') 'Auto coverage did not select Targeted'
        Assert-SelfTestCondition (Test-RequiredPostgresVersion 170010) 'PostgreSQL 17 identity was rejected'
        Assert-SelfTestCondition (-not (Test-RequiredPostgresVersion 160010)) 'PostgreSQL 16 identity was accepted'
        Assert-SelfTestCondition (-not (Test-RequiredPostgresVersion 180000)) 'PostgreSQL 18 identity was accepted'
        Assert-SelfTestCondition (Test-PostgresImageMatch 'pgvector/pgvector:pg17' 'PGVECTOR/PGVECTOR:PG17') 'exact pgvector image comparison should be case-insensitive'
        Assert-SelfTestCondition (-not (Test-PostgresImageMatch 'postgres:17' 'pgvector/pgvector:pg17')) 'non-pgvector image was accepted'
        Assert-SelfTestCondition (Test-ConnectionBudgetFits 6 20 97) 'valid connection headroom was rejected'
        Assert-SelfTestCondition (-not (Test-ConnectionBudgetFits 80 20 97)) 'exhausted connection headroom was accepted'
        Assert-SelfTestCondition (Test-NoResidualRunSessions 0) 'zero post-test sessions were rejected'
        Assert-SelfTestCondition (-not (Test-NoResidualRunSessions 1)) 'a residual post-test session was accepted within the pool budget'
        Write-Output 'SELFTEST PASS: run-db-suite.ps1 (earlier exit 7 remained fatal after later exit 0)'
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

if ($Help) { Show-Help; exit 0 }
if ($SelfTest) { Invoke-SelfTest; exit 0 }
if (-not $FreshDatabase) { Write-Error '-FreshDatabase is mandatory for RELEASE-GATES DB evidence.'; exit 1 }
if ([string]::IsNullOrWhiteSpace($AdminDsn)) { Write-Error '-AdminDsn or ENGRAM_TEST_ADMIN_DSN is required.'; exit 1 }

[string[]]$packages = @(Get-NormalizedPackages $Package)
$effectiveCoverage = Get-EffectiveCoveragePolicy $CoveragePolicy $packages
$connection = Get-ConnectionInfo $AdminDsn
$safeRunToken = [guid]::NewGuid().ToString('N').Substring(0, 10)
if ([string]::IsNullOrWhiteSpace($RunId)) { $RunId = [DateTimeOffset]::UtcNow.ToString('yyyyMMddTHHmmssZ') + '-' + $safeRunToken }
if ($RunId -notmatch '^[A-Za-z0-9._-]+$') { Write-Error '-RunId may contain only letters, digits, dot, underscore, and hyphen.'; exit 1 }

$runDirectory = Join-Path $ArtifactRoot $RunId
if (Test-Path -LiteralPath $runDirectory) { Write-Error "artifact directory already exists; choose a unique -RunId: $runDirectory"; exit 1 }
New-Item -ItemType Directory -Path $runDirectory -Force | Out-Null
$summaryPath = Join-Path $runDirectory 'summary.json'; $commandsPath = Join-Path $runDirectory 'commands.json'; $environmentPath = Join-Path $runDirectory 'environment.json'
$repeatResults = [System.Collections.Generic.List[object]]::new(); $runErrors = [System.Collections.Generic.List[string]]::new()
$overallFailed = $false; $startedAt = [DateTimeOffset]::UtcNow
$scriptDirectory = Split-Path -Parent $PSCommandPath
$jsonAssertionScript = Join-Path $scriptDirectory 'assert-go-test-json.ps1'; $coverageAssertionScript = Join-Path $scriptDirectory 'assert-coverage.ps1'; $cleanupScript = Join-Path $scriptDirectory 'cleanup-db-sessions.ps1'
$pwshCommand = Get-Command pwsh -ErrorAction SilentlyContinue; $goCommand = Get-Command go -ErrorAction SilentlyContinue
$pwshPath = if ($null -ne $pwshCommand) { $pwshCommand.Source } else { 'pwsh' }
$goPath = if ($null -ne $goCommand) { $goCommand.Source } else { 'go' }

$goVersion = Invoke-CapturedProcess 'go-version' $goPath @('version') @{} (Join-Path $runDirectory 'go-version.stdout.log') (Join-Path $runDirectory 'go-version.stderr.log') $connection @() 60
if ($goVersion.ExitCode -ne 0) { $overallFailed = $true; $runErrors.Add("go version failed with exit $($goVersion.ExitCode)") }

$containerIdentity = $null
if ($PostgresContainer) {
    $inspect = Invoke-CapturedProcess 'postgres-container-identity' 'docker' @('inspect', '--format', '{{.Name}}|{{.Config.Image}}|{{.Image}}|{{.State.Running}}', $PostgresContainer) @{} (Join-Path $runDirectory 'postgres-container-identity.stdout.log') (Join-Path $runDirectory 'postgres-container-identity.stderr.log') $connection @() 60
    if ($inspect.ExitCode -ne 0) { $overallFailed = $true; $runErrors.Add("postgres container inspect failed with exit $($inspect.ExitCode)") }
    else {
        $parts = $inspect.Stdout.Trim() -split '\|', 4
        if ($parts.Count -ne 4) { $overallFailed = $true; $runErrors.Add('postgres container identity output was malformed') }
        else {
            try { $containerRunning = [bool]::Parse($parts[3]) } catch { $containerRunning = $false; $overallFailed = $true; $runErrors.Add('postgres container running state was malformed') }
            $containerIdentity = [ordered]@{ name = $parts[0]; configured_image = $parts[1]; image_id = $parts[2]; running = $containerRunning }
            if (-not $containerRunning) { $overallFailed = $true; $runErrors.Add('postgres container is not running') }
            if (-not (Test-PostgresImageMatch $parts[1] $PostgresImage)) {
                $overallFailed = $true; $runErrors.Add("postgres image mismatch: expected '$PostgresImage', got '$($parts[1])'")
            }
        }
    }
}

$serverIdentity = Invoke-Psql 'postgres-server-identity' "SELECT json_build_object('server_version', current_setting('server_version'), 'server_version_num', current_setting('server_version_num'), 'version', version(), 'max_connections', current_setting('max_connections'), 'superuser_reserved_connections', current_setting('superuser_reserved_connections'), 'reserved_connections', COALESCE(NULLIF(current_setting('reserved_connections', true), ''), '0'), 'current_connections', (SELECT count(*)::text FROM pg_stat_activity), 'database', current_database(), 'schema', current_schema(), 'user', current_user)::text;" $connection.Database (Join-Path $runDirectory 'postgres-server-identity') $connection $PostgresContainer
$serverIdentityObject = $null
if ($serverIdentity.ExitCode -ne 0) { $overallFailed = $true; $runErrors.Add("postgres identity query failed with exit $($serverIdentity.ExitCode)") }
else { try { $serverIdentityObject = $serverIdentity.Stdout.Trim() | ConvertFrom-Json } catch { $overallFailed = $true; $runErrors.Add("postgres identity JSON parse failed: $($_.Exception.Message)") } }
$usableConnectionCapacity = $null
if ($null -ne $serverIdentityObject) {
    try {
        $serverVersionNumber = [int]$serverIdentityObject.server_version_num
        $maxConnections = [int]$serverIdentityObject.max_connections
        $superuserReserved = [int]$serverIdentityObject.superuser_reserved_connections
        $reservedConnections = [int]$serverIdentityObject.reserved_connections
        $currentConnections = [int]$serverIdentityObject.current_connections
        $usableConnectionCapacity = $maxConnections - $superuserReserved - $reservedConnections
        if (-not (Test-RequiredPostgresVersion $serverVersionNumber)) { $overallFailed = $true; $runErrors.Add("PostgreSQL 17 is required, got server_version_num=$serverVersionNumber") }
        if ($usableConnectionCapacity -le $ConnectionBudget) { $overallFailed = $true; $runErrors.Add("connection budget $ConnectionBudget leaves no reserved headroom under usable capacity $usableConnectionCapacity") }
        if (-not (Test-ConnectionBudgetFits $currentConnections $ConnectionBudget $usableConnectionCapacity)) { $overallFailed = $true; $runErrors.Add("connection budget $ConnectionBudget exceeds current server headroom: active=$currentConnections usable=$usableConnectionCapacity") }
    }
    catch { $overallFailed = $true; $runErrors.Add("postgres numeric identity was malformed: $($_.Exception.Message)") }
}

for ($repeatIndex = 1; $repeatIndex -le $Repeat; $repeatIndex++) {
    $repeatDirectory = Join-Path $runDirectory ("repeat-{0:D2}" -f $repeatIndex); New-Item -ItemType Directory -Path $repeatDirectory -Force | Out-Null
    $databaseName = "engram_prc_rg_${safeRunToken}_r$repeatIndex"; $schemaName = 'public'; $applicationName = "engram-prc-$safeRunToken-r$repeatIndex"
    $targetDsn = New-DatabaseDsn $AdminDsn $databaseName $applicationName
    $repeatErrors = [System.Collections.Generic.List[string]]::new(); $repeatFailed = $false; $databaseCreated = $false
    $goTestExit = $null; $parserExit = $null; $coverageExit = $null; $cleanupExit = $null; $sessionsBefore = $null; $sessionsAfter = $null; $serverSessionsBefore = $null; $serverSessionsAfter = $null
    $cleanupSummaryPath = Join-Path $repeatDirectory 'cleanup/cleanup.json'

    try {
        $quotedDb = '"' + $databaseName.Replace('"', '""') + '"'; $quotedUser = '"' + $connection.User.Replace('"', '""') + '"'
        $create = Invoke-Psql "repeat-$repeatIndex-create-database" "CREATE DATABASE $quotedDb OWNER $quotedUser;" $connection.Database (Join-Path $repeatDirectory 'create-database') $connection $PostgresContainer
        if ($create.ExitCode -ne 0) { throw "create database failed with exit $($create.ExitCode)" }; $databaseCreated = $true
        $extension = Invoke-Psql "repeat-$repeatIndex-create-pgvector" 'CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;' $databaseName (Join-Path $repeatDirectory 'create-pgvector') $connection $PostgresContainer
        if ($extension.ExitCode -ne 0) { throw "create pgvector extension failed with exit $($extension.ExitCode)" }
        $identity = Invoke-Psql "repeat-$repeatIndex-database-identity" "SELECT json_build_object('database', current_database(), 'schema', current_schema(), 'server_version', current_setting('server_version'), 'user', current_user)::text;" $databaseName (Join-Path $repeatDirectory 'database-identity') $connection $PostgresContainer
        if ($identity.ExitCode -ne 0) { throw "database identity query failed with exit $($identity.ExitCode)" }
        $identityObject = $identity.Stdout.Trim() | ConvertFrom-Json
        if ($identityObject.database -ne $databaseName -or $identityObject.schema -ne $schemaName) { throw "database/schema identity mismatch: expected $databaseName.$schemaName" }

        $literalDb = $databaseName.Replace("'", "''")
        $snapshotSql = "SELECT COALESCE(json_agg(row_to_json(s)), '[]'::json)::text FROM (SELECT pid, usename, datname, state, backend_type, application_name, client_addr::text AS client_addr, wait_event_type, wait_event, query_start FROM pg_stat_activity WHERE datname = '$literalDb' ORDER BY pid) AS s;"
        $beforeSnapshot = Invoke-Psql "repeat-$repeatIndex-pg-stat-before" $snapshotSql $connection.Database (Join-Path $repeatDirectory 'pg-stat-activity-before') $connection $PostgresContainer
        if ($beforeSnapshot.ExitCode -ne 0) { throw "pg_stat_activity before snapshot failed with exit $($beforeSnapshot.ExitCode)" }
        $serverSessionsBefore = Get-IntegerOutput (Invoke-Psql "repeat-$repeatIndex-server-connection-count-before" 'SELECT count(*) FROM pg_stat_activity;' $connection.Database (Join-Path $repeatDirectory 'server-connection-count-before') $connection $PostgresContainer) 'server connection count before tests'
        if ($null -ne $usableConnectionCapacity -and -not (Test-ConnectionBudgetFits $serverSessionsBefore $ConnectionBudget $usableConnectionCapacity)) { throw "connection budget $ConnectionBudget exceeds current server headroom before tests: active=$serverSessionsBefore usable=$usableConnectionCapacity" }
        $sessionsBefore = Get-IntegerOutput (Invoke-Psql "repeat-$repeatIndex-connection-count-before" "SELECT count(*) FROM pg_stat_activity WHERE datname = '$literalDb';" $connection.Database (Join-Path $repeatDirectory 'connection-count-before') $connection $PostgresContainer) 'connection count before tests'
        if ($sessionsBefore -gt $ConnectionBudget) { throw "pre-test connection count $sessionsBefore exceeds budget $ConnectionBudget" }

        $coveragePath = Join-Path $repeatDirectory 'coverage.out'; $goJsonPath = Join-Path $repeatDirectory 'go-test.stdout.jsonl'; $goStderrPath = Join-Path $repeatDirectory 'go-test.stderr.log'
        $goArguments = [System.Collections.Generic.List[string]]::new()
        foreach ($argument in @('test', '-json', '-p', '1', '-parallel', '1', '-count=1', '-timeout', "${TimeoutMinutes}m", '-covermode=atomic', "-coverprofile=$coveragePath")) { $goArguments.Add($argument) }
        if ($Run) { $goArguments.Add('-run'); $goArguments.Add($Run) }; foreach ($pkg in $packages) { $goArguments.Add($pkg) }
        $testEnvironment = @{
            DATABASE_DSN = $targetDsn
            ENGRAM_TEST_DSN = $targetDsn
            TEST_DATABASE_DSN = $targetDsn
            DATABASE_MAX_CONNS = [string]$ConnectionBudget
            ENGRAM_RELEASE_GATE_RUN_ID = $RunId
            ENGRAM_RELEASE_GATE_REPEAT = [string]$repeatIndex
        }
        $goTest = Invoke-CapturedProcess "repeat-$repeatIndex-go-test" $goPath @($goArguments) $testEnvironment $goJsonPath $goStderrPath $connection @($targetDsn) ($TimeoutMinutes * 60 + 60)
        $goTestExit = $goTest.ExitCode; if ($goTestExit -ne 0) { $repeatFailed = $true; $repeatErrors.Add("go test failed with exit $goTestExit") }

        $parserArguments = [System.Collections.Generic.List[string]]::new()
        foreach ($argument in @('-NoProfile', '-File', $jsonAssertionScript, '-InputPath', $goJsonPath, '-SummaryPath', (Join-Path $repeatDirectory 'go-test-summary.json'))) { $parserArguments.Add($argument) }
        if ($FailOnUnexpectedSkip) { $parserArguments.Add('-FailOnUnexpectedSkip') }
        if ($AllowedSkipPattern.Count -gt 0) { $parserArguments.Add('-AllowedSkipPattern'); foreach ($pattern in $AllowedSkipPattern) { $parserArguments.Add($pattern) } }
        $parser = Invoke-CapturedProcess "repeat-$repeatIndex-assert-go-test-json" $pwshPath @($parserArguments) @{} (Join-Path $repeatDirectory 'assert-go-test-json.stdout.log') (Join-Path $repeatDirectory 'assert-go-test-json.stderr.log') $connection @($targetDsn) 120
        $parserExit = $parser.ExitCode; if ($parserExit -ne 0) { $repeatFailed = $true; $repeatErrors.Add("go test JSON assertion failed with exit $parserExit") }

        if ($effectiveCoverage -eq 'Full') {
            $coverage = Invoke-CapturedProcess "repeat-$repeatIndex-assert-coverage" $pwshPath @('-NoProfile', '-File', $coverageAssertionScript, '-CoverageProfile', $coveragePath, '-SummaryPath', (Join-Path $repeatDirectory 'coverage-summary.json'), '-OverallThreshold', '60') @{} (Join-Path $repeatDirectory 'assert-coverage.stdout.log') (Join-Path $repeatDirectory 'assert-coverage.stderr.log') $connection @($targetDsn) 120
            $coverageExit = $coverage.ExitCode; if ($coverageExit -ne 0) { $repeatFailed = $true; $repeatErrors.Add("coverage assertion failed with exit $coverageExit") }
        }
        elseif (-not (Test-Path -LiteralPath $coveragePath -PathType Leaf) -or (Get-Item -LiteralPath $coveragePath).Length -eq 0) {
            $coverageExit = 1; $repeatFailed = $true; $repeatErrors.Add('targeted coverage profile is missing or empty')
        }
        else {
            $coverTool = Invoke-CapturedProcess "repeat-$repeatIndex-targeted-coverage-report" $goPath @('tool', 'cover', "-func=$coveragePath") @{} (Join-Path $repeatDirectory 'targeted-coverage.stdout.log') (Join-Path $repeatDirectory 'targeted-coverage.stderr.log') $connection @($targetDsn) 120
            $coverageExit = $coverTool.ExitCode; if ($coverageExit -ne 0) { $repeatFailed = $true; $repeatErrors.Add("targeted coverage report failed with exit $coverageExit") }
        }

        $afterSnapshot = Invoke-Psql "repeat-$repeatIndex-pg-stat-after" $snapshotSql $connection.Database (Join-Path $repeatDirectory 'pg-stat-activity-after') $connection $PostgresContainer
        if ($afterSnapshot.ExitCode -ne 0) { $repeatFailed = $true; $repeatErrors.Add("pg_stat_activity after snapshot failed with exit $($afterSnapshot.ExitCode)") }
        try { $serverSessionsAfter = Get-IntegerOutput (Invoke-Psql "repeat-$repeatIndex-server-connection-count-after" 'SELECT count(*) FROM pg_stat_activity;' $connection.Database (Join-Path $repeatDirectory 'server-connection-count-after') $connection $PostgresContainer) 'server connection count after tests' }
        catch { $repeatFailed = $true; $repeatErrors.Add($_.Exception.Message) }
        try { $sessionsAfter = Get-IntegerOutput (Invoke-Psql "repeat-$repeatIndex-connection-count-after" "SELECT count(*) FROM pg_stat_activity WHERE datname = '$literalDb';" $connection.Database (Join-Path $repeatDirectory 'connection-count-after') $connection $PostgresContainer) 'connection count after tests' }
        catch { $repeatFailed = $true; $repeatErrors.Add($_.Exception.Message) }
        if ($null -ne $sessionsAfter -and -not (Test-NoResidualRunSessions $sessionsAfter)) { $repeatFailed = $true; $repeatErrors.Add("post-test residual connection leak: expected 0 sessions for $databaseName, observed $sessionsAfter") }
    }
    catch { $repeatFailed = $true; $repeatErrors.Add($_.Exception.Message) }
    finally {
        if ($databaseCreated) {
            $cleanupArguments = @('-NoProfile', '-File', $cleanupScript, '-DatabaseName', $databaseName, '-SchemaName', $schemaName, '-ArtifactRoot', $repeatDirectory, '-RunId', "$RunId-repeat-$repeatIndex")
            if ($PostgresContainer) { $cleanupArguments += @('-PostgresContainer', $PostgresContainer) }
            $cleanup = Invoke-CapturedProcess "repeat-$repeatIndex-cleanup" $pwshPath $cleanupArguments @{ ENGRAM_TEST_ADMIN_DSN = $AdminDsn } (Join-Path $repeatDirectory 'cleanup-process.stdout.log') (Join-Path $repeatDirectory 'cleanup-process.stderr.log') $connection @($targetDsn, $AdminDsn) 180
            $cleanupExit = $cleanup.ExitCode; if ($cleanupExit -ne 0) { $repeatFailed = $true; $repeatErrors.Add("cleanup failed with exit $cleanupExit") }
        }
        else { $cleanupExit = 0 }
        if ($repeatFailed) { $overallFailed = $true }
        $repeatResult = [pscustomobject]@{
            repeat = $repeatIndex; verdict = if ($repeatFailed) { 'FAIL' } else { 'PASS' }
            database = $databaseName; schema = $schemaName; database_schema_identity = "$databaseName.$schemaName"; database_dsn = 'REDACTED_DATABASE_DSN'
            sequential_execution = [ordered]@{ package_parallelism = 1; test_parallelism = 1 }
            connection_budget = $ConnectionBudget; server_sessions_before = $serverSessionsBefore; server_sessions_after = $serverSessionsAfter; sessions_before = $sessionsBefore; sessions_after = $sessionsAfter
            go_test_exit = $goTestExit; json_parser_exit = $parserExit; coverage_policy = $effectiveCoverage; coverage_exit = $coverageExit; cleanup_exit = $cleanupExit
            cleanup_summary = if (Test-Path -LiteralPath $cleanupSummaryPath) { [System.IO.Path]::GetFullPath($cleanupSummaryPath) } else { $null }
            errors = @($repeatErrors); artifact_directory = [System.IO.Path]::GetFullPath($repeatDirectory)
        }
        $repeatResults.Add($repeatResult); Write-Utf8NoBom (Join-Path $repeatDirectory 'repeat-summary.json') (($repeatResult | ConvertTo-Json -Depth 10) + "`n")
        Write-Output ("repeat={0} verdict={1} database_schema={2} go_test_exit={3} parser_exit={4} coverage_exit={5} cleanup_exit={6}" -f $repeatIndex, $repeatResult.verdict, $repeatResult.database_schema_identity, $goTestExit, $parserExit, $coverageExit, $cleanupExit)
    }
}

$finishedAt = [DateTimeOffset]::UtcNow
$environmentSummary = [pscustomobject]@{
    schema_version = 1; run_id = $RunId; timestamp = $startedAt.ToString('O'); go_version = $goVersion.Stdout.Trim()
    postgres = [ordered]@{ declared_image = $PostgresImage; container = $containerIdentity; server = $serverIdentityObject; admin_dsn = Get-RedactedDsn $AdminDsn }
    packages = $packages; run_pattern = if ($Run) { $Run } else { $null }; repeat = $Repeat
    fail_on_unexpected_skip = [bool]$FailOnUnexpectedSkip; allowed_skip_patterns = @($AllowedSkipPattern)
    coverage_policy = $effectiveCoverage; connection_budget = $ConnectionBudget
    sequential_execution = [ordered]@{ go_package_parallelism = 1; go_test_parallelism = 1; database_max_connections = $ConnectionBudget }
    govulncheck_policy = [ordered]@{ authoritative = @('source scan with tests', 'unstripped binary scan'); non_authoritative = 'stripped binary scan (module-level fallback when symbols are absent)' }
}
Write-Utf8NoBom $environmentPath (($environmentSummary | ConvertTo-Json -Depth 10) + "`n")
Write-Utf8NoBom $commandsPath ((ConvertTo-Json -InputObject @($script:CommandRecords.ToArray()) -Depth 10) + "`n")
$passedRepeats = @($repeatResults | Where-Object verdict -eq 'PASS').Count; $failedRepeats = @($repeatResults | Where-Object verdict -eq 'FAIL').Count
$summary = [pscustomobject]@{
    schema_version = 1; gate = 'release-gates-foundation'; run_id = $RunId
    started_at = $startedAt.ToString('O'); finished_at = $finishedAt.ToString('O'); duration_seconds = [math]::Round(($finishedAt - $startedAt).TotalSeconds, 3)
    verdict = if (-not $overallFailed -and $failedRepeats -eq 0 -and $repeatResults.Count -eq $Repeat) { 'PASS' } else { 'FAIL' }
    counts = [ordered]@{ requested_repeats = $Repeat; completed_repeats = $repeatResults.Count; passed_repeats = $passedRepeats; failed_repeats = $failedRepeats; child_commands = $script:CommandRecords.Count; nonzero_child_commands = @($script:CommandRecords | Where-Object exit_code -ne 0).Count }
    packages = $packages; run_pattern = if ($Run) { $Run } else { $null }; coverage_policy = $effectiveCoverage; connection_budget = $ConnectionBudget
    database_dsn = 'REDACTED_DATABASE_DSN'; environment = [System.IO.Path]::GetFullPath($environmentPath); commands = [System.IO.Path]::GetFullPath($commandsPath)
    repeats = @($repeatResults); errors = @($runErrors); artifact_directory = [System.IO.Path]::GetFullPath($runDirectory)
}
Write-Utf8NoBom $summaryPath (($summary | ConvertTo-Json -Depth 12) + "`n")
Write-Output ("release-gates-foundation verdict={0} repeats={1}/{2} nonzero_children={3} coverage_policy={4}" -f $summary.verdict, $passedRepeats, $Repeat, $summary.counts.nonzero_child_commands, $effectiveCoverage)
Write-Output "summary=$([System.IO.Path]::GetFullPath($summaryPath))"
if ($summary.verdict -ne 'PASS') { exit 1 }
exit 0
