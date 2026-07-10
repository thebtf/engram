[CmdletBinding()]
param(
    [string[]]$Package = @('./...'),
    [string]$Run,
    [switch]$FreshDatabase,
    [ValidateRange(1, 20)][int]$Repeat = 3,
    [switch]$FailOnUnexpectedSkip,
    [Alias('AllowedSkipPattern')][string[]]$AllowedSkipIdentity = @(),
    [switch]$Race,
    [string]$AdminDsn = $env:ENGRAM_TEST_ADMIN_DSN,
    [string]$PostgresContainer,
    [string]$PostgresImage = 'pgvector/pgvector:pg17',
    [ValidateRange(1, 99)][int]$ConnectionBudget = 20,
    [ValidateSet('Auto', 'Full', 'Targeted')][string]$CoveragePolicy = 'Auto',
    [ValidateRange(1, 120)][int]$TimeoutMinutes = 30,
    [string]$ArtifactRoot = '.agent/reports/evidence/production-ready/release-gates-foundation',
    [string]$RunId,
    [ValidateSet('None', 'Up', 'Ready', 'Scan', 'Down')][string]$DevStandAction = 'None',
    [string]$ComposeProject = 'engram-critical-stand',
    [string]$ComposeFile = 'docker-compose.yml',
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
    -FreshDatabase [-Package ./...] [-Run '<regex>'] [-Repeat 3] [-Race] \
    [-FailOnUnexpectedSkip] [-AdminDsn <url>] \
    [-PostgresContainer <container>]

  pwsh ./scripts/production-gates/run-db-suite.ps1 \
    -DevStandAction Up|Ready|Scan|Down \
    [-ComposeProject engram-critical-stand] [-ComposeFile docker-compose.yml]

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
  -Repeat                  Fresh database repetitions (1..20); default is 3.
  -FailOnUnexpectedSkip    Fail on non-allowlisted test/package skips.
  -AllowedSkipIdentity     Exact case-sensitive package or package/test allowlist.
  -Race                    Run Go tests with the race detector inside the same
                           fresh-DB/JSON/coverage/cleanup evidence envelope.
  -CoveragePolicy          Auto, Full, or Targeted.
  -ConnectionBudget        App pool cap and required free server headroom.
  -AdminDsn                Admin URL or ENGRAM_TEST_ADMIN_DSN; always redacted.
  -PostgresContainer       Use psql through docker exec; else host psql.
  -DevStandAction          Execute the tracked isolated stand lifecycle. Up
                           generates three distinct process-local cryptographic
                           PostgreSQL/admin/bootstrap credentials,
                           validates exact compose service/image labels, and
                           proves /health + /api/ready without persisting them.
                           Scan runs Docker Scout against the exact running tags
                           and fails on any HIGH or CRITICAL vulnerability.
  -ComposeProject          Exact isolated compose project label.
  -ComposeFile             Compose file used by the tracked stand contract.

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
    foreach ($value in $SensitiveValues) { if ($value) { $protected = $protected.Replace($value, 'REDACTED_SENSITIVE_VALUE') } }
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

function Test-ReadyStatusPayload {
    param([AllowNull()][AllowEmptyString()][string]$Payload)
    if ([string]::IsNullOrWhiteSpace($Payload)) { return $false }
    try { $parsed = $Payload | ConvertFrom-Json -Depth 20 } catch { return $false }
    $statusProperty = $parsed.PSObject.Properties['status']
    return $null -ne $statusProperty -and [string]$statusProperty.Value -ceq 'ready'
}

function Test-LivenessStatusPayload {
    param([AllowNull()][AllowEmptyString()][string]$Payload)
    if ([string]::IsNullOrWhiteSpace($Payload)) { return $false }
    try { $parsed = $Payload | ConvertFrom-Json -Depth 20 } catch { return $false }
    $statusProperty = $parsed.PSObject.Properties['status']
    return $null -ne $statusProperty -and [string]$statusProperty.Value -cin @('starting', 'ready', 'error') -and [string]$statusProperty.Value -ceq ([string]$statusProperty.Value).ToLowerInvariant()
}

function Get-HttpJsonContractResult {
    param(
        [Parameter(Mandatory)][AllowEmptyString()][string]$CapturedOutput,
        [Parameter(Mandatory)][ValidateSet('liveness', 'readiness')][string]$ContractKind
    )

    $normalized = $CapturedOutput.TrimEnd("`r", "`n")
    $lastNewline = $normalized.LastIndexOf("`n")
    if ($lastNewline -lt 0) {
        return [pscustomobject]@{ Pass = $false; StatusCode = $null; Payload = $normalized; Error = 'curl output did not contain a trailing HTTP status line' }
    }
    $payload = $normalized.Substring(0, $lastNewline).TrimEnd("`r")
    $statusCode = $normalized.Substring($lastNewline + 1).Trim()
    if ($statusCode -cne '200') {
        return [pscustomobject]@{ Pass = $false; StatusCode = $statusCode; Payload = $payload; Error = "expected HTTP 200, got '$statusCode'" }
    }
    $semanticPass = if ($ContractKind -ceq 'liveness') { Test-LivenessStatusPayload $payload } else { Test-ReadyStatusPayload $payload }
    $required = if ($ContractKind -ceq 'liveness') { 'starting|ready|error' } else { 'ready' }
    return [pscustomobject]@{
        Pass = $semanticPass; StatusCode = $statusCode; Payload = $payload
        Error = if ($semanticPass) { $null } else { "HTTP 200 payload did not satisfy $ContractKind status contract '$required'" }
    }
}

function Get-DevStandReadyEndpoints {
    return @(
        [pscustomobject][ordered]@{ name = 'health'; url = 'http://localhost:37778/health'; path_kind = 'direct-server'; contract_kind = 'liveness' },
        [pscustomobject][ordered]@{ name = 'api-ready'; url = 'http://localhost:37778/api/ready'; path_kind = 'direct-server'; contract_kind = 'readiness' },
        [pscustomobject][ordered]@{ name = 'operator-api-health'; url = 'http://localhost:3001/api/health'; path_kind = 'operator-console-proxy'; contract_kind = 'liveness' },
        [pscustomobject][ordered]@{ name = 'operator-api-ready'; url = 'http://localhost:3001/api/ready'; path_kind = 'operator-console-proxy'; contract_kind = 'readiness' }
    )
}

function New-CryptographicSecret {
    $bytes = [System.Security.Cryptography.RandomNumberGenerator]::GetBytes(32)
    return [Convert]::ToHexString($bytes).ToLowerInvariant()
}

function Assert-DevStandCredentials {
    param([Parameter(Mandatory)][System.Collections.IDictionary]$Credentials)

    $required = @('postgres_password', 'admin_token', 'bootstrap_capability')
    $values = [System.Collections.Generic.List[string]]::new()
    $forbidden = @('engram', 'password', 'changeme', 'change-me', 'change-me-in-production', 'default', 'admin')
    foreach ($name in $required) {
        if (-not $Credentials.Contains($name)) { throw "dev-stand credential '$name' is missing" }
        $value = [string]$Credentials[$name]
        if ([string]::IsNullOrWhiteSpace($value)) { throw "dev-stand credential '$name' is blank" }
        if ($value.Length -lt 16) { throw "dev-stand credential '$name' is too short to be cryptographically generated" }
        if ($value.ToLowerInvariant() -in $forbidden) { throw "dev-stand credential '$name' uses a forbidden default" }
        $values.Add($value)
    }
    if (@($values | Select-Object -Unique).Count -ne $values.Count) { throw 'dev-stand PostgreSQL, admin, and bootstrap credentials must be distinct' }
}

function Test-RedactedContainerEnvironment {
    param(
        [Parameter(Mandatory)][AllowEmptyString()][string]$CapturedJson,
        [Parameter(Mandatory)][AllowEmptyCollection()][string[]]$RequiredNames
    )
    try { $entries = @(ConvertFrom-Json -InputObject $CapturedJson -Depth 20) } catch { return $false }
    foreach ($name in $RequiredNames) {
        if ($name -notmatch '^[A-Z][A-Z0-9_]*$') { return $false }
        if ($entries -cnotcontains "$name=REDACTED_SENSITIVE_VALUE") { return $false }
    }
    return $true
}

function Get-DevStandEnvironment {
    param(
        [Parameter(Mandatory)][string]$Project,
        [Parameter(Mandatory)][string]$PostgresPassword,
        [Parameter(Mandatory)][string]$AdminToken,
        [Parameter(Mandatory)][string]$BootstrapCapability
    )

    $credentials = [ordered]@{ postgres_password = $PostgresPassword; admin_token = $AdminToken; bootstrap_capability = $BootstrapCapability }
    Assert-DevStandCredentials $credentials
    $escapedPassword = [uri]::EscapeDataString($PostgresPassword)
    $databaseDsn = "postgres://engram:$escapedPassword@postgres:5432/engram?sslmode=disable"

    return @{
        COMPOSE_PROJECT_NAME = $Project
        POSTGRES_PORT = '55433'
        WORKER_PORT = '37778'
        OPERATOR_CONSOLE_PORT = '3001'
        POSTGRES_PASSWORD = $PostgresPassword
        DATABASE_DSN = $databaseDsn
        STAND_API_URL = 'http://localhost:37778'
        STAND_OPERATOR_URL = 'http://localhost:3001'
        NUXT_OPERATOR_API_TARGET = 'http://server:37777'
        ENGRAM_AUTH_ADMIN_TOKEN = $AdminToken
        ENGRAM_AUTH_BOOTSTRAP_CAPABILITY = $BootstrapCapability
        ENGRAM_AUTH_DISABLED = 'false'
    }
}

function Get-NativeCommandPath {
    param([Parameter(Mandatory)][string[]]$Names)
    foreach ($name in $Names) {
        $command = Get-Command $name -ErrorAction SilentlyContinue
        if ($null -ne $command -and -not [string]::IsNullOrWhiteSpace($command.Source)) { return $command.Source }
    }
    throw "required native command was not found: $($Names -join ', ')"
}

function Get-DevStandImageTargets {
    return [ordered]@{
        postgres = 'pgvector/pgvector:pg17'
        server = 'ghcr.io/thebtf/engram:main'
        'operator-console' = 'ghcr.io/thebtf/engram-operator-console:main'
    }
}

function Get-MapStringValue {
    param(
        [AllowNull()]$Map,
        [Parameter(Mandatory)][string]$Key
    )

    if ($null -eq $Map) { return $null }
    if ($Map -is [System.Collections.IDictionary]) {
        if (-not $Map.Contains($Key)) { return $null }
        return [string]$Map[$Key]
    }
    $property = $Map.PSObject.Properties[$Key]
    if ($null -eq $property) { return $null }
    return [string]$property.Value
}

function Test-StrictBoolean {
    param(
        [AllowNull()]$Value,
        [Parameter(Mandatory)][bool]$Expected
    )
    return ($Value -is [bool]) -and ($Value -ceq $Expected)
}

function Test-ExactDevStandInventory {
    param([Parameter(Mandatory)][hashtable]$ActualImages)
    $expectedImages = Get-DevStandImageTargets
    $errors = [System.Collections.Generic.List[string]]::new()
    foreach ($entry in $expectedImages.GetEnumerator()) {
        if (-not $ActualImages.ContainsKey($entry.Key)) { $errors.Add("compose service '$($entry.Key)' is missing from the project inventory"); continue }
        if (-not [string]::Equals([string]$ActualImages[$entry.Key], [string]$entry.Value, [System.StringComparison]::Ordinal)) {
            $errors.Add("compose service '$($entry.Key)' image mismatch: expected '$($entry.Value)', got '$($ActualImages[$entry.Key])'")
        }
    }
    foreach ($service in $ActualImages.Keys) {
        if (-not $expectedImages.Contains($service)) { $errors.Add("unexpected compose service/image target '$service'='$($ActualImages[$service])'") }
    }
    [pscustomobject]@{ Pass = $errors.Count -eq 0; Expected = $expectedImages; Errors = @($errors) }
}

function Invoke-DevStandResidualChecks {
    param(
        [Parameter(Mandatory)][string]$NamePrefix,
        [Parameter(Mandatory)][string]$DockerPath,
        [Parameter(Mandatory)][string]$Project,
        [Parameter(Mandatory)][string]$ActionDirectory,
        [Parameter(Mandatory)]$Connection,
        [Parameter(Mandatory)][AllowEmptyCollection()][System.Collections.Generic.List[string]]$Errors
    )
    $passed = $true
    foreach ($residual in @(
        @('containers', @('ps', '-aq', '--filter', "label=com.docker.compose.project=$Project")),
        @('volumes', @('volume', 'ls', '-q', '--filter', "label=com.docker.compose.project=$Project")),
        @('networks', @('network', 'ls', '-q', '--filter', "label=com.docker.compose.project=$Project"))
    )) {
        $check = Invoke-CapturedProcess "$NamePrefix-$($residual[0])" $DockerPath $residual[1] @{} (Join-Path $ActionDirectory "$NamePrefix-$($residual[0]).stdout.log") (Join-Path $ActionDirectory "$NamePrefix-$($residual[0]).stderr.log") $Connection @() 30
        if ($check.ExitCode -ne 0) {
            $passed = $false
            $Errors.Add("residual $($residual[0]) check failed with exit $($check.ExitCode)")
        }
        elseif (-not [string]::IsNullOrWhiteSpace($check.Stdout)) {
            $passed = $false
            $Errors.Add("residual $($residual[0]) remain for compose project '$Project': $($check.Stdout.Trim())")
        }
    }
    return $passed
}

function Invoke-DevStandContract {
    param(
        [Parameter(Mandatory)][ValidateSet('Up', 'Ready', 'Scan', 'Down')][string]$Action,
        [Parameter(Mandatory)][string]$Project,
        [Parameter(Mandatory)][string]$File,
        [Parameter(Mandatory)][string]$EvidenceRoot,
        [string]$RequestedRunId
    )

    if ($Project -notmatch '^[a-z0-9][a-z0-9_-]{2,62}$') { throw "unsafe compose project '$Project'" }
    if (-not (Test-Path -LiteralPath $File -PathType Leaf)) { throw "compose file does not exist: $File" }
    $actionToken = [guid]::NewGuid().ToString('N').Substring(0, 10)
    if ([string]::IsNullOrWhiteSpace($RequestedRunId)) { $RequestedRunId = [DateTimeOffset]::UtcNow.ToString('yyyyMMddTHHmmssZ') + '-' + $actionToken }
    if ($RequestedRunId -notmatch '^[A-Za-z0-9._-]+$') { throw '-RunId may contain only letters, digits, dot, underscore, and hyphen.' }

    $actionDirectory = Join-Path (Join-Path $EvidenceRoot 'dev-stand') ("$RequestedRunId-$($Action.ToLowerInvariant())")
    if (Test-Path -LiteralPath $actionDirectory) { throw "dev-stand artifact directory already exists: $actionDirectory" }
    New-Item -ItemType Directory -Path $actionDirectory -Force | Out-Null
    $script:CommandRecords.Clear()
    $startedAt = [DateTimeOffset]::UtcNow
    $errors = [System.Collections.Generic.List[string]]::new()
    $actualImages = @{}
    $actualImageIds = @{}
    $tagImageIds = @{}
    $prelaunchImageIds = @{}
    $imageIdentityPass = $true
    $prelaunchToRunningImageIdentity = $false
    $sourceRepository = $null
    $sourceCommit = $null
    $sourceTrackedClean = $false
    $composeBuildCompleted = $false
    $postgresPullCompleted = $false
    $launchNoBuild = $false
    $upProvenanceSummary = $null
    $vulnerabilityScans = [System.Collections.Generic.List[object]]::new()
    $credentialsGenerated = $false
    $postgresPassword = $null
    $adminToken = $null
    $bootstrapCapability = $null
    $credentialValuesPersisted = $false
    $credentialPolicyPass = $false
    $credentialRuntimeInjectionPass = $false
    $endpointResults = [System.Collections.Generic.List[object]]::new()
    $composeOverridePath = $null
    $standEnvironment = @{}
    $automaticFailureCleanup = $false
    $residualChecksPerformed = $false
    $residualResourcesZero = $null
    $connection = [pscustomobject]@{ Original = ''; Password = ''; Uri = $null; User = ''; Host = ''; Port = 0; Database = ''; SslMode = $null }
    $dockerPath = Get-NativeCommandPath @('docker.exe', 'docker')
    $gitPath = $null
    $curlPath = $null
    if ($Action -in @('Up', 'Ready')) { $curlPath = Get-NativeCommandPath @('curl.exe', 'curl') }
    $standDsn = $null
    $sensitiveValues = [System.Collections.Generic.List[string]]::new()

    try {
        if ($Action -in @('Up', 'Ready', 'Scan')) {
            $gitPath = Get-NativeCommandPath @('git.exe', 'git')
            $composeFilePath = [System.IO.Path]::GetFullPath($File)
            $composeDirectory = Split-Path -Parent $composeFilePath
            $sourceRoot = Invoke-CapturedProcess 'dev-stand-source-root' $gitPath @('-C', $composeDirectory, 'rev-parse', '--show-toplevel') @{} (Join-Path $actionDirectory 'source-root.stdout.log') (Join-Path $actionDirectory 'source-root.stderr.log') $connection @() 30
            if ($sourceRoot.ExitCode -ne 0 -or [string]::IsNullOrWhiteSpace($sourceRoot.Stdout)) { throw 'challenged source repository root could not be resolved' }
            $sourceRepository = $sourceRoot.Stdout.Trim()
            $sourceHead = Invoke-CapturedProcess 'dev-stand-source-commit' $gitPath @('-C', $sourceRepository, 'rev-parse', '--verify', 'HEAD^{commit}') @{} (Join-Path $actionDirectory 'source-commit.stdout.log') (Join-Path $actionDirectory 'source-commit.stderr.log') $connection @() 30
            if ($sourceHead.ExitCode -ne 0) { throw "challenged source commit could not be resolved (exit=$($sourceHead.ExitCode))" }
            $sourceCommit = $sourceHead.Stdout.Trim().ToLowerInvariant()
            if ($sourceCommit -notmatch '^[a-f0-9]{40}$') { throw "challenged source commit is malformed: '$sourceCommit'" }
            $sourceStatus = Invoke-CapturedProcess 'dev-stand-source-tracked-status' $gitPath @('-C', $sourceRepository, 'status', '--porcelain=v1', '--untracked-files=all') @{} (Join-Path $actionDirectory 'source-tracked-status.stdout.log') (Join-Path $actionDirectory 'source-tracked-status.stderr.log') $connection @() 30
            if ($sourceStatus.ExitCode -ne 0) { throw "challenged source tracked-status check failed (exit=$($sourceStatus.ExitCode))" }
            $sourceTrackedClean = [string]::IsNullOrWhiteSpace($sourceStatus.Stdout)
            if (-not $sourceTrackedClean) { throw 'challenged source has tracked modifications; image provenance is not commit-exact' }

            if ($Action -in @('Ready', 'Scan')) {
                $upSummaryPath = Join-Path (Join-Path (Join-Path $EvidenceRoot 'dev-stand') "$RequestedRunId-up") 'summary.json'
                if (-not (Test-Path -LiteralPath $upSummaryPath -PathType Leaf)) { throw "Up provenance summary is missing: $upSummaryPath" }
                try { $upProvenanceSummary = Get-Content -LiteralPath $upSummaryPath -Raw | ConvertFrom-Json -Depth 100 }
                catch { throw "Up provenance summary is invalid: $($_.Exception.Message)" }
                if ([string]$upProvenanceSummary.action -cne 'Up' -or [string]$upProvenanceSummary.verdict -cne 'PASS') { throw 'Ready/Scan requires a passing Up provenance summary' }
                if ([string]$upProvenanceSummary.source_commit -cne $sourceCommit -or -not (Test-StrictBoolean $upProvenanceSummary.source_tracked_clean $true)) { throw 'Ready/Scan source identity differs from the passing Up build source' }
                $composeBuildCompleted = Test-StrictBoolean $upProvenanceSummary.compose_build_completed $true
                $postgresPullCompleted = Test-StrictBoolean $upProvenanceSummary.postgres_pull_completed $true
                $launchNoBuild = Test-StrictBoolean $upProvenanceSummary.launch_no_build $true
                if (-not $composeBuildCompleted -or -not $postgresPullCompleted -or -not $launchNoBuild -or -not (Test-StrictBoolean $upProvenanceSummary.prelaunch_to_running_image_identity $true)) { throw 'passing Up summary lacks complete prelaunch build/image provenance' }
                foreach ($target in (Get-DevStandImageTargets).GetEnumerator()) {
                    $prelaunchId = Get-MapStringValue -Map $upProvenanceSummary.prelaunch_image_ids -Key $target.Key
                    if ($prelaunchId -notmatch '^sha256:[a-f0-9]{64}$') { throw "Up prelaunch image ID is missing or malformed for '$($target.Key)'" }
                    $prelaunchImageIds[$target.Key] = $prelaunchId
                }
            }
        }

        if ($Action -eq 'Up') {
            $postgresPassword = New-CryptographicSecret
            $adminToken = New-CryptographicSecret
            $bootstrapCapability = New-CryptographicSecret
            $credentials = [ordered]@{ postgres_password = $postgresPassword; admin_token = $adminToken; bootstrap_capability = $bootstrapCapability }
            Assert-DevStandCredentials $credentials
            $credentialPolicyPass = $true
            $credentialsGenerated = $true
            $standEnvironment = Get-DevStandEnvironment -Project $Project -PostgresPassword $postgresPassword -AdminToken $adminToken -BootstrapCapability $bootstrapCapability
            $standDsn = [string]$standEnvironment.DATABASE_DSN
            foreach ($secret in @($postgresPassword, $adminToken, $bootstrapCapability, $standDsn)) { $sensitiveValues.Add($secret) }

            $composeOverridePath = Join-Path $actionDirectory 'ephemeral-credential-injection.compose.yaml'
            Write-Utf8NoBom $composeOverridePath @'
services:
  server:
    environment:
      ENGRAM_AUTH_BOOTSTRAP_CAPABILITY: "${ENGRAM_AUTH_BOOTSTRAP_CAPABILITY:?required by production dev-stand}"
'@
            $composeArgs = @('compose', '-p', $Project, '-f', $File, '-f', $composeOverridePath)
            $build = Invoke-CapturedProcess 'dev-stand-compose-build' $dockerPath (@($composeArgs) + @('build', '--pull', 'server', 'operator-console')) $standEnvironment (Join-Path $actionDirectory 'compose-build.stdout.log') (Join-Path $actionDirectory 'compose-build.stderr.log') $connection @($sensitiveValues) 900
            if ($build.ExitCode -ne 0) { throw "compose source build failed with exit $($build.ExitCode)" }
            $composeBuildCompleted = $true
            $pull = Invoke-CapturedProcess 'dev-stand-postgres-pull' $dockerPath (@($composeArgs) + @('pull', 'postgres')) $standEnvironment (Join-Path $actionDirectory 'compose-pull-postgres.stdout.log') (Join-Path $actionDirectory 'compose-pull-postgres.stderr.log') $connection @($sensitiveValues) 600
            if ($pull.ExitCode -ne 0) { throw "compose PostgreSQL pull failed with exit $($pull.ExitCode)" }
            $postgresPullCompleted = $true
            $postBuildHead = Invoke-CapturedProcess 'dev-stand-source-commit-post-build' $gitPath @('-C', $sourceRepository, 'rev-parse', '--verify', 'HEAD^{commit}') @{} (Join-Path $actionDirectory 'source-commit-post-build.stdout.log') (Join-Path $actionDirectory 'source-commit-post-build.stderr.log') $connection @() 30
            if ($postBuildHead.ExitCode -ne 0 -or $postBuildHead.Stdout.Trim().ToLowerInvariant() -cne $sourceCommit) { throw 'source HEAD changed during compose build/pull' }
            $postBuildStatus = Invoke-CapturedProcess 'dev-stand-source-tracked-status-post-build' $gitPath @('-C', $sourceRepository, 'status', '--porcelain=v1', '--untracked-files=all') @{} (Join-Path $actionDirectory 'source-tracked-status-post-build.stdout.log') (Join-Path $actionDirectory 'source-tracked-status-post-build.stderr.log') $connection @() 30
            if ($postBuildStatus.ExitCode -ne 0 -or -not [string]::IsNullOrWhiteSpace($postBuildStatus.Stdout)) { throw 'source tree changed during compose build/pull' }
            foreach ($target in (Get-DevStandImageTargets).GetEnumerator()) {
                $prelaunchInspect = Invoke-CapturedProcess "dev-stand-prelaunch-image-inspect-$($target.Key)" $dockerPath @('image', 'inspect', $target.Value, '--format', '{{.Id}}') @{} (Join-Path $actionDirectory "prelaunch-image-inspect-$($target.Key).stdout.log") (Join-Path $actionDirectory "prelaunch-image-inspect-$($target.Key).stderr.log") $connection @() 30
                if ($prelaunchInspect.ExitCode -ne 0) { throw "prelaunch image '$($target.Value)' is unavailable for service '$($target.Key)'" }
                $prelaunchId = $prelaunchInspect.Stdout.Trim()
                if ($prelaunchId -notmatch '^sha256:[a-f0-9]{64}$') { throw "prelaunch image ID is malformed for service '$($target.Key)': '$prelaunchId'" }
                $prelaunchImageIds[$target.Key] = $prelaunchId
            }
            $up = Invoke-CapturedProcess 'dev-stand-up' $dockerPath (@($composeArgs) + @('up', '-d', '--no-build', '--pull', 'never', '--wait')) $standEnvironment (Join-Path $actionDirectory 'compose-up.stdout.log') (Join-Path $actionDirectory 'compose-up.stderr.log') $connection @($sensitiveValues) 600
            if ($up.ExitCode -ne 0) { throw "compose up failed with exit $($up.ExitCode)" }
            $launchNoBuild = $true

            $postgresContainer = Invoke-CapturedProcess 'dev-stand-postgres-container-id' $dockerPath (@($composeArgs) + @('ps', '-q', 'postgres')) $standEnvironment (Join-Path $actionDirectory 'postgres-container-id.stdout.log') (Join-Path $actionDirectory 'postgres-container-id.stderr.log') $connection @($sensitiveValues) 30
            if ($postgresContainer.ExitCode -ne 0 -or [string]::IsNullOrWhiteSpace($postgresContainer.Stdout)) { throw 'running postgres container ID could not be resolved for credential proof' }
            $postgresCredential = Invoke-CapturedProcess 'dev-stand-postgres-credential-injection' $dockerPath @('inspect', $postgresContainer.Stdout.Trim(), '--format', '{{json .Config.Env}}') @{} (Join-Path $actionDirectory 'postgres-credential-injection.stdout.log') (Join-Path $actionDirectory 'postgres-credential-injection.stderr.log') $connection @($sensitiveValues) 30
            if ($postgresCredential.ExitCode -ne 0 -or -not (Test-RedactedContainerEnvironment $postgresCredential.Stdout @('POSTGRES_PASSWORD'))) { throw 'generated PostgreSQL password did not reach the running postgres service exactly' }

            $serverContainer = Invoke-CapturedProcess 'dev-stand-server-container-id' $dockerPath (@($composeArgs) + @('ps', '-q', 'server')) $standEnvironment (Join-Path $actionDirectory 'server-container-id.stdout.log') (Join-Path $actionDirectory 'server-container-id.stderr.log') $connection @($sensitiveValues) 30
            if ($serverContainer.ExitCode -ne 0 -or [string]::IsNullOrWhiteSpace($serverContainer.Stdout)) { throw 'running server container ID could not be resolved for credential proof' }
            $serverCredential = Invoke-CapturedProcess 'dev-stand-server-credential-injection' $dockerPath @('inspect', $serverContainer.Stdout.Trim(), '--format', '{{json .Config.Env}}') @{} (Join-Path $actionDirectory 'server-credential-injection.stdout.log') (Join-Path $actionDirectory 'server-credential-injection.stderr.log') $connection @($sensitiveValues) 30
            if ($serverCredential.ExitCode -ne 0 -or -not (Test-RedactedContainerEnvironment $serverCredential.Stdout @('ENGRAM_AUTH_ADMIN_TOKEN', 'ENGRAM_AUTH_BOOTSTRAP_CAPABILITY'))) { throw 'generated admin token/bootstrap capability did not reach the running server service exactly' }
            $credentialRuntimeInjectionPass = $true
        }

        if ($Action -in @('Up', 'Ready')) {
            $pgReady = Invoke-CapturedProcess 'dev-stand-postgres-ready' $dockerPath @('compose', '-p', $Project, '-f', $File, 'exec', '-T', 'postgres', 'pg_isready', '-U', 'engram', '-d', 'engram') @{} (Join-Path $actionDirectory 'postgres-ready.stdout.log') (Join-Path $actionDirectory 'postgres-ready.stderr.log') $connection @($sensitiveValues) 30
            if ($pgReady.ExitCode -ne 0) { throw "PostgreSQL readiness failed with exit $($pgReady.ExitCode)" }
            foreach ($endpoint in @(Get-DevStandReadyEndpoints)) {
                $http = Invoke-CapturedProcess "dev-stand-$($endpoint.name)" $curlPath @('-sS', '--max-time', '15', '--write-out', '\n%{http_code}', $endpoint.url) @{} (Join-Path $actionDirectory "$($endpoint.name).stdout.log") (Join-Path $actionDirectory "$($endpoint.name).stderr.log") $connection @($sensitiveValues) 30
                if ($http.ExitCode -ne 0) { throw "$($endpoint.url) failed with exit $($http.ExitCode)" }
                $httpContract = Get-HttpJsonContractResult -CapturedOutput $http.Stdout -ContractKind $endpoint.contract_kind
                $endpointResults.Add([pscustomobject][ordered]@{
                    name = $endpoint.name; url = $endpoint.url; path_kind = $endpoint.path_kind; contract_kind = $endpoint.contract_kind
                    http_status = $httpContract.StatusCode; semantic_contract_pass = $httpContract.Pass
                })
                if (-not $httpContract.Pass) { throw "$($endpoint.url) failed contract: $($httpContract.Error)" }
            }
        }

        if ($Action -in @('Up', 'Ready', 'Scan')) {
            $inventory = Invoke-CapturedProcess 'dev-stand-image-inventory' $dockerPath @('ps', '--filter', "label=com.docker.compose.project=$Project", '--format', '{{.ID}}|{{.Label "com.docker.compose.service"}}') @{} (Join-Path $actionDirectory 'image-inventory.stdout.log') (Join-Path $actionDirectory 'image-inventory.stderr.log') $connection @($sensitiveValues) 30
            if ($inventory.ExitCode -ne 0) { throw "compose image inventory failed with exit $($inventory.ExitCode)" }
            foreach ($line in ($inventory.Stdout -split "`r?`n")) {
                if ([string]::IsNullOrWhiteSpace($line)) { continue }
                $parts = $line.Trim() -split '\|', 2
                if ($parts.Count -ne 2 -or [string]::IsNullOrWhiteSpace($parts[0]) -or [string]::IsNullOrWhiteSpace($parts[1])) { throw "malformed compose inventory line '$line'" }
                $containerId = $parts[0]; $service = $parts[1]
                if ($service -notmatch '^[a-z0-9][a-z0-9_-]{0,62}$') { throw "unsafe compose service label '$service'" }
                if ($actualImages.ContainsKey($service)) { throw "duplicate compose service '$service' in image inventory" }
                $inspect = Invoke-CapturedProcess "dev-stand-image-inspect-$service" $dockerPath @('inspect', $containerId, '--format', '{{.Config.Image}}|{{.Image}}') @{} (Join-Path $actionDirectory "image-inspect-$service.stdout.log") (Join-Path $actionDirectory "image-inspect-$service.stderr.log") $connection @($sensitiveValues) 30
                if ($inspect.ExitCode -ne 0) { throw "container image inspect failed for service '$service' with exit $($inspect.ExitCode)" }
                $imageParts = $inspect.Stdout.Trim() -split '\|', 2
                if ($imageParts.Count -ne 2 -or [string]::IsNullOrWhiteSpace($imageParts[0]) -or $imageParts[1] -notmatch '^sha256:[a-f0-9]{64}$') { throw "malformed image identity for service '$service': '$($inspect.Stdout.Trim())'" }
                $actualImages[$service] = $imageParts[0]
                $actualImageIds[$service] = $imageParts[1]
                $tagInspect = Invoke-CapturedProcess "dev-stand-image-tag-inspect-$service" $dockerPath @('image', 'inspect', $imageParts[0], '--format', '{{.Id}}') @{} (Join-Path $actionDirectory "image-tag-inspect-$service.stdout.log") (Join-Path $actionDirectory "image-tag-inspect-$service.stderr.log") $connection @($sensitiveValues) 30
                if ($tagInspect.ExitCode -ne 0) {
                    $imageIdentityPass = $false
                    $errors.Add("exact image tag '$($imageParts[0])' is unavailable for service '$service'")
                }
                else {
                    $tagImageId = $tagInspect.Stdout.Trim()
                    $tagImageIds[$service] = $tagImageId
                    if ($tagImageId -notmatch '^sha256:[a-f0-9]{64}$') {
                        $imageIdentityPass = $false
                        $errors.Add("malformed tag image identity for service '$service': '$tagImageId'")
                    }
                    elseif (-not [string]::Equals($tagImageId, $imageParts[1], [System.StringComparison]::Ordinal)) {
                        $imageIdentityPass = $false
                        $errors.Add("exact tag '$($imageParts[0])' no longer resolves to the running image for service '$service'")
                    }
                }
            }
            $inventoryAssertion = Test-ExactDevStandInventory $actualImages
            foreach ($inventoryError in $inventoryAssertion.Errors) { $errors.Add($inventoryError) }
            $prelaunchToRunningImageIdentity = $inventoryAssertion.Pass -and $prelaunchImageIds.Count -eq 3
            foreach ($target in (Get-DevStandImageTargets).GetEnumerator()) {
                $prelaunchId = Get-MapStringValue -Map $prelaunchImageIds -Key $target.Key
                $runningId = Get-MapStringValue -Map $actualImageIds -Key $target.Key
                if ($prelaunchId -notmatch '^sha256:[a-f0-9]{64}$' -or $runningId -notmatch '^sha256:[a-f0-9]{64}$' -or -not [string]::Equals($prelaunchId, $runningId, [System.StringComparison]::Ordinal)) {
                    $prelaunchToRunningImageIdentity = $false
                    $errors.Add("prelaunch image ID does not equal running image ID for service '$($target.Key)'")
                }
            }

            if ($Action -eq 'Scan' -and $inventoryAssertion.Pass -and $imageIdentityPass -and $prelaunchToRunningImageIdentity) {
                foreach ($entry in @($actualImages.GetEnumerator() | Sort-Object Key)) {
                    $sarifPath = [System.IO.Path]::GetFullPath((Join-Path $actionDirectory ("docker-scout-$($entry.Key).sarif.json")))
                    $imageId = $actualImageIds[$entry.Key]
                    $scanReference = "local://$imageId"
                    $scan = Invoke-CapturedProcess "dev-stand-vulnerability-scan-$($entry.Key)" $dockerPath @('scout', 'cves', '--exit-code', '--only-severity', 'critical,high', '--format', 'sarif', '--output', $sarifPath, $scanReference) @{} (Join-Path $actionDirectory "docker-scout-$($entry.Key).stdout.log") (Join-Path $actionDirectory "docker-scout-$($entry.Key).stderr.log") $connection @() 600
                    $vulnerabilityCount = $null
                    $scanParseError = $null
                    if (Test-Path -LiteralPath $sarifPath -PathType Leaf) {
                        try {
                            $sarif = Get-Content -LiteralPath $sarifPath -Raw | ConvertFrom-Json -Depth 100
                            $vulnerabilityCount = @($sarif.runs | ForEach-Object { $_.results } | Where-Object { $null -ne $_ }).Count
                        }
                        catch { $scanParseError = "Docker Scout SARIF parse failed for '$($entry.Value)': $($_.Exception.Message)"; $errors.Add($scanParseError) }
                    }
                    else { $scanParseError = "Docker Scout did not produce SARIF for '$($entry.Value)'"; $errors.Add($scanParseError) }

                    $vulnerabilityScans.Add([pscustomobject][ordered]@{
                        service = $entry.Key; image = $entry.Value; image_id = $imageId; scanned_reference = $scanReference; scanner = 'docker scout cves'
                        severities = @('critical', 'high'); exit_code = $scan.ExitCode
                        vulnerability_count = $vulnerabilityCount; sarif = $sarifPath; parse_error = $scanParseError
                    })
                    if ($scan.ExitCode -eq 2) { $errors.Add("HIGH/CRITICAL vulnerabilities detected in exact image '$($entry.Value)' (count=$vulnerabilityCount)") }
                    elseif ($scan.ExitCode -ne 0) { $errors.Add("Docker Scout failed for exact image '$($entry.Value)' with exit $($scan.ExitCode)") }
                    elseif ($null -eq $vulnerabilityCount) { $errors.Add("Docker Scout result count is unavailable for exact image '$($entry.Value)'") }
                    elseif ($vulnerabilityCount -ne 0) { $errors.Add("Docker Scout returned exit 0 with $vulnerabilityCount HIGH/CRITICAL results for exact image '$($entry.Value)'") }
                }
            }
        }

        if ($Action -eq 'Down') {
            $down = Invoke-CapturedProcess 'dev-stand-down' $dockerPath @('compose', '-p', $Project, '-f', $File, 'down', '-v', '--remove-orphans') @{} (Join-Path $actionDirectory 'compose-down.stdout.log') (Join-Path $actionDirectory 'compose-down.stderr.log') $connection @() 180
            if ($down.ExitCode -ne 0) { $errors.Add("compose down failed with exit $($down.ExitCode)") }
            $residualChecksPerformed = $true
            $residualResourcesZero = Invoke-DevStandResidualChecks -NamePrefix 'dev-stand-residual' -DockerPath $dockerPath -Project $Project -ActionDirectory $actionDirectory -Connection $connection -Errors $errors
        }
    }
    catch { $errors.Add($_.Exception.Message) }
    finally {
        if ($Action -eq 'Up' -and $errors.Count -gt 0) {
            $automaticFailureCleanup = $true
            $failureComposeArgs = @('compose', '-p', $Project, '-f', $File)
            if ($composeOverridePath -and (Test-Path -LiteralPath $composeOverridePath -PathType Leaf)) { $failureComposeArgs += @('-f', $composeOverridePath) }
            $failureDown = Invoke-CapturedProcess 'dev-stand-failure-cleanup' $dockerPath (@($failureComposeArgs) + @('down', '-v', '--remove-orphans')) $standEnvironment (Join-Path $actionDirectory 'failure-cleanup.stdout.log') (Join-Path $actionDirectory 'failure-cleanup.stderr.log') $connection @($sensitiveValues) 180
            if ($failureDown.ExitCode -ne 0) { $errors.Add("automatic failure cleanup failed with exit $($failureDown.ExitCode)") }
            $residualChecksPerformed = $true
            $residualResourcesZero = Invoke-DevStandResidualChecks -NamePrefix 'dev-stand-failure-residual' -DockerPath $dockerPath -Project $Project -ActionDirectory $actionDirectory -Connection $connection -Errors $errors
        }
    }

    $finishedAt = [DateTimeOffset]::UtcNow
    $commandsPath = Join-Path $actionDirectory 'commands.json'
    Write-Utf8NoBom $commandsPath ((ConvertTo-Json -InputObject @($script:CommandRecords.ToArray()) -Depth 10) + "`n")
    if ($composeOverridePath -and (Test-Path -LiteralPath $composeOverridePath)) {
        Remove-Item -LiteralPath $composeOverridePath -Force -ErrorAction SilentlyContinue
    }
    if ($credentialsGenerated) {
        foreach ($evidenceFile in Get-ChildItem -LiteralPath $actionDirectory -Recurse -File) {
            try {
                $evidenceText = [System.IO.File]::ReadAllText($evidenceFile.FullName)
                foreach ($credential in @($postgresPassword, $adminToken, $bootstrapCapability)) {
                    if (-not [string]::IsNullOrWhiteSpace($credential) -and $evidenceText.Contains($credential)) {
                        $credentialValuesPersisted = $true; $errors.Add("ephemeral dev-stand credential persisted in evidence file '$($evidenceFile.FullName)'")
                        break
                    }
                }
            }
            catch { $errors.Add("could not secret-scan evidence file '$($evidenceFile.FullName)': $($_.Exception.Message)") }
        }
    }
    $summary = [pscustomobject]@{
        schema_version = 1; gate = 'dev-stand-contract'; action = $Action; run_id = $RequestedRunId
        started_at = $startedAt.ToString('O'); finished_at = $finishedAt.ToString('O'); duration_seconds = [math]::Round(($finishedAt - $startedAt).TotalSeconds, 3)
        verdict = if ($errors.Count -eq 0) { 'PASS' } else { 'FAIL' }
        compose_project = $Project; compose_file = [System.IO.Path]::GetFullPath($File)
        ephemeral_postgres_password_generated = $credentialsGenerated
        ephemeral_admin_token_generated = $credentialsGenerated
        ephemeral_bootstrap_capability_generated = $credentialsGenerated
        ephemeral_credentials_distinct_and_nondefault = $credentialPolicyPass
        ephemeral_credentials_runtime_injected = $credentialRuntimeInjectionPass
        ephemeral_postgres_password_persisted = $credentialValuesPersisted
        ephemeral_admin_token_persisted = $credentialValuesPersisted
        ephemeral_bootstrap_capability_persisted = $credentialValuesPersisted
        source_repository = $sourceRepository
        source_commit = $sourceCommit
        source_tracked_clean = $sourceTrackedClean
        compose_build_completed = $composeBuildCompleted
        postgres_pull_completed = $postgresPullCompleted
        launch_no_build = $launchNoBuild
        prelaunch_to_running_image_identity = $prelaunchToRunningImageIdentity
        exact_image_targets = Get-DevStandImageTargets
        prelaunch_image_ids = $prelaunchImageIds
        actual_images = $actualImages; actual_image_ids = $actualImageIds; tag_image_ids = $tagImageIds
        liveness_endpoints = @($endpointResults | Where-Object contract_kind -ceq 'liveness')
        semantic_ready_endpoints = @($endpointResults | Where-Object contract_kind -ceq 'readiness')
        vulnerability_scan = [ordered]@{ scanner = 'docker scout cves'; severity_gate = @('critical', 'high'); scans = @($vulnerabilityScans) }
        automatic_failure_cleanup = $automaticFailureCleanup; residual_checks_performed = $residualChecksPerformed; residual_resources_zero = $residualResourcesZero
        child_commands = $script:CommandRecords.Count; nonzero_child_commands = @($script:CommandRecords | Where-Object exit_code -ne 0).Count
        commands = [System.IO.Path]::GetFullPath($commandsPath); errors = @($errors); artifact_directory = [System.IO.Path]::GetFullPath($actionDirectory)
    }
    $summaryPath = Join-Path $actionDirectory 'summary.json'; Write-Utf8NoBom $summaryPath (($summary | ConvertTo-Json -Depth 12) + "`n")
    Write-Host ("dev-stand action={0} verdict={1} child_commands={2} nonzero_children={3}" -f $Action, $summary.verdict, $summary.child_commands, $summary.nonzero_child_commands)
    Write-Host "summary=$([System.IO.Path]::GetFullPath($summaryPath))"
    return $summary
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
        Assert-SelfTestCondition (Test-StrictBoolean $true $true) 'strict Boolean helper rejected true'
        Assert-SelfTestCondition (Test-StrictBoolean $false $false) 'strict Boolean helper rejected false'
        foreach ($coercedTrue in @('true', 1, '1')) { Assert-SelfTestCondition (-not (Test-StrictBoolean $coercedTrue $true)) "strict Boolean helper accepted wrong-type true '$coercedTrue'" }
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
        Assert-SelfTestCondition (Test-ReadyStatusPayload '{"status":"ready","version":"dev"}') 'semantic ready payload was rejected'
        foreach ($badPayload in @('{"status":"error"}', '{"status":"Ready"}', '{"version":"dev"}', 'not-json', '')) { Assert-SelfTestCondition (-not (Test-ReadyStatusPayload $badPayload)) "false-ready payload '$badPayload' was accepted" }
        foreach ($liveStatus in @('starting', 'ready', 'error')) { Assert-SelfTestCondition (Test-LivenessStatusPayload ("{`"status`":`"$liveStatus`"}")) "valid liveness status '$liveStatus' was rejected" }
        foreach ($badPayload in @('{"status":"Ready"}', '{"status":"degraded"}', '{"version":"dev"}', 'not-json', '')) { Assert-SelfTestCondition (-not (Test-LivenessStatusPayload $badPayload)) "invalid liveness payload '$badPayload' was accepted" }
        Assert-SelfTestCondition (Get-HttpJsonContractResult -CapturedOutput "{`"status`":`"starting`"}`n200" -ContractKind liveness).Pass 'HTTP 200 liveness payload was rejected'
        Assert-SelfTestCondition (Get-HttpJsonContractResult -CapturedOutput "{`"status`":`"ready`"}`n200" -ContractKind readiness).Pass 'HTTP 200 readiness payload was rejected'
        Assert-SelfTestCondition (-not (Get-HttpJsonContractResult -CapturedOutput "{`"status`":`"ready`"}`n204" -ContractKind readiness).Pass) 'non-200 readiness response was accepted'
        Assert-SelfTestCondition (-not (Get-HttpJsonContractResult -CapturedOutput "{`"status`":`"error`"}`n200" -ContractKind readiness).Pass) 'liveness-only error status was accepted as readiness'
        $readyEndpoints = @(Get-DevStandReadyEndpoints)
        Assert-SelfTestCondition ($readyEndpoints.Count -eq 4) 'dev stand does not require both direct and operator-proxied semantic endpoints'
        Assert-SelfTestCondition (@($readyEndpoints | Where-Object { $_.name -eq 'health' -and $_.contract_kind -eq 'liveness' }).Count -eq 1) 'direct /health is not classified as liveness'
        Assert-SelfTestCondition (@($readyEndpoints | Where-Object { $_.name -eq 'api-ready' -and $_.contract_kind -eq 'readiness' }).Count -eq 1) 'direct /api/ready is not classified as readiness'
        Assert-SelfTestCondition (@($readyEndpoints | Where-Object { $_.name -eq 'operator-api-health' -and $_.url -eq 'http://localhost:3001/api/health' -and $_.path_kind -eq 'operator-console-proxy' -and $_.contract_kind -eq 'liveness' }).Count -eq 1) 'operator-console proxied /api/health liveness proof is missing'
        Assert-SelfTestCondition (@($readyEndpoints | Where-Object { $_.name -eq 'operator-api-ready' -and $_.url -eq 'http://localhost:3001/api/ready' -and $_.path_kind -eq 'operator-console-proxy' -and $_.contract_kind -eq 'readiness' }).Count -eq 1) 'operator-console proxied /api/ready readiness proof is missing'
        $credentials = [ordered]@{ postgres_password = 'random-postgres-selftest'; admin_token = 'random-admin-selftest'; bootstrap_capability = 'random-bootstrap-selftest' }
        Assert-DevStandCredentials $credentials
        foreach ($invalidCredentials in @(
            [ordered]@{ postgres_password = ''; admin_token = 'valid-admin-secret-0001'; bootstrap_capability = 'valid-bootstrap-secret-0001' },
            [ordered]@{ postgres_password = 'engram'; admin_token = 'valid-admin-secret-0002'; bootstrap_capability = 'valid-bootstrap-secret-0002' },
            [ordered]@{ postgres_password = 'valid-postgres-secret-0003'; admin_token = 'valid-admin-secret-0003' },
            [ordered]@{ postgres_password = 'same-valid-secret-0004'; admin_token = 'same-valid-secret-0004'; bootstrap_capability = 'same-valid-secret-0004' }
        )) {
            $rejected = $false
            try { Assert-DevStandCredentials $invalidCredentials } catch { $rejected = $true }
            Assert-SelfTestCondition $rejected 'blank/default/missing/reused dev-stand credentials were accepted'
        }
        $standEnvironment = Get-DevStandEnvironment -Project 'engram-critical-stand' -PostgresPassword $credentials.postgres_password -AdminToken $credentials.admin_token -BootstrapCapability $credentials.bootstrap_capability
        Assert-SelfTestCondition ($standEnvironment.NUXT_OPERATOR_API_TARGET -ceq 'http://server:37777') 'dev stand uses the wrong Nuxt operator API target variable or value'
        Assert-SelfTestCondition (-not $standEnvironment.ContainsKey('NUXT_ENGRAM_API_TARGET')) 'stale NUXT_ENGRAM_API_TARGET was accepted into the dev stand environment'
        Assert-SelfTestCondition ($standEnvironment.POSTGRES_PASSWORD -ceq $credentials.postgres_password) 'generated PostgreSQL password did not reach the compose environment'
        Assert-SelfTestCondition ($standEnvironment.ENGRAM_AUTH_ADMIN_TOKEN -ceq $credentials.admin_token) 'generated admin token did not reach the compose environment'
        Assert-SelfTestCondition ($standEnvironment.ENGRAM_AUTH_BOOTSTRAP_CAPABILITY -ceq $credentials.bootstrap_capability) 'generated bootstrap capability did not reach the compose environment'
        Assert-SelfTestCondition ($standEnvironment.DATABASE_DSN -match '^postgres://engram:[^@]+@postgres:5432/engram\?sslmode=disable$' -and -not $standEnvironment.DATABASE_DSN.Contains(':engram@')) 'generated PostgreSQL password did not reach DATABASE_DSN'
        Assert-SelfTestCondition (Test-RedactedContainerEnvironment '["POSTGRES_PASSWORD=REDACTED_SENSITIVE_VALUE","OTHER=value"]' @('POSTGRES_PASSWORD')) 'redacted exact container environment proof was rejected'
        Assert-SelfTestCondition (-not (Test-RedactedContainerEnvironment '["POSTGRES_PASSWORD=wrong"]' @('POSTGRES_PASSWORD'))) 'wrong runtime credential value was accepted'
        Assert-SelfTestCondition (-not (Test-RedactedContainerEnvironment '["ENGRAM_AUTH_ADMIN_TOKEN=REDACTED_SENSITIVE_VALUE"]' @('ENGRAM_AUTH_ADMIN_TOKEN','ENGRAM_AUTH_BOOTSTRAP_CAPABILITY'))) 'missing runtime bootstrap capability was accepted'
        $validInventory = Test-ExactDevStandInventory @{ postgres = 'pgvector/pgvector:pg17'; server = 'ghcr.io/thebtf/engram:main'; 'operator-console' = 'ghcr.io/thebtf/engram-operator-console:main' }
        Assert-SelfTestCondition $validInventory.Pass 'exact compose service/image inventory was rejected'
        $invalidInventory = Test-ExactDevStandInventory @{ postgres = 'pgvector/pgvector:pg17'; server = 'engram:prc-candidate'; 'operator-console' = 'ghcr.io/thebtf/engram-operator-console:main' }
        Assert-SelfTestCondition (-not $invalidInventory.Pass) 'non-produced engram:prc-candidate image was accepted'
        Assert-SelfTestCondition ($Repeat -eq 3) 'default release repetition count is not 3'
        Write-Output 'SELFTEST PASS: run-db-suite.ps1 (earlier exit 7 remained fatal after later exit 0)'
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

if ($Help) { Show-Help; exit 0 }
if ($SelfTest) { Invoke-SelfTest; exit 0 }
if ($DevStandAction -ne 'None') {
    $devStandSummary = Invoke-DevStandContract -Action $DevStandAction -Project $ComposeProject -File $ComposeFile -EvidenceRoot $ArtifactRoot -RequestedRunId $RunId
    if ($devStandSummary.verdict -ne 'PASS') { exit 1 }
    exit 0
}
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
    $goTestExit = $null; $parserExit = $null; $coverageExit = $null; $cleanupExit = $null; $cleanupStatus = $null; $sessionsBefore = $null; $sessionsAfter = $null; $serverSessionsBefore = $null; $serverSessionsAfter = $null
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
        if ($Race) { $goArguments.Add('-race') }
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
        if ($AllowedSkipIdentity.Count -gt 0) { $parserArguments.Add('-AllowedSkipIdentity'); foreach ($identity in $AllowedSkipIdentity) { $parserArguments.Add($identity) } }
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
        # The run owns this prefix-guarded name before CREATE is attempted. A
        # create subprocess can commit and then fail capture/timeout, so cleanup
        # is mandatory even when creation was not confirmed.
        $cleanupArguments = @('-NoProfile', '-File', $cleanupScript, '-DatabaseName', $databaseName, '-SchemaName', $schemaName, '-ArtifactRoot', $repeatDirectory, '-RunId', "$RunId-repeat-$repeatIndex")
        if ($PostgresContainer) { $cleanupArguments += @('-PostgresContainer', $PostgresContainer) }
        $cleanup = Invoke-CapturedProcess "repeat-$repeatIndex-cleanup" $pwshPath $cleanupArguments @{ ENGRAM_TEST_ADMIN_DSN = $AdminDsn } (Join-Path $repeatDirectory 'cleanup-process.stdout.log') (Join-Path $repeatDirectory 'cleanup-process.stderr.log') $connection @($targetDsn, $AdminDsn) 180
        $cleanupExit = $cleanup.ExitCode
        if ($cleanupExit -ne 0) {
            $repeatFailed = $true; $repeatErrors.Add("cleanup failed with exit $cleanupExit")
        }
        if (-not (Test-Path -LiteralPath $cleanupSummaryPath -PathType Leaf)) {
            $repeatFailed = $true; $repeatErrors.Add('cleanup summary is missing after an attempted cleanup')
        }
        else {
            try {
                $cleanupSummary = Get-Content -Raw -LiteralPath $cleanupSummaryPath | ConvertFrom-Json
                $cleanupStatus = [string]$cleanupSummary.cleanup_status
                if ($cleanupSummary.verdict -ne 'PASS') { $repeatFailed = $true; $repeatErrors.Add("cleanup summary verdict is '$($cleanupSummary.verdict)'") }
                if ([int]$cleanupSummary.remaining_database_count -ne 0) { $repeatFailed = $true; $repeatErrors.Add("cleanup absence proof is non-zero: $($cleanupSummary.remaining_database_count)") }
                $expectedCleanupStatus = if ($cleanupSummary.database_existed_before -eq $true) { 'PASS' } elseif ($cleanupSummary.database_existed_before -eq $false) { 'NOT_APPLICABLE' } else { 'FAIL' }
                if ($cleanupStatus -cne $expectedCleanupStatus) { $repeatFailed = $true; $repeatErrors.Add("cleanup status is '$cleanupStatus', expected '$expectedCleanupStatus' for database_existed_before=$($cleanupSummary.database_existed_before)") }
                if ($cleanupSummary.absence_verified -ne $true) { $repeatFailed = $true; $repeatErrors.Add('cleanup did not record positive database absence verification') }
            }
            catch { $repeatFailed = $true; $repeatErrors.Add("cleanup summary is malformed: $($_.Exception.Message)") }
        }
        if ($repeatFailed) { $overallFailed = $true }
        $repeatResult = [pscustomobject]@{
            repeat = $repeatIndex; verdict = if ($repeatFailed) { 'FAIL' } else { 'PASS' }
            database = $databaseName; schema = $schemaName; database_schema_identity = "$databaseName.$schemaName"; database_dsn = 'REDACTED_DATABASE_DSN'
            database_create_confirmed = $databaseCreated; sequential_execution = [ordered]@{ package_parallelism = 1; test_parallelism = 1 }; race = [bool]$Race
            connection_budget = $ConnectionBudget; server_sessions_before = $serverSessionsBefore; server_sessions_after = $serverSessionsAfter; sessions_before = $sessionsBefore; sessions_after = $sessionsAfter
            go_test_exit = $goTestExit; json_parser_exit = $parserExit; coverage_policy = $effectiveCoverage; coverage_exit = $coverageExit; cleanup_exit = $cleanupExit; cleanup_status = $cleanupStatus
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
    fail_on_unexpected_skip = [bool]$FailOnUnexpectedSkip; allowed_skip_identities = @($AllowedSkipIdentity)
    coverage_policy = $effectiveCoverage; connection_budget = $ConnectionBudget; race = [bool]$Race
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
    packages = $packages; run_pattern = if ($Run) { $Run } else { $null }; coverage_policy = $effectiveCoverage; connection_budget = $ConnectionBudget; race = [bool]$Race
    database_dsn = 'REDACTED_DATABASE_DSN'; environment = [System.IO.Path]::GetFullPath($environmentPath); commands = [System.IO.Path]::GetFullPath($commandsPath)
    repeats = @($repeatResults); errors = @($runErrors); artifact_directory = [System.IO.Path]::GetFullPath($runDirectory)
}
Write-Utf8NoBom $summaryPath (($summary | ConvertTo-Json -Depth 12) + "`n")
Write-Output ("release-gates-foundation verdict={0} repeats={1}/{2} nonzero_children={3} coverage_policy={4}" -f $summary.verdict, $passedRepeats, $Repeat, $summary.counts.nonzero_child_commands, $effectiveCoverage)
Write-Output "summary=$([System.IO.Path]::GetFullPath($summaryPath))"
if ($summary.verdict -ne 'PASS') { exit 1 }
exit 0
