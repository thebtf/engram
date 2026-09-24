[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidateSet('Discover','EnsureJournal','Promote','Reconcile')][string]$Mode,
    [Parameter(Mandatory)][string]$RepositoryName,
    [Parameter(Mandatory)][string]$RunId,
    [Parameter(Mandatory)][string]$RunAttempt,
    [Parameter(Mandatory)][string]$HeadSha,
    [string]$ReleaseCommit = '',
    [string]$ReleaseTag = '',
    [string]$ReceiptDir = '',
    [switch]$AllowMissingJournal
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$script:JournalName = 'latest-promotion-journal'
$script:ApiVersion = '2026-03-10'
$script:Repositories = @(
    'ghcr.io/thebtf/engram',
    'ghcr.io/thebtf/engram-operator-console',
    'ghcr.io/thebtf/engram-postgres'
)

function Assert-RunIdentity
{
    if ($RunId -notmatch '^[1-9][0-9]*$' -or $RunAttempt -notmatch '^[1-9][0-9]*$' -or $HeadSha -notmatch '^[0-9a-f]{40}$')
    {
        throw 'latest-promotion journal lacks a valid run ID, attempt, or head SHA'
    }
    if (-not [string]::IsNullOrWhiteSpace($ReleaseCommit) -and $ReleaseCommit -notmatch '^[0-9a-f]{40}$')
    { throw 'latest-promotion journal release commit is invalid'
    }
    if ($RepositoryName -notmatch '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$')
    { throw 'latest-promotion journal lacks a valid repository name'
    }
}

function Get-ExternalId
{ return "latest-promotion:$RunId`:$RunAttempt"
}
function Get-DetailsUrl
{ return "$env:GITHUB_SERVER_URL/$env:GITHUB_REPOSITORY/actions/runs/$RunId"
}

function Invoke-GhJson
{
    param([Parameter(Mandatory)][string[]]$Arguments, [string]$Failure = 'GitHub API request failed')
    $lines = @(gh @Arguments)
    if ($LASTEXITCODE -ne 0)
    { throw $Failure
    }
    try
    { return ($lines -join "`n") | ConvertFrom-Json
    } catch
    { throw "$Failure`: invalid JSON"
    }
}

function Test-RunIdentity
{
    param([Parameter(Mandatory)]$Run)
    return [string]$Run.name -ceq $script:JournalName -and
    [string]$Run.head_sha -ceq $HeadSha -and
    [string]$Run.external_id -ceq (Get-ExternalId) -and
    [string]$Run.details_url -ceq (Get-DetailsUrl)
}

function Find-Journal
{
    $encodedName = [uri]::EscapeDataString($script:JournalName)
    $response = Invoke-GhJson -Arguments @('api','--paginate','--slurp','-H','Accept: application/vnd.github+json','-H',"X-GitHub-Api-Version: $script:ApiVersion","repos/$RepositoryName/commits/$HeadSha/check-runs?check_name=$encodedName&filter=all&per_page=100") -Failure 'could not discover latest-promotion journal check run'
    $runs = if ($response -is [array])
    { @($response | ForEach-Object { $_.check_runs })
    } else
    { @($response.check_runs)
    }
    $matches = @($runs | Where-Object { Test-RunIdentity -Run $_ })
    if ($matches.Count -gt 1)
    { throw "latest-promotion journal discovery found duplicates: $($matches.Count)"
    }
    if ($matches.Count -eq 0)
    { return $null
    }
    return $matches[0]
}

function Get-Journal
{
    param([Parameter(Mandatory)][string]$JournalId)
    if ($JournalId -notmatch '^[1-9][0-9]*$')
    { throw 'latest-promotion journal ID is invalid'
    }
    $run = Invoke-GhJson -Arguments @('api','-H','Accept: application/vnd.github+json','-H',"X-GitHub-Api-Version: $script:ApiVersion","repos/$RepositoryName/check-runs/$JournalId") -Failure 'could not read latest-promotion journal check run'
    if ([string]$run.id -cne $JournalId -or -not (Test-RunIdentity -Run $run))
    { throw 'latest-promotion journal readback returned an unexpected check run'
    }
    return $run
}

function New-Identity
{
    param([string]$State = 'unknown', [string]$ImmutableReference = $null, [string]$ManifestDigest = $null)
    return [ordered]@{ state = $State; immutable_reference = $ImmutableReference; manifest_digest = $ManifestDigest }
}

function New-Target
{
    param([Parameter(Mandatory)][string]$Repository, $Intended = $null)
    $intendedIdentity = if ($null -eq $Intended)
    { New-Identity
    } else
    { New-Identity -State 'present' -ImmutableReference ([string]$Intended.immutable_reference) -ManifestDigest ([string]$Intended.manifest_digest)
    }
    return [ordered]@{
        repository = $Repository
        reference = "$Repository`:latest"
        previous = New-Identity
        intended = $intendedIdentity
        observed = New-Identity
        state = 'unknown'
        failure = $null
    }
}

function New-Snapshot
{
    param([string]$Phase = 'pre_promotion', [string]$Outcome = 'pending', [object[]]$Sources = @())
    $byRepository = @{}
    foreach ($source in $Sources)
    { $byRepository[[string]$source.repository] = $source
    }
    $targets = foreach ($repository in $script:Repositories)
    { New-Target -Repository $repository -Intended $byRepository[$repository]
    }
    return [ordered]@{
        schema_version = 1
        run = [ordered]@{ id = $RunId; attempt = $RunAttempt; head_sha = $HeadSha; external_id = Get-ExternalId; details_url = Get-DetailsUrl }
        release = [ordered]@{
            tag = if ([string]::IsNullOrWhiteSpace($ReleaseTag))
            { $null
            } else
            { $ReleaseTag
            }
            source_commit = if ([string]::IsNullOrWhiteSpace($ReleaseCommit))
            { $null
            } else
            { $ReleaseCommit
            }
        }
        phase = $Phase
        outcome = $Outcome
        targets = @($targets)
        revalidation = [ordered]@{ status = 'not_started'; observed_release_tag = $null; observed_source_commit = $null; failure = $null }
        rollback = [ordered]@{ attempted = $false; outcome = 'not_needed'; failures = @() }
        failure = $null
    }
}

function ConvertTo-SnapshotJson
{
    param([Parameter(Mandatory)]$Snapshot)
    return $Snapshot | ConvertTo-Json -Compress -Depth 12
}

function Assert-DigestIdentity
{
    param([Parameter(Mandatory)]$Identity, [Parameter(Mandatory)][string]$Description, [switch]$AllowUnknown)
    $state = [string]$Identity.state
    if ($AllowUnknown -and $state -ceq 'unknown')
    { return
    }
    if ($state -ceq 'readback_error')
    {
        if ($null -ne $Identity.immutable_reference -or $null -ne $Identity.manifest_digest)
        { throw "$Description readback error identity is contradictory"
        }
        return
    }
    if ($state -ceq 'absent')
    {
        if ($null -ne $Identity.immutable_reference -or $null -ne $Identity.manifest_digest)
        { throw "$Description absent identity is contradictory"
        }
        return
    }
    if ($state -cne 'present' -or [string]$Identity.manifest_digest -notmatch '^sha256:[0-9a-f]{64}$')
    { throw "$Description is not a typed immutable identity"
    }
    $digest = [string]$Identity.manifest_digest
    if ([string]$Identity.immutable_reference -notmatch "@$([regex]::Escape($digest))$")
    { throw "$Description immutable reference does not match its digest"
    }
}

function Assert-Snapshot
{
    param([Parameter(Mandatory)]$Snapshot)
    if ([int]$Snapshot.schema_version -ne 1)
    { throw 'latest-promotion snapshot schema is unsupported'
    }
    if ([string]$Snapshot.run.id -cne $RunId -or [string]$Snapshot.run.attempt -cne $RunAttempt -or [string]$Snapshot.run.head_sha -cne $HeadSha -or [string]$Snapshot.run.external_id -cne (Get-ExternalId) -or [string]$Snapshot.run.details_url -cne (Get-DetailsUrl))
    {
        throw 'latest-promotion snapshot run identity is contradictory'
    }
    $terminalKey = "$([string]$Snapshot.phase)/$([string]$Snapshot.outcome)"
    $snapshotReleaseCommit = [string]$Snapshot.release.source_commit
    if ($terminalKey -cne 'failed/contradiction' -and $snapshotReleaseCommit -notmatch '^[0-9a-f]{40}$')
    { throw 'latest-promotion snapshot release commit is invalid'
    }
    if (-not [string]::IsNullOrWhiteSpace($ReleaseCommit) -and $snapshotReleaseCommit -cne $ReleaseCommit)
    { throw 'latest-promotion snapshot release identity is contradictory'
    }
    if ($terminalKey -cne 'failed/contradiction' -and [string]$Snapshot.release.tag -notmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$')
    { throw 'latest-promotion snapshot release tag is invalid'
    }
    if (-not [string]::IsNullOrWhiteSpace($ReleaseTag) -and [string]$Snapshot.release.tag -cne $ReleaseTag)
    { throw 'latest-promotion snapshot release tag is contradictory'
    }
    $targets = @($Snapshot.targets)
    if ($targets.Count -ne $script:Repositories.Count)
    { throw 'latest-promotion snapshot must contain exactly three targets'
    }
    for ($index = 0; $index -lt $script:Repositories.Count; $index++)
    {
        $target = $targets[$index]
        $repository = $script:Repositories[$index]
        if ([string]$target.repository -cne $repository -or [string]$target.reference -cne "$repository`:latest")
        { throw 'latest-promotion snapshot target ordering or identity is contradictory'
        }
        Assert-DigestIdentity -Identity $target.previous -Description "$repository previous" -AllowUnknown
        Assert-DigestIdentity -Identity $target.intended -Description "$repository intended" -AllowUnknown
        Assert-DigestIdentity -Identity $target.observed -Description "$repository observed" -AllowUnknown
    }
    return $Snapshot
}

function Read-Snapshot
{
    param([Parameter(Mandatory)]$Run)
    $text = [string]$Run.output.text
    if ([string]::IsNullOrWhiteSpace($text))
    { throw 'latest-promotion journal lacks its canonical external snapshot'
    }
    try
    { $snapshot = $text | ConvertFrom-Json
    } catch
    { throw 'latest-promotion journal snapshot is malformed JSON'
    }
    return Assert-Snapshot -Snapshot $snapshot
}

function Write-LocalSnapshot
{
    param([Parameter(Mandatory)]$Snapshot)
    if ([string]::IsNullOrWhiteSpace($ReceiptDir))
    { return
    }
    New-Item -ItemType Directory -Path $ReceiptDir -Force | Out-Null
    ConvertTo-SnapshotJson -Snapshot $Snapshot | Set-Content -LiteralPath (Join-Path $ReceiptDir 'promotion.json') -Encoding utf8NoBOM
}

function New-CheckOutput
{
    param([Parameter(Mandatory)]$Snapshot)
    return [ordered]@{
        title = 'Latest promotion journal'
        summary = "$([string]$Snapshot.phase)/$([string]$Snapshot.outcome)"
        text = ConvertTo-SnapshotJson -Snapshot $Snapshot
    }
}

function Test-DesiredRunState
{
    param([Parameter(Mandatory)]$Run, [Parameter(Mandatory)][string]$Status, [string]$Conclusion, [Parameter(Mandatory)]$Output)
    if ([string]$Run.status -cne $Status)
    { return $false
    }
    if ($Status -ceq 'completed' -and [string]$Run.conclusion -cne $Conclusion)
    { return $false
    }
    return [string]$Run.output.title -ceq [string]$Output.title -and [string]$Run.output.summary -ceq [string]$Output.summary -and [string]$Run.output.text -ceq [string]$Output.text
}

function Invoke-JournalPatch
{
    param([Parameter(Mandatory)][string]$JournalId, [Parameter(Mandatory)]$Snapshot, [string]$Conclusion = $null)
    Assert-Snapshot -Snapshot $Snapshot | Out-Null
    $status = if ([string]::IsNullOrWhiteSpace($Conclusion))
    { 'in_progress'
    } else
    { 'completed'
    }
    $output = New-CheckOutput -Snapshot $Snapshot
    $current = Get-Journal -JournalId $JournalId
    if ([string]$current.status -ceq 'completed')
    {
        if (Test-DesiredRunState -Run $current -Status $status -Conclusion $Conclusion -Output $output)
        { Write-LocalSnapshot -Snapshot $Snapshot; return $current
        }
        throw 'latest-promotion journal is already completed with a contradictory result'
    }
    if ([string]$current.status -cne 'in_progress')
    { throw 'latest-promotion journal has an unexpected status'
    }
    if ($status -ceq 'in_progress' -and (Test-DesiredRunState -Run $current -Status $status -Conclusion $Conclusion -Output $output))
    { Write-LocalSnapshot -Snapshot $Snapshot; return $current
    }

    $body = [ordered]@{ status = $status; output = $output }
    if ($status -ceq 'completed')
    { $body.conclusion = $Conclusion
    }
    if ([string]::IsNullOrWhiteSpace($ReceiptDir))
    { $requestPath = Join-Path $env:RUNNER_TEMP "latest-promotion-patch-$RunId-$RunAttempt.json"
    } else
    { $requestPath = Join-Path $ReceiptDir 'journal-update-request.json'
    }
    New-Item -ItemType Directory -Path ([System.IO.Path]::GetDirectoryName($requestPath)) -Force | Out-Null
    $body | ConvertTo-Json -Compress -Depth 12 | Set-Content -LiteralPath $requestPath -Encoding utf8NoBOM

    for ($attempt = 1; $attempt -le 2; $attempt++)
    {
        $response = $null
        $responseValid = $false
        $lines = @(gh api --method PATCH -H 'Accept: application/vnd.github+json' -H "X-GitHub-Api-Version: $script:ApiVersion" "repos/$RepositoryName/check-runs/$JournalId" --input $requestPath)
        if ($LASTEXITCODE -eq 0)
        {
            try
            {
                $response = ($lines -join "`n") | ConvertFrom-Json
                $responseValid = [string]$response.id -ceq $JournalId -and (Test-RunIdentity -Run $response) -and (Test-DesiredRunState -Run $response -Status $status -Conclusion $Conclusion -Output $output)
            } catch
            { $responseValid = $false
            }
        }
        if ($responseValid)
        { Write-LocalSnapshot -Snapshot $Snapshot; return $response
        }

        try
        { $observed = Get-Journal -JournalId $JournalId
        } catch
        {
            if ($attempt -eq 2)
            { throw
            }
            $observed = Get-Journal -JournalId $JournalId
        }
        if ([string]$observed.status -ceq 'completed')
        {
            if (Test-DesiredRunState -Run $observed -Status $status -Conclusion $Conclusion -Output $output)
            { Write-LocalSnapshot -Snapshot $Snapshot; return $observed
            }
            throw 'latest-promotion ambiguous PATCH observed a contradictory completed journal'
        }
        if (Test-DesiredRunState -Run $observed -Status $status -Conclusion $Conclusion -Output $output)
        { Write-LocalSnapshot -Snapshot $Snapshot; return $observed
        }
        if ($attempt -eq 2)
        { throw 'latest-promotion journal PATCH could not be reconciled'
        }
    }
}

function Get-Sources
{
    if ([string]::IsNullOrWhiteSpace($ReceiptDir))
    { return @()
    }
    $path = Join-Path $ReceiptDir 'sources.json'
    if (-not (Test-Path -LiteralPath $path))
    { return @()
    }
    $sources = Get-Content -LiteralPath $path -Raw | ConvertFrom-Json
    return @($sources.images)
}

function Ensure-Journal
{
    $existing = Find-Journal
    if ($null -ne $existing)
    { return [string]$existing.id
    }

    $sources = Get-Sources
    $snapshot = New-Snapshot -Sources $sources
    $body = [ordered]@{
        name = $script:JournalName
        head_sha = $HeadSha
        status = 'in_progress'
        external_id = Get-ExternalId
        details_url = Get-DetailsUrl
        output = New-CheckOutput -Snapshot $snapshot
    }
    if ([string]::IsNullOrWhiteSpace($ReceiptDir))
    { $requestPath = Join-Path $env:RUNNER_TEMP "latest-promotion-create-$RunId-$RunAttempt.json"
    } else
    { $requestPath = Join-Path $ReceiptDir 'journal-create-request.json'
    }
    New-Item -ItemType Directory -Path ([System.IO.Path]::GetDirectoryName($requestPath)) -Force | Out-Null
    $body | ConvertTo-Json -Compress -Depth 12 | Set-Content -LiteralPath $requestPath -Encoding utf8NoBOM

    $lines = @(gh api --method POST -H 'Accept: application/vnd.github+json' -H "X-GitHub-Api-Version: $script:ApiVersion" "repos/$RepositoryName/check-runs" --input $requestPath)
    $response = $null
    $valid = $false
    if ($LASTEXITCODE -eq 0)
    {
        try
        {
            $response = ($lines -join "`n") | ConvertFrom-Json
            $valid = [string]$response.id -match '^[1-9][0-9]*$' -and (Test-RunIdentity -Run $response) -and (Test-DesiredRunState -Run $response -Status 'in_progress' -Conclusion $null -Output $body.output)
        } catch
        { $valid = $false
        }
    }
    if ($valid)
    { Write-LocalSnapshot -Snapshot $snapshot; return [string]$response.id
    }

    $recovered = Find-Journal
    if ($null -eq $recovered)
    { throw 'latest-promotion journal creation was ambiguous and discovery found no owned check run'
    }
    Read-Snapshot -Run $recovered | Out-Null
    Write-LocalSnapshot -Snapshot $snapshot
    return [string]$recovered.id
}

function Get-TagIdentity
{
    param([Parameter(Mandatory)][string]$Reference, [switch]$ReadbackError)
    $output = @(docker buildx imagetools inspect $Reference --format '{{json .Manifest}}' 2>&1)
    if ($LASTEXITCODE -ne 0)
    {
        $detail = ($output | ForEach-Object { $_.ToString() }) -join "`n"
        if (-not $ReadbackError -and $detail -match '(?i)(\b404\b|manifest unknown|name unknown)')
        { return New-Identity -State 'absent'
        }
        if ($ReadbackError)
        { return [ordered]@{ state = 'readback_error'; immutable_reference = $null; manifest_digest = $null; failure = $detail }
        }
        throw "could not inspect manifest for ${Reference}: $detail"
    }
    try
    { $manifest = $output | ConvertFrom-Json
    } catch
    {
        if ($ReadbackError)
        { return [ordered]@{ state = 'readback_error'; immutable_reference = $null; manifest_digest = $null; failure = 'invalid manifest JSON' }
        }
        throw
    }
    $digest = [string]$manifest.digest
    if ($digest -notmatch '^sha256:[0-9a-f]{64}$')
    {
        if ($ReadbackError)
        { return [ordered]@{ state = 'readback_error'; immutable_reference = $null; manifest_digest = $null; failure = 'invalid manifest digest' }
        }
        throw "image does not expose an immutable manifest digest: $Reference"
    }
    $repository = $Reference.Substring(0, $Reference.LastIndexOf(':'))
    return New-Identity -State 'present' -ImmutableReference "$repository@$digest" -ManifestDigest $digest
}

function Test-SameIdentity
{
    param([Parameter(Mandatory)]$Left, [Parameter(Mandatory)]$Right)
    return [string]$Left.state -ceq [string]$Right.state -and [string]$Left.immutable_reference -ceq [string]$Right.immutable_reference -and [string]$Left.manifest_digest -ceq [string]$Right.manifest_digest
}

function Assert-TargetStateCoherence
{
    param([Parameter(Mandatory)]$Target)
    if ([string]$Target.intended.state -cne 'present')
    { throw "target lacks a typed intended identity: $($Target.reference)"
    }
    switch -CaseSensitive ([string]$Target.state)
    {
        'unknown'
        { if ([string]$Target.previous.state -cne 'unknown' -or [string]$Target.observed.state -cne 'unknown')
            { throw "unknown target state is contradictory: $($Target.reference)"
            }
        }
        'captured'
        { if ([string]$Target.previous.state -notin @('present','absent') -or -not (Test-SameIdentity -Left $Target.observed -Right $Target.previous))
            { throw "captured target state is contradictory: $($Target.reference)"
            }
        }
        'mutation_pending'
        { if ([string]$Target.previous.state -cne 'present' -or -not (Test-SameIdentity -Left $Target.observed -Right $Target.previous))
            { throw "mutation-pending target state is contradictory: $($Target.reference)"
            }
        }
        'updated'
        { if ([string]$Target.previous.state -cne 'present' -or -not (Test-SameIdentity -Left $Target.observed -Right $Target.intended))
            { throw "updated target state is contradictory: $($Target.reference)"
            }
        }
        'rollback_pending'
        { if ([string]$Target.previous.state -cne 'present' -or -not (Test-SameIdentity -Left $Target.observed -Right $Target.intended))
            { throw "rollback-pending target state is contradictory: $($Target.reference)"
            }
        }
        'restored'
        { if ([string]$Target.previous.state -cne 'present' -or -not (Test-SameIdentity -Left $Target.observed -Right $Target.previous))
            { throw "restored target state is contradictory: $($Target.reference)"
            }
        }
        'rollback_failed'
        { if ([string]$Target.previous.state -cne 'present' -or [string]$Target.observed.state -cne 'present')
            { throw "rollback-failed target state is contradictory: $($Target.reference)"
            }
        }
        'readback_error'
        { if ([string]$Target.observed.state -cne 'readback_error')
            { throw "readback-error target state is contradictory: $($Target.reference)"
            }
        }
        'unexpected'
        { if ([string]$Target.observed.state -cne 'present' -or (Test-SameIdentity -Left $Target.observed -Right $Target.previous) -or (Test-SameIdentity -Left $Target.observed -Right $Target.intended))
            { throw "unexpected target state is contradictory: $($Target.reference)"
            }
        }
        default
        { throw "unsupported target state: $([string]$Target.state)"
        }
    }
}

function Assert-NoTargetFailures
{
    param([Parameter(Mandatory)][object[]]$Targets)
    if (@($Targets | Where-Object { -not [string]::IsNullOrWhiteSpace([string]$_.failure) }).Count -ne 0)
    { throw 'snapshot has unexpected target failure metadata'
    }
}

function Assert-UntouchedRevalidation
{
    param([Parameter(Mandatory)]$Snapshot)
    if ([string]$Snapshot.revalidation.status -cne 'not_started' -or $null -ne $Snapshot.revalidation.observed_release_tag -or $null -ne $Snapshot.revalidation.observed_source_commit -or $null -ne $Snapshot.revalidation.failure)
    { throw 'snapshot has contradictory untouched revalidation metadata'
    }
}

function Assert-PassedRevalidation
{
    param([Parameter(Mandatory)]$Snapshot)
    if ([string]$Snapshot.revalidation.status -cne 'passed' -or [string]$Snapshot.revalidation.observed_release_tag -cne [string]$Snapshot.release.tag -or [string]$Snapshot.revalidation.observed_source_commit -cne [string]$Snapshot.release.source_commit -or $null -ne $Snapshot.revalidation.failure)
    { throw 'snapshot has contradictory passed revalidation metadata'
    }
}

function Assert-RollbackMetadata
{
    param([Parameter(Mandatory)]$Snapshot, [Parameter(Mandatory)][bool]$Attempted, [Parameter(Mandatory)][string]$Outcome, [string[]]$FailureStates = @())
    $failures = @($Snapshot.rollback.failures)
    $failureStateList = @($FailureStates)
    if ([bool]$Snapshot.rollback.attempted -ne $Attempted -or [string]$Snapshot.rollback.outcome -cne $Outcome)
    { throw 'snapshot has contradictory rollback metadata'
    }
    if ($failureStateList.Count -eq 0)
    { $failedTargets = @()
    } elseif ($failureStateList -ccontains '*')
    { $failedTargets = @($Snapshot.targets | Where-Object { -not [string]::IsNullOrWhiteSpace([string]$_.failure) })
    } else
    { $failedTargets = @($Snapshot.targets | Where-Object { $failureStateList -ccontains [string]$_.state })
    }
    if ($failures.Count -ne $failedTargets.Count)
    { throw 'snapshot rollback failure count is contradictory'
    }
    foreach ($failure in $failures)
    {
        if ([string]::IsNullOrWhiteSpace([string]$failure.target) -or [string]::IsNullOrWhiteSpace([string]$failure.failure))
        { throw 'snapshot rollback failure entry is incomplete'
        }
        $matches = @($failedTargets | Where-Object { [string]$_.reference -ceq [string]$failure.target -and [string]$_.failure -ceq [string]$failure.failure })
        if ($matches.Count -ne 1)
        { throw 'snapshot rollback failure entry does not match a failed target'
        }
    }
}

function Assert-InterstitialSnapshotInvariant
{
    param([Parameter(Mandatory)]$Snapshot, [Parameter(Mandatory)][string]$Key)
    $targets = @($Snapshot.targets)
    foreach ($target in $targets)
    { Assert-TargetStateCoherence -Target $target
    }
    if (-not [string]::IsNullOrWhiteSpace([string]$Snapshot.failure))
    { throw 'interstitial snapshot has terminal failure metadata'
    }
    $states = (@($targets | ForEach-Object { [string]$_.state }) -join ',') + ','
    switch -CaseSensitive ($Key)
    {
        'pre_promotion/pending'
        {
            if ($states -cne 'unknown,unknown,unknown,')
            { throw 'pre-promotion snapshot is contradictory'
            }
            Assert-UntouchedRevalidation -Snapshot $Snapshot
            Assert-RollbackMetadata -Snapshot $Snapshot -Attempted $false -Outcome 'not_needed'
            Assert-NoTargetFailures -Targets $targets
        }
        'capturing_previous_latest/pending'
        {
            if ($states -notmatch '^(captured,)*(unknown,)*$')
            { throw 'capturing snapshot is contradictory'
            }
            Assert-UntouchedRevalidation -Snapshot $Snapshot
            Assert-RollbackMetadata -Snapshot $Snapshot -Attempted $false -Outcome 'not_needed'
            Assert-NoTargetFailures -Targets $targets
        }
        'writing_latest/pending'
        {
            if ($states -notmatch '^(updated,)*(mutation_pending,)?(captured,)*$')
            { throw 'writing-latest snapshot is contradictory'
            }
            Assert-UntouchedRevalidation -Snapshot $Snapshot
            Assert-RollbackMetadata -Snapshot $Snapshot -Attempted $false -Outcome 'not_needed'
            Assert-NoTargetFailures -Targets $targets
        }
        'post_write_release_revalidation/pending'
        {
            if ($states -cne 'updated,updated,updated,' -or [string]$Snapshot.revalidation.status -notin @('started','mismatch','error'))
            { throw 'post-write revalidation snapshot is contradictory'
            }
            if ([string]$Snapshot.revalidation.status -ceq 'started' -and $null -ne $Snapshot.revalidation.failure)
            { throw 'started revalidation has failure metadata'
            }
            if ([string]$Snapshot.revalidation.status -in @('mismatch','error') -and [string]::IsNullOrWhiteSpace([string]$Snapshot.revalidation.failure))
            { throw 'failed revalidation lacks failure metadata'
            }
            Assert-RollbackMetadata -Snapshot $Snapshot -Attempted $false -Outcome 'not_needed'
            Assert-NoTargetFailures -Targets $targets
        }
        'rolling_back/pending'
        {
            if ($states -notmatch '^((restored|rollback_failed|readback_error|rollback_pending|updated),){3}$')
            { throw 'rolling-back snapshot is contradictory'
            }
            Assert-PassedRevalidation -Snapshot $Snapshot
            Assert-RollbackMetadata -Snapshot $Snapshot -Attempted $true -Outcome 'pending' -FailureStates @('rollback_failed','readback_error')
            foreach ($target in $targets | Where-Object { [string]$_.state -notin @('rollback_failed','readback_error') })
            { if (-not [string]::IsNullOrWhiteSpace([string]$target.failure))
                { throw 'rolling-back snapshot has failure on a non-failed target'
                }
            }
        }
        default
        { throw "unsupported interstitial snapshot: $Key"
        }
    }
}

function Assert-TerminalSnapshotInvariant
{
    param([Parameter(Mandatory)]$Snapshot, [Parameter(Mandatory)][string]$Key)
    $targets = @($Snapshot.targets)
    $states = (@($targets | ForEach-Object { [string]$_.state }) -join ',') + ','
    if ($Key -ceq 'failed/contradiction')
    {
        if ([string]::IsNullOrWhiteSpace([string]$Snapshot.failure) -or $states -cne 'unknown,unknown,unknown,' -or $null -eq $Snapshot.PSObject.Properties['contradicted_snapshot'])
        { throw 'contradiction snapshot is contradictory'
        }
        Assert-UntouchedRevalidation -Snapshot $Snapshot
        Assert-RollbackMetadata -Snapshot $Snapshot -Attempted $false -Outcome 'not_attempted'
        Assert-NoTargetFailures -Targets $targets
        return
    }
    foreach ($target in $targets)
    { Assert-TargetStateCoherence -Target $target
    }
    switch -CaseSensitive ($Key)
    {
        'completed/success'
        {
            if ($null -ne $Snapshot.failure -or $states -cne 'updated,updated,updated,')
            { throw 'completed success snapshot is contradictory'
            }
            Assert-PassedRevalidation -Snapshot $Snapshot
            Assert-RollbackMetadata -Snapshot $Snapshot -Attempted $false -Outcome 'not_needed'
            Assert-NoTargetFailures -Targets $targets
        }
        'failed/bootstrap_required_no_write'
        {
            if ([string]::IsNullOrWhiteSpace([string]$Snapshot.failure) -or $states -cne 'captured,captured,captured,' -or @($targets | Where-Object { [string]$_.previous.state -ceq 'absent' }).Count -eq 0)
            { throw 'bootstrap-required snapshot is contradictory'
            }
            Assert-UntouchedRevalidation -Snapshot $Snapshot
            Assert-RollbackMetadata -Snapshot $Snapshot -Attempted $false -Outcome 'not_needed'
            Assert-NoTargetFailures -Targets $targets
        }
        'failed/failed_before_write'
        {
            if ([string]::IsNullOrWhiteSpace([string]$Snapshot.failure) -or $states -cne 'unknown,unknown,unknown,')
            { throw 'failed-before-write snapshot is contradictory'
            }
            Assert-UntouchedRevalidation -Snapshot $Snapshot
            Assert-RollbackMetadata -Snapshot $Snapshot -Attempted $false -Outcome 'not_needed'
            Assert-NoTargetFailures -Targets $targets
        }
        'failed/rolled_back'
        {
            if ([string]::IsNullOrWhiteSpace([string]$Snapshot.failure) -or $states -cne 'restored,restored,restored,')
            { throw 'rolled-back snapshot is contradictory'
            }
            Assert-PassedRevalidation -Snapshot $Snapshot
            Assert-RollbackMetadata -Snapshot $Snapshot -Attempted $true -Outcome 'succeeded'
            Assert-NoTargetFailures -Targets $targets
        }
        'failed/rollback_failed'
        {
            if ([string]::IsNullOrWhiteSpace([string]$Snapshot.failure) -or $states -notmatch '^((restored|rollback_failed|readback_error),){3}$' -or @($targets | Where-Object { [string]$_.state -ceq 'rollback_failed' }).Count -eq 0)
            { throw 'rollback-failed snapshot is contradictory'
            }
            Assert-PassedRevalidation -Snapshot $Snapshot
            Assert-RollbackMetadata -Snapshot $Snapshot -Attempted $true -Outcome 'failed' -FailureStates @('rollback_failed','readback_error')
        }
        'failed/rollback_readback_error'
        {
            if ([string]::IsNullOrWhiteSpace([string]$Snapshot.failure) -or $states -notmatch '^((restored|readback_error),){3}$' -or @($targets | Where-Object { [string]$_.state -ceq 'readback_error' }).Count -eq 0)
            { throw 'rollback-readback-error snapshot is contradictory'
            }
            Assert-PassedRevalidation -Snapshot $Snapshot
            Assert-RollbackMetadata -Snapshot $Snapshot -Attempted $true -Outcome 'readback_error' -FailureStates @('readback_error')
        }
        'failed/action_required'
        {
            if ([string]::IsNullOrWhiteSpace([string]$Snapshot.failure) -or [string]$Snapshot.revalidation.status -notin @('passed','mismatch','error'))
            { throw 'action-required snapshot is contradictory'
            }
            if ([string]$Snapshot.revalidation.status -ceq 'passed')
            { Assert-PassedRevalidation -Snapshot $Snapshot
            } else
            {
                if ([string]::IsNullOrWhiteSpace([string]$Snapshot.revalidation.failure))
                { throw 'action-required revalidation lacks failure metadata'
                }
                if ([string]$Snapshot.revalidation.status -ceq 'mismatch' -and [string]$Snapshot.revalidation.observed_release_tag -ceq [string]$Snapshot.release.tag -and [string]$Snapshot.revalidation.observed_source_commit -ceq [string]$Snapshot.release.source_commit)
                { throw 'action-required mismatch did not observe a changed release identity'
                }
            }
            if ([bool]$Snapshot.rollback.attempted)
            { Assert-RollbackMetadata -Snapshot $Snapshot -Attempted $true -Outcome 'action_required' -FailureStates @('*')
            } else
            {
                Assert-RollbackMetadata -Snapshot $Snapshot -Attempted $false -Outcome 'not_attempted'
                Assert-NoTargetFailures -Targets $targets
                $producerState = $states -match '^(captured,)*(unknown,)*$' -or $states -match '^(restored,)*(unknown,)+$' -or ($states -match '^(updated,)*(mutation_pending,)?(captured,)*$' -and $states -cne 'updated,updated,updated,')
                $classificationFailure = @($targets | Where-Object { [string]$_.state -in @('unexpected','readback_error') }).Count -gt 0
                if ([string]$Snapshot.revalidation.status -ceq 'passed' -and -not $producerState -and -not $classificationFailure)
                { throw "action-required no-rollback target states are contradictory: states=$states"
                }
            }
        }
        default
        { throw "unsupported terminal snapshot: $Key"
        }
    }
}

function Get-OfficialRelease
{
    $tagLines = @(gh api "repos/$RepositoryName/releases/latest" --jq .tag_name)
    if ($LASTEXITCODE -ne 0)
    { throw 'could not read the latest GitHub Release tag'
    }
    $tag = ([string]($tagLines -join "`n")).Trim()
    if ($tag -notmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$')
    { throw 'latest GitHub Release tag is invalid'
    }
    $commitLines = @(gh api "repos/$RepositoryName/commits/$tag" --jq .sha)
    if ($LASTEXITCODE -ne 0)
    { throw 'could not resolve the latest GitHub Release commit'
    }
    $commit = ([string]($commitLines -join "`n")).Trim()
    if ($commit -notmatch '^[0-9a-f]{40}$')
    { throw 'latest GitHub Release commit is invalid'
    }
    return [ordered]@{ tag = $tag; source_commit = $commit }
}

function Set-TerminalJournal
{
    param([Parameter(Mandatory)][string]$JournalId, [Parameter(Mandatory)]$Snapshot, [Parameter(Mandatory)][string]$Conclusion)
    Invoke-JournalPatch -JournalId $JournalId -Snapshot $Snapshot -Conclusion $Conclusion | Out-Null
}

function New-ContradictionSnapshot
{
    param([Parameter(Mandatory)][string]$Failure, [string]$ContradictedText)
    $snapshot = New-Snapshot -Phase 'failed' -Outcome 'contradiction'
    $snapshot.failure = $Failure
    $snapshot.contradicted_snapshot = $ContradictedText
    $snapshot.rollback.outcome = 'not_attempted'
    return $snapshot
}

function Complete-Contradiction
{
    param([Parameter(Mandatory)]$Run, [Parameter(Mandatory)][string]$Failure)
    if ([string]$Run.status -ceq 'completed')
    { throw "completed latest-promotion journal is contradictory: $Failure"
    }
    $snapshot = New-ContradictionSnapshot -Failure $Failure -ContradictedText ([string]$Run.output.text)
    Set-TerminalJournal -JournalId ([string]$Run.id) -Snapshot $snapshot -Conclusion 'failure'
    return $snapshot
}

function Get-TerminalConclusion
{
    param([Parameter(Mandatory)]$Snapshot)
    $key = "$([string]$Snapshot.phase)/$([string]$Snapshot.outcome)"
    switch -CaseSensitive ($key)
    {
        'completed/success'
        { Assert-TerminalSnapshotInvariant -Snapshot $Snapshot -Key $key; return 'success'
        }
        'failed/bootstrap_required_no_write'
        { Assert-TerminalSnapshotInvariant -Snapshot $Snapshot -Key $key; return 'neutral'
        }
        'failed/rolled_back'
        { Assert-TerminalSnapshotInvariant -Snapshot $Snapshot -Key $key; return 'neutral'
        }
        'failed/failed_before_write'
        { Assert-TerminalSnapshotInvariant -Snapshot $Snapshot -Key $key; return 'failure'
        }
        'failed/rollback_failed'
        { Assert-TerminalSnapshotInvariant -Snapshot $Snapshot -Key $key; return 'failure'
        }
        'failed/rollback_readback_error'
        { Assert-TerminalSnapshotInvariant -Snapshot $Snapshot -Key $key; return 'failure'
        }
        'failed/contradiction'
        { Assert-TerminalSnapshotInvariant -Snapshot $Snapshot -Key $key; return 'failure'
        }
        'failed/action_required'
        { Assert-TerminalSnapshotInvariant -Snapshot $Snapshot -Key $key; return 'failure'
        }
        default
        { return $null
        }
    }
}

function Complete-ActionRequired
{
    param([Parameter(Mandatory)]$Run, [Parameter(Mandatory)]$Snapshot, [Parameter(Mandatory)][string]$Failure, [Parameter(Mandatory)][bool]$RollbackAttempted)
    $Snapshot.phase = 'failed'
    $Snapshot.outcome = 'action_required'
    $Snapshot.failure = $Failure
    if ($RollbackAttempted)
    {
        $failedTargets = @($Snapshot.targets | Where-Object { -not [string]::IsNullOrWhiteSpace([string]$_.failure) })
        if ($failedTargets.Count -eq 0)
        {
            $target = @($Snapshot.targets | Where-Object { [string]$_.state -cne 'restored' } | Select-Object -First 1)
            if ($target.Count -eq 0)
            { $target = @($Snapshot.targets | Select-Object -First 1)
            }
            $target[0].failure = $Failure
            $failedTargets = @($target[0])
        }
        $Snapshot.rollback.attempted = $true
        $Snapshot.rollback.outcome = 'action_required'
        $Snapshot.rollback.failures = @($failedTargets | ForEach-Object { [ordered]@{ target = [string]$_.reference; failure = [string]$_.failure } })
    } else
    {
        foreach ($target in @($Snapshot.targets))
        { $target.failure = $null
        }
        $Snapshot.rollback.attempted = $false
        $Snapshot.rollback.outcome = 'not_attempted'
        $Snapshot.rollback.failures = @()
    }
    Set-TerminalJournal -JournalId ([string]$Run.id) -Snapshot $Snapshot -Conclusion 'failure'
    return Get-Journal -JournalId ([string]$Run.id)
}

function Invoke-Reconcile
{
    $run = Find-Journal
    if ($null -eq $run)
    {
        if ($AllowMissingJournal)
        { return $null
        }
        throw 'latest-promotion journal was not found'
    }
    if ([string]$run.status -ceq 'completed')
    {
        try
        {
            $completedSnapshot = Read-Snapshot -Run $run
            $completedConclusion = Get-TerminalConclusion -Snapshot $completedSnapshot
            if ($null -eq $completedConclusion -or -not (Test-DesiredRunState -Run $run -Status 'completed' -Conclusion $completedConclusion -Output (New-CheckOutput -Snapshot $completedSnapshot)))
            { throw 'completed result does not match its canonical terminal snapshot'
            }
        } catch
        { throw "completed latest-promotion journal is contradictory: $($_.Exception.Message)"
        }
        return $run
    }
    if ([string]$run.status -cne 'in_progress')
    { throw 'latest-promotion journal has an unexpected status'
    }

    try
    {
        $snapshot = Read-Snapshot -Run $run
        $terminalConclusion = Get-TerminalConclusion -Snapshot $snapshot
    } catch
    { return Complete-Contradiction -Run $run -Failure $_.Exception.Message
    }
    if ($null -ne $terminalConclusion)
    {
        Set-TerminalJournal -JournalId ([string]$run.id) -Snapshot $snapshot -Conclusion $terminalConclusion
        return Get-Journal -JournalId ([string]$run.id)
    }

    $validInterstitial = @('pre_promotion/pending','capturing_previous_latest/pending','writing_latest/pending','post_write_release_revalidation/pending','rolling_back/pending')
    $key = "$([string]$snapshot.phase)/$([string]$snapshot.outcome)"
    if ($validInterstitial -cnotcontains $key)
    { return Complete-Contradiction -Run $run -Failure "unsupported phase/outcome: $key"
    }
    try
    { Assert-InterstitialSnapshotInvariant -Snapshot $snapshot -Key $key
    } catch
    { return Complete-Contradiction -Run $run -Failure $_.Exception.Message
    }
    if ($key -ceq 'pre_promotion/pending')
    {
        $snapshot.phase = 'failed'; $snapshot.outcome = 'failed_before_write'; $snapshot.failure = 'promotion did not begin'
        Set-TerminalJournal -JournalId ([string]$run.id) -Snapshot $snapshot -Conclusion 'failure'
        return Get-Journal -JournalId ([string]$run.id)
    }

    $rollbackAlreadyAttempted = [bool]$snapshot.rollback.attempted
    try
    { $release = Get-OfficialRelease
    } catch
    {
        $snapshot.revalidation.status = 'error'
        $snapshot.revalidation.failure = $_.Exception.Message
        return Complete-ActionRequired -Run $run -Snapshot $snapshot -Failure $snapshot.revalidation.failure -RollbackAttempted $rollbackAlreadyAttempted
    }
    $snapshot.revalidation.observed_release_tag = [string]$release.tag
    $snapshot.revalidation.observed_source_commit = [string]$release.source_commit
    if ([string]$release.source_commit -cne [string]$snapshot.release.source_commit -or [string]$release.tag -cne [string]$snapshot.release.tag)
    {
        $snapshot.revalidation.status = 'mismatch'
        $snapshot.revalidation.failure = 'official release identity changed; registry mutation is forbidden'
        return Complete-ActionRequired -Run $run -Snapshot $snapshot -Failure $snapshot.revalidation.failure -RollbackAttempted $rollbackAlreadyAttempted
    }
    $snapshot.revalidation.status = 'passed'
    $snapshot.revalidation.failure = $null

    foreach ($target in @($snapshot.targets))
    {
        $target.failure = $null
        if ([string]$target.previous.state -cne 'present' -or [string]$target.intended.state -cne 'present')
        {
            $failure = "target lacks recoverable previous/intended identity: $($target.reference)"
            if ($rollbackAlreadyAttempted)
            { $target.failure = $failure
            }
            return Complete-ActionRequired -Run $run -Snapshot $snapshot -Failure $failure -RollbackAttempted $rollbackAlreadyAttempted
        }
        $target.observed = Get-TagIdentity -Reference ([string]$target.reference) -ReadbackError
        if ([string]$target.observed.state -ceq 'readback_error')
        { $target.state = 'readback_error'
        } elseif ([string]$target.observed.manifest_digest -ceq [string]$target.previous.manifest_digest)
        { $target.state = 'restored'
        } elseif ([string]$target.observed.manifest_digest -ceq [string]$target.intended.manifest_digest)
        { $target.state = 'updated'
        } else
        { $target.state = 'unexpected'
        }
    }

    $readbackErrors = @($snapshot.targets | Where-Object { [string]$_.state -ceq 'readback_error' })
    $unexpected = @($snapshot.targets | Where-Object { [string]$_.state -ceq 'unexpected' })
    if ($readbackErrors.Count -gt 0 -or $unexpected.Count -gt 0)
    {
        $failedTarget = if ($unexpected.Count -gt 0)
        { $unexpected[0]
        } else
        { $readbackErrors[0]
        }
        $failure = if ($unexpected.Count -gt 0)
        { "unexpected current tag identity: $($failedTarget.reference)"
        } else
        { "could not read current tag identity: $($failedTarget.reference)"
        }
        if ($rollbackAlreadyAttempted)
        { $failedTarget.failure = $failure
        }
        return Complete-ActionRequired -Run $run -Snapshot $snapshot -Failure $failure -RollbackAttempted $rollbackAlreadyAttempted
    }

    foreach ($target in @($snapshot.targets))
    { $target.failure = $null
    }
    $snapshot.phase = 'rolling_back'; $snapshot.outcome = 'pending'; $snapshot.failure = $null
    $snapshot.rollback.attempted = $true; $snapshot.rollback.outcome = 'pending'; $snapshot.rollback.failures = @()
    Invoke-JournalPatch -JournalId ([string]$run.id) -Snapshot $snapshot | Out-Null
    $commandFailures = [System.Collections.Generic.List[object]]::new()
    $rollbackReadbackErrors = [System.Collections.Generic.List[object]]::new()
    $safetyFailures = [System.Collections.Generic.List[object]]::new()
    foreach ($target in @($snapshot.targets | Where-Object { [string]$_.state -ceq 'updated' }))
    {
        $target.state = 'rollback_pending'
        Invoke-JournalPatch -JournalId ([string]$run.id) -Snapshot $snapshot | Out-Null
        try
        { $currentRelease = Get-OfficialRelease
        } catch
        {
            $snapshot.revalidation.status = 'error'; $snapshot.revalidation.failure = $_.Exception.Message
            $target.state = 'updated'; $target.failure = $snapshot.revalidation.failure
            $safetyFailures.Add([ordered]@{ target = [string]$target.reference; failure = [string]$target.failure })
            break
        }
        $snapshot.revalidation.observed_release_tag = [string]$currentRelease.tag
        $snapshot.revalidation.observed_source_commit = [string]$currentRelease.source_commit
        if ([string]$currentRelease.source_commit -cne [string]$snapshot.release.source_commit -or [string]$currentRelease.tag -cne [string]$snapshot.release.tag)
        {
            $snapshot.revalidation.status = 'mismatch'; $snapshot.revalidation.failure = 'official release identity changed immediately before rollback'
            $target.state = 'updated'; $target.failure = $snapshot.revalidation.failure
            $safetyFailures.Add([ordered]@{ target = [string]$target.reference; failure = [string]$target.failure })
            break
        }
        $snapshot.revalidation.status = 'passed'; $snapshot.revalidation.failure = $null
        $currentIdentity = Get-TagIdentity -Reference ([string]$target.reference) -ReadbackError
        $target.observed = $currentIdentity
        if ([string]$currentIdentity.state -ceq 'readback_error')
        {
            $target.state = 'readback_error'; $target.failure = 'could not verify current tag identity immediately before rollback'
            $safetyFailures.Add([ordered]@{ target = [string]$target.reference; failure = [string]$target.failure })
            break
        }
        if (Test-SameIdentity -Left $currentIdentity -Right $target.previous)
        {
            $target.state = 'restored'; $target.failure = $null
            Invoke-JournalPatch -JournalId ([string]$run.id) -Snapshot $snapshot | Out-Null
            continue
        }
        if (-not (Test-SameIdentity -Left $currentIdentity -Right $target.intended))
        {
            $target.state = 'unexpected'; $target.failure = 'current tag no longer matches the intended identity immediately before rollback'
            $safetyFailures.Add([ordered]@{ target = [string]$target.reference; failure = [string]$target.failure })
            break
        }
        docker buildx imagetools create --prefer-index=false --tag ([string]$target.reference) ([string]$target.previous.immutable_reference)
        if ($LASTEXITCODE -ne 0)
        {
            $target.state = 'rollback_failed'; $target.failure = 'rollback command failed'
            $commandFailures.Add([ordered]@{ target = [string]$target.reference; failure = [string]$target.failure })
            $snapshot.rollback.failures = @($commandFailures.ToArray() + $rollbackReadbackErrors.ToArray())
            Invoke-JournalPatch -JournalId ([string]$run.id) -Snapshot $snapshot | Out-Null
            continue
        }
        $target.observed = Get-TagIdentity -Reference ([string]$target.reference) -ReadbackError
        if ([string]$target.observed.state -ceq 'readback_error')
        {
            $target.state = 'readback_error'; $target.failure = [string]$target.observed.failure
            $rollbackReadbackErrors.Add([ordered]@{ target = [string]$target.reference; failure = [string]$target.failure })
        } elseif ([string]$target.observed.manifest_digest -cne [string]$target.previous.manifest_digest)
        {
            $target.state = 'rollback_failed'; $target.failure = 'rollback readback did not restore the previous digest'
            $commandFailures.Add([ordered]@{ target = [string]$target.reference; failure = [string]$target.failure })
        } else
        { $target.state = 'restored'; $target.failure = $null
        }
        $snapshot.rollback.failures = @($commandFailures.ToArray() + $rollbackReadbackErrors.ToArray())
        Invoke-JournalPatch -JournalId ([string]$run.id) -Snapshot $snapshot | Out-Null
    }

    $snapshot.rollback.failures = @($safetyFailures.ToArray() + $commandFailures.ToArray() + $rollbackReadbackErrors.ToArray())
    $snapshot.phase = 'failed'
    if ($safetyFailures.Count -gt 0)
    { $snapshot.outcome = 'action_required'; $snapshot.rollback.outcome = 'action_required'; $snapshot.failure = [string]$safetyFailures[0].failure; $conclusion = 'failure'
    } elseif ($commandFailures.Count -gt 0)
    { $snapshot.outcome = 'rollback_failed'; $snapshot.rollback.outcome = 'failed'; $snapshot.failure = 'one or more rollback commands failed'; $conclusion = 'failure'
    } elseif ($rollbackReadbackErrors.Count -gt 0)
    { $snapshot.outcome = 'rollback_readback_error'; $snapshot.rollback.outcome = 'readback_error'; $snapshot.failure = 'one or more rollback readbacks failed'; $conclusion = 'failure'
    } else
    { $snapshot.outcome = 'rolled_back'; $snapshot.rollback.outcome = 'succeeded'; $snapshot.failure = 'interrupted promotion was safely rolled back'; $conclusion = 'neutral'
    }
    Set-TerminalJournal -JournalId ([string]$run.id) -Snapshot $snapshot -Conclusion $conclusion
    return Get-Journal -JournalId ([string]$run.id)
}

function Invoke-Promote
{
    $run = Find-Journal
    if ($null -eq $run)
    { throw 'latest-promotion journal was not found before promotion'
    }
    if ([string]$run.status -ceq 'completed')
    { return $run
    }
    $snapshot = Read-Snapshot -Run $run
    $journalId = [string]$run.id
    try
    {
        $snapshot.phase = 'capturing_previous_latest'; $snapshot.outcome = 'pending'
        Invoke-JournalPatch -JournalId $journalId -Snapshot $snapshot | Out-Null
        foreach ($target in @($snapshot.targets))
        {
            $target.previous = Get-TagIdentity -Reference ([string]$target.reference)
            $target.observed = $target.previous
            $target.state = 'captured'
            Invoke-JournalPatch -JournalId $journalId -Snapshot $snapshot | Out-Null
        }
        $missing = @($snapshot.targets | Where-Object { [string]$_.previous.state -ceq 'absent' })
        if ($missing.Count -gt 0)
        {
            $snapshot.phase = 'failed'; $snapshot.outcome = 'bootstrap_required_no_write'; $snapshot.failure = "latest tag is absent and requires manual bootstrap: $($missing[0].reference)"
            Set-TerminalJournal -JournalId $journalId -Snapshot $snapshot -Conclusion 'neutral'
            throw $snapshot.failure
        }
        $snapshot.phase = 'writing_latest'; $snapshot.outcome = 'pending'
        Invoke-JournalPatch -JournalId $journalId -Snapshot $snapshot | Out-Null
        foreach ($target in @($snapshot.targets))
        {
            $target.state = 'mutation_pending'; $target.observed = $target.previous
            Invoke-JournalPatch -JournalId $journalId -Snapshot $snapshot | Out-Null
            docker buildx imagetools create --prefer-index=false --tag ([string]$target.reference) ([string]$target.intended.immutable_reference)
            if ($LASTEXITCODE -ne 0)
            { throw "could not promote $($target.intended.immutable_reference) to $($target.reference)"
            }
            $target.observed = Get-TagIdentity -Reference ([string]$target.reference)
            if ([string]$target.observed.manifest_digest -cne [string]$target.intended.manifest_digest)
            { throw "latest readback differs from intended identity: $($target.reference)"
            }
            $target.state = 'updated'
            Invoke-JournalPatch -JournalId $journalId -Snapshot $snapshot | Out-Null
        }
        $snapshot.phase = 'post_write_release_revalidation'; $snapshot.outcome = 'pending'; $snapshot.revalidation.status = 'started'
        Invoke-JournalPatch -JournalId $journalId -Snapshot $snapshot | Out-Null
        $observedRelease = Get-OfficialRelease
        $snapshot.revalidation.observed_release_tag = [string]$observedRelease.tag
        $snapshot.revalidation.observed_source_commit = [string]$observedRelease.source_commit
        if ([string]$observedRelease.tag -cne [string]$snapshot.release.tag -or [string]$observedRelease.source_commit -cne [string]$snapshot.release.source_commit)
        {
            $snapshot.revalidation.status = 'mismatch'; $snapshot.revalidation.failure = 'official release identity changed after latest writes'
            Invoke-JournalPatch -JournalId $journalId -Snapshot $snapshot | Out-Null
            throw $snapshot.revalidation.failure
        }
        $snapshot.revalidation.status = 'passed'
        $snapshot.phase = 'completed'; $snapshot.outcome = 'success'; $snapshot.failure = $null
        Set-TerminalJournal -JournalId $journalId -Snapshot $snapshot -Conclusion 'success'
        return Get-Journal -JournalId $journalId
    } catch
    {
        $failure = $_.Exception.Message
        $current = Get-Journal -JournalId $journalId
        if ([string]$current.status -ceq 'completed')
        { throw $failure
        }
        try
        { Invoke-Reconcile | Out-Null
        } catch
        { throw "latest promotion failed: $failure; reconciliation failed: $($_.Exception.Message)"
        }
        throw "latest promotion failed and was reconciled: $failure"
    }
}

Assert-RunIdentity
if ($Mode -ceq 'EnsureJournal' -and ([string]::IsNullOrWhiteSpace($ReleaseCommit) -or [string]::IsNullOrWhiteSpace($ReleaseTag)))
{ throw 'journal creation requires the official release tag and source commit'
}
switch ($Mode)
{
    'Discover'
    {
        $journal = Find-Journal
        if ($null -eq $journal)
        { if ($AllowMissingJournal)
            { return
            }; throw 'latest-promotion journal was not found'
        }
        Write-Output ([string]$journal.id)
    }
    'EnsureJournal'
    { Write-Output (Ensure-Journal)
    }
    'Promote'
    { Invoke-Promote | Out-Null
    }
    'Reconcile'
    { Invoke-Reconcile | Out-Null
    }
}
