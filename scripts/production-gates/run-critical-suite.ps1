[CmdletBinding()]
param(
    [string]$Config = '.agent/critical-suite.config.yaml',
    [string]$Run,
    [string]$ArtifactRoot = '.agent/reports/evidence/production-ready/release-gates-foundation/critical-suite-runner',
    [string]$RunId,
    [switch]$SelfTest,
    [switch]$Help
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$script:CommandRecords = [System.Collections.Generic.List[object]]::new()
$script:RepositoryRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..'))

function Show-Help {
    @'
run-critical-suite.ps1

Validates .agent/critical-suite.config.yaml, executes the exact tracked Go
critical-suite command, captures raw stdout/stderr and every child exit code,
then applies assert-go-test-json.ps1 with exact skip identities.

Usage:
  pwsh ./scripts/production-gates/run-critical-suite.ps1 \
    -Config .agent/critical-suite.config.yaml [-Run <regex>]

Exit 0 requires a valid tracked config, at least one critical Go test, go test
exit 0, JSON parser exit 0, no unexpected skips, and a PASS machine summary.
'@ | Write-Output
}

function Write-Utf8NoBom {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][AllowEmptyString()][string]$Content)
    $parent = Split-Path -Parent $Path
    if ($parent) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }
    [System.IO.File]::WriteAllText([System.IO.Path]::GetFullPath($Path), $Content, [System.Text.UTF8Encoding]::new($false))
}

function Get-Sha256 {
    param([Parameter(Mandatory)][string]$Path)
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash
}

function Quote-CommandArgument {
    param([Parameter(Mandatory)][AllowEmptyString()][string]$Value)
    if ($Value -match '^[A-Za-z0-9_./:=+,-]+$') { return $Value }
    return '"' + $Value.Replace('"', '\"') + '"'
}

function ConvertTo-EvidencePath {
    param([Parameter(Mandatory)][string]$Path)
    $full = [System.IO.Path]::GetFullPath($Path)
    $root = $script:RepositoryRoot.TrimEnd('\', '/')
    $prefix = $root + [System.IO.Path]::DirectorySeparatorChar
    if ($full.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)) {
        return ([System.IO.Path]::GetRelativePath($root, $full)).Replace('\', '/')
    }
    return 'external/' + [System.IO.Path]::GetFileName($full)
}

function ConvertTo-EvidenceExecutable {
    param([Parameter(Mandatory)][string]$Executable)
    $leaf = [System.IO.Path]::GetFileName($Executable)
    if ($leaf.EndsWith('.exe', [System.StringComparison]::OrdinalIgnoreCase)) {
        return $leaf.Substring(0, $leaf.Length - 4)
    }
    return $leaf
}

function ConvertTo-EvidenceArgument {
    param([Parameter(Mandatory)][AllowEmptyString()][string]$Value)
    if ([System.IO.Path]::IsPathRooted($Value)) { return ConvertTo-EvidencePath $Value }
    if ($Value -match '^(?:\.agent|scripts)[\\/]') { return $Value.Replace('\', '/') }
    return $Value
}

function Invoke-CapturedProcess {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$Executable,
        [string[]]$Arguments = @(),
        [Parameter(Mandatory)][string]$StdoutPath,
        [Parameter(Mandatory)][string]$StderrPath,
        [ValidateRange(1, 7200)][int]$TimeoutSeconds = 1800
    )

    $startedAt = [DateTimeOffset]::UtcNow
    $exitCode = 127
    $timedOut = $false
    $stdout = ''
    $stderr = ''
    $process = $null
    try {
        $info = [System.Diagnostics.ProcessStartInfo]::new()
        $info.FileName = $Executable
        $info.UseShellExecute = $false
        $info.CreateNoWindow = $true
        $info.RedirectStandardOutput = $true
        $info.RedirectStandardError = $true
        foreach ($argument in $Arguments) { [void]$info.ArgumentList.Add([string]$argument) }
        $process = [System.Diagnostics.Process]::new(); $process.StartInfo = $info
        if (-not $process.Start()) { throw "process '$Executable' did not start" }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit($TimeoutSeconds * 1000)) {
            $timedOut = $true
            try { $process.Kill($true) } catch {}
            [void]$process.WaitForExit(30000)
        }
        else { $process.WaitForExit() }
        $stdout = $stdoutTask.GetAwaiter().GetResult()
        $stderr = $stderrTask.GetAwaiter().GetResult()
        $exitCode = if ($timedOut) { 124 } else { $process.ExitCode }
    }
    catch {
        $stderr = "PROCESS_START_OR_CAPTURE_ERROR: $($_.Exception.Message)`n"
        $exitCode = 127
    }
    finally { if ($null -ne $process) { $process.Dispose() } }

    Write-Utf8NoBom $StdoutPath $stdout
    Write-Utf8NoBom $StderrPath $stderr
    $finishedAt = [DateTimeOffset]::UtcNow
    $evidenceExecutable = ConvertTo-EvidenceExecutable $Executable
    $evidenceArguments = @($Arguments | ForEach-Object { ConvertTo-EvidenceArgument ([string]$_) })
    $commandParts = [System.Collections.Generic.List[string]]::new()
    $commandParts.Add((Quote-CommandArgument $evidenceExecutable))
    foreach ($argument in $evidenceArguments) { $commandParts.Add((Quote-CommandArgument ([string]$argument))) }
    $record = [pscustomobject][ordered]@{
        name = $Name; executable = $evidenceExecutable; arguments = @($evidenceArguments)
        command = $commandParts -join ' '
        working_directory = '.'
        started_at = $startedAt.ToString('O'); finished_at = $finishedAt.ToString('O')
        duration_seconds = [math]::Round(($finishedAt - $startedAt).TotalSeconds, 3)
        exit_code = $exitCode; timed_out = $timedOut
        stdout = ConvertTo-EvidencePath $StdoutPath; stderr = ConvertTo-EvidencePath $StderrPath
    }
    $script:CommandRecords.Add($record)
    return [pscustomobject]@{ ExitCode = $exitCode; Stdout = $stdout; Stderr = $stderr; Record = $record }
}

function Unquote-YamlScalar {
    param([Parameter(Mandatory)][AllowEmptyString()][string]$Value)
    $trimmed = $Value.Trim()
    if ($trimmed.Length -ge 2 -and (($trimmed[0] -eq '"' -and $trimmed[-1] -eq '"') -or ($trimmed[0] -eq "'" -and $trimmed[-1] -eq "'"))) {
        return $trimmed.Substring(1, $trimmed.Length - 2)
    }
    return $trimmed
}

function Assert-ContainsExactLine {
    param(
        [Parameter(Mandatory)][string]$Text,
        [Parameter(Mandatory)][string]$Line,
        [Parameter(Mandatory)][string]$Name
    )
    $count = @(($Text -split "`r?`n") | Where-Object { $_ -ceq $Line }).Count
    if ($count -ne 1) { throw "critical config $Name must appear exactly once; found $count" }
}

function Get-TopLevelScalar {
    param([Parameter(Mandatory)][string]$Text, [Parameter(Mandatory)][string]$Name)
    $matches = [regex]::Matches($Text, ('(?m)^' + [regex]::Escape($Name) + ':\s*(?<value>[^\r\n#]+?)\s*$'))
    if ($matches.Count -ne 1) { throw "critical config top-level '$Name' must appear exactly once; found $($matches.Count)" }
    return Unquote-YamlScalar $matches[0].Groups['value'].Value
}

function Get-TopLevelSection {
    param([Parameter(Mandatory)][string]$Text, [Parameter(Mandatory)][string]$Name)
    $headingMatches = [regex]::Matches($Text, ('(?m)^' + [regex]::Escape($Name) + ':\s*$'))
    if ($headingMatches.Count -ne 1) { throw "critical config section '$Name' must appear exactly once; found $($headingMatches.Count)" }
    $match = [regex]::Match($Text, ('(?ms)^' + [regex]::Escape($Name) + ':\s*\r?\n(?<body>.*?)(?=^[A-Za-z0-9_-]+:\s*(?:\r?\n|[^\r\n])|\z)'))
    if (-not $match.Success) { throw "critical config section '$Name' has no parseable body" }
    return $match.Groups['body'].Value
}

function Test-ValidSkipIdentity {
    param([AllowNull()][AllowEmptyString()][string]$Identity)
    if ([string]::IsNullOrWhiteSpace($Identity) -or $Identity.Length -gt 512) { return $false }
    if ($Identity -match '[\x00-\x1f\x7f*?^$\\(){}\[\]|+]') { return $false }
    if ($Identity -notmatch '^[A-Za-z0-9._/-]+$') { return $false }
    return $Identity.Contains('/')
}

function Read-CriticalConfig {
    param([Parameter(Mandatory)][string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { throw "critical config does not exist: $Path" }
    $text = Get-Content -LiteralPath $Path -Raw
    if ((Get-TopLevelScalar $text 'version') -ne '1') { throw 'critical config version must be 1' }
    $testGlob = Get-TopLevelScalar $text 'test_glob'
    if ($testGlob -ne 'tests/critical/**/*.{go,py,ts,js,rs,cs,sh}') { throw "unsupported critical test_glob '$testGlob'" }
    if ((Get-TopLevelScalar $text 'fail_on_missing') -ne 'error') { throw 'critical fail_on_missing must be error' }
    [int]$timeoutMinutes = 0
    if (-not [int]::TryParse((Get-TopLevelScalar $text 'timeout_minutes'), [ref]$timeoutMinutes) -or $timeoutMinutes -lt 1 -or $timeoutMinutes -gt 120) { throw 'critical timeout_minutes must be 1..120' }
    foreach ($section in @('runner', 'database_gate', 'coverage', 'evidence', 'security')) { [void](Get-TopLevelSection $text $section) }

    $requiredExactLines = [ordered]@{
        'category smoke' = '  - smoke'
        'category behavioral' = '  - behavioral'
        'category data consistency' = '  - data-consistency'
        'dev stand requirement' = 'dev_stand_required: true'
        'database command' = '  command: "pwsh -NoProfile -File scripts/production-gates/run-db-suite.ps1 -FreshDatabase -Package ./... -Race -FailOnUnexpectedSkip"'
        'database image' = '  postgres_image: "pgvector/pgvector:pg17"'
        'repeat source' = '  repeat_source: "run-db-suite.ps1 default (3); callers do not duplicate the value"'
        'fresh database policy' = '  fresh_database_per_repeat: true'
        'database schema' = '  schema: "public"'
        'package parallelism' = '  package_parallelism: 1'
        'test parallelism' = '  test_parallelism: 1'
        'race detector' = '  race_detector: true'
        'connection budget' = '  connection_budget: 20'
        'post-test sessions' = '  post_test_sessions_required: 0'
        'cleanup requirement' = '  cleanup_required: true'
        'coverage profile requirement' = '  profile_required_on_all_operating_systems: true'
        'overall coverage floor' = '  overall_statement_minimum_percent: 60'
        'module coverage floor' = '    "internal/module/": 75'
        'engramcore coverage floor' = '    "internal/handlers/engramcore": 60'
        'loom coverage floor' = '    "internal/handlers/loom": 70'
        'launcher coverage floor' = '    "cmd/engram/": 10'
        'server coverage floor' = '    "cmd/engram-server/": 10'
        'update coverage floor' = '    "internal/update/": 20'
        'worker coverage floor' = '    "internal/worker/": 55'
        'MCP coverage floor' = '    "internal/mcp/": 55'
        'database coverage floor' = '    "internal/db/gorm/": 55'
        'evidence root' = '  root: ".agent/reports/evidence/production-ready/release-gates-foundation"'
        'raw stdout evidence' = '  require_raw_stdout: true'
        'raw stderr evidence' = '  require_raw_stderr: true'
        'machine summary evidence' = '  require_machine_summary: true'
        'child exits evidence' = '  require_child_exit_codes: true'
        'database identity evidence' = '  require_database_schema_identity: true'
        'session evidence' = '  require_pg_stat_activity: true'
        'cleanup evidence' = '  require_cleanup_result: true'
        'image scan command' = '    command: "pwsh -NoProfile -File scripts/production-gates/run-db-suite.ps1 -DevStandAction Scan -ComposeProject engram-critical-stand -ComposeFile docker-compose.yml"'
        'image scanner' = '    scanner: "docker scout cves"'
        'image severities' = '    severities: ["critical", "high"]'
        'image findings policy' = '    fail_on_findings: true'
        'PostgreSQL image target' = '      postgres: "ghcr.io/thebtf/engram-postgres:main"'
        'server image target' = '      server: "ghcr.io/thebtf/engram:main"'
        'operator image target' = '      operator-console: "ghcr.io/thebtf/engram-operator-console:main"'
        'forbidden synthetic tag' = '      - "engram:prc-candidate"'
    }
    foreach ($requiredLine in $requiredExactLines.GetEnumerator()) {
        Assert-ContainsExactLine $text $requiredLine.Value $requiredLine.Key
    }
    if ($text -match '(?i)(?<![A-Za-z0-9_])-Repeat(?:\s+|=)\d+') { throw 'critical config must consume the run-db-suite default Repeat=3 instead of duplicating it' }

    $runner = Get-TopLevelSection $text 'runner'
    $fields = @{}
    $allowed = [System.Collections.Generic.List[string]]::new()
    $lines = $runner -split "`r?`n"
    for ($index = 0; $index -lt $lines.Count; $index++) {
        $line = $lines[$index]
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        $fieldMatch = [regex]::Match($line, '^\s{2}(?<name>[A-Za-z0-9_]+):\s*(?<value>.*)$')
        if (-not $fieldMatch.Success) { throw "malformed runner config line '$line'" }
        $name = $fieldMatch.Groups['name'].Value
        if ($fields.ContainsKey($name)) { throw "duplicate runner field '$name'" }
        $value = $fieldMatch.Groups['value'].Value.Trim()
        $fields[$name] = $value
        if ($name -eq 'allowed_skip_identities' -and [string]::IsNullOrWhiteSpace($value)) {
            while ($index + 1 -lt $lines.Count -and $lines[$index + 1] -match '^\s{4}-\s*(?<item>.+?)\s*$') {
                $index++
                $allowed.Add((Unquote-YamlScalar ([regex]::Match($lines[$index], '^\s{4}-\s*(?<item>.+?)\s*$').Groups['item'].Value)))
            }
        }
    }
    $expectedFields = @('command', 'transcript_parser', 'fail_on_unexpected_skip', 'allowed_skip_identities')
    foreach ($field in $expectedFields) { if (-not $fields.ContainsKey($field)) { throw "runner config is missing '$field'" } }
    foreach ($field in $fields.Keys) { if ($field -notin $expectedFields) { throw "unknown runner config field '$field'" } }

    $command = Unquote-YamlScalar $fields.command
    $parser = Unquote-YamlScalar $fields.transcript_parser
    if ($command -cne 'go test -tags=critical -json ./tests/critical/... -count=1') { throw "critical runner command drifted: '$command'" }
    if ($parser -cne 'pwsh -NoProfile -File scripts/production-gates/assert-go-test-json.ps1 -FailOnUnexpectedSkip') { throw "critical transcript parser command drifted: '$parser'" }
    if ((Unquote-YamlScalar $fields.fail_on_unexpected_skip).ToLowerInvariant() -ne 'true') { throw 'critical runner must fail on unexpected skips' }
    if ($fields.allowed_skip_identities -notin @('[]', '') -and $allowed.Count -eq 0) { throw 'allowed_skip_identities must be [] or an exact YAML list' }
    $seen = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    foreach ($identity in $allowed) {
        if (-not (Test-ValidSkipIdentity $identity)) { throw "invalid exact skip identity '$identity'" }
        if (-not $seen.Add($identity)) { throw "duplicate exact skip identity '$identity'" }
    }

    $criticalFiles = @(Get-ChildItem -LiteralPath 'tests/critical' -Recurse -File -ErrorAction SilentlyContinue | Where-Object Extension -in @('.go', '.py', '.ts', '.js', '.rs', '.cs', '.sh'))
    $goFiles = @($criticalFiles | Where-Object Extension -eq '.go')
    if ($criticalFiles.Count -eq 0 -or $goFiles.Count -eq 0) { throw 'critical test glob resolved no Go tests' }
    return [pscustomobject]@{
        Text = $text; Sha256 = Get-Sha256 $Path; TimeoutMinutes = $timeoutMinutes
        Command = $command; ParserCommand = $parser; AllowedSkipIdentities = @($allowed)
        MatchedFiles = @($criticalFiles.FullName); MatchedGoFiles = @($goFiles.FullName)
    }
}

function Assert-SelfTestCondition { param([bool]$Condition, [string]$Message); if (-not $Condition) { throw "SELFTEST FAIL: $Message" } }

function Invoke-SelfTest {
    $root = Join-Path ([System.IO.Path]::GetTempPath()) ('run-critical-suite-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $root -Force | Out-Null
    try {
        if (-not (Test-Path -LiteralPath $Config -PathType Leaf)) { throw "SELFTEST FAIL: config fixture does not exist: $Config" }
        $baseConfig = Get-Content -LiteralPath $Config -Raw
        $validConfigPath = Join-Path $root 'valid.yaml'; Write-Utf8NoBom $validConfigPath $baseConfig
        $parsedConfig = Read-CriticalConfig $validConfigPath
        Assert-SelfTestCondition ($parsedConfig.Command -eq 'go test -tags=critical -json ./tests/critical/... -count=1') 'valid tracked config was rejected'
        $configMutations = @(
            @{ name = 'narrow package'; text = $baseConfig.Replace('./tests/critical/...', './tests/critical/auth/...') },
            @{ name = 'disable skip failure'; text = $baseConfig.Replace('fail_on_unexpected_skip: true', 'fail_on_unexpected_skip: false') },
            @{ name = 'broad skip identity'; text = $baseConfig.Replace('allowed_skip_identities: []', "allowed_skip_identities:`n    - '.*'") },
            @{ name = 'unknown runner field'; text = $baseConfig.Replace('  fail_on_unexpected_skip: true', "  fail_on_unexpected_skip: true`n  surprise: true") },
            @{ name = 'disable dev stand'; text = $baseConfig.Replace('dev_stand_required: true', 'dev_stand_required: false') },
            @{ name = 'remove database race'; text = $baseConfig.Replace(' -Race ', ' ') },
            @{ name = 'weaken coverage'; text = $baseConfig.Replace('overall_statement_minimum_percent: 60', 'overall_statement_minimum_percent: 50') },
            @{ name = 'allow image findings'; text = $baseConfig.Replace('    fail_on_findings: true', '    fail_on_findings: false') },
            @{ name = 'duplicate version'; text = "version: 1`n$baseConfig" }
        )
        foreach ($mutation in $configMutations) {
            $path = Join-Path $root ($mutation.name.Replace(' ', '-') + '.yaml'); Write-Utf8NoBom $path $mutation.text
            $rejected = $false; try { [void](Read-CriticalConfig $path) } catch { $rejected = $true }
            Assert-SelfTestCondition $rejected "config mutation '$($mutation.name)' was accepted"
        }
        Assert-SelfTestCondition (Test-ValidSkipIdentity 'example/package/TestNeedsLinux') 'exact test identity was rejected'
        foreach ($bad in @('', '.*', '^.*$', 'example/Test*', 'example|other', 'TestOnly')) { Assert-SelfTestCondition (-not (Test-ValidSkipIdentity $bad)) "broad skip identity '$bad' was accepted" }
        $pwsh = (Get-Command pwsh -ErrorAction Stop).Source
        $failed = Invoke-CapturedProcess 'selftest-fail' $pwsh @('-NoProfile', '-Command', 'exit 7') (Join-Path $root 'fail.stdout.log') (Join-Path $root 'fail.stderr.log') 30
        $later = Invoke-CapturedProcess 'selftest-later-pass' $pwsh @('-NoProfile', '-Command', 'exit 0') (Join-Path $root 'pass.stdout.log') (Join-Path $root 'pass.stderr.log') 30
        Assert-SelfTestCondition ($failed.ExitCode -eq 7 -and $later.ExitCode -eq 0) 'child exits were not captured independently'
        Assert-SelfTestCondition (($failed.ExitCode -ne 0) -or ($later.ExitCode -ne 0)) 'later success masked earlier failure'
        Assert-SelfTestCondition ($failed.Record.working_directory -ceq '.') 'command evidence does not declare repository-relative working_directory'
        Assert-SelfTestCondition (-not [System.IO.Path]::IsPathRooted([string]$failed.Record.executable)) 'command evidence exposes a host-specific executable path'
        Assert-SelfTestCondition (-not [System.IO.Path]::IsPathRooted([string]$failed.Record.stdout) -and -not [System.IO.Path]::IsPathRooted([string]$failed.Record.stderr)) 'command evidence exposes host-specific artifact paths'
        $missing = Invoke-CapturedProcess 'selftest-missing' ('missing-critical-runner-' + [guid]::NewGuid().ToString('N')) @() (Join-Path $root 'missing.stdout.log') (Join-Path $root 'missing.stderr.log') 30
        Assert-SelfTestCondition ($missing.ExitCode -eq 127 -and $missing.Stderr -match 'PROCESS_START_OR_CAPTURE_ERROR') 'process-start failure did not fail closed'
        Write-Output 'SELFTEST PASS: run-critical-suite.ps1'
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

if ($Help) { Show-Help; exit 0 }
if ($SelfTest) { Invoke-SelfTest; exit 0 }

$startedAt = [DateTimeOffset]::UtcNow
if ([string]::IsNullOrWhiteSpace($RunId)) { $RunId = $startedAt.ToString('yyyyMMddTHHmmssZ') + '-' + [guid]::NewGuid().ToString('N').Substring(0, 10) }
if ($RunId -notmatch '^[A-Za-z0-9._-]+$') { throw '-RunId may contain only letters, digits, dot, underscore, and hyphen.' }
$artifactDirectory = Join-Path $ArtifactRoot $RunId
if (Test-Path -LiteralPath $artifactDirectory) { throw "critical-suite artifact directory already exists: $artifactDirectory" }
New-Item -ItemType Directory -Path $artifactDirectory -Force | Out-Null
$errors = [System.Collections.Generic.List[string]]::new()
$configInfo = $null
$goResult = $null
$parserResult = $null
$parserSummary = $null
$goStdout = Join-Path $artifactDirectory 'go-test.stdout.jsonl'
$goStderr = Join-Path $artifactDirectory 'go-test.stderr.log'
$parserStdout = Join-Path $artifactDirectory 'json-parser.stdout.log'
$parserStderr = Join-Path $artifactDirectory 'json-parser.stderr.log'
$parserSummaryPath = Join-Path $artifactDirectory 'go-test-summary.json'

try {
    $configInfo = Read-CriticalConfig $Config
    $go = (Get-Command go -ErrorAction Stop).Source
    $goArguments = [System.Collections.Generic.List[string]]::new()
    foreach ($argument in @('test', '-tags=critical', '-json')) { $goArguments.Add($argument) }
    if (-not [string]::IsNullOrWhiteSpace($Run)) { $goArguments.Add('-run'); $goArguments.Add($Run) }
    foreach ($argument in @('./tests/critical/...', '-count=1')) { $goArguments.Add($argument) }
    $goResult = Invoke-CapturedProcess 'critical-go-test' $go @($goArguments) $goStdout $goStderr ($configInfo.TimeoutMinutes * 60)
    if ($goResult.ExitCode -ne 0) { $errors.Add("critical go test failed with exit $($goResult.ExitCode)") }

    $pwsh = (Get-Command pwsh -ErrorAction Stop).Source
    $parserScript = Join-Path $PSScriptRoot 'assert-go-test-json.ps1'
    $parserArguments = [System.Collections.Generic.List[string]]::new()
    foreach ($argument in @('-NoProfile', '-File', $parserScript, '-InputPath', $goStdout, '-SummaryPath', $parserSummaryPath, '-FailOnUnexpectedSkip')) { $parserArguments.Add($argument) }
    if ($configInfo.AllowedSkipIdentities.Count -gt 0) {
        $parserArguments.Add('-AllowedSkipIdentity')
        foreach ($identity in $configInfo.AllowedSkipIdentities) { $parserArguments.Add($identity) }
    }
    $parserResult = Invoke-CapturedProcess 'critical-json-parser' $pwsh @($parserArguments) $parserStdout $parserStderr 120
    if ($parserResult.ExitCode -ne 0) { $errors.Add("critical JSON parser failed with exit $($parserResult.ExitCode)") }
    if (-not (Test-Path -LiteralPath $parserSummaryPath -PathType Leaf)) { $errors.Add('critical JSON parser summary is missing') }
    else {
        try { $parserSummary = Get-Content -LiteralPath $parserSummaryPath -Raw | ConvertFrom-Json -Depth 100 }
        catch { $errors.Add("critical JSON parser summary is invalid: $($_.Exception.Message)") }
        if ($null -ne $parserSummary -and $parserSummary.verdict -ne 'PASS') { $errors.Add("critical JSON parser verdict is '$($parserSummary.verdict)'") }
    }
}
catch { $errors.Add($_.Exception.Message) }
finally {
    $commandsPath = Join-Path $artifactDirectory 'commands.json'
    Write-Utf8NoBom $commandsPath ((ConvertTo-Json -InputObject @($script:CommandRecords.ToArray()) -Depth 12) + "`n")
    $finishedAt = [DateTimeOffset]::UtcNow
    $summary = [pscustomobject][ordered]@{
        schema_version = 1; gate = 'critical-suite'; run_id = $RunId
        started_at = $startedAt.ToString('O'); finished_at = $finishedAt.ToString('O'); duration_seconds = [math]::Round(($finishedAt - $startedAt).TotalSeconds, 3)
        verdict = if ($errors.Count -eq 0) { 'PASS' } else { 'FAIL' }
        config = [ordered]@{ path = ConvertTo-EvidencePath $Config; sha256 = if ($null -ne $configInfo) { $configInfo.Sha256 } else { $null }; command = if ($null -ne $configInfo) { $configInfo.Command } else { $null } }
        run_pattern = $Run; allowed_skip_identities = if ($null -ne $configInfo) { @($configInfo.AllowedSkipIdentities) } else { @() }
        matched_test_files = if ($null -ne $configInfo) { $configInfo.MatchedFiles.Count } else { 0 }; matched_go_files = if ($null -ne $configInfo) { $configInfo.MatchedGoFiles.Count } else { 0 }
        go_test_exit = if ($null -ne $goResult) { $goResult.ExitCode } else { $null }; json_parser_exit = if ($null -ne $parserResult) { $parserResult.ExitCode } else { $null }
        json_summary = if (Test-Path -LiteralPath $parserSummaryPath) { ConvertTo-EvidencePath $parserSummaryPath } else { $null }
        counts = if ($null -ne $parserSummary) { $parserSummary.counts } else { $null }
        child_commands = $script:CommandRecords.Count; nonzero_child_commands = @($script:CommandRecords | Where-Object exit_code -ne 0).Count
        commands = ConvertTo-EvidencePath $commandsPath; errors = @($errors); artifact_directory = ConvertTo-EvidencePath $artifactDirectory
    }
    $summaryPath = Join-Path $artifactDirectory 'summary.json'
    Write-Utf8NoBom $summaryPath (($summary | ConvertTo-Json -Depth 20) + "`n")
    Write-Host ("critical-suite verdict={0} go_test_exit={1} parser_exit={2}" -f $summary.verdict, $summary.go_test_exit, $summary.json_parser_exit)
    Write-Host "summary=$([System.IO.Path]::GetFullPath($summaryPath))"
}

if ($errors.Count -ne 0) { exit 1 }
exit 0
