[CmdletBinding()]
param(
    [string]$Config = '.agent/dev-stand.config.yaml',
    [string]$ArtifactRoot = '.agent/reports/evidence/production-ready/release-gates-foundation/dev-stand-runner',
    [string]$RunId,
    [switch]$SelfTest,
    [switch]$Help
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$script:CommandRecords = [System.Collections.Generic.List[object]]::new()

function Show-Help {
    @'
run-dev-stand.ps1

Validates .agent/dev-stand.config.yaml and owns the complete isolated lifecycle:
Up -> Ready -> Docker Scout Scan -> Down. Down always runs after an Up attempt.
Every child exit/raw stream and nested action summary is retained. Success also
requires generated non-persisted credentials and zero residual compose resources.

Usage:
  pwsh ./scripts/production-gates/run-dev-stand.ps1 \
    -Config .agent/dev-stand.config.yaml
'@ | Write-Output
}

function Write-Utf8NoBom {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][AllowEmptyString()][string]$Content)
    $parent = Split-Path -Parent $Path
    if ($parent) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }
    [System.IO.File]::WriteAllText([System.IO.Path]::GetFullPath($Path), $Content, [System.Text.UTF8Encoding]::new($false))
}

function Get-Sha256 { param([Parameter(Mandatory)][string]$Path); return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash }

function Quote-CommandArgument {
    param([Parameter(Mandatory)][AllowEmptyString()][string]$Value)
    if ($Value -match '^[A-Za-z0-9_./:=+,-]+$') { return $Value }
    return '"' + $Value.Replace('"', '\"') + '"'
}

function Invoke-CapturedProcess {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$Executable,
        [string[]]$Arguments = @(),
        [Parameter(Mandatory)][string]$StdoutPath,
        [Parameter(Mandatory)][string]$StderrPath,
        [ValidateRange(1, 7200)][int]$TimeoutSeconds = 900
    )
    $startedAt = [DateTimeOffset]::UtcNow; $exitCode = 127; $timedOut = $false; $stdout = ''; $stderr = ''; $process = $null
    try {
        $info = [System.Diagnostics.ProcessStartInfo]::new(); $info.FileName = $Executable; $info.UseShellExecute = $false; $info.CreateNoWindow = $true
        $info.RedirectStandardOutput = $true; $info.RedirectStandardError = $true
        foreach ($argument in $Arguments) { [void]$info.ArgumentList.Add([string]$argument) }
        $process = [System.Diagnostics.Process]::new(); $process.StartInfo = $info
        if (-not $process.Start()) { throw "process '$Executable' did not start" }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync(); $stderrTask = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit($TimeoutSeconds * 1000)) { $timedOut = $true; try { $process.Kill($true) } catch {}; [void]$process.WaitForExit(30000) }
        else { $process.WaitForExit() }
        $stdout = $stdoutTask.GetAwaiter().GetResult(); $stderr = $stderrTask.GetAwaiter().GetResult(); $exitCode = if ($timedOut) { 124 } else { $process.ExitCode }
    }
    catch { $stderr = "PROCESS_START_OR_CAPTURE_ERROR: $($_.Exception.Message)`n"; $exitCode = 127 }
    finally { if ($null -ne $process) { $process.Dispose() } }
    Write-Utf8NoBom $StdoutPath $stdout; Write-Utf8NoBom $StderrPath $stderr
    $finishedAt = [DateTimeOffset]::UtcNow
    $commandParts = [System.Collections.Generic.List[string]]::new(); $commandParts.Add((Quote-CommandArgument $Executable)); foreach ($argument in $Arguments) { $commandParts.Add((Quote-CommandArgument ([string]$argument))) }
    $record = [pscustomobject][ordered]@{
        name = $Name; executable = $Executable; arguments = @($Arguments); command = $commandParts -join ' '
        started_at = $startedAt.ToString('O'); finished_at = $finishedAt.ToString('O'); duration_seconds = [math]::Round(($finishedAt - $startedAt).TotalSeconds, 3)
        exit_code = $exitCode; timed_out = $timedOut; stdout = [System.IO.Path]::GetFullPath($StdoutPath); stderr = [System.IO.Path]::GetFullPath($StderrPath)
    }
    $script:CommandRecords.Add($record)
    return [pscustomobject]@{ ExitCode = $exitCode; Stdout = $stdout; Stderr = $stderr; Record = $record }
}

function Assert-ContainsExactLine {
    param([Parameter(Mandatory)][string]$Text, [Parameter(Mandatory)][string]$Line, [Parameter(Mandatory)][string]$Name)
    $count = @(($Text -split "`r?`n") | Where-Object { $_ -ceq $Line }).Count
    if ($count -ne 1) { throw "dev-stand config $Name must appear exactly once; found $count" }
}

function Read-DevStandConfig {
    param([Parameter(Mandatory)][string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { throw "dev-stand config does not exist: $Path" }
    $text = Get-Content -LiteralPath $Path -Raw
    Assert-ContainsExactLine $text 'version: 1' 'version'
    Assert-ContainsExactLine $text 'shape: docker-compose' 'shape'
    $commands = [ordered]@{
        Up = 'pwsh -NoProfile -File scripts/production-gates/run-db-suite.ps1 -DevStandAction Up -ComposeProject engram-critical-stand -ComposeFile docker-compose.yml'
        Ready = 'pwsh -NoProfile -File scripts/production-gates/run-db-suite.ps1 -DevStandAction Ready -ComposeProject engram-critical-stand -ComposeFile docker-compose.yml'
        Scan = 'pwsh -NoProfile -File scripts/production-gates/run-db-suite.ps1 -DevStandAction Scan -ComposeProject engram-critical-stand -ComposeFile docker-compose.yml'
        Down = 'pwsh -NoProfile -File scripts/production-gates/run-db-suite.ps1 -DevStandAction Down -ComposeProject engram-critical-stand -ComposeFile docker-compose.yml'
    }
    foreach ($entry in $commands.GetEnumerator()) {
        $matches = [regex]::Matches($text, ('(?m)^\s+(?:command|readiness_check):\s*"' + [regex]::Escape($entry.Value) + '"\s*$'))
        if ($matches.Count -ne 1) { throw "dev-stand config must declare exact $($entry.Key) command once; found $($matches.Count)" }
    }
    $requiredExactLines = [ordered]@{
        'Up timeout' = '  timeout_seconds: 600'
        'Down timeout' = '  timeout_seconds: 180'
        'logs command' = '  command: "docker compose -p engram-critical-stand -f docker-compose.yml logs --no-color --tail=300"'
        'compose project' = '  COMPOSE_PROJECT_NAME: "engram-critical-stand"'
        'PostgreSQL port' = '  POSTGRES_PORT: "55433"'
        'worker port' = '  WORKER_PORT: "37778"'
        'operator port' = '  OPERATOR_CONSOLE_PORT: "3001"'
        'PostgreSQL password' = '  POSTGRES_PASSWORD: "engram"'
        'database DSN' = '  DATABASE_DSN: "postgres://engram:engram@postgres:5432/engram?sslmode=disable"'
        'stand API URL' = '  STAND_API_URL: "http://localhost:37778"'
        'stand operator URL' = '  STAND_OPERATOR_URL: "http://localhost:3001"'
        'operator-console API proxy target' = '  NUXT_OPERATOR_API_TARGET: "http://server:37777"'
        'auth-disabled policy' = '  ENGRAM_AUTH_DISABLED: "false"'
        'credential generation policy' = '  admin_token: "generated cryptographically inside the Up runner process"'
        'credential persistence policy' = '  persistence: "never written to raw logs, machine summaries, config, or caller environment"'
        'credential fallback policy' = '  auth_disabled_fallback: false'
        'image discovery' = '  discovery: "docker service inventory filtered by com.docker.compose.project=engram-critical-stand"'
        'image scanner' = '  scanner: "docker scout cves"'
        'image scan severities' = '  severities: ["critical", "high"]'
        'image scan finding policy' = '  fail_on_findings: true'
        'PostgreSQL image' = '    postgres: "pgvector/pgvector:pg17"'
        'server image' = '    server: "ghcr.io/thebtf/engram:main"'
        'operator-console image' = '    operator-console: "ghcr.io/thebtf/engram-operator-console:main"'
        'forbidden synthetic image' = '    - "engram:prc-candidate"'
        'database runner' = '  runner: "pwsh -NoProfile -File scripts/production-gates/run-db-suite.ps1"'
        'database lifecycle' = '  database_lifecycle: "fresh unique database per repetition; cleanup owns only engram_prc_rg_* databases"'
        'shared service policy' = '  shared_service_policy: "Never stop or remove an operator-owned PostgreSQL container; only terminate/drop the current run database."'
    }
    foreach ($requiredLine in $requiredExactLines.GetEnumerator()) {
        Assert-ContainsExactLine $text $requiredLine.Value $requiredLine.Key
    }
    if ($text -match '(?m)^\s+NUXT_ENGRAM_API_TARGET:') { throw 'dev-stand config must not use stale NUXT_ENGRAM_API_TARGET' }
    if ($text -match '(?m)^\s+ENGRAM_AUTH_ADMIN_TOKEN:') { throw 'dev-stand config must not persist an admin token' }
    foreach ($requiredSection in @('up:', 'down:', 'logs:', 'env:', 'credential_policy:', 'image_scan:', 'database_evidence:')) { Assert-ContainsExactLine $text $requiredSection "section $requiredSection" }
    return [pscustomobject]@{ Text = $text; Sha256 = Get-Sha256 $Path; Commands = $commands; Project = 'engram-critical-stand'; ComposeFile = 'docker-compose.yml' }
}

function Test-ExactImageMaps {
    param([Parameter(Mandatory)]$Summary)
    $expected = [ordered]@{ postgres = 'pgvector/pgvector:pg17'; server = 'ghcr.io/thebtf/engram:main'; 'operator-console' = 'ghcr.io/thebtf/engram-operator-console:main' }
    foreach ($entry in $expected.GetEnumerator()) {
        $imageProperty = $Summary.actual_images.PSObject.Properties[$entry.Key]
        $runningProperty = $Summary.actual_image_ids.PSObject.Properties[$entry.Key]
        $tagProperty = $Summary.tag_image_ids.PSObject.Properties[$entry.Key]
        if ($null -eq $imageProperty -or [string]$imageProperty.Value -cne $entry.Value -or $null -eq $runningProperty -or $null -eq $tagProperty) { return $false }
        $runningId = [string]$runningProperty.Value; $tagId = [string]$tagProperty.Value
        if ($runningId -notmatch '^sha256:[a-f0-9]{64}$' -or $runningId -cne $tagId) { return $false }
    }
    return @($Summary.actual_images.PSObject.Properties).Count -eq 3
}

function Read-ActionSummary {
    param(
        [Parameter(Mandatory)][ValidateSet('Up', 'Ready', 'Scan', 'Down')][string]$Action,
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][int]$ChildExit
    )
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { throw "$Action action summary is missing: $Path" }
    try { $summary = Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json -Depth 100 } catch { throw "$Action action summary is invalid: $($_.Exception.Message)" }
    if ($summary.action -cne $Action) { throw "$Action action summary reports '$($summary.action)'" }
    $expectedVerdict = if ($ChildExit -eq 0) { 'PASS' } else { 'FAIL' }
    if ($summary.verdict -cne $expectedVerdict) { throw "$Action exit/verdict mismatch: exit=$ChildExit verdict=$($summary.verdict)" }
    if ($Action -in @('Up', 'Ready', 'Scan') -and -not (Test-ExactImageMaps $summary)) { throw "$Action did not prove exact tag-to-running-image identity" }
    if ($Action -eq 'Up') {
        if (-not $summary.ephemeral_admin_token_generated -or $summary.ephemeral_admin_token_persisted) { throw 'Up did not prove generated, non-persisted admin credentials' }
        $commands = Get-Content -LiteralPath $summary.commands -Raw | ConvertFrom-Json -Depth 100
        foreach ($name in @('dev-stand-postgres-ready', 'dev-stand-health', 'dev-stand-api-ready', 'dev-stand-operator-api-health', 'dev-stand-operator-api-ready')) { if (@($commands | Where-Object name -eq $name).Count -ne 1) { throw "Up did not execute '$name' exactly once" } }
    }
    elseif ($Action -eq 'Ready') {
        $commands = Get-Content -LiteralPath $summary.commands -Raw | ConvertFrom-Json -Depth 100
        foreach ($name in @('dev-stand-postgres-ready', 'dev-stand-health', 'dev-stand-api-ready', 'dev-stand-operator-api-health', 'dev-stand-operator-api-ready')) { if (@($commands | Where-Object name -eq $name).Count -ne 1) { throw "Ready did not execute '$name' exactly once" } }
    }
    elseif ($Action -eq 'Scan') {
        $scans = @($summary.vulnerability_scan.scans)
        if ($scans.Count -ne 3) { throw "Scan must emit three exact image results; found $($scans.Count)" }
        foreach ($scan in $scans) {
            if ($scan.image -notin @('pgvector/pgvector:pg17', 'ghcr.io/thebtf/engram:main', 'ghcr.io/thebtf/engram-operator-console:main')) { throw "Scan used untracked image '$($scan.image)'" }
            if (-not (Test-Path -LiteralPath $scan.sarif -PathType Leaf)) { throw "Scan SARIF is missing for '$($scan.image)'" }
        }
    }
    elseif ($Action -eq 'Down') {
        if (-not $summary.residual_checks_performed -or $summary.residual_resources_zero -ne $true) { throw 'Down did not prove zero residual containers, volumes, and networks' }
    }
    return $summary
}

function Assert-SelfTestCondition { param([bool]$Condition, [string]$Message); if (-not $Condition) { throw "SELFTEST FAIL: $Message" } }

function Invoke-SelfTest {
    $root = Join-Path ([System.IO.Path]::GetTempPath()) ('run-dev-stand-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $root -Force | Out-Null
    try {
        if (-not (Test-Path -LiteralPath $Config -PathType Leaf)) { throw "SELFTEST FAIL: config fixture does not exist: $Config" }
        $base = Get-Content -LiteralPath $Config -Raw
        $validPath = Join-Path $root 'valid.yaml'; Write-Utf8NoBom $validPath $base
        $parsed = Read-DevStandConfig $validPath; Assert-SelfTestCondition ($parsed.Commands.Count -eq 4) 'valid lifecycle config was rejected'
        $mutations = @(
            @{ name = 'auth disabled'; text = $base.Replace('ENGRAM_AUTH_DISABLED: "false"', 'ENGRAM_AUTH_DISABLED: "true"') },
            @{ name = 'wrong operator target'; text = $base.Replace('NUXT_OPERATOR_API_TARGET: "http://server:37777"', 'NUXT_ENGRAM_API_TARGET: "http://server:37777"') },
            @{ name = 'wrong operator port'; text = $base.Replace('OPERATOR_CONSOLE_PORT: "3001"', 'OPERATOR_CONSOLE_PORT: "3002"') },
            @{ name = 'weaken up timeout'; text = $base.Replace('timeout_seconds: 600', 'timeout_seconds: 60') },
            @{ name = 'static credential'; text = $base.Replace('generated cryptographically inside the Up runner process', 'static-token') },
            @{ name = 'remove scan'; text = $base.Replace('-DevStandAction Scan', '-DevStandAction Ready') },
            @{ name = 'wrong image'; text = $base.Replace('ghcr.io/thebtf/engram:main', 'engram:prc-candidate') },
            @{ name = 'allow findings'; text = $base.Replace('fail_on_findings: true', 'fail_on_findings: false') },
            @{ name = 'duplicate version'; text = "version: 1`n$base" }
        )
        foreach ($mutation in $mutations) {
            $path = Join-Path $root ($mutation.name.Replace(' ', '-') + '.yaml'); Write-Utf8NoBom $path $mutation.text
            $rejected = $false; try { [void](Read-DevStandConfig $path) } catch { $rejected = $true }
            Assert-SelfTestCondition $rejected "config mutation '$($mutation.name)' was accepted"
        }
        $pwsh = (Get-Command pwsh -ErrorAction Stop).Source
        $failed = Invoke-CapturedProcess 'selftest-fail' $pwsh @('-NoProfile', '-Command', 'exit 9') (Join-Path $root 'fail.stdout.log') (Join-Path $root 'fail.stderr.log') 30
        $later = Invoke-CapturedProcess 'selftest-pass' $pwsh @('-NoProfile', '-Command', 'exit 0') (Join-Path $root 'pass.stdout.log') (Join-Path $root 'pass.stderr.log') 30
        Assert-SelfTestCondition ($failed.ExitCode -eq 9 -and $later.ExitCode -eq 0 -and (($failed.ExitCode -ne 0) -or ($later.ExitCode -ne 0))) 'later child success masked an earlier failure'
        Write-Output 'SELFTEST PASS: run-dev-stand.ps1'
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

if ($Help) { Show-Help; exit 0 }
if ($SelfTest) { Invoke-SelfTest; exit 0 }

$startedAt = [DateTimeOffset]::UtcNow
if ([string]::IsNullOrWhiteSpace($RunId)) { $RunId = $startedAt.ToString('yyyyMMddTHHmmssZ') + '-' + [guid]::NewGuid().ToString('N').Substring(0, 10) }
if ($RunId -notmatch '^[A-Za-z0-9._-]+$') { throw '-RunId may contain only letters, digits, dot, underscore, and hyphen.' }
$artifactDirectory = Join-Path $ArtifactRoot $RunId
if (Test-Path -LiteralPath $artifactDirectory) { throw "dev-stand runner artifact directory already exists: $artifactDirectory" }
New-Item -ItemType Directory -Path $artifactDirectory -Force | Out-Null
$errors = [System.Collections.Generic.List[string]]::new(); $actions = [System.Collections.Generic.List[object]]::new()
$configInfo = $null; $upAttempted = $false; $downAttempted = $false; $downSummary = $null
$nestedRoot = Join-Path $artifactDirectory 'nested'
$pwsh = $null; $runnerScript = Join-Path $PSScriptRoot 'run-db-suite.ps1'

try {
    $configInfo = Read-DevStandConfig $Config
    $pwsh = (Get-Command pwsh -ErrorAction Stop).Source
    foreach ($action in @('Up', 'Ready', 'Scan')) {
        if ($errors.Count -ne 0) { break }
        if ($action -eq 'Up') { $upAttempted = $true }
        $stdoutPath = Join-Path $artifactDirectory ("$($action.ToLowerInvariant()).stdout.log")
        $stderrPath = Join-Path $artifactDirectory ("$($action.ToLowerInvariant()).stderr.log")
        $arguments = @('-NoProfile', '-File', $runnerScript, '-DevStandAction', $action, '-ComposeProject', $configInfo.Project, '-ComposeFile', $configInfo.ComposeFile, '-ArtifactRoot', $nestedRoot, '-RunId', $RunId)
        $child = Invoke-CapturedProcess "dev-stand-$($action.ToLowerInvariant())" $pwsh $arguments $stdoutPath $stderrPath 1200
        $summaryPath = Join-Path (Join-Path (Join-Path $nestedRoot 'dev-stand') ("$RunId-$($action.ToLowerInvariant())")) 'summary.json'
        $summary = $null
        try { $summary = Read-ActionSummary $action $summaryPath $child.ExitCode } catch { $errors.Add($_.Exception.Message) }
        $actions.Add([pscustomobject][ordered]@{ action = $action; exit_code = $child.ExitCode; summary = if (Test-Path -LiteralPath $summaryPath) { [System.IO.Path]::GetFullPath($summaryPath) } else { $null }; verdict = if ($null -ne $summary) { $summary.verdict } else { $null } })
        if ($child.ExitCode -ne 0) {
            $errors.Add("dev-stand $action failed with exit $($child.ExitCode)")
            if ($null -ne $summary) { foreach ($nestedError in @($summary.errors)) { $errors.Add("$action`: $nestedError") } }
        }
    }
}
catch { $errors.Add($_.Exception.Message) }
finally {
    if ($upAttempted) {
        $downAttempted = $true
        if ($null -eq $pwsh) { try { $pwsh = (Get-Command pwsh -ErrorAction Stop).Source } catch { $errors.Add($_.Exception.Message) } }
        if ($null -ne $pwsh -and $null -ne $configInfo) {
            $downStdout = Join-Path $artifactDirectory 'down.stdout.log'; $downStderr = Join-Path $artifactDirectory 'down.stderr.log'
            $downArguments = @('-NoProfile', '-File', $runnerScript, '-DevStandAction', 'Down', '-ComposeProject', $configInfo.Project, '-ComposeFile', $configInfo.ComposeFile, '-ArtifactRoot', $nestedRoot, '-RunId', $RunId)
            $downChild = Invoke-CapturedProcess 'dev-stand-down' $pwsh $downArguments $downStdout $downStderr 300
            $downSummaryPath = Join-Path (Join-Path (Join-Path $nestedRoot 'dev-stand') "$RunId-down") 'summary.json'
            try { $downSummary = Read-ActionSummary 'Down' $downSummaryPath $downChild.ExitCode } catch { $errors.Add($_.Exception.Message) }
            $actions.Add([pscustomobject][ordered]@{ action = 'Down'; exit_code = $downChild.ExitCode; summary = if (Test-Path -LiteralPath $downSummaryPath) { [System.IO.Path]::GetFullPath($downSummaryPath) } else { $null }; verdict = if ($null -ne $downSummary) { $downSummary.verdict } else { $null } })
            if ($downChild.ExitCode -ne 0) { $errors.Add("dev-stand Down failed with exit $($downChild.ExitCode)") }
        }
    }

    $commandsPath = Join-Path $artifactDirectory 'commands.json'; Write-Utf8NoBom $commandsPath ((ConvertTo-Json -InputObject @($script:CommandRecords.ToArray()) -Depth 14) + "`n")
    $finishedAt = [DateTimeOffset]::UtcNow
    $summary = [pscustomobject][ordered]@{
        schema_version = 1; gate = 'dev-stand-lifecycle'; run_id = $RunId
        started_at = $startedAt.ToString('O'); finished_at = $finishedAt.ToString('O'); duration_seconds = [math]::Round(($finishedAt - $startedAt).TotalSeconds, 3)
        verdict = if ($errors.Count -eq 0) { 'PASS' } else { 'FAIL' }
        config = [ordered]@{ path = [System.IO.Path]::GetFullPath($Config); sha256 = if ($null -ne $configInfo) { $configInfo.Sha256 } else { $null } }
        up_attempted = $upAttempted; down_attempted = $downAttempted
        cleanup_status = if (-not $upAttempted) { 'NOT_APPLICABLE' } elseif ($null -ne $downSummary -and $downSummary.verdict -eq 'PASS' -and $downSummary.residual_resources_zero) { 'PASS' } else { 'FAIL' }
        residual_resources_zero = if ($null -ne $downSummary) { $downSummary.residual_resources_zero } else { $null }
        actions = @($actions); child_commands = $script:CommandRecords.Count; nonzero_child_commands = @($script:CommandRecords | Where-Object exit_code -ne 0).Count
        commands = [System.IO.Path]::GetFullPath($commandsPath); errors = @($errors); artifact_directory = [System.IO.Path]::GetFullPath($artifactDirectory)
    }
    $summaryPath = Join-Path $artifactDirectory 'summary.json'; Write-Utf8NoBom $summaryPath (($summary | ConvertTo-Json -Depth 20) + "`n")
    Write-Host ("dev-stand-lifecycle verdict={0} cleanup={1} actions={2}" -f $summary.verdict, $summary.cleanup_status, $summary.actions.Count)
    Write-Host "summary=$([System.IO.Path]::GetFullPath($summaryPath))"
}

if ($errors.Count -ne 0) { exit 1 }
exit 0
