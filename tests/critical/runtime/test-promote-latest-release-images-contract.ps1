param(
  [Parameter(Mandatory)][string]$WorkflowPath
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Assert-That
{
  param([Parameter(Mandatory)][bool]$Condition, [Parameter(Mandatory)][string]$Message)
  if (-not $Condition)
  { throw $Message
  }
}

function New-Digest
{
  param([Parameter(Mandatory)][char]$Character)
  return "sha256:$(''.PadLeft(64, $Character))"
}

function New-Identity
{
  param(
    [Parameter(Mandatory)][string]$Repository,
    [Parameter(Mandatory)][char]$ManifestCharacter,
    [Parameter(Mandatory)][char]$ConfigCharacter,
    [Parameter(Mandatory)][string]$Version,
    [Parameter(Mandatory)][string]$Revision
  )
  return [pscustomobject]@{
    repository = $Repository
    manifest_digest = New-Digest $ManifestCharacter
    config_digest = New-Digest $ConfigCharacter
    source = 'https://github.com/thebtf/engram'
    version = $Version
    revision = $Revision
  }
}

function Set-Scenario
{
  param(
    [string]$PostWriteReleaseTag = 'v1.2.3',
    [string]$PostWriteSourceCommit = ('1' * 40 -join ''),
    [string[]]$MissingLatest = @(),
    [string]$InspectFailureTarget = $null,
    [switch]$FailLatestReceiptWrite,
    [int]$FailPromotionStateWriteOn = 0,
    [switch]$RejectPromotionStateAfterUpdate,
    [switch]$InterruptAfterPendingState,
    [switch]$FailJournalCreate,
    [int]$FailJournalPatchOn = 0,
    [switch]$InvalidJournalCreateResponse,
    [switch]$InvalidJournalPatchResponse,
    [switch]$InterruptAfterRegistryCreate,
    [switch]$FailGHCRLogin
  )

  $script:scenarioCount++
  $script:runningTerminalizer = $false
  $script:repositories = @(
    'ghcr.io/thebtf/engram',
    'ghcr.io/thebtf/engram-operator-console',
    'ghcr.io/thebtf/engram-postgres'
  )
  $script:releaseTag = 'v1.2.3'
  $script:sourceCommit = '1' * 40 -join ''
  $script:postWriteReleaseTag = $PostWriteReleaseTag
  $script:postWriteSourceCommit = $PostWriteSourceCommit
  $script:files = @{}
  $script:tags = @{}
  $script:immutable = @{}
  $script:oldByRepository = @{}
  $script:newByRepository = @{}
  $script:createCalls = [System.Collections.Generic.List[object]]::new()
  $script:inspectFailureTarget = $InspectFailureTarget
  $script:failReadbackTarget = $null
  $script:failReadbackManifest = $null
  $script:readbackFailureInjected = $false
  $script:failCreateTarget = $null
  $script:failCreateSource = $null
  $script:failLatestReceiptWrite = [bool]$FailLatestReceiptWrite
  $script:latestReceiptWriteFailed = $false
  $script:failPromotionStateWriteOn = $FailPromotionStateWriteOn
  $script:promotionStateWrites = 0
  $script:rejectPromotionStateAfterUpdate = [bool]$RejectPromotionStateAfterUpdate
  $script:interruptAfterPendingState = [bool]$InterruptAfterPendingState
  $script:interrupted = $false
  $script:rejectPromotionStateWrites = $false
  $script:journalRuns = @{}
  $script:journalEvents = [System.Collections.Generic.List[object]]::new()
  $script:eventLog = [System.Collections.Generic.List[object]]::new()
  $script:eventSequence = 0
  $script:journalCreateCount = 0
  $script:journalPatchCount = 0
  $script:failJournalCreate = [bool]$FailJournalCreate
  $script:failJournalPatchOn = $FailJournalPatchOn
  $script:invalidJournalCreateResponse = [bool]$InvalidJournalCreateResponse
  $script:invalidJournalPatchResponse = [bool]$InvalidJournalPatchResponse
  $script:interruptAfterRegistryCreate = [bool]$InterruptAfterRegistryCreate
  $script:processLossJournal = $null
  $script:failGHCRLogin = [bool]$FailGHCRLogin

  $env:RECEIPT_DIR = 'contract-receipt'
  $env:REPOSITORY_NAME = 'thebtf/engram'
  $env:GITHUB_SERVER_URL = 'https://github.com'
  $env:GITHUB_REPOSITORY = 'thebtf/engram'
  $env:GITHUB_RUN_ID = '8675309'
  $env:GITHUB_RUN_ATTEMPT = '3'
  $env:GITHUB_ENV = 'contract-github-env'
  Remove-Item Env:LOGICAL_PROMOTION_JOURNAL_ID -ErrorAction SilentlyContinue
  Remove-Item Env:LATEST_PROMOTION_JOURNAL_ID -ErrorAction SilentlyContinue
  $sourceImages = [System.Collections.Generic.List[object]]::new()
  for ($index = 0; $index -lt $script:repositories.Count; $index++)
  {
    $repository = $script:repositories[$index]
    $old = New-Identity -Repository $repository -ManifestCharacter ([char]([int][char]'a' + $index)) -ConfigCharacter ([char]([int][char]'d' + $index)) -Version 'v1.2.2' -Revision ('2' * 40 -join '')
    $new = New-Identity -Repository $repository -ManifestCharacter ([char]([int][char]'7' + $index)) -ConfigCharacter ([char]([int][char]'4' + $index)) -Version $script:releaseTag -Revision $script:sourceCommit
    if ($MissingLatest -notcontains $repository)
    {
      $script:tags["$repository`:latest"] = $old
      $script:oldByRepository[$repository] = $old
    }
    $script:newByRepository[$repository] = $new
    $script:immutable["$repository@$($old.manifest_digest)"] = $old
    $script:immutable["$repository@$($new.manifest_digest)"] = $new
    $sourceImages.Add([pscustomobject]@{
        repository = $repository
        immutable_reference = "$repository@$($new.manifest_digest)"
        manifest_digest = $new.manifest_digest
        config_digest = $new.config_digest
        source = $new.source
        version = $new.version
        revision = $new.revision
      })
  }

  $script:files['contract-receipt/release.json'] = ([ordered]@{
      release_tag = $script:releaseTag
      source_commit = $script:sourceCommit
      triggering_workflow_name = $null
      triggering_workflow_head_sha = $null
      triggering_workflow_run_id = $null
      triggering_workflow_job_conclusion = $null
    } | ConvertTo-Json -Compress)
  $script:files['contract-receipt/sources.json'] = ([ordered]@{
      release_tag = $script:releaseTag
      source_commit = $script:sourceCommit
      images = $sourceImages.ToArray()
    } | ConvertTo-Json -Depth 5)
}
function Key($Path)
{
  return $Path -replace '\\', '/'
}


function global:Get-Content
{
  param([Parameter(Mandatory)][string]$LiteralPath, [switch]$Raw)
  if (-not $script:files.ContainsKey((Key $LiteralPath)))
  { throw "missing simulated file: $LiteralPath"
  }
  return [string]$script:files[(Key $LiteralPath)]
}

function global:Set-Content
{
  param([Parameter(Mandatory, ValueFromPipeline)][AllowEmptyString()][string]$Value, [Parameter(Mandatory)][string]$LiteralPath, [string]$Encoding)
  process
  {
    $key = Key $LiteralPath
    if ($key -ceq 'contract-receipt/promotion.json')
    {
      $script:promotionStateWrites++
      if ($script:rejectPromotionStateWrites)
      { throw 'simulated persistent promotion state persistence failure'
      }
      if ($script:failPromotionStateWriteOn -eq $script:promotionStateWrites)
      { throw 'simulated promotion state persistence failure'
      }
      $state = $Value | ConvertFrom-Json
      if ($script:interruptAfterPendingState -and -not $script:interrupted -and @($state.final_latest_images | Where-Object { $_.state -eq 'mutation_pending' }).Count -gt 0)
      {
        $script:files[$key] = $Value
        $script:interrupted = $true
        $script:rejectPromotionStateWrites = $true
        throw 'simulated interruption after durable mutation_pending state'
      }
      if ($script:rejectPromotionStateAfterUpdate -and @($state.updated_latest_images).Count -gt 0)
      {
        $script:rejectPromotionStateWrites = $true
        throw 'simulated persistent promotion state persistence failure after mutation'
      }
    }
    if ($script:failLatestReceiptWrite -and $key -ceq 'contract-receipt/latest.json' -and -not $script:latestReceiptWriteFailed)
    {
      $script:latestReceiptWriteFailed = $true
      throw 'simulated latest receipt persistence failure'
    }
    $script:files[$key] = $Value
  }
}

function global:Add-Content
{
  param([Parameter(Mandatory, ValueFromPipeline)][string]$Value, [Parameter(Mandatory)][string]$LiteralPath)
  process
  {
    if ($LiteralPath -ne 'contract-github-env')
    { throw "unexpected simulated environment path: $LiteralPath"
    }
    $name, $value = $Value -split '=', 2
    Set-Item -LiteralPath "Env:$name" -Value $value
  }
}

function global:Test-Path
{
  param([Parameter(Mandatory)][string]$LiteralPath)
  return $script:files.ContainsKey((Key $LiteralPath))
}

function global:New-Item
{
  param([string]$ItemType, [Parameter(Mandatory)][string]$Path, [switch]$Force)
}

function global:docker
{
  param([Parameter(ValueFromRemainingArguments)][string[]]$Arguments)
  $global:LASTEXITCODE = 0
  if ($Arguments.Count -gt 0 -and $Arguments[0] -eq 'login')
  {
    if ($script:failGHCRLogin)
    {
      $global:LASTEXITCODE = 1
      return 'simulated GHCR login failure'
    }
    return
  }
  if ($Arguments.Count -lt 3 -or $Arguments[0] -ne 'buildx' -or $Arguments[1] -ne 'imagetools')
  { throw "unexpected docker invocation: $($Arguments -join ' ')"
  }
  if ($Arguments[2] -eq 'inspect')
  {
    $reference = $Arguments[3]
    if ($reference -eq $script:inspectFailureTarget)
    {
      $global:LASTEXITCODE = 1
      return 'unauthorized: authentication required'
    }
    if (-not $script:tags.ContainsKey($reference))
    {
      $global:LASTEXITCODE = 1
      return 'manifest unknown'
    }
    $identity = $script:tags[$reference]
    if ($reference -eq $script:failReadbackTarget -and $identity.manifest_digest -eq $script:failReadbackManifest -and -not $script:readbackFailureInjected)
    {
      $script:readbackFailureInjected = $true
      $global:LASTEXITCODE = 1
      return 'simulated readback failure'
    }
    if ($Arguments -contains '--raw')
    { return (@{ config = @{ digest = $identity.config_digest } } | ConvertTo-Json -Compress)
    }
    $formatIndex = [array]::IndexOf($Arguments, '--format')
    if ($Arguments[$formatIndex + 1] -eq '{{json .Manifest}}')
    { return (@{ digest = $identity.manifest_digest } | ConvertTo-Json -Compress)
    }
    if ($Arguments[$formatIndex + 1] -eq '{{json .Image}}')
    { return (@{ config = @{ Labels = @{ 'org.opencontainers.image.source' = $identity.source; 'org.opencontainers.image.version' = $identity.version; 'org.opencontainers.image.revision' = $identity.revision } } } | ConvertTo-Json -Compress)
    }
    throw "unexpected simulated inspect format: $($Arguments[$formatIndex + 1])"
  }
  if ($Arguments[2] -eq 'create')
  {
    $tagIndex = [array]::IndexOf($Arguments, '--tag')
    $target = $Arguments[$tagIndex + 1]
    $source = $Arguments[$Arguments.Count - 1]
    $script:createCalls.Add([pscustomobject]@{ target = $target; source = $source })
    if ($target -eq $script:failCreateTarget -and $source -eq $script:failCreateSource)
    {
      $global:LASTEXITCODE = 1
      return
    }
    if (-not $script:immutable.ContainsKey($source))
    {
      $global:LASTEXITCODE = 1
      return
    }
    $script:tags[$target] = $script:immutable[$source]
    $script:eventSequence++
    $script:eventLog.Add([pscustomobject]@{ sequence = $script:eventSequence; kind = 'registry_create'; target = $target; source = $source })
    $targetRepository = $target.Substring(0, $target.LastIndexOf(':'))
    if ($script:interruptAfterRegistryCreate -and $null -eq $script:processLossJournal -and $source -ceq "$targetRepository@$($script:newByRepository[$targetRepository].manifest_digest)")
    {
      $journal = $script:journalRuns[$env:LATEST_PROMOTION_JOURNAL_ID]
      $script:processLossJournal = [pscustomobject]@{ status = [string]$journal.status; conclusion = $journal.conclusion; patch_count = $script:journalPatchCount; summary = [string]$journal.output.summary }
      throw 'simulated event-log cut immediately after the first registry mutation'
    }
    return
  }
  throw "unexpected docker subcommand: $($Arguments -join ' ')"
}

function global:gh
{
  param([Parameter(ValueFromRemainingArguments)][string[]]$Arguments)
  $global:LASTEXITCODE = 0
  $request = $Arguments -join ' '
  if ($request -match 'repos/thebtf/engram/check-runs\b')
  {
    if ($request -match 'repos/thebtf/engram/check-runs/([1-9][0-9]*)$' -and $request -notmatch '--method')
    {
      $id = $Matches[1]
      if (-not $script:journalRuns.ContainsKey($id))
      { throw "unknown simulated journal: $id"
      }
      return ($script:journalRuns[$id] | ConvertTo-Json -Compress -Depth 10)
    }
    $inputIndex = [array]::IndexOf($Arguments, '--input')
    if ($inputIndex -lt 0 -or $inputIndex -eq ($Arguments.Count - 1))
    { throw "journal request lacks a simulated input body: $request"
    }
    $body = Get-Content -LiteralPath $Arguments[$inputIndex + 1] -Raw | ConvertFrom-Json
    if ($request -match '--method POST')
    {
      $script:journalCreateCount++
      if ($script:failJournalCreate)
      {
        $global:LASTEXITCODE = 1
        return 'simulated journal create failure'
      }
      $id = '41'
      $run = [ordered]@{
        id = $id
        name = [string]$body.name
        head_sha = [string]$body.head_sha
        external_id = [string]$body.external_id
        details_url = [string]$body.details_url
        status = [string]$body.status
        conclusion = $null
        output = $null
      }
      $script:journalRuns[$id] = $run
      $script:eventSequence++
      $event = [pscustomobject]@{ sequence = $script:eventSequence; kind = 'journal_create'; body = $body }
      $script:journalEvents.Add($event)
      $script:eventLog.Add($event)
      $response = [ordered]@{} + $run
      if ($script:invalidJournalCreateResponse)
      { $response.id = 'invalid'
      }
      return ($response | ConvertTo-Json -Compress -Depth 10)
    }
    if ($request -match '--method PATCH' -and $request -match 'check-runs/([1-9][0-9]*)')
    {
      $id = $Matches[1]
      if (-not $script:journalRuns.ContainsKey($id))
      { throw "unknown simulated journal: $id"
      }
      if ($script:interruptAfterPendingState -and $script:interrupted -and -not $script:runningTerminalizer)
      {
        $global:LASTEXITCODE = 1
        return 'simulated runner loss before journal patch'
      }
      $script:journalPatchCount++
      if ($script:failJournalPatchOn -eq $script:journalPatchCount)
      {
        $global:LASTEXITCODE = 1
        return 'simulated journal patch failure'
      }
      $run = $script:journalRuns[$id]
      $run.status = [string]$body.status
      $run.conclusion = if ($null -eq $body.PSObject.Properties['conclusion']) { $null } else { $body.conclusion }
      $run.output = $body.output
      $script:eventSequence++
      $event = [pscustomobject]@{ sequence = $script:eventSequence; kind = 'journal_patch'; body = $body }
      $script:journalEvents.Add($event)
      $script:eventLog.Add($event)
      $response = [ordered]@{} + $run
      if ($script:invalidJournalPatchResponse)
      { $response.id = 'invalid'
      }
      return ($response | ConvertTo-Json -Compress -Depth 10)
    }
  }
  if ($request -match 'releases/latest')
  { return $script:postWriteReleaseTag
  }
  if ($request -match '/commits/')
  { return $script:postWriteSourceCommit
  }
  throw "unexpected gh invocation: $request"
}

function Invoke-WorkflowStep
{
  param([Parameter(Mandatory)][string]$Name)
  if ($Name -ceq 'Promote each official release image to latest as a recoverable set')
  { Invoke-WorkflowStep -Name 'Create durable latest-promotion journal'
  }
  $workflow = [System.IO.File]::ReadAllText($WorkflowPath)
  $match = [regex]::Match($workflow, '(?ms)^      - name: ' + [regex]::Escape($Name) + '\r?\n.*?^        run: \|\r?\n(?<body>.*?)(?=^      - name:|\z)')
  if (-not $match.Success)
  { throw "missing workflow step: $Name"
  }
  $body = $match.Groups['body'].Value -replace '(?m)^ {10}', ''
  $wasRunningTerminalizer = $script:runningTerminalizer
  $script:runningTerminalizer = $Name -ceq 'Complete unstarted latest-promotion journal'
  try
  { & ([scriptblock]::Create($body))
  } finally
  { $script:runningTerminalizer = $wasRunningTerminalizer
  }
}

function Read-PromotionState
{
  return $script:files['contract-receipt/promotion.json'] | ConvertFrom-Json
}

function Read-Receipt
{
  Invoke-WorkflowStep -Name 'Write latest-promotion receipt'
  return $script:files['contract-receipt/latest-promotion-receipt.json'] | ConvertFrom-Json
}

function Get-MockFinalState
{
  param([Parameter(Mandatory)][string]$Reference)
  $repository = $Reference.Substring(0, $Reference.LastIndexOf(':'))
  if ($Reference -eq $script:inspectFailureTarget)
  {
    return [pscustomobject]@{ reference = $Reference; repository = $repository; state = 'readback_error'; immutable_reference = $null; manifest_digest = $null }
  }
  if (-not $script:tags.ContainsKey($Reference))
  {
    return [pscustomobject]@{ reference = $Reference; repository = $repository; state = 'absent'; immutable_reference = $null; manifest_digest = $null }
  }
  $identity = $script:tags[$Reference]
  return [pscustomobject]@{ reference = $Reference; repository = $repository; state = 'present'; immutable_reference = "$repository@$($identity.manifest_digest)"; manifest_digest = $identity.manifest_digest }
}

function Assert-ReceiptMatchesState
{
  param([Parameter(Mandatory)]$Promotion, [Parameter(Mandatory)]$Receipt)
  $expected = @($Promotion.final_latest_images) | ConvertTo-Json -Depth 10 -Compress
  $actual = @($Receipt.latest_images) | ConvertTo-Json -Depth 10 -Compress
  Assert-That ($actual -ceq $expected) 'terminal receipt latest_images must equal the persisted promotion final state'
}

function Get-ExternalJournal
{
  Assert-That ($script:journalRuns.Count -eq 1) 'scenario must create exactly one external journal'
  return @($script:journalRuns.Values)[0]
}

function New-ExpectedPendingJournalSummary
{
  $repository = $script:repositories[0]
  $previous = $script:oldByRepository[$repository]
  $intended = $script:newByRepository[$repository]
  $final = foreach ($candidate in $script:repositories)
  {
    $identity = if ($candidate -ceq $repository)
    { $intended
    } else
    { $script:oldByRepository[$candidate]
    }
    [ordered]@{
      reference = "$candidate`:latest"
      repository = $candidate
      state = if ($candidate -ceq $repository)
      { 'mutation_pending'
      } else
      { 'present'
      }
      manifest_digest = $identity.manifest_digest
    }
  }
  return ([ordered]@{
      phase = 'writing_latest'
      outcome = 'mutation_pending'
      repository = $repository
      previous_identity = [ordered]@{ reference = "$repository`:latest"; state = 'present'; immutable_reference = "$repository@$($previous.manifest_digest)"; manifest_digest = $previous.manifest_digest }
      intended_identity = [ordered]@{ reference = "$repository`:latest"; state = 'mutation_pending'; immutable_reference = "$repository@$($intended.manifest_digest)"; manifest_digest = $intended.manifest_digest }
      updated_summary = [ordered]@{ count = 0; images = @() }
      final_summary = [ordered]@{ count = 3; images = @($final) }
    } | ConvertTo-Json -Compress -Depth 10)
}

function Assert-PendingJournalPrecedesRegistryMutation
{
  $expected = New-ExpectedPendingJournalSummary
  $pending = @($script:journalEvents | Where-Object { $_.kind -eq 'journal_patch' -and [string]$_.body.output.summary -ceq $expected })
  Assert-That ($pending.Count -eq 1) 'first external journal PATCH must carry the exact mutation_pending payload'
  $registryCreate = @($script:eventLog | Where-Object { $_.kind -eq 'registry_create' })[0]
  Assert-That ($pending[0].sequence -lt $registryCreate.sequence) 'external mutation_pending PATCH must precede the first registry create'
}

function Assert-ActualJournalPatch
{
  $actual = @($script:journalEvents | Where-Object { $_.kind -eq 'journal_patch' -and (($_.body.output.summary | ConvertFrom-Json).outcome -eq 'updated') })[0]
  $registryCreate = @($script:eventLog | Where-Object { $_.kind -eq 'registry_create' })[0]
  Assert-That ($null -ne $actual -and $actual.sequence -gt $registryCreate.sequence) 'actual journal PATCH must follow the registry create/readback'
  $summary = $actual.body.output.summary | ConvertFrom-Json
  Assert-That ($summary.updated_summary.count -eq 1 -and $summary.updated_summary.images[0].manifest_digest -ceq $script:newByRepository[$script:repositories[0]].manifest_digest) 'actual journal PATCH must summarize the readback identity'
}

function Assert-RollbackJournalPatch
{
  param([Parameter(Mandatory)][string]$Outcome, [Parameter(Mandatory)][string]$Repository)
  $patches = @($script:journalEvents | Where-Object {
      if ($_.kind -cne 'journal_patch')
      { return $false
      }
      $summary = $_.body.output.summary | ConvertFrom-Json
      return $summary.outcome -ceq $Outcome -and $summary.repository -ceq $Repository
    })
  Assert-That ($patches.Count -eq 1) "rollback journal must emit exactly one $Outcome PATCH for $Repository"
  $summary = $patches[0].body.output.summary | ConvertFrom-Json
  Assert-That ($summary.phase -ceq 'rolling_back') "rollback journal $Outcome PATCH must use the rolling_back phase"
  $previous = $script:oldByRepository[$Repository]
  $expected = ([ordered]@{ reference = "$Repository`:latest"; state = 'present'; immutable_reference = "$Repository@$($previous.manifest_digest)"; manifest_digest = $previous.manifest_digest } | ConvertTo-Json -Compress)
  Assert-That (($summary.previous_identity | ConvertTo-Json -Compress) -ceq $expected) "rollback journal $Outcome PATCH must carry the exact previous immutable identity"
  Assert-That (($summary.intended_identity | ConvertTo-Json -Compress) -ceq $expected) "rollback journal $Outcome PATCH must carry the exact intended rollback identity"
}

function Assert-JournalTerminal
{
  param([Parameter(Mandatory)][string]$Outcome, [Parameter(Mandatory)][string]$Conclusion)
  $journal = Get-ExternalJournal
  Assert-That ($journal.name -ceq 'latest-promotion-journal') 'journal name must be stable'
  Assert-That ($journal.head_sha -ceq $script:sourceCommit) 'journal must target the selected release commit'
  Assert-That ($journal.external_id -ceq 'latest-promotion:8675309:3') 'journal must bind the run and attempt'
  Assert-That ($journal.details_url -ceq 'https://github.com/thebtf/engram/actions/runs/8675309') 'journal must bind the current run URL'
  Assert-That ($journal.status -ceq 'completed' -and $journal.conclusion -ceq $Conclusion) "journal $Outcome must complete with $Conclusion, got $($journal.status)/$($journal.conclusion)"
  $summary = $journal.output.summary | ConvertFrom-Json
  Assert-That ($summary.outcome -ceq $Outcome) "journal terminal outcome must be $Outcome"
}

function Assert-ReceiptMatchesRegistryOrTypedState
{
  param([Parameter(Mandatory)]$Receipt)
  Assert-That (@($Receipt.latest_images).Count -eq $script:repositories.Count) 'terminal receipt must include every latest tag'
  foreach ($image in @($Receipt.latest_images))
  {
    if ($image.state -eq 'mutation_pending')
    {
      Assert-That (-not [string]::IsNullOrWhiteSpace([string]$image.intended_manifest_digest)) "pending receipt state lacks an intended manifest identity: $($image.reference)"
      continue
    }
    $actual = Get-MockFinalState -Reference $image.reference
    Assert-That ($image.state -ceq $actual.state) "terminal receipt state differs from mock registry: $($image.reference)"
    if ($image.state -eq 'present')
    {
      Assert-That ($image.manifest_digest -ceq $actual.manifest_digest) "terminal receipt manifest differs from mock registry: $($image.reference)"
      Assert-That ($image.immutable_reference -ceq $actual.immutable_reference) "terminal receipt immutable reference differs from mock registry: $($image.reference)"
    }
  }
}

function Assert-AllExistingLatestAreOld
{
  foreach ($repository in $script:oldByRepository.Keys)
  {
    Assert-That ($script:tags["$repository`:latest"].manifest_digest -ceq $script:oldByRepository[$repository].manifest_digest) "latest did not return to its captured identity: $repository"
  }
}

$workflowText = [System.IO.File]::ReadAllText($WorkflowPath)
Assert-That (-not $workflowText.Contains('Remove-CreatedLatestTag')) 'promotion workflow must not retain unsafe absent-tag cleanup'
Assert-That (-not $workflowText.Contains('api --method DELETE')) 'promotion workflow must not delete registry package versions'
Assert-That ($workflowText.Contains('Create durable latest-promotion journal')) 'promotion workflow must create an external check-run journal before login'

$prewrite = 'Final revalidate official GitHub Release before registry login'
$promotion = 'Promote each official release image to latest as a recoverable set'
$journal = 'Create durable latest-promotion journal'
$login = 'Login to GHCR only after immutable provenance inspection'
$terminalizer = 'Complete unstarted latest-promotion journal'
$firstRepository = 'ghcr.io/thebtf/engram'
$secondRepository = 'ghcr.io/thebtf/engram-operator-console'

$script:scenarioCount = 0
Set-Scenario -PostWriteReleaseTag 'v1.2.4' -PostWriteSourceCommit ('4' * 40 -join '')
$failed = $false
try
{ Invoke-WorkflowStep -Name $prewrite
} catch
{ $failed = $true
}
Assert-That $failed 'stale prewrite release must abort'
Assert-That ($script:createCalls.Count -eq 0) 'stale prewrite release must perform no tag writes'
$staleReceipt = Read-Receipt
Assert-That ($staleReceipt.outcome -ceq 'no_write_stale_abort') 'stale prewrite receipt must distinguish the no-write abort'

Set-Scenario
Invoke-WorkflowStep -Name $promotion
$success = Read-PromotionState
Assert-That ($success.outcome -ceq 'success') 'verified promotion must record success'
Assert-That ($script:createCalls.Count -eq 3) 'verified promotion must write each latest tag once'
$successReceipt = Read-Receipt
Assert-ReceiptMatchesState -Promotion $success -Receipt $successReceipt
Assert-PendingJournalPrecedesRegistryMutation
Assert-ActualJournalPatch
Assert-JournalTerminal -Outcome 'success' -Conclusion 'success'
Assert-ReceiptMatchesRegistryOrTypedState -Receipt $successReceipt
Set-Scenario -FailGHCRLogin
Invoke-WorkflowStep -Name $journal
$failed = $false
try
{ Invoke-WorkflowStep -Name $login
} catch
{ $failed = $true
}
Assert-That $failed 'GHCR login failure must stop promotion'
Assert-That (-not $script:files.ContainsKey('contract-receipt/promotion.json') -and $script:createCalls.Count -eq 0) 'GHCR login failure must precede promotion state and registry mutation'
Invoke-WorkflowStep -Name $terminalizer
Assert-That (-not $script:files.ContainsKey('contract-receipt/promotion.json')) 'login-failure terminalizer must not synthesize promotion state'
Assert-That ($script:journalPatchCount -eq 1) 'login-failure terminalizer must PATCH the exact pre-promotion journal once'
Assert-JournalTerminal -Outcome 'failed_before_write' -Conclusion 'failure'
$loginFailureSummary = (Get-ExternalJournal).output.summary | ConvertFrom-Json
Assert-That ($loginFailureSummary.phase -ceq 'pre_promotion' -and $loginFailureSummary.outcome -ceq 'failed_before_write') 'login-failure journal must use the typed pre-promotion failure outcome'
Assert-That ($null -eq $loginFailureSummary.repository -and $null -eq $loginFailureSummary.previous_identity -and $null -eq $loginFailureSummary.intended_identity) 'login-failure journal must not invent a promotion identity'
Assert-That ($loginFailureSummary.updated_summary.count -eq 0 -and $loginFailureSummary.final_summary.count -eq 0) 'login-failure journal must prove that no promotion state exists'
$loginFailureReceipt = Read-Receipt
Assert-That ($loginFailureReceipt.outcome -ceq 'failed_before_write') 'login failure receipt must record the no-write terminal outcome'

Set-Scenario -FailGHCRLogin -InvalidJournalPatchResponse
Invoke-WorkflowStep -Name $journal
try
{ Invoke-WorkflowStep -Name $login
} catch {}
$failed = $false
try
{ Invoke-WorkflowStep -Name $terminalizer
} catch
{ $failed = $true
}
Assert-That $failed 'invalid terminalizer response must fail exact external-journal validation'
Assert-That (-not $script:files.ContainsKey('contract-receipt/promotion.json') -and $script:journalPatchCount -eq 1) 'invalid terminalizer response must remain a no-promotion journal-only failure'

Set-Scenario -FailJournalCreate
$failed = $false
try
{ Invoke-WorkflowStep -Name $promotion
} catch
{ $failed = $true
}
Assert-That $failed 'journal creation failure must stop promotion'
Assert-That ($script:createCalls.Count -eq 0 -and $script:journalRuns.Count -eq 0) 'journal creation failure must precede every registry mutation'

Set-Scenario -FailJournalPatchOn 1
$failed = $false
try
{ Invoke-WorkflowStep -Name $promotion
} catch
{ $failed = $true
}
Assert-That $failed 'mandatory mutation_pending journal PATCH failure must stop promotion'
Assert-That ($script:createCalls.Count -eq 0) 'mandatory mutation_pending journal PATCH failure must precede every registry mutation'
Assert-JournalTerminal -Outcome 'failed_before_write' -Conclusion 'failure'

Set-Scenario -InvalidJournalCreateResponse
$failed = $false
try
{ Invoke-WorkflowStep -Name $promotion
} catch
{ $failed = $true
}
Assert-That $failed 'invalid journal creation response must stop promotion'
Assert-That ($script:createCalls.Count -eq 0 -and $script:journalRuns.Count -eq 1) 'invalid journal creation response must precede every registry mutation'

Set-Scenario -InvalidJournalPatchResponse
$failed = $false
try
{ Invoke-WorkflowStep -Name $promotion
} catch
{ $failed = $true
}
Assert-That $failed 'invalid mutation_pending journal PATCH response must stop promotion'
Assert-That ($script:createCalls.Count -eq 0) 'invalid journal PATCH response must precede every registry mutation'

Set-Scenario -FailJournalPatchOn 2
$failed = $false
try
{ Invoke-WorkflowStep -Name $promotion
} catch
{ $failed = $true
}
Assert-That $failed 'actual-state journal PATCH failure must stop promotion'
Assert-That ($script:createCalls.Count -eq 2) 'actual-state journal PATCH failure must restore the first registry mutation before stopping'
Assert-AllExistingLatestAreOld
Assert-JournalTerminal -Outcome 'rolled_back' -Conclusion 'neutral'

Set-Scenario -InterruptAfterRegistryCreate
$failed = $false
try
{ Invoke-WorkflowStep -Name $promotion
} catch
{ $failed = $true
}
Assert-That $failed 'event-log cut after the first registry mutation must stop this mock runner'
Assert-That (-not $script:files.ContainsKey('contract-receipt/latest-promotion-receipt.json')) 'process-loss cut must have no local always-step receipt'
$processLossJournalRun = $script:processLossJournal
Assert-That ($processLossJournalRun.status -ceq 'in_progress') 'process loss after registry mutation must leave the external journal in progress'
Assert-That ($null -eq $processLossJournalRun.conclusion) 'process loss after registry mutation must leave the external journal unconcluded'
Assert-That ($processLossJournalRun.patch_count -eq 1) 'process loss after registry mutation must retain only its mutation_pending journal PATCH'
$processLossSummary = $processLossJournalRun.summary | ConvertFrom-Json
Assert-That ($processLossSummary.outcome -ceq 'mutation_pending' -and $processLossSummary.previous_identity.manifest_digest -ceq $script:oldByRepository[$firstRepository].manifest_digest -and $processLossSummary.intended_identity.manifest_digest -ceq $script:newByRepository[$firstRepository].manifest_digest) 'external-only process-loss journal must retain the uncertain prior and intended identities'


Set-Scenario -MissingLatest @($firstRepository)
$failed = $false
try
{ Invoke-WorkflowStep -Name $promotion
} catch
{ $failed = $true
}
$missing = Read-PromotionState
Assert-That $failed 'confirmed missing latest tag must stop promotion'
Assert-That ($missing.outcome -ceq 'bootstrap_required_no_write') 'confirmed missing latest tag must require manual bootstrap'
Assert-That ($missing.previous_latest_images[0].state -ceq 'absent') 'confirmed missing latest tag must be recorded as absent'
Assert-That ($script:createCalls.Count -eq 0) 'confirmed missing latest tag must perform zero writes'
$missingReceipt = Read-Receipt
Assert-ReceiptMatchesState -Promotion $missing -Receipt $missingReceipt
Assert-JournalTerminal -Outcome 'bootstrap_required_no_write' -Conclusion 'neutral'
Assert-ReceiptMatchesRegistryOrTypedState -Receipt $missingReceipt

Set-Scenario -InspectFailureTarget "$firstRepository`:latest"
$failed = $false
try
{ Invoke-WorkflowStep -Name $promotion
} catch
{ $failed = $true
}
$inspect = Read-PromotionState
Assert-That $failed 'non-absence latest inspection error must stop promotion'
Assert-That ($inspect.outcome -ceq 'failed_before_write') 'authentication or network inspection error must not bootstrap'
Assert-That ($script:createCalls.Count -eq 0) 'non-absence latest inspection error must perform zero writes'
$inspectReceipt = Read-Receipt
Assert-ReceiptMatchesState -Promotion $inspect -Receipt $inspectReceipt
Assert-JournalTerminal -Outcome 'failed_before_write' -Conclusion 'failure'
Assert-ReceiptMatchesRegistryOrTypedState -Receipt $inspectReceipt

Set-Scenario
$script:failPromotionStateWriteOn = 3
$failed = $false
try
{ Invoke-WorkflowStep -Name $promotion
} catch
{ $failed = $true
}
$pendingWriteFailure = Read-PromotionState
Assert-That $failed 'mandatory mutation_pending persistence failure must stop promotion'
Assert-That ($pendingWriteFailure.outcome -ceq 'failed_before_write') 'mandatory mutation_pending persistence failure must be no-write'
Assert-That ($script:createCalls.Count -eq 0) 'mandatory mutation_pending persistence failure must abort before registry write'
$pendingWriteReceipt = Read-Receipt
Assert-ReceiptMatchesState -Promotion $pendingWriteFailure -Receipt $pendingWriteReceipt
Assert-JournalTerminal -Outcome 'failed_before_write' -Conclusion 'failure'
Assert-ReceiptMatchesRegistryOrTypedState -Receipt $pendingWriteReceipt

Set-Scenario
$script:failReadbackTarget = "$firstRepository`:latest"
$script:failReadbackManifest = $script:newByRepository[$firstRepository].manifest_digest
$failed = $false
try
{ Invoke-WorkflowStep -Name $promotion
} catch
{ $failed = $true
}
$readbackRollback = Read-PromotionState
Assert-That $failed 'post-create readback failure must stop promotion'
Assert-That ($readbackRollback.outcome -ceq 'rolled_back') 'post-create readback failure must roll back existing tags'
Assert-AllExistingLatestAreOld
$readbackReceipt = Read-Receipt
Assert-ReceiptMatchesState -Promotion $readbackRollback -Receipt $readbackReceipt
Assert-JournalTerminal -Outcome 'rolled_back' -Conclusion 'neutral'
Assert-ReceiptMatchesRegistryOrTypedState -Receipt $readbackReceipt
Assert-RollbackJournalPatch -Outcome 'rollback_pending' -Repository $firstRepository
Assert-RollbackJournalPatch -Outcome 'restored' -Repository $firstRepository

Set-Scenario
$script:failCreateTarget = "$secondRepository`:latest"
$script:failCreateSource = "$secondRepository@$($script:newByRepository[$secondRepository].manifest_digest)"
$failed = $false
try
{ Invoke-WorkflowStep -Name $promotion
} catch
{ $failed = $true
}
$createRollback = Read-PromotionState
Assert-That $failed 'create failure must stop promotion'
Assert-That ($createRollback.outcome -ceq 'rolled_back') 'create failure must roll back existing tags'
Assert-AllExistingLatestAreOld
$createReceipt = Read-Receipt
Assert-ReceiptMatchesState -Promotion $createRollback -Receipt $createReceipt
Assert-JournalTerminal -Outcome 'rolled_back' -Conclusion 'neutral'
Assert-ReceiptMatchesRegistryOrTypedState -Receipt $createReceipt

Set-Scenario -PostWriteReleaseTag 'v1.2.4' -PostWriteSourceCommit ('4' * 40 -join '') -FailLatestReceiptWrite
$failed = $false
try
{ Invoke-WorkflowStep -Name $promotion
} catch
{ $failed = $true
}
$latestReceiptRollback = Read-PromotionState
Assert-That $failed 'later release mismatch must stop promotion after a latest receipt failure'
Assert-That ($latestReceiptRollback.outcome -ceq 'rolled_back') 'latest receipt failure must not prevent rollback'
Assert-That (@($latestReceiptRollback.intermediate_receipt_failures).Count -eq 1) 'latest receipt failure must be preserved in promotion state'
Assert-AllExistingLatestAreOld
$latestReceipt = Read-Receipt
Assert-ReceiptMatchesState -Promotion $latestReceiptRollback -Receipt $latestReceipt
Assert-ReceiptMatchesRegistryOrTypedState -Receipt $latestReceipt

Set-Scenario -RejectPromotionStateAfterUpdate
$failed = $false
try
{ Invoke-WorkflowStep -Name $promotion
} catch
{ $failed = $true
}
$persistentFailure = Read-PromotionState
Assert-That $failed 'persistent promotion receipt failure after mutation must stop promotion'
Assert-That ($script:createCalls.Count -eq 2) 'persistent promotion receipt failure must not start the next mutation'
Assert-That ($persistentFailure.final_latest_images[0].state -ceq 'mutation_pending') 'persistent promotion receipt failure must retain the durable pending identity'
Assert-AllExistingLatestAreOld
$persistentReceipt = Read-Receipt
Assert-ReceiptMatchesState -Promotion $persistentFailure -Receipt $persistentReceipt
Assert-JournalTerminal -Outcome 'rolled_back' -Conclusion 'neutral'
Assert-ReceiptMatchesRegistryOrTypedState -Receipt $persistentReceipt

Set-Scenario -InterruptAfterPendingState
$failed = $false
try
{ Invoke-WorkflowStep -Name $promotion
} catch
{ $failed = $true
}
$interrupted = Read-PromotionState
Assert-That $failed 'interruption after durable pending state must stop promotion'
Assert-That ($script:createCalls.Count -eq 0) 'interruption after durable pending state must happen before registry write'
Assert-That ($interrupted.final_latest_images[0].state -ceq 'mutation_pending') 'interruption must leave the explicit pending final state'
Assert-That ($script:journalPatchCount -eq 0) 'interruption before the promotion PATCH must leave the external journal unpatched'
Invoke-WorkflowStep -Name $terminalizer
Assert-That ($script:journalPatchCount -eq 1) 'terminalizer must PATCH a durable mutation_pending promotion state exactly once'
Assert-JournalTerminal -Outcome 'mutation_pending' -Conclusion 'failure'
$interruptedSummary = (Get-ExternalJournal).output.summary
Assert-That ($interruptedSummary -ceq (New-ExpectedPendingJournalSummary)) 'terminalized mutation_pending journal must preserve the exact previous and intended recovery identities'
Invoke-WorkflowStep -Name $terminalizer
Assert-That ($script:journalPatchCount -eq 1) 'terminalizer must not overwrite an already completed mutation_pending journal'
$interruptedReceipt = Read-Receipt
Assert-ReceiptMatchesState -Promotion $interrupted -Receipt $interruptedReceipt
Assert-ReceiptMatchesRegistryOrTypedState -Receipt $interruptedReceipt

Set-Scenario -PostWriteReleaseTag 'v1.2.4' -PostWriteSourceCommit ('4' * 40 -join '')
$script:failCreateTarget = "$firstRepository`:latest"
$script:failCreateSource = "$firstRepository@$($script:oldByRepository[$firstRepository].manifest_digest)"
$failed = $false
try
{ Invoke-WorkflowStep -Name $promotion
} catch
{ $failed = $true
}
$rollbackFailure = Read-PromotionState
Assert-That $failed 'rollback readback failure must stop promotion'
Assert-That ($rollbackFailure.outcome -ceq 'rollback_failed') 'failed existing-tag rollback must remain explicit'
Assert-That (@($rollbackFailure.rollback.failures).Count -eq 1) 'failed existing-tag rollback must record its target'
$rollbackReceipt = Read-Receipt
Assert-JournalTerminal -Outcome 'rollback_failed' -Conclusion 'failure'
Assert-RollbackJournalPatch -Outcome 'rollback_pending' -Repository $firstRepository
Assert-RollbackJournalPatch -Outcome 'rollback_failed' -Repository $firstRepository
Assert-ReceiptMatchesState -Promotion $rollbackFailure -Receipt $rollbackReceipt
Assert-ReceiptMatchesRegistryOrTypedState -Receipt $rollbackReceipt
Assert-That ($script:scenarioCount -eq 19) "promotion state matrix must execute exactly 19 scenarios, got $($script:scenarioCount)"
"PASS: external journal create/PATCH ordering, rollback PATCH identities (rollback_pending/restored/rollback_failed), GHCR login terminalization, process-loss pending state, no-write journal failures, success, rollback, bootstrap-required no-write, inspection rejection, local receipt failures, and rollback failure; scenarios=19"
