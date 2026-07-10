param(
    [Parameter(Mandatory = $true)]
    [string]$RepositoryRoot,

    [ValidateSet('GitRevision', 'GitIndex')]
    [string]$SourceMode = 'GitRevision',

    [string]$Revision = 'HEAD',

    [string]$ManifestPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/MANIFEST.json',

    [string]$SumsPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/SHA256SUMS.txt',

    [string]$InventoryPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/INVENTORY.json',

    [string]$ManifestOverridePath,

    [string]$SumsOverridePath,

    [string]$InventoryOverridePath,

    [string]$OutputPath,

    [switch]$Quiet
)

$ErrorActionPreference = 'Stop'
$utf8Strict = [Text.UTF8Encoding]::new($false, $true)
$utf8NoBom = [Text.UTF8Encoding]::new($false)
$failures = [Collections.Generic.List[string]]::new()

function Add-Failure {
    param([Parameter(Mandatory = $true)][string]$Message)
    $failures.Add($Message)
}

function Invoke-GitRaw {
    param([Parameter(Mandatory = $true)][string[]]$Arguments)

    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = 'git'
    $startInfo.WorkingDirectory = $RepositoryRoot
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

function Get-SourceBytes {
    param([Parameter(Mandatory = $true)][string]$Path)

    if ($Path -eq $ManifestPath -and -not [string]::IsNullOrWhiteSpace($ManifestOverridePath)) {
        return [IO.File]::ReadAllBytes((Resolve-Path -LiteralPath $ManifestOverridePath))
    }
    if ($Path -eq $SumsPath -and -not [string]::IsNullOrWhiteSpace($SumsOverridePath)) {
        return [IO.File]::ReadAllBytes((Resolve-Path -LiteralPath $SumsOverridePath))
    }
    if ($Path -eq $InventoryPath -and -not [string]::IsNullOrWhiteSpace($InventoryOverridePath)) {
        return [IO.File]::ReadAllBytes((Resolve-Path -LiteralPath $InventoryOverridePath))
    }

    if ($SourceMode -eq 'GitIndex') {
        return Invoke-GitRaw -Arguments @('show', ":$Path")
    }
    return Invoke-GitRaw -Arguments @('show', "${Revision}:$Path")
}

function Get-SourceBlobOID {
    param([Parameter(Mandatory = $true)][string]$Path)

    $spec = if ($SourceMode -eq 'GitIndex') { ":$Path" } else { "${Revision}:$Path" }
    return (Convert-BytesToText (Invoke-GitRaw -Arguments @('rev-parse', $spec))).Trim()
}

function Get-ObjectEvidence {
    param([Parameter(Mandatory = $true)][string]$OID)

    $type = (Convert-BytesToText (Invoke-GitRaw -Arguments @('cat-file', '-t', $OID))).Trim()
    if ($type -ne 'blob') {
        throw "object $OID is $type, expected blob"
    }
    $bytes = Invoke-GitRaw -Arguments @('cat-file', 'blob', $OID)
    return [pscustomobject]@{
        bytes = $bytes
        length = $bytes.Length
        sha256 = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant()
    }
}

function Convert-JsonBytes {
    param(
        [Parameter(Mandatory = $true)][byte[]]$Bytes,
        [Parameter(Mandatory = $true)][string]$Label
    )
    try {
        return (Convert-BytesToText $Bytes) | ConvertFrom-Json
    }
    catch {
        throw "$Label is not strict UTF-8 JSON: $($_.Exception.Message)"
    }
}

function Test-ContainsCarriageReturn {
    param([Parameter(Mandatory = $true)][byte[]]$Bytes)

    return ($Bytes -contains [byte]0x0D)
}

function Get-ParentInventory {
    param([Parameter(Mandatory = $true)][string]$ParentSHA)

    $pathsText = Convert-BytesToText (Invoke-GitRaw -Arguments @('ls-tree', '-r', '--name-only', $ParentSHA, '--', 'internal/db/gorm'))
    $paths = @($pathsText -split "`n" | ForEach-Object { $_.TrimEnd("`r") } | Where-Object { $_.EndsWith('_test.go') })
    $entries = @()
    foreach ($path in $paths) {
        $text = Convert-BytesToText (Invoke-GitRaw -Arguments @('show', "${ParentSHA}:$path"))
        $lines = @($text -split "`n")
        $callLines = @()
        for ($index = 0; $index -lt $lines.Count; $index++) {
            $line = $lines[$index].TrimEnd("`r")
            if ($line.Contains('openCandidateTestDB(') -and -not $line.Contains('func openCandidateTestDB(')) {
                $callLines += ($index + 1)
            }
        }
        if ($callLines.Count -gt 0) {
            $entries += [pscustomobject]@{
                path = $path
                count = $callLines.Count
                lines = $callLines
            }
        }
    }
    return @($entries | Sort-Object path)
}

$manifestEntryCount = 0
$sumEntryCount = 0
$actualInventoryCount = 0
$actualInventoryFiles = 0

try {
    $manifestBytes = Get-SourceBytes -Path $ManifestPath
    if (Test-ContainsCarriageReturn -Bytes $manifestBytes) {
        Add-Failure 'manifest bytes contain CR; LF Git blob bytes are required'
    }
    $manifest = Convert-JsonBytes -Bytes $manifestBytes -Label $ManifestPath

    if ($manifest.representation_contract.id -ne 'git-blob-bytes-v1') {
        Add-Failure "unsupported representation contract: $($manifest.representation_contract.id)"
    }
    if ($manifest.representation_contract.digest -ne 'SHA-256') {
        Add-Failure "unsupported digest: $($manifest.representation_contract.digest)"
    }
    if ($manifest.representation_contract.text_git_blob_line_endings -ne 'LF') {
        Add-Failure "unsupported Git blob line-ending contract: $($manifest.representation_contract.text_git_blob_line_endings)"
    }
    if ($manifest.representation_contract.manifest_self_reference -ne 'excluded') {
        Add-Failure 'manifest self-reference must be excluded'
    }
    if ($manifest.representation_contract.outer_checksum_generation_order -ne 'manifest-first-checksum-second') {
        Add-Failure 'outer checksum generation order is not manifest-first-checksum-second'
    }

    $manifestPaths = @{}
    foreach ($entry in @($manifest.entries)) {
        $manifestEntryCount++
        if ($manifestPaths.ContainsKey($entry.path)) {
            Add-Failure "duplicate manifest path: $($entry.path)"
            continue
        }
        $manifestPaths[$entry.path] = $true

        try {
            $object = Get-ObjectEvidence -OID $entry.git_blob_oid
            if ($object.sha256 -ne $entry.sha256) {
                Add-Failure "manifest SHA-256 mismatch: $($entry.path)"
            }
            if ($object.length -ne [int64]$entry.bytes) {
                Add-Failure "manifest byte-count mismatch: $($entry.path)"
            }
            if (Test-ContainsCarriageReturn -Bytes $object.bytes) {
                Add-Failure "manifest entry contains CR; LF Git blob bytes are required: $($entry.path)"
            }
            $pathOID = Get-SourceBlobOID -Path $entry.path
            if ($pathOID -ne $entry.git_blob_oid) {
                Add-Failure "manifest path/blob binding mismatch: $($entry.path)"
            }
        }
        catch {
            Add-Failure "manifest entry unreadable: $($entry.path): $($_.Exception.Message)"
        }
    }

    $inventoryBytes = Get-SourceBytes -Path $InventoryPath
    $inventory = Convert-JsonBytes -Bytes $inventoryBytes -Label $InventoryPath
    $actualInventory = Get-ParentInventory -ParentSHA $inventory.parent_sha
    $actualInventoryCount = [int](($actualInventory | Measure-Object count -Sum).Sum)
    $actualInventoryFiles = $actualInventory.Count

    if ([int]$inventory.required_call_sites -ne 83 -or [int]$inventory.required_files -ne 8) {
        Add-Failure "inventory acceptance constants must be 83/8, got $($inventory.required_call_sites)/$($inventory.required_files)"
    }
    if ([int]$inventory.actual_call_sites -ne $actualInventoryCount -or [int]$inventory.actual_files -ne $actualInventoryFiles) {
        Add-Failure "inventory total mismatch: declared $($inventory.actual_call_sites)/$($inventory.actual_files), actual $actualInventoryCount/$actualInventoryFiles"
    }

    $declaredByPath = @{}
    foreach ($entry in @($inventory.entries)) {
        $declaredByPath[$entry.path] = $entry
    }
    foreach ($actual in $actualInventory) {
        if (-not $declaredByPath.ContainsKey($actual.path)) {
            Add-Failure "inventory missing path: $($actual.path)"
            continue
        }
        $declared = $declaredByPath[$actual.path]
        if ([int]$declared.count -ne [int]$actual.count -or (@($declared.lines) -join ',') -ne (@($actual.lines) -join ',')) {
            Add-Failure "inventory site list mismatch: $($actual.path)"
        }
    }
    foreach ($declaredPath in $declaredByPath.Keys) {
        if (-not @($actualInventory.path).Contains($declaredPath)) {
            Add-Failure "inventory has extra path: $declaredPath"
        }
    }

    $sumsBytes = Get-SourceBytes -Path $SumsPath
    if (Test-ContainsCarriageReturn -Bytes $sumsBytes) {
        Add-Failure 'outer checksum bytes contain CR; LF Git blob bytes are required'
    }
    $sumLines = @((Convert-BytesToText $sumsBytes) -split "`n" | ForEach-Object { $_.TrimEnd("`r") } | Where-Object { $_ -ne '' })
    $headers = @($sumLines | Where-Object { $_.StartsWith('#') })
    foreach ($requiredHeader in @(
        '# representation_contract=git-blob-bytes-v1',
        '# manifest_generation_order=manifest-first-checksum-second',
        '# checksum_self_reference=excluded'
    )) {
        if (-not $headers.Contains($requiredHeader)) {
            Add-Failure "missing checksum header: $requiredHeader"
        }
    }

    $sumMap = @{}
    foreach ($line in @($sumLines | Where-Object { -not $_.StartsWith('#') })) {
        if ($line -notmatch '^([0-9a-f]{64})  (.+)$') {
            Add-Failure "invalid checksum line: $line"
            continue
        }
        $expected = $Matches[1]
        $path = $Matches[2]
        if ($sumMap.ContainsKey($path)) {
            Add-Failure "duplicate checksum path: $path"
            continue
        }
        $sumMap[$path] = $expected
        $sumEntryCount++
        try {
            $actualBytes = Get-SourceBytes -Path $path
            $actualHash = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($actualBytes)).ToLowerInvariant()
            if ($actualHash -ne $expected) {
                Add-Failure "outer checksum mismatch: $path"
            }
        }
        catch {
            Add-Failure "checksum path unreadable: ${path}: $($_.Exception.Message)"
        }
    }

    if ($sumMap.ContainsKey($SumsPath)) {
        Add-Failure 'outer checksum must exclude itself'
    }
    if (-not $sumMap.ContainsKey($ManifestPath)) {
        Add-Failure 'outer checksum does not bind final MANIFEST.json'
    }
    $requiredSumPaths = @($manifestPaths.Keys) + @($ManifestPath)
    foreach ($path in $requiredSumPaths) {
        if (-not $sumMap.ContainsKey($path)) {
            Add-Failure "outer checksum missing manifest-bound path: $path"
        }
    }
    foreach ($path in $sumMap.Keys) {
        if (-not $requiredSumPaths.Contains($path)) {
            Add-Failure "outer checksum contains unbound extra path: $path"
        }
    }
}
catch {
    Add-Failure "verifier exception: $($_.Exception.Message)"
}

$result = [ordered]@{
    generated_utc = [DateTime]::UtcNow.ToString('o')
    status = if ($failures.Count -eq 0) { 'PASS' } else { 'FAIL' }
    source_mode = $SourceMode
    revision = if ($SourceMode -eq 'GitIndex') { 'INDEX' } else { $Revision }
    representation_contract = 'git-blob-bytes-v1'
    manifest_entries = $manifestEntryCount
    checksum_entries = $sumEntryCount
    inventory_call_sites = $actualInventoryCount
    inventory_files = $actualInventoryFiles
    failures = @($failures)
}
$json = (($result | ConvertTo-Json -Depth 8) -replace "`r`n", "`n")
if (-not [string]::IsNullOrWhiteSpace($OutputPath)) {
    $resolvedOutput = [IO.Path]::GetFullPath($OutputPath)
    [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($resolvedOutput)) | Out-Null
    [IO.File]::WriteAllText($resolvedOutput, $json + "`n", $utf8NoBom)
}
if (-not $Quiet) {
    $json
}
if ($failures.Count -ne 0) {
    exit 1
}
exit 0
