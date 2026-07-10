[CmdletBinding()]
param(
    [string]$CoverageProfile,
    [string]$SummaryPath,
    [double]$OverallThreshold = 60.0,
    [switch]$Help,
    [switch]$SelfTest
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$RequiredPackageThresholds = [ordered]@{
    'internal/module/'             = 75.0
    'internal/handlers/engramcore' = 60.0
    'internal/handlers/loom'       = 70.0
    'cmd/engram/'                  = 0.0
}

function Show-Help {
    @'
assert-coverage.ps1

Validates a Go coverage profile with statement-weighted accounting. Missing or
empty coverage is fatal on every operating system. The release floor is 60%
overall statement coverage, and the historical package gates remain mandatory:

  internal/module/              >= 75%
  internal/handlers/engramcore  >= 60%
  internal/handlers/loom        >= 70%
  cmd/engram/                   >= 0% (presence is still required)

Usage:
  pwsh ./scripts/production-gates/assert-coverage.ps1 \
    -CoverageProfile <coverage.out> \
    -SummaryPath <coverage-summary.json> \
    [-OverallThreshold 60]

Options:
  -Help                Print this help and exit 0.
  -SelfTest            Run deterministic coverage-parser regression tests.
  -CoverageProfile     Go `-coverprofile` file.
  -SummaryPath         Machine-readable result path. Defaults beside profile.
  -OverallThreshold    May raise, but never lower, the mandatory 60% floor.

Exit codes:
  0  Profile exists and all overall/package thresholds pass.
  1  Missing/malformed coverage, missing required package data, or low coverage.
'@ | Write-Output
}

function Write-Utf8NoBom {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][AllowEmptyString()][string]$Content)
    $parent = Split-Path -Parent $Path
    if ($parent) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }
    [System.IO.File]::WriteAllText([System.IO.Path]::GetFullPath($Path), $Content, [System.Text.UTF8Encoding]::new($false))
}

function Get-CoverageSummary {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][double]$MinimumOverall)

    $errors = [System.Collections.Generic.List[string]]::new()
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        $errors.Add("coverage profile does not exist: $Path")
        return [pscustomobject]@{
            schema_version = 1; verdict = 'FAIL'; profile = [System.IO.Path]::GetFullPath($Path); mode = $null
            overall = [ordered]@{ covered_statements = 0; total_statements = 0; percent = 0.0; threshold = $MinimumOverall; pass = $false }
            required_packages = @(); packages = @(); errors = @($errors)
        }
    }

    $lines = [System.IO.File]::ReadAllLines([System.IO.Path]::GetFullPath($Path))
    if ($lines.Count -lt 2 -or -not $lines[0].StartsWith('mode: ')) { $errors.Add('coverage profile is empty or has no valid mode header') }
    $mode = if ($lines.Count -gt 0 -and $lines[0].StartsWith('mode: ')) { $lines[0].Substring(6).Trim() } else { $null }
    $packageStats = @{}
    [int64]$overallTotal = 0
    [int64]$overallCovered = 0

    for ($index = 1; $index -lt $lines.Count; $index++) {
        $line = $lines[$index]
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        $match = [regex]::Match($line, '^(?<file>.+):\d+\.\d+,\d+\.\d+\s+(?<statements>\d+)\s+(?<count>\d+)$')
        if (-not $match.Success) { $errors.Add("malformed coverage line $($index + 1): $line"); continue }
        $file = $match.Groups['file'].Value.Replace('\\', '/')
        $lastSlash = $file.LastIndexOf('/')
        $package = if ($lastSlash -ge 0) { $file.Substring(0, $lastSlash + 1) } else { '' }
        $statements = [int64]$match.Groups['statements'].Value
        $count = [int64]$match.Groups['count'].Value
        if (-not $packageStats.ContainsKey($package)) { $packageStats[$package] = [ordered]@{ package = $package; total = [int64]0; covered = [int64]0 } }
        $packageStats[$package].total += $statements
        $overallTotal += $statements
        if ($count -gt 0) { $packageStats[$package].covered += $statements; $overallCovered += $statements }
    }
    if ($overallTotal -le 0) { $errors.Add('coverage profile contains zero statements') }

    $packages = @($packageStats.Values | ForEach-Object {
        $percent = if ($_.total -gt 0) { [math]::Round(($_.covered / $_.total) * 100.0, 2) } else { 0.0 }
        [pscustomobject]@{ package = $_.package; covered_statements = $_.covered; total_statements = $_.total; percent = $percent }
    } | Sort-Object package)

    $required = [System.Collections.Generic.List[object]]::new()
    foreach ($entry in $RequiredPackageThresholds.GetEnumerator()) {
        $fullPrefix = "github.com/thebtf/engram/$($entry.Key.TrimEnd('/'))/"
        [int64]$total = 0; [int64]$covered = 0
        foreach ($pkg in $packages) {
            if ($pkg.package.StartsWith($fullPrefix, [System.StringComparison]::Ordinal)) { $total += [int64]$pkg.total_statements; $covered += [int64]$pkg.covered_statements }
        }
        $present = $total -gt 0
        $exactPercent = if ($present) { ($covered / $total) * 100.0 } else { 0.0 }
        $percent = [math]::Round($exactPercent, 2)
        $pass = $present -and $exactPercent -ge [double]$entry.Value
        if (-not $present) { $errors.Add("required package coverage is missing: $($entry.Key)") }
        elseif (-not $pass) { $errors.Add(("package coverage below threshold: {0} {1:N2}% < {2:N2}%" -f $entry.Key, $percent, [double]$entry.Value)) }
        $required.Add([pscustomobject]@{ package_prefix = $entry.Key; covered_statements = $covered; total_statements = $total; percent = $percent; threshold = [double]$entry.Value; present = $present; pass = $pass })
    }

    $overallExactPercent = if ($overallTotal -gt 0) { ($overallCovered / $overallTotal) * 100.0 } else { 0.0 }
    $overallPercent = [math]::Round($overallExactPercent, 2)
    $overallPass = $overallTotal -gt 0 -and $overallExactPercent -ge $MinimumOverall
    if ($overallTotal -gt 0 -and -not $overallPass) { $errors.Add(("overall statement coverage below threshold: {0:N2}% < {1:N2}%" -f $overallPercent, $MinimumOverall)) }

    [pscustomobject]@{
        schema_version = 1; verdict = if ($errors.Count -eq 0) { 'PASS' } else { 'FAIL' }
        profile = [System.IO.Path]::GetFullPath($Path); mode = $mode
        overall = [ordered]@{ covered_statements = $overallCovered; total_statements = $overallTotal; percent = $overallPercent; threshold = $MinimumOverall; pass = $overallPass }
        required_packages = @($required); packages = $packages; errors = @($errors)
    }
}

function Assert-SelfTest { param([bool]$Condition, [string]$Message); if (-not $Condition) { throw "SELFTEST FAIL: $Message" } }

function New-SyntheticProfile {
    param([Parameter(Mandatory)][string]$Path, [int]$EngramCoreCovered = 6, [int]$OtherCovered = 10, [switch]$OmitLoom)
    $lines = [System.Collections.Generic.List[string]]::new()
    $lines.Add('mode: set')
    $lines.Add('github.com/thebtf/engram/internal/module/a.go:1.1,2.1 10 1')
    $lines.Add("github.com/thebtf/engram/internal/handlers/engramcore/a.go:1.1,2.1 $EngramCoreCovered 1")
    if ($EngramCoreCovered -lt 10) { $lines.Add("github.com/thebtf/engram/internal/handlers/engramcore/b.go:1.1,2.1 $(10 - $EngramCoreCovered) 0") }
    if (-not $OmitLoom) {
        $lines.Add('github.com/thebtf/engram/internal/handlers/loom/a.go:1.1,2.1 7 1')
        $lines.Add('github.com/thebtf/engram/internal/handlers/loom/b.go:1.1,2.1 3 0')
    }
    $lines.Add('github.com/thebtf/engram/cmd/engram/main.go:1.1,2.1 10 0')
    if ($OtherCovered -gt 0) { $lines.Add("github.com/thebtf/engram/internal/other/a.go:1.1,2.1 $OtherCovered 1") }
    if ($OtherCovered -lt 10) { $lines.Add("github.com/thebtf/engram/internal/other/b.go:1.1,2.1 $(10 - $OtherCovered) 0") }
    Write-Utf8NoBom $Path (($lines -join "`n") + "`n")
}

function Invoke-SelfTest {
    $root = Join-Path ([System.IO.Path]::GetTempPath()) ("assert-coverage-" + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $root -Force | Out-Null
    try {
        $passPath = Join-Path $root 'pass.out'; New-SyntheticProfile $passPath
        $pass = Get-CoverageSummary $passPath 60
        Assert-SelfTest ($pass.verdict -eq 'PASS') 'valid profile was rejected'
        $lowOverallPath = Join-Path $root 'low-overall.out'; New-SyntheticProfile $lowOverallPath -OtherCovered 0
        $lowOverall = Get-CoverageSummary $lowOverallPath 60
        Assert-SelfTest ($lowOverall.verdict -eq 'FAIL' -and -not $lowOverall.overall.pass) 'low overall coverage was accepted'
        $lowPackagePath = Join-Path $root 'low-package.out'; New-SyntheticProfile $lowPackagePath -EngramCoreCovered 5
        Assert-SelfTest ((Get-CoverageSummary $lowPackagePath 60).verdict -eq 'FAIL') 'low package coverage was accepted'

        $prefixBoundaryPath = Join-Path $root 'prefix-boundary.out'
        Write-Utf8NoBom $prefixBoundaryPath ((@(
            'mode: set'
            'github.com/thebtf/engram/internal/module/a.go:1.1,2.1 10 1'
            'github.com/thebtf/engram/internal/handlers/engramcore/a.go:1.1,2.1 6 1'
            'github.com/thebtf/engram/internal/handlers/engramcore/b.go:1.1,2.1 4 0'
            'github.com/thebtf/engram/internal/handlers/loom/a.go:1.1,2.1 6 1'
            'github.com/thebtf/engram/internal/handlers/loom/b.go:1.1,2.1 4 0'
            'github.com/thebtf/engram/internal/handlers/looming/decoy.go:1.1,2.1 100000 1'
            'github.com/thebtf/engram/cmd/engram/main.go:1.1,2.1 10 0'
        ) -join "`n") + "`n")
        $prefixBoundary = Get-CoverageSummary $prefixBoundaryPath 60
        $boundedLoom = $prefixBoundary.required_packages | Where-Object package_prefix -eq 'internal/handlers/loom'
        Assert-SelfTest ($prefixBoundary.verdict -eq 'FAIL' -and $boundedLoom.percent -eq 60.0 -and -not $boundedLoom.pass) 'neighbor package prefix boosted the loom threshold'

        $missingPath = Join-Path $root 'missing-package.out'; New-SyntheticProfile $missingPath -OmitLoom
        $missing = Get-CoverageSummary $missingPath 60
        Assert-SelfTest ($missing.verdict -eq 'FAIL' -and @($missing.errors | Where-Object { $_ -match 'internal/handlers/loom' }).Count -eq 1) 'missing package was accepted'

        # 59.999% renders as 60.00% at two decimals but is still below the
        # release floor. Gate decisions must use the exact ratio, never the
        # rounded display value.
        $roundingOverallPath = Join-Path $root 'rounding-overall.out'
        Write-Utf8NoBom $roundingOverallPath ((@(
            'mode: set'
            'github.com/thebtf/engram/internal/module/a.go:1.1,2.1 10 1'
            'github.com/thebtf/engram/internal/handlers/engramcore/a.go:1.1,2.1 6 1'
            'github.com/thebtf/engram/internal/handlers/engramcore/b.go:1.1,2.1 4 0'
            'github.com/thebtf/engram/internal/handlers/loom/a.go:1.1,2.1 7 1'
            'github.com/thebtf/engram/internal/handlers/loom/b.go:1.1,2.1 3 0'
            'github.com/thebtf/engram/cmd/engram/main.go:1.1,2.1 10 0'
            'github.com/thebtf/engram/internal/other/a.go:1.1,2.1 59976 1'
            'github.com/thebtf/engram/internal/other/b.go:1.1,2.1 39984 0'
        ) -join "`n") + "`n")
        $roundingOverall = Get-CoverageSummary $roundingOverallPath 60
        Assert-SelfTest ($roundingOverall.verdict -eq 'FAIL' -and $roundingOverall.overall.percent -eq 60.0 -and -not $roundingOverall.overall.pass) 'rounded 59.999% overall coverage was accepted as 60%'

        # The same exact-ratio rule applies to package floors: 69.999% loom
        # coverage displays as 70.00% but must not satisfy the 70% contract.
        $roundingPackagePath = Join-Path $root 'rounding-package.out'
        Write-Utf8NoBom $roundingPackagePath ((@(
            'mode: set'
            'github.com/thebtf/engram/internal/module/a.go:1.1,2.1 10 1'
            'github.com/thebtf/engram/internal/handlers/engramcore/a.go:1.1,2.1 6 1'
            'github.com/thebtf/engram/internal/handlers/engramcore/b.go:1.1,2.1 4 0'
            'github.com/thebtf/engram/internal/handlers/loom/a.go:1.1,2.1 69999 1'
            'github.com/thebtf/engram/internal/handlers/loom/b.go:1.1,2.1 30001 0'
            'github.com/thebtf/engram/cmd/engram/main.go:1.1,2.1 10 0'
            'github.com/thebtf/engram/internal/other/a.go:1.1,2.1 100000 1'
        ) -join "`n") + "`n")
        $roundingPackage = Get-CoverageSummary $roundingPackagePath 60
        $loomPackage = $roundingPackage.required_packages | Where-Object package_prefix -eq 'internal/handlers/loom'
        Assert-SelfTest ($roundingPackage.verdict -eq 'FAIL' -and $loomPackage.percent -eq 70.0 -and -not $loomPackage.pass) 'rounded 69.999% package coverage was accepted as 70%'
        Assert-SelfTest ((Get-CoverageSummary (Join-Path $root 'absent.out') 60).verdict -eq 'FAIL') 'absent profile was accepted'
        Write-Output 'SELFTEST PASS: assert-coverage.ps1'
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

if ($Help) { Show-Help; exit 0 }
if ($SelfTest) { Invoke-SelfTest; exit 0 }
if ([string]::IsNullOrWhiteSpace($CoverageProfile)) { Write-Error '-CoverageProfile is required.'; exit 1 }
if ($OverallThreshold -lt 60.0) { Write-Error '-OverallThreshold cannot lower the mandatory 60% release floor.'; exit 1 }
if ([string]::IsNullOrWhiteSpace($SummaryPath)) { $SummaryPath = "$CoverageProfile.summary.json" }

try {
    $summary = Get-CoverageSummary $CoverageProfile $OverallThreshold
    Write-Utf8NoBom $SummaryPath (($summary | ConvertTo-Json -Depth 10) + "`n")
    Write-Output ("coverage verdict={0} overall={1:N2}% threshold={2:N2}% statements={3}/{4}" -f $summary.verdict, $summary.overall.percent, $summary.overall.threshold, $summary.overall.covered_statements, $summary.overall.total_statements)
    foreach ($package in $summary.required_packages) {
        Write-Output ("coverage package={0} percent={1:N2}% threshold={2:N2}% present={3} pass={4}" -f $package.package_prefix, $package.percent, $package.threshold, $package.present, $package.pass)
    }
    Write-Output "summary=$([System.IO.Path]::GetFullPath($SummaryPath))"
    if ($summary.verdict -ne 'PASS') { exit 1 }
    exit 0
}
catch { Write-Error $_; exit 1 }
