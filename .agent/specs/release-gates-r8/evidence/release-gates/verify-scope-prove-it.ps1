param(
    [string]$Repository = (Resolve-Path (Join-Path $PSScriptRoot '..\..\..\..\..')).Path,
    [string]$Label = 'scope-prove-it'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Write-Utf8NoBom {
    param([string]$Path, [string]$Content)
    [System.IO.File]::WriteAllText([System.IO.Path]::GetFullPath($Path), $Content, [System.Text.UTF8Encoding]::new($false))
}

$repositoryPath = [System.IO.Path]::GetFullPath($Repository)
$sourcePath = Join-Path $repositoryPath 'scripts/production-gates/assert-plan-path-ownership.ps1'
$mutatedPath = Join-Path $PSScriptRoot "$Label.assert-plan-path-ownership.ps1"
$stdoutPath = Join-Path $PSScriptRoot "$Label.stdout.log"
$stderrPath = Join-Path $PSScriptRoot "$Label.stderr.log"
$resultPath = Join-Path $PSScriptRoot "$Label.json"

$source = Get-Content -Raw -LiteralPath $sourcePath
$enforcement = "`$errors.Add('live register unique slice set differs from the frozen scope map')"
$count = ([regex]::Matches($source, [regex]::Escape($enforcement))).Count
if ($count -ne 1) { throw "scope-set enforcement fixture cardinality is $count, expected 1" }
Write-Utf8NoBom $mutatedPath ($source.Replace($enforcement, "`$null = 'scope-set enforcement disabled for Prove-It'"))

Push-Location $repositoryPath
try {
    & pwsh -NoProfile -File $mutatedPath -SelfTest 1> $stdoutPath 2> $stderrPath
    $exitCode = $LASTEXITCODE
}
finally { Pop-Location }

$result = [pscustomobject][ordered]@{
    schema_version = 1
    label = $Label
    mutation = 'disable live unique-slice-set enforcement'
    observed_at = [DateTimeOffset]::UtcNow.ToString('O')
    exit_code = $exitCode
    source_sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $sourcePath).Hash.ToLowerInvariant()
    mutated_sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $mutatedPath).Hash.ToLowerInvariant()
    stdout_tail = @((Get-Content -LiteralPath $stdoutPath -ErrorAction SilentlyContinue | Select-Object -Last 8))
    stderr_tail = @((Get-Content -LiteralPath $stderrPath -ErrorAction SilentlyContinue | Select-Object -Last 8))
}
Write-Utf8NoBom $resultPath (($result | ConvertTo-Json -Depth 5) + "`n")
$result | ConvertTo-Json -Depth 5
exit $exitCode
