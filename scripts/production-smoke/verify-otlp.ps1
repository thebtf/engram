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

function Get-ResidualProcessSnapshot {
    return @(Get-Process -ErrorAction SilentlyContinue |
        Where-Object { $_.ProcessName -match '^(?:engram|otelcol|obs\.test)(?:\.exe)?$' } |
        Sort-Object Id |
        ForEach-Object {
            $startedAtUtcTicks = try { [long]$_.StartTime.ToUniversalTime().Ticks } catch { [long]0 }
            [pscustomobject][ordered]@{ id = [int]$_.Id; name = [string]$_.ProcessName; started_at_utc_ticks = $startedAtUtcTicks }
        })
}

function Get-ResidualContainerSnapshot {
    if ($null -eq (Get-Command docker -ErrorAction SilentlyContinue)) {
        throw "Docker is required to verify that the OTLP gate leaves no container residue"
    }
    $ids = @(& docker ps -aq 2>&1)
    if ($LASTEXITCODE -ne 0) {
        throw "docker ps failed while measuring OTLP container residue: $($ids -join ' ')"
    }
    return @($ids | Where-Object { $_ -match '^[0-9a-f]{12,64}$' } | Sort-Object -Unique)
}

function Get-NewResidue {
    param(
        [Parameter(Mandatory)][AllowEmptyCollection()][object[]]$Before,
        [Parameter(Mandatory)][AllowEmptyCollection()][object[]]$After,
        [Parameter(Mandatory)][scriptblock]$Identity
    )
    $known = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    foreach ($item in $Before) { [void]$known.Add([string](& $Identity $item)) }
    return @($After | Where-Object { -not $known.Contains([string](& $Identity $_)) })
}

function Wait-ForResidualResources {
    param(
        [Parameter(Mandatory)][AllowEmptyCollection()][object[]]$ProcessesBefore,
        [Parameter(Mandatory)][AllowEmptyCollection()][object[]]$ContainersBefore,
        [ValidateRange(100, 30000)][int]$TimeoutMilliseconds = 5000,
        [ValidateRange(25, 1000)][int]$PollMilliseconds = 200
    )
    $deadline = [DateTimeOffset]::UtcNow.AddMilliseconds($TimeoutMilliseconds)
    $attempts = 0
    do {
        $attempts++
        $processesAfter = @(Get-ResidualProcessSnapshot)
        $containersAfter = @(Get-ResidualContainerSnapshot)
        $processResidue = @(Get-NewResidue -Before $ProcessesBefore -After $processesAfter -Identity { param($item) "$($item.id):$($item.started_at_utc_ticks)" })
        $containerResidue = @(Get-NewResidue -Before $ContainersBefore -After $containersAfter -Identity { param($item) [string]$item })
        if ($processResidue.Count -eq 0 -and $containerResidue.Count -eq 0) {
            return [pscustomobject]@{ Processes = $processResidue; Containers = $containerResidue; Attempts = $attempts; TimedOut = $false; TimeoutMilliseconds = $TimeoutMilliseconds }
        }
        if ([DateTimeOffset]::UtcNow -ge $deadline) {
            return [pscustomobject]@{ Processes = $processResidue; Containers = $containerResidue; Attempts = $attempts; TimedOut = $true; TimeoutMilliseconds = $TimeoutMilliseconds }
        }
        Start-Sleep -Milliseconds $PollMilliseconds
    } while ($true)
}

Push-Location $repoRoot
try {
    $processesBefore = @(Get-ResidualProcessSnapshot)
    $containersBefore = @(Get-ResidualContainerSnapshot)
    $output = & go test ./internal/module/obs -count=1 -json 2>&1
    $exitCode = $LASTEXITCODE
    $output | Set-Content -Encoding utf8 $jsonLog
    $residue = Wait-ForResidualResources -ProcessesBefore $processesBefore -ContainersBefore $containersBefore
    $processResidue = @($residue.Processes)
    $containerResidue = @($residue.Containers)

    $events = @($output | ForEach-Object {
        try { $_ | ConvertFrom-Json -ErrorAction Stop } catch { $null }
    } | Where-Object { $null -ne $_ })
    $failedTests = @($events | Where-Object { $_.Action -eq "fail" -and $_.Test } | Select-Object -ExpandProperty Test -Unique)
    $required = @(
        "TestInitNoEndpointIsNoop",
        "TestInitForService_ExportsDaemonResourceIdentity",
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
        "TestRepeatedLifecycleWhileRecording"
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
        process_residue_checked = $true
        process_residue = $processResidue
        container_residue_checked = $true
        container_residue = $containerResidue
        residue_poll_attempts = [int]$residue.Attempts
        residue_poll_timeout_milliseconds = [int]$residue.TimeoutMilliseconds
        residue_poll_timed_out = [bool]$residue.TimedOut
        verdict = if ($exitCode -eq 0 -and $missing.Count -eq 0 -and $failedTests.Count -eq 0 -and $processResidue.Count -eq 0 -and $containerResidue.Count -eq 0) { "PASS" } else { "FAIL" }
    }
    $result | ConvertTo-Json -Depth 5 | Set-Content -Encoding utf8 $summaryPath
    if ($result.verdict -ne "PASS") {
        throw "OTLP verification failed; see $summaryPath"
    }
    Write-Output $summaryPath
} finally {
    Pop-Location
}
