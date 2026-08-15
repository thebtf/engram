[CmdletBinding()]
param(
    [ValidateSet('BuildAndScan', 'ScanPublished', 'ValidateRelease', 'ValidateWorkflowRun', 'ValidateArtifactMetadata', 'ValidatePayload', 'LoadPayload', 'ValidatePublicationEvidence', 'PlanPublication', 'Publish')]
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
    [string]$CredentialDirectoryPath,

    [string]$ManifestPath,
    [string]$ReleaseVersion,
    [string]$RegistryFixturePath,
    [string]$OutputPath,
    [string]$Registry = 'ghcr.io',
    [string]$Repository = 'thebtf/engram'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

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
    if ($Value -cmatch $commitPattern) {
        return $Value
    }
    if ($Value -cnotmatch $releasePattern) {
        throw "Version is not a canonical Docker-safe release or immutable commit identity: $Value"
    }
    if ($Matches[4]) {
        foreach ($identifier in $Matches[4].Split('.')) {
            if ($identifier -cmatch '^[0-9]+$' -and $identifier.Length -gt 1 -and $identifier.StartsWith('0', [StringComparison]::Ordinal)) {
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
    if ((Assert-CanonicalVersion -Value $value) -cnotmatch '^v') {
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
        $response = Invoke-RestMethod -Method Get -Uri $uri -Headers $headers
        $batch = @($response | ForEach-Object { $_ })
        $summaries += $batch
        if ($batch.Count -lt 100) { break }
    }
    $details = @()
    foreach ($summary in $summaries) {
        if ($summary.target -cne 'tag' -or $summary.enforcement -cne 'active') { continue }
        $uri = "$($GitHubApiUrl.TrimEnd('/'))/repos/$Repository/rulesets/$($summary.id)"
        $details += Invoke-RestMethod -Method Get -Uri $uri -Headers $headers
    }
    return @($details)
}

function Assert-ImmutableReleaseRuleset {
    param([Parameter(Mandatory = $true)][AllowEmptyCollection()][object[]]$Rulesets)

    $matches = @()
    foreach ($ruleset in $Rulesets) {
        if ($ruleset.target -cne 'tag' -or $ruleset.enforcement -cne 'active') { continue }
        $includes = @($ruleset.conditions.ref_name.include)
        $excludes = @($ruleset.conditions.ref_name.exclude)
        if ($includes.Count -ne 1 -or $includes[0] -cne 'refs/tags/v*' -or $excludes.Count -ne 0) { continue }
        $bypassProperty = $ruleset.PSObject.Properties['bypass_actors']
        if ($null -eq $bypassProperty -or @($bypassProperty.Value).Count -ne 0) { continue }
        $types = @($ruleset.rules | ForEach-Object { [string]$_.type })
        if ($types -cnotcontains 'deletion' -or $types -cnotcontains 'non_fast_forward') { continue }
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
        if ($ruleset.target -cne 'branch' -or $ruleset.enforcement -cne 'active') { continue }
        $includes = @($ruleset.conditions.ref_name.include)
        $excludes = @($ruleset.conditions.ref_name.exclude)
        if ($includes.Count -ne 1 -or $includes[0] -cne "refs/heads/$ExpectedDefaultBranch" -or $excludes.Count -ne 0) { continue }
        $types = @($ruleset.rules | ForEach-Object { [string]$_.type })
        if ($types -cnotcontains 'deletion' -or $types -cnotcontains 'non_fast_forward') { continue }
        $statusRules = @($ruleset.rules | Where-Object { $_.type -ceq 'required_status_checks' })
        if ($statusRules.Count -ne 1 -or -not [bool]$statusRules[0].parameters.strict_required_status_checks_policy) { continue }
        $authorityChecks = @($statusRules[0].parameters.required_status_checks | Where-Object {
            [string]$_.context -ceq 'authority-guard'
        })
        if ($authorityChecks.Count -ne 1) { continue }
        $integrationID = $authorityChecks[0].PSObject.Properties['integration_id']
        if ($null -eq $integrationID -or $integrationID.Value -isnot [int] -and $integrationID.Value -isnot [long]) { continue }
        if ([int64]$integrationID.Value -ne 15368) { continue }
        $bypassProperty = $ruleset.PSObject.Properties['bypass_actors']
        if ($null -eq $bypassProperty) { continue }
        $bypassActors = @($bypassProperty.Value)
        if ($bypassActors.Count -ne 1) { continue }
        $recoveryActor = $bypassActors[0]
        if ($recoveryActor.actor_type -cne 'User' -or [int64]$recoveryActor.actor_id -ne 7106373 -or $recoveryActor.bypass_mode -cne 'pull_request') { continue }
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
        $response = Invoke-GitHubApi -Path "/repos/$Repository/rulesets?per_page=100&page=$page"
        $batch = @($response | ForEach-Object { $_ })
        foreach ($summary in $batch) {
            if ($summary.target -cne $Target -or $summary.enforcement -cne 'active') { continue }
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
    if ($Repository -cnotmatch '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$') {
        throw "Repository must be owner/name: $Repository"
    }
    $inputs = Get-WorkflowRunValidationInputs
    $event = $inputs.event
    $eventRun = $event.workflow_run
    $apiRun = $inputs.api_run
    if ($event.action -cne 'completed') { throw 'Publisher accepts only workflow_run/completed.' }
    Assert-WorkflowRunFieldParity -EventRun $eventRun -ApiRun $apiRun
    if ($apiRun.name -cne $ExpectedWorkflowName -or $apiRun.path -cne $TrustedWorkflowPath) {
        throw 'Triggering run is not the named unprivileged Docker verification workflow.'
    }
    if ($apiRun.event -cne 'push' -or $apiRun.status -cne 'completed' -or $apiRun.conclusion -cne 'success') {
        throw 'Triggering workflow must be a successful completed push verification.'
    }
    if ($apiRun.head_repository.full_name -cne $Repository -or $apiRun.repository.full_name -cne $Repository -or $event.repository.full_name -cne $Repository) {
        throw 'Triggering run must originate from the same repository.'
    }
    if ($inputs.repository.full_name -cne $Repository -or $inputs.repository.default_branch -cne $ExpectedDefaultBranch) {
        throw 'Repository/default-branch API identity mismatch.'
    }
    if ([int64]$apiRun.workflow_id -ne [int64]$inputs.trusted_workflow.id -or
        $inputs.trusted_workflow.path -cne $TrustedWorkflowPath -or
        $inputs.trusted_workflow.name -cne $ExpectedWorkflowName -or
        $inputs.trusted_workflow.state -cne 'active') {
        throw 'Triggering workflow ID/path/name is not the active trusted default-branch workflow.'
    }

    $version = Assert-CanonicalReleaseRef -Ref "refs/tags/$($apiRun.head_branch)"
    $commit = Assert-FullCommitSha -Value ([string]$apiRun.head_sha) -Name 'workflow_run head_sha'
    $tagRuleset = Assert-ImmutableReleaseRuleset -Rulesets @($inputs.tag_rulesets)
    $mainRuleset = Assert-ProtectedMainRuleset -Rulesets @($inputs.branch_rulesets)

    if ($null -ne $inputs.git) {
        if ([string]$inputs.git.tag_commit -cne $commit) {
            throw 'Fixture tag does not peel to workflow_run head_sha.'
        }
        if (@($inputs.git.main_ancestors) -cnotcontains $commit) {
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
        if ($peeled -cne $commit) {
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
    if ($normalized -cmatch '^[0-9a-f]{64}$') {
        $normalized = "sha256:$normalized"
    }
    if ($normalized -cnotmatch '^sha256:[0-9a-f]{64}$') {
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
    if ($Repository -cnotmatch '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$') { throw "Repository must be owner/name: $Repository" }
    if ($ExpectedArtifactID -le 0 -or $CurrentRunID -le 0) { throw 'ExpectedArtifactID and CurrentRunID must be positive.' }
    if ($ExpectedArtifactName -cnotmatch '^engram-release-payload-[0-9]+-[0-9]+$') {
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
    if ($apiDigest -cne $expectedDigest) { throw 'Artifact API digest does not match the immutable upload-artifact job output.' }
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
    if ($actualNames.Count -ne $wantedNames.Count -or @(Compare-Object -CaseSensitive -ReferenceObject $wantedNames -DifferenceObject $actualNames).Count -ne 0) {
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


function Assert-ExactObjectProperties {
    param(
        [Parameter(Mandatory = $true)]$Value,
        [Parameter(Mandatory = $true)][string[]]$ExpectedNames,
        [Parameter(Mandatory = $true)][string]$Name
    )

    if ($null -eq $Value -or $Value -is [string] -or $Value -is [ValueType]) {
        throw "$Name must be an object."
    }
    $actualNames = @($Value.PSObject.Properties.Name | Sort-Object)
    $wantedNames = @($ExpectedNames | Sort-Object)
    if ($actualNames.Count -ne $wantedNames.Count -or @(Compare-Object -CaseSensitive -ReferenceObject $wantedNames -DifferenceObject $actualNames).Count -ne 0) {
        throw "$Name must contain exactly: $($wantedNames -join ', ')."
    }
}

function Assert-AcceptanceManifest {
    param(
        [Parameter(Mandatory = $true)]$Manifest,
        [Parameter(Mandatory = $true)][string]$Commit,
        [Parameter(Mandatory = $true)][string]$Version,
        [Parameter(Mandatory = $true)][Collections.IDictionary]$SarifPaths
    )

    if ([int]$Manifest.schema_version -ne 1 -or
        [string]$Manifest.status -cne 'PASS' -or
        [string]$Manifest.source_parent_commit -cne $Commit -or
        [string]$Manifest.build_version -cne $Version) {
        throw 'Acceptance manifest is not a schema-v1 PASS for the validated commit/version.'
    }
    foreach ($booleanField in @('build_context_cleanup_passed', 'no_allowlist')) {
        $property = $Manifest.PSObject.Properties[$booleanField]
        if ($null -eq $property -or $property.Value -isnot [bool] -or -not $property.Value) {
            throw "Acceptance manifest requires $booleanField=true."
        }
    }
    foreach ($falseField in @('source_worktree_dirty', 'git_credentials_present_in_build_context')) {
        $property = $Manifest.PSObject.Properties[$falseField]
        if ($null -eq $property -or $property.Value -isnot [bool] -or $property.Value) {
            throw "Acceptance manifest requires $falseField=false."
        }
    }
    if ([string]$Manifest.build_context -cne 'git-archive-tracked-files-only') {
        throw 'Acceptance manifest lacks the tracked-files-only build-context proof.'
    }
    $exceptionProperty = $Manifest.PSObject.Properties['scanner_exception_inputs']
    if ($null -eq $exceptionProperty -or $exceptionProperty.Value -isnot [array] -or @($exceptionProperty.Value).Count -ne 0) {
        throw 'Acceptance manifest contains scanner exception inputs.'
    }
    $failureProperty = $Manifest.PSObject.Properties['failure']
    if ($null -eq $failureProperty -or $null -ne $failureProperty.Value) {
        throw 'Acceptance manifest retains a failure value.'
    }

    $imageNames = @('server', 'operator_console', 'postgres')
    Assert-ExactObjectProperties -Value $Manifest.image_ids -ExpectedNames $imageNames -Name 'Acceptance manifest image_ids'
    Assert-ExactObjectProperties -Value $Manifest.high_critical_findings -ExpectedNames $imageNames -Name 'Acceptance manifest high_critical_findings'
    Assert-ExactObjectProperties -Value $Manifest.sarif_sha256 -ExpectedNames $imageNames -Name 'Acceptance manifest sarif_sha256'
    $sarifPathNames = @($SarifPaths.Keys | Sort-Object)
    if ($sarifPathNames.Count -ne 3 -or @(Compare-Object -CaseSensitive -ReferenceObject @($imageNames | Sort-Object) -DifferenceObject $sarifPathNames).Count -ne 0) {
        throw 'Acceptance manifest validator requires exactly three SARIF paths.'
    }
    foreach ($name in $imageNames) {
        $imageID = [string]$Manifest.image_ids.PSObject.Properties[$name].Value
        if ($imageID -cnotmatch '^sha256:[0-9a-f]{64}$') {
            throw "Acceptance manifest contains an invalid image ID for $name."
        }
        $findingProperty = $Manifest.high_critical_findings.PSObject.Properties[$name]
        if (($findingProperty.Value -isnot [int] -and $findingProperty.Value -isnot [long]) -or [int64]$findingProperty.Value -ne 0) {
            throw "Acceptance manifest contains HIGH/CRITICAL findings for $name."
        }
        $expectedHash = [string]$Manifest.sarif_sha256.PSObject.Properties[$name].Value
        if ($expectedHash -cnotmatch '^[0-9a-f]{64}$') {
            throw "Acceptance manifest contains an invalid SARIF hash for $name."
        }
        $sarifPath = [string]$SarifPaths[$name]
        if (-not (Test-Path -LiteralPath $sarifPath -PathType Leaf)) {
            throw "Acceptance manifest SARIF is missing for $name."
        }
        $actualHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $sarifPath).Hash.ToLowerInvariant()
        if ($actualHash -cne $expectedHash -or (Get-SarifResultCount -Path $sarifPath) -ne 0) {
            throw "Acceptance manifest SARIF is not a zero-finding match for $name."
        }
    }

    $runtimeBooleanFields = @(
        'critical_tests', 'volume_ownership_contract', 'server_home_persistence',
        'legacy_postgres_uid_migration', 'compose_all_healthy', 'server_liveness',
        'server_readiness', 'operator_readiness', 'postgres_17_10', 'pgvector_0_8_1',
        'migrations_present', 'restart_recovery', 'postgres_recreation_retained_marker',
        'local_tags_promoted_from_exact_ids'
    )
    $runtimeFields = @($runtimeBooleanFields) + @('migration_table_count', 'core_schema_table_count')
    Assert-ExactObjectProperties -Value $Manifest.runtime_proof -ExpectedNames $runtimeFields -Name 'Acceptance manifest runtime_proof'
    foreach ($name in $runtimeBooleanFields) {
        $property = $Manifest.runtime_proof.PSObject.Properties[$name]
        if ($property.Value -isnot [bool] -or -not $property.Value) {
            throw "Acceptance manifest runtime proof is incomplete at $name."
        }
    }
    $migrationCount = $Manifest.runtime_proof.PSObject.Properties['migration_table_count'].Value
    $coreCount = $Manifest.runtime_proof.PSObject.Properties['core_schema_table_count'].Value
    if (($migrationCount -isnot [int] -and $migrationCount -isnot [long]) -or [int64]$migrationCount -lt 40 -or
        ($coreCount -isnot [int] -and $coreCount -isnot [long]) -or [int64]$coreCount -ne 6) {
        throw 'Acceptance manifest runtime schema-count proof is incomplete.'
    }

    foreach ($field in @('status', 'containers', 'volumes', 'networks')) {
        if ($null -eq $Manifest.cleanup.PSObject.Properties[$field]) {
            throw "Acceptance manifest cleanup proof lacks $field."
        }
    }
    if ([string]$Manifest.cleanup.status -cne 'PASS') {
        throw 'Acceptance manifest cleanup status is not PASS.'
    }
    foreach ($field in @('containers', 'volumes', 'networks')) {
        $value = $Manifest.cleanup.PSObject.Properties[$field].Value
        if ($value -isnot [array] -or @($value).Count -ne 0) {
            throw "Acceptance manifest cleanup retained $field."
        }
    }
}

function Read-AndValidatePayload {
    if ([string]::IsNullOrWhiteSpace($PayloadRoot)) { throw 'PayloadRoot is required.' }
    $root = Assert-RegularFileEnvelope -Root $PayloadRoot -ExpectedNames @(
        'release-bundle.json', 'final-image-set.json', 'server.trivy.sarif', 'operator-console.trivy.sarif', 'postgres.trivy.sarif', 'server.tar', 'operator-console.tar', 'postgres.tar'
    )
    $bundlePath = Join-Path $root 'release-bundle.json'
    $bundle = Get-Content -Raw -LiteralPath $bundlePath | ConvertFrom-Json -Depth 100
    if ([int]$bundle.schema_version -ne 1) { throw 'Unsupported release bundle schema.' }
    $commit = Assert-FullCommitSha -Value $ExpectedSha -Name 'ExpectedSha'
    $version = Assert-CanonicalVersion -Value $ReleaseVersion
    if ([string]$bundle.source_commit -cne $commit -or [string]$bundle.release_version -cne $version) {
        throw 'Release bundle commit/version does not match validated workflow provenance.'
    }
    if ([string]$bundle.manifest.file -cne 'final-image-set.json') { throw 'Release bundle manifest path is not canonical.' }
    $manifestPath = Join-Path $root 'final-image-set.json'
    $manifestBytes = [IO.File]::ReadAllBytes($manifestPath)
    $manifestHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $manifestPath).Hash.ToLowerInvariant()
    if ((Normalize-Sha256Digest -Value ([string]$bundle.manifest.sha256) -Name 'manifest digest') -cne "sha256:$manifestHash" -or
        [int64]$bundle.manifest.size_bytes -ne $manifestBytes.LongLength) {
        throw 'Release bundle manifest hash/size mismatch.'
    }
    $manifest = [Text.Encoding]::UTF8.GetString($manifestBytes) | ConvertFrom-Json -Depth 100
    $definitions = @(
        [ordered]@{ name = 'server'; archive = 'server.tar'; sarif = 'server.trivy.sarif'; manifest_property = 'server' },
        [ordered]@{ name = 'operator_console'; archive = 'operator-console.tar'; sarif = 'operator-console.trivy.sarif'; manifest_property = 'operator_console' },
        [ordered]@{ name = 'postgres'; archive = 'postgres.tar'; sarif = 'postgres.trivy.sarif'; manifest_property = 'postgres' }
    )
    $sarifPaths = [ordered]@{}
    foreach ($definition in $definitions) {
        $sarifPaths[$definition.manifest_property] = Join-Path $root $definition.sarif
    }
    Assert-AcceptanceManifest -Manifest $manifest -Commit $commit -Version $version -SarifPaths $sarifPaths
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
        if ($imageID -cnotmatch '^sha256:[0-9a-f]{64}$' -or $imageID -cne $manifestID) {
            throw "Release bundle image ID mismatch for $($definition.name)."
        }
        $archivePath = Join-Path $root $definition.archive
        $archiveItem = Get-Item -Force -LiteralPath $archivePath
        $archiveHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $archivePath).Hash.ToLowerInvariant()
        if ((Normalize-Sha256Digest -Value ([string]$image.sha256) -Name "$($definition.name) archive digest") -cne "sha256:$archiveHash" -or
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

function Get-PayloadImageTag {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][string]$ImageID
    )

    if ($Name -cnotin @('server', 'operator_console', 'postgres')) {
        throw "Unsupported release payload image name: $Name"
    }
    if ($ImageID -cnotmatch '^sha256:[0-9a-f]{64}$') {
        throw "Invalid release payload image ID for $Name`: $ImageID"
    }
    $safeName = $Name.Replace('_', '-')
    return "engram-release-payload-$($safeName):$($ImageID.Substring(7))"
}

function Resolve-LocalImageId {
    param(
        [Parameter(Mandatory = $true)][string]$Tag,
        [Parameter(Mandatory = $true)][string]$ExpectedConfigDigest
    )

    $inspectJson = Invoke-CapturedNative -File 'docker' -Arguments @('image', 'inspect', $Tag, '--format', '{{json .}}')
    $inspect = $inspectJson | ConvertFrom-Json -Depth 100
    if ($null -eq $inspect -or $inspect -is [array]) {
        throw "Docker inspect returned an invalid result shape for $Tag."
    }
    $idProperty = $inspect.PSObject.Properties['Id']
    if ($null -eq $idProperty) {
        throw "Docker inspect returned no image ID for $Tag."
    }
    $localId = [string]$idProperty.Value
    if ($localId -cnotmatch '^sha256:[0-9a-f]{64}$') {
        throw "Docker returned an invalid local image ID for $Tag`: $localId"
    }

    $descriptorConfigDigest = $null
    $descriptorProperty = $inspect.PSObject.Properties['Descriptor']
    if ($null -ne $descriptorProperty -and $null -ne $descriptorProperty.Value) {
        $annotationsProperty = $descriptorProperty.Value.PSObject.Properties['annotations']
        if ($null -ne $annotationsProperty -and $null -ne $annotationsProperty.Value) {
            $configProperty = $annotationsProperty.Value.PSObject.Properties['config.digest']
            if ($null -ne $configProperty) {
                $descriptorConfigDigest = [string]$configProperty.Value
            }
        }
    }
    $observedConfigDigest = if ($localId -ceq $ExpectedConfigDigest) { $localId } else { $descriptorConfigDigest }
    if ($observedConfigDigest -cne $ExpectedConfigDigest) {
        throw "Loaded image $Tag does not match Buildx config digest $ExpectedConfigDigest."
    }
    return $localId
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
        $payloadTag = Get-PayloadImageTag -Name $image.name -ImageID $image.image_id
        $localId = Resolve-LocalImageId -Tag $payloadTag -ExpectedConfigDigest $image.image_id
        $inspectJson = Invoke-CapturedNative -File 'docker' -Arguments @('image', 'inspect', $localId, '--format', '{{json .}}')
        $inspect = $inspectJson | ConvertFrom-Json -Depth 100
        if ($null -eq $inspect -or $inspect -is [array]) {
            throw "Docker inspect returned an invalid result shape for loaded image $($image.name)."
        }
        $configProperty = $inspect.PSObject.Properties['Config']
        $labelsProperty = if ($null -ne $configProperty -and $null -ne $configProperty.Value) {
            $configProperty.Value.PSObject.Properties['Labels']
        } else {
            $null
        }
        $revisionProperty = if ($null -ne $labelsProperty -and $null -ne $labelsProperty.Value) {
            $labelsProperty.Value.PSObject.Properties['org.opencontainers.image.revision']
        } else {
            $null
        }
        $versionProperty = if ($null -ne $labelsProperty -and $null -ne $labelsProperty.Value) {
            $labelsProperty.Value.PSObject.Properties['org.opencontainers.image.version']
        } else {
            $null
        }
        $revision = if ($null -ne $revisionProperty) { [string]$revisionProperty.Value } else { $null }
        $version = if ($null -ne $versionProperty) { [string]$versionProperty.Value } else { $null }
        if ($revision -cne $validated.source_commit -or $version -cne $validated.release_version) {
            throw "Loaded exact image $($image.image_id) lacks validated revision/version labels."
        }
        $image['local_id'] = $localId
    }
    $validated | ConvertTo-Json -Depth 30
}

function Assert-PublicationDestinationRecord {
    param(
        [Parameter(Mandatory = $true)]$Record,
        [Parameter(Mandatory = $true)]$ExpectedPlan,
        [Parameter(Mandatory = $true)][ValidateSet('preflight', 'publication')][string]$Phase
    )

    $trustProperty = $Record.PSObject.Properties['external_package_admin_trust_boundary']
    $expectedInspection = if ($Phase -ceq 'preflight') { 'deferred-until-authenticated-publish' } else { 'complete' }
    if ([int]$Record.schema_version -ne 1 -or
        [string]$Record.release_version -cne [string]$ExpectedPlan.release_version -or
        [string]$Record.source_commit -cne [string]$ExpectedPlan.source_commit -or
        [string]$Record.single_writer_model -cne 'repository-workflow-release-publish' -or
        $null -eq $trustProperty -or $trustProperty.Value -isnot [bool] -or -not $trustProperty.Value -or
        [string]$Record.remote_inspection -cne $expectedInspection) {
        throw "Publisher $Phase evidence does not match the canonical publication plan."
    }

    $expectedDestinations = @($ExpectedPlan.destinations)
    $actualDestinations = @($Record.destinations)
    if ($actualDestinations.Count -ne 6 -or @($actualDestinations | Group-Object reference | Where-Object Count -ne 1).Count -ne 0) {
        throw "Publisher $Phase evidence does not contain six unique destinations."
    }
    $expectedReferences = @($expectedDestinations | ForEach-Object { [string]$_.reference } | Sort-Object)
    $actualReferences = @($actualDestinations | ForEach-Object { [string]$_.reference } | Sort-Object)
    if (@(Compare-Object -CaseSensitive -ReferenceObject $expectedReferences -DifferenceObject $actualReferences).Count -ne 0) {
        throw "Publisher $Phase evidence does not cover the canonical six destinations."
    }

    foreach ($expected in $expectedDestinations) {
        $actual = @($actualDestinations | Where-Object { [string]$_.reference -ceq [string]$expected.reference })[0]
        if ([string]$actual.image -cne [string]$expected.image -or
            [string]$actual.config_digest -cne [string]$expected.config_digest) {
            throw "Publisher $Phase evidence is bound to the wrong image for $($expected.reference)."
        }
        if ($Phase -ceq 'preflight') {
            $manifestDigestProperty = $actual.PSObject.Properties['manifest_digest']
            if ([string]$actual.action -cne 'inspect-after-login' -or $null -eq $manifestDigestProperty -or $null -ne $manifestDigestProperty.Value) {
                throw "Publisher preflight evidence is not an unmodified deferred plan for $($expected.reference)."
            }
        } elseif ($actual.action -cnotin @('pushed', 'verified-noop') -or
            [string]$actual.manifest_digest -cnotmatch '^sha256:[0-9a-f]{64}$') {
            throw "Publisher evidence contains an incomplete destination readback: $($expected.reference)"
        }
    }
}

function Invoke-ValidatePublicationEvidence {
    if ([string]::IsNullOrWhiteSpace($EvidenceRoot)) { throw 'EvidenceRoot is required.' }
    if ([string]::IsNullOrWhiteSpace($CredentialDirectoryPath)) { throw 'CredentialDirectoryPath is required.' }
    $credentialDirectory = Resolve-TrustedOutputPath -Path $CredentialDirectoryPath -LeafMayNotExist
    if (Test-Path -LiteralPath $credentialDirectory) {
        throw "Registry credential directory still exists before evidence validation: $credentialDirectory"
    }
    $root = Assert-RegularFileEnvelope -Root $EvidenceRoot -ExpectedNames @(
        'artifact-census.json', 'payload-validation.json', 'pre-login-publication-plan.json', 'publication-result.json',
        'final-image-set.json', 'server.trivy.sarif', 'operator-console.trivy.sarif', 'postgres.trivy.sarif'
    )
    $census = Get-Content -Raw -LiteralPath (Join-Path $root 'artifact-census.json') | ConvertFrom-Json -Depth 100
    $payload = Get-Content -Raw -LiteralPath (Join-Path $root 'payload-validation.json') | ConvertFrom-Json -Depth 100
    $preflight = Get-Content -Raw -LiteralPath (Join-Path $root 'pre-login-publication-plan.json') | ConvertFrom-Json -Depth 100
    $publication = Get-Content -Raw -LiteralPath (Join-Path $root 'publication-result.json') | ConvertFrom-Json -Depth 100
    if ([int]$census.artifact_count -ne 1 -or [int]$payload.schema_version -ne 1) {
        throw 'Publisher evidence lacks the single-artifact or payload-validation proof.'
    }
    $payloadCommit = Assert-FullCommitSha -Value ([string]$payload.source_commit) -Name 'payload source commit'
    $payloadVersion = Assert-CanonicalVersion -Value ([string]$payload.release_version)
    $manifestPath = Join-Path $root 'final-image-set.json'
    $manifestHash = "sha256:$((Get-FileHash -Algorithm SHA256 -LiteralPath $manifestPath).Hash.ToLowerInvariant())"
    if ($manifestHash -cne [string]$payload.manifest_sha256) {
        throw 'Retained publication evidence manifest does not match the validated payload.'
    }
    $manifest = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json -Depth 100
    Assert-AcceptanceManifest `
        -Manifest $manifest `
        -Commit $payloadCommit `
        -Version $payloadVersion `
        -SarifPaths ([ordered]@{
            server = Join-Path $root 'server.trivy.sarif'
            operator_console = Join-Path $root 'operator-console.trivy.sarif'
            postgres = Join-Path $root 'postgres.trivy.sarif'
        })
    $expectedPlan = New-PublicationPlan -Manifest $manifest -ReleaseVersion $payloadVersion -DeferLiveRemoteInspection
    Assert-PublicationDestinationRecord -Record $preflight -ExpectedPlan $expectedPlan -Phase preflight
    Assert-PublicationDestinationRecord -Record $publication -ExpectedPlan $expectedPlan -Phase publication
    if ([string]$preflight.acceptance_manifest_sha256 -cnotmatch '^sha256:[0-9a-f]{64}$' -or
        [string]$preflight.acceptance_manifest_sha256 -cne [string]$publication.acceptance_manifest_sha256 -or
        [string]$payload.manifest_sha256 -cne [string]$publication.acceptance_manifest_sha256 -or
        [string]$publication.status -cne 'PASS' -or $null -ne $publication.failure) {
        throw 'Publisher evidence does not remain bound to one complete successful publication.'
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
        [Parameter(Mandatory = $true)]$LocalImageIDs,
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
    $acceptanceRoot = Split-Path -Parent $AcceptanceManifestPath
    $definitions = @(
        [ordered]@{ name = 'server'; archive = 'server.tar'; sarif_source = 'server/trivy.sarif'; sarif = 'server.trivy.sarif'; image_id = [string]$ExactImageIDs.server; local_id = [string]$LocalImageIDs.server },
        [ordered]@{ name = 'operator_console'; archive = 'operator-console.tar'; sarif_source = 'operator-console/trivy.sarif'; sarif = 'operator-console.trivy.sarif'; image_id = [string]$ExactImageIDs.operator_console; local_id = [string]$LocalImageIDs.operator_console },
        [ordered]@{ name = 'postgres'; archive = 'postgres.tar'; sarif_source = 'postgres/trivy.sarif'; sarif = 'postgres.trivy.sarif'; image_id = [string]$ExactImageIDs.postgres; local_id = [string]$LocalImageIDs.postgres }
    )
    $images = @()
    foreach ($definition in $definitions) {
        $sarifSource = Join-Path $acceptanceRoot $definition.sarif_source
        if (-not (Test-Path -LiteralPath $sarifSource -PathType Leaf)) {
            throw "Acceptance SARIF is missing for $($definition.name): $sarifSource"
        }
        [IO.File]::Copy($sarifSource, (Join-Path $payloadPath $definition.sarif), $false)
        $archivePath = Join-Path $payloadPath $definition.archive
        $payloadTag = Get-PayloadImageTag -Name $definition.name -ImageID $definition.image_id
        Invoke-CapturedNative -File 'docker' -Arguments @('image', 'tag', $definition.local_id, $payloadTag) | Out-Null
        try {
            $resolvedLocalId = Resolve-LocalImageId -Tag $payloadTag -ExpectedConfigDigest $definition.image_id
            if ($resolvedLocalId -cne $definition.local_id) {
                throw "Payload tag $payloadTag resolved to unexpected local image ID $resolvedLocalId."
            }
            & docker image save --output $archivePath $payloadTag
            if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $archivePath -PathType Leaf)) {
                throw "Failed to export exact image data for $($definition.name)."
            }
        } finally {
            & docker image rm $payloadTag 2>$null | Out-Null
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
        'release-bundle.json', 'final-image-set.json', 'server.trivy.sarif', 'operator-console.trivy.sarif', 'postgres.trivy.sarif', 'server.tar', 'operator-console.tar', 'postgres.tar'
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
        $RegistryFixture,
        [switch]$DeferLiveRemoteInspection
    )

    if ($DeferLiveRemoteInspection -and $null -ne $RegistryFixture) {
        throw 'Deferred live registry inspection cannot be combined with a registry fixture.'
    }
    $validatedVersion = Assert-CanonicalVersion -Value $ReleaseVersion
    if ($validatedVersion -cnotmatch '^v') {
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
        if ($image.id -cnotmatch '^sha256:[0-9a-f]{64}$') {
            throw "Manifest contains an invalid exact image ID for $($image.name): $($image.id)"
        }
        foreach ($tag in @($validatedVersion, "sha-$sourceCommit")) {
            $reference = "$($image.repository):$tag"
            $remote = if ($DeferLiveRemoteInspection) {
                [ordered]@{ exists = $false; config_digest = $null; manifest_digest = $null }
            } elseif ($null -ne $RegistryFixture) {
                Get-FixtureRemoteIdentity -Fixture $RegistryFixture -Reference $reference
            } else {
                Get-LiveRemoteIdentity -Reference $reference
            }
            if ($remote.exists -and $remote.config_digest -cne $image.id) {
                throw "Destination $reference already resolves to $($remote.config_digest), not exact scanned image $($image.id); refusing every write."
            }
            $action = if ($DeferLiveRemoteInspection) {
                'inspect-after-login'
            } elseif ($remote.exists) {
                'noop'
            } else {
                'push'
            }
            $destinations += [ordered]@{
                image = $image.name
                reference = $reference
                config_digest = $image.id
                action = $action
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
        remote_inspection = if ($DeferLiveRemoteInspection) { 'deferred-until-authenticated-publish' } else { 'complete' }
        destinations = @($destinations | Sort-Object reference)
    }
}

function Invoke-ValidateRelease {
    $version = Assert-CanonicalReleaseRef -Ref $ReleaseRef
    $expected = Assert-FullCommitSha -Value $ExpectedSha -Name 'ExpectedSha'
    $actual = Assert-FullCommitSha -Value $ActualSha -Name 'ActualSha'
    if ($expected -cne $actual) {
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
    $plan = if ($null -eq $inputs.fixture) {
        New-PublicationPlan -Manifest $inputs.manifest -ReleaseVersion $ReleaseVersion -DeferLiveRemoteInspection
    } else {
        New-PublicationPlan -Manifest $inputs.manifest -ReleaseVersion $ReleaseVersion -RegistryFixture $inputs.fixture
    }
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
    if ([string]$inputs.manifest.status -cne 'PASS') {
        throw 'Publication requires a PASS image acceptance manifest produced before registry login.'
    }
    $validatedCommit = Assert-FullCommitSha -Value $ExpectedSha -Name 'ExpectedSha'
    $manifestCommit = Assert-FullCommitSha -Value ([string]$inputs.manifest.source_parent_commit) -Name 'manifest source_parent_commit'
    if ($manifestCommit -cne $validatedCommit) {
        throw "Trusted manifest commit $manifestCommit does not match validated workflow_run commit $validatedCommit."
    }
    $plan = New-PublicationPlan -Manifest $inputs.manifest -ReleaseVersion $ReleaseVersion -RegistryFixture $null
    $plan['acceptance_manifest_sha256'] = $inputs.manifest_sha256
    $plan['status'] = 'RUNNING'
    $plan['failure'] = $null

    $publishFailure = $null
    try {
        $localImageIdsByName = @{}
        foreach ($destination in $plan.destinations) {
            if (-not $localImageIdsByName.ContainsKey($destination.image)) {
                $payloadTag = Get-PayloadImageTag -Name $destination.image -ImageID $destination.config_digest
                $localImageIdsByName[$destination.image] = Resolve-LocalImageId `
                    -Tag $payloadTag -ExpectedConfigDigest $destination.config_digest
            }
            $destination['local_id'] = $localImageIdsByName[$destination.image]
        }

        # All six destinations were resolved to their exact local store IDs above. No registry write occurs until
        # every mismatch check has passed. This is a repository single-writer model,
        # not an atomic registry compare-and-swap claim.
        foreach ($destination in @($plan.destinations | Where-Object { $_.action -ceq 'push' })) {
            & docker tag $destination.local_id $destination.reference
            if ($LASTEXITCODE -ne 0) { throw "Local exact-ID tag failed: $($destination.reference)" }
            & docker push $destination.reference
            if ($LASTEXITCODE -ne 0) { throw "Registry push failed: $($destination.reference)" }
            $destination.action = 'push-sent'
        }

        foreach ($destination in $plan.destinations) {
            $remote = Get-LiveRemoteIdentity -Reference $destination.reference
            if (-not $remote.exists -or $remote.config_digest -cne $destination.config_digest) {
                throw "Post-write readback mismatch for $($destination.reference)."
            }
            $destination.manifest_digest = $remote.manifest_digest
            $destination.action = if ($destination.action -ceq 'push-sent') { 'pushed' } else { 'verified-noop' }
        }
        $plan.status = 'PASS'
    } catch {
        $publishFailure = $_
        $plan.status = 'FAIL'
        $plan.failure = $_.Exception.Message
    } finally {
        $plan.completed_at = (Get-Date).ToUniversalTime().ToString('o')
        if (-not [string]::IsNullOrWhiteSpace($OutputPath)) {
            Write-JsonFile -Value $plan -Path (Resolve-RepositoryPath -Path $OutputPath)
        }
    }
    if ($null -ne $publishFailure) {
        throw $publishFailure
    }
    $plan | ConvertTo-Json -Depth 100
}

function Assert-CanonicalPublishedReleaseVersion {
    param([Parameter(Mandatory = $true)][string]$Value)

    $version = Assert-CanonicalVersion -Value $Value
    if ($version -cnotmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$') {
        throw "Published-image scans require a canonical final vMAJOR.MINOR.PATCH release version: $Value"
    }
    return $version
}

function Invoke-LoggedNative {
    param(
        [Parameter(Mandatory = $true)][string]$File,
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [Parameter(Mandatory = $true)][string]$LogPath,
        [switch]$AllowFailure
    )

    Write-Host "> $File $($Arguments -join ' ')"
    $normalized = [Collections.Generic.List[string]]::new()
    & $File @Arguments 2>&1 | ForEach-Object {
        $line = $_.ToString().TrimEnd()
        $normalized.Add($line)
        Write-Host $line
    }
    $exitCode = $LASTEXITCODE
    if ($normalized.Count -eq 0) {
        [IO.File]::WriteAllText($LogPath, '')
    } else {
        $normalized | Set-Content -Encoding utf8NoBOM -LiteralPath $LogPath
    }
    if ($exitCode -ne 0 -and -not $AllowFailure) {
        throw "$File exited $exitCode; transcript: $LogPath"
    }
    if ($AllowFailure) {
        return $exitCode
    }
}

function Get-SarifResultCount {
    param([Parameter(Mandatory = $true)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "Scanner did not write SARIF evidence: $Path"
    }
    try {
        $sarif = Get-Content -Raw -LiteralPath $Path | ConvertFrom-Json -Depth 100
    } catch {
        throw "Scanner wrote malformed SARIF evidence at $Path`: $($_.Exception.Message)"
    }
    if ($null -eq $sarif -or $sarif -is [array] -or [string]$sarif.version -cne '2.1.0') {
        throw "Scanner wrote invalid SARIF evidence at $Path"
    }
    $runsProperty = $sarif.PSObject.Properties['runs']
    if ($null -eq $runsProperty -or $runsProperty.Value -isnot [array] -or @($runsProperty.Value).Count -eq 0) {
        throw "Scanner wrote SARIF without a runs array at $Path"
    }
    $results = @(
        foreach ($run in @($runsProperty.Value)) {
            if ($null -eq $run -or $run -is [string] -or $run -is [ValueType]) {
                throw "Scanner wrote malformed SARIF run at $Path"
            }
            $toolProperty = $run.PSObject.Properties['tool']
            $driverProperty = if ($null -ne $toolProperty -and $null -ne $toolProperty.Value) {
                $toolProperty.Value.PSObject.Properties['driver']
            } else {
                $null
            }
            $driverNameProperty = if ($null -ne $driverProperty -and $null -ne $driverProperty.Value) {
                $driverProperty.Value.PSObject.Properties['name']
            } else {
                $null
            }
            if ($null -eq $driverNameProperty -or [string]::IsNullOrWhiteSpace([string]$driverNameProperty.Value)) {
                throw "Scanner wrote SARIF without run.tool.driver.name at $Path"
            }
            $resultsProperty = $run.PSObject.Properties['results']
            if ($null -eq $resultsProperty -or $resultsProperty.Value -isnot [array]) {
                throw "Scanner wrote SARIF without a results array at $Path"
            }
            foreach ($result in @($resultsProperty.Value)) {
                if ($null -eq $result -or $result -is [string] -or $result -is [ValueType]) {
                    throw "Scanner wrote malformed SARIF result at $Path"
                }
                $result
            }
        }
    )
    return $results.Count
}

function Invoke-ScanPublished {
    foreach ($required in @{
        ReleaseVersion = $ReleaseVersion
        Registry = $Registry
        Repository = $Repository
        ArtifactRoot = $ArtifactRoot
    }.GetEnumerator()) {
        if ([string]::IsNullOrWhiteSpace([string]$required.Value)) {
            throw "$($required.Key) is mandatory in ScanPublished mode."
        }
    }
    if (-not $NoAllowlist) {
        throw 'Published-image scans are fail-closed: -NoAllowlist is mandatory and scanner exceptions are unsupported.'
    }
    $version = Assert-CanonicalPublishedReleaseVersion -Value $ReleaseVersion
    if ($Registry -cnotmatch '^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?(?::[0-9]+)?$') {
        throw "Registry must be a canonical registry host name: $Registry"
    }
    if ($Repository -cnotmatch '^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?/[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$') {
        throw "Repository must be a canonical owner/name: $Repository"
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
    New-Item -ItemType Directory -Force -Path $artifactPath | Out-Null
    foreach ($name in @('server', 'operator-console', 'postgres')) {
        New-Item -ItemType Directory -Force -Path (Join-Path $artifactPath $name) | Out-Null
    }

    $startedAt = (Get-Date).ToUniversalTime().ToString('o')
    $scanErrors = [Collections.Generic.List[string]]::new()
    $targets = @()
    $databaseLog = Join-Path $artifactPath 'trivy-db-refresh.log'
    $databaseVersionLog = Join-Path $artifactPath 'trivy-version.log'
    $databaseVersionSha256 = $null
    $databaseExitCode = $null
    $databaseError = $null
    $trivyVersion = $null
    try {
        $databaseExitCode = Invoke-LoggedNative -File 'trivy' -Arguments @('image', '--download-db-only') -LogPath $databaseLog -AllowFailure
        if ($databaseExitCode -ne 0) {
            $databaseError = "Trivy vulnerability database refresh exited $databaseExitCode; transcript: $databaseLog"
            $scanErrors.Add($databaseError)
        } else {
            Invoke-LoggedNative -File 'trivy' -Arguments @('--version') -LogPath $databaseVersionLog
            $trivyVersion = (Get-Content -Raw -LiteralPath $databaseVersionLog).Trim()
        }
    } catch {
        $databaseError = "Trivy vulnerability database refresh failed: $($_.Exception.Message)"
        $scanErrors.Add($databaseError)
        if (-not (Test-Path -LiteralPath $databaseLog -PathType Leaf)) {
            $databaseError | Set-Content -Encoding utf8NoBOM -LiteralPath $databaseLog
        }
    }
    if (Test-Path -LiteralPath $databaseVersionLog -PathType Leaf) {
        $databaseVersionSha256 = "sha256:$((Get-FileHash -Algorithm SHA256 -LiteralPath $databaseVersionLog).Hash.ToLowerInvariant())"
    }

    $definitions = @(
        [ordered]@{ name = 'server'; repository = "$Registry/$Repository" },
        [ordered]@{ name = 'operator-console'; repository = "$Registry/$Repository-operator-console" },
        [ordered]@{ name = 'postgres'; repository = "$Registry/$Repository-postgres" }
    )
    foreach ($definition in $definitions) {
        $tagReference = "$($definition.repository):$version"
        $logPath = Join-Path $artifactPath "$($definition.name)/trivy.log"
        $entry = [ordered]@{
            name = $definition.name
            tag_reference = $tagReference
            config_digest = $null
            manifest_digest = $null
            scan_reference = $null
            sarif = "$($definition.name)/trivy.sarif"
            sarif_sha256 = $null
            log = "$($definition.name)/trivy.log"
            scan_exit_code = $null
            high_critical_findings = $null
            error = $null
        }
        try {
            $remote = Get-LiveRemoteIdentity -Reference $tagReference
            if (-not $remote.exists) {
                throw "Published release tag does not resolve: $tagReference"
            }
            $entry.config_digest = Normalize-Sha256Digest -Value ([string]$remote.config_digest) -Name "$($definition.name) config digest"
            $entry.manifest_digest = Normalize-Sha256Digest -Value ([string]$remote.manifest_digest) -Name "$($definition.name) manifest digest"
            $entry.scan_reference = "$($definition.repository)@$($entry.manifest_digest)"
        } catch {
            $entry.error = "Published tag resolution failed: $($_.Exception.Message)"
            $scanErrors.Add("$($definition.name): $($entry.error)")
        }
        if ($null -eq $entry.error -and $null -eq $databaseError) {
            $sarifPath = Join-Path $artifactPath $entry.sarif
            try {
                $entry.scan_exit_code = Invoke-LoggedNative -File 'trivy' -Arguments @(
                    'image', '--skip-db-update', '--platform', $Platform,
                    '--scanners', 'vuln', '--severity', 'HIGH,CRITICAL', '--exit-code', '1',
                    '--format', 'sarif', '--output', $sarifPath, $entry.scan_reference
                ) -LogPath $logPath -AllowFailure
                $entry.high_critical_findings = Get-SarifResultCount -Path $sarifPath
                $entry.sarif_sha256 = "sha256:$((Get-FileHash -Algorithm SHA256 -LiteralPath $sarifPath).Hash.ToLowerInvariant())"
                if ($entry.scan_exit_code -ne 0 -and ($entry.scan_exit_code -ne 1 -or $entry.high_critical_findings -eq 0)) {
                    throw "Trivy scanner exited $($entry.scan_exit_code); transcript: $logPath"
                }
            } catch {
                $entry.error = "Trivy scanner failure: $($_.Exception.Message)"
                $scanErrors.Add("$($definition.name): $($entry.error)")
            }
        } elseif ($null -eq $entry.error) {
            $entry.error = 'Trivy scan was not attempted because the fresh vulnerability database could not be obtained.'
            $entry.error | Set-Content -Encoding utf8NoBOM -LiteralPath $logPath
        } else {
            $entry.error | Set-Content -Encoding utf8NoBOM -LiteralPath $logPath
        }
        $targets += ,$entry
    }

    $totalFindings = 0
    foreach ($entry in $targets) {
        if ($null -ne $entry.high_critical_findings) {
            $totalFindings += [int]$entry.high_critical_findings
        }
    }
    $failure = if ($scanErrors.Count -ne 0) {
        $scanErrors -join [Environment]::NewLine
    } elseif ($totalFindings -ne 0) {
        "Published images contain $totalFindings HIGH/CRITICAL finding(s)."
    } else {
        $null
    }
    $summary = [ordered]@{
        schema_version = 1
        status = if ($null -eq $failure) { 'PASS' } else { 'FAIL' }
        started_at = $startedAt
        completed_at = (Get-Date).ToUniversalTime().ToString('o')
        release_version = $version
        registry = $Registry
        repository = $Repository
        platform = $Platform
        no_allowlist = $true
        trivy_version = $trivyVersion
        trivy_database = [ordered]@{
            refresh_command = 'trivy image --download-db-only'
            refresh_log = 'trivy-db-refresh.log'
            refresh_exit_code = $databaseExitCode
            version_log = 'trivy-version.log'
            version_log_sha256 = $databaseVersionSha256
            error = $databaseError
        }
        images = $targets
        total_high_critical_findings = $totalFindings
        failure = $failure
    }
    Write-JsonFile -Value $summary -Path (Join-Path $artifactPath 'published-image-scan-summary.json')
    if ($scanErrors.Count -ne 0) {
        throw "Published image scan encountered scanner or registry errors; evidence: $artifactPath"
    }
    if ($totalFindings -ne 0) {
        throw "Published image scan found $totalFindings HIGH/CRITICAL finding(s); evidence: $artifactPath"
    }
    Write-Host 'PASS: scanned three published immutable image digests with a fresh Trivy database'
    Write-Host "Evidence: $artifactPath"
}

switch ($Mode) {
    'ValidateRelease' { Invoke-ValidateRelease; exit 0 }
    'ScanPublished' { Invoke-ScanPublished; exit 0 }
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
$localImageIds = [ordered]@{}
$sarifHashes = [ordered]@{}
$scanCounts = [ordered]@{}
$toolVersions = [ordered]@{}
$lddHash = $null
$sourceCommit = $null
$sourceTree = $null
$buildVersion = $null
$sourceDateEpoch = $null
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
        if ($LASTEXITCODE -eq 0 -and $status -ceq 'healthy') {
            return
        }
        if ($status -cin @('exited', 'dead')) {
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
    if ($response.StatusCode -ne 200 -or $response.Content.Trim() -cne '{"status":"ready"}') {
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
            if ($health.status -ceq 'ready') {
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

$environmentNames = @(
    'ENGRAM_SERVER_IMAGE', 'ENGRAM_OPERATOR_IMAGE', 'ENGRAM_POSTGRES_IMAGE', 'ENGRAM_BUILD_VERSION',
    'ENGRAM_TEST_RESOURCE_PREFIX', 'POSTGRES_PASSWORD', 'ENGRAM_AUTH_DISABLED',
    'WORKER_BIND', 'WORKER_PORT', 'OPERATOR_CONSOLE_BIND', 'OPERATOR_CONSOLE_PORT',
    'DATABASE_DSN', 'ENGRAM_AUTH_ADMIN_TOKEN', 'ENGRAM_VAULT_KEY',
    'ENGRAM_EMBEDDING_URL', 'ENGRAM_EMBEDDING_MODEL', 'ENGRAM_EMBEDDING_API_KEY',
    'ENGRAM_VNEXT_ENABLED', 'ENGRAM_LIFECYCLE_ENABLED', 'ENGRAM_VNEXT_F_ENABLED',
    'ENGRAM_GRAPH_ENABLED', 'ENGRAM_TEMPORAL_TRUTH_ENABLED',
    'ENGRAM_CRYSTALLIZATION_ENABLED', 'OPERATOR_CONSOLE_API_DISPLAY_HOST', 'SOURCE_DATE_EPOCH'
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

Push-Location $repoRoot
try {
    $toolVersions.docker = Invoke-CapturedNative -File 'docker' -Arguments @('version', '--format', '{{.Client.Version}} client / {{.Server.Version}} server')
    $toolVersions.buildx = Invoke-CapturedNative -File 'docker' -Arguments @('buildx', 'version')
    $toolVersions.trivy = Invoke-CapturedNative -File 'trivy' -Arguments @('--version')
    $toolVersions.go = Invoke-CapturedNative -File 'go' -Arguments @('version')
    $toolVersions.node = Invoke-CapturedNative -File 'node' -Arguments @('--version')
    $toolVersions.npm = Invoke-CapturedNative -File 'npm' -Arguments @('--version')

    $sourceCommit = Invoke-CapturedNative -File 'git' -Arguments @('rev-parse', 'HEAD')
    $sourceTree = Invoke-CapturedNative -File 'git' -Arguments @('rev-parse', 'HEAD^{tree}')
    $sourceDateEpoch = Invoke-CapturedNative -File 'git' -Arguments @('show', '-s', '--format=%ct', 'HEAD')
    if ($sourceDateEpoch -cnotmatch '^[1-9][0-9]*$') {
        throw "Git returned an invalid source commit epoch: $sourceDateEpoch"
    }
    $env:SOURCE_DATE_EPOCH = $sourceDateEpoch
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

    # BuildKit's default provenance attestation contains per-run metadata and changes --iidfile identity.
    # SOURCE_DATE_EPOCH pins image metadata and rewrite-timestamp pins exported layer file mtimes.
    # Disable implicit unpack because it conflicts with timestamp rewriting on Docker's default driver;
    # later runtime exercises unpack the locally stored images while release provenance stays immutable.
    Invoke-LoggedNative -File 'docker' -Arguments @(
        'buildx', 'build', '--pull', '--no-cache', '--provenance=false', '--output', 'type=docker,rewrite-timestamp=true,unpack=false', '--platform', $Platform,
        '--target', 'server', '--build-arg', "VERSION=$buildVersion",
        '--label', 'org.opencontainers.image.source=https://github.com/thebtf/engram',
        '--label', "org.opencontainers.image.revision=$sourceCommit",
        '--label', "org.opencontainers.image.version=$buildVersion",
        '--iidfile', (Join-Path $artifactPath 'server/image-id.txt'), '-t', $ServerTag, $buildContext
    ) -LogPath (Join-Path $artifactPath 'server/build.log')

    Invoke-LoggedNative -File 'docker' -Arguments @(
        'buildx', 'build', '--pull', '--no-cache', '--provenance=false', '--output', 'type=docker,rewrite-timestamp=true,unpack=false', '--platform', $Platform,
        '--target', 'operator-console', '--build-arg', "VERSION=$buildVersion",
        '--label', 'org.opencontainers.image.source=https://github.com/thebtf/engram',
        '--label', "org.opencontainers.image.revision=$sourceCommit",
        '--label', "org.opencontainers.image.version=$buildVersion",
        '--iidfile', (Join-Path $artifactPath 'operator-console/image-id.txt'),
        '-t', $OperatorTag, $buildContext
    ) -LogPath (Join-Path $artifactPath 'operator-console/build.log')

    Invoke-LoggedNative -File 'docker' -Arguments @(
        'buildx', 'build', '--pull', '--no-cache', '--provenance=false', '--output', 'type=docker,rewrite-timestamp=true,unpack=false', '--platform', $Platform,
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
        if ($entry.Value -cnotmatch '^sha256:[0-9a-f]{64}$') {
            throw "Buildx wrote an invalid exact image ID for $($entry.Key): $($entry.Value)"
        }
    }
    $localImageIds.server = Resolve-LocalImageId -Tag $ServerTag -ExpectedConfigDigest $imageIds.server
    $localImageIds.operator_console = Resolve-LocalImageId -Tag $OperatorTag -ExpectedConfigDigest $imageIds.operator_console
    $localImageIds.postgres = Resolve-LocalImageId -Tag $PostgresTag -ExpectedConfigDigest $imageIds.postgres
    Invoke-CapturedNative -File 'docker' -Arguments @('image', 'inspect', $localImageIds.server) |
        Set-Content -Encoding utf8NoBOM -LiteralPath (Join-Path $artifactPath 'server/image-inspect.json')
    Invoke-CapturedNative -File 'docker' -Arguments @('image', 'inspect', $localImageIds.operator_console) |
        Set-Content -Encoding utf8NoBOM -LiteralPath (Join-Path $artifactPath 'operator-console/image-inspect.json')
    Invoke-CapturedNative -File 'docker' -Arguments @('image', 'inspect', $localImageIds.postgres) |
        Set-Content -Encoding utf8NoBOM -LiteralPath (Join-Path $artifactPath 'postgres/image-inspect.json')

    $lddContainer = "$prefix-ldd-proof"
    Invoke-CapturedNative -File 'docker' -Arguments @('create', '--name', $lddContainer, $localImageIds.server) | Out-Null
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
        @{ Name = 'server'; ManifestName = 'server'; Id = $localImageIds.server },
        @{ Name = 'operator-console'; ManifestName = 'operator_console'; Id = $localImageIds.operator_console },
        @{ Name = 'postgres'; ManifestName = 'postgres'; Id = $localImageIds.postgres }
    )
    $scannerErrors = [Collections.Generic.List[string]]::new()
    $findingTargets = [Collections.Generic.List[string]]::new()
    foreach ($target in $scanTargets) {
        $sarif = Join-Path $artifactPath "$($target.Name)/trivy.sarif"
        $logPath = Join-Path $artifactPath "$($target.Name)/trivy.log"
        $exitCode = $null
        try {
            $exitCode = Invoke-LoggedNative -File 'trivy' -Arguments @(
                'image', '--image-src', 'docker', '--platform', $Platform,
                '--scanners', 'vuln', '--severity', 'HIGH,CRITICAL', '--exit-code', '1',
                '--format', 'sarif', '--output', $sarif, $target.Id
            ) -LogPath $logPath -AllowFailure
        } catch {
            $scannerErrors.Add("$($target.Name): $($_.Exception.Message)")
            continue
        }
        try {
            $count = Get-SarifResultCount -Path $sarif
            $scanCounts[$target.ManifestName] = $count
            $sarifHashes[$target.ManifestName] = (Get-FileHash -Algorithm SHA256 -LiteralPath $sarif).Hash.ToLowerInvariant()
            if ($exitCode -ne 0 -and ($exitCode -ne 1 -or $count -eq 0)) {
                $scannerErrors.Add("$($target.Name): trivy exited $exitCode; transcript: $logPath")
            }
            if ($count -ne 0) {
                $findingTargets.Add("$($target.Name)=$count")
            }
        } catch {
            $scannerErrors.Add("$($target.Name): $($_.Exception.Message)")
        }
    }
    if ($scannerErrors.Count -ne 0) {
        throw "Trivy scanner/tool failure(s) after all image scan attempts: $($scannerErrors -join '; ')"
    }
    if ($findingTargets.Count -ne 0) {
        throw "Scanner found HIGH/CRITICAL result(s) after all image scans: $($findingTargets -join ', ')"
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

    $env:ENGRAM_SERVER_IMAGE = $localImageIds.server
    $env:ENGRAM_OPERATOR_IMAGE = $localImageIds.operator_console
    $env:ENGRAM_POSTGRES_IMAGE = $localImageIds.postgres
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

    if ((Invoke-ComposePsql -Sql 'SHOW server_version;').Trim() -cne '17.10') {
        throw 'Canonical compose PostgreSQL is not 17.10.'
    }
    $runtimeProof.postgres_17_10 = $true
    Invoke-ComposePsql -Sql 'CREATE EXTENSION IF NOT EXISTS vector;' | Out-Null
    if ((Invoke-ComposePsql -Sql "SELECT extversion FROM pg_extension WHERE extname='vector';").Trim() -cne '0.8.1') {
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
    if ($marker.Trim() -cne 'compose-retained:[1,2,3]') {
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
        @{ Id = $localImageIds.server; Tag = $ServerTag },
        @{ Id = $localImageIds.operator_console; Tag = $OperatorTag },
        @{ Id = $localImageIds.postgres; Tag = $PostgresTag }
    )) {
        Invoke-CapturedNative -File 'docker' -Arguments @('tag', $promotion.Id, $promotion.Tag) | Out-Null
        if ((Get-ImageId -Tag $promotion.Tag) -cne $promotion.Id) {
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
    $cleanupInventory.status = if ($cleanupPassed) { 'PASS' } else { 'FAIL' }
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
        source_date_epoch = $sourceDateEpoch
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
            go_builder = 'golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36'
            node_builder = 'node:22-bookworm-slim@sha256:53ada149d435c38b14476cb57e4a7da73c15595aba79bd6971b547ceb6d018bf'
        }
        pinned_packages = [ordered]@{
            bash = '5.3-r12'
            gosu = '1.19-r13'
            postgresql = '17.10-r1'
            pgvector = '0.8.1-r0'
        }
        image_ids = $imageIds
        local_runtime_image_ids = $localImageIds
        high_critical_findings = $scanCounts
        sarif_sha256 = $sarifHashes
        runtime_proof = $runtimeProof
        cleanup = $cleanupInventory
        published_digests = [ordered]@{ server = $null; operator_console = $null; postgres = $null }
        tools = $toolVersions
        failure = if ($null -eq $caught) { $null } else { $caught.Exception.Message }
    }
    Write-JsonFile -Value $manifest -Path (Join-Path $artifactPath 'final-image-set.json')
    Pop-Location
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
        -LocalImageIDs $localImageIds `
        -Commit $sourceCommit `
        -BuildVersion $buildVersion
    Write-Host "Release payload: $payload"
}

Write-Host "PASS: pinned images, exact-ID scans, runtime matrix, compose recreation, and cleanup proof"
Write-Host "Evidence: $artifactPath"
