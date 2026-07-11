[CmdletBinding()]
param(
    [string]$Repository = '.',
    [string]$Ref = 'HEAD',
    [ValidateRange(1, 240)]
    [int]$CheckoutPrefixLength = 66,
    [ValidateRange(32, 259)]
    [int]$MaximumCombinedPathLength = 240,
    [string]$Artifact = '.agent/e/rg4/path-budget.json',
    [switch]$SelfTest,
    [switch]$Help
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Show-Help {
    @'
assert-windows-path-budget.ps1

Fails when any path tracked at Ref would exceed the declared ordinary-Windows
checkout budget once placed below a checkout root whose absolute path has the
declared prefix length. The default 240-character ceiling intentionally leaves
headroom below legacy MAX_PATH. This gate never changes global or repository Git
configuration; a separate fresh-checkout proof must still run with
core.longpaths unset/false.

Usage:
  pwsh ./scripts/production-gates/assert-windows-path-budget.ps1 `
    -Repository . -Ref HEAD -CheckoutPrefixLength 66 `
    -MaximumCombinedPathLength 240 -Artifact .agent/e/rg4/path-budget.json

  pwsh ./scripts/production-gates/assert-windows-path-budget.ps1 -SelfTest
'@ | Write-Output
}

function Write-Utf8NoBom {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][AllowEmptyString()][string]$Content
    )

    $parent = Split-Path -Parent $Path
    if ($parent) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }
    [System.IO.File]::WriteAllText(
        [System.IO.Path]::GetFullPath($Path),
        $Content,
        [System.Text.UTF8Encoding]::new($false)
    )
}

function Measure-TrackedPathBudget {
    param(
        [Parameter(Mandatory)][AllowEmptyCollection()][string[]]$Paths,
        [Parameter(Mandatory)][int]$PrefixLength,
        [Parameter(Mandatory)][int]$MaximumLength
    )

    $entries = [System.Collections.Generic.List[object]]::new()
    $errors = [System.Collections.Generic.List[string]]::new()
    foreach ($rawPath in $Paths) {
        $path = ([string]$rawPath).Replace('\', '/')
        if ([string]::IsNullOrWhiteSpace($path)) {
            $errors.Add('tracked path list contains an empty path')
            continue
        }
        if ($path.StartsWith('/') -or $path -match '^[A-Za-z]:' -or $path -match '(^|/)\.\.(?:/|$)') {
            $errors.Add("tracked path is not repository-relative: '$path'")
            continue
        }
        $combinedLength = $PrefixLength + 1 + $path.Length
        $entries.Add([pscustomobject][ordered]@{
            path = $path
            relative_length = $path.Length
            combined_length = $combinedLength
            within_budget = $combinedLength -le $MaximumLength
        })
    }

    $ordered = @($entries | Sort-Object @{ Expression = 'combined_length'; Descending = $true }, @{ Expression = 'path'; Descending = $false })
    $violations = @($ordered | Where-Object { -not $_.within_budget })
    foreach ($violation in $violations) {
        $errors.Add("tracked path exceeds Windows checkout budget ($($violation.combined_length) > $MaximumLength): '$($violation.path)'")
    }
    return [pscustomobject][ordered]@{
        verdict = if ($errors.Count -eq 0) { 'PASS' } else { 'FAIL' }
        prefix_length = $PrefixLength
        maximum_combined_length = $MaximumLength
        longest_combined_length = if ($ordered.Count -gt 0) { $ordered[0].combined_length } else { $null }
        path_count = $ordered.Count
        violation_count = $violations.Count
        longest_paths = @($ordered | Select-Object -First 20)
        violations = $violations
        errors = @($errors)
    }
}

function Assert-SelfTestCondition {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw "SELFTEST FAIL: $Message" }
}

function Invoke-SelfTest {
    $boundaryPath = 'a' * 173
    $overPath = 'b' * 174
    $boundary = Measure-TrackedPathBudget -Paths @($boundaryPath) -PrefixLength 66 -MaximumLength 240
    $over = Measure-TrackedPathBudget -Paths @($overPath) -PrefixLength 66 -MaximumLength 240
    Assert-SelfTestCondition ($boundary.verdict -eq 'PASS' -and $boundary.longest_combined_length -eq 240) 'exact path-budget boundary was rejected'
    Assert-SelfTestCondition ($over.verdict -eq 'FAIL' -and $over.longest_combined_length -eq 241 -and $over.violation_count -eq 1) 'over-budget path was accepted'
    Assert-SelfTestCondition ((Measure-TrackedPathBudget -Paths @('../escape') -PrefixLength 66 -MaximumLength 240).verdict -eq 'FAIL') 'path traversal was accepted'
    Write-Output 'SELFTEST PASS: assert-windows-path-budget.ps1'
}

if ($Help) { Show-Help; exit 0 }
if ($SelfTest) { Invoke-SelfTest; exit 0 }

$startedAt = [DateTimeOffset]::UtcNow
$artifactObject = $null
$exitCode = 1
try {
    $repoRootOutput = @(& git -C $Repository rev-parse --show-toplevel 2>&1)
    if ($LASTEXITCODE -ne 0) { throw "repository is not a Git worktree: $($repoRootOutput -join ' ')" }
    $repoRoot = ([string]$repoRootOutput[-1]).Trim()

    $commitOutput = @(& git -C $repoRoot rev-parse --verify "$Ref^{commit}" 2>&1)
    if ($LASTEXITCODE -ne 0) { throw "ref '$Ref' is not a commit: $($commitOutput -join ' ')" }
    $commit = ([string]$commitOutput[-1]).Trim().ToLowerInvariant()
    if ($commit -notmatch '^[0-9a-f]{40}$') { throw "ref '$Ref' resolved to invalid commit '$commit'" }

    $rawPaths = @(& git -c core.quotepath=false -C $repoRoot ls-tree -r --name-only --full-tree $commit -- 2>&1)
    if ($LASTEXITCODE -ne 0) { throw "git ls-tree failed: $($rawPaths -join ' ')" }
    $paths = @($rawPaths | ForEach-Object { [string]$_ })
    if ($paths.Count -eq 0) { throw "ref '$commit' contains no tracked paths" }

    $measurement = Measure-TrackedPathBudget -Paths $paths -PrefixLength $CheckoutPrefixLength -MaximumLength $MaximumCombinedPathLength
    $finishedAt = [DateTimeOffset]::UtcNow
    $artifactObject = [ordered]@{
        schema_version = 1
        gate = 'windows-tracked-path-budget'
        verdict = $measurement.verdict
        started_at = $startedAt.ToString('O')
        finished_at = $finishedAt.ToString('O')
        duration_seconds = [math]::Round(($finishedAt - $startedAt).TotalSeconds, 3)
        repository = $repoRoot
        requested_ref = $Ref
        commit = $commit
        checkout_prefix_length = $CheckoutPrefixLength
        maximum_combined_path_length = $MaximumCombinedPathLength
        longest_combined_path_length = $measurement.longest_combined_length
        path_count = $measurement.path_count
        violation_count = $measurement.violation_count
        longest_paths = $measurement.longest_paths
        violations = $measurement.violations
        errors = $measurement.errors
    }
    $exitCode = if ($measurement.verdict -eq 'PASS') { 0 } else { 1 }
}
catch {
    $finishedAt = [DateTimeOffset]::UtcNow
    $artifactObject = [ordered]@{
        schema_version = 1
        gate = 'windows-tracked-path-budget'
        verdict = 'FAIL'
        started_at = $startedAt.ToString('O')
        finished_at = $finishedAt.ToString('O')
        duration_seconds = [math]::Round(($finishedAt - $startedAt).TotalSeconds, 3)
        repository = $Repository
        requested_ref = $Ref
        path_count = $null
        longest_combined_path_length = $null
        maximum_combined_path_length = $MaximumCombinedPathLength
        violation_count = $null
        errors = @($_.Exception.Message)
    }
    $exitCode = 1
}

Write-Utf8NoBom -Path $Artifact -Content (($artifactObject | ConvertTo-Json -Depth 12) + "`n")
Write-Host ("windows-path-budget verdict={0} paths={1} longest={2} ceiling={3} violations={4}" -f $artifactObject.verdict, $artifactObject.path_count, $artifactObject.longest_combined_path_length, $artifactObject.maximum_combined_path_length, $artifactObject.violation_count)
exit $exitCode
