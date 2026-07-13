[CmdletBinding()]
param(
    [ValidateSet('BuildAndScan', 'ValidateRelease', 'ValidateWorkflowRun', 'ValidateArtifactMetadata', 'ValidatePayload', 'LoadPayload', 'ValidatePublicationEvidence', 'PlanPublication', 'Publish')]
    [string]$Mode = 'BuildAndScan',

    [string]$ServerTag,
    [string]$OperatorTag,
    [string]$PostgresTag,
    [ValidateSet('linux/amd64')]
    [string]$Platform = 'linux/amd64',
    [string]$ArtifactRoot,
    [switch]$NoAllowlist,
    [string]$Version,
    [string]$RepositoryRoot,
    [string]$TrustedOutputRoot,
    [string]$ReleasePayloadPath,

    [string]$ReleaseRef,
    [string]$ExpectedSha,
    [string]$ActualSha,
    [string]$RulesetFixturePath,
    [string]$GitHubToken,
    [string]$GitHubApiUrl = 'https://api.github.com',
    [string]$GitHubOutputPath,
    [string]$WorkflowRunEventPath,
    [string]$WorkflowRunFixturePath,
    [string]$ExpectedDefaultBranch = 'main',
    [string]$TrustedWorkflowPath = '.github/workflows/docker.yaml',
    [string]$ExpectedWorkflowName = 'Docker',
    [switch]$EventOnlyValidation,
    [string]$ArtifactListFixturePath,
    [long]$ExpectedArtifactID,
    [string]$ExpectedArtifactName,
    [string]$ExpectedArtifactDigest,
    [long]$CurrentRunID,
    [string]$PayloadRoot,
    [string]$EvidenceRoot,

    [string]$ManifestPath,
    [string]$ReleaseVersion,
    [string]$RegistryFixturePath,
    [string]$OutputPath,
    [string]$Registry = 'ghcr.io',
    [string]$Repository = 'thebtf/engram'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'compose-secret-access.ps1')

$defaultRepositoryRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..'))
$repoRoot = if ([string]::IsNullOrWhiteSpace($RepositoryRoot)) {
    $defaultRepositoryRoot
} else {
    [IO.Path]::GetFullPath($RepositoryRoot)
}
if (-not (Test-Path -LiteralPath $repoRoot -PathType Container)) {
    throw "RepositoryRoot does not exist: $repoRoot"
}

function Resolve-RepositoryPath {
    param([Parameter(Mandatory = $true)][string]$Path)

    if ([IO.Path]::IsPathRooted($Path)) {
        return [IO.Path]::GetFullPath($Path)
    }
    return [IO.Path]::GetFullPath((Join-Path $repoRoot $Path))
}

function Assert-PathHasNoLinkComponents {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$TrustRoot,
        [switch]$LeafMayNotExist
    )

    $root = [IO.Path]::GetFullPath($TrustRoot).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
    $candidate = [IO.Path]::GetFullPath($Path)
    $prefix = $root + [IO.Path]::DirectorySeparatorChar
    if ($candidate -ne $root -and -not $candidate.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Trusted output path escapes its trust root: $candidate"
    }
    if (-not (Test-Path -LiteralPath $root -PathType Container)) {
        throw "Trusted output root must already exist as a runner-owned directory: $root"
    }

    $relative = [IO.Path]::GetRelativePath($root, $candidate)
    $components = if ($relative -eq '.') { @() } else { @($relative -split '[\\/]') }
    $current = $root
    foreach ($component in @('.') + $components) {
        if ($component -ne '.') {
            $current = Join-Path $current $component
        }
        if (-not (Test-Path -LiteralPath $current)) {
            if ($LeafMayNotExist) { break }
            throw "Trusted output path does not exist: $current"
        }
        $item = Get-Item -Force -LiteralPath $current
        $isReparsePoint = ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0
        $linkType = if ($item.PSObject.Properties.Name -contains 'LinkType') { [string]$item.LinkType } else { '' }
        if ($isReparsePoint -or -not [string]::IsNullOrWhiteSpace($linkType)) {
            throw "Trusted output path contains a symlink/reparse component: $current"
        }
    }
    return $candidate
}

function Assert-TrustedOutputRoot {
    if ([string]::IsNullOrWhiteSpace($TrustedOutputRoot)) {
        throw 'TrustedOutputRoot is required for privileged publication inputs and outputs.'
    }
    $root = Assert-PathHasNoLinkComponents -Path $TrustedOutputRoot -TrustRoot $TrustedOutputRoot
    if (-not [string]::IsNullOrWhiteSpace($env:RUNNER_TEMP)) {
        $runnerTemp = [IO.Path]::GetFullPath($env:RUNNER_TEMP).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
        if ($root -ne $runnerTemp) {
            throw "TrustedOutputRoot must equal the runner-owned RUNNER_TEMP directory: $runnerTemp"
        }
    }
    return $root
}

function New-TrustedOutputDirectory {
    param([Parameter(Mandatory = $true)][string]$Path)

    $root = Assert-TrustedOutputRoot
    $fullPath = Assert-PathHasNoLinkComponents -Path $Path -TrustRoot $root -LeafMayNotExist
    if (Test-Path -LiteralPath $fullPath) {
        throw "Trusted output directory must be freshly created by this gate: $fullPath"
    }
    New-Item -ItemType Directory -Path $fullPath | Out-Null
    return Assert-PathHasNoLinkComponents -Path $fullPath -TrustRoot $root
}

function Resolve-TrustedOutputPath {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [switch]$LeafMayNotExist
    )

    $root = Assert-TrustedOutputRoot
    return Assert-PathHasNoLinkComponents -Path $Path -TrustRoot $root -LeafMayNotExist:$LeafMayNotExist
}

function Assert-TrustedOutputTree {
    param([Parameter(Mandatory = $true)][string]$Path)

    $rootPath = Resolve-TrustedOutputPath -Path $Path
    foreach ($item in @(Get-ChildItem -Force -Recurse -LiteralPath $rootPath)) {
        $isReparsePoint = ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0
        $linkType = if ($item.PSObject.Properties.Name -contains 'LinkType') { [string]$item.LinkType } else { '' }
        if ($isReparsePoint -or -not [string]::IsNullOrWhiteSpace($linkType)) {
            throw "Trusted output tree contains a symlink/reparse entry: $($item.FullName)"
        }
    }
    return $rootPath
}

function Assert-CanonicalVersion {
    param([Parameter(Mandatory = $true)][string]$Value)

    $releasePattern = '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$'
    $commitPattern = '^sha-[0-9a-f]{40}$'
    if ($Value -match $commitPattern) {
        return $Value
    }
    if ($Value -notmatch $releasePattern) {
        throw "Version is not a canonical Docker-safe release or immutable commit identity: $Value"
    }
    if ($Matches[4]) {
        foreach ($identifier in $Matches[4].Split('.')) {
            if ($identifier -match '^[0-9]+$' -and $identifier.Length -gt 1 -and $identifier.StartsWith('0', [StringComparison]::Ordinal)) {
                throw "Numeric prerelease identifiers may not contain leading zeroes: $Value"
            }
        }
    }
    return $Value
}

function Assert-CanonicalReleaseRef {
    param([Parameter(Mandatory = $true)][string]$Ref)

    if (-not $Ref.StartsWith('refs/tags/', [StringComparison]::Ordinal)) {
        throw "Release publication requires refs/tags/v*: $Ref"
    }
    $value = $Ref.Substring('refs/tags/'.Length)
    if ((Assert-CanonicalVersion -Value $value) -notmatch '^v') {
        throw "Release publication requires a canonical SemVer tag: $Ref"
    }
    return $value
}

function Assert-FullCommitSha {
    param([Parameter(Mandatory = $true)][string]$Value, [Parameter(Mandatory = $true)][string]$Name)

    if ($Value -notmatch '^[0-9a-fA-F]{40}$') {
        throw "$Name must be a full 40-hex commit SHA."
    }
    return $Value.ToLowerInvariant()
}

function Get-TagRulesets {
    if (-not [string]::IsNullOrWhiteSpace($RulesetFixturePath)) {
        if (-not (Test-Path -LiteralPath $RulesetFixturePath -PathType Leaf)) {
            throw "Ruleset fixture does not exist: $RulesetFixturePath"
        }
        return @((Get-Content -Raw -LiteralPath $RulesetFixturePath | ConvertFrom-Json -Depth 100))
    }

    if ($Repository -notmatch '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$') {
        throw "Repository must be owner/name: $Repository"
    }
    if ([string]::IsNullOrWhiteSpace($GitHubToken)) {
        throw 'GitHubToken is required for live ruleset validation.'
    }
    $headers = @{
        Accept = 'application/vnd.github+json'
        Authorization = "Bearer $GitHubToken"
        'X-GitHub-Api-Version' = '2022-11-28'
    }
    $summaries = @()
    for ($page = 1; ; $page++) {
        $uri = "$($GitHubApiUrl.TrimEnd('/'))/repos/$Repository/rulesets?per_page=100&page=$page"
        $batch = @(Invoke-RestMethod -Method Get -Uri $uri -Headers $headers)
        $summaries += $batch
        if ($batch.Count -lt 100) { break }
    }
    $details = @()
    foreach ($summary in $summaries) {
        if ($summary.target -ne 'tag' -or $summary.enforcement -ne 'active') { continue }
        $uri = "$($GitHubApiUrl.TrimEnd('/'))/repos/$Repository/rulesets/$($summary.id)"
        $details += Invoke-RestMethod -Method Get -Uri $uri -Headers $headers
    }
    return @($details)
}

function Assert-ImmutableReleaseRuleset {
    param([Parameter(Mandatory = $true)][AllowEmptyCollection()][object[]]$Rulesets)

    $matches = @()
    foreach ($ruleset in $Rulesets) {
        if ($ruleset.target -ne 'tag' -or $ruleset.enforcement -ne 'active') { continue }
        $includes = @($ruleset.conditions.ref_name.include)
        $excludes = @($ruleset.conditions.ref_name.exclude)
        if ($includes.Count -ne 1 -or $includes[0] -ne 'refs/tags/v*' -or $excludes.Count -ne 0) { continue }
        if (@($ruleset.bypass_actors).Count -ne 0) { continue }
        $types = @($ruleset.rules | ForEach-Object { [string]$_.type })
        if ($types -notcontains 'deletion' -or $types -notcontains 'non_fast_forward') { continue }
        $matches += $ruleset
    }
    if ($matches.Count -ne 1) {
        throw "Expected exactly one active, no-bypass tag ruleset for refs/tags/v* with deletion and non_fast_forward protection; found $($matches.Count)."
    }
    return $matches[0]
}

function Assert-ProtectedMainRuleset {
    param([Parameter(Mandatory = $true)][AllowEmptyCollection()][object[]]$Rulesets)

    $matches = @()
    foreach ($ruleset in $Rulesets) {
        if ($ruleset.target -ne 'branch' -or $ruleset.enforcement -ne 'active') { continue }
        $includes = @($ruleset.conditions.ref_name.include)
        $excludes = @($ruleset.conditions.ref_name.exclude)
        if ($includes.Count -ne 1 -or $includes[0] -ne "refs/heads/$ExpectedDefaultBranch" -or $excludes.Count -ne 0) { continue }
        $types = @($ruleset.rules | ForEach-Object { [string]$_.type })
        if ($types -notcontains 'deletion' -or $types -notcontains 'non_fast_forward') { continue }
        $statusRules = @($ruleset.rules | Where-Object { $_.type -eq 'required_status_checks' })
        if ($statusRules.Count -ne 1 -or -not [bool]$statusRules[0].parameters.strict_required_status_checks_policy) { continue }
        $authorityChecks = @($statusRules[0].parameters.required_status_checks | Where-Object {
            [string]$_.context -ceq 'authority-guard'
        })
        if ($authorityChecks.Count -ne 1) { continue }
        $integrationID = $authorityChecks[0].PSObject.Properties['integration_id']
        if ($null -eq $integrationID -or $integrationID.Value -isnot [int] -and $integrationID.Value -isnot [long]) { continue }
        if ([int64]$integrationID.Value -ne 15368) { continue }
        $bypassActors = @($ruleset.bypass_actors)
        if ($bypassActors.Count -ne 1) { continue }
        $recoveryActor = $bypassActors[0]
        if ($recoveryActor.actor_type -ne 'User' -or [int64]$recoveryActor.actor_id -ne 7106373 -or $recoveryActor.bypass_mode -ne 'pull_request') { continue }
        $matches += $ruleset
    }
    if ($matches.Count -ne 1) {
        throw "Expected exactly one active strict protected-main ruleset requiring authority-guard; found $($matches.Count)."
    }
    return $matches[0]
}

function Invoke-GitText {
    param([Parameter(Mandatory = $true)][string[]]$Arguments, [switch]$AllowExitOne)

    $output = @(& git -C $repoRoot @Arguments 2>&1 | ForEach-Object { $_.ToString() })
    $exitCode = $LASTEXITCODE
    if ($AllowExitOne -and $exitCode -eq 1) {
        return [ordered]@{ exit_code = 1; output = ($output -join [Environment]::NewLine).Trim() }
    }
    if ($exitCode -ne 0) {
        throw "git $($Arguments -join ' ') exited $exitCode`: $($output -join [Environment]::NewLine)"
    }
    return [ordered]@{ exit_code = 0; output = ($output -join [Environment]::NewLine).Trim() }
}

function Invoke-GitHubApi {
    param([Parameter(Mandatory = $true)][string]$Path)

    if ([string]::IsNullOrWhiteSpace($GitHubToken)) {
        throw 'GitHubToken is required for trusted workflow-run validation.'
    }
    $headers = @{
        Accept = 'application/vnd.github+json'
        Authorization = "Bearer $GitHubToken"
        'X-GitHub-Api-Version' = '2022-11-28'
    }
    $uri = "$($GitHubApiUrl.TrimEnd('/'))$Path"
    return Invoke-RestMethod -Method Get -Uri $uri -Headers $headers
}

function Get-LiveRulesetDetails {
    param([Parameter(Mandatory = $true)][ValidateSet('branch', 'tag')][string]$Target)

    $details = @()
    for ($page = 1; ; $page++) {
        $batch = @(Invoke-GitHubApi -Path "/repos/$Repository/rulesets?per_page=100&page=$page")
        foreach ($summary in $batch) {
            if ($summary.target -ne $Target -or $summary.enforcement -ne 'active') { continue }
            $details += Invoke-GitHubApi -Path "/repos/$Repository/rulesets/$($summary.id)"
        }
        if ($batch.Count -lt 100) { break }
    }
    return @($details)
}

function Get-WorkflowRunValidationInputs {
    if (-not [string]::IsNullOrWhiteSpace($WorkflowRunFixturePath)) {
        if (-not (Test-Path -LiteralPath $WorkflowRunFixturePath -PathType Leaf)) {
            throw "Workflow-run fixture does not exist: $WorkflowRunFixturePath"
        }
        return Get-Content -Raw -LiteralPath $WorkflowRunFixturePath | ConvertFrom-Json -Depth 100
    }

    if ([string]::IsNullOrWhiteSpace($WorkflowRunEventPath) -or -not (Test-Path -LiteralPath $WorkflowRunEventPath -PathType Leaf)) {
        throw "WorkflowRunEventPath is required for live workflow_run validation: $WorkflowRunEventPath"
    }
    $event = Get-Content -Raw -LiteralPath $WorkflowRunEventPath | ConvertFrom-Json -Depth 100
    if ($EventOnlyValidation) {
        return [ordered]@{
            event = $event
            api_run = $event.workflow_run
            trusted_workflow = [ordered]@{
                id = [int64]$event.workflow_run.workflow_id
                name = [string]$event.workflow_run.name
                path = [string]$event.workflow_run.path
                state = 'active'
            }
            repository = [ordered]@{ full_name = $Repository; default_branch = $ExpectedDefaultBranch }
            tag_rulesets = @(Get-LiveRulesetDetails -Target tag)
            branch_rulesets = @(Get-LiveRulesetDetails -Target branch)
            git = $null
        }
    }
    $runID = [int64]$event.workflow_run.id
    $apiRun = Invoke-GitHubApi -Path "/repos/$Repository/actions/runs/$runID"
    $workflow = Invoke-GitHubApi -Path "/repos/$Repository/actions/workflows/$($apiRun.workflow_id)"
    $repositoryInfo = Invoke-GitHubApi -Path "/repos/$Repository"
    return [ordered]@{
        event = $event
        api_run = $apiRun
        trusted_workflow = $workflow
        repository = $repositoryInfo
        tag_rulesets = @(Get-LiveRulesetDetails -Target tag)
        branch_rulesets = @(Get-LiveRulesetDetails -Target branch)
        git = $null
    }
}

function Assert-WorkflowRunFieldParity {
    param($EventRun, $ApiRun)

    foreach ($field in @('id', 'workflow_id', 'name', 'path', 'event', 'status', 'conclusion', 'head_branch', 'head_sha')) {
        $eventProperty = $EventRun.PSObject.Properties[$field]
        $apiProperty = $ApiRun.PSObject.Properties[$field]
        if ($null -eq $eventProperty -or $null -eq $apiProperty -or [string]$eventProperty.Value -cne [string]$apiProperty.Value) {
            throw "workflow_run event/API mismatch for $field."
        }
    }
    if ([string]$EventRun.head_repository.full_name -cne [string]$ApiRun.head_repository.full_name) {
        throw 'workflow_run event/API head repository mismatch.'
    }
    if ([string]$EventRun.repository.full_name -cne [string]$ApiRun.repository.full_name) {
        throw 'workflow_run event/API repository mismatch.'
    }
}

function Invoke-ValidateWorkflowRun {
    if ($Repository -notmatch '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$') {
        throw "Repository must be owner/name: $Repository"
    }
    $inputs = Get-WorkflowRunValidationInputs
    $event = $inputs.event
    $eventRun = $event.workflow_run
    $apiRun = $inputs.api_run
    if ($event.action -ne 'completed') { throw 'Publisher accepts only workflow_run/completed.' }
    Assert-WorkflowRunFieldParity -EventRun $eventRun -ApiRun $apiRun
    if ($apiRun.name -ne $ExpectedWorkflowName -or $apiRun.path -ne $TrustedWorkflowPath) {
        throw 'Triggering run is not the named unprivileged Docker verification workflow.'
    }
    if ($apiRun.event -ne 'push' -or $apiRun.status -ne 'completed' -or $apiRun.conclusion -ne 'success') {
        throw 'Triggering workflow must be a successful completed push verification.'
    }
    if ($apiRun.head_repository.full_name -ne $Repository -or $apiRun.repository.full_name -ne $Repository -or $event.repository.full_name -ne $Repository) {
        throw 'Triggering run must originate from the same repository.'
    }
    if ($inputs.repository.full_name -ne $Repository -or $inputs.repository.default_branch -ne $ExpectedDefaultBranch) {
        throw 'Repository/default-branch API identity mismatch.'
    }
    if ([int64]$apiRun.workflow_id -ne [int64]$inputs.trusted_workflow.id -or
        $inputs.trusted_workflow.path -ne $TrustedWorkflowPath -or
        $inputs.trusted_workflow.name -ne $ExpectedWorkflowName -or
        $inputs.trusted_workflow.state -ne 'active') {
        throw 'Triggering workflow ID/path/name is not the active trusted default-branch workflow.'
    }

    $version = Assert-CanonicalReleaseRef -Ref "refs/tags/$($apiRun.head_branch)"
    $commit = Assert-FullCommitSha -Value ([string]$apiRun.head_sha) -Name 'workflow_run head_sha'
    $tagRuleset = Assert-ImmutableReleaseRuleset -Rulesets @($inputs.tag_rulesets)
    $mainRuleset = Assert-ProtectedMainRuleset -Rulesets @($inputs.branch_rulesets)

    if ($null -ne $inputs.git) {
        if ([string]$inputs.git.tag_commit -ne $commit) {
            throw 'Fixture tag does not peel to workflow_run head_sha.'
        }
        if (@($inputs.git.main_ancestors) -notcontains $commit) {
            throw 'Fixture release commit is not an ancestor of protected main.'
        }
    } else {
        $origin = (Invoke-GitText -Arguments @('remote', 'get-url', 'origin')).output
        $escapedRepository = [regex]::Escape($Repository)
        if ($origin -notmatch "(?i)(?:github\.com[:/])$escapedRepository(?:\.git)?$") {
            throw "Trusted checkout origin does not match repository $Repository."
        }
        $tagRef = "refs/tags/$version"
        $guardRef = "refs/engram-release-validation/$($apiRun.id)"
        Invoke-GitText -Arguments @('fetch', '--no-tags', '--force', 'origin', "+$tagRef`:$guardRef") | Out-Null
        Invoke-GitText -Arguments @('fetch', '--no-tags', '--force', 'origin', "+refs/heads/$ExpectedDefaultBranch`:refs/remotes/origin/$ExpectedDefaultBranch") | Out-Null
        $peeled = (Invoke-GitText -Arguments @('rev-parse', '--verify', "$guardRef^{commit}")).output.ToLowerInvariant()
        if ($peeled -ne $commit) {
            throw "Protected release tag peels to $peeled, not workflow_run head_sha $commit."
        }
        $ancestry = Invoke-GitText -Arguments @('merge-base', '--is-ancestor', $commit, "refs/remotes/origin/$ExpectedDefaultBranch") -AllowExitOne
        if ($ancestry.exit_code -ne 0) {
            throw "Release commit $commit is not an ancestor of protected $ExpectedDefaultBranch."
        }
    }

    $result = [ordered]@{
        schema_version = 1
        version = $version
        commit = $commit
        triggering_run_id = [int64]$apiRun.id
        triggering_workflow_id = [int64]$apiRun.workflow_id
        tag_ruleset_id = $tagRuleset.id
        main_ruleset_id = $mainRuleset.id
        trusted_publisher_source = "default-branch:$ExpectedDefaultBranch"
        validation_level = if ($EventOnlyValidation) { 'event-git-rulesets-unprivileged' } else { 'event-api-git-rulesets-full' }
        artifacts_consumed = 0
    }
    if (-not [string]::IsNullOrWhiteSpace($OutputPath)) {
        Write-JsonFile -Value $result -Path (Resolve-RepositoryPath -Path $OutputPath)
    }
    if (-not [string]::IsNullOrWhiteSpace($GitHubOutputPath)) {
        Add-Content -Encoding utf8NoBOM -LiteralPath $GitHubOutputPath -Value "version=$version"
        Add-Content -Encoding utf8NoBOM -LiteralPath $GitHubOutputPath -Value "commit=$commit"
        Add-Content -Encoding utf8NoBOM -LiteralPath $GitHubOutputPath -Value "triggering_run_id=$($apiRun.id)"
    }
    $result | ConvertTo-Json -Depth 30
}

function Write-JsonFile {
    param([Parameter(Mandatory = $true)]$Value, [Parameter(Mandatory = $true)][string]$Path)

    if (-not [string]::IsNullOrWhiteSpace($TrustedOutputRoot)) {
        $Path = Resolve-TrustedOutputPath -Path $Path -LeafMayNotExist
    }
    $directory = Split-Path -Parent $Path
    if (-not [string]::IsNullOrWhiteSpace($directory)) {
        New-Item -ItemType Directory -Force -Path $directory | Out-Null
    }
    $Value | ConvertTo-Json -Depth 100 | Set-Content -Encoding utf8NoBOM -LiteralPath $Path
}

function Normalize-Sha256Digest {
    param([Parameter(Mandatory = $true)][string]$Value, [Parameter(Mandatory = $true)][string]$Name)

    $normalized = $Value.ToLowerInvariant()
    if ($normalized -match '^[0-9a-f]{64}$') {
        $normalized = "sha256:$normalized"
    }
    if ($normalized -notmatch '^sha256:[0-9a-f]{64}$') {
        throw "$Name must be a SHA-256 digest."
    }
    return $normalized
}

function Get-ArtifactCensus {
    if (-not [string]::IsNullOrWhiteSpace($ArtifactListFixturePath)) {
        if (-not (Test-Path -LiteralPath $ArtifactListFixturePath -PathType Leaf)) {
            throw "Artifact-list fixture does not exist: $ArtifactListFixturePath"
        }
        return Get-Content -Raw -LiteralPath $ArtifactListFixturePath | ConvertFrom-Json -Depth 100
    }
    if ($CurrentRunID -le 0) { throw 'CurrentRunID must identify the active publisher workflow run.' }
    $artifacts = @()
    $reportedTotal = $null
    for ($page = 1; ; $page++) {
        $response = Invoke-GitHubApi -Path "/repos/$Repository/actions/runs/$CurrentRunID/artifacts?per_page=100&page=$page"
        if ($null -eq $reportedTotal) { $reportedTotal = [int64]$response.total_count }
        $batch = @($response.artifacts)
        $artifacts += $batch
        if ($batch.Count -lt 100) { break }
    }
    return [ordered]@{ total_count = $reportedTotal; artifacts = @($artifacts) }
}

function Invoke-ValidateArtifactMetadata {
    if ($Repository -notmatch '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$') { throw "Repository must be owner/name: $Repository" }
    if ($ExpectedArtifactID -le 0 -or $CurrentRunID -le 0) { throw 'ExpectedArtifactID and CurrentRunID must be positive.' }
    if ($ExpectedArtifactName -notmatch '^engram-release-payload-[0-9]+-[0-9]+$') {
        throw 'ExpectedArtifactName must be the fixed publisher-run payload name.'
    }
    $expectedDigest = Normalize-Sha256Digest -Value $ExpectedArtifactDigest -Name 'ExpectedArtifactDigest'
    $census = Get-ArtifactCensus
    $artifacts = @($census.artifacts)
    if ([int64]$census.total_count -ne $artifacts.Count -or $artifacts.Count -ne 1) {
        throw "Publisher workflow run must contain exactly one bridge artifact before download; API reported $($census.total_count), enumerated $($artifacts.Count)."
    }
    $artifact = $artifacts[0]
    if ([int64]$artifact.id -ne $ExpectedArtifactID -or [string]$artifact.name -cne $ExpectedArtifactName) {
        throw 'Artifact ID/name does not match the immutable prepare-release job output.'
    }
    if ([bool]$artifact.expired) { throw 'Release payload artifact is expired.' }
    $apiDigest = Normalize-Sha256Digest -Value ([string]$artifact.digest) -Name 'artifact API digest'
    if ($apiDigest -ne $expectedDigest) { throw 'Artifact API digest does not match the immutable upload-artifact job output.' }
    if ($null -eq $artifact.workflow_run -or [int64]$artifact.workflow_run.id -ne $CurrentRunID) {
        throw 'Artifact provenance does not bind to the current publisher workflow run.'
    }
    $result = [ordered]@{
        schema_version = 1
        artifact_id = [int64]$artifact.id
        artifact_name = [string]$artifact.name
        artifact_digest = $apiDigest
        workflow_run_id = $CurrentRunID
        artifact_count = 1
    }
    if (-not [string]::IsNullOrWhiteSpace($OutputPath)) { Write-JsonFile -Value $result -Path $OutputPath }
    $result | ConvertTo-Json -Depth 20
}

function Assert-RegularFileEnvelope {
    param([Parameter(Mandatory = $true)][string]$Root, [Parameter(Mandatory = $true)][string[]]$ExpectedNames)

    $resolvedRoot = [IO.Path]::GetFullPath($Root)
    if (-not (Test-Path -LiteralPath $resolvedRoot -PathType Container)) { throw "Payload root is not a directory: $resolvedRoot" }
    if (-not [string]::IsNullOrWhiteSpace($TrustedOutputRoot)) {
        $resolvedRoot = Assert-TrustedOutputTree -Path $resolvedRoot
    }
    $entries = @(Get-ChildItem -Force -LiteralPath $resolvedRoot)
    $actualNames = @($entries | ForEach-Object { $_.Name } | Sort-Object)
    $wantedNames = @($ExpectedNames | Sort-Object)
    if ($actualNames.Count -ne $wantedNames.Count -or @(Compare-Object -ReferenceObject $wantedNames -DifferenceObject $actualNames).Count -ne 0) {
        throw "Payload envelope must contain exactly: $($wantedNames -join ', ')."
    }
    foreach ($entry in $entries) {
        $isReparsePoint = ($entry.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0
        $linkType = if ($entry.PSObject.Properties.Name -contains 'LinkType') { [string]$entry.LinkType } else { '' }
        if (-not $entry.PSIsContainer -and -not $isReparsePoint -and [string]::IsNullOrWhiteSpace($linkType)) { continue }
        throw "Payload envelope contains a directory, symlink, or reparse entry: $($entry.FullName)"
    }
    return $resolvedRoot
}

function Assert-ArchiveEnvelope {
    param([Parameter(Mandatory = $true)][string]$Path)

    $entries = @(& tar -tf $Path 2>&1 | ForEach-Object { $_.ToString() })
    if ($LASTEXITCODE -ne 0 -or $entries.Count -eq 0) { throw "Image archive is not a readable tar payload: $Path" }
    foreach ($entry in $entries) {
        $normalized = $entry.Replace('\', '/')
        if ($normalized.StartsWith('/', [StringComparison]::Ordinal) -or $normalized -match '(^|/)\.\.(/|$)') {
            throw "Image archive contains a path-traversal entry: $entry"
        }
    }
    $verbose = @(& tar -tvf $Path 2>&1 | ForEach-Object { $_.ToString() })
    if ($LASTEXITCODE -ne 0 -or $verbose.Count -eq 0) { throw "Image archive metadata cannot be inspected: $Path" }
    foreach ($line in $verbose) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        if ($line[0] -ne '-' -and $line[0] -ne 'd') { throw "Image archive contains a non-regular outer entry: $line" }
    }
}

function Read-AndValidatePayload {
    if ([string]::IsNullOrWhiteSpace($PayloadRoot)) { throw 'PayloadRoot is required.' }
    $root = Assert-RegularFileEnvelope -Root $PayloadRoot -ExpectedNames @(
        'release-bundle.json', 'final-image-set.json', 'server.tar', 'operator-console.tar', 'postgres.tar'
    )
    $bundlePath = Join-Path $root 'release-bundle.json'
    $bundle = Get-Content -Raw -LiteralPath $bundlePath | ConvertFrom-Json -Depth 100
    if ([int]$bundle.schema_version -ne 1) { throw 'Unsupported release bundle schema.' }
    $commit = Assert-FullCommitSha -Value $ExpectedSha -Name 'ExpectedSha'
    $version = Assert-CanonicalVersion -Value $ReleaseVersion
    if ([string]$bundle.source_commit -ne $commit -or [string]$bundle.release_version -ne $version) {
        throw 'Release bundle commit/version does not match validated workflow provenance.'
    }
    if ([string]$bundle.manifest.file -cne 'final-image-set.json') { throw 'Release bundle manifest path is not canonical.' }
    $manifestPath = Join-Path $root 'final-image-set.json'
    $manifestBytes = [IO.File]::ReadAllBytes($manifestPath)
    $manifestHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $manifestPath).Hash.ToLowerInvariant()
    if ((Normalize-Sha256Digest -Value ([string]$bundle.manifest.sha256) -Name 'manifest digest') -ne "sha256:$manifestHash" -or
        [int64]$bundle.manifest.size_bytes -ne $manifestBytes.LongLength) {
        throw 'Release bundle manifest hash/size mismatch.'
    }
    $manifest = [Text.Encoding]::UTF8.GetString($manifestBytes) | ConvertFrom-Json -Depth 100
    if ([string]$manifest.status -ne 'PASS' -or [string]$manifest.source_parent_commit -ne $commit -or [string]$manifest.build_version -ne $version) {
        throw 'Acceptance manifest is not a PASS for the validated commit/version.'
    }
    $definitions = @(
        [ordered]@{ name = 'server'; archive = 'server.tar'; manifest_property = 'server' },
        [ordered]@{ name = 'operator_console'; archive = 'operator-console.tar'; manifest_property = 'operator_console' },
        [ordered]@{ name = 'postgres'; archive = 'postgres.tar'; manifest_property = 'postgres' }
    )
    $images = @($bundle.images)
    if ($images.Count -ne 3) { throw 'Release bundle must contain exactly three image records.' }
    $validatedImages = @()
    foreach ($definition in $definitions) {
        $matches = @($images | Where-Object { [string]$_.name -ceq $definition.name })
        if ($matches.Count -ne 1) { throw "Release bundle must contain exactly one $($definition.name) image record." }
        $image = $matches[0]
        if ([string]$image.archive -cne $definition.archive -or [IO.Path]::GetFileName([string]$image.archive) -cne [string]$image.archive) {
            throw "Release bundle archive path is not canonical for $($definition.name)."
        }
        $imageID = [string]$image.image_id
        $manifestID = [string]$manifest.image_ids.PSObject.Properties[$definition.manifest_property].Value
        if ($imageID -notmatch '^sha256:[0-9a-f]{64}$' -or $imageID -ne $manifestID) {
            throw "Release bundle image ID mismatch for $($definition.name)."
        }
        $archivePath = Join-Path $root $definition.archive
        $archiveItem = Get-Item -Force -LiteralPath $archivePath
        $archiveHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $archivePath).Hash.ToLowerInvariant()
        if ((Normalize-Sha256Digest -Value ([string]$image.sha256) -Name "$($definition.name) archive digest") -ne "sha256:$archiveHash" -or
            [int64]$image.size_bytes -ne $archiveItem.Length) {
            throw "Release bundle archive hash/size mismatch for $($definition.name)."
        }
        Assert-ArchiveEnvelope -Path $archivePath
        $validatedImages += [ordered]@{ name = $definition.name; archive = $archivePath; image_id = $imageID }
    }
    return [ordered]@{
        schema_version = 1
        payload_root = $root
        source_commit = $commit
        release_version = $version
        manifest_path = $manifestPath
        manifest_sha256 = "sha256:$manifestHash"
        images = @($validatedImages)
    }
}

function Invoke-ValidatePayload {
    $validated = Read-AndValidatePayload
    if (-not [string]::IsNullOrWhiteSpace($OutputPath)) { Write-JsonFile -Value $validated -Path $OutputPath }
    $validated | ConvertTo-Json -Depth 30
}

function Invoke-LoadPayload {
    $validated = Read-AndValidatePayload
    foreach ($image in $validated.images) {
        & docker image load --input $image.archive
        if ($LASTEXITCODE -ne 0) { throw "Docker failed to load validated image archive for $($image.name)." }
        $inspectRaw = & docker image inspect $image.image_id 2>&1
        if ($LASTEXITCODE -ne 0) { throw "Loaded archive does not contain exact image ID $($image.image_id)." }
        $inspect = @((($inspectRaw | Out-String) | ConvertFrom-Json -Depth 100))[0]
        if ([string]$inspect.Config.Labels.'org.opencontainers.image.revision' -ne $validated.source_commit -or
            [string]$inspect.Config.Labels.'org.opencontainers.image.version' -ne $validated.release_version) {
            throw "Loaded exact image $($image.image_id) lacks validated revision/version labels."
        }
    }
    $validated | ConvertTo-Json -Depth 30
}

function Invoke-ValidatePublicationEvidence {
    if ([string]::IsNullOrWhiteSpace($EvidenceRoot)) { throw 'EvidenceRoot is required.' }
    $root = Assert-RegularFileEnvelope -Root $EvidenceRoot -ExpectedNames @(
        'artifact-census.json', 'payload-validation.json', 'pre-login-publication-plan.json', 'publication-result.json'
    )
    $census = Get-Content -Raw -LiteralPath (Join-Path $root 'artifact-census.json') | ConvertFrom-Json -Depth 100
    $payload = Get-Content -Raw -LiteralPath (Join-Path $root 'payload-validation.json') | ConvertFrom-Json -Depth 100
    $preflight = Get-Content -Raw -LiteralPath (Join-Path $root 'pre-login-publication-plan.json') | ConvertFrom-Json -Depth 100
    $publication = Get-Content -Raw -LiteralPath (Join-Path $root 'publication-result.json') | ConvertFrom-Json -Depth 100
    if ([int]$census.artifact_count -ne 1 -or [int]$payload.schema_version -ne 1) {
        throw 'Publisher evidence lacks the single-artifact or payload-validation proof.'
    }
    foreach ($record in @($preflight, $publication)) {
        if (@($record.destinations).Count -ne 6 -or -not [bool]$record.external_package_admin_trust_boundary) {
            throw 'Publisher evidence does not cover all six immutable destinations and the external package-admin boundary.'
        }
    }
    if ([string]$preflight.acceptance_manifest_sha256 -notmatch '^sha256:[0-9a-f]{64}$' -or
        [string]$preflight.acceptance_manifest_sha256 -ne [string]$publication.acceptance_manifest_sha256 -or
        [string]$payload.manifest_sha256 -ne [string]$publication.acceptance_manifest_sha256) {
        throw 'Publisher evidence does not remain bound to one exact acceptance manifest.'
    }
    foreach ($destination in @($publication.destinations)) {
        if ($destination.action -notin @('pushed', 'verified-noop') -or
            [string]$destination.config_digest -notmatch '^sha256:[0-9a-f]{64}$' -or
            [string]$destination.manifest_digest -notmatch '^sha256:[0-9a-f]{64}$') {
            throw "Publisher evidence contains an incomplete destination readback: $($destination.reference)"
        }
    }
    [ordered]@{
        schema_version = 1
        evidence_root = $root
        artifact_count = 1
        destination_count = 6
        credentials_erased_before_validation = $true
    } | ConvertTo-Json -Depth 20
}

function Export-ReleasePayload {
    param(
        [Parameter(Mandatory = $true)][string]$AcceptanceManifestPath,
        [Parameter(Mandatory = $true)]$ExactImageIDs,
        [Parameter(Mandatory = $true)][string]$Commit,
        [Parameter(Mandatory = $true)][string]$BuildVersion
    )

    Assert-TrustedOutputRoot | Out-Null
    $payloadPath = if ([IO.Path]::IsPathRooted($ReleasePayloadPath)) {
        [IO.Path]::GetFullPath($ReleasePayloadPath)
    } else {
        [IO.Path]::GetFullPath((Join-Path $TrustedOutputRoot $ReleasePayloadPath))
    }
    $payloadPath = New-TrustedOutputDirectory -Path $payloadPath
    $manifestDestination = Join-Path $payloadPath 'final-image-set.json'
    [IO.File]::Copy($AcceptanceManifestPath, $manifestDestination, $false)
    $manifestHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $manifestDestination).Hash.ToLowerInvariant()
    $manifestSize = (Get-Item -LiteralPath $manifestDestination).Length
    $definitions = @(
        [ordered]@{ name = 'server'; archive = 'server.tar'; image_id = [string]$ExactImageIDs.server },
        [ordered]@{ name = 'operator_console'; archive = 'operator-console.tar'; image_id = [string]$ExactImageIDs.operator_console },
        [ordered]@{ name = 'postgres'; archive = 'postgres.tar'; image_id = [string]$ExactImageIDs.postgres }
    )
    $images = @()
    foreach ($definition in $definitions) {
        $archivePath = Join-Path $payloadPath $definition.archive
        & docker image save --output $archivePath $definition.image_id
        if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $archivePath -PathType Leaf)) {
            throw "Failed to export exact image data for $($definition.name)."
        }
        Assert-ArchiveEnvelope -Path $archivePath
        $images += [ordered]@{
            name = $definition.name
            archive = $definition.archive
            image_id = $definition.image_id
            sha256 = "sha256:$((Get-FileHash -Algorithm SHA256 -LiteralPath $archivePath).Hash.ToLowerInvariant())"
            size_bytes = (Get-Item -LiteralPath $archivePath).Length
        }
    }
    $bundle = [ordered]@{
        schema_version = 1
        source_commit = $Commit
        release_version = $BuildVersion
        manifest = [ordered]@{
            file = 'final-image-set.json'
            sha256 = "sha256:$manifestHash"
            size_bytes = $manifestSize
        }
        images = $images
    }
    Write-JsonFile -Value $bundle -Path (Join-Path $payloadPath 'release-bundle.json')
    Assert-RegularFileEnvelope -Root $payloadPath -ExpectedNames @(
        'release-bundle.json', 'final-image-set.json', 'server.tar', 'operator-console.tar', 'postgres.tar'
    ) | Out-Null
    return $payloadPath
}

function Get-FixtureRemoteIdentity {
    param([Parameter(Mandatory = $true)]$Fixture, [Parameter(Mandatory = $true)][string]$Reference)

    $property = $Fixture.refs.PSObject.Properties[$Reference]
    if ($null -eq $property -or $null -eq $property.Value) {
        return [ordered]@{ exists = $false; config_digest = $null; manifest_digest = $null }
    }
    return [ordered]@{
        exists = $true
        config_digest = [string]$property.Value.config_digest
        manifest_digest = [string]$property.Value.manifest_digest
    }
}

function Get-LiveRemoteIdentity {
    param([Parameter(Mandatory = $true)][string]$Reference)

    $rawOutput = & docker buildx imagetools inspect $Reference --raw 2>&1
    if ($LASTEXITCODE -ne 0) {
        $failure = ($rawOutput | Out-String)
        if ($failure -match '(?i)manifest unknown|not found|no such manifest') {
            return [ordered]@{ exists = $false; config_digest = $null; manifest_digest = $null }
        }
        throw "Remote image inspection failed for $Reference`: $failure"
    }
    $raw = ($rawOutput | Out-String).Trim()
    $manifest = $raw | ConvertFrom-Json -Depth 100
    if ([string]::IsNullOrWhiteSpace([string]$manifest.config.digest)) {
        throw "Remote destination must be a single-platform manifest with an exact config digest: $Reference"
    }
    $descriptorOutput = & docker buildx imagetools inspect $Reference --format '{{json .Manifest}}' 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "Remote descriptor inspection failed for $Reference`: $($descriptorOutput | Out-String)"
    }
    $descriptor = (($descriptorOutput | Out-String).Trim() | ConvertFrom-Json -Depth 100)
    if ([string]::IsNullOrWhiteSpace([string]$descriptor.digest)) {
        throw "Remote destination lacks a manifest digest: $Reference"
    }
    return [ordered]@{
        exists = $true
        config_digest = [string]$manifest.config.digest
        manifest_digest = [string]$descriptor.digest
    }
}

function New-PublicationPlan {
    param(
        [Parameter(Mandatory = $true)]$Manifest,
        [Parameter(Mandatory = $true)][string]$ReleaseVersion,
        $RegistryFixture
    )

    $validatedVersion = Assert-CanonicalVersion -Value $ReleaseVersion
    if ($validatedVersion -notmatch '^v') {
        throw 'Publication requires a canonical release version, not a commit-only version.'
    }
    $sourceCommit = Assert-FullCommitSha -Value ([string]$Manifest.source_parent_commit) -Name 'manifest source_parent_commit'
    $imageDefinitions = @(
        [ordered]@{ name = 'server'; repository = "$Registry/$Repository"; id = [string]$Manifest.image_ids.server },
        [ordered]@{ name = 'operator_console'; repository = "$Registry/$Repository-operator-console"; id = [string]$Manifest.image_ids.operator_console },
        [ordered]@{ name = 'postgres'; repository = "$Registry/$Repository-postgres"; id = [string]$Manifest.image_ids.postgres }
    )
    $destinations = @()
    foreach ($image in $imageDefinitions) {
        if ($image.id -notmatch '^sha256:[0-9a-f]{64}$') {
            throw "Manifest contains an invalid exact image ID for $($image.name): $($image.id)"
        }
        foreach ($tag in @($validatedVersion, "sha-$sourceCommit")) {
            $reference = "$($image.repository):$tag"
            $remote = if ($null -ne $RegistryFixture) {
                Get-FixtureRemoteIdentity -Fixture $RegistryFixture -Reference $reference
            } else {
                Get-LiveRemoteIdentity -Reference $reference
            }
            if ($remote.exists -and $remote.config_digest -ne $image.id) {
                throw "Destination $reference already resolves to $($remote.config_digest), not exact scanned image $($image.id); refusing every write."
            }
            $destinations += [ordered]@{
                image = $image.name
                reference = $reference
                config_digest = $image.id
                action = if ($remote.exists) { 'noop' } else { 'push' }
                manifest_digest = $remote.manifest_digest
            }
        }
    }
    return [ordered]@{
        schema_version = 1
        release_version = $validatedVersion
        source_commit = $sourceCommit
        single_writer_model = 'repository-workflow-release-publish'
        external_package_admin_trust_boundary = $true
        destinations = @($destinations | Sort-Object reference)
    }
}

function Invoke-ValidateRelease {
    $version = Assert-CanonicalReleaseRef -Ref $ReleaseRef
    $expected = Assert-FullCommitSha -Value $ExpectedSha -Name 'ExpectedSha'
    $actual = Assert-FullCommitSha -Value $ActualSha -Name 'ActualSha'
    if ($expected -ne $actual) {
        throw "Live release tag peels to $actual, expected workflow commit $expected."
    }
    $ruleset = Assert-ImmutableReleaseRuleset -Rulesets @(Get-TagRulesets)
    $result = [ordered]@{
        schema_version = 1
        version = $version
        commit = $expected
        ruleset_id = $ruleset.id
        ruleset_name = $ruleset.name
        immutable_tag_namespace = 'refs/tags/v*'
    }
    if (-not [string]::IsNullOrWhiteSpace($OutputPath)) {
        Write-JsonFile -Value $result -Path $OutputPath
    }
    if (-not [string]::IsNullOrWhiteSpace($GitHubOutputPath)) {
        Add-Content -Encoding utf8NoBOM -LiteralPath $GitHubOutputPath -Value "version=$version"
        Add-Content -Encoding utf8NoBOM -LiteralPath $GitHubOutputPath -Value "commit=$expected"
        Add-Content -Encoding utf8NoBOM -LiteralPath $GitHubOutputPath -Value "ruleset_id=$($ruleset.id)"
    }
    $result | ConvertTo-Json -Depth 20
}

function Read-PublicationInputs {
    $resolvedManifestPath = if ([string]::IsNullOrWhiteSpace($ManifestPath)) { $null } else { Resolve-RepositoryPath -Path $ManifestPath }
    if (-not [string]::IsNullOrWhiteSpace($TrustedOutputRoot) -and -not [string]::IsNullOrWhiteSpace($resolvedManifestPath)) {
        $resolvedManifestPath = Resolve-TrustedOutputPath -Path $resolvedManifestPath
    }
    if ([string]::IsNullOrWhiteSpace($resolvedManifestPath) -or -not (Test-Path -LiteralPath $resolvedManifestPath -PathType Leaf)) {
        throw "ManifestPath must name the final exact-image-set manifest: $ManifestPath"
    }
    $manifest = Get-Content -Raw -LiteralPath $resolvedManifestPath | ConvertFrom-Json -Depth 100
    $manifestHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $resolvedManifestPath).Hash.ToLowerInvariant()
    $fixture = if ([string]::IsNullOrWhiteSpace($RegistryFixturePath)) {
        $null
    } else {
        Get-Content -Raw -LiteralPath $RegistryFixturePath | ConvertFrom-Json -Depth 100
    }
    return [ordered]@{ manifest = $manifest; fixture = $fixture; manifest_sha256 = "sha256:$manifestHash" }
}

function Invoke-PlanPublication {
    $inputs = Read-PublicationInputs
    $plan = New-PublicationPlan -Manifest $inputs.manifest -ReleaseVersion $ReleaseVersion -RegistryFixture $inputs.fixture
    $plan['acceptance_manifest_sha256'] = $inputs.manifest_sha256
    if (-not [string]::IsNullOrWhiteSpace($OutputPath)) {
        Write-JsonFile -Value $plan -Path (Resolve-RepositoryPath -Path $OutputPath)
    }
    $plan | ConvertTo-Json -Depth 100
}

function Invoke-Publish {
    if (-not [string]::IsNullOrWhiteSpace($RegistryFixturePath)) {
        throw 'Publish mode never accepts a registry fixture.'
    }
    Assert-TrustedOutputRoot | Out-Null
    $inputs = Read-PublicationInputs
    if ([string]$inputs.manifest.status -ne 'PASS') {
        throw 'Publication requires a PASS image acceptance manifest produced before registry login.'
    }
    $validatedCommit = Assert-FullCommitSha -Value $ExpectedSha -Name 'ExpectedSha'
    $manifestCommit = Assert-FullCommitSha -Value ([string]$inputs.manifest.source_parent_commit) -Name 'manifest source_parent_commit'
    if ($manifestCommit -ne $validatedCommit) {
        throw "Trusted manifest commit $manifestCommit does not match validated workflow_run commit $validatedCommit."
    }
    $plan = New-PublicationPlan -Manifest $inputs.manifest -ReleaseVersion $ReleaseVersion -RegistryFixture $null
    $plan['acceptance_manifest_sha256'] = $inputs.manifest_sha256

    foreach ($destination in $plan.destinations) {
        & docker image inspect $destination.config_digest *> $null
        if ($LASTEXITCODE -ne 0) {
            throw "Exact scanned local image is missing before publication: $($destination.config_digest)"
        }
    }

    # All six destinations were inspected above. No registry write occurs until
    # every mismatch check has passed. This is a repository single-writer model,
    # not an atomic registry compare-and-swap claim.
    foreach ($destination in @($plan.destinations | Where-Object { $_.action -eq 'push' })) {
        & docker tag $destination.config_digest $destination.reference
        if ($LASTEXITCODE -ne 0) { throw "Local exact-ID tag failed: $($destination.reference)" }
        & docker push $destination.reference
        if ($LASTEXITCODE -ne 0) { throw "Registry push failed: $($destination.reference)" }
    }

    foreach ($destination in $plan.destinations) {
        $remote = Get-LiveRemoteIdentity -Reference $destination.reference
        if (-not $remote.exists -or $remote.config_digest -ne $destination.config_digest) {
            throw "Post-write readback mismatch for $($destination.reference)."
        }
        $destination.manifest_digest = $remote.manifest_digest
        $destination.action = if ($destination.action -eq 'push') { 'pushed' } else { 'verified-noop' }
    }
    $plan.completed_at = (Get-Date).ToUniversalTime().ToString('o')
    if (-not [string]::IsNullOrWhiteSpace($OutputPath)) {
        Write-JsonFile -Value $plan -Path (Resolve-RepositoryPath -Path $OutputPath)
    }
    $plan | ConvertTo-Json -Depth 100
}

switch ($Mode) {
    'ValidateRelease' { Invoke-ValidateRelease; exit 0 }
    'ValidateWorkflowRun' { Invoke-ValidateWorkflowRun; exit 0 }
    'ValidateArtifactMetadata' { Invoke-ValidateArtifactMetadata; exit 0 }
    'ValidatePayload' { Invoke-ValidatePayload; exit 0 }
    'LoadPayload' { Invoke-LoadPayload; exit 0 }
    'ValidatePublicationEvidence' { Invoke-ValidatePublicationEvidence; exit 0 }
    'PlanPublication' { Invoke-PlanPublication; exit 0 }
    'Publish' { Invoke-Publish; exit 0 }
}

foreach ($required in @{
    ServerTag = $ServerTag
    OperatorTag = $OperatorTag
    PostgresTag = $PostgresTag
    ArtifactRoot = $ArtifactRoot
}.GetEnumerator()) {
    if ([string]::IsNullOrWhiteSpace([string]$required.Value)) {
        throw "$($required.Key) is mandatory in BuildAndScan mode."
    }
}

if (-not $NoAllowlist) {
    throw 'Image acceptance is fail-closed: -NoAllowlist is mandatory and scanner exceptions are unsupported.'
}

$artifactPath = if ([IO.Path]::IsPathRooted($ArtifactRoot)) {
    [IO.Path]::GetFullPath($ArtifactRoot)
} elseif (-not [string]::IsNullOrWhiteSpace($TrustedOutputRoot)) {
    [IO.Path]::GetFullPath((Join-Path $TrustedOutputRoot $ArtifactRoot))
} else {
    [IO.Path]::GetFullPath((Join-Path $repoRoot $ArtifactRoot))
}
if (-not [string]::IsNullOrWhiteSpace($TrustedOutputRoot)) {
    $artifactPath = New-TrustedOutputDirectory -Path $artifactPath
} else {
    $repoPrefix = $repoRoot.TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    if (-not $artifactPath.StartsWith($repoPrefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "ArtifactRoot must resolve inside the repository unless TrustedOutputRoot is supplied: $artifactPath"
    }
}

$prefix = "engram-prc-img-$PID-$([Guid]::NewGuid().ToString('N').Substring(0, 8))"
$composeProject = "$prefix-compose"
$startedAt = (Get-Date).ToUniversalTime().ToString('o')
$caught = $null
$cleanupPassed = $false
$runtimePassed = $false
$imageIds = [ordered]@{}
$sarifHashes = [ordered]@{}
$scanCounts = [ordered]@{}
$toolVersions = [ordered]@{}
$lddHash = $null
$sourceCommit = $null
$sourceTree = $null
$buildVersion = $null
$buildContextRoot = $null
$buildContext = $null
$buildContextCleaned = $false
$runtimeProof = [ordered]@{
    critical_tests = $false
    volume_ownership_contract = $false
    server_home_persistence = $false
    legacy_postgres_uid_migration = $false
    compose_all_healthy = $false
    server_liveness = $false
    server_readiness = $false
    operator_readiness = $false
    postgres_17_10 = $false
    pgvector_0_8_1 = $false
    migrations_present = $false
    migration_table_count = 0
    core_schema_table_count = 0
    restart_recovery = $false
    postgres_recreation_retained_marker = $false
    local_tags_promoted_from_exact_ids = $false
}

New-Item -ItemType Directory -Force -Path $artifactPath | Out-Null
foreach ($name in @('server', 'operator-console', 'postgres', 'runtime', 'cleanup')) {
    New-Item -ItemType Directory -Force -Path (Join-Path $artifactPath $name) | Out-Null
}

function Invoke-LoggedNative {
    param(
        [Parameter(Mandatory = $true)][string]$File,
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [Parameter(Mandatory = $true)][string]$LogPath
    )

    Write-Host "> $File $($Arguments -join ' ')"
    $normalized = [Collections.Generic.List[string]]::new()
    & $File @Arguments 2>&1 | ForEach-Object {
        $line = $_.ToString().TrimEnd()
        $normalized.Add($line)
        Write-Host $line
    }
    $exitCode = $LASTEXITCODE
    $normalized | Set-Content -Encoding utf8NoBOM -LiteralPath $LogPath
    if ($exitCode -ne 0) {
        throw "$File exited $exitCode; transcript: $LogPath"
    }
}

function Remove-TrackedBuildContext {
    if ([string]::IsNullOrWhiteSpace($buildContextRoot)) {
        return
    }
    if (-not (Test-Path -LiteralPath $buildContextRoot)) {
        $script:buildContextCleaned = $true
        return
    }
    $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
    $resolved = [IO.Path]::GetFullPath($buildContextRoot)
    $tempPrefix = $tempRoot + [IO.Path]::DirectorySeparatorChar
    if (-not $resolved.StartsWith($tempPrefix, [StringComparison]::OrdinalIgnoreCase) -or
        -not ([IO.Path]::GetFileName($resolved)).StartsWith('engram-tracked-build-', [StringComparison]::Ordinal)) {
        throw "Refusing to remove unverified build-context path: $resolved"
    }
    Remove-Item -Force -Recurse -LiteralPath $resolved
    $script:buildContextCleaned = -not (Test-Path -LiteralPath $resolved)
}

function Invoke-CapturedNative {
    param(
        [Parameter(Mandatory = $true)][string]$File,
        [Parameter(Mandatory = $true)][string[]]$Arguments
    )

    $output = @(& $File @Arguments 2>&1 | ForEach-Object { $_.ToString() })
    $exitCode = $LASTEXITCODE
    if ($exitCode -ne 0) {
        throw "$File $($Arguments -join ' ') exited $exitCode`: $($output -join [Environment]::NewLine)"
    }
    return ($output -join [Environment]::NewLine).Trim()
}

function Get-ImageId {
    param([Parameter(Mandatory = $true)][string]$Tag)
    return Invoke-CapturedNative -File 'docker' -Arguments @('image', 'inspect', $Tag, '--format', '{{.Id}}')
}

function Get-ComposeContainerId {
    param([Parameter(Mandatory = $true)][string]$Service)
    return Invoke-CapturedNative -File 'docker' -Arguments @(
        'compose', '-p', $composeProject, '-f', 'docker-compose.yml', 'ps', '-q', $Service
    )
}

function Wait-Healthy {
    param(
        [Parameter(Mandatory = $true)][string]$Container,
        [int]$TimeoutSeconds = 120
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        $status = (& docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' $Container 2>$null).Trim()
        if ($LASTEXITCODE -eq 0 -and $status -eq 'healthy') {
            return
        }
        if ($status -in @('exited', 'dead')) {
            $logs = & docker logs --tail 80 $Container 2>&1
            throw "Container $Container reached $status before healthy: $logs"
        }
        Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $deadline)

    $inspect = & docker inspect --format '{{json .State}}' $Container 2>&1
    $logs = & docker logs --tail 80 $Container 2>&1
    throw "Container $Container did not become healthy. state=$inspect logs=$logs"
}

function Get-PublishedUrl {
    param(
        [Parameter(Mandatory = $true)][string]$Service,
        [Parameter(Mandatory = $true)][string]$ContainerPort
    )

    $published = Invoke-CapturedNative -File 'docker' -Arguments @(
        'compose', '-p', $composeProject, '-f', 'docker-compose.yml', 'port', $Service, $ContainerPort
    )
    $first = ($published -split "`r?`n")[0]
    if ($first -notmatch ':(?<port>\d+)$') {
        throw "Unexpected published-port value for $Service`: $first"
    }
    return "http://127.0.0.1:$($Matches.port)"
}

function Assert-ReadyJson {
    param([Parameter(Mandatory = $true)][string]$Url)
    $response = Invoke-WebRequest -UseBasicParsing -TimeoutSec 10 -Uri $Url
    if ($response.StatusCode -ne 200 -or $response.Content.Trim() -ne '{"status":"ready"}') {
        throw "Semantic readiness mismatch at $Url`: status=$($response.StatusCode) body=$($response.Content)"
    }
}

function Wait-ReadyJson {
    param(
        [Parameter(Mandatory = $true)][string]$Url,
        [int]$TimeoutSeconds = 60
    )
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $lastError = $null
    do {
        try {
            Assert-ReadyJson -Url $Url
            return
        } catch {
            $lastError = $_.Exception.Message
            Start-Sleep -Milliseconds 500
        }
    } while ((Get-Date) -lt $deadline)
    throw "Readiness did not recover at $Url`: $lastError"
}

function Wait-LivenessReady {
    param(
        [Parameter(Mandatory = $true)][string]$Url,
        [int]$TimeoutSeconds = 60
    )
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $lastError = $null
    do {
        try {
            $health = Invoke-RestMethod -TimeoutSec 5 -Uri $Url
            if ($health.status -eq 'ready') {
                return
            }
            $lastError = "status=$($health.status)"
        } catch {
            $lastError = $_.Exception.Message
        }
        Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $deadline)
    throw "Liveness did not recover at $Url`: $lastError"
}

function Invoke-ComposePsql {
    param([Parameter(Mandatory = $true)][string]$Sql)
    return Invoke-CapturedNative -File 'docker' -Arguments @(
        'compose', '-p', $composeProject, '-f', 'docker-compose.yml',
        'exec', '-T', 'postgres', 'psql', '-qAt', '-v', 'ON_ERROR_STOP=1',
        '-U', 'engram', '-d', 'engram', '-c', $Sql
    )
}

function Get-SarifResultCount {
    param([Parameter(Mandatory = $true)][string]$Path)
    $sarif = Get-Content -Raw -LiteralPath $Path | ConvertFrom-Json -Depth 100
    $results = @(
        foreach ($run in @($sarif.runs)) {
            if ($run.PSObject.Properties.Name -contains 'results') {
                @($run.results)
            }
        }
    )
    return $results.Count
}

function Get-PrefixedResourceInventory {
    param([Parameter(Mandatory = $true)][string]$ResourcePrefix)

    $containersOutput = Invoke-CapturedNative -File 'docker' -Arguments @('ps', '-aq', '--filter', "name=$ResourcePrefix")
    $volumesOutput = Invoke-CapturedNative -File 'docker' -Arguments @('volume', 'ls', '-q', '--filter', "name=$ResourcePrefix")
    $networksOutput = Invoke-CapturedNative -File 'docker' -Arguments @('network', 'ls', '-q', '--filter', "name=$ResourcePrefix")

    return [ordered]@{
        containers = @($containersOutput -split "`r?`n" | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
        volumes = @($volumesOutput -split "`r?`n" | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
        networks = @($networksOutput -split "`r?`n" | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    }
}

function Remove-PrefixedResources {
    param([Parameter(Mandatory = $true)][string]$ResourcePrefix)

    foreach ($entry in ([ordered]@{
        ENGRAM_SERVER_IMAGE = 'engram:cleanup-placeholder'
        ENGRAM_OPERATOR_IMAGE = 'engram:cleanup-placeholder'
        ENGRAM_POSTGRES_IMAGE = 'engram:cleanup-placeholder'
        ENGRAM_BUILD_VERSION = "sha-$('0' * 40)"
    }).GetEnumerator()) {
        if ([string]::IsNullOrWhiteSpace([Environment]::GetEnvironmentVariable($entry.Key, 'Process'))) {
            [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, 'Process')
        }
    }
    foreach ($entry in $script:imageSecretFiles.GetEnumerator()) {
        if ([string]::IsNullOrWhiteSpace([Environment]::GetEnvironmentVariable($entry.Key, 'Process'))) {
            [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, 'Process')
        }
    }
    Invoke-LoggedNative -File 'docker' -Arguments @(
        'compose', '-p', $composeProject, '-f', 'docker-compose.yml',
        'down', '--volumes', '--remove-orphans'
    ) -LogPath (Join-Path $artifactPath 'cleanup/compose-down.log')

    $before = Get-PrefixedResourceInventory -ResourcePrefix $ResourcePrefix

    foreach ($id in $before.containers) {
        Invoke-CapturedNative -File 'docker' -Arguments @('rm', '-f', $id) | Out-Null
    }
    foreach ($name in $before.volumes) {
        Invoke-CapturedNative -File 'docker' -Arguments @('volume', 'rm', $name) | Out-Null
    }
    foreach ($id in $before.networks) {
        Invoke-CapturedNative -File 'docker' -Arguments @('network', 'rm', $id) | Out-Null
    }

    $after = Get-PrefixedResourceInventory -ResourcePrefix $ResourcePrefix
    return [ordered]@{
        prefix = $ResourcePrefix
        removed = $before
        containers = $after.containers
        volumes = $after.volumes
        networks = $after.networks
    }
}

function New-CryptographicHex {
    param([ValidateRange(16, 128)][int]$ByteCount = 32)
    return [Convert]::ToHexString([System.Security.Cryptography.RandomNumberGenerator]::GetBytes($ByteCount)).ToLowerInvariant()
}

function Initialize-ImageSecretFiles {
    $root = Join-Path ([IO.Path]::GetTempPath()) "engram-image-secrets-$PID-$([Guid]::NewGuid().ToString('N'))"
    New-Item -ItemType Directory -Path $root -Force | Out-Null
    $script:PendingImageSecretRoot = [IO.Path]::GetFullPath($root)
    Set-ComposeSecretPathAccess -Path $root -Directory
    $postgresPassword = New-CryptographicHex
    $values = [ordered]@{
        ENGRAM_AUTH_ADMIN_TOKEN_SECRET_FILE = New-CryptographicHex
        ENGRAM_DATABASE_DSN_SECRET_FILE = "postgres://engram:$postgresPassword@postgres:5432/engram?sslmode=disable"
        ENGRAM_POSTGRES_PASSWORD_SECRET_FILE = $postgresPassword
        ENGRAM_VAULT_KEY_SECRET_FILE = New-CryptographicHex
    }
    $files = [ordered]@{}
    foreach ($entry in $values.GetEnumerator()) {
        $leaf = ($entry.Key.ToLowerInvariant() -replace '^engram_', '' -replace '_secret_file$', '' -replace '_', '-') + '.secret'
        $path = Join-Path $root $leaf
        [IO.File]::WriteAllText($path, [string]$entry.Value, [System.Text.UTF8Encoding]::new($false))
        Set-ComposeSecretPathAccess -Path $path
        $files[$entry.Key] = [IO.Path]::GetFullPath($path)
        [Environment]::SetEnvironmentVariable($entry.Key, $files[$entry.Key], 'Process')
    }
    $script:imageSecretFiles = $files
    return [IO.Path]::GetFullPath($root)
}

function Remove-ImageSecretFiles {
    param([AllowNull()][string]$Path)
    if ([string]::IsNullOrWhiteSpace($Path)) { return $true }
    $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\', '/')
    $resolved = [IO.Path]::GetFullPath($Path)
    $prefix = $tempRoot + [IO.Path]::DirectorySeparatorChar
    if (-not $resolved.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase) -or
        -not ([IO.Path]::GetFileName($resolved)).StartsWith('engram-image-secrets-', [StringComparison]::Ordinal)) {
        throw "Refusing to remove unverified image secret path: $resolved"
    }
    if (Test-Path -LiteralPath $resolved) { Remove-Item -LiteralPath $resolved -Recurse -Force }
    return -not (Test-Path -LiteralPath $resolved)
}

$environmentNames = @(
    'ENGRAM_SERVER_IMAGE', 'ENGRAM_OPERATOR_IMAGE', 'ENGRAM_POSTGRES_IMAGE', 'ENGRAM_BUILD_VERSION',
    'ENGRAM_TEST_RESOURCE_PREFIX', 'POSTGRES_PASSWORD', 'ENGRAM_AUTH_DISABLED',
    'WORKER_BIND', 'WORKER_PORT', 'OPERATOR_CONSOLE_BIND', 'OPERATOR_CONSOLE_PORT',
    'DATABASE_DSN', 'ENGRAM_AUTH_ADMIN_TOKEN', 'ENGRAM_VAULT_KEY',
    'ENGRAM_EMBEDDING_URL', 'ENGRAM_EMBEDDING_MODEL', 'ENGRAM_EMBEDDING_API_KEY',
    'ENGRAM_VNEXT_ENABLED', 'ENGRAM_LIFECYCLE_ENABLED', 'ENGRAM_VNEXT_F_ENABLED',
    'ENGRAM_GRAPH_ENABLED', 'ENGRAM_TEMPORAL_TRUTH_ENABLED',
    'ENGRAM_CRYSTALLIZATION_ENABLED', 'OPERATOR_CONSOLE_API_DISPLAY_HOST',
    'ENGRAM_AUTH_ADMIN_TOKEN_SECRET_FILE', 'ENGRAM_DATABASE_DSN_SECRET_FILE',
    'ENGRAM_POSTGRES_PASSWORD_SECRET_FILE', 'ENGRAM_VAULT_KEY_SECRET_FILE'
)
$savedEnvironment = [ordered]@{}
foreach ($name in $environmentNames) {
    $savedEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
    [Environment]::SetEnvironmentVariable($name, $null, 'Process')
}
$env:POSTGRES_PASSWORD = "prc-$([Guid]::NewGuid().ToString('N'))"
$env:ENGRAM_AUTH_DISABLED = 'true'
$env:WORKER_BIND = '127.0.0.1'
$env:WORKER_PORT = '0'
$env:OPERATOR_CONSOLE_BIND = '127.0.0.1'
$env:OPERATOR_CONSOLE_PORT = '0'
$script:imageSecretFiles = [ordered]@{}
$script:PendingImageSecretRoot = $null
$imageSecretRoot = $null
$secretFilesCleaned = $false
$locationPushed = $false

try {
    $imageSecretRoot = Initialize-ImageSecretFiles
    Push-Location $repoRoot
    $locationPushed = $true
    $toolVersions.docker = Invoke-CapturedNative -File 'docker' -Arguments @('version', '--format', '{{.Client.Version}} client / {{.Server.Version}} server')
    $toolVersions.buildx = Invoke-CapturedNative -File 'docker' -Arguments @('buildx', 'version')
    $toolVersions.scout = Invoke-CapturedNative -File 'docker' -Arguments @('scout', 'version')
    $toolVersions.go = Invoke-CapturedNative -File 'go' -Arguments @('version')
    $toolVersions.node = Invoke-CapturedNative -File 'node' -Arguments @('--version')
    $toolVersions.npm = Invoke-CapturedNative -File 'npm' -Arguments @('--version')

    $sourceCommit = Invoke-CapturedNative -File 'git' -Arguments @('rev-parse', 'HEAD')
    $sourceTree = Invoke-CapturedNative -File 'git' -Arguments @('rev-parse', 'HEAD^{tree}')
    $buildVersion = if ([string]::IsNullOrWhiteSpace($Version)) {
        Assert-CanonicalVersion -Value "sha-$sourceCommit"
    } else {
        Assert-CanonicalVersion -Value $Version
    }
    $sourceStatus = Invoke-CapturedNative -File 'git' -Arguments @('status', '--porcelain=v1', '--untracked-files=all')
    if (-not [string]::IsNullOrWhiteSpace($sourceStatus)) {
        throw "Image acceptance requires a clean source worktree; commit the candidate first: $sourceStatus"
    }

    $buildContextRoot = Join-Path ([IO.Path]::GetTempPath()) "engram-tracked-build-$PID-$([Guid]::NewGuid().ToString('N'))"
    $buildContext = Join-Path $buildContextRoot 'source'
    $archivePath = Join-Path $buildContextRoot 'source.zip'
    New-Item -ItemType Directory -Path $buildContext | Out-Null
    & git -C $repoRoot archive --format=zip --output=$archivePath HEAD
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $archivePath -PathType Leaf)) {
        throw 'Failed to create the tracked-file-only build context from candidate HEAD.'
    }
    Expand-Archive -LiteralPath $archivePath -DestinationPath $buildContext
    Remove-Item -Force -LiteralPath $archivePath
    $gitMetadata = @(Get-ChildItem -Force -Recurse -LiteralPath $buildContext | Where-Object { $_.Name -eq '.git' })
    if ($gitMetadata.Count -ne 0) {
        throw 'Tracked build context unexpectedly contains Git metadata or credentials.'
    }

    Invoke-LoggedNative -File 'docker' -Arguments @(
        'buildx', 'build', '--pull', '--no-cache', '--load', '--platform', $Platform,
        '--target', 'server', '--build-arg', "VERSION=$buildVersion",
        '--label', 'org.opencontainers.image.source=https://github.com/thebtf/engram',
        '--label', "org.opencontainers.image.revision=$sourceCommit",
        '--label', "org.opencontainers.image.version=$buildVersion",
        '--iidfile', (Join-Path $artifactPath 'server/image-id.txt'), '-t', $ServerTag, $buildContext
    ) -LogPath (Join-Path $artifactPath 'server/build.log')

    Invoke-LoggedNative -File 'docker' -Arguments @(
        'buildx', 'build', '--pull', '--no-cache', '--load', '--platform', $Platform,
        '--target', 'operator-console', '--build-arg', "VERSION=$buildVersion",
        '--label', 'org.opencontainers.image.source=https://github.com/thebtf/engram',
        '--label', "org.opencontainers.image.revision=$sourceCommit",
        '--label', "org.opencontainers.image.version=$buildVersion",
        '--iidfile', (Join-Path $artifactPath 'operator-console/image-id.txt'),
        '-t', $OperatorTag, $buildContext
    ) -LogPath (Join-Path $artifactPath 'operator-console/build.log')

    Invoke-LoggedNative -File 'docker' -Arguments @(
        'buildx', 'build', '--pull', '--no-cache', '--load', '--platform', $Platform,
        '-f', (Join-Path $buildContext 'deploy/postgres/Dockerfile'),
        '--label', 'org.opencontainers.image.source=https://github.com/thebtf/engram',
        '--label', "org.opencontainers.image.revision=$sourceCommit",
        '--label', "org.opencontainers.image.version=$buildVersion",
        '--iidfile', (Join-Path $artifactPath 'postgres/image-id.txt'),
        '-t', $PostgresTag, $buildContext
    ) -LogPath (Join-Path $artifactPath 'postgres/build.log')

    Remove-TrackedBuildContext

    $imageIds.server = (Get-Content -Raw -LiteralPath (Join-Path $artifactPath 'server/image-id.txt')).Trim()
    $imageIds.operator_console = (Get-Content -Raw -LiteralPath (Join-Path $artifactPath 'operator-console/image-id.txt')).Trim()
    $imageIds.postgres = (Get-Content -Raw -LiteralPath (Join-Path $artifactPath 'postgres/image-id.txt')).Trim()
    foreach ($entry in $imageIds.GetEnumerator()) {
        if ($entry.Value -notmatch '^sha256:[0-9a-f]{64}$') {
            throw "Buildx wrote an invalid exact image ID for $($entry.Key): $($entry.Value)"
        }
        Invoke-CapturedNative -File 'docker' -Arguments @('image', 'inspect', $entry.Value) | Out-Null
    }
    Invoke-CapturedNative -File 'docker' -Arguments @('image', 'inspect', $imageIds.server) |
        Set-Content -Encoding utf8NoBOM -LiteralPath (Join-Path $artifactPath 'server/image-inspect.json')
    Invoke-CapturedNative -File 'docker' -Arguments @('image', 'inspect', $imageIds.operator_console) |
        Set-Content -Encoding utf8NoBOM -LiteralPath (Join-Path $artifactPath 'operator-console/image-inspect.json')
    Invoke-CapturedNative -File 'docker' -Arguments @('image', 'inspect', $imageIds.postgres) |
        Set-Content -Encoding utf8NoBOM -LiteralPath (Join-Path $artifactPath 'postgres/image-inspect.json')

    $lddContainer = "$prefix-ldd-proof"
    Invoke-CapturedNative -File 'docker' -Arguments @('create', '--name', $lddContainer, $imageIds.server) | Out-Null
    try {
        $lddPath = Join-Path $artifactPath 'server/engram-server.ldd'
        Invoke-CapturedNative -File 'docker' -Arguments @('cp', "${lddContainer}:/usr/share/engram/engram-server.ldd", $lddPath) | Out-Null
        $lddText = Get-Content -Raw -LiteralPath $lddPath
        if ($lddText -match 'not found' -or $lddText -notmatch 'libc\.so') {
            throw "Server ldd proof is incomplete: $lddText"
        }
        $lddHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $lddPath).Hash.ToLowerInvariant()
    } finally {
        & docker rm -f $lddContainer 2>$null | Out-Null
    }

    $scanTargets = @(
        @{ Name = 'server'; Id = $imageIds.server },
        @{ Name = 'operator-console'; Id = $imageIds.operator_console },
        @{ Name = 'postgres'; Id = $imageIds.postgres }
    )
    foreach ($target in $scanTargets) {
        $sarif = Join-Path $artifactPath "$($target.Name)/docker-scout.sarif"
        Invoke-LoggedNative -File 'docker' -Arguments @(
            'scout', 'cves', "local://$($target.Id)", '--platform', $Platform,
            '--only-severity', 'critical,high', '--exit-code', '--format', 'sarif', '--output', $sarif
        ) -LogPath (Join-Path $artifactPath "$($target.Name)/docker-scout.log")
        $count = Get-SarifResultCount -Path $sarif
        if ($count -ne 0) {
            throw "Scanner returned $count HIGH/CRITICAL result(s) for $($target.Name)"
        }
        $scanCounts[$target.Name] = $count
        $sarifHashes[$target.Name] = (Get-FileHash -Algorithm SHA256 -LiteralPath $sarif).Hash.ToLowerInvariant()
    }

    Push-Location (Join-Path $repoRoot 'apps/operator-console')
    try {
        $auditPath = Join-Path $artifactPath 'operator-console/npm-audit.json'
        & npm audit --package-lock-only --audit-level=high --json 2>&1 |
            Set-Content -LiteralPath $auditPath
        if ($LASTEXITCODE -ne 0) {
            throw "npm audit reported a HIGH/CRITICAL locked-graph finding; evidence: $auditPath"
        }
        $auditText = Get-Content -Raw -LiteralPath $auditPath
        if ($auditText -match '"(picomatch|sigstore)"\s*:') {
            throw 'Operator lock audit contains a picomatch or sigstore finding.'
        }
    } finally {
        Pop-Location
    }

    $env:ENGRAM_SERVER_IMAGE = $imageIds.server
    $env:ENGRAM_OPERATOR_IMAGE = $imageIds.operator_console
    $env:ENGRAM_POSTGRES_IMAGE = $imageIds.postgres
    $env:ENGRAM_BUILD_VERSION = $buildVersion
    $env:ENGRAM_TEST_RESOURCE_PREFIX = "$prefix-test"

    Invoke-LoggedNative -File 'go' -Arguments @(
        'test', '-json', '-tags=critical', './tests/critical/runtime',
        '-run', '^(TestOperatorConsoleRuntimeTargetContract|TestServerImageContract|TestPostgresImageContract)$',
        '-count=1'
    ) -LogPath (Join-Path $artifactPath 'runtime/go-test.jsonl')
    $runtimeProof.critical_tests = $true
    $runtimeProof.volume_ownership_contract = $true
    $runtimeProof.server_home_persistence = $true
    $runtimeProof.legacy_postgres_uid_migration = $true

    Invoke-LoggedNative -File 'docker' -Arguments @(
        'compose', '-p', $composeProject, '-f', 'docker-compose.yml',
        'up', '-d', '--no-build', '--pull', 'never'
    ) -LogPath (Join-Path $artifactPath 'runtime/compose-up.log')

    foreach ($service in @('postgres', 'server', 'operator-console')) {
        Wait-Healthy -Container (Get-ComposeContainerId -Service $service)
    }
    $runtimeProof.compose_all_healthy = $true

    $serverUrl = Get-PublishedUrl -Service 'server' -ContainerPort '37777'
    $operatorUrl = Get-PublishedUrl -Service 'operator-console' -ContainerPort '3000'
    Wait-LivenessReady -Url "$serverUrl/health"
    $runtimeProof.server_liveness = $true
    Wait-ReadyJson -Url "$serverUrl/api/ready"
    $runtimeProof.server_readiness = $true
    Wait-ReadyJson -Url "$operatorUrl/api/ready"
    $runtimeProof.operator_readiness = $true

    if ((Invoke-ComposePsql -Sql 'SHOW server_version;').Trim() -ne '17.10') {
        throw 'Canonical compose PostgreSQL is not 17.10.'
    }
    $runtimeProof.postgres_17_10 = $true
    Invoke-ComposePsql -Sql 'CREATE EXTENSION IF NOT EXISTS vector;' | Out-Null
    if ((Invoke-ComposePsql -Sql "SELECT extversion FROM pg_extension WHERE extname='vector';").Trim() -ne '0.8.1') {
        throw 'Canonical compose pgvector is not 0.8.1.'
    }
    $runtimeProof.pgvector_0_8_1 = $true
    $tableCount = [int](Invoke-ComposePsql -Sql "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public';")
    if ($tableCount -lt 40) {
        throw "Server startup created only $tableCount public tables; expected the production schema (at least 40)."
    }
    $runtimeProof.migration_table_count = $tableCount
    $coreTableCount = [int](Invoke-ComposePsql -Sql "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name IN ('memories','behavioral_rules','credentials','issues','documents','api_tokens');")
    if ($coreTableCount -ne 6) {
        throw "Server startup is missing one or more core tables: found $coreTableCount of 6."
    }
    $runtimeProof.core_schema_table_count = $coreTableCount
    $runtimeProof.migrations_present = $true

    Invoke-ComposePsql -Sql "CREATE TABLE IF NOT EXISTS image_gate_marker (id integer PRIMARY KEY, note text NOT NULL, embedding vector(3) NOT NULL); INSERT INTO image_gate_marker VALUES (1, 'compose-retained', '[1,2,3]') ON CONFLICT (id) DO UPDATE SET note=EXCLUDED.note, embedding=EXCLUDED.embedding;" | Out-Null

    Invoke-LoggedNative -File 'docker' -Arguments @(
        'compose', '-p', $composeProject, '-f', 'docker-compose.yml', 'restart', 'server', 'operator-console'
    ) -LogPath (Join-Path $artifactPath 'runtime/compose-restart.log')
    foreach ($service in @('server', 'operator-console')) {
        Wait-Healthy -Container (Get-ComposeContainerId -Service $service)
    }
    $serverUrl = Get-PublishedUrl -Service 'server' -ContainerPort '37777'
    $operatorUrl = Get-PublishedUrl -Service 'operator-console' -ContainerPort '3000'
    Wait-ReadyJson -Url "$serverUrl/api/ready"
    Wait-ReadyJson -Url "$operatorUrl/api/ready"
    $runtimeProof.restart_recovery = $true

    Invoke-LoggedNative -File 'docker' -Arguments @(
        'compose', '-p', $composeProject, '-f', 'docker-compose.yml', 'stop', 'server', 'operator-console'
    ) -LogPath (Join-Path $artifactPath 'runtime/compose-stop-app.log')
    Invoke-LoggedNative -File 'docker' -Arguments @(
        'compose', '-p', $composeProject, '-f', 'docker-compose.yml', 'rm', '-f', '-s', 'postgres'
    ) -LogPath (Join-Path $artifactPath 'runtime/compose-remove-postgres.log')
    Invoke-LoggedNative -File 'docker' -Arguments @(
        'compose', '-p', $composeProject, '-f', 'docker-compose.yml',
        'up', '-d', '--no-build', '--pull', 'never', 'postgres'
    ) -LogPath (Join-Path $artifactPath 'runtime/compose-recreate-postgres.log')
    Wait-Healthy -Container (Get-ComposeContainerId -Service 'postgres')
    $marker = Invoke-ComposePsql -Sql "SELECT note || ':' || embedding::text FROM image_gate_marker WHERE id=1;"
    if ($marker.Trim() -ne 'compose-retained:[1,2,3]') {
        throw "PostgreSQL marker was not retained after container recreation: $marker"
    }
    $runtimeProof.postgres_recreation_retained_marker = $true

    Invoke-LoggedNative -File 'docker' -Arguments @(
        'compose', '-p', $composeProject, '-f', 'docker-compose.yml',
        'up', '-d', '--no-build', '--pull', 'never', 'server', 'operator-console'
    ) -LogPath (Join-Path $artifactPath 'runtime/compose-recover-app.log')
    foreach ($service in @('server', 'operator-console')) {
        Wait-Healthy -Container (Get-ComposeContainerId -Service $service)
    }

    foreach ($promotion in @(
        @{ Id = $imageIds.server; Tag = $ServerTag },
        @{ Id = $imageIds.operator_console; Tag = $OperatorTag },
        @{ Id = $imageIds.postgres; Tag = $PostgresTag }
    )) {
        Invoke-CapturedNative -File 'docker' -Arguments @('tag', $promotion.Id, $promotion.Tag) | Out-Null
        if ((Get-ImageId -Tag $promotion.Tag) -ne $promotion.Id) {
            throw "Local tag promotion did not retain exact image ID for $($promotion.Tag)."
        }
    }
    $runtimeProof.local_tags_promoted_from_exact_ids = $true
    $runtimePassed = $true
} catch {
    $caught = $_
} finally {
    try {
        Remove-TrackedBuildContext
    } catch {
        if ($null -eq $caught) { $caught = $_ }
        $buildContextCleaned = $false
    }
    try {
        $cleanupInventory = Remove-PrefixedResources -ResourcePrefix $prefix
        $cleanupPassed = @($cleanupInventory.containers).Count -eq 0 -and
            @($cleanupInventory.volumes).Count -eq 0 -and
            @($cleanupInventory.networks).Count -eq 0
    } catch {
        $cleanupPassed = $false
        $cleanupInventory = [ordered]@{
            prefix = $prefix
            removed = $null
            containers = $null
            volumes = $null
            networks = $null
            error = $_.Exception.Message
        }
        if ($null -eq $caught) {
            $caught = $_
        }
    }
    try {
        $secretCleanupRoot = if (-not [string]::IsNullOrWhiteSpace($imageSecretRoot)) { $imageSecretRoot } else { $script:PendingImageSecretRoot }
        $secretFilesCleaned = Remove-ImageSecretFiles -Path $secretCleanupRoot
        if (-not $secretFilesCleaned) { throw 'image secret-file cleanup did not remove the verified temporary root' }
        $script:PendingImageSecretRoot = $null
    } catch {
        $secretFilesCleaned = $false
        $cleanupPassed = $false
        if ($null -eq $caught) { $caught = $_ }
    }
    $cleanupInventory.status = if ($cleanupPassed) { 'PASS' } else { 'FAIL' }
    $cleanupInventory.secret_files_cleaned = $secretFilesCleaned
    $cleanupInventory.observed_at = (Get-Date).ToUniversalTime().ToString('o')
    $cleanupInventory | ConvertTo-Json -Depth 8 |
        Set-Content -Encoding utf8NoBOM -LiteralPath (Join-Path $artifactPath 'cleanup/cleanup.json')

    foreach ($name in $environmentNames) {
        [Environment]::SetEnvironmentVariable($name, $savedEnvironment[$name], 'Process')
    }

    if (-not [string]::IsNullOrWhiteSpace($TrustedOutputRoot)) {
        $artifactPath = Assert-TrustedOutputTree -Path $artifactPath
    }

    $manifest = [ordered]@{
        schema_version = 1
        status = if ($null -eq $caught -and $runtimePassed -and $cleanupPassed) { 'PASS' } else { 'FAIL' }
        started_at = $startedAt
        completed_at = (Get-Date).ToUniversalTime().ToString('o')
        source_parent_commit = $sourceCommit
        source_parent_tree = $sourceTree
        build_version = $buildVersion
        source_worktree_dirty = $false
        build_context = 'git-archive-tracked-files-only'
        git_credentials_present_in_build_context = $false
        build_context_cleanup_passed = $buildContextCleaned
        platform = $Platform
        no_allowlist = $true
        scanner_exception_inputs = @()
        tags = [ordered]@{ server = $ServerTag; operator_console = $OperatorTag; postgres = $PostgresTag }
        dockerfiles = [ordered]@{
            runtime_sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $repoRoot 'Dockerfile')).Hash.ToLowerInvariant()
            postgres_sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $repoRoot 'deploy/postgres/Dockerfile')).Hash.ToLowerInvariant()
        }
        server_ldd_sha256 = $lddHash
        pinned_sources = [ordered]@{
            dockerfile_frontend = 'docker/dockerfile:1@sha256:87999aa3d42bdc6bea60565083ee17e86d1f3339802f543c0d03998580f9cb89'
            server = 'gcr.io/distroless/base-debian13@sha256:b78832f41c8128046807c24840ebee4f1c18ba7870eed423d8750c272c15e147'
            operator_console = 'gcr.io/distroless/nodejs22-debian13@sha256:773a62fbe24a3f8c8b24b16fd59154627f8b406737bc906f83bf1732bc8907dd'
            postgres = 'cgr.dev/chainguard/wolfi-base@sha256:02dab76bd852a70556b5b2002195c8a5fdab77d323c433bf6642aab080489795'
            go_builder = 'golang:1.25.12-bookworm@sha256:a9c020ee3d1508c7be5435c262434e3d3fc1d0e76a11afeb9ddae7d60bc86aa4'
            node_builder = 'node:22-bookworm-slim@sha256:53ada149d435c38b14476cb57e4a7da73c15595aba79bd6971b547ceb6d018bf'
        }
        pinned_packages = [ordered]@{
            bash = '5.3-r12'
            gosu = '1.19-r13'
            postgresql = '17.10-r1'
            pgvector = '0.8.1-r0'
        }
        image_ids = $imageIds
        high_critical_findings = $scanCounts
        sarif_sha256 = $sarifHashes
        runtime_proof = $runtimeProof
        cleanup = $cleanupInventory
        published_digests = [ordered]@{ server = $null; operator_console = $null; postgres = $null }
        tools = $toolVersions
        failure = if ($null -eq $caught) { $null } else { $caught.Exception.Message }
    }
    Write-JsonFile -Value $manifest -Path (Join-Path $artifactPath 'final-image-set.json')
    if ($locationPushed) { Pop-Location }
}

if ($null -ne $caught) {
    throw $caught
}
if (-not $cleanupPassed) {
    throw "Cleanup verification failed for exact prefix $prefix"
}

if (-not [string]::IsNullOrWhiteSpace($ReleasePayloadPath)) {
    $payload = Export-ReleasePayload `
        -AcceptanceManifestPath (Join-Path $artifactPath 'final-image-set.json') `
        -ExactImageIDs $imageIds `
        -Commit $sourceCommit `
        -BuildVersion $buildVersion
    Write-Host "Release payload: $payload"
}

Write-Host "PASS: pinned images, exact-ID scans, runtime matrix, compose recreation, and cleanup proof"
Write-Host "Evidence: $artifactPath"
