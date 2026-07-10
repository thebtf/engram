param(
    [Parameter(Mandatory = $true)]
    [string]$RepositoryRoot,

    [ValidateSet('GitRevision', 'GitIndex')]
    [string]$SourceMode = 'GitRevision',

    [string]$Revision = 'HEAD',

    [string]$OutputPath,

    [string]$VerifierPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/Verify-DBPoolHygieneEvidence.ps1',

    [string]$ManifestPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/MANIFEST.json',

    [string]$SumsPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/SHA256SUMS.txt',

    [string]$InventoryPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/INVENTORY.json'
)

$ErrorActionPreference = 'Stop'
$utf8NoBom = [Text.UTF8Encoding]::new($false)
$resolvedRepository = (Resolve-Path -LiteralPath $RepositoryRoot).Path
$resolvedVerifier = (Resolve-Path -LiteralPath (Join-Path $resolvedRepository $VerifierPath)).Path
$tempBase = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
$tempRoot = Join-Path $tempBase ("engram-dbph-evidence-r2-" + [Guid]::NewGuid().ToString('N'))
if (-not $tempRoot.StartsWith($tempBase, [StringComparison]::OrdinalIgnoreCase) -or
    -not [IO.Path]::GetFileName($tempRoot).StartsWith('engram-dbph-evidence-r2-', [StringComparison]::Ordinal)) {
    throw "unsafe temporary root: $tempRoot"
}
[IO.Directory]::CreateDirectory($tempRoot) | Out-Null

function Invoke-GitRaw {
    param([Parameter(Mandatory = $true)][string[]]$Arguments)
    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = 'git'
    $startInfo.WorkingDirectory = $resolvedRepository
    $startInfo.UseShellExecute = $false
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    foreach ($argument in $Arguments) {
        $startInfo.ArgumentList.Add($argument)
    }
    $process = [Diagnostics.Process]::Start($startInfo)
    $stream = [IO.MemoryStream]::new()
    $process.StandardOutput.BaseStream.CopyTo($stream)
    $standardError = $process.StandardError.ReadToEnd()
    $process.WaitForExit()
    if ($process.ExitCode -ne 0) {
        throw "git $($Arguments -join ' ') failed: $standardError"
    }
    return $stream.ToArray()
}

function Get-CanonicalBytes {
    param([Parameter(Mandatory = $true)][string]$Path)
    if ($SourceMode -eq 'GitIndex') {
        return Invoke-GitRaw -Arguments @('show', ":$Path")
    }
    return Invoke-GitRaw -Arguments @('show', "${Revision}:$Path")
}

function Write-JsonNoBom {
    param(
        [Parameter(Mandatory = $true)]$Value,
        [Parameter(Mandatory = $true)][string]$Path
    )
    $json = (($Value | ConvertTo-Json -Depth 16) -replace "`r`n", "`n")
    [IO.File]::WriteAllText($Path, $json + "`n", $utf8NoBom)
}

function Invoke-VerifierCase {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [string]$ManifestOverride,
        [string]$SumsOverride,
        [string]$InventoryOverride,
        [Parameter(Mandatory = $true)][int]$ExpectedExit,
        [string]$ExpectedFailure
    )

    $resultPath = Join-Path $tempRoot "$Name.result.json"
    $logPath = Join-Path $tempRoot "$Name.console.log"
    $arguments = @(
        '-NoProfile',
        '-File', $resolvedVerifier,
        '-RepositoryRoot', $resolvedRepository,
        '-SourceMode', $SourceMode,
        '-Revision', $Revision,
        '-ManifestPath', $ManifestPath,
        '-SumsPath', $SumsPath,
        '-InventoryPath', $InventoryPath,
        '-OutputPath', $resultPath,
        '-Quiet'
    )
    if (-not [string]::IsNullOrWhiteSpace($ManifestOverride)) {
        $arguments += @('-ManifestOverridePath', $ManifestOverride)
    }
    if (-not [string]::IsNullOrWhiteSpace($SumsOverride)) {
        $arguments += @('-SumsOverridePath', $SumsOverride)
    }
    if (-not [string]::IsNullOrWhiteSpace($InventoryOverride)) {
        $arguments += @('-InventoryOverridePath', $InventoryOverride)
    }

    & pwsh @arguments *> $logPath
    $exitCode = $LASTEXITCODE
    $result = if (Test-Path -LiteralPath $resultPath) {
        Get-Content -Raw -LiteralPath $resultPath | ConvertFrom-Json
    } else {
        $null
    }
    $failureText = if ($null -eq $result) { '' } else { @($result.failures) -join ' | ' }
    $exitMatches = if ($ExpectedExit -eq 0) { $exitCode -eq 0 } else { $exitCode -ne 0 }
    $failureMatches = if ([string]::IsNullOrWhiteSpace($ExpectedFailure)) {
        $true
    } else {
        $failureText.Contains($ExpectedFailure)
    }
    return [pscustomobject]@{
        name = $Name
        expected_exit = if ($ExpectedExit -eq 0) { 0 } else { 'nonzero' }
        actual_exit = $exitCode
        verifier_status = if ($null -eq $result) { 'NO_RESULT' } else { $result.status }
        expected_failure = $ExpectedFailure
        observed_failures = if ($null -eq $result) { @('verifier did not write result JSON') } else { @($result.failures) }
        pass = $exitMatches -and $failureMatches -and $null -ne $result
    }
}

$cases = [Collections.Generic.List[object]]::new()
$cleanupError = $null
try {
    $canonicalManifest = Get-CanonicalBytes -Path $ManifestPath
    $canonicalSums = Get-CanonicalBytes -Path $SumsPath
    $canonicalInventory = Get-CanonicalBytes -Path $InventoryPath
    $manifestFixture = Join-Path $tempRoot 'MANIFEST.canonical.json'
    $sumsFixture = Join-Path $tempRoot 'SHA256SUMS.canonical.txt'
    $inventoryFixture = Join-Path $tempRoot 'INVENTORY.canonical.json'
    [IO.File]::WriteAllBytes($manifestFixture, $canonicalManifest)
    [IO.File]::WriteAllBytes($sumsFixture, $canonicalSums)
    [IO.File]::WriteAllBytes($inventoryFixture, $canonicalInventory)

    $cases.Add((Invoke-VerifierCase -Name 'baseline' -ExpectedExit 0))

    $staleManifest = (Get-Content -Raw -LiteralPath $manifestFixture | ConvertFrom-Json)
    $staleManifest.entries[0].sha256 = '0000000000000000000000000000000000000000000000000000000000000000'
    $staleManifestPath = Join-Path $tempRoot 'MANIFEST.stale-entry.json'
    Write-JsonNoBom -Value $staleManifest -Path $staleManifestPath
    $cases.Add((Invoke-VerifierCase -Name 'stale_manifest_entry' -ManifestOverride $staleManifestPath -ExpectedExit 1 -ExpectedFailure 'manifest SHA-256 mismatch'))

    $manifestText = $utf8NoBom.GetString($canonicalManifest)
    $crlfManifestPath = Join-Path $tempRoot 'MANIFEST.raw-crlf.json'
    [IO.File]::WriteAllText($crlfManifestPath, ($manifestText -replace "(?<!`r)`n", "`r`n"), $utf8NoBom)
    $cases.Add((Invoke-VerifierCase -Name 'crlf_raw_representation' -ManifestOverride $crlfManifestPath -ExpectedExit 1 -ExpectedFailure 'outer checksum mismatch'))

    $wrongRepresentation = (Get-Content -Raw -LiteralPath $manifestFixture | ConvertFrom-Json)
    $wrongRepresentation.representation_contract.id = 'raw-checkout-bytes-v1'
    $wrongRepresentationPath = Join-Path $tempRoot 'MANIFEST.wrong-representation.json'
    Write-JsonNoBom -Value $wrongRepresentation -Path $wrongRepresentationPath
    $cases.Add((Invoke-VerifierCase -Name 'incorrect_representation_contract' -ManifestOverride $wrongRepresentationPath -ExpectedExit 1 -ExpectedFailure 'unsupported representation contract'))

    $falseInventory = (Get-Content -Raw -LiteralPath $inventoryFixture | ConvertFrom-Json)
    $falseInventory.required_call_sites = 76
    $falseInventory.required_files = 6
    $falseInventory.actual_call_sites = 76
    $falseInventory.actual_files = 6
    $falseInventory.entries = @($falseInventory.entries | Where-Object { -not $_.path.Contains('temporal_truth') })
    $falseInventoryPath = Join-Path $tempRoot 'INVENTORY.false-76-6.json'
    Write-JsonNoBom -Value $falseInventory -Path $falseInventoryPath
    $cases.Add((Invoke-VerifierCase -Name 'false_inventory_76_6' -InventoryOverride $falseInventoryPath -ExpectedExit 1 -ExpectedFailure 'inventory acceptance constants must be 83/8'))
}
finally {
    try {
        $resolvedTempRoot = [IO.Path]::GetFullPath($tempRoot)
        if (-not $resolvedTempRoot.StartsWith($tempBase, [StringComparison]::OrdinalIgnoreCase) -or
            -not [IO.Path]::GetFileName($resolvedTempRoot).StartsWith('engram-dbph-evidence-r2-', [StringComparison]::Ordinal)) {
            throw "refusing unsafe cleanup: $resolvedTempRoot"
        }
        if ([IO.Directory]::Exists($resolvedTempRoot)) {
            [IO.Directory]::Delete($resolvedTempRoot, $true)
        }
    }
    catch {
        $cleanupError = $_.Exception.Message
    }
}

$tempResidue = [IO.Directory]::Exists($tempRoot)
$allPass = @($cases | Where-Object { -not $_.pass }).Count -eq 0 -and -not $tempResidue -and $null -eq $cleanupError
$proof = [ordered]@{
    generated_utc = [DateTime]::UtcNow.ToString('o')
    status = if ($allPass) { 'PASS' } else { 'FAIL' }
    source_mode = $SourceMode
    revision = if ($SourceMode -eq 'GitIndex') { 'INDEX' } else { $Revision }
    cases = @($cases)
    temp_root_removed = -not $tempResidue
    cleanup_error = $cleanupError
}
$proofJson = (($proof | ConvertTo-Json -Depth 10) -replace "`r`n", "`n")
if (-not [string]::IsNullOrWhiteSpace($OutputPath)) {
    $resolvedOutput = [IO.Path]::GetFullPath($OutputPath)
    [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($resolvedOutput)) | Out-Null
    [IO.File]::WriteAllText($resolvedOutput, $proofJson + "`n", $utf8NoBom)
}
$proofJson
if (-not $allPass) {
    exit 1
}
exit 0
