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
$utf8Strict = [Text.UTF8Encoding]::new($false, $true)
$utf8NoBom = [Text.UTF8Encoding]::new($false)
$resolvedRepository = (Resolve-Path -LiteralPath $RepositoryRoot).Path
$resolvedVerifier = (Resolve-Path -LiteralPath (Join-Path $resolvedRepository $VerifierPath)).Path
$tempBase = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
$tempRoot = Join-Path $tempBase ("engram-dbph-evidence-r3-" + [Guid]::NewGuid().ToString('N'))
if (-not $tempRoot.StartsWith($tempBase, [StringComparison]::OrdinalIgnoreCase) -or
    -not [IO.Path]::GetFileName($tempRoot).StartsWith('engram-dbph-evidence-r3-', [StringComparison]::Ordinal)) {
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
    foreach ($argument in $Arguments) { $startInfo.ArgumentList.Add($argument) }
    $process = [Diagnostics.Process]::Start($startInfo)
    $stream = [IO.MemoryStream]::new()
    $process.StandardOutput.BaseStream.CopyTo($stream)
    $standardError = $process.StandardError.ReadToEnd()
    $process.WaitForExit()
    if ($process.ExitCode -ne 0) { throw "git $($Arguments -join ' ') failed: $standardError" }
    return $stream.ToArray()
}

function Get-CanonicalBytes {
    param([Parameter(Mandatory = $true)][string]$Path)
    if ($SourceMode -eq 'GitIndex') { return Invoke-GitRaw -Arguments @('show', ":$Path") }
    return Invoke-GitRaw -Arguments @('show', "${Revision}:$Path")
}

function Write-JsonNoBom {
    param([Parameter(Mandatory = $true)]$Value, [Parameter(Mandatory = $true)][string]$Path)
    $json = (($Value | ConvertTo-Json -Depth 20) -replace "`r`n", "`n")
    [IO.File]::WriteAllText($Path, $json + "`n", $utf8NoBom)
}

function Write-TextNoBom {
    param([Parameter(Mandatory = $true)][string]$Text, [Parameter(Mandatory = $true)][string]$Path)
    [IO.File]::WriteAllText($Path, $Text, $utf8NoBom)
}

function New-CoherentSumsOverride {
    param(
        [Parameter(Mandatory = $true)][byte[]]$CanonicalSums,
        [Parameter(Mandatory = $true)][string]$MutatedManifestPath,
        [string]$RemovePath,
        [switch]$ReverseData,
        [string]$DuplicatePath,
        [Parameter(Mandatory = $true)][string]$OutputFile
    )

    $lines = @($utf8Strict.GetString($CanonicalSums) -split "`n" | Where-Object { $_ -ne '' })
    $headers = @($lines | Where-Object { $_.StartsWith('#') })
    $data = [Collections.Generic.List[string]]::new()
    foreach ($line in @($lines | Where-Object { -not $_.StartsWith('#') })) {
        if (-not [string]::IsNullOrWhiteSpace($RemovePath) -and $line.EndsWith("  $RemovePath", [StringComparison]::Ordinal)) { continue }
        $data.Add($line)
    }
    if (-not [string]::IsNullOrWhiteSpace($DuplicatePath)) {
        $duplicate = @($data | Where-Object { $_.EndsWith("  $DuplicatePath", [StringComparison]::Ordinal) })
        if ($duplicate.Count -ne 1) { throw "cannot duplicate checksum path: $DuplicatePath" }
        $data.Add($duplicate[0])
    }
    if ($ReverseData) {
        $array = [string[]]@($data)
        [Array]::Reverse($array)
        $data = [Collections.Generic.List[string]]::new()
        foreach ($line in $array) { $data.Add($line) }
    }
    $manifestBytes = [IO.File]::ReadAllBytes($MutatedManifestPath)
    $manifestHash = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($manifestBytes)).ToLowerInvariant()
    for ($index = 0; $index -lt $data.Count; $index++) {
        if ($data[$index].EndsWith("  $ManifestPath", [StringComparison]::Ordinal)) {
            $data[$index] = "$manifestHash  $ManifestPath"
        }
    }
    Write-TextNoBom -Text ((@($headers) + @($data) -join "`n") + "`n") -Path $OutputFile
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
        '-NoProfile', '-File', $resolvedVerifier,
        '-RepositoryRoot', $resolvedRepository,
        '-SourceMode', $SourceMode,
        '-Revision', $Revision,
        '-ManifestPath', $ManifestPath,
        '-SumsPath', $SumsPath,
        '-InventoryPath', $InventoryPath,
        '-OutputPath', $resultPath,
        '-Quiet'
    )
    if (-not [string]::IsNullOrWhiteSpace($ManifestOverride)) { $arguments += @('-ManifestOverridePath', $ManifestOverride) }
    if (-not [string]::IsNullOrWhiteSpace($SumsOverride)) { $arguments += @('-SumsOverridePath', $SumsOverride) }
    if (-not [string]::IsNullOrWhiteSpace($InventoryOverride)) { $arguments += @('-InventoryOverridePath', $InventoryOverride) }

    & pwsh @arguments *> $logPath
    $exitCode = $LASTEXITCODE
    $result = if (Test-Path -LiteralPath $resultPath) { Get-Content -Raw -LiteralPath $resultPath | ConvertFrom-Json } else { $null }
    $failureText = if ($null -eq $result) { '' } else { @($result.failures) -join ' | ' }
    $exitMatches = if ($ExpectedExit -eq 0) { $exitCode -eq 0 } else { $exitCode -ne 0 }
    $failureMatches = [string]::IsNullOrWhiteSpace($ExpectedFailure) -or $failureText.Contains($ExpectedFailure)
    return [ordered]@{
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
    $manifestText = $utf8Strict.GetString($canonicalManifest)
    $inventoryText = $utf8Strict.GetString($canonicalInventory)

    $cases.Add((Invoke-VerifierCase -Name 'baseline' -ExpectedExit 0))

    $missingPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/14-evidence-r2-focused.log'
    $missingManifest = $manifestText | ConvertFrom-Json
    $missingManifest.entries = @($missingManifest.entries | Where-Object { $_.path -cne $missingPath })
    $missingManifest.entry_count = [int64]$missingManifest.entries.Count
    $missingManifestPath = Join-Path $tempRoot 'MANIFEST.missing-changed-path.json'
    $missingSumsPath = Join-Path $tempRoot 'SHA256SUMS.missing-changed-path.txt'
    Write-JsonNoBom -Value $missingManifest -Path $missingManifestPath
    New-CoherentSumsOverride -CanonicalSums $canonicalSums -MutatedManifestPath $missingManifestPath -RemovePath $missingPath -OutputFile $missingSumsPath
    $cases.Add((Invoke-VerifierCase -Name 'missing_changed_path' -ManifestOverride $missingManifestPath -SumsOverride $missingSumsPath -ExpectedExit 1 -ExpectedFailure 'manifest missing changed path'))

    $unsortedManifest = $manifestText | ConvertFrom-Json
    $reversedEntries = [object[]]@($unsortedManifest.entries)
    [Array]::Reverse($reversedEntries)
    $unsortedManifest.entries = $reversedEntries
    $unsortedManifestPath = Join-Path $tempRoot 'MANIFEST.unsorted.json'
    $unsortedSumsPath = Join-Path $tempRoot 'SHA256SUMS.unsorted.txt'
    Write-JsonNoBom -Value $unsortedManifest -Path $unsortedManifestPath
    New-CoherentSumsOverride -CanonicalSums $canonicalSums -MutatedManifestPath $unsortedManifestPath -ReverseData -OutputFile $unsortedSumsPath
    $cases.Add((Invoke-VerifierCase -Name 'unsorted_manifest_and_sums' -ManifestOverride $unsortedManifestPath -SumsOverride $unsortedSumsPath -ExpectedExit 1 -ExpectedFailure 'not canonical ordinal order'))

    $duplicateManifest = $manifestText | ConvertFrom-Json
    $duplicatePath = [string]$duplicateManifest.entries[0].path
    $duplicateManifest.entries = @($duplicateManifest.entries) + @($duplicateManifest.entries[0])
    $duplicateManifest.entry_count = [int64]$duplicateManifest.entries.Count
    $duplicateManifestPath = Join-Path $tempRoot 'MANIFEST.duplicate.json'
    $duplicateSumsPath = Join-Path $tempRoot 'SHA256SUMS.duplicate.txt'
    Write-JsonNoBom -Value $duplicateManifest -Path $duplicateManifestPath
    New-CoherentSumsOverride -CanonicalSums $canonicalSums -MutatedManifestPath $duplicateManifestPath -DuplicatePath $duplicatePath -OutputFile $duplicateSumsPath
    $cases.Add((Invoke-VerifierCase -Name 'duplicate_manifest_and_sums' -ManifestOverride $duplicateManifestPath -SumsOverride $duplicateSumsPath -ExpectedExit 1 -ExpectedFailure 'duplicate manifest path'))

    $wrongTypeIDPath = Join-Path $tempRoot 'MANIFEST.id-array.json'
    Write-TextNoBom -Text ($manifestText.Replace('"id": "git-blob-bytes-v1"', '"id": ["git-blob-bytes-v1"]')) -Path $wrongTypeIDPath
    $cases.Add((Invoke-VerifierCase -Name 'wrong_type_representation_id_array' -ManifestOverride $wrongTypeIDPath -ExpectedExit 1 -ExpectedFailure 'must be JSON string, got array'))

    $nullIDPath = Join-Path $tempRoot 'MANIFEST.id-null.json'
    Write-TextNoBom -Text ($manifestText.Replace('"id": "git-blob-bytes-v1"', '"id": null')) -Path $nullIDPath
    $cases.Add((Invoke-VerifierCase -Name 'null_representation_id' -ManifestOverride $nullIDPath -ExpectedExit 1 -ExpectedFailure 'must be JSON string, got null'))

    $scalarExclusion = $manifestText | ConvertFrom-Json
    $scalarExclusion.evidence_delta.manifest_entry_self_excluded_paths = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/MANIFEST.json'
    $scalarExclusionPath = Join-Path $tempRoot 'MANIFEST.exclusion-scalar.json'
    Write-JsonNoBom -Value $scalarExclusion -Path $scalarExclusionPath
    $cases.Add((Invoke-VerifierCase -Name 'wrong_type_exclusions_scalar' -ManifestOverride $scalarExclusionPath -ExpectedExit 1 -ExpectedFailure 'must be JSON array, got string'))

    $numericStringsPath = Join-Path $tempRoot 'INVENTORY.numeric-strings.json'
    $numericStrings = $inventoryText.Replace('"required_call_sites": 83', '"required_call_sites": "83"').Replace('"required_files": 8', '"required_files": "8"')
    Write-TextNoBom -Text $numericStrings -Path $numericStringsPath
    $cases.Add((Invoke-VerifierCase -Name 'wrong_type_inventory_numeric_strings' -InventoryOverride $numericStringsPath -ExpectedExit 1 -ExpectedFailure 'must be JSON number, got string'))

    $nullCountPath = Join-Path $tempRoot 'INVENTORY.null-count.json'
    Write-TextNoBom -Text ($inventoryText.Replace('"required_call_sites": 83', '"required_call_sites": null')) -Path $nullCountPath
    $cases.Add((Invoke-VerifierCase -Name 'null_inventory_count' -InventoryOverride $nullCountPath -ExpectedExit 1 -ExpectedFailure 'must be JSON number, got null'))

    $crlfManifestPath = Join-Path $tempRoot 'MANIFEST.raw-crlf.json'
    Write-TextNoBom -Text ($manifestText -replace "(?<!`r)`n", "`r`n") -Path $crlfManifestPath
    $cases.Add((Invoke-VerifierCase -Name 'crlf_raw_representation' -ManifestOverride $crlfManifestPath -ExpectedExit 1 -ExpectedFailure 'manifest bytes contain CR'))

    $wrongRepresentationPath = Join-Path $tempRoot 'MANIFEST.wrong-representation.json'
    Write-TextNoBom -Text ($manifestText.Replace('"id": "git-blob-bytes-v1"', '"id": "raw-checkout-bytes-v1"')) -Path $wrongRepresentationPath
    $cases.Add((Invoke-VerifierCase -Name 'incorrect_representation_contract' -ManifestOverride $wrongRepresentationPath -ExpectedExit 1 -ExpectedFailure 'unsupported representation contract'))

    $falseInventory = $inventoryText | ConvertFrom-Json
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
            -not [IO.Path]::GetFileName($resolvedTempRoot).StartsWith('engram-dbph-evidence-r3-', [StringComparison]::Ordinal)) {
            throw "refusing unsafe cleanup: $resolvedTempRoot"
        }
        if ([IO.Directory]::Exists($resolvedTempRoot)) { [IO.Directory]::Delete($resolvedTempRoot, $true) }
    }
    catch { $cleanupError = $_.Exception.Message }
}

$tempResidue = [IO.Directory]::Exists($tempRoot)
$allPass = @($cases | Where-Object { -not $_.pass }).Count -eq 0 -and -not $tempResidue -and $null -eq $cleanupError
$proof = [ordered]@{
    schema_version = 3
    status = if ($allPass) { 'PASS' } else { 'FAIL' }
    source_mode = $SourceMode
    revision = if ($SourceMode -eq 'GitIndex') { 'INDEX' } else { $Revision }
    cases = @($cases)
    temp_root_removed = -not $tempResidue
    cleanup = if ($null -eq $cleanupError) { 'PASS' } else { "FAIL: $cleanupError" }
}
$proofJson = (($proof | ConvertTo-Json -Depth 12) -replace "`r`n", "`n")
if (-not [string]::IsNullOrWhiteSpace($OutputPath)) {
    $resolvedOutput = [IO.Path]::GetFullPath($OutputPath)
    [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($resolvedOutput)) | Out-Null
    [IO.File]::WriteAllText($resolvedOutput, $proofJson + "`n", $utf8NoBom)
}
$proofJson
if (-not $allPass) { exit 1 }
exit 0
