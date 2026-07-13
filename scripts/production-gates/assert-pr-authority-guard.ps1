
[CmdletBinding(DefaultParameterSetName='Run')]
param(
    [Parameter(Mandatory, ParameterSetName='Run')][string]$Repository,
    [Parameter(Mandatory, ParameterSetName='Run')][string]$Remote,
    [Parameter(Mandatory, ParameterSetName='Run')][string]$BaseRemoteRef,
    [Parameter(Mandatory, ParameterSetName='Run')][ValidatePattern('^[0-9a-f]{40}$')][string]$BaseSha,
    [Parameter(Mandatory, ParameterSetName='Run')][string]$HeadRemoteRef,
    [Parameter(Mandatory, ParameterSetName='Run')][ValidatePattern('^[0-9a-f]{40}$')][string]$HeadSha,
    [string]$ExpectedDefaultBranch = 'main',
    [string]$ValidatorPath = 'scripts/production-gates/assert-pr-authority-guard.ps1',
    [Parameter(Mandatory, ParameterSetName='Run')][ValidatePattern('^[0-9a-f]{40}$')][string]$ExpectedValidatorGitBlob,
    [Parameter(Mandatory, ParameterSetName='SelfTest')][switch]$SelfTest,
    [string]$Artifact = '.agent/e/rg17/pr-authority-guard.json'
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
    $start.FileName = 'git'
    $start.UseShellExecute = $false
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    [void]$start.ArgumentList.Add('-C'); [void]$start.ArgumentList.Add($WorkingTree)
    [void]$start.ArgumentList.Add('cat-file'); [void]$start.ArgumentList.Add('blob'); [void]$start.ArgumentList.Add($Spec)
    $process = [System.Diagnostics.Process]::new(); $process.StartInfo = $start
    if (-not $process.Start()) { throw "could not start git cat-file for '$Spec'" }
    $stream = [System.IO.File]::Open($Destination, [System.IO.FileMode]::Create, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
    try { $process.StandardOutput.BaseStream.CopyTo($stream) } finally { $stream.Dispose() }
    $stderr = $process.StandardError.ReadToEnd(); $process.WaitForExit()
    if ($process.ExitCode -ne 0) { throw "git cat-file failed for '$Spec': $stderr" }
}

function Get-CanonicalTextSha256 {
    param([Parameter(Mandatory)][string]$Path)
    $text = [System.IO.File]::ReadAllText($Path)
    $canonical = ($text -replace "`r`n", "`n") -replace "`r", "`n"
    return [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData([System.Text.UTF8Encoding]::new($false).GetBytes($canonical))).ToLowerInvariant()
}

function Assert-MergeTreeMatchesHead {
    param(
        [Parameter(Mandatory)][string]$MergeTree,
        [Parameter(Mandatory)][string]$HeadTree
    )
    if ($MergeTree -cne $HeadTree) { throw "merge-tree result '$MergeTree' does not equal exact PR head tree '$HeadTree'" }
}

function Assert-AuthoritySafeHeadObject {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Mode,
        [Parameter(Mandatory)][string]$DeclaredType,
        [Parameter(Mandatory)][string]$ActualType
    )
    if ($Mode -cnotin @('100644','100755')) { throw "changed PR path '$Path' has forbidden head mode '$Mode'; only regular-file modes 100644 and 100755 are authority-safe" }
    if ($DeclaredType -cne 'blob') { throw "changed PR path '$Path' has forbidden head object type '$DeclaredType'; only blobs are authority-safe" }
    if ($ActualType -cne 'blob') { throw "changed PR path '$Path' head object is '$ActualType', not an available blob" }
}

function Get-DiffEntries {
    param([Parameter(Mandatory)][string]$WorkingTree, [Parameter(Mandatory)][string]$Base, [Parameter(Mandatory)][string]$Head)
    $result = Invoke-Git $WorkingTree @('-c','core.quotepath=false','diff','--name-status','--find-renames','--find-copies',"$Base..$Head",'--')
    $entries = [System.Collections.Generic.List[object]]::new()
    foreach ($line in $result.output) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        $parts = $line -split "`t"
        if ($parts.Count -ne 2) { throw "unsupported or malformed PR diff line '$line'; renames/copies are not authority-safe" }
        if ($parts[0] -ceq 'D') { throw "deleted PR path '$($parts[1])' is not authority-safe" }
        if ($parts[0] -notmatch '^[AM]$') { throw "PR diff status '$($parts[0])' for '$($parts[1])' is not authority-safe; only literal add/modify entries are allowed" }
        $path = [string]$parts[1]
        if ($path.Contains('\') -or $path.StartsWith('/') -or $path -match '(^|/)(\.|\.\.)(/|$)') { throw "non-canonical PR diff path '$path'" }
        $headEntry = Invoke-Git $WorkingTree @('-c','core.quotepath=false','ls-tree','--full-tree',$Head,'--',":(literal)$path")
        if ($headEntry.output.Count -ne 1) { throw "changed PR path '$path' does not resolve to exactly one head tree entry" }
        $treeLine = [string]$headEntry.output[0]
        if ($treeLine -notmatch '^(?<mode>[0-9]{6}) (?<type>[^ ]+) (?<object>[0-9a-f]{40,64})\t(?<path>.+)$') {
            throw "changed PR path '$path' has malformed head tree metadata '$treeLine'"
        }
        $headMode = [string]$Matches.mode
        $headType = [string]$Matches.type
        $headObject = [string]$Matches.object
        $headPath = [string]$Matches.path
        if ($headPath -cne $path) { throw "changed PR path '$path' resolved to unexpected head tree path '$headPath'" }

        $objectType = Invoke-Git $WorkingTree @('cat-file','-t',$headObject) -AllowFailure
        $actualType = if ($objectType.exit_code -eq 0 -and $objectType.output.Count -eq 1) { [string]$objectType.output[0] } else { 'unavailable' }
        Assert-AuthoritySafeHeadObject -Path $path -Mode $headMode -DeclaredType $headType -ActualType $actualType
        $entries.Add([pscustomobject][ordered]@{
            git_status = [string]$parts[0]
            path = $path
            head_mode = $headMode
            head_type = $headType
            head_object = $headObject
        })
    }
    if ($entries.Count -eq 0) { throw 'PR diff contains zero paths' }
    return @($entries)
}

function Test-ProtectedPath {
    param([Parameter(Mandatory)][string]$Path)
    if ($Path.StartsWith('.github/workflows/', [System.StringComparison]::Ordinal)) { return $true }
    if ($Path.StartsWith('scripts/production-gates/', [System.StringComparison]::Ordinal)) { return $true }
    if ($Path.StartsWith('.agent/plans/', [System.StringComparison]::Ordinal)) { return $true }
    if ($Path.StartsWith('.agent/specs/release-gates-r12/evidence/plan-governance/', [System.StringComparison]::Ordinal)) { return $true }
    return $false
}

function Get-OrdinalSignature {
    param([Parameter(Mandatory)][AllowEmptyCollection()][object[]]$Entries, [switch]$PathsOnly)
    [string[]]$values = if ($PathsOnly) { @($Entries | ForEach-Object { [string]$_.path }) } else { @($Entries | ForEach-Object { "$([string]$_.git_status)`t$([string]$_.path)" }) }
    [Array]::Sort($values, [System.StringComparer]::Ordinal)
    return ($values -join "`n")
}

function Get-OptionalArrayProperty {
    param([Parameter(Mandatory)]$Object, [Parameter(Mandatory)][string]$Name)
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) { return @() }
    return @($property.Value)
}

function Find-Authorization {
    param([Parameter(Mandatory)]$Contract, [Parameter(Mandatory)][object[]]$DiffEntries)
    $actualStatus = Get-OrdinalSignature $DiffEntries
    $actualPaths = Get-OrdinalSignature $DiffEntries -PathsOnly
    [string[]]$current = @($Contract.status_classes.current)
    foreach ($candidate in @($Contract.candidates)) {
        if ([string]$candidate.status_class -cnotin $current) { continue }
        $candidateStatus = Get-OrdinalSignature @($candidate.paths)
        if ($candidateStatus -ceq $actualStatus) { return [pscustomobject][ordered]@{ kind='current-candidate'; slice=[string]$candidate.slice; status_class=[string]$candidate.status_class } }
    }
    [string[]]$pendingStatuses = @($Contract.status_classes.pending)
    foreach ($pending in @($Contract.pending_namespaces)) {
        if ([int]$Contract.revision -ne 12 -and [string]$pending.status_class -cnotin $pendingStatuses) { continue }
        if ($pending.PSObject.Properties['release_accepted'] -and [bool]$pending.release_accepted) { continue }
        [string[]]$forbidden = @(Get-OptionalArrayProperty $pending 'forbidden_final_paths')
        $containsForbidden = @($DiffEntries | Where-Object { [string]$_.path -cin $forbidden }).Count -gt 0
        if ($containsForbidden) { continue }
        [string[]]$exact = @(Get-OptionalArrayProperty $pending 'exact_paths')
        if ($exact.Count -gt 0) {
            [object[]]$expectedEntries = @($exact | ForEach-Object { [pscustomobject]@{path=[string]$_} })
            if ((Get-OrdinalSignature $expectedEntries -PathsOnly) -ceq $actualPaths) { return [pscustomobject][ordered]@{ kind='pending-exact-envelope'; slice=[string]$pending.slice; status_class=[string]$pending.status_class } }
        }
        [string[]]$prefixes = @(Get-OptionalArrayProperty $pending 'exact_prefixes')
        if ($prefixes.Count -gt 0) {
            $allCovered = $true
            foreach ($entry in $DiffEntries) {
                if (@($prefixes | Where-Object { ([string]$entry.path).StartsWith(([string]$_).Substring(0, ([string]$_).Length - 2), [System.StringComparison]::Ordinal) }).Count -eq 0) { $allCovered = $false; break }
            }
            if ($allCovered) { return [pscustomobject][ordered]@{ kind='pending-prefix-envelope'; slice=[string]$pending.slice; status_class=[string]$pending.status_class } }
        }
    }
    return $null
}

function Invoke-AuthorizationSelfTest {
    $contract = [pscustomobject][ordered]@{
        revision = 12
        status_classes = [pscustomobject]@{ current=@(); pending=@() }
        candidates = @()
        pending_namespaces = @([pscustomobject][ordered]@{
            slice='SELFTEST'; status_class='current-maker-in-progress'; release_accepted=$false
            exact_paths=@('src/exact.go'); exact_prefixes=@('src/generated/**'); forbidden_final_paths=@('src/exact.go','src/generated/secret.go')
        })
    }
    $exactForbidden = Find-Authorization $contract @([pscustomobject]@{path='src/exact.go';git_status='M'})
    if ($null -ne $exactForbidden) { throw 'SELFTEST FAIL: exact forbidden_final_paths entry was authorized' }
    $prefixForbidden = Find-Authorization $contract @([pscustomobject]@{path='src/generated/secret.go';git_status='A'})
    if ($null -ne $prefixForbidden) { throw 'SELFTEST FAIL: prefix-covered forbidden_final_paths entry was authorized' }
    $prefixAllowed = Find-Authorization $contract @([pscustomobject]@{path='src/generated/public.go';git_status='A'})
    if ($null -eq $prefixAllowed -or [string]$prefixAllowed.kind -cne 'pending-prefix-envelope') { throw 'SELFTEST FAIL: allowed prefix path was rejected' }
    Write-Output 'SELFTEST PASS: ordinary authorization rejects exact and prefix-covered forbidden_final_paths entries'
}

if ($SelfTest) { Invoke-AuthorizationSelfTest; exit 0 }

$startedAt = [DateTimeOffset]::UtcNow
$artifactObject = $null
$exitCode = 1
$trustedRoot = $null
try {
    $repo = [System.IO.Path]::GetFullPath($Repository)
    if (-not (Test-Path -LiteralPath $repo -PathType Container)) { throw "repository does not exist: $repo" }
    if ($ExpectedDefaultBranch -notmatch '^[A-Za-z0-9._/-]+$') { throw 'expected default branch has invalid syntax' }
    $requiredBaseRef = "refs/heads/$ExpectedDefaultBranch"
    if ($BaseRemoteRef -cne $requiredBaseRef) { throw "base ref '$BaseRemoteRef' is not trusted default-branch ref '$requiredBaseRef'" }
    if ($HeadRemoteRef -notmatch '^refs/pull/[1-9][0-9]*/head$') { throw "head ref '$HeadRemoteRef' is not an explicit pull-request head ref" }
    if ($ValidatorPath -cne 'scripts/production-gates/assert-pr-authority-guard.ps1') { throw 'validator path drifted from the protected trusted-base path' }

    [void](Invoke-Git $repo @('rev-parse','--is-inside-work-tree'))
    [void](Invoke-Git $repo @('fetch','--no-tags','--force',$Remote,"+$BaseRemoteRef`:refs/authority/base"))
    [void](Invoke-Git $repo @('fetch','--no-tags','--force',$Remote,"+$HeadRemoteRef`:refs/authority/head"))
    $fetchedBase = [string](Invoke-Git $repo @('rev-parse','refs/authority/base^{commit}')).output[-1]
    $fetchedHead = [string](Invoke-Git $repo @('rev-parse','refs/authority/head^{commit}')).output[-1]
    if ($fetchedBase -cne $BaseSha) { throw "fetched base SHA mismatch: expected=$BaseSha observed=$fetchedBase" }
    if ($fetchedHead -cne $HeadSha) { throw "fetched head SHA mismatch: expected=$HeadSha observed=$fetchedHead" }

    $baseValidatorBlob = [string](Invoke-Git $repo @('rev-parse',"$BaseSha`:$ValidatorPath")).output[-1]
    if ($baseValidatorBlob -cne $ExpectedValidatorGitBlob) { throw "trusted validator blob mismatch: expected=$ExpectedValidatorGitBlob observed=$baseValidatorBlob" }
    $executedBlob = [string](Invoke-Git $repo @('hash-object','--no-filters',[System.IO.Path]::GetFullPath($PSCommandPath))).output[-1]
    if ($executedBlob -cne $ExpectedValidatorGitBlob) { throw "executed validator bytes are not the trusted base blob: expected=$ExpectedValidatorGitBlob observed=$executedBlob" }

    $ancestor = Invoke-Git $repo @('merge-base','--is-ancestor',$BaseSha,$HeadSha) -AllowFailure
    if ($ancestor.exit_code -ne 0) { throw 'PR head is not based on the exact trusted base; rebase is required' }
    $merge = Invoke-Git $repo @('merge-tree','--write-tree',$BaseSha,$HeadSha) -AllowFailure
    if ($merge.exit_code -ne 0 -or [string]$merge.output[-1] -notmatch '^[0-9a-f]{40}$') { throw "merge-tree failed or conflicted: $($merge.output -join ' ')" }
    $mergeTree = [string]$merge.output[-1]
    $headTree = [string](Invoke-Git $repo @('rev-parse',"$HeadSha`^{tree}")).output[-1]
    Assert-MergeTreeMatchesHead -MergeTree $mergeTree -HeadTree $headTree

    $treeInventory = @(Invoke-Git $repo @('-c','core.quotepath=false','ls-tree','-r','--name-only',$HeadSha)).output
    $diffEntries = @(Get-DiffEntries $repo $BaseSha $HeadSha)
    $protected = @($diffEntries | Where-Object { Test-ProtectedPath ([string]$_.path) })
    if ($protected.Count -gt 0) { throw "protected authority/gate/governance mutation rejected: $(@($protected.path) -join ', ')" }

    $trustedRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('engram-authority-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $trustedRoot -Force | Out-Null
    $activePath = 'scripts/production-gates/assert-active-candidate-path-authority.ps1'
    $maintenanceValidatorPath = 'scripts/production-gates/assert-pr-authority-maintenance.ps1'
    $contractPath = '.agent/plans/2026-07-10-engram-production-ready-active-diff-contracts.json'
    $planPath = '.agent/plans/2026-07-10-engram-production-ready-master-plan.md'
    $statePath = '.agent/plans/2026-07-10-engram-production-ready-ownership-state.json'
    $scopeMapPath = '.agent/plans/2026-07-10-engram-production-ready-scope-map.json'
    $planGovernancePath = '.agent/specs/release-gates-r12/evidence/plan-governance/test-r12-plan-governance.ps1'
    $pathEnvelopePath = '.agent/specs/release-gates-r12/evidence/plan-governance/path-envelope.json'
    $fixedPointPath = '.agent/specs/release-gates-r12/evidence/plan-governance/fixed-point-proof.json'
    $authoritySnapshotPath = '.agent/specs/release-gates-r12/evidence/plan-governance/authority-snapshot.json'
    $activeFile = Join-Path $trustedRoot 'assert-active-candidate-path-authority.ps1'
    $maintenanceValidatorFile = Join-Path $trustedRoot 'assert-pr-authority-maintenance.ps1'
    $contractFile = Join-Path $trustedRoot 'active-diff-contracts.json'
    $planFile = Join-Path $trustedRoot 'master-plan.md'
    $stateFile = Join-Path $trustedRoot 'ownership-state.json'
    $scopeMapFile = Join-Path $trustedRoot 'scope-map.json'
    $planGovernanceFile = Join-Path $trustedRoot 'test-r12-plan-governance.ps1'
    $pathEnvelopeFile = Join-Path $trustedRoot 'path-envelope.json'
    $fixedPointFile = Join-Path $trustedRoot 'fixed-point-proof.json'
    $authoritySnapshotFile = Join-Path $trustedRoot 'authority-snapshot.json'
    Export-GitBlob $repo "$BaseSha`:$activePath" $activeFile
    Export-GitBlob $repo "$BaseSha`:$maintenanceValidatorPath" $maintenanceValidatorFile
    Export-GitBlob $repo "$BaseSha`:$contractPath" $contractFile
    Export-GitBlob $repo "$BaseSha`:$planPath" $planFile
    Export-GitBlob $repo "$BaseSha`:$statePath" $stateFile
    Export-GitBlob $repo "$BaseSha`:$scopeMapPath" $scopeMapFile
    Export-GitBlob $repo "$BaseSha`:$planGovernancePath" $planGovernanceFile
    Export-GitBlob $repo "$BaseSha`:$pathEnvelopePath" $pathEnvelopeFile
    Export-GitBlob $repo "$BaseSha`:$fixedPointPath" $fixedPointFile
    Export-GitBlob $repo "$BaseSha`:$authoritySnapshotPath" $authoritySnapshotFile
    $activeBlob = [string](Invoke-Git $repo @('rev-parse',"$BaseSha`:$activePath")).output[-1]
    $activeExportBlob = [string](Invoke-Git $repo @('hash-object','--no-filters',$activeFile)).output[-1]
    if ($activeBlob -cne $activeExportBlob) { throw 'active authority validator export does not match trusted base bytes' }
    $maintenanceValidatorBlob = [string](Invoke-Git $repo @('rev-parse',"$BaseSha`:$maintenanceValidatorPath")).output[-1]
    $maintenanceValidatorExportBlob = [string](Invoke-Git $repo @('hash-object','--no-filters',$maintenanceValidatorFile)).output[-1]
    if ($maintenanceValidatorBlob -cne $maintenanceValidatorExportBlob) { throw 'maintenance base validator export does not match trusted base bytes' }
    $activeArtifact = Join-Path $trustedRoot 'active-authority.json'
    $contractHash = Get-CanonicalTextSha256 $contractFile
    $planHash = Get-CanonicalTextSha256 $planFile
    $stateHash = Get-CanonicalTextSha256 $stateFile
    $scopeMapHash = Get-CanonicalTextSha256 $scopeMapFile
    $maintenanceBaseArtifact = Join-Path $trustedRoot 'maintenance-base-authority.json'
    & pwsh -NoProfile -File $maintenanceValidatorFile -ValidateBaseOnly -Repository $repo -Remote $Remote -BaseRemoteRef $BaseRemoteRef -BaseSha $BaseSha -ExpectedDefaultBranch $ExpectedDefaultBranch -ExpectedValidatorGitBlob $maintenanceValidatorBlob -Artifact $maintenanceBaseArtifact
    if ($LASTEXITCODE -ne 0) {
        $detail = if (Test-Path -LiteralPath $maintenanceBaseArtifact) { @((Get-Content -LiteralPath $maintenanceBaseArtifact -Raw | ConvertFrom-Json -Depth 100).errors) -join '; ' } else { 'artifact missing' }
        throw "trusted-base inductive maintenance authority validation failed: $detail"
    }
    $maintenanceBaseResult = Get-Content -LiteralPath $maintenanceBaseArtifact -Raw | ConvertFrom-Json -Depth 100
    if ([string]$maintenanceBaseResult.verdict -cne 'PASS') { throw 'trusted-base inductive maintenance authority artifact is not PASS' }
    & pwsh -NoProfile -File $activeFile -Contract $contractFile -ExpectedContractSha256 $contractHash -Plan $planFile -ExpectedPlanSha256 $planHash -Artifact $activeArtifact
    if ($LASTEXITCODE -ne 0) { throw 'trusted-base active candidate authority validator failed' }
    $activeResult = Get-Content -LiteralPath $activeArtifact -Raw | ConvertFrom-Json -Depth 100
    if ([string]$activeResult.verdict -cne 'PASS') { throw 'trusted-base active candidate authority artifact is not PASS' }
    $contract = Get-Content -LiteralPath $contractFile -Raw | ConvertFrom-Json -Depth 100
    $authorization = Find-Authorization $contract $diffEntries
    if ($null -eq $authorization) { throw 'PR diff does not exactly match a trusted-base current candidate or pending authority envelope' }

    $finishedAt = [DateTimeOffset]::UtcNow
    $artifactObject = [ordered]@{
        schema_version = 1; gate = 'pr-authority-guard'; verdict = 'PASS'; started_at = $startedAt.ToString('O'); finished_at = $finishedAt.ToString('O'); duration_seconds = [math]::Round(($finishedAt-$startedAt).TotalSeconds,3)
        base = [ordered]@{ remote_ref=$BaseRemoteRef; expected_sha=$BaseSha; fetched_sha=$fetchedBase }
        head = [ordered]@{ remote_ref=$HeadRemoteRef; expected_sha=$HeadSha; fetched_sha=$fetchedHead; tree=$headTree; inventory_count=$treeInventory.Count; treated_as_data_only=$true; executed=$false; checked_out=$false }
        trusted_execution = [ordered]@{ validator_path=$ValidatorPath; expected_git_blob=$ExpectedValidatorGitBlob; executed_git_blob=$executedBlob; active_validator_git_blob=$activeBlob; maintenance_validator_path=$maintenanceValidatorPath; maintenance_validator_git_blob=$maintenanceValidatorBlob; inductive_base_artifact_verdict=[string]$maintenanceBaseResult.verdict; plan_governance_validator_path=$planGovernancePath; contract_sha256=$contractHash; plan_sha256=$planHash; state_sha256=$stateHash; scope_map_sha256=$scopeMapHash; secrets_used=$false }
        merge_tree = $mergeTree
        changed_paths = @($diffEntries)
        protected_path_count = 0
        authorization = $authorization
        errors = @()
    }
    $exitCode = 0
}
catch {
    $finishedAt = [DateTimeOffset]::UtcNow
    $artifactObject = [ordered]@{ schema_version=1; gate='pr-authority-guard'; verdict='FAIL'; started_at=$startedAt.ToString('O'); finished_at=$finishedAt.ToString('O'); head=[ordered]@{treated_as_data_only=$true;executed=$false;checked_out=$false}; trusted_execution=[ordered]@{secrets_used=$false}; errors=@($_.Exception.Message) }
    $exitCode = 1
}
finally {
    if ($null -ne $trustedRoot -and (Test-Path -LiteralPath $trustedRoot)) {
        $resolvedTemp = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath()).TrimEnd('\','/') + [System.IO.Path]::DirectorySeparatorChar
        $resolvedTarget = [System.IO.Path]::GetFullPath($trustedRoot)
        if (-not $resolvedTarget.StartsWith($resolvedTemp, [System.StringComparison]::OrdinalIgnoreCase) -or -not ([System.IO.Path]::GetFileName($resolvedTarget)).StartsWith('engram-authority-', [System.StringComparison]::Ordinal)) { throw "refusing unsafe temp cleanup '$resolvedTarget'" }
        Remove-Item -LiteralPath $resolvedTarget -Recurse -Force
    }
}

Write-Utf8NoBom -Path $Artifact -Text (($artifactObject | ConvertTo-Json -Depth 100) + "`n")
Write-Output "pr-authority-guard verdict=$($artifactObject.verdict) artifact=$Artifact"
exit $exitCode
