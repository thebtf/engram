[CmdletBinding(DefaultParameterSetName='Transition')]
param(
    [Parameter(Mandatory)][string]$Repository,
    [Parameter(Mandatory)][string]$Remote,
    [Parameter(Mandatory)][string]$BaseRemoteRef,
    [Parameter(Mandatory)][ValidatePattern('^[0-9a-f]{40}$')][string]$BaseSha,
    [Parameter(Mandatory, ParameterSetName='Transition')][string]$HeadRemoteRef,
    [Parameter(Mandatory, ParameterSetName='Transition')][ValidatePattern('^[0-9a-f]{40}$')][string]$HeadSha,
    [string]$ExpectedDefaultBranch = 'main',
    [Parameter(Mandatory, ParameterSetName='Transition')][string]$EventRepositoryFullName,
    [Parameter(Mandatory, ParameterSetName='Transition')][string]$EventHeadRepositoryFullName,
    [Parameter(Mandatory, ParameterSetName='Transition')][string]$ActorLogin,
    [Parameter(Mandatory, ParameterSetName='Transition')][long]$ActorId,
    [Parameter(Mandatory, ParameterSetName='Transition')][string]$ActorType,
    [Parameter(Mandatory, ParameterSetName='Transition')][string]$AuthorAssociation,
    [Parameter(Mandatory, ParameterSetName='Transition')][string]$ApprovalLabel,
    [Parameter(Mandatory, ParameterSetName='Transition')][string]$ApprovalEpoch,
    [Parameter(Mandatory, ParameterSetName='BaseOnly')][switch]$ValidateBaseOnly,
    [string]$ValidatorPath = 'scripts/production-gates/assert-pr-authority-maintenance.ps1',
    [Parameter(Mandatory)][ValidatePattern('^[0-9a-f]{40}$')][string]$ExpectedValidatorGitBlob,
    [string]$Artifact = '.agent/e/rg17/pr-authority-maintenance.json'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Write-Utf8NoBom {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$Text)
    $parent = Split-Path -Parent $Path
    if ($parent) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }
    [System.IO.File]::WriteAllText([System.IO.Path]::GetFullPath($Path), $Text, [System.Text.UTF8Encoding]::new($false))
}

function Invoke-Git {
    param([Parameter(Mandatory)][string]$WorkingTree, [Parameter(Mandatory)][string[]]$Arguments, [switch]$AllowFailure)
    $output = @(& git -C $WorkingTree @Arguments 2>&1)
    $code = $LASTEXITCODE
    if (-not $AllowFailure -and $code -ne 0) { throw "git $($Arguments -join ' ') failed ($code): $($output -join ' ')" }
    return [pscustomobject][ordered]@{ exit_code = $code; output = @($output | ForEach-Object { [string]$_ }) }
}

function Export-GitBlob {
    param([Parameter(Mandatory)][string]$WorkingTree, [Parameter(Mandatory)][string]$Spec, [Parameter(Mandatory)][string]$Destination)
    $parent = Split-Path -Parent $Destination
    if ($parent) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }
    $start = [System.Diagnostics.ProcessStartInfo]::new()
    $start.FileName = 'git'; $start.UseShellExecute = $false; $start.RedirectStandardOutput = $true; $start.RedirectStandardError = $true
    foreach ($argument in @('-C',$WorkingTree,'cat-file','blob',$Spec)) { [void]$start.ArgumentList.Add($argument) }
    $process = [System.Diagnostics.Process]::new(); $process.StartInfo = $start
    if (-not $process.Start()) { throw "could not start git cat-file for '$Spec'" }
    $stream = [System.IO.File]::Open($Destination, [System.IO.FileMode]::Create, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
    try { $process.StandardOutput.BaseStream.CopyTo($stream) } finally { $stream.Dispose() }
    $stderr = $process.StandardError.ReadToEnd(); $process.WaitForExit()
    if ($process.ExitCode -ne 0) { throw "git cat-file failed for '$Spec': $stderr" }
}

function Get-CanonicalTextSha256 {
    param([Parameter(Mandatory)][string]$Path)
    $text = [System.IO.File]::ReadAllText([System.IO.Path]::GetFullPath($Path))
    $canonical = ($text -replace "`r`n", "`n") -replace "`r", "`n"
    return [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData([System.Text.UTF8Encoding]::new($false).GetBytes($canonical))).ToLowerInvariant()
}

function Assert-NoDuplicateJsonProperties {
    param([Parameter(Mandatory)][string]$Text, [Parameter(Mandatory)][string]$Context)
    $document = [System.Text.Json.JsonDocument]::Parse($Text)
    try {
        $walk = $null
        $walk = {
            param([System.Text.Json.JsonElement]$Element, [string]$Path)
            if ($Element.ValueKind -eq [System.Text.Json.JsonValueKind]::Object) {
                $seen = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
                foreach ($property in $Element.EnumerateObject()) {
                    if (-not $seen.Add($property.Name)) { throw "$Context contains duplicate property '$($property.Name)' at '$Path'" }
                    & $walk $property.Value "$Path.$($property.Name)"
                }
            }
            elseif ($Element.ValueKind -eq [System.Text.Json.JsonValueKind]::Array) {
                $index = 0
                foreach ($item in $Element.EnumerateArray()) { & $walk $item "$Path[$index]"; $index++ }
            }
        }
        & $walk $document.RootElement '$'
    }
    finally { $document.Dispose() }
}

function Assert-ObjectShape {
    param([Parameter(Mandatory)]$Object, [Parameter(Mandatory)][string[]]$Names, [Parameter(Mandatory)][string]$Context)
    if ($Object -isnot [pscustomobject]) { throw "$Context must be a JSON object" }
    [string[]]$actual = @($Object.PSObject.Properties.Name); [string[]]$expected = @($Names)
    [Array]::Sort($actual, [System.StringComparer]::Ordinal); [Array]::Sort($expected, [System.StringComparer]::Ordinal)
    if (($actual -join "`n") -cne ($expected -join "`n")) { throw "$Context property set drifted: expected=[$($expected -join ', ')] observed=[$($actual -join ', ')]" }
}

function Assert-JsonArray {
    param([Parameter(Mandatory)][AllowNull()]$Value, [Parameter(Mandatory)][string]$Context)
    if ($Value -isnot [System.Array]) { throw "$Context must be a JSON array" }
}

function Assert-JsonBoolean {
    param([Parameter(Mandatory)][AllowNull()]$Value, [Parameter(Mandatory)][bool]$Expected, [Parameter(Mandatory)][string]$Context)
    if ($Value -isnot [bool] -or [bool]$Value -ne $Expected) { throw "$Context must be the JSON boolean '$($Expected.ToString().ToLowerInvariant())'" }
}

function Assert-JsonInteger {
    param([Parameter(Mandatory)][AllowNull()]$Value, [Parameter(Mandatory)][long]$Expected, [Parameter(Mandatory)][string]$Context)
    if (($Value -isnot [int] -and $Value -isnot [long]) -or [long]$Value -ne $Expected) { throw "$Context must be the JSON integer $Expected" }
}

function Assert-JsonString {
    param([Parameter(Mandatory)][AllowNull()]$Value, [Parameter(Mandatory)][string]$Context)
    if ($Value -isnot [string]) { throw "$Context must be a JSON string" }
}

function Get-CanonicalJsonText {
    param([AllowNull()]$Value)
    if ($null -eq $Value) { return 'null' }
    if ($Value -is [string]) { return ($Value | ConvertTo-Json -Compress) }
    if ($Value -is [bool]) { return $(if ($Value) { 'true' } else { 'false' }) }
    if ($Value -is [pscustomobject]) {
        [string[]]$names = @($Value.PSObject.Properties.Name); [Array]::Sort($names, [System.StringComparer]::Ordinal)
        $parts = foreach ($name in $names) { "$(($name | ConvertTo-Json -Compress)):$(Get-CanonicalJsonText $Value.$name)" }
        return '{' + ($parts -join ',') + '}'
    }
    if ($Value -is [System.Collections.IDictionary]) {
        [string[]]$names = @($Value.Keys | ForEach-Object { [string]$_ }); [Array]::Sort($names, [System.StringComparer]::Ordinal)
        $parts = foreach ($name in $names) { "$(($name | ConvertTo-Json -Compress)):$(Get-CanonicalJsonText $Value[$name])" }
        return '{' + ($parts -join ',') + '}'
    }
    if ($Value -is [System.Collections.IEnumerable]) {
        $parts = foreach ($item in $Value) { Get-CanonicalJsonText $item }
        return '[' + ($parts -join ',') + ']'
    }
    return ($Value | ConvertTo-Json -Compress)
}

function Get-SemanticDigest {
    param([AllowNull()]$Value)
    $bytes = [System.Text.UTF8Encoding]::new($false).GetBytes((Get-CanonicalJsonText $Value))
    return [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant()
}

function Assert-CanonicalPath {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$Context)
    if ([string]::IsNullOrWhiteSpace($Path) -or $Path.Contains('\') -or $Path.Contains('//') -or $Path.StartsWith('/') -or $Path.EndsWith('/') -or $Path -match '(^|/)(\.|\.\.)(/|$)') { throw "$Context contains non-canonical path '$Path'" }
}

function Test-ProtectedPath {
    param([Parameter(Mandatory)][string]$Path)
    return $Path.StartsWith('.github/workflows/', [System.StringComparison]::Ordinal) -or $Path.StartsWith('scripts/production-gates/', [System.StringComparison]::Ordinal) -or $Path.StartsWith('.agent/plans/', [System.StringComparison]::Ordinal) -or $Path.StartsWith('.agent/specs/release-gates-r12/evidence/plan-governance/', [System.StringComparison]::Ordinal)
}

function ConvertTo-Rfc3339 {
    param([Parameter(Mandatory)]$Value, [Parameter(Mandatory)][string]$Context)
    if ($Value -is [DateTimeOffset]) {
        if ($Value.Offset -ne [TimeSpan]::Zero) { throw "$Context must be an RFC3339 UTC timestamp" }
        return $Value.ToUniversalTime()
    }
    if ($Value -is [DateTime]) {
        if ($Value.Kind -ne [DateTimeKind]::Utc) { throw "$Context must be an RFC3339 UTC timestamp" }
        return [DateTimeOffset]::new($Value)
    }
    if ($Value -isnot [string]) { throw "$Context must be an RFC3339 UTC timestamp string" }
    if ($Value -notmatch '^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,7})?Z$') { throw "$Context must be an RFC3339 UTC timestamp" }
    $parsed = [DateTimeOffset]::MinValue
    if (-not [DateTimeOffset]::TryParse($Value, [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::AssumeUniversal, [ref]$parsed)) { throw "$Context is not a valid timestamp" }
    return $parsed.ToUniversalTime()
}

function Get-DiffEntries {
    param([Parameter(Mandatory)][string]$WorkingTree, [Parameter(Mandatory)][string]$Base, [Parameter(Mandatory)][string]$Head)
    $result = Invoke-Git $WorkingTree @('-c','core.quotepath=false','diff','--name-status','--find-renames','--find-copies',"$Base..$Head",'--')
    $entries = [System.Collections.Generic.List[object]]::new()
    foreach ($line in $result.output) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        $parts = $line -split "`t"
        if ($parts.Count -ne 2 -or $parts[0] -notmatch '^[AM]$') { throw "unsupported maintenance diff line '$line'; only literal add/modify entries are allowed" }
        $path = [string]$parts[1]; Assert-CanonicalPath $path 'maintenance diff'
        if (-not (Test-ProtectedPath $path)) { throw "maintenance diff path '$path' is outside the protected control-plane namespaces" }
        $headEntry = Invoke-Git $WorkingTree @('-c','core.quotepath=false','ls-tree','--full-tree',$Head,'--',":(literal)$path")
        if ($headEntry.output.Count -ne 1 -or [string]$headEntry.output[0] -cnotmatch '^(?<mode>100644|100755) blob (?<object>[0-9a-f]{40})\t(?<path>.+)$') { throw "maintenance path '$path' is not one regular head blob" }
        if ([string]$Matches.path -cne $path) { throw "maintenance path '$path' resolved as '$([string]$Matches.path)'" }
        $actualType = Invoke-Git $WorkingTree @('cat-file','-t',[string]$Matches.object) -AllowFailure
        if ($actualType.exit_code -ne 0 -or $actualType.output.Count -ne 1 -or [string]$actualType.output[0] -cne 'blob') { throw "maintenance path '$path' head object is unavailable or not a blob" }
        $entries.Add([pscustomobject][ordered]@{ status=[string]$parts[0]; path=$path; mode=[string]$Matches.mode; git_blob=[string]$Matches.object })
    }
    if ($entries.Count -eq 0) { throw 'maintenance diff contains zero paths' }
    return @($entries)
}

function Get-ChangeSignature {
    param([Parameter(Mandatory)][object[]]$Entries)
    [string[]]$values = @($Entries | ForEach-Object { "$([string]$_.status)`t$([string]$_.path)" }); [Array]::Sort($values, [System.StringComparer]::Ordinal)
    return ($values -join "`n")
}

function Get-BlobSignature {
    param([Parameter(Mandatory)][object[]]$Entries, [switch]$Protected)
    [string[]]$values = if ($Protected) { @($Entries | ForEach-Object { "$([string]$_.status)`t$([string]$_.path)`t$([string]$_.mode)`t$([string]$_.git_blob)" }) } else { @($Entries | ForEach-Object { "$([string]$_.path)`t$([string]$_.git_blob)" }) }
    [Array]::Sort($values, [System.StringComparer]::Ordinal); return ($values -join "`n")
}

function Assert-ChangeArray {
    param([Parameter(Mandatory)][object[]]$Entries, [Parameter(Mandatory)][string]$Context, [switch]$AllowAdd)
    $seen = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    foreach ($entry in $Entries) {
        Assert-ObjectShape $entry @('status','path') "$Context entry"
        $status = [string]$entry.status; $path = [string]$entry.path
        if ($status -cne 'M' -and (-not $AllowAdd -or $status -cne 'A')) { throw "$Context contains forbidden status '$status'" }
        Assert-CanonicalPath $path $Context
        if (-not (Test-ProtectedPath $path)) { throw "$Context path '$path' is outside the protected namespaces" }
        if (-not $seen.Add($path)) { throw "$Context contains duplicate path '$path'" }
    }
}

function Assert-ValidatorBlobArray {
    param([Parameter(Mandatory)][object[]]$Entries, [Parameter(Mandatory)][string]$Context)
    $seen = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    foreach ($entry in $Entries) {
        Assert-ObjectShape $entry @('path','git_blob') "$Context entry"
        Assert-CanonicalPath ([string]$entry.path) $Context
        if ([string]$entry.git_blob -cnotmatch '^[0-9a-f]{40}$') { throw "$Context contains invalid git blob '$([string]$entry.git_blob)'" }
        if (-not $seen.Add([string]$entry.path)) { throw "$Context contains duplicate path '$([string]$entry.path)'" }
    }
}

function Assert-ProtectedBlobArray {
    param([Parameter(Mandatory)][object[]]$Entries, [Parameter(Mandatory)][string]$Context)
    $seen = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    foreach ($entry in $Entries) {
        Assert-ObjectShape $entry @('status','path','mode','git_blob') "$Context entry"
        if ([string]$entry.status -cnotin @('A','M')) { throw "$Context contains invalid status '$([string]$entry.status)'" }
        Assert-CanonicalPath ([string]$entry.path) $Context
        if (-not (Test-ProtectedPath ([string]$entry.path))) { throw "$Context contains unprotected path '$([string]$entry.path)'" }
        if ([string]$entry.mode -cnotin @('100644','100755')) { throw "$Context contains invalid mode '$([string]$entry.mode)'" }
        if ([string]$entry.git_blob -cnotmatch '^[0-9a-f]{40}$') { throw "$Context contains invalid git blob '$([string]$entry.git_blob)'" }
        if (-not $seen.Add([string]$entry.path)) { throw "$Context contains duplicate path '$([string]$entry.path)'" }
    }
}

function Get-ValidatorBlobs {
    param([Parameter(Mandatory)][string]$WorkingTree, [Parameter(Mandatory)][string]$Commit, [Parameter(Mandatory)][string[]]$Paths)
    $result = foreach ($path in $Paths) {
        $blob = [string](Invoke-Git $WorkingTree @('rev-parse',"$Commit`:$path")).output[-1]
        if ($blob -cnotmatch '^[0-9a-f]{40}$') { throw "validator '$path' is not a regular blob at '$Commit'" }
        [pscustomobject][ordered]@{ path=$path; git_blob=$blob }
    }
    return @($result)
}

function Get-ProtectedBlobsAtCommit {
    param(
        [Parameter(Mandatory)][string]$WorkingTree,
        [Parameter(Mandatory)][string]$Commit,
        [Parameter(Mandatory)][object[]]$Changes,
        [Parameter(Mandatory)][string]$ExcludedPath
    )
    $result = foreach ($change in @($Changes | Where-Object { [string]$_.path -cne $ExcludedPath })) {
        $path = [string]$change.path
        $tree = Invoke-Git $WorkingTree @('-c','core.quotepath=false','ls-tree','--full-tree',$Commit,'--',":(literal)$path")
        if ($tree.output.Count -ne 1 -or [string]$tree.output[0] -cnotmatch '^(?<mode>100644|100755) blob (?<object>[0-9a-f]{40})\t(?<path>.+)$') { throw "historical protected path '$path' is not one regular blob at '$Commit'" }
        if ([string]$Matches.path -cne $path) { throw "historical protected path '$path' resolved as '$([string]$Matches.path)'" }
        [pscustomobject][ordered]@{ status=[string]$change.status; path=$path; mode=[string]$Matches.mode; git_blob=[string]$Matches.object }
    }
    return @($result)
}

function Assert-HistoricalTransitionGitAnchor {
    param(
        [Parameter(Mandatory)][string]$WorkingTree,
        [Parameter(Mandatory)][string]$CurrentBaseSha,
        [Parameter(Mandatory)]$BaseFacts,
        [Parameter(Mandatory)][string[]]$ValidatorPaths
    )
    if ($null -eq $BaseFacts.historical_anchor) { return }
    $manifest = $BaseFacts.historical_anchor.manifest
    $ancestor = Invoke-Git $WorkingTree @('merge-base','--is-ancestor',[string]$manifest.event_base_sha,$CurrentBaseSha) -AllowFailure
    if ($ancestor.exit_code -ne 0) { throw 'historical manifest event_base_sha is not an ancestor of the current trusted base' }
    [object[]]$expectedCurrentValidators = @(Get-ValidatorBlobs $WorkingTree ([string]$manifest.event_base_sha) $ValidatorPaths)
    [object[]]$expectedSuccessorValidators = @(Get-ValidatorBlobs $WorkingTree $CurrentBaseSha $ValidatorPaths)
    if ((Get-BlobSignature @($manifest.current_validator_blobs)) -cne (Get-BlobSignature $expectedCurrentValidators)) { throw 'historical manifest current-validator blobs differ from its immutable event base' }
    if ((Get-BlobSignature @($manifest.successor_validator_blobs)) -cne (Get-BlobSignature $expectedSuccessorValidators)) { throw 'historical manifest successor-validator blobs differ from the current trusted base' }
    [object[]]$expectedProtected = @(Get-ProtectedBlobsAtCommit $WorkingTree $CurrentBaseSha @($manifest.exact_changes) ([string]$BaseFacts.maintenance.manifest_container_path))
    if ((Get-BlobSignature @($manifest.protected_head_blobs_except_manifest_container) -Protected) -cne (Get-BlobSignature $expectedProtected -Protected)) { throw 'historical manifest protected-head blobs differ from the current trusted-base Git tree' }
}

function Get-RootYamlBlock {
    param([Parameter(Mandatory)][string]$Text, [Parameter(Mandatory)][string]$Name, [Parameter(Mandatory)][string]$Context)
    [string[]]$lines = [regex]::Split($Text, '\r?\n')
    $headerPattern = '^' + [regex]::Escape($Name) + '\s*:\s*(?:#.*)?$'
    [int[]]$headers = @(for ($i=0; $i -lt $lines.Count; $i++) { if ($lines[$i] -match $headerPattern) { $i } })
    if ($headers.Count -ne 1) { throw "$Context must contain exactly one block-form root '${Name}:' key" }
    $start = $headers[0]
    $body = [System.Collections.Generic.List[string]]::new()
    for ($i=$start+1; $i -lt $lines.Count; $i++) {
        if (-not [string]::IsNullOrWhiteSpace($lines[$i]) -and $lines[$i] -notmatch '^\s' -and $lines[$i] -notmatch '^\s*#') { break }
        $body.Add($lines[$i])
    }
    return ($body -join "`n")
}

function Assert-ReadOnlyPermissionBlocks {
    param([Parameter(Mandatory)][string]$Text, [Parameter(Mandatory)][string]$Context, [Parameter(Mandatory)][int]$MinimumCount)
    [string[]]$lines = [regex]::Split($Text, '\r?\n')
    $count = 0
    for ($i=0; $i -lt $lines.Count; $i++) {
        if ($lines[$i] -notmatch '^(?<indent>\s*)permissions\s*:(?<tail>.*)$') { continue }
        $count++
        $indent = [string]$Matches.indent
        $tail = [string]$Matches.tail
        if ($tail -notmatch '^\s*(?:#.*)?$') { throw "$Context permissions must use block form with only contents: read" }
        $contentsIndex = -1
        for ($j=$i+1; $j -lt $lines.Count; $j++) {
            $trimmed = $lines[$j].Trim()
            if ([string]::IsNullOrWhiteSpace($trimmed) -or $trimmed.StartsWith('#', [System.StringComparison]::Ordinal)) { continue }
            if (-not $lines[$j].StartsWith($indent + '  ', [System.StringComparison]::Ordinal)) { break }
            if ($lines[$j] -notmatch ('^' + [regex]::Escape($indent + '  ') + 'contents\s*:\s*read\s*(?:#.*)?$')) { throw "$Context permissions block must contain exactly contents: read" }
            $contentsIndex = $j
            break
        }
        if ($contentsIndex -lt 0) { throw "$Context permissions block is empty" }
        for ($j=$contentsIndex+1; $j -lt $lines.Count; $j++) {
            $trimmed = $lines[$j].Trim()
            if ([string]::IsNullOrWhiteSpace($trimmed) -or $trimmed.StartsWith('#', [System.StringComparison]::Ordinal)) { continue }
            if ($lines[$j].StartsWith($indent + '  ', [System.StringComparison]::Ordinal)) { throw "$Context permissions block contains an additional capability" }
            break
        }
    }
    if ($count -lt $MinimumCount) { throw "$Context contains $count permissions blocks, expected at least $MinimumCount" }
}

function Assert-ImmutableWorkflowActions {
    param([Parameter(Mandatory)][string]$Text, [Parameter(Mandatory)][string]$Context, [Parameter(Mandatory)][string[]]$AllowedActions)
    if ([regex]::IsMatch($Text, '(?i)\{[^}\r\n]*["'']?uses["'']?\s*:')) { throw "$Context may not use flow-style action declarations" }
    foreach ($line in [regex]::Split($Text, '\r?\n')) {
        if ($line -notmatch '^\s*(?:-\s*)?["'']?uses["'']?\s*:\s*(?<action>[^#]+?)(?:\s+#.*)?$') { continue }
        $action = ([string]$Matches.action).Trim().Trim('"', "'")
        if ($action -cnotmatch '^[^\s@]+@[0-9a-f]{40}$') { throw "$Context action is not pinned to a full immutable SHA: $action" }
        if ($action -cnotin $AllowedActions) { throw "$Context action is pinned but outside the audited allowlist: $action" }
    }
}

function Get-PrivilegedWorkflowSkeleton {
    param([Parameter(Mandatory)][string]$Text)
    $result = [System.Collections.Generic.List[string]]::new()
    foreach ($rawLine in [regex]::Split($Text, '\r?\n')) {
        $line = $rawLine.TrimEnd()
        if ([string]::IsNullOrWhiteSpace($line) -or $line -match '^\s*#') { continue }
        if ($line -match '^(?<prefix>\s*(?:-\s*)?["'']?uses["'']?\s*:\s*)(?<action>[^#]+?)(?<comment>\s+#.*)?$') {
            $line = ([string]$Matches.prefix) + '<AUDITED-IMMUTABLE-ACTION>'
        }
        $result.Add($line)
    }
    return ($result -join "`n")
}

function Assert-HeadWorkflowSafety {
    param(
        [Parameter(Mandatory)][string]$BaseAuthorityWorkflow,
        [Parameter(Mandatory)][string]$AuthorityWorkflow,
        [Parameter(Mandatory)][string]$TestWorkflow
    )
    $baseAuthority = [System.IO.File]::ReadAllText([System.IO.Path]::GetFullPath($BaseAuthorityWorkflow))
    $authority = [System.IO.File]::ReadAllText([System.IO.Path]::GetFullPath($AuthorityWorkflow))
    $test = [System.IO.File]::ReadAllText([System.IO.Path]::GetFullPath($TestWorkflow))
    foreach ($required in @(
        'pull_request_target:',
        'permissions:',
        'contents: read',
        'Export-TrustedValidator',
        'cat-file',
        'refs/pull/$env:EVENT_PR_NUMBER/head',
        'ExpectedValidatorGitBlob',
        'actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02'
    )) {
        if (-not $authority.Contains($required)) { throw "candidate authority workflow is missing trusted-base invariant '$required'" }
    }
    foreach ($forbidden in @('actions/checkout@', 'secrets.')) {
        if ($authority.Contains($forbidden)) { throw "candidate authority workflow contains forbidden privileged surface '$forbidden'" }
    }
    $authorityOn = Get-RootYamlBlock $authority 'on' 'candidate authority workflow'
    if ([regex]::Matches($authorityOn, '(?m)^\s{2}pull_request_target:\s*$').Count -ne 1) { throw 'candidate authority workflow must contain exactly one pull_request_target trigger' }
    Assert-ReadOnlyPermissionBlocks $authority 'candidate authority workflow' 1
    $successorMatch = [regex]::Match($test, '(?ms)^  authority-successor-selftest:[ \t]*(?:#[^\r\n]*)?\r?\n(?<body>.*?)(?=^  [A-Za-z0-9_-]+:[ \t]*(?:#[^\r\n]*)?\r?\n|\z)')
    if (-not $successorMatch.Success) { throw 'candidate test workflow lacks the unprivileged authority-successor-selftest job' }
    $successor = $successorMatch.Groups['body'].Value
    foreach ($required in @('if: github.event_name == ''pull_request''', 'contents: read', 'persist-credentials: false', 'test-r12-authority-maintenance.ps1', 'r12-authority-maintenance.json')) {
        if (-not $successor.Contains($required)) { throw "candidate successor self-test is missing '$required'" }
    }
    foreach ($forbidden in @('secrets.', 'packages: write', 'contents: write', 'id-token: write')) {
        if ($successor.Contains($forbidden)) { throw "candidate successor self-test contains forbidden surface '$forbidden'" }
    }
    if ([regex]::IsMatch($test, '\$\{\{\s*secrets\.')) { throw 'candidate test workflow may not reference repository secrets' }
    $testOn = Get-RootYamlBlock $test 'on' 'candidate test workflow'
    if ($testOn -match '(?m)^\s{2}pull_request_target\s*:') { throw 'candidate test workflow may not use pull_request_target' }
    if ($testOn -notmatch '(?m)^\s{2}pull_request\s*:') { throw 'candidate test workflow must retain the unprivileged pull_request trigger' }
    if (-not [regex]::IsMatch($test, '(?ms)^permissions:\r?\n\s{2}contents:\s*read\s*$')) { throw 'candidate test workflow must keep workflow-level contents: read permissions' }
    Assert-ReadOnlyPermissionBlocks $test 'candidate test workflow' 2
    $checkout = 'actions/checkout@34e114876b0b11c390a56381ad16ebd13914f8d5'
    $setupGo = 'actions/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff'
    $uploadArtifact = 'actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02'
    Assert-ImmutableWorkflowActions $authority 'candidate authority workflow' @($uploadArtifact)
    Assert-ImmutableWorkflowActions $test 'candidate test workflow' @($checkout, $setupGo, $uploadArtifact)
    if ((Get-PrivilegedWorkflowSkeleton $authority) -cne (Get-PrivilegedWorkflowSkeleton $baseAuthority)) { throw 'candidate privileged workflow skeleton or run commands differ from the trusted base; only comments and pre-authorized action-pin rotation may change' }
}

function Assert-RequiredControlPlaneChangeSet {
    param([Parameter(Mandatory)][object[]]$Changes, [Parameter(Mandatory)][string]$Context)
    if ($Changes.Count -lt 6 -or $Changes.Count -gt 32) { throw "$Context must contain 6..32 bounded protected paths" }
    [string[]]$paths = @($Changes | ForEach-Object { [string]$_.path })
    foreach ($required in @(
        '.agent/plans/2026-07-10-engram-production-ready-active-diff-contracts.json',
        '.github/workflows/authority-guard.yml',
        '.github/workflows/test.yml',
        'scripts/production-gates/assert-active-candidate-path-authority.ps1',
        'scripts/production-gates/assert-pr-authority-guard.ps1',
        'scripts/production-gates/assert-pr-authority-maintenance.ps1'
    )) {
        if ($required -cnotin $paths) { throw "$Context omits required control-plane path '$required'" }
    }
}

function Assert-HistoricalTransitionAnchor {
    param(
        [Parameter(Mandatory)]$Maintenance,
        [Parameter(Mandatory)][AllowEmptyCollection()][object[]]$Consumed,
        [Parameter(Mandatory)]$ActiveEpoch
    )
    $manifest = $Maintenance.transition_manifest
    if ($Consumed.Count -eq 0) {
        if ($null -ne $manifest) { throw 'zero-history base transition_manifest must be null' }
        return $null
    }
    if ($null -eq $manifest) { throw 'historical base must retain the last accepted transition_manifest' }
    Assert-ObjectShape $manifest @('schema_version','reason','created_at','expires_at','event_base_sha','current_validator_blobs','successor_validator_blobs','protected_head_blobs_except_manifest_container','exact_changes','approval_epoch','successor_epoch') 'historical transition_manifest'
    Assert-JsonInteger $manifest.schema_version 2 'historical transition_manifest.schema_version'
    Assert-JsonString $manifest.reason 'historical transition_manifest.reason'
    if ([string]$manifest.event_base_sha -cnotmatch '^[0-9a-f]{40}$' -or [string]$manifest.approval_epoch -notmatch '^r12-[0-9]{4}$' -or [string]$manifest.successor_epoch -notmatch '^r12-[0-9]{4}$') { throw 'historical transition manifest identity is malformed' }
    if ([string]::IsNullOrWhiteSpace([string]$manifest.reason) -or ([string]$manifest.reason).Length -gt 512 -or [string]$manifest.reason -match '[\x00-\x08\x0B\x0C\x0E-\x1F]') { throw 'historical transition manifest reason is invalid' }
    $created = ConvertTo-Rfc3339 $manifest.created_at 'historical transition_manifest.created_at'
    $expires = ConvertTo-Rfc3339 $manifest.expires_at 'historical transition_manifest.expires_at'
    if ($expires -le $created -or ($expires-$created).TotalSeconds -gt [int64]$ActiveEpoch.max_manifest_ttl_seconds) { throw 'historical transition manifest TTL is invalid or unbounded' }
    Assert-JsonArray $manifest.exact_changes 'historical transition_manifest.exact_changes'
    Assert-JsonArray $manifest.current_validator_blobs 'historical transition_manifest.current_validator_blobs'
    Assert-JsonArray $manifest.successor_validator_blobs 'historical transition_manifest.successor_validator_blobs'
    Assert-JsonArray $manifest.protected_head_blobs_except_manifest_container 'historical transition_manifest.protected_head_blobs_except_manifest_container'
    [object[]]$manifestChanges = @($manifest.exact_changes)
    Assert-ChangeArray $manifestChanges 'historical transition_manifest.exact_changes' -AllowAdd
    Assert-RequiredControlPlaneChangeSet $manifestChanges 'historical transition_manifest.exact_changes'
    [object[]]$currentBlobs = @($manifest.current_validator_blobs)
    [object[]]$successorBlobs = @($manifest.successor_validator_blobs)
    [object[]]$protectedBlobs = @($manifest.protected_head_blobs_except_manifest_container)
    Assert-ValidatorBlobArray $currentBlobs 'historical transition_manifest.current_validator_blobs'
    Assert-ValidatorBlobArray $successorBlobs 'historical transition_manifest.successor_validator_blobs'
    Assert-ProtectedBlobArray $protectedBlobs 'historical transition_manifest.protected_head_blobs_except_manifest_container'
    [string[]]$requiredValidatorPaths = @($Maintenance.current_validator_path, 'scripts/production-gates/assert-active-candidate-path-authority.ps1', $Maintenance.maintenance_validator_path, 'scripts/production-gates/assert-plan-path-ownership.ps1')
    $requiredValidatorPaths = @($requiredValidatorPaths | Sort-Object -CaseSensitive)
    foreach ($set in @($currentBlobs, $successorBlobs)) {
        [string[]]$observedValidatorPaths = @($set | ForEach-Object { [string]$_.path } | Sort-Object -CaseSensitive)
        if (($observedValidatorPaths -join "`n") -cne ($requiredValidatorPaths -join "`n")) { throw 'historical transition manifest validator path set drifted' }
    }
    [object[]]$expectedProtected = @($manifestChanges | Where-Object { [string]$_.path -cne [string]$Maintenance.manifest_container_path } | ForEach-Object {
        [pscustomobject][ordered]@{ status=[string]$_.status; path=[string]$_.path; mode='100644'; git_blob='0000000000000000000000000000000000000000' }
    })
    if ((Get-ChangeSignature $protectedBlobs) -cne (Get-ChangeSignature $expectedProtected)) { throw 'historical manifest protected path/status inventory differs from its exact changes' }
    $last = $Consumed[-1]
    $manifestSha = Get-SemanticDigest $manifest
    if ([string]$last.epoch -cne [string]$manifest.approval_epoch -or [string]$last.event_base_sha -cne [string]$manifest.event_base_sha -or [string]$last.manifest_sha256 -cne $manifestSha -or [string]$last.successor_epoch -cne [string]$manifest.successor_epoch -or [string]$ActiveEpoch.epoch -cne [string]$manifest.successor_epoch) { throw 'historical manifest, last consumed record, and active successor epoch are not exact-bound' }
    $consumedAt = ConvertTo-Rfc3339 $last.consumed_at 'historical last consumed_at'
    if ($consumedAt -ne $created) { throw 'historical last consumed_at must equal manifest created_at' }
    return [pscustomobject][ordered]@{ manifest=$manifest; canonical_manifest_sha256=$manifestSha; created_at=$created; expires_at=$expires }
}

function Assert-BaseMaintenanceAuthority {
    param([Parameter(Mandatory)]$Contract, [Parameter(Mandatory)][DateTimeOffset]$Now)
    $maintenance = $Contract.control_plane_maintenance
    Assert-ObjectShape $maintenance @('schema_version','manifest_container_path','manifest_property','current_validator_path','maintenance_validator_path','active_epoch','consumed_epochs','transition_manifest','manifest_binding_policy','trusted_execution_artifact_policy','lifecycle_policy','replay_policy','head_execution_policy') 'base control_plane_maintenance'
    Assert-JsonInteger $maintenance.schema_version 2 'base maintenance.schema_version'
    if ([string]$maintenance.manifest_container_path -cne '.agent/plans/2026-07-10-engram-production-ready-active-diff-contracts.json') { throw 'base maintenance manifest container path drifted' }
    if ([string]$maintenance.manifest_property -cne 'control_plane_maintenance.transition_manifest') { throw 'base maintenance manifest property drifted' }
    if ([string]$maintenance.current_validator_path -cne 'scripts/production-gates/assert-pr-authority-guard.ps1' -or [string]$maintenance.maintenance_validator_path -cne 'scripts/production-gates/assert-pr-authority-maintenance.ps1') { throw 'base maintenance validator paths drifted' }
    $epoch = $maintenance.active_epoch
    Assert-ObjectShape $epoch @('epoch','state','reason','not_before','expires_at','max_manifest_ttl_seconds','approval','exact_changes') 'base active_epoch'
    Assert-JsonString $epoch.reason 'base active_epoch.reason'
    Assert-JsonInteger $epoch.max_manifest_ttl_seconds 86400 'base active_epoch.max_manifest_ttl_seconds'
    Assert-JsonArray $epoch.exact_changes 'base active_epoch.exact_changes'
    if ([string]$epoch.epoch -notmatch '^r12-[0-9]{4}$' -or [string]$epoch.state -cne 'open') { throw 'base active epoch is not one open R12 epoch' }
    $notBefore = ConvertTo-Rfc3339 $epoch.not_before 'base active_epoch.not_before'
    $expires = ConvertTo-Rfc3339 $epoch.expires_at 'base active_epoch.expires_at'
    if ($Now -lt $notBefore) { throw 'base maintenance epoch is not active yet' }
    if ($Now -ge $expires) { throw 'base maintenance epoch is expired' }
    $approval = $epoch.approval
    Assert-ObjectShape $approval @('actor_login','actor_id','actor_type','association','label','approval_epoch') 'base active_epoch.approval'
    Assert-JsonInteger $approval.actor_id 7106373 'base active_epoch.approval.actor_id'
    if ([string]$approval.actor_login -cne 'thebtf' -or [int64]$approval.actor_id -ne 7106373 -or [string]$approval.actor_type -cne 'User' -or [string]$approval.association -cne 'OWNER') { throw 'base maintenance approval actor drifted' }
    if ([string]$approval.approval_epoch -cne [string]$epoch.epoch -or [string]$approval.label -cne "authority-maintenance:$([string]$epoch.epoch)") { throw 'base maintenance approval epoch/label drifted' }
    [object[]]$changes = @($epoch.exact_changes); Assert-ChangeArray $changes 'base active_epoch.exact_changes' -AllowAdd
    Assert-RequiredControlPlaneChangeSet $changes 'base active_epoch.exact_changes'
    Assert-JsonArray $maintenance.consumed_epochs 'base control_plane_maintenance.consumed_epochs'
    [object[]]$consumed = @($maintenance.consumed_epochs)
    $seenEpochs = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    $previousEpochNumber = $null
    foreach ($record in $consumed) {
        Assert-ObjectShape $record @('epoch','state','consumed_at','event_base_sha','manifest_sha256','successor_epoch') 'base consumed epoch'
        if ([string]$record.epoch -notmatch '^r12-[0-9]{4}$' -or [string]$record.state -cne 'consumed' -or [string]$record.event_base_sha -cnotmatch '^[0-9a-f]{40}$' -or [string]$record.manifest_sha256 -cnotmatch '^[0-9a-f]{64}$' -or [string]$record.successor_epoch -notmatch '^r12-[0-9]{4}$') { throw 'base consumed epoch record is malformed' }
        [void](ConvertTo-Rfc3339 $record.consumed_at 'base consumed epoch consumed_at')
        if (-not $seenEpochs.Add([string]$record.epoch)) { throw "base consumed epoch '$([string]$record.epoch)' is duplicated" }
        $epochNumber = [int]([regex]::Match([string]$record.epoch, '^r12-([0-9]{4})$').Groups[1].Value)
        $successorNumber = [int]([regex]::Match([string]$record.successor_epoch, '^r12-([0-9]{4})$').Groups[1].Value)
        if (($null -eq $previousEpochNumber -and $epochNumber -ne 1) -or $successorNumber -ne $epochNumber + 1 -or ($null -ne $previousEpochNumber -and $epochNumber -ne $previousEpochNumber + 1)) { throw 'base consumed epoch history is not one complete contiguous monotonic chain from r12-0001' }
        $previousEpochNumber = $epochNumber
    }
    if ($seenEpochs.Contains([string]$epoch.epoch)) { throw 'base active epoch is already recorded as consumed' }
    $historicalAnchor = Assert-HistoricalTransitionAnchor -Maintenance $maintenance -Consumed $consumed -ActiveEpoch $epoch
    $binding = $maintenance.manifest_binding_policy
    Assert-ObjectShape $binding @('committed_manifest_required_fields','committed_manifest_forbidden_self_references','manifest_container_excluded_from_protected_head_blobs','all_other_protected_head_blobs_required','canonical_manifest_sha256_required','external_mutable_payload_is_sole_authority') 'base manifest_binding_policy'
    Assert-JsonArray $binding.committed_manifest_required_fields 'base manifest_binding_policy.committed_manifest_required_fields'
    Assert-JsonArray $binding.committed_manifest_forbidden_self_references 'base manifest_binding_policy.committed_manifest_forbidden_self_references'
    $requiredManifestFields = @('event_base_sha','approval_epoch','successor_epoch','created_at','expires_at','exact_changes','current_validator_blobs','successor_validator_blobs','protected_head_blobs_except_manifest_container')
    if ((Get-SemanticDigest @($binding.committed_manifest_required_fields)) -cne (Get-SemanticDigest $requiredManifestFields)) { throw 'base committed manifest required fields drifted' }
    if ((Get-SemanticDigest @($binding.committed_manifest_forbidden_self_references)) -cne (Get-SemanticDigest @('event_head_sha','manifest_container_git_blob'))) { throw 'base committed manifest self-reference ban drifted' }
    Assert-JsonBoolean $binding.manifest_container_excluded_from_protected_head_blobs $true 'base manifest_binding_policy.manifest_container_excluded_from_protected_head_blobs'
    Assert-JsonBoolean $binding.all_other_protected_head_blobs_required $true 'base manifest_binding_policy.all_other_protected_head_blobs_required'
    Assert-JsonBoolean $binding.canonical_manifest_sha256_required $true 'base manifest_binding_policy.canonical_manifest_sha256_required'
    Assert-JsonBoolean $binding.external_mutable_payload_is_sole_authority $false 'base manifest_binding_policy.external_mutable_payload_is_sole_authority'
    $artifactPolicy = $maintenance.trusted_execution_artifact_policy
    Assert-ObjectShape $artifactPolicy @('producer','required_fields','binds_actual_event_head_after_checkout','binds_actual_manifest_container_blob_after_checkout','candidate_artifact_is_data_only') 'base trusted_execution_artifact_policy'
    Assert-JsonArray $artifactPolicy.required_fields 'base trusted_execution_artifact_policy.required_fields'
    if ([string]$artifactPolicy.producer -cne 'trusted base pull_request_target validator') { throw 'base trusted artifact producer drifted' }
    $requiredArtifactFields = @('event_base_sha','event_head_sha','manifest_container_git_blob','canonical_manifest_sha256','trusted_validator_git_blob','required_status_context','required_status_integration_id','required_status_app_slug')
    if ((Get-SemanticDigest @($artifactPolicy.required_fields)) -cne (Get-SemanticDigest $requiredArtifactFields)) { throw 'base trusted artifact required fields drifted' }
    Assert-JsonBoolean $artifactPolicy.binds_actual_event_head_after_checkout $true 'base trusted_execution_artifact_policy.binds_actual_event_head_after_checkout'
    Assert-JsonBoolean $artifactPolicy.binds_actual_manifest_container_blob_after_checkout $true 'base trusted_execution_artifact_policy.binds_actual_manifest_container_blob_after_checkout'
    Assert-JsonBoolean $artifactPolicy.candidate_artifact_is_data_only $true 'base trusted_execution_artifact_policy.candidate_artifact_is_data_only'
    $lifecycle = $maintenance.lifecycle_policy
    Assert-ObjectShape $lifecycle @('states','initial_state','active_to_consumed_requires_accepted_exact_transition','active_to_expired_occurs_at_epoch_expiry','consumed_or_expired_epoch_cannot_reopen','successor_epoch_must_be_monotonic_and_new','max_epoch_lifetime_seconds','rotate_before_expiry_seconds','automatic_extension','expired_epoch_reseed_requires_audited_pr_only_recovery_actor') 'base lifecycle_policy'
    Assert-JsonArray $lifecycle.states 'base lifecycle_policy.states'
    if ((Get-SemanticDigest $lifecycle.states) -cne (Get-SemanticDigest @('active','consumed','expired')) -or [string]$lifecycle.initial_state -cne 'active') { throw 'base maintenance lifecycle states drifted' }
    foreach ($name in @('active_to_consumed_requires_accepted_exact_transition','active_to_expired_occurs_at_epoch_expiry','consumed_or_expired_epoch_cannot_reopen','successor_epoch_must_be_monotonic_and_new','expired_epoch_reseed_requires_audited_pr_only_recovery_actor')) { Assert-JsonBoolean $lifecycle.$name $true "base lifecycle_policy.$name" }
    Assert-JsonInteger $lifecycle.max_epoch_lifetime_seconds 7776000 'base lifecycle_policy.max_epoch_lifetime_seconds'
    Assert-JsonInteger $lifecycle.rotate_before_expiry_seconds 604800 'base lifecycle_policy.rotate_before_expiry_seconds'
    Assert-JsonBoolean $lifecycle.automatic_extension $false 'base lifecycle_policy.automatic_extension'
    $replay = $maintenance.replay_policy
    Assert-ObjectShape $replay @('strict_latest_base','require_event_base_equals_current_default_branch','require_consumed_epoch_transition','require_bounded_successor_epoch','two_pr_race_loser_must_be_stale','new_base_rejects_consumed_epoch') 'base replay_policy'
    foreach ($name in @('strict_latest_base','require_event_base_equals_current_default_branch','require_consumed_epoch_transition','require_bounded_successor_epoch','two_pr_race_loser_must_be_stale','new_base_rejects_consumed_epoch')) { Assert-JsonBoolean $replay.$name $true "base replay_policy.$name" }
    $headExecution = $maintenance.head_execution_policy
    Assert-ObjectShape $headExecution @('pull_request_target_executes_trusted_base_only','head_workflow_and_script_bytes_are_data_only','pull_request_successor_selftest_is_unprivileged','pull_request_successor_selftest_uses_secrets','trusted_execution_artifact_required','candidate_supplied_execution_artifact_forbidden') 'base head_execution_policy'
    foreach ($name in @('pull_request_target_executes_trusted_base_only','head_workflow_and_script_bytes_are_data_only','pull_request_successor_selftest_is_unprivileged','trusted_execution_artifact_required','candidate_supplied_execution_artifact_forbidden')) { Assert-JsonBoolean $headExecution.$name $true "base head_execution_policy.$name" }
    Assert-JsonBoolean $headExecution.pull_request_successor_selftest_uses_secrets $false 'base head_execution_policy.pull_request_successor_selftest_uses_secrets'
    return [pscustomobject][ordered]@{ contract=$Contract; maintenance=$maintenance; epoch=$epoch; consumed=$consumed; changes=$changes; historical_anchor=$historicalAnchor; not_before=$notBefore; expires_at=$expires }
}

function Assert-HeadMaintenanceTransition {
    param(
        [Parameter(Mandatory)]$BaseFacts,
        [Parameter(Mandatory)]$HeadContract,
        [Parameter(Mandatory)][object[]]$DiffEntries,
        [Parameter(Mandatory)][object[]]$CurrentValidatorBlobs,
        [Parameter(Mandatory)][object[]]$SuccessorValidatorBlobs,
        [Parameter(Mandatory)][string]$EventBaseSha,
        [Parameter(Mandatory)][DateTimeOffset]$Now
    )
    $head = $HeadContract.control_plane_maintenance
    Assert-ObjectShape $head @('schema_version','manifest_container_path','manifest_property','current_validator_path','maintenance_validator_path','active_epoch','consumed_epochs','transition_manifest','manifest_binding_policy','trusted_execution_artifact_policy','lifecycle_policy','replay_policy','head_execution_policy') 'head control_plane_maintenance'
    Assert-JsonInteger $head.schema_version 2 'head control_plane_maintenance.schema_version'
    if ([string]$head.manifest_container_path -cne [string]$BaseFacts.maintenance.manifest_container_path -or [string]$head.manifest_property -cne [string]$BaseFacts.maintenance.manifest_property -or [string]$head.current_validator_path -cne [string]$BaseFacts.maintenance.current_validator_path -or [string]$head.maintenance_validator_path -cne [string]$BaseFacts.maintenance.maintenance_validator_path) { throw 'head maintenance identity or validator paths drifted' }
    Assert-ObjectShape $HeadContract @($BaseFacts.contract.PSObject.Properties.Name) 'head active contract'
    foreach ($surface in @($BaseFacts.contract.PSObject.Properties.Name | Where-Object { $_ -cne 'control_plane_maintenance' })) {
        if ((Get-SemanticDigest $HeadContract.$surface) -cne (Get-SemanticDigest $BaseFacts.contract.$surface)) { throw "head contract $surface must remain semantically identical to the trusted base" }
    }
    foreach ($policy in @('manifest_binding_policy','trusted_execution_artifact_policy','lifecycle_policy','replay_policy','head_execution_policy')) {
        if ((Get-SemanticDigest $head.$policy) -cne (Get-SemanticDigest $BaseFacts.maintenance.$policy)) { throw "head maintenance $policy must remain byte-independent semantically identical to the trusted base" }
    }

    $manifest = $head.transition_manifest
    Assert-ObjectShape $manifest @('schema_version','reason','created_at','expires_at','event_base_sha','current_validator_blobs','successor_validator_blobs','protected_head_blobs_except_manifest_container','exact_changes','approval_epoch','successor_epoch') 'head transition_manifest'
    Assert-JsonInteger $manifest.schema_version 2 'head transition_manifest.schema_version'
    Assert-JsonString $manifest.reason 'head transition_manifest.reason'
    if ([string]$manifest.approval_epoch -cne [string]$BaseFacts.epoch.epoch) { throw 'head transition manifest does not consume the exact trusted-base epoch' }
    if ([string]$manifest.event_base_sha -cne $EventBaseSha) { throw 'head transition manifest does not bind the exact event base SHA' }
    if ([string]::IsNullOrWhiteSpace([string]$manifest.reason) -or ([string]$manifest.reason).Length -gt 512 -or [string]$manifest.reason -match '[\x00-\x08\x0B\x0C\x0E-\x1F]') { throw 'head transition manifest reason is blank, oversized, or contains control characters' }
    $issued = ConvertTo-Rfc3339 $manifest.created_at 'head transition_manifest.created_at'
    $manifestExpires = ConvertTo-Rfc3339 $manifest.expires_at 'head transition_manifest.expires_at'
    if ($issued -lt $BaseFacts.not_before) { throw 'head transition manifest predates the active maintenance epoch' }
    if ($issued -gt $Now.AddMinutes(5)) { throw 'head transition manifest created_at is too far in the future' }
    if ($manifestExpires -le $issued -or ($manifestExpires-$issued).TotalSeconds -gt [int64]$BaseFacts.epoch.max_manifest_ttl_seconds) { throw 'head transition manifest TTL is invalid or unbounded' }
    if ($Now -ge $manifestExpires) { throw 'head transition manifest is expired' }

    Assert-JsonArray $manifest.exact_changes 'head transition_manifest.exact_changes'
    Assert-JsonArray $manifest.current_validator_blobs 'head transition_manifest.current_validator_blobs'
    Assert-JsonArray $manifest.successor_validator_blobs 'head transition_manifest.successor_validator_blobs'
    Assert-JsonArray $manifest.protected_head_blobs_except_manifest_container 'head transition_manifest.protected_head_blobs_except_manifest_container'
    [object[]]$manifestChanges = @($manifest.exact_changes); Assert-ChangeArray $manifestChanges 'head transition_manifest.exact_changes' -AllowAdd
    if ((Get-ChangeSignature $manifestChanges) -cne (Get-ChangeSignature $BaseFacts.changes) -or (Get-ChangeSignature $manifestChanges) -cne (Get-ChangeSignature $DiffEntries)) { throw 'head transition manifest, trusted-base epoch, and actual diff status/path sets are not exact-equal' }

    [object[]]$currentBlobs = @($manifest.current_validator_blobs); Assert-ValidatorBlobArray $currentBlobs 'head transition_manifest.current_validator_blobs'
    [object[]]$successorBlobs = @($manifest.successor_validator_blobs); Assert-ValidatorBlobArray $successorBlobs 'head transition_manifest.successor_validator_blobs'
    [object[]]$protectedBlobs = @($manifest.protected_head_blobs_except_manifest_container); Assert-ProtectedBlobArray $protectedBlobs 'head transition_manifest.protected_head_blobs_except_manifest_container'
    if ((Get-BlobSignature $currentBlobs) -cne (Get-BlobSignature $CurrentValidatorBlobs)) { throw 'head transition manifest current-validator blobs differ from trusted-base Git objects' }
    if ((Get-BlobSignature $successorBlobs) -cne (Get-BlobSignature $SuccessorValidatorBlobs)) { throw 'head transition manifest successor-validator blobs differ from exact head Git objects' }
    [object[]]$expectedProtectedBlobs = @($DiffEntries | Where-Object { [string]$_.path -cne [string]$BaseFacts.maintenance.manifest_container_path })
    if ((Get-BlobSignature $protectedBlobs -Protected) -cne (Get-BlobSignature $expectedProtectedBlobs -Protected)) { throw 'head transition manifest protected-head blob inventory differs from exact non-container head Git objects' }
    $manifestSha256 = Get-SemanticDigest $manifest

    Assert-JsonArray $head.consumed_epochs 'head control_plane_maintenance.consumed_epochs'
    [object[]]$headConsumed = @($head.consumed_epochs)
    if ($headConsumed.Count -ne $BaseFacts.consumed.Count + 1) { throw 'head consumed_epochs must append exactly one record' }
    for ($i=0; $i -lt $BaseFacts.consumed.Count; $i++) {
        if ((Get-SemanticDigest $headConsumed[$i]) -cne (Get-SemanticDigest $BaseFacts.consumed[$i])) { throw "head consumed_epochs rewrote trusted record index $i" }
    }
    $consumed = $headConsumed[-1]
    Assert-ObjectShape $consumed @('epoch','state','consumed_at','event_base_sha','manifest_sha256','successor_epoch') 'head appended consumed epoch'
    if ([string]$consumed.epoch -cne [string]$BaseFacts.epoch.epoch -or [string]$consumed.state -cne 'consumed' -or [string]$consumed.event_base_sha -cne $EventBaseSha -or [string]$consumed.manifest_sha256 -cne $manifestSha256 -or [string]$consumed.successor_epoch -cne [string]$manifest.successor_epoch) { throw 'head appended consumed epoch does not bind the accepted canonical manifest and successor' }
    $consumedAt = ConvertTo-Rfc3339 $consumed.consumed_at 'head appended consumed_at'
    if ($consumedAt -ne $issued) { throw 'head consumed_at must equal the manifest created_at' }

    $successor = $head.active_epoch
    Assert-ObjectShape $successor @('epoch','state','reason','not_before','expires_at','max_manifest_ttl_seconds','approval','exact_changes') 'head successor active_epoch'
    Assert-JsonString $successor.reason 'head successor active_epoch.reason'
    Assert-JsonInteger $successor.max_manifest_ttl_seconds 86400 'head successor active_epoch.max_manifest_ttl_seconds'
    Assert-JsonArray $successor.exact_changes 'head successor active_epoch.exact_changes'
    if ([string]$successor.epoch -notmatch '^r12-(?<number>[0-9]{4})$' -or [string]$BaseFacts.epoch.epoch -notmatch '^r12-(?<baseNumber>[0-9]{4})$') { throw 'base/successor epoch format drifted' }
    $successorNumber = [int]([regex]::Match([string]$successor.epoch, '^r12-([0-9]{4})$').Groups[1].Value)
    $baseNumber = [int]([regex]::Match([string]$BaseFacts.epoch.epoch, '^r12-([0-9]{4})$').Groups[1].Value)
    if ($successorNumber -ne $baseNumber + 1 -or [string]$manifest.successor_epoch -cne [string]$successor.epoch) { throw 'head successor epoch is not the exact monotonic next R12 epoch' }
    if ([string]$successor.state -cne 'open' -or [string]::IsNullOrWhiteSpace([string]$successor.reason) -or ([string]$successor.reason).Length -gt 512 -or [string]$successor.reason -match '[\x00-\x08\x0B\x0C\x0E-\x1F]') { throw 'head successor active epoch state or reason is invalid' }
    $successorNotBefore = ConvertTo-Rfc3339 $successor.not_before 'head successor not_before'
    $successorExpires = ConvertTo-Rfc3339 $successor.expires_at 'head successor expires_at'
    if ($successorNotBefore -lt $issued -or $successorNotBefore -gt $manifestExpires) { throw 'head successor not_before is outside the accepted manifest window' }
    if ($successorExpires -le $successorNotBefore -or ($successorExpires-$successorNotBefore).TotalSeconds -gt [int64]$BaseFacts.maintenance.lifecycle_policy.max_epoch_lifetime_seconds) { throw 'head successor epoch lifetime is invalid or unbounded' }
    if (($successorExpires-$Now).TotalSeconds -le [int64]$BaseFacts.maintenance.lifecycle_policy.rotate_before_expiry_seconds) { throw 'head successor epoch lacks the required post-validation rotation window' }
    $successorApproval = $successor.approval
    Assert-ObjectShape $successorApproval @('actor_login','actor_id','actor_type','association','label','approval_epoch') 'head successor approval'
    Assert-JsonInteger $successorApproval.actor_id 7106373 'head successor approval.actor_id'
    if ([string]$successorApproval.actor_login -cne 'thebtf' -or [int64]$successorApproval.actor_id -ne 7106373 -or [string]$successorApproval.actor_type -cne 'User' -or [string]$successorApproval.association -cne 'OWNER' -or [string]$successorApproval.approval_epoch -cne [string]$successor.epoch -or [string]$successorApproval.label -cne "authority-maintenance:$([string]$successor.epoch)") { throw 'head successor approval authority/epoch/label drifted' }
    [object[]]$nextChanges = @($successor.exact_changes); Assert-ChangeArray $nextChanges 'head successor exact_changes' -AllowAdd
    if ($nextChanges.Count -lt 6 -or $nextChanges.Count -gt 32) { throw 'head successor exact_changes must contain 6..32 bounded protected paths' }
    $requiredSuccessorPaths = @(
        '.agent/plans/2026-07-10-engram-production-ready-active-diff-contracts.json',
        '.github/workflows/authority-guard.yml',
        '.github/workflows/test.yml',
        'scripts/production-gates/assert-active-candidate-path-authority.ps1',
        'scripts/production-gates/assert-pr-authority-guard.ps1',
        'scripts/production-gates/assert-pr-authority-maintenance.ps1'
    )
    [string[]]$nextPaths = @($nextChanges | ForEach-Object { [string]$_.path })
    foreach ($required in $requiredSuccessorPaths) { if ($required -cnotin $nextPaths) { throw "head successor exact_changes omits required control-plane path '$required'" } }
    if (@($headConsumed | Where-Object { [string]$_.epoch -ceq [string]$successor.epoch }).Count -ne 0) { throw 'head successor epoch was already consumed' }
    return [pscustomobject][ordered]@{ manifest=$manifest; canonical_manifest_sha256=$manifestSha256; issued_at=$issued; expires_at=$manifestExpires; successor=$successor; consumed=$consumed }
}

$startedAt = [DateTimeOffset]::UtcNow
$artifactObject = $null
$exitCode = 1
$trustedRoot = $null
try {
    if ($BaseSha -cnotmatch '^[0-9a-f]{40}$' -or $ExpectedValidatorGitBlob -cnotmatch '^[0-9a-f]{40}$' -or ($PSCmdlet.ParameterSetName -eq 'Transition' -and $HeadSha -cnotmatch '^[0-9a-f]{40}$')) { throw 'base, head, and validator Git identities must be canonical lowercase full SHAs' }
    if ($ValidateBaseOnly) {
        $repo = [System.IO.Path]::GetFullPath($Repository)
        if (-not (Test-Path -LiteralPath $repo -PathType Container)) { throw "repository does not exist: $repo" }
        if ($ExpectedDefaultBranch -notmatch '^[A-Za-z0-9._/-]+$') { throw 'expected default branch has invalid syntax' }
        $requiredBaseRef = "refs/heads/$ExpectedDefaultBranch"
        if ($BaseRemoteRef -cne $requiredBaseRef) { throw "base ref '$BaseRemoteRef' is not trusted default-branch ref '$requiredBaseRef'" }
        if ($ValidatorPath -cne 'scripts/production-gates/assert-pr-authority-maintenance.ps1') { throw 'base-only validator path drifted from the protected trusted-base path' }

        [void](Invoke-Git $repo @('rev-parse','--is-inside-work-tree'))
        [void](Invoke-Git $repo @('fetch','--no-tags','--force',$Remote,"+$BaseRemoteRef`:refs/authority/base"))
        $fetchedBase = [string](Invoke-Git $repo @('rev-parse','refs/authority/base^{commit}')).output[-1]
        if ($fetchedBase -cne $BaseSha) { throw "fetched default-branch base differs from the event base: expected=$BaseSha observed=$fetchedBase" }
        $baseValidatorBlob = [string](Invoke-Git $repo @('rev-parse',"$BaseSha`:$ValidatorPath")).output[-1]
        if ($baseValidatorBlob -cne $ExpectedValidatorGitBlob) { throw "trusted base-only validator blob mismatch: expected=$ExpectedValidatorGitBlob observed=$baseValidatorBlob" }
        $executedBlob = [string](Invoke-Git $repo @('hash-object','--no-filters',[System.IO.Path]::GetFullPath($PSCommandPath))).output[-1]
        if ($executedBlob -cne $ExpectedValidatorGitBlob) { throw "executed base-only validator is not the trusted base blob: expected=$ExpectedValidatorGitBlob observed=$executedBlob" }

        $trustedRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('engram-authority-maintenance-' + [guid]::NewGuid().ToString('N'))
        New-Item -ItemType Directory -Path $trustedRoot -Force | Out-Null
        $activePath = 'scripts/production-gates/assert-active-candidate-path-authority.ps1'
        $ownershipValidatorPath = 'scripts/production-gates/assert-plan-path-ownership.ps1'
        $ordinaryValidatorPath = 'scripts/production-gates/assert-pr-authority-guard.ps1'
        $contractPath = '.agent/plans/2026-07-10-engram-production-ready-active-diff-contracts.json'
        $planPath = '.agent/plans/2026-07-10-engram-production-ready-master-plan.md'
        $statePath = '.agent/plans/2026-07-10-engram-production-ready-ownership-state.json'
        $scopeMapPath = '.agent/plans/2026-07-10-engram-production-ready-scope-map.json'
        $planGovernancePath = '.agent/specs/release-gates-r12/evidence/plan-governance/test-r12-plan-governance.ps1'
        $pathEnvelopePath = '.agent/specs/release-gates-r12/evidence/plan-governance/path-envelope.json'
        $fixedPointPath = '.agent/specs/release-gates-r12/evidence/plan-governance/fixed-point-proof.json'
        $authoritySnapshotPath = '.agent/specs/release-gates-r12/evidence/plan-governance/authority-snapshot.json'
        $activeFile = Join-Path $trustedRoot 'assert-active-candidate-path-authority.ps1'
        $ownershipValidatorFile = Join-Path $trustedRoot 'assert-plan-path-ownership.ps1'
        $contractFile = Join-Path $trustedRoot 'base-active-diff-contracts.json'
        $planFile = Join-Path $trustedRoot 'base-master-plan.md'
        $stateFile = Join-Path $trustedRoot 'base-ownership-state.json'
        $scopeMapFile = Join-Path $trustedRoot 'base-scope-map.json'
        $planGovernanceFile = Join-Path $trustedRoot 'test-r12-plan-governance.ps1'
        $pathEnvelopeFile = Join-Path $trustedRoot 'path-envelope.json'
        $fixedPointFile = Join-Path $trustedRoot 'fixed-point-proof.json'
        $authoritySnapshotFile = Join-Path $trustedRoot 'authority-snapshot.json'
        Export-GitBlob $repo "$BaseSha`:$activePath" $activeFile
        Export-GitBlob $repo "$BaseSha`:$ownershipValidatorPath" $ownershipValidatorFile
        Export-GitBlob $repo "$BaseSha`:$contractPath" $contractFile
        Export-GitBlob $repo "$BaseSha`:$planPath" $planFile
        Export-GitBlob $repo "$BaseSha`:$statePath" $stateFile
        Export-GitBlob $repo "$BaseSha`:$scopeMapPath" $scopeMapFile
        Export-GitBlob $repo "$BaseSha`:$planGovernancePath" $planGovernanceFile
        Export-GitBlob $repo "$BaseSha`:$pathEnvelopePath" $pathEnvelopeFile
        Export-GitBlob $repo "$BaseSha`:$fixedPointPath" $fixedPointFile
        Export-GitBlob $repo "$BaseSha`:$authoritySnapshotPath" $authoritySnapshotFile
        $activeBlob = [string](Invoke-Git $repo @('rev-parse',"$BaseSha`:$activePath")).output[-1]
        $ownershipValidatorBlob = [string](Invoke-Git $repo @('rev-parse',"$BaseSha`:$ownershipValidatorPath")).output[-1]
        if ([string](Invoke-Git $repo @('hash-object','--no-filters',$activeFile)).output[-1] -cne $activeBlob) { throw 'base-only active validator export differs from its trusted Git blob' }
        if ([string](Invoke-Git $repo @('hash-object','--no-filters',$ownershipValidatorFile)).output[-1] -cne $ownershipValidatorBlob) { throw 'base-only ownership validator export differs from its trusted Git blob' }

        $contractHash = Get-CanonicalTextSha256 $contractFile; $planHash = Get-CanonicalTextSha256 $planFile; $stateHash = Get-CanonicalTextSha256 $stateFile; $scopeMapHash = Get-CanonicalTextSha256 $scopeMapFile
        $baseText = [System.IO.File]::ReadAllText($contractFile); Assert-NoDuplicateJsonProperties $baseText 'base-only active contract'
        $baseContract = $baseText | ConvertFrom-Json -Depth 100
        $baseFacts = Assert-BaseMaintenanceAuthority $baseContract ([DateTimeOffset]::UtcNow)
        $ownershipArtifact = Join-Path $trustedRoot 'base-only-plan-ownership.json'
        & pwsh -NoProfile -File $ownershipValidatorFile -Mode Ledger -Plan $planFile -ExpectedPlanSha256 $planHash -State $stateFile -ScopeMap $scopeMapFile -ExpectedScopeMapSha256 $scopeMapHash -Artifact $ownershipArtifact
        if ($LASTEXITCODE -ne 0) { throw 'base-only plan/state/scope ownership validation failed' }
        $bootstrapClosedWorld = $baseFacts.consumed.Count -eq 0
        if ($bootstrapClosedWorld) {
            & pwsh -NoProfile -File $planGovernanceFile -Plan $planFile -OwnershipState $stateFile -ScopeMap $scopeMapFile -ActiveContracts $contractFile -PathEnvelope $pathEnvelopeFile -FixedPointProof $fixedPointFile -AuthoritySnapshot $authoritySnapshotFile
            if ($LASTEXITCODE -ne 0) { throw 'base-only bootstrap closed-world proof failed' }
        }
        $activeArtifact = Join-Path $trustedRoot 'base-only-active-authority.json'
        & pwsh -NoProfile -File $activeFile -Contract $contractFile -ExpectedContractSha256 $contractHash -Plan $planFile -ExpectedPlanSha256 $planHash -Artifact $activeArtifact
        if ($LASTEXITCODE -ne 0 -or [string](Get-Content -LiteralPath $activeArtifact -Raw | ConvertFrom-Json -Depth 100).verdict -cne 'PASS') { throw 'base-only active authority validation failed' }
        [string[]]$validatorPaths = @($ordinaryValidatorPath, $activePath, $ValidatorPath, $ownershipValidatorPath)
        Assert-HistoricalTransitionGitAnchor -WorkingTree $repo -CurrentBaseSha $BaseSha -BaseFacts $baseFacts -ValidatorPaths $validatorPaths

        $finishedAt = [DateTimeOffset]::UtcNow
        $artifactObject = [ordered]@{
            schema_version=1; gate='pr-authority-maintenance-base'; verdict='PASS'; started_at=$startedAt.ToString('O'); finished_at=$finishedAt.ToString('O'); duration_seconds=[math]::Round(($finishedAt-$startedAt).TotalSeconds,3)
            base=[ordered]@{remote_ref=$BaseRemoteRef;expected_sha=$BaseSha;fetched_sha=$fetchedBase;active_epoch=[string]$baseFacts.epoch.epoch;consumed_epoch_count=$baseFacts.consumed.Count;bootstrap_closed_world_proof=$bootstrapClosedWorld;historical_anchor_valid=$null -ne $baseFacts.historical_anchor;strict_latest=$true}
            trusted_execution=[ordered]@{validator_path=$ValidatorPath;expected_git_blob=$ExpectedValidatorGitBlob;executed_git_blob=$executedBlob;active_validator_git_blob=$activeBlob;ownership_validator_git_blob=$ownershipValidatorBlob;contract_sha256=$contractHash;plan_sha256=$planHash;state_sha256=$stateHash;scope_map_sha256=$scopeMapHash;secrets_used=$false}
            errors=@()
        }
        $exitCode = 0
    }
    else {
    $repo = [System.IO.Path]::GetFullPath($Repository)
    if (-not (Test-Path -LiteralPath $repo -PathType Container)) { throw "repository does not exist: $repo" }
    if ($ExpectedDefaultBranch -notmatch '^[A-Za-z0-9._/-]+$') { throw 'expected default branch has invalid syntax' }
    $requiredBaseRef = "refs/heads/$ExpectedDefaultBranch"
    if ($BaseRemoteRef -cne $requiredBaseRef) { throw "base ref '$BaseRemoteRef' is not trusted default-branch ref '$requiredBaseRef'" }
    if ($HeadRemoteRef -notmatch '^refs/pull/[1-9][0-9]*/head$') { throw "head ref '$HeadRemoteRef' is not an explicit pull-request head ref" }
    if ($ValidatorPath -cne 'scripts/production-gates/assert-pr-authority-maintenance.ps1') { throw 'maintenance validator path drifted from the protected trusted-base path' }
    if ($EventRepositoryFullName -cne $EventHeadRepositoryFullName) { throw 'maintenance PR head must be in the same repository as the trusted base' }
    if ($ActorLogin -cne 'thebtf' -or $ActorId -ne 7106373 -or $ActorType -cne 'User' -or $AuthorAssociation -cne 'OWNER') { throw 'maintenance event actor identity/association is not the audited owner authority' }
    if ($ApprovalEpoch -notmatch '^r12-[0-9]{4}$' -or $ApprovalLabel -cne "authority-maintenance:$ApprovalEpoch") { throw 'maintenance approval label does not bind the exact approval epoch' }

    [void](Invoke-Git $repo @('rev-parse','--is-inside-work-tree'))
    [void](Invoke-Git $repo @('fetch','--no-tags','--force',$Remote,"+$BaseRemoteRef`:refs/authority/base"))
    [void](Invoke-Git $repo @('fetch','--no-tags','--force',$Remote,"+$HeadRemoteRef`:refs/authority/head"))
    $fetchedBase = [string](Invoke-Git $repo @('rev-parse','refs/authority/base^{commit}')).output[-1]
    $fetchedHead = [string](Invoke-Git $repo @('rev-parse','refs/authority/head^{commit}')).output[-1]
    if ($fetchedBase -cne $BaseSha) { throw "fetched default-branch base differs from the event base; maintenance PR is stale: expected=$BaseSha observed=$fetchedBase" }
    if ($fetchedHead -cne $HeadSha) { throw "fetched head SHA mismatch: expected=$HeadSha observed=$fetchedHead" }

    $baseValidatorBlob = [string](Invoke-Git $repo @('rev-parse',"$BaseSha`:$ValidatorPath")).output[-1]
    if ($baseValidatorBlob -cne $ExpectedValidatorGitBlob) { throw "trusted maintenance validator blob mismatch: expected=$ExpectedValidatorGitBlob observed=$baseValidatorBlob" }
    $executedBlob = [string](Invoke-Git $repo @('hash-object','--no-filters',[System.IO.Path]::GetFullPath($PSCommandPath))).output[-1]
    if ($executedBlob -cne $ExpectedValidatorGitBlob) { throw "executed maintenance validator is not the trusted base blob: expected=$ExpectedValidatorGitBlob observed=$executedBlob" }

    $ancestor = Invoke-Git $repo @('merge-base','--is-ancestor',$BaseSha,$HeadSha) -AllowFailure
    if ($ancestor.exit_code -ne 0) { throw 'maintenance PR head is not based on the exact current default-branch base' }
    $merge = Invoke-Git $repo @('merge-tree','--write-tree',$BaseSha,$HeadSha) -AllowFailure
    if ($merge.exit_code -ne 0 -or [string]$merge.output[-1] -cnotmatch '^[0-9a-f]{40}$') { throw "maintenance merge-tree failed or conflicted: $($merge.output -join ' ')" }
    $mergeTree = [string]$merge.output[-1]
    $headTree = [string](Invoke-Git $repo @('rev-parse',"$HeadSha`^{tree}")).output[-1]
    if ($mergeTree -cne $headTree) { throw "maintenance merge-tree '$mergeTree' differs from exact head tree '$headTree'" }
    [object[]]$diffEntries = @(Get-DiffEntries $repo $BaseSha $HeadSha)

    $trustedRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('engram-authority-maintenance-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $trustedRoot -Force | Out-Null
    $activePath = 'scripts/production-gates/assert-active-candidate-path-authority.ps1'
    $contractPath = '.agent/plans/2026-07-10-engram-production-ready-active-diff-contracts.json'
    $planPath = '.agent/plans/2026-07-10-engram-production-ready-master-plan.md'
    $statePath = '.agent/plans/2026-07-10-engram-production-ready-ownership-state.json'
    $scopeMapPath = '.agent/plans/2026-07-10-engram-production-ready-scope-map.json'
    $ownershipValidatorPath = 'scripts/production-gates/assert-plan-path-ownership.ps1'
    $planGovernancePath = '.agent/specs/release-gates-r12/evidence/plan-governance/test-r12-plan-governance.ps1'
    $pathEnvelopePath = '.agent/specs/release-gates-r12/evidence/plan-governance/path-envelope.json'
    $fixedPointPath = '.agent/specs/release-gates-r12/evidence/plan-governance/fixed-point-proof.json'
    $authoritySnapshotPath = '.agent/specs/release-gates-r12/evidence/plan-governance/authority-snapshot.json'
    $authorityWorkflowPath = '.github/workflows/authority-guard.yml'
    $testWorkflowPath = '.github/workflows/test.yml'
    $activeFile = Join-Path $trustedRoot 'assert-active-candidate-path-authority.ps1'
    $ownershipValidatorFile = Join-Path $trustedRoot 'assert-plan-path-ownership.ps1'
    $contractFile = Join-Path $trustedRoot 'base-active-diff-contracts.json'
    $headContractFile = Join-Path $trustedRoot 'head-active-diff-contracts.json'
    $planFile = Join-Path $trustedRoot 'base-master-plan.md'
    $stateFile = Join-Path $trustedRoot 'base-ownership-state.json'
    $scopeMapFile = Join-Path $trustedRoot 'base-scope-map.json'
    $headPlanFile = Join-Path $trustedRoot 'head-master-plan.md'
    $headStateFile = Join-Path $trustedRoot 'head-ownership-state.json'
    $headScopeMapFile = Join-Path $trustedRoot 'head-scope-map.json'
    $planGovernanceFile = Join-Path $trustedRoot 'test-r12-plan-governance.ps1'
    $pathEnvelopeFile = Join-Path $trustedRoot 'path-envelope.json'
    $fixedPointFile = Join-Path $trustedRoot 'fixed-point-proof.json'
    $authoritySnapshotFile = Join-Path $trustedRoot 'authority-snapshot.json'
    $headAuthorityWorkflowFile = Join-Path $trustedRoot 'head-authority-guard.yml'
    $baseAuthorityWorkflowFile = Join-Path $trustedRoot 'base-authority-guard.yml'
    $headTestWorkflowFile = Join-Path $trustedRoot 'head-test.yml'
    Export-GitBlob $repo "$BaseSha`:$activePath" $activeFile
    Export-GitBlob $repo "$BaseSha`:$ownershipValidatorPath" $ownershipValidatorFile
    Export-GitBlob $repo "$BaseSha`:$contractPath" $contractFile
    Export-GitBlob $repo "$HeadSha`:$contractPath" $headContractFile
    Export-GitBlob $repo "$BaseSha`:$planPath" $planFile
    Export-GitBlob $repo "$BaseSha`:$statePath" $stateFile
    Export-GitBlob $repo "$BaseSha`:$scopeMapPath" $scopeMapFile
    Export-GitBlob $repo "$HeadSha`:$planPath" $headPlanFile
    Export-GitBlob $repo "$HeadSha`:$statePath" $headStateFile
    Export-GitBlob $repo "$HeadSha`:$scopeMapPath" $headScopeMapFile
    Export-GitBlob $repo "$BaseSha`:$planGovernancePath" $planGovernanceFile
    Export-GitBlob $repo "$BaseSha`:$pathEnvelopePath" $pathEnvelopeFile
    Export-GitBlob $repo "$BaseSha`:$fixedPointPath" $fixedPointFile
    Export-GitBlob $repo "$BaseSha`:$authoritySnapshotPath" $authoritySnapshotFile
    Export-GitBlob $repo "$HeadSha`:$authorityWorkflowPath" $headAuthorityWorkflowFile
    Export-GitBlob $repo "$BaseSha`:$authorityWorkflowPath" $baseAuthorityWorkflowFile
    Export-GitBlob $repo "$HeadSha`:$testWorkflowPath" $headTestWorkflowFile
    $activeBlob = [string](Invoke-Git $repo @('rev-parse',"$BaseSha`:$activePath")).output[-1]
    $activeExportBlob = [string](Invoke-Git $repo @('hash-object','--no-filters',$activeFile)).output[-1]
    if ($activeBlob -cne $activeExportBlob) { throw 'trusted active-authority validator export differs from the base Git blob' }
    $ownershipValidatorBlob = [string](Invoke-Git $repo @('rev-parse',"$BaseSha`:$ownershipValidatorPath")).output[-1]
    $ownershipValidatorExportBlob = [string](Invoke-Git $repo @('hash-object','--no-filters',$ownershipValidatorFile)).output[-1]
    if ($ownershipValidatorBlob -cne $ownershipValidatorExportBlob) { throw 'trusted plan-ownership validator export differs from the base Git blob' }
    $baseActiveArtifact = Join-Path $trustedRoot 'base-active-authority.json'
    $headActiveArtifact = Join-Path $trustedRoot 'head-active-authority.json'
    $baseOwnershipArtifact = Join-Path $trustedRoot 'base-plan-ownership.json'
    $headOwnershipArtifact = Join-Path $trustedRoot 'head-plan-ownership.json'
    $contractHash = Get-CanonicalTextSha256 $contractFile; $planHash = Get-CanonicalTextSha256 $planFile; $stateHash = Get-CanonicalTextSha256 $stateFile; $scopeMapHash = Get-CanonicalTextSha256 $scopeMapFile
    $headContractHash = Get-CanonicalTextSha256 $headContractFile; $headPlanHash = Get-CanonicalTextSha256 $headPlanFile; $headStateHash = Get-CanonicalTextSha256 $headStateFile; $headScopeMapHash = Get-CanonicalTextSha256 $headScopeMapFile
    $baseText = [System.IO.File]::ReadAllText($contractFile); Assert-NoDuplicateJsonProperties $baseText 'base active contract'
    $headText = [System.IO.File]::ReadAllText($headContractFile); Assert-NoDuplicateJsonProperties $headText 'head active contract'
    $baseContract = $baseText | ConvertFrom-Json -Depth 100
    $headContract = $headText | ConvertFrom-Json -Depth 100
    if ([string]$baseContract.external_enforcement.repository -cne $EventRepositoryFullName) { throw "event repository '$EventRepositoryFullName' differs from trusted contract repository '$([string]$baseContract.external_enforcement.repository)'" }
    $baseFacts = Assert-BaseMaintenanceAuthority $baseContract ([DateTimeOffset]::UtcNow)
    & pwsh -NoProfile -File $ownershipValidatorFile -Mode Ledger -Plan $planFile -ExpectedPlanSha256 $planHash -State $stateFile -ScopeMap $scopeMapFile -ExpectedScopeMapSha256 $scopeMapHash -Artifact $baseOwnershipArtifact
    if ($LASTEXITCODE -ne 0) { throw 'trusted-base plan/state/scope ownership validation failed' }
    $bootstrapClosedWorld = $baseFacts.consumed.Count -eq 0
    if ($bootstrapClosedWorld) {
        & pwsh -NoProfile -File $planGovernanceFile -Plan $planFile -OwnershipState $stateFile -ScopeMap $scopeMapFile -ActiveContracts $contractFile -PathEnvelope $pathEnvelopeFile -FixedPointProof $fixedPointFile -AuthoritySnapshot $authoritySnapshotFile
        if ($LASTEXITCODE -ne 0) { throw 'trusted-base R12 bootstrap closed-world plan-governance proof failed' }
    }
    & pwsh -NoProfile -File $activeFile -Contract $contractFile -ExpectedContractSha256 $contractHash -Plan $planFile -ExpectedPlanSha256 $planHash -Artifact $baseActiveArtifact
    if ($LASTEXITCODE -ne 0) { throw 'trusted-base active candidate authority validator failed during maintenance validation' }
    $baseActiveResult = Get-Content -LiteralPath $baseActiveArtifact -Raw | ConvertFrom-Json -Depth 100
    if ([string]$baseActiveResult.verdict -cne 'PASS') { throw 'trusted-base active candidate authority artifact is not PASS' }
    & pwsh -NoProfile -File $ownershipValidatorFile -Mode Ledger -Plan $headPlanFile -ExpectedPlanSha256 $headPlanHash -State $headStateFile -ScopeMap $headScopeMapFile -ExpectedScopeMapSha256 $headScopeMapHash -Artifact $headOwnershipArtifact
    if ($LASTEXITCODE -ne 0) {
        $detail = if (Test-Path -LiteralPath $headOwnershipArtifact) { @((Get-Content -LiteralPath $headOwnershipArtifact -Raw | ConvertFrom-Json -Depth 100).errors) -join '; ' } else { 'artifact missing' }
        throw "candidate head plan/state/scope ownership validation failed under trusted-base bytes: $detail"
    }
    & pwsh -NoProfile -File $activeFile -Contract $headContractFile -ExpectedContractSha256 $headContractHash -Plan $headPlanFile -ExpectedPlanSha256 $headPlanHash -Artifact $headActiveArtifact
    if ($LASTEXITCODE -ne 0) {
        $detail = if (Test-Path -LiteralPath $headActiveArtifact) { @((Get-Content -LiteralPath $headActiveArtifact -Raw | ConvertFrom-Json -Depth 100).errors) -join '; ' } else { 'artifact missing' }
        throw "candidate head active authority validation failed under trusted-base bytes: $detail"
    }
    $headActiveResult = Get-Content -LiteralPath $headActiveArtifact -Raw | ConvertFrom-Json -Depth 100
    if ([string]$headActiveResult.verdict -cne 'PASS') { throw 'candidate head active authority artifact is not PASS' }
    Assert-HeadWorkflowSafety -BaseAuthorityWorkflow $baseAuthorityWorkflowFile -AuthorityWorkflow $headAuthorityWorkflowFile -TestWorkflow $headTestWorkflowFile
    if ([string]$baseFacts.epoch.approval.label -cne $ApprovalLabel -or [string]$baseFacts.epoch.approval.approval_epoch -cne $ApprovalEpoch) { throw 'trusted event approval label/epoch differs from the active base epoch' }
    if ((Get-ChangeSignature $diffEntries) -cne (Get-ChangeSignature $baseFacts.changes)) { throw 'actual maintenance diff does not exactly equal the active trusted-base epoch status/path set' }

    [string[]]$validatorPaths = @($baseFacts.maintenance.current_validator_path, $activePath, $baseFacts.maintenance.maintenance_validator_path, $ownershipValidatorPath)
    Assert-HistoricalTransitionGitAnchor -WorkingTree $repo -CurrentBaseSha $BaseSha -BaseFacts $baseFacts -ValidatorPaths $validatorPaths
    [object[]]$currentValidatorBlobs = @(Get-ValidatorBlobs $repo $BaseSha $validatorPaths)
    [object[]]$successorValidatorBlobs = @(Get-ValidatorBlobs $repo $HeadSha $validatorPaths)
    $headFacts = Assert-HeadMaintenanceTransition -BaseFacts $baseFacts -HeadContract $headContract -DiffEntries $diffEntries -CurrentValidatorBlobs $currentValidatorBlobs -SuccessorValidatorBlobs $successorValidatorBlobs -EventBaseSha $BaseSha -Now ([DateTimeOffset]::UtcNow)
    $manifestContainerBlob = [string](Invoke-Git $repo @('rev-parse',"$HeadSha`:$contractPath")).output[-1]
    if ($manifestContainerBlob -cnotmatch '^[0-9a-f]{40}$') { throw 'head manifest container is not one immutable Git blob' }
    if ([string]$baseContract.external_enforcement.required_status_context -cne 'authority-guard' -or [int64]$baseContract.external_enforcement.required_status_integration_id -ne 15368 -or [string]$baseContract.external_enforcement.required_status_app_slug -cne 'github-actions') { throw 'trusted required-status identity drifted' }

    $finishedAt = [DateTimeOffset]::UtcNow
    $artifactObject = [ordered]@{
        schema_version=1; gate='pr-authority-maintenance'; verdict='PASS'; started_at=$startedAt.ToString('O'); finished_at=$finishedAt.ToString('O'); duration_seconds=[math]::Round(($finishedAt-$startedAt).TotalSeconds,3)
        repository=[ordered]@{event=$EventRepositoryFullName;head=$EventHeadRepositoryFullName;same_repository=$true;default_branch=$ExpectedDefaultBranch}
        approval=[ordered]@{actor_login=$ActorLogin;actor_id=$ActorId;actor_type=$ActorType;association=$AuthorAssociation;label=$ApprovalLabel;epoch=$ApprovalEpoch;trusted_event_metadata_only=$true}
        base=[ordered]@{remote_ref=$BaseRemoteRef;expected_sha=$BaseSha;fetched_sha=$fetchedBase;active_epoch=[string]$baseFacts.epoch.epoch;consumed_epoch_count=$baseFacts.consumed.Count;bootstrap_closed_world_proof=$bootstrapClosedWorld;historical_anchor_valid=$null -ne $baseFacts.historical_anchor;strict_latest=$true}
        head=[ordered]@{remote_ref=$HeadRemoteRef;expected_sha=$HeadSha;fetched_sha=$fetchedHead;tree=$headTree;treated_as_data_only=$true;executed=$false;checked_out=$false;successor_epoch=[string]$headFacts.successor.epoch}
        trusted_execution=[ordered]@{validator_path=$ValidatorPath;expected_git_blob=$ExpectedValidatorGitBlob;executed_git_blob=$executedBlob;active_validator_git_blob=$activeBlob;ownership_validator_path=$ownershipValidatorPath;ownership_validator_git_blob=$ownershipValidatorBlob;bootstrap_plan_governance_validator_path=$planGovernancePath;contract_sha256=$contractHash;plan_sha256=$planHash;state_sha256=$stateHash;scope_map_sha256=$scopeMapHash;head_contract_sha256=$headContractHash;head_plan_sha256=$headPlanHash;head_state_sha256=$headStateHash;head_scope_map_sha256=$headScopeMapHash;event_base_sha=$BaseSha;event_head_sha=$HeadSha;manifest_container_git_blob=$manifestContainerBlob;canonical_manifest_sha256=[string]$headFacts.canonical_manifest_sha256;trusted_validator_git_blob=$executedBlob;required_status_context='authority-guard';required_status_integration_id=15368;required_status_app_slug='github-actions';candidate_workflow_safety_valid=$true;candidate_artifact_is_data_only=$true;secrets_used=$false}
        merge_tree=$mergeTree; changed_paths=@($diffEntries); current_validator_blobs=@($currentValidatorBlobs); successor_validator_blobs=@($successorValidatorBlobs)
        transition=[ordered]@{reason=[string]$headFacts.manifest.reason;created_at=$headFacts.issued_at.ToString('O');expires_at=$headFacts.expires_at.ToString('O');consumed_epoch=[string]$headFacts.consumed.epoch;successor_epoch=[string]$headFacts.successor.epoch;canonical_manifest_sha256=[string]$headFacts.canonical_manifest_sha256;manifest_container_excluded_from_self_reference=$true;all_other_protected_head_blobs_bound=$true;replay_state_advanced=$true}
        errors=@()
    }
    $exitCode = 0
    }
}
catch {
    $finishedAt = [DateTimeOffset]::UtcNow
    $artifactObject = [ordered]@{schema_version=1;gate='pr-authority-maintenance';verdict='FAIL';started_at=$startedAt.ToString('O');finished_at=$finishedAt.ToString('O');head=[ordered]@{treated_as_data_only=$true;executed=$false;checked_out=$false};trusted_execution=[ordered]@{secrets_used=$false};errors=@($_.Exception.Message)}
    $exitCode = 1
}
finally {
    if ($null -ne $trustedRoot -and (Test-Path -LiteralPath $trustedRoot)) {
        $tempPrefix = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath()).TrimEnd('\','/') + [System.IO.Path]::DirectorySeparatorChar
        $target = [System.IO.Path]::GetFullPath($trustedRoot)
        if (-not $target.StartsWith($tempPrefix, [System.StringComparison]::OrdinalIgnoreCase) -or -not ([System.IO.Path]::GetFileName($target)).StartsWith('engram-authority-maintenance-', [System.StringComparison]::Ordinal)) { throw "refusing unsafe temp cleanup '$target'" }
        Remove-Item -LiteralPath $target -Recurse -Force
    }
}

Write-Utf8NoBom -Path $Artifact -Text (($artifactObject | ConvertTo-Json -Depth 100) + "`n")
Write-Output "pr-authority-maintenance verdict=$($artifactObject.verdict) artifact=$Artifact"
exit $exitCode
