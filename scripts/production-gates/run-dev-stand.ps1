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
. (Join-Path $PSScriptRoot 'compose-secret-access.ps1')
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

function Initialize-DevStandSecretRoot {
    $root = Join-Path ([System.IO.Path]::GetTempPath()) "engram-dev-stand-secrets-$PID-$([guid]::NewGuid().ToString('N'))"
    New-Item -ItemType Directory -Path $root -Force | Out-Null
    $script:PendingDevStandSecretRoot = [System.IO.Path]::GetFullPath($root)
    Set-ComposeSecretPathAccess -Path $root -Directory
    $files = [ordered]@{
        ENGRAM_AUTH_ADMIN_TOKEN_SECRET_FILE = 'admin-token.secret'
        ENGRAM_DATABASE_DSN_SECRET_FILE = 'database-dsn.secret'
        ENGRAM_POSTGRES_PASSWORD_SECRET_FILE = 'postgres-password.secret'
        ENGRAM_VAULT_KEY_SECRET_FILE = 'vault-key.secret'
    }
    foreach ($entry in $files.GetEnumerator()) {
        $path = Join-Path $root $entry.Value
        Write-Utf8NoBom $path ''
        Set-ComposeSecretPathAccess -Path $path
        [Environment]::SetEnvironmentVariable($entry.Key, [System.IO.Path]::GetFullPath($path), 'Process')
    }
    return [System.IO.Path]::GetFullPath($root)
}

function Remove-DevStandSecretRoot {
    param([Parameter(Mandatory)][string]$Path)
    $temp = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath()).TrimEnd('\', '/')
    $resolved = [System.IO.Path]::GetFullPath($Path)
    if (-not $resolved.StartsWith($temp + [System.IO.Path]::DirectorySeparatorChar, [System.StringComparison]::OrdinalIgnoreCase) -or
        -not ([System.IO.Path]::GetFileName($resolved)).StartsWith('engram-dev-stand-secrets-', [System.StringComparison]::Ordinal)) {
        throw "refusing unsafe dev-stand secret cleanup '$resolved'"
    }
    if (Test-Path -LiteralPath $resolved) { Remove-Item -LiteralPath $resolved -Recurse -Force }
    return -not (Test-Path -LiteralPath $resolved)
}

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

function Assert-DevStandConfigCredentialPolicy {
    param([Parameter(Mandatory)][string]$Text)

    foreach ($sensitiveEnvKey in @('POSTGRES_PASSWORD', 'DATABASE_DSN', 'ENGRAM_AUTH_ADMIN_TOKEN', 'ENGRAM_AUTH_BOOTSTRAP_CAPABILITY')) {
        if ($Text -cmatch ("(?m)^\s+" + [regex]::Escape($sensitiveEnvKey) + ':')) {
            throw "dev-stand config must not persist sensitive env key '$sensitiveEnvKey'"
        }
    }
    $required = [ordered]@{
        'generation scope' = '  generation_scope: "four independent cryptographic 256-bit values generated inside the Up runner process"'
        'PostgreSQL password generation' = '  postgres_password: "generated cryptographically inside the Up runner process"'
        'admin token generation' = '  admin_token: "generated cryptographically inside the Up runner process"'
        'vault key generation' = '  vault_key: "generated cryptographically inside the Up runner process"'
        'bootstrap capability generation' = '  bootstrap_capability: "generated cryptographically inside the Up runner process"'
        'PostgreSQL runtime interface' = '  postgres_runtime_interface: "ENGRAM_POSTGRES_PASSWORD_SECRET_FILE plus ENGRAM_DATABASE_DSN_SECRET_FILE"'
        'admin runtime interface' = '  admin_runtime_interface: "ENGRAM_AUTH_ADMIN_TOKEN_SECRET_FILE"'
        'bootstrap runtime interface' = '  bootstrap_runtime_interface: "ENGRAM_AUTH_BOOTSTRAP_CAPABILITY via ephemeral compose override"'
        'credential distinctness' = '  required_distinct: true'
        'credential forbidden defaults' = '  forbidden_defaults: ["engram", "password", "changeme", "change-me", "change-me-in-production", "default", "admin"]'
        'credential persistence policy' = '  persistence: "never written to raw logs, machine summaries, config, or caller environment"'
        'credential fallback policy' = '  auth_disabled_fallback: false'
    }
    foreach ($entry in $required.GetEnumerator()) { Assert-ContainsExactLine $Text $entry.Value $entry.Key }
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
        'stand API URL' = '  STAND_API_URL: "http://localhost:37778"'
        'stand operator URL' = '  STAND_OPERATOR_URL: "http://localhost:3001"'
        'operator-console API proxy target' = '  NUXT_OPERATOR_API_TARGET: "http://server:37777"'
        'auth-disabled policy' = '  ENGRAM_AUTH_DISABLED: "false"'
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
    Assert-DevStandConfigCredentialPolicy $text
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
        $prelaunchProperty = $Summary.prelaunch_image_ids.PSObject.Properties[$entry.Key]
        if ($null -eq $imageProperty -or [string]$imageProperty.Value -cne $entry.Value -or $null -eq $runningProperty -or $null -eq $tagProperty -or $null -eq $prelaunchProperty) { return $false }
        $runningId = [string]$runningProperty.Value; $tagId = [string]$tagProperty.Value; $prelaunchId = [string]$prelaunchProperty.Value
        if ($runningId -notmatch '^sha256:[a-f0-9]{64}$' -or $runningId -cne $tagId -or $runningId -cne $prelaunchId) { return $false }
    }
    return @($Summary.actual_images.PSObject.Properties).Count -eq 3 -and @($Summary.prelaunch_image_ids.PSObject.Properties).Count -eq 3
}

function Test-StrictBoolean {
    param(
        [AllowNull()]$Value,
        [Parameter(Mandatory)][bool]$Expected
    )
    return ($Value -is [bool]) -and ($Value -ceq $Expected)
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
    if ($Action -in @('Up', 'Ready', 'Scan')) {
        if ([string]$summary.source_commit -notmatch '^[a-f0-9]{40}$' -or -not (Test-StrictBoolean $summary.source_tracked_clean $true)) { throw "$Action did not prove a clean exact source commit" }
        foreach ($field in @('compose_build_completed', 'postgres_pull_completed', 'launch_no_build', 'prelaunch_to_running_image_identity')) {
            if (-not (Test-StrictBoolean $summary.$field $true)) { throw "$Action did not preserve strict-Boolean source-build/prelaunch/running image provenance for '$field'" }
        }
    }
    if ($Action -eq 'Up') {
        foreach ($field in @('ephemeral_postgres_password_generated', 'ephemeral_admin_token_generated', 'ephemeral_vault_key_generated', 'ephemeral_bootstrap_capability_generated', 'ephemeral_credentials_distinct_and_nondefault', 'ephemeral_credentials_runtime_injected', 'credential_secret_mounts_verified')) {
            if (-not (Test-StrictBoolean $summary.$field $true)) { throw "Up lacks strict true credential proof '$field'" }
        }
        foreach ($field in @('ephemeral_postgres_password_persisted', 'ephemeral_admin_token_persisted', 'ephemeral_vault_key_persisted', 'ephemeral_bootstrap_capability_persisted')) {
            if (-not (Test-StrictBoolean $summary.$field $false)) { throw "Up credential persistence proof '$field' is not strict false" }
        }
        $commands = Get-Content -LiteralPath $summary.commands -Raw | ConvertFrom-Json -Depth 100
        foreach ($name in @('dev-stand-source-root', 'dev-stand-source-commit', 'dev-stand-source-tracked-status', 'dev-stand-compose-build', 'dev-stand-postgres-pull', 'dev-stand-prelaunch-image-inspect-postgres', 'dev-stand-prelaunch-image-inspect-server', 'dev-stand-prelaunch-image-inspect-operator-console', 'dev-stand-up', 'dev-stand-postgres-container-id', 'dev-stand-postgres-credential-injection', 'dev-stand-postgres-secret-mounts', 'dev-stand-server-container-id', 'dev-stand-server-credential-injection', 'dev-stand-server-secret-mounts', 'dev-stand-postgres-ready', 'dev-stand-health', 'dev-stand-api-ready', 'dev-stand-operator-api-health', 'dev-stand-operator-api-ready')) { if (@($commands | Where-Object name -eq $name).Count -ne 1) { throw "Up did not execute '$name' exactly once" } }
    }
    elseif ($Action -eq 'Ready') {
        $commands = Get-Content -LiteralPath $summary.commands -Raw | ConvertFrom-Json -Depth 100
        foreach ($name in @('dev-stand-source-root', 'dev-stand-source-commit', 'dev-stand-source-tracked-status', 'dev-stand-postgres-ready', 'dev-stand-health', 'dev-stand-api-ready', 'dev-stand-operator-api-health', 'dev-stand-operator-api-ready')) { if (@($commands | Where-Object name -eq $name).Count -ne 1) { throw "Ready did not execute '$name' exactly once" } }
    }
    elseif ($Action -eq 'Scan') {
        $commands = Get-Content -LiteralPath $summary.commands -Raw | ConvertFrom-Json -Depth 100
        foreach ($name in @('dev-stand-source-root', 'dev-stand-source-commit', 'dev-stand-source-tracked-status', 'dev-stand-image-inventory', 'dev-stand-vulnerability-scan-postgres', 'dev-stand-vulnerability-scan-server', 'dev-stand-vulnerability-scan-operator-console')) { if (@($commands | Where-Object name -eq $name).Count -ne 1) { throw "Scan did not execute '$name' exactly once" } }
        $scans = @($summary.vulnerability_scan.scans)
        if ($scans.Count -ne 3) { throw "Scan must emit three exact image results; found $($scans.Count)" }
        $expectedScans = [ordered]@{ postgres = 'pgvector/pgvector:pg17'; server = 'ghcr.io/thebtf/engram:main'; 'operator-console' = 'ghcr.io/thebtf/engram-operator-console:main' }
        foreach ($entry in $expectedScans.GetEnumerator()) {
            $matches = @($scans | Where-Object { [string]$_.service -ceq $entry.Key })
            if ($matches.Count -ne 1) { throw "Scan must contain exactly one result for service '$($entry.Key)'; found $($matches.Count)" }
            $scan = $matches[0]
            $actualId = [string]$summary.actual_image_ids.PSObject.Properties[$entry.Key].Value
            $prelaunchId = [string]$summary.prelaunch_image_ids.PSObject.Properties[$entry.Key].Value
            if ([string]$scan.image -cne $entry.Value) { throw "Scan image mismatch for service '$($entry.Key)'" }
            if ([string]$scan.image_id -notmatch '^sha256:[a-f0-9]{64}$' -or [string]$scan.image_id -cne $actualId -or [string]$scan.image_id -cne $prelaunchId) { throw "Scan image ID is not the exact prelaunch/running ID for service '$($entry.Key)'" }
            if ([string]$scan.scanned_reference -cne "local://$($scan.image_id)") { throw "Scan did not target the exact running image ID for '$($scan.image)'" }
            $command = @($commands | Where-Object { [string]$_.name -ceq "dev-stand-vulnerability-scan-$($entry.Key)" })[0]
            $arguments = @($command.arguments | ForEach-Object { [string]$_ })
            if ($arguments.Count -eq 0 -or $arguments[-1] -cne [string]$scan.scanned_reference) { throw "Scan command arguments do not end in the recorded immutable reference for service '$($entry.Key)'" }
            if (-not (Test-Path -LiteralPath $scan.sarif -PathType Leaf)) { throw "Scan SARIF is missing for '$($scan.image)'" }
        }
    }
    elseif ($Action -eq 'Down') {
        if (-not (Test-StrictBoolean $summary.residual_checks_performed $true) -or -not (Test-StrictBoolean $summary.residual_resources_zero $true)) { throw 'Down did not prove strict-Boolean zero residual containers, volumes, and networks' }
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
        Assert-SelfTestCondition (Test-StrictBoolean $true $true) 'strict Boolean helper rejected true'
        foreach ($coercedTrue in @('true', 1, '1')) { Assert-SelfTestCondition (-not (Test-StrictBoolean $coercedTrue $true)) "strict Boolean helper accepted wrong-type true '$coercedTrue'" }
        Assert-DevStandConfigCredentialPolicy $base
        $validPath = Join-Path $root 'valid.yaml'; Write-Utf8NoBom $validPath $base
        $parsed = Read-DevStandConfig $validPath; Assert-SelfTestCondition ($parsed.Commands.Count -eq 4) 'valid lifecycle config was rejected'
        $mutations = @(
            @{ name = 'auth disabled'; text = $base.Replace('ENGRAM_AUTH_DISABLED: "false"', 'ENGRAM_AUTH_DISABLED: "true"') },
            @{ name = 'wrong operator target'; text = $base.Replace('NUXT_OPERATOR_API_TARGET: "http://server:37777"', 'NUXT_ENGRAM_API_TARGET: "http://server:37777"') },
            @{ name = 'wrong operator port'; text = $base.Replace('OPERATOR_CONSOLE_PORT: "3001"', 'OPERATOR_CONSOLE_PORT: "3002"') },
            @{ name = 'weaken up timeout'; text = $base.Replace('timeout_seconds: 600', 'timeout_seconds: 60') },
            @{ name = 'static credential'; text = $base.Replace('generated cryptographically inside the Up runner process', 'static-token') },
            @{ name = 'blank postgres credential policy'; text = $base.Replace('  postgres_password: "generated cryptographically inside the Up runner process"', '  postgres_password: ""') },
            @{ name = 'default admin credential policy'; text = $base.Replace('  admin_token: "generated cryptographically inside the Up runner process"', '  admin_token: "engram"') },
            @{ name = 'missing bootstrap credential policy'; text = $base.Replace('  bootstrap_capability: "generated cryptographically inside the Up runner process"', '') },
            @{ name = 'reused runtime interface'; text = $base.Replace('  bootstrap_runtime_interface: "ENGRAM_AUTH_BOOTSTRAP_CAPABILITY via ephemeral compose override"', '  bootstrap_runtime_interface: "ENGRAM_AUTH_ADMIN_TOKEN"') },
            @{ name = 'distinct credentials disabled'; text = $base.Replace('  required_distinct: true', '  required_distinct: false') },
            @{ name = 'persisted postgres env'; text = $base.Replace('  POSTGRES_PORT: "55433"', "  POSTGRES_PORT: `"55433`"`n  POSTGRES_PASSWORD: `"engram`"") },
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
$script:PendingDevStandSecretRoot = $null
$secretRoot = $null
$secretFilesCleaned = $false

try {
    $secretRoot = Initialize-DevStandSecretRoot
    $configInfo = Read-DevStandConfig $Config
    $pwsh = (Get-Command pwsh -ErrorAction Stop).Source
    foreach ($action in @('Up', 'Ready', 'Scan')) {
        if ($errors.Count -ne 0) { break }
        if ($action -eq 'Up') { $upAttempted = $true }
        $stdoutPath = Join-Path $artifactDirectory ("$($action.ToLowerInvariant()).stdout.log")
        $stderrPath = Join-Path $artifactDirectory ("$($action.ToLowerInvariant()).stderr.log")
        $arguments = @('-NoProfile', '-File', $runnerScript, '-DevStandAction', $action, '-ComposeProject', $configInfo.Project, '-ComposeFile', $configInfo.ComposeFile, '-DevStandSecretRoot', $secretRoot, '-ArtifactRoot', $nestedRoot, '-RunId', $RunId)
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
            $downArguments = @('-NoProfile', '-File', $runnerScript, '-DevStandAction', 'Down', '-ComposeProject', $configInfo.Project, '-ComposeFile', $configInfo.ComposeFile, '-DevStandSecretRoot', $secretRoot, '-ArtifactRoot', $nestedRoot, '-RunId', $RunId)
            $downChild = Invoke-CapturedProcess 'dev-stand-down' $pwsh $downArguments $downStdout $downStderr 300
            $downSummaryPath = Join-Path (Join-Path (Join-Path $nestedRoot 'dev-stand') "$RunId-down") 'summary.json'
            try { $downSummary = Read-ActionSummary 'Down' $downSummaryPath $downChild.ExitCode } catch { $errors.Add($_.Exception.Message) }
            $actions.Add([pscustomobject][ordered]@{ action = 'Down'; exit_code = $downChild.ExitCode; summary = if (Test-Path -LiteralPath $downSummaryPath) { [System.IO.Path]::GetFullPath($downSummaryPath) } else { $null }; verdict = if ($null -ne $downSummary) { $downSummary.verdict } else { $null } })
            if ($downChild.ExitCode -ne 0) { $errors.Add("dev-stand Down failed with exit $($downChild.ExitCode)") }
        }
    }

    try {
        $secretCleanupRoot = if (-not [string]::IsNullOrWhiteSpace($secretRoot)) { $secretRoot } else { $script:PendingDevStandSecretRoot }
        if ([string]::IsNullOrWhiteSpace($secretCleanupRoot)) {
            $secretFilesCleaned = $true
        } else {
            $secretFilesCleaned = Remove-DevStandSecretRoot -Path $secretCleanupRoot
        }
        if (-not $secretFilesCleaned) { throw 'dev-stand secret root remains after cleanup' }
        $script:PendingDevStandSecretRoot = $null
    } catch {
        $secretFilesCleaned = $false
        $errors.Add($_.Exception.Message)
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
        secret_files_cleaned = $secretFilesCleaned
        actions = @($actions); child_commands = $script:CommandRecords.Count; nonzero_child_commands = @($script:CommandRecords | Where-Object exit_code -ne 0).Count
        commands = [System.IO.Path]::GetFullPath($commandsPath); errors = @($errors); artifact_directory = [System.IO.Path]::GetFullPath($artifactDirectory)
    }
    $summaryPath = Join-Path $artifactDirectory 'summary.json'; Write-Utf8NoBom $summaryPath (($summary | ConvertTo-Json -Depth 20) + "`n")
    Write-Host ("dev-stand-lifecycle verdict={0} cleanup={1} actions={2}" -f $summary.verdict, $summary.cleanup_status, $summary.actions.Count)
    Write-Host "summary=$([System.IO.Path]::GetFullPath($summaryPath))"
}

if ($errors.Count -ne 0) { exit 1 }
exit 0
