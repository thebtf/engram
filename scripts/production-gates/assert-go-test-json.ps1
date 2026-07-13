[CmdletBinding()]
param(
    [string]$InputPath,
    [string]$SummaryPath,
    [switch]$FailOnUnexpectedSkip,
    [Alias('AllowedSkipPattern')][string[]]$AllowedSkipIdentity = @(),
    [switch]$Help,
    [switch]$SelfTest
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$script:RepositoryRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..'))

function Show-Help {
    @'
assert-go-test-json.ps1

Parses a raw `go test -json` transcript into a stable machine summary. The
assertion fails on malformed/incomplete output, failed tests or packages, and
(when requested) any skip that is not explicitly allowed. Packages with no test
files are reported as `no_tests`; they are not treated as test skips.

Usage:
  pwsh ./scripts/production-gates/assert-go-test-json.ps1 \
    -InputPath <go-test.jsonl> \
    -SummaryPath <summary.json> \
    [-FailOnUnexpectedSkip] \
    [-AllowedSkipIdentity <exact-package-or-package/test>[,...]]

Options:
  -Help                    Print this help and exit 0.
  -SelfTest                Run deterministic parser regression tests and exit.
  -InputPath               Raw stdout produced by `go test -json`.
  -SummaryPath             JSON summary destination. Defaults beside input.
  -FailOnUnexpectedSkip    Make any non-allowlisted test/package skip fatal.
  -AllowedSkipIdentity     Exact case-sensitive package or package/test identity.
                           Regex, wildcard, output-only, and broad matches are rejected.

Exit codes:
  0  Transcript is structurally complete and all enabled assertions pass.
  1  A test/package failed, output was malformed/incomplete, or a skip gate failed.
'@ | Write-Output
}

function Write-Utf8NoBom {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][AllowEmptyString()][string]$Content)
    $parent = Split-Path -Parent $Path
    if ($parent) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }
    [System.IO.File]::WriteAllText([System.IO.Path]::GetFullPath($Path), $Content, [System.Text.UTF8Encoding]::new($false))
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

function Test-IsValidAllowedSkipIdentity {
    param([string]$Identity)
    if ([string]::IsNullOrWhiteSpace($Identity)) { return $false }
    if ($Identity.Length -gt 512) { return $false }
    if ($Identity -match '[\x00-\x1F\x7F]') { return $false }
    if ($Identity.Contains('*') -or $Identity.Contains('?')) { return $false }
    return $true
}

function Test-MatchesAllowedSkip {
    param([Parameter(Mandatory)][string]$Identity, [string[]]$AllowedIdentities)
    foreach ($allowedIdentity in $AllowedIdentities) {
        if ([string]::Equals($Identity, $allowedIdentity, [System.StringComparison]::Ordinal)) { return $true }
    }
    return $false
}

function Read-GoTestTranscript {
    param(
        [Parameter(Mandatory)][string]$Path,
        [bool]$EnforceUnexpectedSkip,
        [string[]]$AllowedIdentities
    )

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        return [pscustomobject]@{
            schema_version = 1; verdict = 'FAIL'; input_path = ConvertTo-EvidencePath $Path
            fail_on_unexpected_skip = $EnforceUnexpectedSkip; allowed_skip_identities = @($AllowedIdentities)
            counts = [ordered]@{ packages = 0; tests = 0; passed = 0; failed = 0; skipped = 0; no_tests = 0; zero_tests = 1; incomplete = 0; unexpected_skips = 0; malformed_lines = 0 }
            packages = @(); tests = @(); unexpected_skips = @(); errors = @("input transcript does not exist: $(ConvertTo-EvidencePath $Path)")
        }
    }

    $identityErrors = [System.Collections.Generic.List[string]]::new()
    $seenIdentities = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    foreach ($allowedIdentity in $AllowedIdentities) {
        if (-not (Test-IsValidAllowedSkipIdentity $allowedIdentity)) {
            $identityErrors.Add("invalid allowed-skip identity '$allowedIdentity': identities must be non-empty exact values without wildcards or control characters")
            continue
        }
        if (-not $seenIdentities.Add($allowedIdentity)) {
            $identityErrors.Add("duplicate allowed-skip identity '$allowedIdentity'")
        }
    }
    if ($identityErrors.Count -gt 0) {
        return [pscustomobject]@{
            schema_version = 1; verdict = 'FAIL'; input_path = ConvertTo-EvidencePath $Path
            fail_on_unexpected_skip = $EnforceUnexpectedSkip; allowed_skip_identities = @($AllowedIdentities)
            counts = [ordered]@{ packages = 0; tests = 0; passed = 0; failed = 0; skipped = 0; no_tests = 0; zero_tests = 1; incomplete = 0; unexpected_skips = 0; malformed_lines = 0 }
            packages = @(); tests = @(); unexpected_skips = @(); errors = @($identityErrors)
        }
    }

    $packageStates = @{}
    $testStates = @{}
    $parseErrors = [System.Collections.Generic.List[string]]::new()
    $gateErrors = [System.Collections.Generic.List[string]]::new()
    $lineNumber = 0

    foreach ($line in [System.IO.File]::ReadLines([System.IO.Path]::GetFullPath($Path))) {
        $lineNumber++
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        try { $event = $line | ConvertFrom-Json -ErrorAction Stop }
        catch { $parseErrors.Add("line $lineNumber is not valid JSON: $($_.Exception.Message)"); continue }

        if (-not $event.PSObject.Properties['Action'] -or [string]::IsNullOrWhiteSpace([string]$event.Action)) {
            $parseErrors.Add("line $lineNumber has no Action"); continue
        }
        if (-not $event.PSObject.Properties['Package'] -or [string]::IsNullOrWhiteSpace([string]$event.Package)) {
            $parseErrors.Add("line $lineNumber has no Package"); continue
        }

        $packageName = [string]$event.Package
        if (-not $packageStates.ContainsKey($packageName)) {
            $packageStates[$packageName] = [ordered]@{ package = $packageName; outcome = 'incomplete'; elapsed_seconds = $null; last_output = ''; tests_observed = 0 }
        }
        $packageState = $packageStates[$packageName]
        $output = if ($event.PSObject.Properties['Output']) { [string]$event.Output } else { '' }
        $testName = if ($event.PSObject.Properties['Test']) { [string]$event.Test } else { '' }
        $elapsed = if ($event.PSObject.Properties['Elapsed']) { [double]$event.Elapsed } else { $null }
        $action = [string]$event.Action

        if ([string]::IsNullOrWhiteSpace($testName)) {
            if (-not [string]::IsNullOrEmpty($output)) { $packageState.last_output = $output.TrimEnd() }
            if ($action -in @('pass', 'fail', 'skip')) {
                if ($action -eq 'skip' -and $packageState.last_output -match '\[no test files\]') { $packageState.outcome = 'no_tests' }
                else { $packageState.outcome = $action }
                $packageState.elapsed_seconds = $elapsed
            }
            continue
        }

        $testKey = "$packageName`0$testName"
        if (-not $testStates.ContainsKey($testKey)) {
            $testStates[$testKey] = [ordered]@{ package = $packageName; test = $testName; outcome = 'incomplete'; elapsed_seconds = $null; last_output = ''; skip_allowed = $false }
            $packageState.tests_observed++
        }
        $testState = $testStates[$testKey]
        if (-not [string]::IsNullOrEmpty($output)) { $testState.last_output = $output.TrimEnd() }
        if ($action -in @('pass', 'fail', 'skip')) {
            $testState.outcome = $action
            $testState.elapsed_seconds = $elapsed
            if ($action -eq 'skip') {
                $testState.skip_allowed = Test-MatchesAllowedSkip -Identity "$packageName/$testName" -AllowedIdentities $AllowedIdentities
            }
        }
    }

    $packages = @($packageStates.Values | ForEach-Object { [pscustomobject]$_ } | Sort-Object package)
    $tests = @($testStates.Values | ForEach-Object { [pscustomobject]$_ } | Sort-Object package, test)
    $unexpectedSkips = [System.Collections.Generic.List[object]]::new()
    if ($EnforceUnexpectedSkip) {
        foreach ($test in $tests) {
            if ($test.outcome -eq 'skip' -and -not $test.skip_allowed) {
                $unexpectedSkips.Add([pscustomobject]@{ package = $test.package; test = $test.test; output = $test.last_output })
            }
        }
        foreach ($package in $packages) {
            if ($package.outcome -eq 'skip' -and -not (Test-MatchesAllowedSkip -Identity $package.package -AllowedIdentities $AllowedIdentities)) {
                $unexpectedSkips.Add([pscustomobject]@{ package = $package.package; test = $null; output = $package.last_output })
            }
        }
    }

    $failedTests = @($tests | Where-Object outcome -eq 'fail').Count
    $failedPackages = @($packages | Where-Object outcome -eq 'fail').Count
    $incomplete = @($tests | Where-Object outcome -eq 'incomplete').Count + @($packages | Where-Object outcome -eq 'incomplete').Count
    if ($tests.Count -eq 0) { $gateErrors.Add('zero tests executed; refusing a false-green package-only result') }
    $verdict = if ($parseErrors.Count -gt 0 -or $gateErrors.Count -gt 0 -or $failedTests -gt 0 -or $failedPackages -gt 0 -or $incomplete -gt 0 -or $unexpectedSkips.Count -gt 0) { 'FAIL' } else { 'PASS' }

    [pscustomobject]@{
        schema_version = 1
        verdict = $verdict
        input_path = ConvertTo-EvidencePath $Path
        fail_on_unexpected_skip = $EnforceUnexpectedSkip
        allowed_skip_identities = @($AllowedIdentities)
        counts = [ordered]@{
            packages = $packages.Count; tests = $tests.Count
            passed = @($tests | Where-Object outcome -eq 'pass').Count
            failed = $failedTests; skipped = @($tests | Where-Object outcome -eq 'skip').Count
            no_tests = @($packages | Where-Object outcome -eq 'no_tests').Count
            zero_tests = if ($tests.Count -eq 0) { 1 } else { 0 }
            incomplete = $incomplete; unexpected_skips = $unexpectedSkips.Count; malformed_lines = $parseErrors.Count
        }
        packages = $packages
        tests = $tests
        unexpected_skips = @($unexpectedSkips)
        errors = @(@($parseErrors) + @($gateErrors))
    }
}

function Assert-SelfTest { param([bool]$Condition, [string]$Message); if (-not $Condition) { throw "SELFTEST FAIL: $Message" } }

function Invoke-SelfTest {
    $root = Join-Path ([System.IO.Path]::GetTempPath()) ("assert-go-test-json-" + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $root -Force | Out-Null
    try {
        $passPath = Join-Path $root 'pass.jsonl'
        Write-Utf8NoBom $passPath ((@(
            '{"Action":"start","Package":"example/pass"}',
            '{"Action":"run","Package":"example/pass","Test":"TestOK"}',
            '{"Action":"pass","Package":"example/pass","Test":"TestOK","Elapsed":0.01}',
            '{"Action":"pass","Package":"example/pass","Elapsed":0.02}'
        ) -join "`n") + "`n")
        $pass = Read-GoTestTranscript $passPath $true @()
        Assert-SelfTest ($pass.verdict -eq 'PASS') 'passing transcript was rejected'
        Assert-SelfTest (-not [System.IO.Path]::IsPathRooted([string]$pass.input_path)) 'parser summary exposes a host-specific input path'

        $skipPath = Join-Path $root 'skip.jsonl'
        Write-Utf8NoBom $skipPath ((@(
            '{"Action":"start","Package":"example/skip"}',
            '{"Action":"run","Package":"example/skip","Test":"TestNeedsDB"}',
            '{"Action":"output","Package":"example/skip","Test":"TestNeedsDB","Output":"--- SKIP: TestNeedsDB (DATABASE_DSN not set)\\n"}',
            '{"Action":"skip","Package":"example/skip","Test":"TestNeedsDB","Elapsed":0}',
            '{"Action":"pass","Package":"example/skip","Elapsed":0.01}'
        ) -join "`n") + "`n")
        $skip = Read-GoTestTranscript $skipPath $true @()
        Assert-SelfTest ($skip.verdict -eq 'FAIL' -and $skip.counts.unexpected_skips -eq 1) 'unexpected skip did not fail'
        $allowed = Read-GoTestTranscript $skipPath $true @('example/skip/TestNeedsDB')
        Assert-SelfTest ($allowed.verdict -eq 'PASS') 'allowlisted skip did not pass'
        $sibling = Read-GoTestTranscript $skipPath $true @('example/skip/TestNeeds')
        Assert-SelfTest ($sibling.verdict -eq 'FAIL' -and $sibling.counts.unexpected_skips -eq 1) 'sibling-prefix allowlist overmatched the skipped test'
        $outputOnly = Read-GoTestTranscript $skipPath $true @('DATABASE_DSN not set')
        Assert-SelfTest ($outputOnly.verdict -eq 'FAIL' -and $outputOnly.counts.unexpected_skips -eq 1) 'skip output was accepted as an identity allowlist'
        foreach ($broadIdentity in @('', '.*', '^.*$', 'example/skip/Test*')) {
            $broad = Read-GoTestTranscript $skipPath $true @($broadIdentity)
            Assert-SelfTest ($broad.verdict -eq 'FAIL' -and @($broad.errors | Where-Object { $_ -match 'invalid allowed-skip identity' }).Count -eq 1) "broad allowlist '$broadIdentity' was accepted"
        }
        $duplicate = Read-GoTestTranscript $skipPath $true @('example/skip/TestNeedsDB', 'example/skip/TestNeedsDB')
        Assert-SelfTest ($duplicate.verdict -eq 'FAIL' -and @($duplicate.errors | Where-Object { $_ -match 'duplicate allowed-skip identity' }).Count -eq 1) 'duplicate allowlist identity was accepted'

        $noTestsPath = Join-Path $root 'no-tests.jsonl'
        Write-Utf8NoBom $noTestsPath ((@(
            '{"Action":"start","Package":"example/no-tests"}',
            '{"Action":"output","Package":"example/no-tests","Output":"?   example/no-tests [no test files]\\n"}',
            '{"Action":"skip","Package":"example/no-tests","Elapsed":0}'
        ) -join "`n") + "`n")
        $noTests = Read-GoTestTranscript $noTestsPath $true @()
        Assert-SelfTest ($noTests.verdict -eq 'FAIL' -and $noTests.counts.no_tests -eq 1) 'zero-test transcript did not fail closed'

        $malformedPath = Join-Path $root 'malformed.jsonl'
        Write-Utf8NoBom $malformedPath "not-json`n"
        $malformed = Read-GoTestTranscript $malformedPath $true @()
        Assert-SelfTest ($malformed.verdict -eq 'FAIL' -and $malformed.counts.malformed_lines -eq 1) 'malformed transcript was accepted'
        Write-Output 'SELFTEST PASS: assert-go-test-json.ps1'
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

if ($Help) { Show-Help; exit 0 }
if ($SelfTest) { Invoke-SelfTest; exit 0 }
if ([string]::IsNullOrWhiteSpace($InputPath)) { Write-Error '-InputPath is required.'; exit 1 }
if ([string]::IsNullOrWhiteSpace($SummaryPath)) { $SummaryPath = "$InputPath.summary.json" }

try {
    $summary = Read-GoTestTranscript $InputPath ([bool]$FailOnUnexpectedSkip) $AllowedSkipIdentity
    Write-Utf8NoBom $SummaryPath (($summary | ConvertTo-Json -Depth 12) + "`n")
    Write-Output ("go test JSON verdict={0} packages={1} tests={2} passed={3} failed={4} skipped={5} unexpected_skips={6} malformed={7}" -f $summary.verdict, $summary.counts.packages, $summary.counts.tests, $summary.counts.passed, $summary.counts.failed, $summary.counts.skipped, $summary.counts.unexpected_skips, $summary.counts.malformed_lines)
    Write-Output "summary=$([System.IO.Path]::GetFullPath($SummaryPath))"
    if ($summary.verdict -ne 'PASS') { exit 1 }
    exit 0
}
catch { Write-Error $_; exit 1 }
