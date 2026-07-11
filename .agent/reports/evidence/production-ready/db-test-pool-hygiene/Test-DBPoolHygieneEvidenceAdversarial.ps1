param(
    [Parameter(Mandatory = $true)]
    [string]$RepositoryRoot,

    [ValidateSet('GitRevision', 'GitIndex')]
    [string]$SourceMode = 'GitIndex',

    [string]$Revision = 'HEAD',

    [string]$OutputPath,

    [string]$VerifierPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/Verify-DBPoolHygieneEvidence.ps1',

    [string]$ManifestPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/MANIFEST.json',

    [string]$SumsPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/SHA256SUMS.txt',

    [string]$InventoryPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/INVENTORY.json',

    [string]$AdversarialProofPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/adversarial-proof.json',

    [string]$VerifierProofPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/verifier-proof.json'
)

$ErrorActionPreference = 'Stop'
$utf8Strict = [Text.UTF8Encoding]::new($false, $true)
$utf8NoBom = [Text.UTF8Encoding]::new($false)
$ordinal = [StringComparer]::Ordinal
$resolvedRepository = (Resolve-Path -LiteralPath $RepositoryRoot).Path
$resolvedVerifier = (Resolve-Path -LiteralPath (Join-Path $resolvedRepository $VerifierPath)).Path
$contractPath = Join-Path (Split-Path -Parent $resolvedVerifier) 'DBPoolHygieneEvidenceContract.ps1'
if (-not (Test-Path -LiteralPath $contractPath)) { throw "evidence contract not found: $contractPath" }
. $contractPath
if ($SourceMode -ne 'GitIndex') { throw 'R4 adversarial proof must run from SourceMode GitIndex' }

$tempBase = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
$tempPrefix = 'engram-dbph-evidence-r4-'
$tempRoot = Join-Path $tempBase ($tempPrefix + [Guid]::NewGuid().ToString('N'))
if (-not $tempRoot.StartsWith($tempBase, [StringComparison]::OrdinalIgnoreCase) -or
    -not [IO.Path]::GetFileName($tempRoot).StartsWith($tempPrefix, [StringComparison]::Ordinal)) {
    throw "unsafe temporary root: $tempRoot"
}
[IO.Directory]::CreateDirectory($tempRoot) | Out-Null

function Invoke-GitRawAt {
    param(
        [Parameter(Mandatory = $true)][string]$WorkingDirectory,
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [byte[]]$InputBytes
    )

    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = 'git'
    $startInfo.WorkingDirectory = $WorkingDirectory
    $startInfo.UseShellExecute = $false
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    $hasInput = $PSBoundParameters.ContainsKey('InputBytes')
    $startInfo.RedirectStandardInput = $hasInput
    foreach ($argument in $Arguments) { $startInfo.ArgumentList.Add($argument) }
    $process = [Diagnostics.Process]::Start($startInfo)
    if ($hasInput) {
        $process.StandardInput.BaseStream.Write($InputBytes, 0, $InputBytes.Length)
        $process.StandardInput.Close()
    }
    $stream = [IO.MemoryStream]::new()
    $process.StandardOutput.BaseStream.CopyTo($stream)
    $standardError = $process.StandardError.ReadToEnd()
    $process.WaitForExit()
    if ($process.ExitCode -ne 0) { throw "git $($Arguments -join ' ') failed: $standardError" }
    return $stream.ToArray()
}

function Convert-BytesToText {
    param([Parameter(Mandatory = $true)][byte[]]$Bytes)
    return $utf8Strict.GetString($Bytes)
}

function Invoke-GitTextAt {
    param(
        [Parameter(Mandatory = $true)][string]$WorkingDirectory,
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [byte[]]$InputBytes
    )
    if ($PSBoundParameters.ContainsKey('InputBytes')) {
        return (Convert-BytesToText (Invoke-GitRawAt -WorkingDirectory $WorkingDirectory -Arguments $Arguments -InputBytes $InputBytes)).Trim()
    }
    return (Convert-BytesToText (Invoke-GitRawAt -WorkingDirectory $WorkingDirectory -Arguments $Arguments)).Trim()
}

function ConvertTo-CanonicalJsonBytes {
    param([Parameter(Mandatory = $true)]$Value)
    $json = (($Value | ConvertTo-Json -Depth 30) -replace "`r`n", "`n") + "`n"
    $bytes = $utf8NoBom.GetBytes($json)
    if ($bytes -contains [byte]0x0D) { throw 'canonical JSON serialization produced CR bytes' }
    return $bytes
}

function ConvertFrom-JsonBytes {
    param([Parameter(Mandatory = $true)][byte[]]$Bytes)
    return (Convert-BytesToText $Bytes) | ConvertFrom-Json
}

function Get-SHA256 {
    param([Parameter(Mandatory = $true)][byte[]]$Bytes)
    return [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($Bytes)).ToLowerInvariant()
}

function Get-IndexBytes {
    param([Parameter(Mandatory = $true)][string]$CaseRepository, [Parameter(Mandatory = $true)][string]$Path)
    return Invoke-GitRawAt -WorkingDirectory $CaseRepository -Arguments @('show', ":$Path")
}

function Set-IndexBytes {
    param(
        [Parameter(Mandatory = $true)][string]$CaseRepository,
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][byte[]]$Bytes
    )
    $oid = Invoke-GitTextAt -WorkingDirectory $CaseRepository -Arguments @('hash-object', '-w', '--stdin') -InputBytes $Bytes
    Invoke-GitRawAt -WorkingDirectory $CaseRepository -Arguments @('update-index', '--add', '--cacheinfo', "100644,$oid,$Path") | Out-Null
    return $oid
}

function Write-SumsFromManifest {
    param(
        [Parameter(Mandatory = $true)][string]$CaseRepository,
        [Parameter(Mandatory = $true)]$Manifest,
        [Parameter(Mandatory = $true)][byte[]]$ManifestBytes,
        [switch]$PreserveManifestEntryOrder,
        [switch]$DuplicateFirstDataLine
    )

    $data = [Collections.Generic.List[string]]::new()
    foreach ($entry in @($Manifest.entries)) { $data.Add("$($entry.sha256)  $($entry.path)") }
    $data.Add("$(Get-SHA256 -Bytes $ManifestBytes)  $ManifestPath")
    if (-not $PreserveManifestEntryOrder) {
        $array = [string[]]@($data)
        [Array]::Sort($array, [Comparison[string]]{
            param($left, $right)
            $leftPath = $left.Substring(66)
            $rightPath = $right.Substring(66)
            return $ordinal.Compare($leftPath, $rightPath)
        })
        $data = [Collections.Generic.List[string]]::new()
        foreach ($line in $array) { $data.Add($line) }
    }
    if ($DuplicateFirstDataLine) { $data.Add($data[0]) }
    $lines = @(
        '# representation_contract=git-blob-bytes-v1',
        '# manifest_generation_order=manifest-first-checksum-second',
        '# checksum_self_reference=excluded'
    ) + @($data)
    Set-IndexBytes -CaseRepository $CaseRepository -Path $SumsPath -Bytes $utf8NoBom.GetBytes(($lines -join "`n") + "`n") | Out-Null
}

function Set-ManifestBytesCoherently {
    param(
        [Parameter(Mandatory = $true)][string]$CaseRepository,
        [Parameter(Mandatory = $true)][byte[]]$ManifestBytes
    )
    Set-IndexBytes -CaseRepository $CaseRepository -Path $ManifestPath -Bytes $ManifestBytes | Out-Null
    $sumsText = Convert-BytesToText (Get-IndexBytes -CaseRepository $CaseRepository -Path $SumsPath)
    $manifestHash = Get-SHA256 -Bytes $ManifestBytes
    $lines = @($sumsText -split "`n" | Where-Object { $_ -ne '' })
    $replaced = 0
    for ($index = 0; $index -lt $lines.Count; $index++) {
        if ($lines[$index].EndsWith("  $ManifestPath", [StringComparison]::Ordinal)) {
            $lines[$index] = "$manifestHash  $ManifestPath"
            $replaced++
        }
    }
    if ($replaced -ne 1) { throw "expected one manifest checksum line, got $replaced" }
    Set-IndexBytes -CaseRepository $CaseRepository -Path $SumsPath -Bytes $utf8NoBom.GetBytes(($lines -join "`n") + "`n") | Out-Null
}

function Set-ManifestObjectCoherently {
    param([Parameter(Mandatory = $true)][string]$CaseRepository, [Parameter(Mandatory = $true)]$Manifest)
    Set-ManifestBytesCoherently -CaseRepository $CaseRepository -ManifestBytes (ConvertTo-CanonicalJsonBytes -Value $Manifest)
}

function Set-ArtifactBytesCoherently {
    param(
        [Parameter(Mandatory = $true)][string]$CaseRepository,
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][byte[]]$Bytes
    )
    $oid = Set-IndexBytes -CaseRepository $CaseRepository -Path $Path -Bytes $Bytes
    $manifest = ConvertFrom-JsonBytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $ManifestPath)
    $entry = @($manifest.entries | Where-Object { $_.path -ceq $Path })
    if ($entry.Count -ne 1) { throw "manifest must contain exactly one entry for coherent mutation: $Path" }
    $entry[0].git_blob_oid = $oid
    $entry[0].bytes = [int64]$Bytes.Length
    $entry[0].sha256 = Get-SHA256 -Bytes $Bytes
    $manifestBytes = ConvertTo-CanonicalJsonBytes -Value $manifest
    Set-IndexBytes -CaseRepository $CaseRepository -Path $ManifestPath -Bytes $manifestBytes | Out-Null
    Write-SumsFromManifest -CaseRepository $CaseRepository -Manifest $manifest -ManifestBytes $manifestBytes
}

function Set-ArtifactObjectCoherently {
    param(
        [Parameter(Mandatory = $true)][string]$CaseRepository,
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)]$Value
    )
    Set-ArtifactBytesCoherently -CaseRepository $CaseRepository -Path $Path -Bytes (ConvertTo-CanonicalJsonBytes -Value $Value)
}

function Add-UnknownProperty {
    param([Parameter(Mandatory = $true)]$Object, [string]$Name = 'unexpected_r4_key')
    $Object | Add-Member -MemberType NoteProperty -Name $Name -Value 'must-be-rejected'
}

function Replace-ExactlyOnce {
    param(
        [Parameter(Mandatory = $true)][string]$Text,
        [Parameter(Mandatory = $true)][string]$Old,
        [Parameter(Mandatory = $true)][string]$New
    )
    $first = $Text.IndexOf($Old, [StringComparison]::Ordinal)
    if ($first -lt 0 -or $Text.IndexOf($Old, $first + $Old.Length, [StringComparison]::Ordinal) -ge 0) {
        throw "replacement source must occur exactly once: $Old"
    }
    return $Text.Substring(0, $first) + $New + $Text.Substring($first + $Old.Length)
}

function New-CaseRepository {
    param([Parameter(Mandatory = $true)][int]$Ordinal, [Parameter(Mandatory = $true)][string]$Name)
    $caseRepository = Join-Path $tempRoot ('case-{0:D2}-{1}' -f $Ordinal, $Name)
    [IO.Directory]::CreateDirectory($caseRepository) | Out-Null
    Invoke-GitRawAt -WorkingDirectory $caseRepository -Arguments @('init', '-q') | Out-Null
    $gitDirectory = Invoke-GitTextAt -WorkingDirectory $caseRepository -Arguments @('rev-parse', '--absolute-git-dir')
    $infoDirectory = Join-Path $gitDirectory 'objects/info'
    [IO.Directory]::CreateDirectory($infoDirectory) | Out-Null
    [IO.File]::WriteAllText((Join-Path $infoDirectory 'alternates'), $script:sourceObjectsPath + "`n", $utf8NoBom)
    Invoke-GitRawAt -WorkingDirectory $caseRepository -Arguments @('config', 'core.autocrlf', 'false') | Out-Null
    Invoke-GitRawAt -WorkingDirectory $caseRepository -Arguments @('update-ref', 'refs/heads/evidence', $DBPHEvidenceParentSHA) | Out-Null
    Invoke-GitRawAt -WorkingDirectory $caseRepository -Arguments @('symbolic-ref', 'HEAD', 'refs/heads/evidence') | Out-Null
    Invoke-GitRawAt -WorkingDirectory $caseRepository -Arguments @('read-tree', $script:sourceTree) | Out-Null
    return $caseRepository
}

function Invoke-Verifier {
    param([Parameter(Mandatory = $true)][string]$CaseRepository, [Parameter(Mandatory = $true)][string]$Name)
    $resultPath = Join-Path $CaseRepository 'verifier-result.json'
    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = 'pwsh'
    $startInfo.WorkingDirectory = $CaseRepository
    $startInfo.UseShellExecute = $false
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    foreach ($argument in @(
        '-NoProfile', '-File', $resolvedVerifier,
        '-RepositoryRoot', $CaseRepository,
        '-SourceMode', 'GitIndex',
        '-ManifestPath', $ManifestPath,
        '-SumsPath', $SumsPath,
        '-InventoryPath', $InventoryPath,
        '-AdversarialProofPath', $AdversarialProofPath,
        '-VerifierProofPath', $VerifierProofPath,
        '-OutputPath', $resultPath,
        '-Quiet'
    )) { $startInfo.ArgumentList.Add($argument) }
    $process = [Diagnostics.Process]::Start($startInfo)
    $stdout = $process.StandardOutput.ReadToEnd()
    $stderr = $process.StandardError.ReadToEnd()
    $process.WaitForExit()
    $result = if (Test-Path -LiteralPath $resultPath) {
        Get-Content -Raw -LiteralPath $resultPath | ConvertFrom-Json
    } else {
        [pscustomobject]@{ status = 'NO_RESULT'; failures = @("verifier did not write result JSON: $Name", $stdout.Trim(), $stderr.Trim()) }
    }
    return [pscustomobject]@{ exit_code = $process.ExitCode; result = $result }
}

function Apply-CaseMutation {
    param([Parameter(Mandatory = $true)][string]$CaseRepository, [Parameter(Mandatory = $true)][string]$Name)
    if ($Name -eq 'baseline') { return }

    $manifestBytes = Get-IndexBytes -CaseRepository $CaseRepository -Path $ManifestPath
    $manifestText = Convert-BytesToText $manifestBytes
    $inventoryBytes = Get-IndexBytes -CaseRepository $CaseRepository -Path $InventoryPath
    $inventoryText = Convert-BytesToText $inventoryBytes
    switch ($Name) {
        'missing_changed_path' {
            $missingPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/16-evidence-r4-red.json'
            $manifest = ConvertFrom-JsonBytes $manifestBytes
            $before = @($manifest.entries).Count
            $manifest.entries = @($manifest.entries | Where-Object { $_.path -cne $missingPath })
            if (@($manifest.entries).Count -ne $before - 1) { throw "missing-path fixture not manifest-bound: $missingPath" }
            $manifest.entry_count = [int64]@($manifest.entries).Count
            $newBytes = ConvertTo-CanonicalJsonBytes $manifest
            Set-IndexBytes -CaseRepository $CaseRepository -Path $ManifestPath -Bytes $newBytes | Out-Null
            Write-SumsFromManifest -CaseRepository $CaseRepository -Manifest $manifest -ManifestBytes $newBytes
        }
        'unsorted_manifest_and_sums' {
            $manifest = ConvertFrom-JsonBytes $manifestBytes
            $entries = [object[]]@($manifest.entries)
            [Array]::Reverse($entries)
            $manifest.entries = $entries
            $newBytes = ConvertTo-CanonicalJsonBytes $manifest
            Set-IndexBytes -CaseRepository $CaseRepository -Path $ManifestPath -Bytes $newBytes | Out-Null
            Write-SumsFromManifest -CaseRepository $CaseRepository -Manifest $manifest -ManifestBytes $newBytes -PreserveManifestEntryOrder
        }
        'duplicate_manifest_and_sums' {
            $manifest = ConvertFrom-JsonBytes $manifestBytes
            $manifest.entries = @($manifest.entries) + @($manifest.entries[0])
            $manifest.entry_count = [int64]@($manifest.entries).Count
            $newBytes = ConvertTo-CanonicalJsonBytes $manifest
            Set-IndexBytes -CaseRepository $CaseRepository -Path $ManifestPath -Bytes $newBytes | Out-Null
            Write-SumsFromManifest -CaseRepository $CaseRepository -Manifest $manifest -ManifestBytes $newBytes -PreserveManifestEntryOrder
        }
        'wrong_type_representation_id_array' {
            Set-ManifestBytesCoherently -CaseRepository $CaseRepository -ManifestBytes $utf8NoBom.GetBytes((Replace-ExactlyOnce -Text $manifestText -Old '"id": "git-blob-bytes-v1"' -New '"id": ["git-blob-bytes-v1"]'))
        }
        'null_representation_id' {
            Set-ManifestBytesCoherently -CaseRepository $CaseRepository -ManifestBytes $utf8NoBom.GetBytes((Replace-ExactlyOnce -Text $manifestText -Old '"id": "git-blob-bytes-v1"' -New '"id": null'))
        }
        'wrong_type_exclusions_scalar' {
            $manifest = ConvertFrom-JsonBytes $manifestBytes
            $manifest.evidence_delta.manifest_entry_self_excluded_paths = $ManifestPath
            Set-ManifestObjectCoherently -CaseRepository $CaseRepository -Manifest $manifest
        }
        'wrong_type_inventory_numeric_strings' {
            $text = Replace-ExactlyOnce -Text $inventoryText -Old '"required_call_sites": 83' -New '"required_call_sites": "83"'
            $text = Replace-ExactlyOnce -Text $text -Old '"required_files": 8' -New '"required_files": "8"'
            Set-ArtifactBytesCoherently -CaseRepository $CaseRepository -Path $InventoryPath -Bytes $utf8NoBom.GetBytes($text)
        }
        'null_inventory_count' {
            $text = Replace-ExactlyOnce -Text $inventoryText -Old '"required_call_sites": 83' -New '"required_call_sites": null'
            Set-ArtifactBytesCoherently -CaseRepository $CaseRepository -Path $InventoryPath -Bytes $utf8NoBom.GetBytes($text)
        }
        'crlf_raw_representation' {
            Set-ManifestBytesCoherently -CaseRepository $CaseRepository -ManifestBytes $utf8NoBom.GetBytes($manifestText.Replace("`n", "`r`n"))
        }
        'incorrect_representation_contract' {
            $manifest = ConvertFrom-JsonBytes $manifestBytes
            $manifest.representation_contract.id = 'raw-checkout-bytes-v1'
            Set-ManifestObjectCoherently -CaseRepository $CaseRepository -Manifest $manifest
        }
        'false_inventory_76_6' {
            $inventory = ConvertFrom-JsonBytes $inventoryBytes
            $inventory.required_call_sites = 76
            $inventory.required_files = 6
            $inventory.actual_call_sites = 76
            $inventory.actual_files = 6
            $inventory.entries = @($inventory.entries | Where-Object { -not $_.path.Contains('temporal_truth') })
            Set-ArtifactObjectCoherently -CaseRepository $CaseRepository -Path $InventoryPath -Value $inventory
        }
        'mixed_eol_representation' {
            $firstLF = $manifestText.IndexOf("`n", [StringComparison]::Ordinal)
            if ($firstLF -lt 0) { throw 'manifest fixture contains no LF' }
            $mixed = $manifestText.Substring(0, $firstLF) + "`r`n" + $manifestText.Substring($firstLF + 1)
            Set-ManifestBytesCoherently -CaseRepository $CaseRepository -ManifestBytes $utf8NoBom.GetBytes($mixed)
        }
        'unknown_manifest_key' {
            $manifest = ConvertFrom-JsonBytes $manifestBytes
            Add-UnknownProperty -Object $manifest
            Set-ManifestObjectCoherently -CaseRepository $CaseRepository -Manifest $manifest
        }
        'unknown_manifest_nested_key' {
            $manifest = ConvertFrom-JsonBytes $manifestBytes
            Add-UnknownProperty -Object $manifest.representation_contract
            Set-ManifestObjectCoherently -CaseRepository $CaseRepository -Manifest $manifest
        }
        'unknown_manifest_entry_key' {
            $manifest = ConvertFrom-JsonBytes $manifestBytes
            Add-UnknownProperty -Object $manifest.entries[0]
            Set-ManifestObjectCoherently -CaseRepository $CaseRepository -Manifest $manifest
        }
        'unknown_inventory_key' {
            $inventory = ConvertFrom-JsonBytes $inventoryBytes
            Add-UnknownProperty -Object $inventory
            Set-ArtifactObjectCoherently -CaseRepository $CaseRepository -Path $InventoryPath -Value $inventory
        }
        'unknown_inventory_entry_key' {
            $inventory = ConvertFrom-JsonBytes $inventoryBytes
            Add-UnknownProperty -Object $inventory.entries[0]
            Set-ArtifactObjectCoherently -CaseRepository $CaseRepository -Path $InventoryPath -Value $inventory
        }
        'manifest_schema_99' {
            $manifest = ConvertFrom-JsonBytes $manifestBytes
            $manifest.schema_version = 99
            Set-ManifestObjectCoherently -CaseRepository $CaseRepository -Manifest $manifest
        }
        'inventory_schema_99' {
            $inventory = ConvertFrom-JsonBytes $inventoryBytes
            $inventory.schema_version = 99
            Set-ArtifactObjectCoherently -CaseRepository $CaseRepository -Path $InventoryPath -Value $inventory
        }
        'adversarial_proof_schema_99' {
            $proof = ConvertFrom-JsonBytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $AdversarialProofPath)
            $proof.schema_version = 99
            Set-ArtifactObjectCoherently -CaseRepository $CaseRepository -Path $AdversarialProofPath -Value $proof
        }
        'verifier_proof_schema_99' {
            $proof = ConvertFrom-JsonBytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $VerifierProofPath)
            $proof.schema_version = 99
            Set-ArtifactObjectCoherently -CaseRepository $CaseRepository -Path $VerifierProofPath -Value $proof
        }
        'unknown_adversarial_proof_key' {
            $proof = ConvertFrom-JsonBytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $AdversarialProofPath)
            Add-UnknownProperty -Object $proof
            Set-ArtifactObjectCoherently -CaseRepository $CaseRepository -Path $AdversarialProofPath -Value $proof
        }
        'unknown_adversarial_case_key' {
            $proof = ConvertFrom-JsonBytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $AdversarialProofPath)
            Add-UnknownProperty -Object $proof.cases[0]
            Set-ArtifactObjectCoherently -CaseRepository $CaseRepository -Path $AdversarialProofPath -Value $proof
        }
        'unknown_verifier_proof_key' {
            $proof = ConvertFrom-JsonBytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $VerifierProofPath)
            Add-UnknownProperty -Object $proof
            Set-ArtifactObjectCoherently -CaseRepository $CaseRepository -Path $VerifierProofPath -Value $proof
        }
        'stale_adversarial_proof' {
            $proof = ConvertFrom-JsonBytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $AdversarialProofPath)
            $proof.cases = @($proof.cases[0..($proof.cases.Count - 2)])
            Set-ArtifactObjectCoherently -CaseRepository $CaseRepository -Path $AdversarialProofPath -Value $proof
        }
        'stale_verifier_proof' {
            $proof = ConvertFrom-JsonBytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $VerifierProofPath)
            $proof.changed_paths = [int64]$proof.changed_paths - 1
            Set-ArtifactObjectCoherently -CaseRepository $CaseRepository -Path $VerifierProofPath -Value $proof
        }
        'false_adversarial_case_order' {
            $proof = ConvertFrom-JsonBytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $AdversarialProofPath)
            $cases = [object[]]@($proof.cases)
            $temporary = $cases[0]
            $cases[0] = $cases[1]
            $cases[1] = $temporary
            $proof.cases = $cases
            Set-ArtifactObjectCoherently -CaseRepository $CaseRepository -Path $AdversarialProofPath -Value $proof
        }
        'false_adversarial_status' {
            $proof = ConvertFrom-JsonBytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $AdversarialProofPath)
            $proof.status = 'FAIL'
            Set-ArtifactObjectCoherently -CaseRepository $CaseRepository -Path $AdversarialProofPath -Value $proof
        }
        'false_adversarial_actual_status' {
            $proof = ConvertFrom-JsonBytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $AdversarialProofPath)
            $proof.cases[1].actual_exit = 0
            Set-ArtifactObjectCoherently -CaseRepository $CaseRepository -Path $AdversarialProofPath -Value $proof
        }
        'false_adversarial_required_diagnostic' {
            $proof = ConvertFrom-JsonBytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $AdversarialProofPath)
            $proof.cases[1].observed_failures = [object[]]@()
            Set-ArtifactObjectCoherently -CaseRepository $CaseRepository -Path $AdversarialProofPath -Value $proof
        }
        'false_verifier_counts' {
            $proof = ConvertFrom-JsonBytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $VerifierProofPath)
            $proof.manifest_entries = [int64]$proof.manifest_entries + 1
            Set-ArtifactObjectCoherently -CaseRepository $CaseRepository -Path $VerifierProofPath -Value $proof
        }
        'false_verifier_status' {
            $proof = ConvertFrom-JsonBytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $VerifierProofPath)
            $proof.status = 'FAIL'
            Set-ArtifactObjectCoherently -CaseRepository $CaseRepository -Path $VerifierProofPath -Value $proof
        }
        'false_verifier_source_revision' {
            $proof = ConvertFrom-JsonBytes (Get-IndexBytes -CaseRepository $CaseRepository -Path $VerifierProofPath)
            $proof.source_mode = 'GitRevision'
            $proof.revision = 'HEAD'
            Set-ArtifactObjectCoherently -CaseRepository $CaseRepository -Path $VerifierProofPath -Value $proof
        }
        'duplicate_json_property' {
            $duplicate = '"schema_version": 4,' + "`n  " + '"schema_version": 4,'
            $text = Replace-ExactlyOnce -Text $manifestText -Old '"schema_version": 4,' -New $duplicate
            Set-ManifestBytesCoherently -CaseRepository $CaseRepository -ManifestBytes $utf8NoBom.GetBytes($text)
        }
        default { throw "unimplemented adversarial case: $Name" }
    }
}

$cases = [Collections.Generic.List[object]]::new()
$cleanupError = $null
$fatalError = $null
try {
    $sourceTree = Invoke-GitTextAt -WorkingDirectory $resolvedRepository -Arguments @('write-tree')
    $objectsPathText = Invoke-GitTextAt -WorkingDirectory $resolvedRepository -Arguments @('rev-parse', '--git-path', 'objects')
    $sourceObjectsPath = if ([IO.Path]::IsPathRooted($objectsPathText)) {
        [IO.Path]::GetFullPath($objectsPathText)
    } else {
        [IO.Path]::GetFullPath((Join-Path $resolvedRepository $objectsPathText))
    }
    if (-not [IO.Directory]::Exists($sourceObjectsPath)) { throw "source Git object directory not found: $sourceObjectsPath" }

    $specs = @(Get-DBPHAdversarialCaseSpecs)
    for ($index = 0; $index -lt $specs.Count; $index++) {
        $spec = $specs[$index]
        $caseRepository = New-CaseRepository -Ordinal ($index + 1) -Name $spec.name
        Apply-CaseMutation -CaseRepository $caseRepository -Name $spec.name
        $invocation = Invoke-Verifier -CaseRepository $caseRepository -Name $spec.name
        $result = $invocation.result
        $observedFailures = [object[]]@($result.failures)
        $exitMatches = if ($spec.expect_nonzero) { $invocation.exit_code -ne 0 } else { $invocation.exit_code -eq 0 }
        $statusMatches = [string]$result.status -ceq $(if ($spec.expect_nonzero) { 'FAIL' } else { 'PASS' })
        $diagnosticMatches = if ($spec.expect_nonzero) {
            $matched = $false
            foreach ($failure in $observedFailures) {
                if (([string]$failure).Contains([string]$spec.expected_failure, [StringComparison]::Ordinal)) { $matched = $true; break }
            }
            $matched
        } else {
            $observedFailures.Count -eq 0
        }
        $cases.Add([ordered]@{
            name = $spec.name
            expected_exit = if ($spec.expect_nonzero) { 'nonzero' } else { 0 }
            actual_exit = [int64]$invocation.exit_code
            verifier_status = [string]$result.status
            expected_failure = [string]$spec.expected_failure
            observed_failures = $observedFailures
            pass = $exitMatches -and $statusMatches -and $diagnosticMatches
        })
    }
}
catch {
    $fatalError = $_.Exception.Message
}
finally {
    try {
        $resolvedTempRoot = [IO.Path]::GetFullPath($tempRoot)
        if (-not $resolvedTempRoot.StartsWith($tempBase, [StringComparison]::OrdinalIgnoreCase) -or
            -not [IO.Path]::GetFileName($resolvedTempRoot).StartsWith($tempPrefix, [StringComparison]::Ordinal)) {
            throw "refusing unsafe cleanup: $resolvedTempRoot"
        }
        if ([IO.Directory]::Exists($resolvedTempRoot)) {
            Get-ChildItem -LiteralPath $resolvedTempRoot -Recurse -Force | ForEach-Object { $_.Attributes = [IO.FileAttributes]::Normal }
            [IO.Directory]::Delete($resolvedTempRoot, $true)
        }
    }
    catch { $cleanupError = $_.Exception.Message }
}

$tempResidue = [IO.Directory]::Exists($tempRoot)
$allPass = $null -eq $fatalError -and $null -eq $cleanupError -and -not $tempResidue -and
    $cases.Count -eq @(Get-DBPHAdversarialCaseSpecs).Count -and @($cases | Where-Object { -not $_.pass }).Count -eq 0
$proof = [ordered]@{
    schema_version = $DBPHAdversarialProofSchemaVersion
    status = if ($allPass) { 'PASS' } else { 'FAIL' }
    source_mode = 'GitIndex'
    revision = 'INDEX'
    cases = [object[]]@($cases)
    temp_root_removed = -not $tempResidue
    cleanup = if ($null -eq $cleanupError -and $null -eq $fatalError) { 'PASS' } elseif ($null -ne $fatalError) { "FAIL: $fatalError" } else { "FAIL: $cleanupError" }
}
$proofJson = (($proof | ConvertTo-Json -Depth 30) -replace "`r`n", "`n")
if (-not [string]::IsNullOrWhiteSpace($OutputPath)) {
    $resolvedOutput = [IO.Path]::GetFullPath($OutputPath)
    [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($resolvedOutput)) | Out-Null
    [IO.File]::WriteAllText($resolvedOutput, $proofJson + "`n", $utf8NoBom)
}
$proofJson
if (-not $allPass) { exit 1 }
exit 0
