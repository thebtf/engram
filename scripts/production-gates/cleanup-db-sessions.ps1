[CmdletBinding()]
param(
    [string]$AdminDsn = $env:ENGRAM_TEST_ADMIN_DSN,
    [string]$DatabaseName,
    [string]$SchemaName = 'public',
    [string]$PostgresContainer,
    [string]$ArtifactRoot = '.agent/reports/evidence/production-ready/release-gates-foundation',
    [string]$RunId,
    [switch]$Help,
    [switch]$SelfTest
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ExpectedPrefix = 'engram_prc_rg_'

function Show-Help {
    @'
cleanup-db-sessions.ps1

Fail-closed cleanup for a disposable RELEASE-GATES PostgreSQL database. It
captures pg_stat_activity, terminates remaining sessions, drops the database
with FORCE, verifies absence, and writes raw stdout/stderr plus cleanup.json.

Usage:
  pwsh ./scripts/production-gates/cleanup-db-sessions.ps1 \
    -AdminDsn <postgres-admin-dsn> \
    -DatabaseName <engram_prc_rg_...> \
    [-SchemaName public] \
    [-PostgresContainer <container>] \
    -ArtifactRoot <run-directory> \
    -RunId <run-id>

Safety:
  Only names beginning with the fixed `engram_prc_rg_` prefix and matching
  PostgreSQL identifier rules can be dropped. The prefix is not configurable;
  system and operator-owned databases are rejected.

Options:
  -Help               Print this help and exit 0.
  -SelfTest           Run deterministic safety/redaction tests without a DB.
  -AdminDsn           Administrative PostgreSQL URL; password is never logged.
  -PostgresContainer  Run psql through docker exec; otherwise host psql is used.

Exit codes:
  0  Sessions terminated (if any), database dropped, absence verified.
  1  Unsafe input, psql failure, termination/drop failure, or failed verification.
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
    $builder.UserName = [uri]::EscapeDataString($connection.User)
    $builder.Password = 'REDACTED'
    return $builder.Uri.AbsoluteUri
}

function Protect-Text {
    param([string]$Text, [Parameter(Mandatory)]$Connection)
    if ($null -eq $Text) { return '' }
    $protected = [string]$Text
    if (-not [string]::IsNullOrEmpty($Connection.Original)) { $protected = $protected.Replace($Connection.Original, (Get-RedactedDsn $Connection.Original)) }
    if (-not [string]::IsNullOrEmpty($Connection.Password)) {
        $protected = $protected.Replace(":" + $Connection.Password + "@", ':REDACTED@')
        $escaped = [regex]::Escape($Connection.Password)
        $protected = [regex]::Replace($protected, "(?i)(password|pwd|PGPASSWORD)(\s*[:=]\s*)$escaped", '$1$2REDACTED')
    }
    return $protected
}

function Assert-SafeDatabaseName {
    param([Parameter(Mandatory)][string]$Name, [Parameter(Mandatory)][string]$Prefix)
    if ($Name -in @('postgres', 'template0', 'template1')) { throw "refusing to clean protected database '$Name'" }
    if (-not $Name.StartsWith($Prefix, [System.StringComparison]::Ordinal)) { throw "database '$Name' does not begin with required prefix '$Prefix'" }
    if ($Name -notmatch '^[a-z][a-z0-9_]{0,62}$') { throw "database '$Name' is not a safe PostgreSQL identifier" }
}

$script:CommandRecords = [System.Collections.Generic.List[object]]::new()

function Invoke-CapturedProcess {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$FilePath,
        [Parameter(Mandatory)][AllowEmptyCollection()][string[]]$ArgumentList,
        [hashtable]$Environment = @{},
        [Parameter(Mandatory)][string]$StdoutPath,
        [Parameter(Mandatory)][string]$StderrPath,
        [Parameter(Mandatory)]$Connection,
        [int]$TimeoutSeconds = 120
    )
    $start = [DateTimeOffset]::UtcNow
    $process = $null; $stdout = ''; $stderr = ''; $exitCode = 127; $timedOut = $false
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
        $stderr = "PROCESS_START_OR_CAPTURE_ERROR: $($_.Exception.Message)`n"; $exitCode = 127
    }
    finally { if ($null -ne $process) { $process.Dispose() } }
    $stdout = Protect-Text $stdout $Connection
    $stderr = Protect-Text $stderr $Connection
    Write-Utf8NoBom $StdoutPath $stdout; Write-Utf8NoBom $StderrPath $stderr
    $end = [DateTimeOffset]::UtcNow
    $displayArgs = @($ArgumentList | ForEach-Object { Protect-Text $_ $Connection })
    $record = [pscustomobject]@{
        name = $Name; executable = $FilePath; arguments = $displayArgs; environment_keys = @($Environment.Keys | Sort-Object); command = (@($FilePath) + $displayArgs) -join ' '
        started_at = $start.ToString('O'); finished_at = $end.ToString('O'); duration_seconds = [math]::Round(($end - $start).TotalSeconds, 3)
        exit_code = $exitCode; timed_out = $timedOut; stdout = [System.IO.Path]::GetFullPath($StdoutPath); stderr = [System.IO.Path]::GetFullPath($StderrPath)
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
    if (-not [string]::IsNullOrWhiteSpace($Container)) {
        return Invoke-CapturedProcess $Name 'docker' @('exec', $Container, 'psql', '-X', '-v', 'ON_ERROR_STOP=1', '-U', $Connection.User, '-d', $Database, '-At', '-F', '|', '-c', $Sql) @{} "$OutputStem.stdout.log" "$OutputStem.stderr.log" $Connection
    }
    $psql = Get-Command psql -ErrorAction SilentlyContinue
    $psqlPath = if ($null -ne $psql) { $psql.Source } else { 'psql' }
    $environment = @{}; if ($Connection.Password) { $environment.PGPASSWORD = $Connection.Password }; if ($Connection.SslMode) { $environment.PGSSLMODE = $Connection.SslMode }
    return Invoke-CapturedProcess $Name $psqlPath @('-X', '-v', 'ON_ERROR_STOP=1', '-h', $Connection.Host, '-p', [string]$Connection.Port, '-U', $Connection.User, '-d', $Database, '-At', '-F', '|', '-c', $Sql) $environment "$OutputStem.stdout.log" "$OutputStem.stderr.log" $Connection
}

function Assert-SelfTest { param([bool]$Condition, [string]$Message); if (-not $Condition) { throw "SELFTEST FAIL: $Message" } }

function Get-CleanupStatus {
    param([Parameter(Mandatory)][string]$Verdict, [AllowNull()]$DatabaseExistedBefore, [Parameter(Mandatory)][int]$Remaining)
    if ($Verdict -ne 'PASS') { return 'FAIL' }
    if ($DatabaseExistedBefore -eq $true -and $Remaining -eq 0) { return 'PASS' }
    if ($DatabaseExistedBefore -eq $false -and $Remaining -eq 0) { return 'NOT_APPLICABLE' }
    return 'FAIL'
}

function Invoke-SelfTest {
    Assert-SafeDatabaseName 'engram_prc_rg_20260710_abcd1234_r1' 'engram_prc_rg_'
    $unsafeRejected = $false; try { Assert-SafeDatabaseName 'engram' 'engram_prc_rg_' } catch { $unsafeRejected = $true }
    Assert-SelfTest $unsafeRejected 'unsafe database name was accepted'
    $connection = Get-ConnectionInfo 'postgres://release_user:s3cr3t@localhost:55432/postgres?sslmode=disable'
    Assert-SelfTest ($connection.User -eq 'release_user' -and $connection.Port -eq 55432 -and $connection.SslMode -eq 'disable') 'DSN parsing failed'
    $protected = Protect-Text 'dsn=postgres://release_user:s3cr3t@localhost:55432/postgres?sslmode=disable password=s3cr3t' $connection
    Assert-SelfTest (-not $protected.Contains('s3cr3t')) 'DSN/password redaction failed'
    Assert-SelfTest ((Get-CleanupStatus 'PASS' $true 0) -eq 'PASS') 'existing database cleanup status was not PASS'
    Assert-SelfTest ((Get-CleanupStatus 'PASS' $false 0) -eq 'NOT_APPLICABLE') 'absent database cleanup status was not NOT_APPLICABLE'
    Assert-SelfTest ((Get-CleanupStatus 'PASS' $false 1) -eq 'FAIL') 'non-zero absence proof was accepted'
    Write-Output 'SELFTEST PASS: cleanup-db-sessions.ps1'
}

if ($Help) { Show-Help; exit 0 }
if ($SelfTest) { Invoke-SelfTest; exit 0 }
if ([string]::IsNullOrWhiteSpace($AdminDsn)) { Write-Error '-AdminDsn or ENGRAM_TEST_ADMIN_DSN is required.'; exit 1 }
if ([string]::IsNullOrWhiteSpace($DatabaseName)) { Write-Error '-DatabaseName is required.'; exit 1 }
if ([string]::IsNullOrWhiteSpace($RunId)) { $RunId = 'cleanup-' + [DateTimeOffset]::UtcNow.ToString('yyyyMMddTHHmmssZ') }

$cleanupDirectory = Join-Path $ArtifactRoot 'cleanup'; New-Item -ItemType Directory -Path $cleanupDirectory -Force | Out-Null
$summaryPath = Join-Path $cleanupDirectory 'cleanup.json'
$connection = $null; $verdict = 'FAIL'; [int]$remaining = -1; $terminated = $null; $databaseExistedBefore = $null
$errors = [System.Collections.Generic.List[string]]::new()

try {
    Assert-SafeDatabaseName $DatabaseName $ExpectedPrefix
    if ($SchemaName -notmatch '^[a-z][a-z0-9_]{0,62}$') { throw "schema '$SchemaName' is not a safe PostgreSQL identifier" }
    $connection = Get-ConnectionInfo $AdminDsn
    # Cleanup never trusts the database component of the caller's DSN. An early
    # create/start/capture failure may have used an invalid or now-missing
    # database, while the run-owned database can still exist. PostgreSQL's
    # maintenance database is the stable control-plane connection for DROP.
    $maintenanceDatabase = 'postgres'
    $quotedDb = '"' + $DatabaseName.Replace('"', '""') + '"'
    $literalDb = $DatabaseName.Replace("'", "''")
    $existsBefore = Invoke-Psql 'database-exists-before-cleanup' "SELECT count(*) FROM pg_database WHERE datname = '$literalDb';" $maintenanceDatabase (Join-Path $cleanupDirectory 'database-exists-before') $connection $PostgresContainer
    [int]$existsBeforeCount = -1
    if ($existsBefore.ExitCode -ne 0) { $errors.Add("database existence check failed with exit $($existsBefore.ExitCode)") }
    elseif (-not [int]::TryParse($existsBefore.Stdout.Trim(), [ref]$existsBeforeCount)) { $errors.Add("database existence check returned non-integer '$($existsBefore.Stdout.Trim())'") }
    else { $databaseExistedBefore = $existsBeforeCount -gt 0 }
    $snapshotSql = "SELECT COALESCE(json_agg(row_to_json(s)), '[]'::json)::text FROM (SELECT pid, usename, datname, state, backend_type, application_name, client_addr::text AS client_addr, wait_event_type, wait_event, query_start FROM pg_stat_activity WHERE datname = '$literalDb' ORDER BY pid) AS s;"
    $before = Invoke-Psql 'pg-stat-activity-before-cleanup' $snapshotSql $maintenanceDatabase (Join-Path $cleanupDirectory 'pg-stat-activity-before') $connection $PostgresContainer
    if ($before.ExitCode -ne 0) { $errors.Add("pg_stat_activity snapshot failed with exit $($before.ExitCode)") }

    $terminateSql = "SELECT COALESCE(json_agg(row_to_json(s)), '[]'::json)::text FROM (SELECT pid, pg_terminate_backend(pid) AS terminated FROM pg_stat_activity WHERE datname = '$literalDb' AND pid <> pg_backend_pid() ORDER BY pid) AS s;"
    $terminate = Invoke-Psql 'terminate-database-sessions' $terminateSql $maintenanceDatabase (Join-Path $cleanupDirectory 'terminate-sessions') $connection $PostgresContainer
    if ($terminate.ExitCode -ne 0) { $errors.Add("session termination failed with exit $($terminate.ExitCode)") }
    else {
        try {
            $terminationRows = @($terminate.Stdout.Trim() | ConvertFrom-Json)
            $terminated = @($terminationRows | Where-Object terminated -eq $true).Count
            $failedTerminations = @($terminationRows | Where-Object terminated -ne $true).Count
            if ($failedTerminations -gt 0) { $errors.Add("$failedTerminations database session(s) refused termination") }
        }
        catch { $errors.Add("could not parse termination result: $($_.Exception.Message)") }
    }

    $drop = Invoke-Psql 'drop-fresh-database' "DROP DATABASE IF EXISTS $quotedDb WITH (FORCE);" $maintenanceDatabase (Join-Path $cleanupDirectory 'drop-database') $connection $PostgresContainer
    if ($drop.ExitCode -ne 0) { $errors.Add("database drop failed with exit $($drop.ExitCode)") }
    $verify = Invoke-Psql 'verify-database-absent' "SELECT count(*) FROM pg_database WHERE datname = '$literalDb';" $maintenanceDatabase (Join-Path $cleanupDirectory 'verify-database-absent') $connection $PostgresContainer
    if ($verify.ExitCode -ne 0) { $errors.Add("database absence verification failed with exit $($verify.ExitCode)") }
    elseif (-not [int]::TryParse($verify.Stdout.Trim(), [ref]$remaining)) { $errors.Add("database absence verification returned non-integer '$($verify.Stdout.Trim())'") }
    elseif ($remaining -ne 0) { $errors.Add('database still exists after cleanup') }
    if ($errors.Count -eq 0) { $verdict = 'PASS' }
}
catch { $errors.Add($_.Exception.Message) }
finally {
    $cleanupStatus = Get-CleanupStatus $verdict $databaseExistedBefore $remaining
    $summary = [pscustomobject]@{
        schema_version = 1; run_id = $RunId; timestamp = [DateTimeOffset]::UtcNow.ToString('O'); verdict = $verdict
        database = $DatabaseName; schema = $SchemaName; database_schema_identity = "$DatabaseName.$SchemaName"
        admin_dsn = if ($null -ne $connection) { Get-RedactedDsn $AdminDsn } else { 'REDACTED' }
        postgres_container = if ([string]::IsNullOrWhiteSpace($PostgresContainer)) { $null } else { $PostgresContainer }
        cleanup_status = $cleanupStatus; cleanup_attempted = $script:CommandRecords.Count -gt 0; database_existed_before = $databaseExistedBefore
        absence_verified = $remaining -eq 0
        terminated_sessions = $terminated; remaining_database_count = $remaining
        commands = @($script:CommandRecords); errors = @($errors)
    }
    Write-Utf8NoBom $summaryPath (($summary | ConvertTo-Json -Depth 10) + "`n")
    Write-Output "cleanup verdict=$verdict database=$DatabaseName schema=$SchemaName terminated_sessions=$terminated remaining_database_count=$remaining"
    Write-Output "summary=$([System.IO.Path]::GetFullPath($summaryPath))"
}
if ($verdict -ne 'PASS') { exit 1 }
exit 0
