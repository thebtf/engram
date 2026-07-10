param(
    [string]$Repository = (Resolve-Path (Join-Path $PSScriptRoot '..\..\..\..\..')).Path,
    [string]$Label = 'current',
    [switch]$DisableLivePackageBinding
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Assert-Condition {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw $Message }
}

function Write-Utf8NoBom {
    param([string]$Path, [string]$Content)
    [System.IO.File]::WriteAllText([System.IO.Path]::GetFullPath($Path), $Content, [System.Text.UTF8Encoding]::new($false))
}

function Get-WorkflowConformanceScript {
    param([string]$WorkflowPath)

    $lines = [System.IO.File]::ReadAllLines($WorkflowPath)
    $stepIndex = [Array]::IndexOf($lines, '      - name: Assert tracked gate / CI conformance')
    Assert-Condition ($stepIndex -ge 0) 'workflow conformance step is missing'

    $runIndex = -1
    for ($index = $stepIndex + 1; $index -lt $lines.Count; $index++) {
        if ($lines[$index] -ceq '        run: |') { $runIndex = $index; break }
        if ($lines[$index] -match '^      - name: ') { break }
    }
    Assert-Condition ($runIndex -ge 0) 'workflow conformance run block is missing'

    $body = [System.Collections.Generic.List[string]]::new()
    for ($index = $runIndex + 1; $index -lt $lines.Count; $index++) {
        if ($lines[$index] -match '^      - name: ') { break }
        $line = $lines[$index]
        Assert-Condition ($line.Length -eq 0 -or $line.StartsWith('          ', [System.StringComparison]::Ordinal)) "workflow conformance line is not an exact YAML block at line $($index + 1)"
        $body.Add($(if ($line.Length -eq 0) { '' } else { $line.Substring(10) }))
    }
    return (($body -join "`n") + "`n")
}

$repositoryPath = [System.IO.Path]::GetFullPath($Repository)
$evidenceDirectory = [System.IO.Path]::GetFullPath($PSScriptRoot)
$workflowPath = Join-Path $repositoryPath '.github/workflows/test.yml'
$runnerPath = Join-Path $repositoryPath 'scripts/production-gates/run-db-suite.ps1'
$scriptPath = Join-Path $evidenceDirectory "$Label.workflow-conformance.ps1"
$stdoutPath = Join-Path $evidenceDirectory "$Label.workflow-conformance.stdout.log"
$stderrPath = Join-Path $evidenceDirectory "$Label.workflow-conformance.stderr.log"
$resultPath = Join-Path $evidenceDirectory "$Label.workflow-conformance.json"

$workflowSourcePath = $workflowPath
if ($DisableLivePackageBinding) {
    $workflowText = Get-Content -Raw -LiteralPath $workflowPath
    $enforcement = "throw 'required session-start live consumer does not bind exact case-sensitive package plus test identity at the point of consumption'"
    $enforcementCount = ([regex]::Matches($workflowText, [regex]::Escape($enforcement))).Count
    Assert-Condition ($enforcementCount -eq 1) "live package-binding enforcement fixture cardinality is $enforcementCount, expected 1"
    $workflowSourcePath = Join-Path $evidenceDirectory "$Label.workflow-under-test.yml"
    Write-Utf8NoBom $workflowSourcePath ($workflowText.Replace($enforcement, '$null = $liveMatchesAssignment'))
}

Write-Utf8NoBom $scriptPath (Get-WorkflowConformanceScript $workflowSourcePath)
$previousRunnerTemp = $env:RUNNER_TEMP
$env:RUNNER_TEMP = Join-Path $evidenceDirectory "$Label.runner-temp"
New-Item -ItemType Directory -Path $env:RUNNER_TEMP -Force | Out-Null
try {
    Push-Location $repositoryPath
    try {
        & pwsh -NoProfile -File $scriptPath 1> $stdoutPath 2> $stderrPath
        $exitCode = $LASTEXITCODE
    }
    finally { Pop-Location }
}
finally { $env:RUNNER_TEMP = $previousRunnerTemp }

$result = [pscustomobject][ordered]@{
    schema_version = 1
    label = $Label
    live_package_binding_disabled = [bool]$DisableLivePackageBinding
    observed_at = [DateTimeOffset]::UtcNow.ToString('O')
    exit_code = $exitCode
    workflow_sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $workflowPath).Hash.ToLowerInvariant()
    runner_sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $runnerPath).Hash.ToLowerInvariant()
    extracted_script_sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $scriptPath).Hash.ToLowerInvariant()
    stdout_tail = @((Get-Content -LiteralPath $stdoutPath -ErrorAction SilentlyContinue | Select-Object -Last 8))
    stderr_tail = @((Get-Content -LiteralPath $stderrPath -ErrorAction SilentlyContinue | Select-Object -Last 8))
}
Write-Utf8NoBom $resultPath (($result | ConvertTo-Json -Depth 5) + "`n")
$result | ConvertTo-Json -Depth 5
exit $exitCode
