[CmdletBinding()]
param(
    [string]$ArtifactRoot = ".agent/reports/evidence/production-ready/observability"
)

# Prerequisites: go (>= 1.21) must be on PATH.
# Optional: a live OTLP collector at OTEL_EXPORTER_OTLP_METRICS_ENDPOINT.
# If go is absent this script exits with a clear error message.
# If the collector is absent the suite still passes (tests spin up in-process receivers).

$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "../..")).Path
$artifactPath = if ([System.IO.Path]::IsPathRooted($ArtifactRoot)) {
    $ArtifactRoot
} else {
    Join-Path $repoRoot $ArtifactRoot
}

# Check that go is available.
if (-not (Get-Command "go" -ErrorAction SilentlyContinue)) {
    Write-Error "MISSING PREREQUISITE: 'go' executable not found on PATH. Install Go >= 1.21 to run this smoke gate."
    exit 1
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
        "TestOTLPTLSWithExplicitTrustRoot",
        "TestCollectorAuthFailureIsBoundedAndSecretFree",
        "TestCollectorBackpressureHonorsDeadline",
        "TestTransientCollectorFailureRetriesWithinCallerDeadline",
        "TestExporterOutageAndTLSMismatchAreBounded",
        "TestShutdownFlushesPendingMetric",
        "TestEndpointCredentialsAreRejectedWithoutEcho",
        "TestConcurrentInitRecordShutdown",
        "TestRuntimeOwnershipIsIdempotentAndReinitializable",
        "TestRepeatedLifecycleWhileRecording",
        "TestMeterFor"
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
