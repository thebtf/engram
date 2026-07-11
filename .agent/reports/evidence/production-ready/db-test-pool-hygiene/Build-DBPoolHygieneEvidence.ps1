param(
    [Parameter(Mandatory = $true)]
    [string]$RepositoryRoot,

    [string]$ProductCandidateSHA = '276337b3e96aa5af6d2e7dd9a0002ff957e5ffc9',

    [string]$ManifestPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/MANIFEST.json',

    [string]$SumsPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/SHA256SUMS.txt',

    [string]$AdversarialProofPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/adversarial-proof.json',

    [string]$VerifierProofPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/verifier-proof.json',

    [switch]$SeedDynamicProofs
)

$ErrorActionPreference = 'Stop'
$utf8Strict = [Text.UTF8Encoding]::new($false, $true)
$utf8NoBom = [Text.UTF8Encoding]::new($false)
$ordinal = [StringComparer]::Ordinal
$resolvedRepository = (Resolve-Path -LiteralPath $RepositoryRoot).Path
$evidencePrefix = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/'
$contractPath = Join-Path $PSScriptRoot 'DBPoolHygieneEvidenceContract.ps1'
if (-not (Test-Path -LiteralPath $contractPath)) {
    throw "evidence contract not found: $contractPath"
}
. $contractPath
if ($ProductCandidateSHA -cne $DBPHProductCandidateSHA) {
    throw "unsupported product candidate: $ProductCandidateSHA"
}
$productBlobPath = $DBPHProductBlobPath
$manifestExclusions = @($ManifestPath, $SumsPath)
$checksumExclusions = @($SumsPath)

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

function Convert-BytesToText {
    param([Parameter(Mandatory = $true)][byte[]]$Bytes)
    return $utf8Strict.GetString($Bytes)
}

function Convert-NulList {
    param([Parameter(Mandatory = $true)][byte[]]$Bytes)

    $text = Convert-BytesToText -Bytes $Bytes
    return @($text -split [char]0 | Where-Object { $_ -ne '' })
}

function Sort-Ordinal {
    param([Parameter(Mandatory = $true)][string[]]$Values)

    $copy = [string[]]@($Values)
    [Array]::Sort($copy, $ordinal)
    return $copy
}

function Get-IndexObjectEvidence {
    param([Parameter(Mandatory = $true)][string]$Path)

    $oid = (Convert-BytesToText (Invoke-GitRaw -Arguments @('rev-parse', ":$Path"))).Trim()
    $type = (Convert-BytesToText (Invoke-GitRaw -Arguments @('cat-file', '-t', $oid))).Trim()
    if ($type -ne 'blob') {
        throw "index object for $Path is $type, expected blob"
    }
    $bytes = Invoke-GitRaw -Arguments @('cat-file', 'blob', $oid)
    if ($bytes -contains [byte]0x0D) {
        throw "index blob contains CR, LF bytes are required: $Path"
    }
    return [pscustomobject]@{
        oid = $oid
        bytes = $bytes
        length = $bytes.Length
        sha256 = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant()
    }
}

$head = (Convert-BytesToText (Invoke-GitRaw -Arguments @('rev-parse', 'HEAD'))).Trim()
$changedPaths = Convert-NulList (Invoke-GitRaw -Arguments @('diff', '--cached', '--name-only', '-z', $ProductCandidateSHA, '--'))
$changedPaths = Sort-Ordinal -Values $changedPaths
$productOrTestChanges = @($changedPaths | Where-Object { -not $_.StartsWith('.agent/reports/', [StringComparison]::Ordinal) })
if ($productOrTestChanges.Count -ne 0) {
    throw "evidence revision changes forbidden product/test paths: $($productOrTestChanges -join ', ')"
}

$trackedPaths = Convert-NulList (Invoke-GitRaw -Arguments @('ls-files', '-z'))
$entryPaths = @($trackedPaths | Where-Object {
    ($_.StartsWith($evidencePrefix, [StringComparison]::Ordinal) -or
        ($_ -match '^\.agent/reports/[0-9]{4}-[0-9]{2}-[0-9]{2}-db-test-pool-hygiene.*\.md$') -or
        $_ -eq $productBlobPath) -and
    -not $manifestExclusions.Contains($_)
})
$entryPaths = Sort-Ordinal -Values $entryPaths

$directChangedPaths = @($changedPaths | Where-Object { -not $manifestExclusions.Contains($_) })
$missingChangedPaths = @($directChangedPaths | Where-Object { -not $entryPaths.Contains($_) })
if ($missingChangedPaths.Count -ne 0) {
    throw "builder selection omits changed evidence paths: $($missingChangedPaths -join ', ')"
}

if ($SeedDynamicProofs) {
    $adversarialProof = New-DBPHSeedAdversarialProof
    $verifierProof = New-DBPHSeedVerifierProof `
        -ChangedPaths ([int64]$changedPaths.Count) `
        -DirectlyBoundChangedPaths ([int64]$directChangedPaths.Count) `
        -ManifestEntries ([int64]$entryPaths.Count) `
        -ChecksumEntries ([int64]($entryPaths.Count + 1))
    foreach ($proof in @(
        [pscustomobject]@{ path = $AdversarialProofPath; value = $adversarialProof },
        [pscustomobject]@{ path = $VerifierProofPath; value = $verifierProof }
    )) {
        $proofJson = (($proof.value | ConvertTo-Json -Depth 20) -replace "`r`n", "`n") + "`n"
        if ($utf8NoBom.GetBytes($proofJson) -contains [byte]0x0D) {
            throw "generated dynamic proof contains CR: $($proof.path)"
        }
        [IO.File]::WriteAllText((Join-Path $resolvedRepository $proof.path), $proofJson, $utf8NoBom)
    }
    [ordered]@{
        status = 'SEEDED_DYNAMIC_PROOFS'
        source_mode = 'GitIndex'
        changed_path_count = $changedPaths.Count
        directly_bound_changed_path_count = $directChangedPaths.Count
        manifest_entries = $entryPaths.Count
        checksum_entries = $entryPaths.Count + 1
        next_action = 'stage both proof files, then rerun builder without -SeedDynamicProofs'
    } | ConvertTo-Json -Depth 4
    return
}

$entries = [Collections.Generic.List[object]]::new()
$entryHashes = @{}
foreach ($path in $entryPaths) {
    $object = Get-IndexObjectEvidence -Path $path
    $entryHashes[$path] = $object.sha256
    $entries.Add([ordered]@{
        path = $path
        git_blob_oid = $object.oid
        bytes = [int64]$object.length
        sha256 = $object.sha256
    })
}

$manifest = [ordered]@{
    schema_version = $DBPHManifestSchemaVersion
    status = 'READY_FOR_RECHECK_EVIDENCE_R4'
    product_parent_sha = $DBPHProductParentSHA
    product_candidate_sha = $ProductCandidateSHA
    evidence_revision_parent_sha = $head
    evidence_revision_target = 'GitIndex'
    evidence_delta = [ordered]@{
        comparison = "$ProductCandidateSHA..GitIndex"
        changed_path_count = [int64]$changedPaths.Count
        directly_manifest_bound_count = [int64]$directChangedPaths.Count
        product_or_test_paths_changed = @()
        manifest_entry_self_excluded_paths = $manifestExclusions
        checksum_self_excluded_paths = $checksumExclusions
    }
    representation_contract = [ordered]@{
        id = $DBPHRepresentationContract
        digest = 'SHA-256'
        object_type = 'blob'
        path_binding = 'each path at the verified Git index/revision resolves to git_blob_oid'
        contract_bytes = 'git cat-file blob <git_blob_oid> exact stdout bytes'
        working_tree_bytes = 'excluded; raw CRLF checkout bytes fail verification'
        text_git_blob_line_endings = 'LF'
        manifest_self_reference = 'excluded-from-manifest-entries-bound-by-outer-checksum'
        outer_checksum_path = $SumsPath
        outer_checksum_generation_order = 'manifest-first-checksum-second'
        outer_checksum_self_reference = 'excluded'
    }
    product_test_blob = [ordered]@{
        path = $productBlobPath
        git_blob_oid = $DBPHProductBlobOID
        sha256 = $DBPHProductBlobSHA256
        byte_identical_to_product_candidate = $true
    }
    inventory = [ordered]@{
        parent_sha = $DBPHProductParentSHA
        required_call_sites = [int64]83
        required_files = [int64]8
        path = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/INVENTORY.json'
    }
    entry_count = [int64]$entries.Count
    entries = @($entries)
}

$manifestJson = (($manifest | ConvertTo-Json -Depth 12) -replace "`r`n", "`n") + "`n"
$manifestBytes = $utf8NoBom.GetBytes($manifestJson)
if ($manifestBytes -contains [byte]0x0D) {
    throw 'generated manifest contains CR'
}
$resolvedManifest = Join-Path $resolvedRepository $ManifestPath
[IO.File]::WriteAllText($resolvedManifest, $manifestJson, $utf8NoBom)
$manifestHash = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($manifestBytes)).ToLowerInvariant()

$sumPaths = Sort-Ordinal -Values @($entryPaths + $ManifestPath)
$sumLines = [Collections.Generic.List[string]]::new()
$sumLines.Add('# representation_contract=git-blob-bytes-v1')
$sumLines.Add('# manifest_generation_order=manifest-first-checksum-second')
$sumLines.Add('# checksum_self_reference=excluded')
foreach ($path in $sumPaths) {
    $hash = if ($path -eq $ManifestPath) { $manifestHash } else { $entryHashes[$path] }
    if ([string]::IsNullOrWhiteSpace($hash)) {
        throw "missing hash for checksum path: $path"
    }
    $sumLines.Add("$hash  $path")
}
$sumsText = ($sumLines -join "`n") + "`n"
$sumsBytes = $utf8NoBom.GetBytes($sumsText)
if ($sumsBytes -contains [byte]0x0D) {
    throw 'generated outer checksum contains CR'
}
$resolvedSums = Join-Path $resolvedRepository $SumsPath
[IO.File]::WriteAllText($resolvedSums, $sumsText, $utf8NoBom)

[ordered]@{
    status = 'BUILT'
    source_mode = 'GitIndex'
    evidence_revision_parent_sha = $head
    changed_path_count = $changedPaths.Count
    manifest_entries = $entries.Count
    checksum_entries = $sumPaths.Count
    manifest_self_excluded_paths = $manifestExclusions
    checksum_self_excluded_paths = $checksumExclusions
} | ConvertTo-Json -Depth 6
