[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Repository,
    [Parameter(Mandatory)][string]$Remote,
    [Parameter(Mandatory)][string]$BaseRemoteRef,
    [Parameter(Mandatory)][ValidatePattern('^[0-9a-f]{40}$')][string]$BaseSha,
    [Parameter(Mandatory)][string]$HeadRemoteRef,
    [Parameter(Mandatory)][ValidatePattern('^[0-9a-f]{40}$')][string]$HeadSha,
    [string]$ExpectedDefaultBranch = 'main',
    [Parameter(Mandatory)][string]$EventAction,
    [Parameter(Mandatory)][AllowEmptyString()][string]$EventLabel,
    [Parameter(Mandatory)][string]$EventRepositoryFullName,
    [Parameter(Mandatory)][string]$EventHeadRepositoryFullName,
    [Parameter(Mandatory)][string]$ActorLogin,
    [Parameter(Mandatory)][long]$ActorId,
    [Parameter(Mandatory)][string]$ActorType,
    [string]$ValidatorPath = 'scripts/production-gates/assert-pr-authority-data-only.ps1',
    [string]$PolicyPath = '.github/authority-policy.json',
    [Parameter(Mandatory)][ValidatePattern('^[0-9a-f]{40}$')][string]$ExpectedValidatorGitBlob,
    [string]$Artifact = '.agent/e/default-branch-authority/result.json'
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
    if (-not $AllowFailure -and $code -ne 0) {
        throw "git $($Arguments -join ' ') failed ($code): $($output -join ' ')"
    }
    return [pscustomobject][ordered]@{
        exit_code = $code
        output = @($output | ForEach-Object { [string]$_ })
    }
}

function Get-GitLine {
    param([Parameter(Mandatory)][string]$WorkingTree, [Parameter(Mandatory)][string[]]$Arguments)
    $result = Invoke-Git $WorkingTree $Arguments
    $lines = @($result.output | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    if ($lines.Count -ne 1) { throw "git $($Arguments -join ' ') returned $($lines.Count) non-empty lines" }
    return [string]$lines[0].Trim()
}

function Export-GitBlob {
    param([Parameter(Mandatory)][string]$WorkingTree, [Parameter(Mandatory)][string]$Spec, [Parameter(Mandatory)][string]$Destination)
    $parent = Split-Path -Parent $Destination
    if ($parent) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }
    $start = [System.Diagnostics.ProcessStartInfo]::new()
    $start.FileName = 'git'
    $start.UseShellExecute = $false
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    foreach ($argument in @('-C', $WorkingTree, 'cat-file', 'blob', $Spec)) { [void]$start.ArgumentList.Add($argument) }
    $process = [System.Diagnostics.Process]::new()
    $process.StartInfo = $start
    if (-not $process.Start()) { throw "could not start git cat-file for '$Spec'" }
    $stream = [System.IO.File]::Open($Destination, [System.IO.FileMode]::Create, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
    try { $process.StandardOutput.BaseStream.CopyTo($stream) } finally { $stream.Dispose() }
    $stderr = $process.StandardError.ReadToEnd()
    $process.WaitForExit()
    if ($process.ExitCode -ne 0) { throw "git cat-file failed for '$Spec': $stderr" }
}

function Assert-CanonicalPath {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$Context, [switch]$Prefix)
    if ([string]::IsNullOrWhiteSpace($Path) -or $Path -cne $Path.Trim() -or $Path.Contains('\') -or $Path.StartsWith('/') -or $Path.Contains('//') -or $Path -match '(^|/)(\.|\.\.)(/|$)') {
        throw "$Context contains non-canonical path '$Path'"
    }
    if ($Prefix) {
        if (-not $Path.EndsWith('/')) { throw "$Context prefix '$Path' must end with '/'" }
    }
    elseif ($Path.EndsWith('/')) {
        throw "$Context exact path '$Path' must not end with '/'"
    }
    if ($Path -notmatch '^[A-Za-z0-9._/-]+$') { throw "$Context path '$Path' contains unsupported characters" }
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
                foreach ($item in $Element.EnumerateArray()) {
                    & $walk $item "$Path[$index]"
                    $index++
                }
            }
        }
        & $walk $document.RootElement '$'
    }
    finally { $document.Dispose() }
}

function Assert-ObjectShape {
    param([Parameter(Mandatory)]$Object, [Parameter(Mandatory)][string[]]$Names, [Parameter(Mandatory)][string]$Context)
    if ($Object -isnot [pscustomobject]) { throw "$Context must be a JSON object" }
    [string[]]$actual = @($Object.PSObject.Properties.Name)
    [string[]]$expected = @($Names)
    [Array]::Sort($actual, [System.StringComparer]::Ordinal)
    [Array]::Sort($expected, [System.StringComparer]::Ordinal)
    if (($actual -join "`n") -cne ($expected -join "`n")) {
        throw "$Context property set drifted: expected=[$($expected -join ', ')] observed=[$($actual -join ', ')]"
    }
}

function Get-CanonicalJsonText {
    param([AllowNull()]$Value)
    if ($null -eq $Value) { return 'null' }
    if ($Value -is [string]) { return ($Value | ConvertTo-Json -Compress) }
    if ($Value -is [bool]) { return $(if ($Value) { 'true' } else { 'false' }) }
    if ($Value -is [byte] -or $Value -is [int16] -or $Value -is [int32] -or $Value -is [int64] -or $Value -is [decimal] -or $Value -is [double]) {
        return ([Convert]::ToString($Value, [Globalization.CultureInfo]::InvariantCulture))
    }
    if ($Value -is [pscustomobject]) {
        [string[]]$names = @($Value.PSObject.Properties.Name)
        [Array]::Sort($names, [System.StringComparer]::Ordinal)
        $parts = foreach ($name in $names) { "$(($name | ConvertTo-Json -Compress)):$(Get-CanonicalJsonText $Value.$name)" }
        return '{' + ($parts -join ',') + '}'
    }
    if ($Value -is [System.Collections.IDictionary]) {
        [string[]]$names = @($Value.Keys | ForEach-Object { [string]$_ })
        [Array]::Sort($names, [System.StringComparer]::Ordinal)
        $parts = foreach ($name in $names) { "$(($name | ConvertTo-Json -Compress)):$(Get-CanonicalJsonText $Value[$name])" }
        return '{' + ($parts -join ',') + '}'
    }
    if ($Value -is [System.Collections.IEnumerable]) {
        $parts = foreach ($item in $Value) { Get-CanonicalJsonText $item }
        return '[' + ($parts -join ',') + ']'
    }
    return ($Value | ConvertTo-Json -Compress)
}

function Test-ProtectedPath {
    param([Parameter(Mandatory)]$Policy, [Parameter(Mandatory)][string]$Path)
    if ($Path -cin @($Policy.protected_paths.exact)) { return $true }
    foreach ($prefix in @($Policy.protected_paths.prefixes)) {
        if ($Path.StartsWith([string]$prefix, [System.StringComparison]::Ordinal)) { return $true }
    }
    return $false
}

function Get-ChangeSignature {
    param([Parameter(Mandatory)][object[]]$Entries)
    [string[]]$values = @($Entries | ForEach-Object { "$([string]$_.status)`t$([string]$_.path)" })
    [Array]::Sort($values, [System.StringComparer]::Ordinal)
    return ($values -join "`n")
}

function Get-BlobSignature {
    param([Parameter(Mandatory)][AllowEmptyCollection()][object[]]$Entries)
    [string[]]$values = @($Entries | ForEach-Object { "$([string]$_.path)`t$([string]$_.git_blob)" })
    [Array]::Sort($values, [System.StringComparer]::Ordinal)
    return ($values -join "`n")
}

function Assert-Policy {
    param([Parameter(Mandatory)]$Policy, [Parameter(Mandatory)][string]$Context)
    Assert-ObjectShape $Policy @('schema_version','kind','repository','default_branch','protected_paths','trusted_actor','transition','active_epoch') $Context
    if ($Policy.schema_version -isnot [long] -and $Policy.schema_version -isnot [int]) { throw "$Context schema_version must be an integer" }
    if ([int64]$Policy.schema_version -ne 1) { throw "$Context schema_version must be 1" }
    if ([string]$Policy.kind -cne 'engram-default-branch-authority-policy') { throw "$Context kind drifted" }
    if ([string]$Policy.repository -notmatch '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$') { throw "$Context repository is invalid" }
    if ([string]$Policy.default_branch -notmatch '^[A-Za-z0-9._/-]+$') { throw "$Context default_branch is invalid" }

    Assert-ObjectShape $Policy.protected_paths @('exact','prefixes') "$Context protected_paths"
    $seenProtected = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    foreach ($path in @($Policy.protected_paths.exact)) {
        if ($path -isnot [string]) { throw "$Context protected exact path must be a string" }
        Assert-CanonicalPath $path "$Context protected exact"
        if (-not $seenProtected.Add("exact:$path")) { throw "$Context repeats protected exact path '$path'" }
    }
    foreach ($prefix in @($Policy.protected_paths.prefixes)) {
        if ($prefix -isnot [string]) { throw "$Context protected prefix must be a string" }
        Assert-CanonicalPath $prefix "$Context protected prefix" -Prefix
        if (-not $seenProtected.Add("prefix:$prefix")) { throw "$Context repeats protected prefix '$prefix'" }
    }
    if ('.github/authority-policy.json' -cnotin @($Policy.protected_paths.exact)) { throw "$Context must protect its own policy path" }

    Assert-ObjectShape $Policy.trusted_actor @('login','id','type') "$Context trusted_actor"
    if ([string]::IsNullOrWhiteSpace([string]$Policy.trusted_actor.login) -or [int64]$Policy.trusted_actor.id -le 0 -or [string]$Policy.trusted_actor.type -cne 'User') {
        throw "$Context trusted_actor is invalid"
    }

    Assert-ObjectShape $Policy.transition @('consumed_epoch','event_base_sha') "$Context transition"
    $transitionEpoch = $Policy.transition.consumed_epoch
    $transitionBase = $Policy.transition.event_base_sha
    if (($null -eq $transitionEpoch) -ne ($null -eq $transitionBase)) { throw "$Context transition fields must both be null or both be set" }
    if ($null -ne $transitionEpoch) {
        if ([string]$transitionEpoch -notmatch '^authority-[0-9]{4}$' -or [string]$transitionBase -notmatch '^[0-9a-f]{40}$') { throw "$Context transition is invalid" }
    }

    Assert-ObjectShape $Policy.active_epoch @('id','label','exact_changes','expected_head_blobs') "$Context active_epoch"
    if ([string]$Policy.active_epoch.id -notmatch '^authority-(?<number>[0-9]{4})$') { throw "$Context active_epoch.id is invalid" }
    $epochNumber = [int]$Matches.number
    if ([string]$Policy.active_epoch.label -cne "authority-maintenance:$([string]$Policy.active_epoch.id)") { throw "$Context active_epoch.label does not bind its epoch" }

    [object[]]$changes = @($Policy.active_epoch.exact_changes)
    if ($changes.Count -lt 1 -or $changes.Count -gt 32) { throw "$Context active_epoch.exact_changes must contain 1..32 entries" }
    $seenChanges = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    foreach ($change in $changes) {
        Assert-ObjectShape $change @('status','path') "$Context active_epoch.exact_changes entry"
        if ([string]$change.status -cnotin @('A','M')) { throw "$Context active epoch contains forbidden status '$([string]$change.status)'" }
        Assert-CanonicalPath ([string]$change.path) "$Context active epoch"
        if (-not (Test-ProtectedPath $Policy ([string]$change.path))) { throw "$Context active epoch path '$([string]$change.path)' is outside protected authority" }
        if (-not $seenChanges.Add("$([string]$change.status)`t$([string]$change.path)")) { throw "$Context repeats active epoch change '$([string]$change.path)'" }
    }
    if (@($changes | Where-Object { [string]$_.path -ceq '.github/authority-policy.json' -and [string]$_.status -ceq 'M' }).Count -ne 1) {
        throw "$Context active epoch must modify the authority policy exactly once"
    }

    [object[]]$expectedBlobs = @($Policy.active_epoch.expected_head_blobs)
    $seenBlobs = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    foreach ($entry in $expectedBlobs) {
        Assert-ObjectShape $entry @('path','git_blob') "$Context active_epoch.expected_head_blobs entry"
        Assert-CanonicalPath ([string]$entry.path) "$Context expected head blob"
        if ([string]$entry.path -ceq '.github/authority-policy.json') { throw "$Context must not bind the policy file to its own future blob" }
        if ([string]$entry.git_blob -notmatch '^[0-9a-f]{40}$') { throw "$Context expected head blob is invalid" }
        if (-not $seenBlobs.Add([string]$entry.path)) { throw "$Context repeats expected head blob path '$([string]$entry.path)'" }
    }
    [string[]]$expectedBlobPaths = @($changes | Where-Object { [string]$_.path -cne '.github/authority-policy.json' } | ForEach-Object { [string]$_.path })
    [string[]]$actualBlobPaths = @($expectedBlobs | ForEach-Object { [string]$_.path })
    [Array]::Sort($expectedBlobPaths, [System.StringComparer]::Ordinal)
    [Array]::Sort($actualBlobPaths, [System.StringComparer]::Ordinal)
    if (($expectedBlobPaths -join "`n") -cne ($actualBlobPaths -join "`n")) { throw "$Context expected head blobs must cover every non-policy active change exactly" }

    return [pscustomobject][ordered]@{
        policy = $Policy
        epoch_number = $epochNumber
        changes = $changes
        expected_blobs = $expectedBlobs
    }
}

function Read-PolicyBlob {
    param([Parameter(Mandatory)][string]$WorkingTree, [Parameter(Mandatory)][string]$Spec, [Parameter(Mandatory)][string]$Destination, [Parameter(Mandatory)][string]$Context)
    Export-GitBlob $WorkingTree $Spec $Destination
    $text = [System.IO.File]::ReadAllText($Destination)
    Assert-NoDuplicateJsonProperties $text $Context
    $policy = $text | ConvertFrom-Json -Depth 100
    $facts = Assert-Policy $policy $Context
    return $facts
}

function Get-DiffEntries {
    param([Parameter(Mandatory)][string]$WorkingTree, [Parameter(Mandatory)][string]$Base, [Parameter(Mandatory)][string]$Head)
    $result = Invoke-Git $WorkingTree @('-c','core.quotepath=false','diff','--name-status','--no-renames',"$Base..$Head",'--')
    $entries = [System.Collections.Generic.List[object]]::new()
    foreach ($line in $result.output) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        $parts = $line -split "`t"
        if ($parts.Count -ne 2 -or $parts[0] -notmatch '^[AMDT]$') { throw "unsupported or malformed PR diff line '$line'" }
        $status = [string]$parts[0]
        $path = [string]$parts[1]
        Assert-CanonicalPath $path 'PR diff'
        $mode = $null
        $object = $null
        if ($status -cne 'D') {
            $headEntry = Invoke-Git $WorkingTree @('-c','core.quotepath=false','ls-tree','--full-tree',$Head,'--',":(literal)$path")
            if ($headEntry.output.Count -ne 1 -or [string]$headEntry.output[0] -notmatch '^(?<mode>100644|100755) blob (?<object>[0-9a-f]{40})\t(?<path>.+)$') {
                throw "changed PR path '$path' is not one regular 100644/100755 blob"
            }
            if ([string]$Matches.path -cne $path) { throw "changed PR path '$path' resolved as '$([string]$Matches.path)'" }
            $objectType = Invoke-Git $WorkingTree @('cat-file','-t',[string]$Matches.object) -AllowFailure
            if ($objectType.exit_code -ne 0 -or $objectType.output.Count -ne 1 -or [string]$objectType.output[0] -cne 'blob') { throw "changed PR path '$path' head object is unavailable or not a blob" }
            $mode = [string]$Matches.mode
            $object = [string]$Matches.object
        }
        $entries.Add([pscustomobject][ordered]@{ status=$status; path=$path; head_mode=$mode; head_blob=$object })
    }
    if ($entries.Count -eq 0) { throw 'PR diff contains zero paths' }
    return @($entries)
}

$startedAt = [DateTimeOffset]::UtcNow
$artifactObject = $null
$exitCode = 1
$tempRoot = $null
try {
    $repo = [System.IO.Path]::GetFullPath($Repository)
    if (-not (Test-Path -LiteralPath $repo -PathType Container)) { throw "repository does not exist: $repo" }
    if ($ExpectedDefaultBranch -notmatch '^[A-Za-z0-9._/-]+$') { throw 'expected default branch is invalid' }
    $requiredBaseRef = "refs/heads/$ExpectedDefaultBranch"
    if ($BaseRemoteRef -cne $requiredBaseRef) { throw "base ref '$BaseRemoteRef' is not trusted default-branch ref '$requiredBaseRef'" }
    if ($HeadRemoteRef -notmatch '^refs/pull/[1-9][0-9]*/head$') { throw "head ref '$HeadRemoteRef' is not an explicit pull-request head ref" }
    if ($ValidatorPath -cne 'scripts/production-gates/assert-pr-authority-data-only.ps1') { throw 'validator path drifted from the protected trusted-base path' }
    if ($PolicyPath -cne '.github/authority-policy.json') { throw 'policy path drifted from the protected trusted-base path' }
    if ($EventAction -notmatch '^[a-z_]+$') { throw 'event action is invalid' }
    if ($EventRepositoryFullName -notmatch '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$' -or $EventHeadRepositoryFullName -notmatch '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$') { throw 'event repository identity is invalid' }

    [void](Invoke-Git $repo @('rev-parse','--is-inside-work-tree'))
    [void](Invoke-Git $repo @('fetch','--no-tags','--force',$Remote,"+$BaseRemoteRef`:refs/authority/base"))
    [void](Invoke-Git $repo @('fetch','--no-tags','--force',$Remote,"+$HeadRemoteRef`:refs/authority/head"))
    $fetchedBase = Get-GitLine $repo @('rev-parse','refs/authority/base^{commit}')
    $fetchedHead = Get-GitLine $repo @('rev-parse','refs/authority/head^{commit}')
    if ($fetchedBase -cne $BaseSha) { throw "fetched default-branch base differs from event base: expected=$BaseSha observed=$fetchedBase" }
    if ($fetchedHead -cne $HeadSha) { throw "fetched PR head differs from event head: expected=$HeadSha observed=$fetchedHead" }

    $baseValidatorBlob = Get-GitLine $repo @('rev-parse',"$BaseSha`:$ValidatorPath")
    if ($baseValidatorBlob -cne $ExpectedValidatorGitBlob) { throw "trusted validator blob mismatch: expected=$ExpectedValidatorGitBlob observed=$baseValidatorBlob" }
    $executedBlob = Get-GitLine $repo @('hash-object','--no-filters',[System.IO.Path]::GetFullPath($PSCommandPath))
    if ($executedBlob -cne $ExpectedValidatorGitBlob) { throw "executed validator bytes are not the trusted base blob: expected=$ExpectedValidatorGitBlob observed=$executedBlob" }

    $tempRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('engram-data-only-authority-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tempRoot -Force | Out-Null
    $basePolicyFile = Join-Path $tempRoot 'base-policy.json'
    $basePolicyBlob = Get-GitLine $repo @('rev-parse',"$BaseSha`:$PolicyPath")
    $baseFacts = Read-PolicyBlob $repo "$BaseSha`:$PolicyPath" $basePolicyFile 'trusted base policy'
    $materializedPolicyBlob = Get-GitLine $repo @('hash-object','--no-filters',$basePolicyFile)
    if ($materializedPolicyBlob -cne $basePolicyBlob) { throw 'materialized policy bytes differ from trusted base Git blob' }
    if ([string]$baseFacts.policy.default_branch -cne $ExpectedDefaultBranch) { throw 'trusted policy default branch differs from the workflow contract' }
    if ([string]$baseFacts.policy.repository -cne $EventRepositoryFullName) { throw "event repository '$EventRepositoryFullName' differs from trusted policy repository '$([string]$baseFacts.policy.repository)'" }

    $ancestor = Invoke-Git $repo @('merge-base','--is-ancestor',$BaseSha,$HeadSha) -AllowFailure
    if ($ancestor.exit_code -ne 0) { throw 'PR head is not descended from the exact trusted default-branch base' }
    $merge = Invoke-Git $repo @('merge-tree','--write-tree',$BaseSha,$HeadSha) -AllowFailure
    if ($merge.exit_code -ne 0 -or [string]$merge.output[-1] -notmatch '^[0-9a-f]{40}$') { throw "merge-tree failed or conflicted: $($merge.output -join ' ')" }
    $mergeTree = [string]$merge.output[-1]
    $headTree = Get-GitLine $repo @('rev-parse',"$HeadSha`^{tree}")
    if ($mergeTree -cne $headTree) { throw "merge-tree '$mergeTree' differs from exact PR head tree '$headTree'" }

    [object[]]$diffEntries = @(Get-DiffEntries $repo $BaseSha $HeadSha)
    [object[]]$protected = @($diffEntries | Where-Object { Test-ProtectedPath $baseFacts.policy ([string]$_.path) })
    $maintenance = $EventAction -ceq 'labeled' -and $EventLabel -ceq [string]$baseFacts.policy.active_epoch.label
    $nextEpoch = $null

    if ($protected.Count -eq 0) {
        if ($maintenance) { throw 'maintenance approval label was applied to a PR with no protected authority changes' }
        $mode = 'ordinary'
    }
    else {
        $mode = 'maintenance'
        if (-not $maintenance) { throw "protected authority paths require exact labeled event '$([string]$baseFacts.policy.active_epoch.label)'" }
        if ($EventHeadRepositoryFullName -cne $EventRepositoryFullName) { throw 'authority maintenance PR head must come from the same repository as the trusted base' }
        if ($ActorLogin -cne [string]$baseFacts.policy.trusted_actor.login -or $ActorId -ne [int64]$baseFacts.policy.trusted_actor.id -or $ActorType -cne [string]$baseFacts.policy.trusted_actor.type) {
            throw 'maintenance event sender is not the trusted base actor'
        }
        if ($protected.Count -ne $diffEntries.Count) { throw 'authority maintenance PR must not mix protected control-plane and ordinary product changes' }
        if ((Get-ChangeSignature $diffEntries) -cne (Get-ChangeSignature $baseFacts.changes)) { throw 'actual maintenance diff does not exactly equal the trusted base epoch status/path set' }

        [object[]]$actualBlobs = @($diffEntries | Where-Object { [string]$_.path -cne $PolicyPath } | ForEach-Object { [pscustomobject][ordered]@{ path=[string]$_.path; git_blob=[string]$_.head_blob } })
        if ((Get-BlobSignature $actualBlobs) -cne (Get-BlobSignature $baseFacts.expected_blobs)) { throw 'actual maintenance head blobs differ from the trusted base epoch preapproval' }

        $headPolicyFile = Join-Path $tempRoot 'head-policy.json'
        $headFacts = Read-PolicyBlob $repo "$HeadSha`:$PolicyPath" $headPolicyFile 'untrusted head policy data'
        foreach ($field in @('repository','default_branch','protected_paths','trusted_actor')) {
            if ((Get-CanonicalJsonText $headFacts.policy.$field) -cne (Get-CanonicalJsonText $baseFacts.policy.$field)) { throw "head policy $field differs from trusted base authority" }
        }
        if ([string]$headFacts.policy.transition.consumed_epoch -cne [string]$baseFacts.policy.active_epoch.id) { throw 'head transition does not consume the exact trusted base epoch' }
        if ([string]$headFacts.policy.transition.event_base_sha -cne $BaseSha) { throw 'head transition event_base_sha differs from the actual trusted default-branch base' }
        if ($headFacts.epoch_number -ne $baseFacts.epoch_number + 1) { throw 'head active epoch is not the exact monotonic successor' }
        $nextEpoch = [string]$headFacts.policy.active_epoch.id
    }

    $finishedAt = [DateTimeOffset]::UtcNow
    $artifactObject = [ordered]@{
        schema_version = 1
        gate = 'default-branch-data-only-authority'
        verdict = 'PASS'
        mode = $mode
        started_at = $startedAt.ToString('O')
        finished_at = $finishedAt.ToString('O')
        event = [ordered]@{ action=$EventAction; label=$EventLabel; repository=$EventRepositoryFullName; base_sha=$BaseSha; head_sha=$HeadSha }
        trusted_execution = [ordered]@{ source='default-branch-git-objects'; validator_path=$ValidatorPath; validator_git_blob=$baseValidatorBlob; policy_path=$PolicyPath; policy_git_blob=$basePolicyBlob; secrets_used=$false }
        head = [ordered]@{ tree=$headTree; treated_as_data_only=$true; executed=$false; checked_out=$false }
        topology = [ordered]@{ fetched_base_sha=$fetchedBase; fetched_head_sha=$fetchedHead; base_is_ancestor=$true; merge_tree=$mergeTree }
        changed_paths = @($diffEntries)
        protected_path_count = $protected.Count
        maintenance = [ordered]@{ consumed_epoch=$(if ($mode -ceq 'maintenance') { [string]$baseFacts.policy.active_epoch.id } else { $null }); next_epoch=$nextEpoch }
        errors = @()
    }
    $exitCode = 0
}
catch {
    $finishedAt = [DateTimeOffset]::UtcNow
    $artifactObject = [ordered]@{
        schema_version = 1
        gate = 'default-branch-data-only-authority'
        verdict = 'FAIL'
        started_at = $startedAt.ToString('O')
        finished_at = $finishedAt.ToString('O')
        head = [ordered]@{ treated_as_data_only=$true; executed=$false; checked_out=$false }
        trusted_execution = [ordered]@{ source='default-branch-git-objects'; secrets_used=$false }
        errors = @($_.Exception.Message)
    }
    $exitCode = 1
}
finally {
    if ($null -ne $tempRoot -and (Test-Path -LiteralPath $tempRoot)) {
        $tempPrefix = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath()).TrimEnd('\','/') + [System.IO.Path]::DirectorySeparatorChar
        $resolved = [System.IO.Path]::GetFullPath($tempRoot)
        if (-not $resolved.StartsWith($tempPrefix, [System.StringComparison]::OrdinalIgnoreCase) -or -not ([System.IO.Path]::GetFileName($resolved)).StartsWith('engram-data-only-authority-', [System.StringComparison]::Ordinal)) {
            throw "refusing unsafe temporary cleanup '$resolved'"
        }
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
}

Write-Utf8NoBom -Path $Artifact -Text (($artifactObject | ConvertTo-Json -Depth 100) + "`n")
Write-Output "default-branch-data-only-authority verdict=$($artifactObject.verdict) artifact=$Artifact"
exit $exitCode
