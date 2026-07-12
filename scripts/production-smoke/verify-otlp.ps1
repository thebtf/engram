[CmdletBinding()]
param(
    [string]$ArtifactRoot = ".agent/reports/evidence/production-ready/observability"
)

$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "../..")).Path
$artifactPath = if ([System.IO.Path]::IsPathRooted($ArtifactRoot)) {
    $ArtifactRoot
} else {
    Join-Path $repoRoot $ArtifactRoot
}

New-Item -ItemType Directory -Force -Path $artifactPath | Out-Null
$jsonLog = Join-Path $artifactPath "go-test.jsonl"
$summaryPath = Join-Path $artifactPath "summary.json"
$startedAt = [DateTimeOffset]::UtcNow

Push-Location $repoRoot
try {
    $output = & go test ./internal/module/obs -count=1 -json 2>&1
    $exitCode = $LASTEXITCODE
    $output | Set-Content -Encoding utf8 $jsonLog

    $events = @($output | ForEach-Object {
        try { $_ | ConvertFrom-Json -ErrorAction Stop } catch { $null }
    } | Where-Object { $null -ne $_ })
    $failedTests = @($events | Where-Object { $_.Action -eq "fail" -and $_.Test } | Select-Object -ExpandProperty Test -Unique)
    $required = @(
        "TestInitNoEndpointIsNoop",
        "TestOTLPExportsStableMetricsAndKeepsHeaderOutOfPayload",
        "TestCollectorAuthFailureIsBoundedAndSecretFree",
        "TestCollectorBackpressureHonorsDeadline",
        "TestTransientCollectorFailureRetriesWithinCallerDeadline",
        "TestExporterOutageAndTLSMismatchAreBounded",
        "TestShutdownFlushesPendingMetric",
        "TestEndpointCredentialsAreRejectedWithoutEcho"
    )
    $passedTests = @($events | Where-Object { $_.Action -eq "pass" -and $_.Test } | Select-Object -ExpandProperty Test -Unique)
    $missing = @($required | Where-Object { $_ -notin $passedTests })
    $result = [ordered]@{
        schema_version = 1
        gate = "observability-otlp"
        started_at_utc = $startedAt.ToString("o")
        completed_at_utc = [DateTimeOffset]::UtcNow.ToString("o")
        command = "go test ./internal/module/obs -count=1 -json"
        exit_code = $exitCode
        required_tests = $required
        missing_tests = $missing
        failed_tests = $failedTests
        process_residue = @()
        container_residue = @()
        verdict = if ($exitCode -eq 0 -and $missing.Count -eq 0 -and $failedTests.Count -eq 0) { "PASS" } else { "FAIL" }
    }
    $result | ConvertTo-Json -Depth 5 | Set-Content -Encoding utf8 $summaryPath
    if ($result.verdict -ne "PASS") {
        throw "OTLP verification failed; see $summaryPath"
    }
    Write-Output $summaryPath
} finally {
    Pop-Location
}
