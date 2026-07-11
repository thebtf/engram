param(
    [Parameter(Mandatory = $true)]
    [string]$RepositoryRoot,

    [Parameter(Mandatory = $true)]
    [string]$OutputPath
)

$ErrorActionPreference = 'Stop'
$utf8Strict = [Text.UTF8Encoding]::new($false, $true)
$utf8NoBom = [Text.UTF8Encoding]::new($false)
$ordinal = [StringComparer]::Ordinal
$maker = 'ce6a40d72fc39932ccbc4b949647f321b91f70c3'
$evidenceParent = '331b5b195a967e7f27dca94038a3480c9afcc84f'
$productCandidate = '276337b3e96aa5af6d2e7dd9a0002ff957e5ffc9'
$manifestPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/MANIFEST.json'
$sumsPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/SHA256SUMS.txt'
$inventoryPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/INVENTORY.json'
$adversarialProofPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/adversarial-proof.json'
$verifierProofPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/verifier-proof.json'
$productBlobPath = 'internal/db/gorm/candidate_store_test.go'
$resolvedRepository = (Resolve-Path -LiteralPath $RepositoryRoot).Path
$verifierPath = Join-Path $resolvedRepository '.agent/reports/evidence/production-ready/db-test-pool-hygiene/Verify-DBPoolHygieneEvidence.ps1'
$tempBase = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
$tempPrefix = 'engram-dbph-r4-independent-'
$tempRoot = Join-Path $tempBase ($tempPrefix + [Guid]::NewGuid().ToString('N'))

if (-not $tempRoot.StartsWith($tempBase, [StringComparison]::OrdinalIgnoreCase) -or
    -not [IO.Path]::GetFileName($tempRoot).StartsWith($tempPrefix, [StringComparison]::Ordinal)) {
    throw "unsafe temp root: $tempRoot"
}
[IO.Directory]::CreateDirectory($tempRoot) | Out-Null

function Invoke-GitRaw {
    param(
        [Parameter(Mandatory = $true)][string]$WorkingDirectory,
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [byte[]]$InputBytes
    )

    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = 'git'
    $start.WorkingDirectory = $WorkingDirectory
    $start.UseShellExecute = $false
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $hasInput = $PSBoundParameters.ContainsKey('InputBytes')
    $start.RedirectStandardInput = $hasInput
    foreach ($argument in $Arguments) { $start.ArgumentList.Add($argument) }
    $process = [Diagnostics.Process]::Start($start)
    if ($hasInput) {
        $process.StandardInput.BaseStream.Write($InputBytes, 0, $InputBytes.Length)
        $process.StandardInput.Close()
    }
    $stream = [IO.MemoryStream]::new()
    $process.StandardOutput.BaseStream.CopyTo($stream)
    $stderr = $process.StandardError.ReadToEnd()
    $process.WaitForExit()
    if ($process.ExitCode -ne 0) {
        throw "git $($Arguments -join ' ') failed: $stderr"
    }
    return $stream.ToArray()
}

function Invoke-GitText {
    param(
        [Parameter(Mandatory = $true)][string]$WorkingDirectory,
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [byte[]]$InputBytes
    )
    $bytes = if ($PSBoundParameters.ContainsKey('InputBytes')) {
        Invoke-GitRaw -WorkingDirectory $WorkingDirectory -Arguments $Arguments -InputBytes $InputBytes
    } else {
        Invoke-GitRaw -WorkingDirectory $WorkingDirectory -Arguments $Arguments
    }
    return $utf8Strict.GetString($bytes).Trim()
}

function Get-Hash {
    param([Parameter(Mandatory = $true)][byte[]]$Bytes)
    return [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($Bytes)).ToLowerInvariant()
}

function ConvertTo-JsonBytes {
    param([Parameter(Mandatory = $true)]$Value)
    $json = (($Value | ConvertTo-Json -Depth 30) -replace "`r`n", "`n") + "`n"
    $bytes = $utf8NoBom.GetBytes($json)
    if ($bytes -contains [byte]0x0D) { throw 'canonical JSON contains CR' }
    return $bytes
}

function ConvertFrom-Bytes {
    param([Parameter(Mandatory = $true)][byte[]]$Bytes)
    return ($utf8Strict.GetString($Bytes) | ConvertFrom-Json)
}

function Get-IndexBytes {
    param([Parameter(Mandatory = $true)][string]$CaseRepository, [Parameter(Mandatory = $true)][string]$Path)
    return Invoke-GitRaw -WorkingDirectory $CaseRepository -Arguments @('show', ":$Path")
}

function Set-IndexBytes {
    param(
        [Parameter(Mandatory = $true)][string]$CaseRepository,
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][byte[]]$Bytes
    )
    $oid = Invoke-GitText -WorkingDirectory $CaseRepository -Arguments @('hash-object', '-w', '--stdin') -InputBytes $Bytes
    Invoke-GitRaw -WorkingDirectory $CaseRepository -Arguments @('update-index', '--add', '--cacheinfo', "100644,$oid,$Path") | Out-Null
    return $oid
}

function Write-SumsFromManifest {
    param(
        [Parameter(Mandatory = $true)][string]$CaseRepository,
        [Parameter(Mandatory = $true)]$Manifest,
        [Parameter(Mandatory = $true)][byte[]]$ManifestBytes,
        [hashtable]$ExtraPaths
    )

    $lines = [Collections.Generic.List[string]]::new()
    foreach ($entry in @($Manifest.entries)) {
        $lines.Add("$($entry.sha256)  $($entry.path)")
    }
    $lines.Add("$(Get-Hash -Bytes $ManifestBytes)  $manifestPath")
    if ($null -ne $ExtraPaths) {
        foreach ($path in $ExtraPaths.Keys) { $lines.Add("$($ExtraPaths[$path])  $path") }
    }
    $array = [string[]]@($lines)
    [Array]::Sort($array, [Comparison[string]]{
        param($left, $right)
        return $ordinal.Compare($left.Substring(66), $right.Substring(66))
    })
    $text = (@(
        '# representation_contract=git-blob-bytes-v1',
        '# manifest_generation_order=manifest-first-checksum-second',
        '# checksum_self_reference=excluded'
    ) + $array) -join "`n"
    Set-IndexBytes -CaseRepository $CaseRepository -Path $sumsPath -Bytes $utf8NoBom.GetBytes($text + "`n") | Out-Null
}

function Set-ManifestBytes {
    param(
        [Parameter(Mandatory = $true)][string]$CaseRepository,
        [Parameter(Mandatory = $true)][byte[]]$Bytes,
        [switch]$RebuildAllSums
    )
    Set-IndexBytes -CaseRepository $CaseRepository -Path $manifestPath -Bytes $Bytes | Out-Null
    if ($RebuildAllSums) {
        Write-SumsFromManifest -CaseRepository $CaseRepository -Manifest (ConvertFrom-Bytes $Bytes) -ManifestBytes $Bytes
        return
    }
    $sums = $utf8Strict.GetString((Get-IndexBytes -CaseRepository $CaseRepository -Path $sumsPath))
    $hash = Get-Hash -Bytes $Bytes
    $lines = @($sums -split "`n" | Where-Object { $_ -ne '' })
    $replaced = 0
    for ($index = 0; $index -lt $lines.Count; $index++) {
        if ($lines[$index].EndsWith("  $manifestPath", [StringComparison]::Ordinal)) {
            $lines[$index] = "$hash  $manifestPath"
            $replaced++
        }
    }
    if ($replaced -ne 1) { throw "expected one manifest checksum, got $replaced" }
    Set-IndexBytes -CaseRepository $CaseRepository -Path $sumsPath -Bytes $utf8NoBom.GetBytes(($lines -join "`n") + "`n") | Out-Null
}

function Set-ManifestObject {
    param(
        [Parameter(Mandatory = $true)][string]$CaseRepository,
        [Parameter(Mandatory = $true)]$Manifest,
        [switch]$RebuildAllSums
    )
    Set-ManifestBytes -CaseRepository $CaseRepository -Bytes (ConvertTo-JsonBytes $Manifest) -RebuildAllSums:$RebuildAllSums
}

function Set-ArtifactObject {
    param(
        [Parameter(Mandatory = $true)][string]$CaseRepository,
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)]$Value
    )
    $bytes = ConvertTo-JsonBytes $Value
    $oid = Set-IndexBytes -CaseRepository $CaseRepository -Path $Path -Bytes $bytes
    $manifest = ConvertFrom-Bytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $manifestPath)
    $entry = @($manifest.entries | Where-Object { $_.path -ceq $Path })
    if ($entry.Count -ne 1) { throw "manifest entry count for $Path is $($entry.Count)" }
    $entry[0].git_blob_oid = $oid
    $entry[0].bytes = [int64]$bytes.Length
    $entry[0].sha256 = Get-Hash -Bytes $bytes
    $manifestBytes = ConvertTo-JsonBytes $manifest
    Set-IndexBytes -CaseRepository $CaseRepository -Path $manifestPath -Bytes $manifestBytes | Out-Null
    Write-SumsFromManifest -CaseRepository $CaseRepository -Manifest $manifest -ManifestBytes $manifestBytes
}

function Replace-Once {
    param(
        [Parameter(Mandatory = $true)][string]$Text,
        [Parameter(Mandatory = $true)][string]$Old,
        [Parameter(Mandatory = $true)][string]$New
    )
    $index = $Text.IndexOf($Old, [StringComparison]::Ordinal)
    if ($index -lt 0 -or $Text.IndexOf($Old, $index + $Old.Length, [StringComparison]::Ordinal) -ge 0) {
        throw "replacement source must occur exactly once: $Old"
    }
    return $Text.Substring(0, $index) + $New + $Text.Substring($index + $Old.Length)
}

function New-CaseRepository {
    param([Parameter(Mandatory = $true)][int]$Number, [Parameter(Mandatory = $true)][string]$Name)
    $path = Join-Path $tempRoot ('case-{0:D2}-{1}' -f $Number, $Name)
    [IO.Directory]::CreateDirectory($path) | Out-Null
    Invoke-GitRaw -WorkingDirectory $path -Arguments @('init', '-q') | Out-Null
    $gitDir = Invoke-GitText -WorkingDirectory $path -Arguments @('rev-parse', '--absolute-git-dir')
    $info = Join-Path $gitDir 'objects/info'
    [IO.Directory]::CreateDirectory($info) | Out-Null
    [IO.File]::WriteAllText((Join-Path $info 'alternates'), $script:sourceObjects + "`n", $utf8NoBom)
    Invoke-GitRaw -WorkingDirectory $path -Arguments @('config', 'core.autocrlf', 'false') | Out-Null
    Invoke-GitRaw -WorkingDirectory $path -Arguments @('update-ref', 'refs/heads/evidence', $evidenceParent) | Out-Null
    Invoke-GitRaw -WorkingDirectory $path -Arguments @('symbolic-ref', 'HEAD', 'refs/heads/evidence') | Out-Null
    Invoke-GitRaw -WorkingDirectory $path -Arguments @('read-tree', $script:makerTree) | Out-Null
    return $path
}

function Invoke-Verifier {
    param([Parameter(Mandatory = $true)][string]$CaseRepository)
    $resultPath = Join-Path $CaseRepository 'result.json'
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = 'pwsh'
    $start.WorkingDirectory = $CaseRepository
    $start.UseShellExecute = $false
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    foreach ($argument in @(
        '-NoProfile', '-File', $verifierPath,
        '-RepositoryRoot', $CaseRepository,
        '-SourceMode', 'GitIndex',
        '-OutputPath', $resultPath,
        '-Quiet'
    )) { $start.ArgumentList.Add($argument) }
    $process = [Diagnostics.Process]::Start($start)
    $stdout = $process.StandardOutput.ReadToEnd()
    $stderr = $process.StandardError.ReadToEnd()
    $process.WaitForExit()
    $result = if (Test-Path -LiteralPath $resultPath) {
        Get-Content -Raw -LiteralPath $resultPath | ConvertFrom-Json
    } else {
        [pscustomobject]@{ status = 'NO_RESULT'; failures = @($stdout.Trim(), $stderr.Trim()) }
    }
    return [pscustomobject]@{ exit = $process.ExitCode; result = $result }
}

function Apply-Mutation {
    param([Parameter(Mandatory = $true)][string]$CaseRepository, [Parameter(Mandatory = $true)][string]$Name)
    if ($Name -eq 'baseline') { return }
    $manifestBytes = Get-IndexBytes -CaseRepository $CaseRepository -Path $manifestPath
    $manifestText = $utf8Strict.GetString($manifestBytes)
    $manifest = ConvertFrom-Bytes $manifestBytes

    switch ($Name) {
        'unknown_manifest_key' {
            $manifest | Add-Member NoteProperty checker_unknown 'reject'
            Set-ManifestObject -CaseRepository $CaseRepository -Manifest $manifest
        }
        'unknown_inventory_key' {
            $inventory = ConvertFrom-Bytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $inventoryPath)
            $inventory | Add-Member NoteProperty checker_unknown 'reject'
            Set-ArtifactObject -CaseRepository $CaseRepository -Path $inventoryPath -Value $inventory
        }
        'inventory_schema_99' {
            $inventory = ConvertFrom-Bytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $inventoryPath)
            $inventory.schema_version = 99
            Set-ArtifactObject -CaseRepository $CaseRepository -Path $inventoryPath -Value $inventory
        }
        'stale_adversarial_proof' {
            $proof = ConvertFrom-Bytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $adversarialProofPath)
            $proof.cases = @($proof.cases[0..($proof.cases.Count - 2)])
            Set-ArtifactObject -CaseRepository $CaseRepository -Path $adversarialProofPath -Value $proof
        }
        'stale_verifier_proof' {
            $proof = ConvertFrom-Bytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $verifierProofPath)
            $proof.changed_paths = [int64]$proof.changed_paths - 1
            Set-ArtifactObject -CaseRepository $CaseRepository -Path $verifierProofPath -Value $proof
        }
        'case_variant_manifest_status' {
            $bytes = $utf8NoBom.GetBytes((Replace-Once -Text $manifestText -Old '"status": "READY_FOR_RECHECK_EVIDENCE_R4"' -New '"Status": "READY_FOR_RECHECK_EVIDENCE_R4"'))
            Set-ManifestBytes -CaseRepository $CaseRepository -Bytes $bytes
        }
        'manifest_schema_string' {
            $bytes = $utf8NoBom.GetBytes((Replace-Once -Text $manifestText -Old '"schema_version": 4' -New '"schema_version": "4"'))
            Set-ManifestBytes -CaseRepository $CaseRepository -Bytes $bytes
        }
        'inventory_entries_scalar' {
            $inventory = ConvertFrom-Bytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $inventoryPath)
            $inventory.entries = $inventory.entries[0]
            Set-ArtifactObject -CaseRepository $CaseRepository -Path $inventoryPath -Value $inventory
        }
        'adversarial_cases_scalar' {
            $proof = ConvertFrom-Bytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $adversarialProofPath)
            $proof.cases = $proof.cases[0]
            Set-ArtifactObject -CaseRepository $CaseRepository -Path $adversarialProofPath -Value $proof
        }
        'manifest_entry_count_mismatch' {
            $manifest.entry_count = [int64]$manifest.entry_count + 1
            Set-ManifestObject -CaseRepository $CaseRepository -Manifest $manifest
        }
        'inventory_actual_count_mismatch' {
            $inventory = ConvertFrom-Bytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $inventoryPath)
            $inventory.actual_call_sites = [int64]$inventory.actual_call_sites + 1
            Set-ArtifactObject -CaseRepository $CaseRepository -Path $inventoryPath -Value $inventory
        }
        'verifier_checksum_count_mismatch' {
            $proof = ConvertFrom-Bytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $verifierProofPath)
            $proof.checksum_entries = [int64]$proof.checksum_entries + 1
            Set-ArtifactObject -CaseRepository $CaseRepository -Path $verifierProofPath -Value $proof
        }
        'manifest_entry_digest_mismatch' {
            $manifest.entries[0].sha256 = ('0' * 64)
            Set-ManifestObject -CaseRepository $CaseRepository -Manifest $manifest -RebuildAllSums
        }
        'outer_checksum_digest_mismatch' {
            $sums = $utf8Strict.GetString((Get-IndexBytes -CaseRepository $CaseRepository -Path $sumsPath))
            $lines = @($sums -split "`n" | Where-Object { $_ -ne '' })
            for ($index = 0; $index -lt $lines.Count; $index++) {
                if (-not $lines[$index].StartsWith('#')) { $lines[$index] = ('0' * 64) + $lines[$index].Substring(64); break }
            }
            Set-IndexBytes -CaseRepository $CaseRepository -Path $sumsPath -Bytes $utf8NoBom.GetBytes(($lines -join "`n") + "`n") | Out-Null
        }
        'inventory_line_mismatch' {
            $inventory = ConvertFrom-Bytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $inventoryPath)
            $inventory.entries[0].lines[0] = [int64]$inventory.entries[0].lines[0] + 1
            Set-ArtifactObject -CaseRepository $CaseRepository -Path $inventoryPath -Value $inventory
        }
        'inventory_case_duplicate_path' {
            $inventory = ConvertFrom-Bytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $inventoryPath)
            $duplicate = (($inventory.entries[0] | ConvertTo-Json -Depth 10) | ConvertFrom-Json)
            $duplicate.path = $duplicate.path.Substring(0, 1).ToUpperInvariant() + $duplicate.path.Substring(1)
            $inventory.entries = @($inventory.entries) + @($duplicate)
            Set-ArtifactObject -CaseRepository $CaseRepository -Path $inventoryPath -Value $inventory
        }
        'manifest_path_traversal' {
            $manifest.entries[0].path = '../checker-escape'
            Set-ManifestObject -CaseRepository $CaseRepository -Manifest $manifest -RebuildAllSums
        }
        'foreign_index_path' {
            $readme = Get-IndexBytes -CaseRepository $CaseRepository -Path 'README.md'
            Set-IndexBytes -CaseRepository $CaseRepository -Path 'README.md' -Bytes ($readme + $utf8NoBom.GetBytes("`nchecker foreign index mutation`n")) | Out-Null
        }
        'incomplete_checksum' {
            $sums = $utf8Strict.GetString((Get-IndexBytes -CaseRepository $CaseRepository -Path $sumsPath))
            $lines = [Collections.Generic.List[string]]::new()
            foreach ($line in @($sums -split "`n" | Where-Object { $_ -ne '' })) { $lines.Add($line) }
            $dataIndex = 3
            $lines.RemoveAt($dataIndex)
            Set-IndexBytes -CaseRepository $CaseRepository -Path $sumsPath -Bytes $utf8NoBom.GetBytes(($lines -join "`n") + "`n") | Out-Null
        }
        'unknown_inventory_entry_key' {
            $inventory = ConvertFrom-Bytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $inventoryPath)
            $inventory.entries[0] | Add-Member NoteProperty checker_unknown 'reject'
            Set-ArtifactObject -CaseRepository $CaseRepository -Path $inventoryPath -Value $inventory
        }
        'duplicate_json_property' {
            $duplicate = '"schema_version": 4,' + "`n  " + '"schema_version": 4,'
            $bytes = $utf8NoBom.GetBytes((Replace-Once -Text $manifestText -Old '"schema_version": 4,' -New $duplicate))
            Set-ManifestBytes -CaseRepository $CaseRepository -Bytes $bytes
        }
        'manifest_inventory_path_case' {
            $changedCase = $manifest.inventory.path.Replace('.agent/', '.Agent/')
            if ($changedCase -ceq $manifest.inventory.path) { throw 'inventory path case fixture did not change bytes' }
            $manifest.inventory.path = $changedCase
            Set-ManifestObject -CaseRepository $CaseRepository -Manifest $manifest
        }
        'verifier_failures_nonempty' {
            $proof = ConvertFrom-Bytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $verifierProofPath)
            $proof.failures = @('fabricated clean proof')
            Set-ArtifactObject -CaseRepository $CaseRepository -Path $verifierProofPath -Value $proof
        }
        'adversarial_case_pass_false' {
            $proof = ConvertFrom-Bytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $adversarialProofPath)
            $proof.cases[1].pass = $false
            Set-ArtifactObject -CaseRepository $CaseRepository -Path $adversarialProofPath -Value $proof
        }
        'adversarial_observed_failures_scalar' {
            $proof = ConvertFrom-Bytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $adversarialProofPath)
            $proof.cases[1].observed_failures = 'not-an-array'
            Set-ArtifactObject -CaseRepository $CaseRepository -Path $adversarialProofPath -Value $proof
        }
        'product_blob_mutation' {
            $bytes = Get-IndexBytes -CaseRepository $CaseRepository -Path $productBlobPath
            Set-IndexBytes -CaseRepository $CaseRepository -Path $productBlobPath -Bytes ($bytes + $utf8NoBom.GetBytes("`n// checker mutation`n")) | Out-Null
        }
        'checksum_unbound_extra_path' {
            $readme = Get-IndexBytes -CaseRepository $CaseRepository -Path 'README.md'
            Write-SumsFromManifest -CaseRepository $CaseRepository -Manifest $manifest -ManifestBytes $manifestBytes -ExtraPaths @{ 'README.md' = (Get-Hash -Bytes $readme) }
        }
        default { throw "unknown checker case: $Name" }
    }
}

$specs = @(
    @('baseline', $false, ''),
    @('unknown_manifest_key', $true, 'manifest has unknown JSON property'),
    @('unknown_inventory_key', $true, 'inventory has unknown JSON property'),
    @('inventory_schema_99', $true, 'inventory schema_version must be 1'),
    @('stale_adversarial_proof', $true, 'adversarial proof case count mismatch'),
    @('stale_verifier_proof', $true, 'verifier proof changed_paths mismatch'),
    @('case_variant_manifest_status', $true, 'manifest has unknown JSON property'),
    @('manifest_schema_string', $true, 'manifest.schema_version must be JSON number'),
    @('inventory_entries_scalar', $true, 'inventory.entries must be JSON array'),
    @('adversarial_cases_scalar', $true, 'adversarial proof.cases must be JSON array'),
    @('manifest_entry_count_mismatch', $true, 'manifest entry_count mismatch'),
    @('inventory_actual_count_mismatch', $true, 'inventory total mismatch'),
    @('verifier_checksum_count_mismatch', $true, 'verifier proof checksum_entries mismatch'),
    @('manifest_entry_digest_mismatch', $true, 'manifest SHA-256 mismatch'),
    @('outer_checksum_digest_mismatch', $true, 'outer checksum mismatch'),
    @('inventory_line_mismatch', $true, 'inventory site list mismatch'),
    @('inventory_case_duplicate_path', $true, 'duplicate inventory path'),
    @('manifest_path_traversal', $true, 'manifest entry unreadable'),
    @('foreign_index_path', $true, 'evidence revision changes product/test paths'),
    @('incomplete_checksum', $true, 'outer checksum missing manifest-bound path'),
    @('unknown_inventory_entry_key', $true, 'inventory.entries[0] has unknown JSON property'),
    @('duplicate_json_property', $true, 'duplicate JSON property'),
    @('manifest_inventory_path_case', $true, 'manifest inventory contract mismatch'),
    @('verifier_failures_nonempty', $true, 'verifier proof failures must be an empty JSON array'),
    @('adversarial_case_pass_false', $true, 'adversarial proof case result mismatch'),
    @('adversarial_observed_failures_scalar', $true, 'observed_failures must be JSON array'),
    @('product_blob_mutation', $true, 'product test blob differs from accepted product candidate'),
    @('checksum_unbound_extra_path', $true, 'outer checksum contains unbound extra path')
)

$results = [Collections.Generic.List[object]]::new()
$fatal = $null
$cleanupError = $null
try {
    $script:makerTree = Invoke-GitText -WorkingDirectory $resolvedRepository -Arguments @('rev-parse', "$maker^{tree}")
    $objectsText = Invoke-GitText -WorkingDirectory $resolvedRepository -Arguments @('rev-parse', '--git-path', 'objects')
    $script:sourceObjects = if ([IO.Path]::IsPathRooted($objectsText)) {
        [IO.Path]::GetFullPath($objectsText)
    } else {
        [IO.Path]::GetFullPath((Join-Path $resolvedRepository $objectsText))
    }
    if (-not [IO.Directory]::Exists($script:sourceObjects)) { throw "source objects missing: $($script:sourceObjects)" }

    for ($index = 0; $index -lt $specs.Count; $index++) {
        $name = [string]$specs[$index][0]
        $expectReject = [bool]$specs[$index][1]
        $expectedFailure = [string]$specs[$index][2]
        $caseRepository = New-CaseRepository -Number ($index + 1) -Name $name
        Apply-Mutation -CaseRepository $caseRepository -Name $name
        $invocation = Invoke-Verifier -CaseRepository $caseRepository
        $failures = @($invocation.result.failures)
        $exitMatches = if ($expectReject) { $invocation.exit -ne 0 } else { $invocation.exit -eq 0 }
        $statusMatches = [string]$invocation.result.status -ceq $(if ($expectReject) { 'FAIL' } else { 'PASS' })
        $diagnosticMatches = if ($expectReject) {
            @($failures | Where-Object { ([string]$_).Contains($expectedFailure, [StringComparison]::Ordinal) }).Count -gt 0
        } else {
            $failures.Count -eq 0
        }
        $results.Add([ordered]@{
            name = $name
            expected = if ($expectReject) { 'REJECT' } else { 'PASS' }
            expected_failure = $expectedFailure
            actual_exit = [int64]$invocation.exit
            actual_status = [string]$invocation.result.status
            failures = [object[]]$failures
            pass = $exitMatches -and $statusMatches -and $diagnosticMatches
        })
    }
}
catch {
    $fatal = $_.Exception.Message
}
finally {
    try {
        $resolvedTempRoot = [IO.Path]::GetFullPath($tempRoot)
        if (-not $resolvedTempRoot.StartsWith($tempBase, [StringComparison]::OrdinalIgnoreCase) -or
            -not [IO.Path]::GetFileName($resolvedTempRoot).StartsWith($tempPrefix, [StringComparison]::Ordinal)) {
            throw "unsafe cleanup target: $resolvedTempRoot"
        }
        if ([IO.Directory]::Exists($resolvedTempRoot)) {
            Get-ChildItem -LiteralPath $resolvedTempRoot -Recurse -Force | ForEach-Object { $_.Attributes = [IO.FileAttributes]::Normal }
            [IO.Directory]::Delete($resolvedTempRoot, $true)
        }
    }
    catch { $cleanupError = $_.Exception.Message }
}

$tempRemoved = -not [IO.Directory]::Exists($tempRoot)
$allPass = $null -eq $fatal -and $null -eq $cleanupError -and $tempRemoved -and
    $results.Count -eq $specs.Count -and @($results | Where-Object { -not $_.pass }).Count -eq 0
$proof = [ordered]@{
    schema_version = 1
    target = $maker
    target_tree = $script:makerTree
    method = 'checker-owned fresh repository and alternate Git index per case'
    status = if ($allPass) { 'PASS' } else { 'FAIL' }
    case_count = [int64]$results.Count
    cases = [object[]]$results
    fatal_error = $fatal
    temp_root_removed = $tempRemoved
    cleanup_error = $cleanupError
}
$json = (($proof | ConvertTo-Json -Depth 30) -replace "`r`n", "`n") + "`n"
$resolvedOutput = [IO.Path]::GetFullPath($OutputPath)
[IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($resolvedOutput)) | Out-Null
[IO.File]::WriteAllText($resolvedOutput, $json, $utf8NoBom)
$json
if (-not $allPass) { exit 1 }
exit 0
