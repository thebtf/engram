param(
    [Parameter(Mandatory = $true)]
    [string]$RepositoryRoot,

    [ValidateSet('GitRevision', 'GitIndex')]
    [string]$SourceMode = 'GitRevision',

    [string]$Revision = 'HEAD',

    [string]$ProductCandidateSHA = '276337b3e96aa5af6d2e7dd9a0002ff957e5ffc9',

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
$ordinal = [StringComparer]::Ordinal
$failures = [Collections.Generic.List[string]]::new()
$expectedProductParentSHA = 'bd68c05baf4b7250096dd84f56bebea2aa555970'
$expectedProductBlobPath = 'internal/db/gorm/candidate_store_test.go'
$expectedProductBlobOID = '7337f1bd8da4fb315de842eea2e3cce5476250a3'
$expectedProductBlobSHA256 = '62260c1a2e0705b065295322dd23fcf9b17fd47cb5ebc64134630788e2d23e09'
$expectedManifestExclusions = @($ManifestPath, $SumsPath)
$expectedChecksumExclusions = @($SumsPath)
$requiredDynamicProofPaths = @(
    '.agent/reports/evidence/production-ready/db-test-pool-hygiene/adversarial-proof.json',
    '.agent/reports/evidence/production-ready/db-test-pool-hygiene/verifier-proof.json'
)
$resolvedRepository = (Resolve-Path -LiteralPath $RepositoryRoot).Path

function Add-Failure {
    param([Parameter(Mandatory = $true)][string]$Message)
    $failures.Add($Message)
}

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
    return @((Convert-BytesToText -Bytes $Bytes) -split [char]0 | Where-Object { $_ -ne '' })
}

function Sort-Ordinal {
    param([Parameter(Mandatory = $true)][string[]]$Values)
    $copy = [string[]]@($Values)
    [Array]::Sort($copy, $ordinal)
    return $copy
}

function Test-ContainsCarriageReturn {
    param([Parameter(Mandatory = $true)][byte[]]$Bytes)
    return ($Bytes -contains [byte]0x0D)
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

function Get-ChangedPaths {
    $arguments = if ($SourceMode -eq 'GitIndex') {
        @('diff', '--cached', '--name-only', '-z', $ProductCandidateSHA, '--')
    } else {
        @('diff', '--name-only', '-z', $ProductCandidateSHA, $Revision, '--')
    }
    return Sort-Ordinal -Values (Convert-NulList (Invoke-GitRaw -Arguments $arguments))
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

function Open-JsonDocument {
    param(
        [Parameter(Mandatory = $true)][byte[]]$Bytes,
        [Parameter(Mandatory = $true)][string]$Label
    )
    try {
        return [Text.Json.JsonDocument]::Parse((Convert-BytesToText $Bytes))
    }
    catch {
        throw "$Label is not strict UTF-8 JSON: $($_.Exception.Message)"
    }
}

function Get-RequiredJsonProperty {
    param(
        [Parameter(Mandatory = $true)][Text.Json.JsonElement]$Object,
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][string]$Label
    )

    if ($Object.ValueKind -ne [Text.Json.JsonValueKind]::Object) {
        Add-Failure "$Label must be a JSON object"
        return $null
    }
    foreach ($property in $Object.EnumerateObject()) {
        if ($property.Name -ceq $Name) {
            return $property.Value
        }
    }
    Add-Failure "$Label.$Name is required"
    return $null
}

function Test-JsonKind {
    param(
        $Element,
        [Parameter(Mandatory = $true)][Text.Json.JsonValueKind]$Expected,
        [Parameter(Mandatory = $true)][string]$Label
    )

    if ($null -eq $Element) {
        return $false
    }
    if ($Element.ValueKind -ne $Expected) {
        Add-Failure "$Label must be JSON $($Expected.ToString().ToLowerInvariant()), got $($Element.ValueKind.ToString().ToLowerInvariant())"
        return $false
    }
    return $true
}

function Test-JsonInteger {
    param($Element, [Parameter(Mandatory = $true)][string]$Label)

    if (-not (Test-JsonKind -Element $Element -Expected ([Text.Json.JsonValueKind]::Number) -Label $Label)) {
        return $false
    }
    if ($Element.GetRawText() -notmatch '^(0|[1-9][0-9]*)$') {
        Add-Failure "$Label must be a non-negative JSON integer"
        return $false
    }
    return $true
}

function Test-NoDuplicateJsonProperties {
    param([Parameter(Mandatory = $true)][Text.Json.JsonElement]$Element, [Parameter(Mandatory = $true)][string]$Label)

    if ($Element.ValueKind -eq [Text.Json.JsonValueKind]::Object) {
        $names = [Collections.Generic.HashSet[string]]::new($ordinal)
        foreach ($property in $Element.EnumerateObject()) {
            if (-not $names.Add($property.Name)) {
                Add-Failure "duplicate JSON property: $Label.$($property.Name)"
            }
            Test-NoDuplicateJsonProperties -Element $property.Value -Label "$Label.$($property.Name)"
        }
    } elseif ($Element.ValueKind -eq [Text.Json.JsonValueKind]::Array) {
        $index = 0
        foreach ($item in $Element.EnumerateArray()) {
            Test-NoDuplicateJsonProperties -Element $item -Label "$Label[$index]"
            $index++
        }
    }
}

function Test-ManifestRawSchema {
    param([Parameter(Mandatory = $true)][Text.Json.JsonDocument]$Document)

    $root = $Document.RootElement
    if (-not (Test-JsonKind -Element $root -Expected ([Text.Json.JsonValueKind]::Object) -Label 'manifest')) { return }
    Test-NoDuplicateJsonProperties -Element $root -Label 'manifest'

    foreach ($name in @('schema_version', 'entry_count')) {
        Test-JsonInteger -Element (Get-RequiredJsonProperty -Object $root -Name $name -Label 'manifest') -Label "manifest.$name" | Out-Null
    }
    foreach ($name in @('status', 'product_parent_sha', 'product_candidate_sha', 'evidence_revision_parent_sha', 'evidence_revision_target')) {
        Test-JsonKind -Element (Get-RequiredJsonProperty -Object $root -Name $name -Label 'manifest') -Expected ([Text.Json.JsonValueKind]::String) -Label "manifest.$name" | Out-Null
    }

    $delta = Get-RequiredJsonProperty -Object $root -Name 'evidence_delta' -Label 'manifest'
    if (Test-JsonKind -Element $delta -Expected ([Text.Json.JsonValueKind]::Object) -Label 'manifest.evidence_delta') {
        Test-JsonKind -Element (Get-RequiredJsonProperty -Object $delta -Name 'comparison' -Label 'manifest.evidence_delta') -Expected ([Text.Json.JsonValueKind]::String) -Label 'manifest.evidence_delta.comparison' | Out-Null
        foreach ($name in @('changed_path_count', 'directly_manifest_bound_count')) {
            Test-JsonInteger -Element (Get-RequiredJsonProperty -Object $delta -Name $name -Label 'manifest.evidence_delta') -Label "manifest.evidence_delta.$name" | Out-Null
        }
        foreach ($name in @('product_or_test_paths_changed', 'manifest_entry_self_excluded_paths', 'checksum_self_excluded_paths')) {
            $array = Get-RequiredJsonProperty -Object $delta -Name $name -Label 'manifest.evidence_delta'
            if (Test-JsonKind -Element $array -Expected ([Text.Json.JsonValueKind]::Array) -Label "manifest.evidence_delta.$name") {
                $index = 0
                foreach ($item in $array.EnumerateArray()) {
                    Test-JsonKind -Element $item -Expected ([Text.Json.JsonValueKind]::String) -Label "manifest.evidence_delta.$name[$index]" | Out-Null
                    $index++
                }
            }
        }
    }

    $representation = Get-RequiredJsonProperty -Object $root -Name 'representation_contract' -Label 'manifest'
    if (Test-JsonKind -Element $representation -Expected ([Text.Json.JsonValueKind]::Object) -Label 'manifest.representation_contract') {
        foreach ($name in @('id', 'digest', 'object_type', 'path_binding', 'contract_bytes', 'working_tree_bytes', 'text_git_blob_line_endings', 'manifest_self_reference', 'outer_checksum_path', 'outer_checksum_generation_order', 'outer_checksum_self_reference')) {
            Test-JsonKind -Element (Get-RequiredJsonProperty -Object $representation -Name $name -Label 'manifest.representation_contract') -Expected ([Text.Json.JsonValueKind]::String) -Label "manifest.representation_contract.$name" | Out-Null
        }
    }

    $productBlob = Get-RequiredJsonProperty -Object $root -Name 'product_test_blob' -Label 'manifest'
    if (Test-JsonKind -Element $productBlob -Expected ([Text.Json.JsonValueKind]::Object) -Label 'manifest.product_test_blob') {
        foreach ($name in @('path', 'git_blob_oid', 'sha256')) {
            Test-JsonKind -Element (Get-RequiredJsonProperty -Object $productBlob -Name $name -Label 'manifest.product_test_blob') -Expected ([Text.Json.JsonValueKind]::String) -Label "manifest.product_test_blob.$name" | Out-Null
        }
        Test-JsonKind -Element (Get-RequiredJsonProperty -Object $productBlob -Name 'byte_identical_to_product_candidate' -Label 'manifest.product_test_blob') -Expected ([Text.Json.JsonValueKind]::True) -Label 'manifest.product_test_blob.byte_identical_to_product_candidate' | Out-Null
    }

    $inventory = Get-RequiredJsonProperty -Object $root -Name 'inventory' -Label 'manifest'
    if (Test-JsonKind -Element $inventory -Expected ([Text.Json.JsonValueKind]::Object) -Label 'manifest.inventory') {
        foreach ($name in @('parent_sha', 'path')) {
            Test-JsonKind -Element (Get-RequiredJsonProperty -Object $inventory -Name $name -Label 'manifest.inventory') -Expected ([Text.Json.JsonValueKind]::String) -Label "manifest.inventory.$name" | Out-Null
        }
        foreach ($name in @('required_call_sites', 'required_files')) {
            Test-JsonInteger -Element (Get-RequiredJsonProperty -Object $inventory -Name $name -Label 'manifest.inventory') -Label "manifest.inventory.$name" | Out-Null
        }
    }

    $entries = Get-RequiredJsonProperty -Object $root -Name 'entries' -Label 'manifest'
    if (Test-JsonKind -Element $entries -Expected ([Text.Json.JsonValueKind]::Array) -Label 'manifest.entries') {
        $index = 0
        foreach ($entry in $entries.EnumerateArray()) {
            if (Test-JsonKind -Element $entry -Expected ([Text.Json.JsonValueKind]::Object) -Label "manifest.entries[$index]") {
                foreach ($name in @('path', 'git_blob_oid', 'sha256')) {
                    Test-JsonKind -Element (Get-RequiredJsonProperty -Object $entry -Name $name -Label "manifest.entries[$index]") -Expected ([Text.Json.JsonValueKind]::String) -Label "manifest.entries[$index].$name" | Out-Null
                }
                Test-JsonInteger -Element (Get-RequiredJsonProperty -Object $entry -Name 'bytes' -Label "manifest.entries[$index]") -Label "manifest.entries[$index].bytes" | Out-Null
            }
            $index++
        }
    }
}

function Test-InventoryRawSchema {
    param([Parameter(Mandatory = $true)][Text.Json.JsonDocument]$Document)

    $root = $Document.RootElement
    if (-not (Test-JsonKind -Element $root -Expected ([Text.Json.JsonValueKind]::Object) -Label 'inventory')) { return }
    Test-NoDuplicateJsonProperties -Element $root -Label 'inventory'
    foreach ($name in @('schema_version', 'required_call_sites', 'required_files', 'actual_call_sites', 'actual_files')) {
        Test-JsonInteger -Element (Get-RequiredJsonProperty -Object $root -Name $name -Label 'inventory') -Label "inventory.$name" | Out-Null
    }
    foreach ($name in @('parent_sha', 'symbol', 'definition', 'method')) {
        Test-JsonKind -Element (Get-RequiredJsonProperty -Object $root -Name $name -Label 'inventory') -Expected ([Text.Json.JsonValueKind]::String) -Label "inventory.$name" | Out-Null
    }
    $entries = Get-RequiredJsonProperty -Object $root -Name 'entries' -Label 'inventory'
    if (Test-JsonKind -Element $entries -Expected ([Text.Json.JsonValueKind]::Array) -Label 'inventory.entries') {
        $index = 0
        foreach ($entry in $entries.EnumerateArray()) {
            if (Test-JsonKind -Element $entry -Expected ([Text.Json.JsonValueKind]::Object) -Label "inventory.entries[$index]") {
                Test-JsonKind -Element (Get-RequiredJsonProperty -Object $entry -Name 'path' -Label "inventory.entries[$index]") -Expected ([Text.Json.JsonValueKind]::String) -Label "inventory.entries[$index].path" | Out-Null
                Test-JsonInteger -Element (Get-RequiredJsonProperty -Object $entry -Name 'count' -Label "inventory.entries[$index]") -Label "inventory.entries[$index].count" | Out-Null
                $lines = Get-RequiredJsonProperty -Object $entry -Name 'lines' -Label "inventory.entries[$index]"
                if (Test-JsonKind -Element $lines -Expected ([Text.Json.JsonValueKind]::Array) -Label "inventory.entries[$index].lines") {
                    $lineIndex = 0
                    foreach ($line in $lines.EnumerateArray()) {
                        Test-JsonInteger -Element $line -Label "inventory.entries[$index].lines[$lineIndex]" | Out-Null
                        $lineIndex++
                    }
                }
            }
            $index++
        }
    }
}

function Test-ExactStringArray {
    param(
        [Parameter(Mandatory = $true)][object[]]$Actual,
        [Parameter(Mandatory = $true)][string[]]$Expected,
        [Parameter(Mandatory = $true)][string]$Label
    )

    if ($Actual.Count -ne $Expected.Count) {
        Add-Failure "$Label must contain exactly $($Expected.Count) item(s)"
        return
    }
    for ($index = 0; $index -lt $Expected.Count; $index++) {
        if ([string]$Actual[$index] -cne $Expected[$index]) {
            Add-Failure "$Label mismatch at index $index"
        }
    }
}

function Test-StrictOrdinalOrder {
    param([Parameter(Mandatory = $true)][string[]]$Paths, [Parameter(Mandatory = $true)][string]$Label)

    for ($index = 1; $index -lt $Paths.Count; $index++) {
        $comparison = $ordinal.Compare($Paths[$index - 1], $Paths[$index])
        if ($comparison -eq 0) {
            Add-Failure "duplicate $Label path: $($Paths[$index])"
        } elseif ($comparison -gt 0) {
            Add-Failure "$Label paths are not canonical ordinal order: $($Paths[$index - 1]) before $($Paths[$index])"
        }
    }
}

function Get-ParentInventory {
    param([Parameter(Mandatory = $true)][string]$ParentSHA)

    $pathsText = Convert-BytesToText (Invoke-GitRaw -Arguments @('ls-tree', '-r', '--name-only', $ParentSHA, '--', 'internal/db/gorm'))
    $paths = @($pathsText -split "`n" | ForEach-Object { $_.TrimEnd("`r") } | Where-Object { $_.EndsWith('_test.go', [StringComparison]::Ordinal) })
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
            $entries += [pscustomobject]@{ path = $path; count = $callLines.Count; lines = $callLines }
        }
    }
    return @($entries | Sort-Object path)
}

$manifestEntryCount = 0
$sumEntryCount = 0
$actualInventoryCount = 0
$actualInventoryFiles = 0
$changedPathCount = 0
$directlyBoundChangedPathCount = 0

try {
    $manifestBytes = Get-SourceBytes -Path $ManifestPath
    if (Test-ContainsCarriageReturn -Bytes $manifestBytes) {
        Add-Failure 'manifest bytes contain CR; LF Git blob bytes are required'
    }
    $manifestDocument = Open-JsonDocument -Bytes $manifestBytes -Label $ManifestPath
    try { Test-ManifestRawSchema -Document $manifestDocument } finally { $manifestDocument.Dispose() }
    $manifest = Convert-JsonBytes -Bytes $manifestBytes -Label $ManifestPath

    if ([int64]$manifest.schema_version -ne 3) { Add-Failure 'manifest schema_version must be 3' }
    if ($manifest.product_parent_sha -cne $expectedProductParentSHA) { Add-Failure 'manifest product_parent_sha mismatch' }
    if ($manifest.product_candidate_sha -cne $ProductCandidateSHA) { Add-Failure 'manifest product_candidate_sha mismatch' }
    if ($manifest.representation_contract.id -cne 'git-blob-bytes-v1') { Add-Failure "unsupported representation contract: $($manifest.representation_contract.id)" }
    if ($manifest.representation_contract.digest -cne 'SHA-256') { Add-Failure "unsupported digest: $($manifest.representation_contract.digest)" }
    if ($manifest.representation_contract.text_git_blob_line_endings -cne 'LF') { Add-Failure 'unsupported Git blob line-ending contract' }
    if ($manifest.representation_contract.manifest_self_reference -cne 'excluded-from-manifest-entries-bound-by-outer-checksum') { Add-Failure 'manifest self-reference contract mismatch' }
    if ($manifest.representation_contract.outer_checksum_generation_order -cne 'manifest-first-checksum-second') { Add-Failure 'outer checksum generation order mismatch' }
    if ($manifest.representation_contract.outer_checksum_self_reference -cne 'excluded') { Add-Failure 'outer checksum self-reference contract mismatch' }
    Test-ExactStringArray -Actual @($manifest.evidence_delta.manifest_entry_self_excluded_paths) -Expected $expectedManifestExclusions -Label 'manifest entry self exclusions'
    Test-ExactStringArray -Actual @($manifest.evidence_delta.checksum_self_excluded_paths) -Expected $expectedChecksumExclusions -Label 'checksum self exclusions'

    $changedPaths = Get-ChangedPaths
    $changedPathCount = $changedPaths.Count
    if ([int64]$manifest.evidence_delta.changed_path_count -ne $changedPathCount) {
        Add-Failure "changed path count mismatch: declared $($manifest.evidence_delta.changed_path_count), actual $changedPathCount"
    }
    $productOrTestChanges = @($changedPaths | Where-Object { -not $_.StartsWith('.agent/reports/', [StringComparison]::Ordinal) })
    if ($productOrTestChanges.Count -ne 0) {
        Add-Failure "evidence revision changes product/test paths: $($productOrTestChanges -join ', ')"
    }
    if (@($manifest.evidence_delta.product_or_test_paths_changed).Count -ne 0) {
        Add-Failure 'manifest product_or_test_paths_changed must be an empty JSON array'
    }

    $manifestPaths = @{}
    $manifestPathOrder = [Collections.Generic.List[string]]::new()
    foreach ($entry in @($manifest.entries)) {
        $manifestEntryCount++
        $path = [string]$entry.path
        $manifestPathOrder.Add($path)
        if ($manifestPaths.ContainsKey($path)) {
            Add-Failure "duplicate manifest path: $path"
            continue
        }
        $manifestPaths[$path] = $true
        if ($expectedManifestExclusions.Contains($path)) {
            Add-Failure "manifest contains self-excluded path: $path"
        }
        try {
            $object = Get-ObjectEvidence -OID ([string]$entry.git_blob_oid)
            if ($object.sha256 -cne [string]$entry.sha256) { Add-Failure "manifest SHA-256 mismatch: $path" }
            if ($object.length -ne [int64]$entry.bytes) { Add-Failure "manifest byte-count mismatch: $path" }
            if (Test-ContainsCarriageReturn -Bytes $object.bytes) { Add-Failure "manifest entry contains CR; LF Git blob bytes are required: $path" }
            $pathOID = Get-SourceBlobOID -Path $path
            if ($pathOID -cne [string]$entry.git_blob_oid) { Add-Failure "manifest path/blob binding mismatch: $path" }
        }
        catch {
            Add-Failure "manifest entry unreadable: ${path}: $($_.Exception.Message)"
        }
    }
    Test-StrictOrdinalOrder -Paths @($manifestPathOrder) -Label 'manifest'
    if ([int64]$manifest.entry_count -ne $manifestEntryCount) { Add-Failure 'manifest entry_count mismatch' }

    $directChangedPaths = @($changedPaths | Where-Object { -not $expectedManifestExclusions.Contains($_) })
    $directlyBoundChangedPathCount = $directChangedPaths.Count
    if ([int64]$manifest.evidence_delta.directly_manifest_bound_count -ne $directlyBoundChangedPathCount) {
        Add-Failure 'directly_manifest_bound_count mismatch'
    }
    foreach ($path in $directChangedPaths) {
        if (-not $manifestPaths.ContainsKey($path)) { Add-Failure "manifest missing changed path: $path" }
    }
    foreach ($path in $requiredDynamicProofPaths) {
        if (-not $manifestPaths.ContainsKey($path)) { Add-Failure "manifest missing dynamic proof path: $path" }
    }

    if ($manifest.product_test_blob.path -cne $expectedProductBlobPath -or
        $manifest.product_test_blob.git_blob_oid -cne $expectedProductBlobOID -or
        $manifest.product_test_blob.sha256 -cne $expectedProductBlobSHA256 -or
        $manifest.product_test_blob.byte_identical_to_product_candidate -ne $true) {
        Add-Failure 'manifest product test blob contract mismatch'
    }
    $productOID = Get-SourceBlobOID -Path $expectedProductBlobPath
    $productObject = Get-ObjectEvidence -OID $productOID
    if ($productOID -cne $expectedProductBlobOID -or $productObject.sha256 -cne $expectedProductBlobSHA256) {
        Add-Failure 'product test blob differs from accepted product candidate'
    }

    $inventoryBytes = Get-SourceBytes -Path $InventoryPath
    if (Test-ContainsCarriageReturn -Bytes $inventoryBytes) { Add-Failure 'inventory bytes contain CR; LF Git blob bytes are required' }
    $inventoryDocument = Open-JsonDocument -Bytes $inventoryBytes -Label $InventoryPath
    try { Test-InventoryRawSchema -Document $inventoryDocument } finally { $inventoryDocument.Dispose() }
    $inventory = Convert-JsonBytes -Bytes $inventoryBytes -Label $InventoryPath
    if ($inventory.parent_sha -cne $expectedProductParentSHA) { Add-Failure 'inventory parent_sha mismatch' }
    $actualInventory = Get-ParentInventory -ParentSHA $expectedProductParentSHA
    $actualInventoryCount = [int](($actualInventory | Measure-Object count -Sum).Sum)
    $actualInventoryFiles = $actualInventory.Count
    if ([int64]$inventory.required_call_sites -ne 83 -or [int64]$inventory.required_files -ne 8) {
        Add-Failure "inventory acceptance constants must be 83/8, got $($inventory.required_call_sites)/$($inventory.required_files)"
    }
    if ([int64]$inventory.actual_call_sites -ne $actualInventoryCount -or [int64]$inventory.actual_files -ne $actualInventoryFiles) {
        Add-Failure "inventory total mismatch: declared $($inventory.actual_call_sites)/$($inventory.actual_files), actual $actualInventoryCount/$actualInventoryFiles"
    }

    $declaredByPath = @{}
    $inventoryPathOrder = [Collections.Generic.List[string]]::new()
    foreach ($entry in @($inventory.entries)) {
        $path = [string]$entry.path
        $inventoryPathOrder.Add($path)
        if ($declaredByPath.ContainsKey($path)) {
            Add-Failure "duplicate inventory path: $path"
        } else {
            $declaredByPath[$path] = $entry
        }
    }
    Test-StrictOrdinalOrder -Paths @($inventoryPathOrder) -Label 'inventory'
    foreach ($actual in $actualInventory) {
        if (-not $declaredByPath.ContainsKey($actual.path)) {
            Add-Failure "inventory missing path: $($actual.path)"
            continue
        }
        $declared = $declaredByPath[$actual.path]
        if ([int64]$declared.count -ne [int64]$actual.count -or (@($declared.lines) -join ',') -cne (@($actual.lines) -join ',')) {
            Add-Failure "inventory site list mismatch: $($actual.path)"
        }
    }
    foreach ($declaredPath in $declaredByPath.Keys) {
        if (-not @($actualInventory.path).Contains($declaredPath)) { Add-Failure "inventory has extra path: $declaredPath" }
    }

    $sumsBytes = Get-SourceBytes -Path $SumsPath
    if (Test-ContainsCarriageReturn -Bytes $sumsBytes) { Add-Failure 'outer checksum bytes contain CR; LF Git blob bytes are required' }
    $sumLines = @((Convert-BytesToText $sumsBytes) -split "`n" | Where-Object { $_ -ne '' })
    $expectedHeaders = @(
        '# representation_contract=git-blob-bytes-v1',
        '# manifest_generation_order=manifest-first-checksum-second',
        '# checksum_self_reference=excluded'
    )
    for ($index = 0; $index -lt $expectedHeaders.Count; $index++) {
        if ($sumLines.Count -le $index -or $sumLines[$index] -cne $expectedHeaders[$index]) {
            Add-Failure "checksum header mismatch at index $index"
        }
    }
    if (@($sumLines | Where-Object { $_.StartsWith('#') }).Count -ne $expectedHeaders.Count) {
        Add-Failure 'outer checksum must contain exactly the canonical three headers'
    }

    $sumMap = @{}
    $sumPathOrder = [Collections.Generic.List[string]]::new()
    foreach ($line in @($sumLines | Where-Object { -not $_.StartsWith('#') })) {
        if ($line -notmatch '^([0-9a-f]{64})  (.+)$') {
            Add-Failure "invalid checksum line: $line"
            continue
        }
        $expected = $Matches[1]
        $path = $Matches[2]
        $sumPathOrder.Add($path)
        if ($sumMap.ContainsKey($path)) {
            Add-Failure "duplicate checksum path: $path"
            continue
        }
        $sumMap[$path] = $expected
        $sumEntryCount++
        try {
            $actualBytes = Get-SourceBytes -Path $path
            $actualHash = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($actualBytes)).ToLowerInvariant()
            if ($actualHash -cne $expected) { Add-Failure "outer checksum mismatch: $path" }
        }
        catch {
            Add-Failure "checksum path unreadable: ${path}: $($_.Exception.Message)"
        }
    }
    Test-StrictOrdinalOrder -Paths @($sumPathOrder) -Label 'checksum'
    if ($sumMap.ContainsKey($SumsPath)) { Add-Failure 'outer checksum must exclude itself' }
    if (-not $sumMap.ContainsKey($ManifestPath)) { Add-Failure 'outer checksum does not bind final MANIFEST.json' }
    $requiredSumPaths = @($manifestPathOrder) + @($ManifestPath)
    foreach ($path in $requiredSumPaths) {
        if (-not $sumMap.ContainsKey($path)) { Add-Failure "outer checksum missing manifest-bound path: $path" }
    }
    foreach ($path in $sumMap.Keys) {
        if (-not $requiredSumPaths.Contains($path)) { Add-Failure "outer checksum contains unbound extra path: $path" }
    }
}
catch {
    Add-Failure "verifier exception: $($_.Exception.Message)"
}

$result = [ordered]@{
    schema_version = 3
    status = if ($failures.Count -eq 0) { 'PASS' } else { 'FAIL' }
    source_mode = $SourceMode
    revision = if ($SourceMode -eq 'GitIndex') { 'INDEX' } else { $Revision }
    product_candidate_sha = $ProductCandidateSHA
    representation_contract = 'git-blob-bytes-v1'
    changed_paths = $changedPathCount
    directly_bound_changed_paths = $directlyBoundChangedPathCount
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
if (-not $Quiet) { $json }
if ($failures.Count -ne 0) { exit 1 }
exit 0
