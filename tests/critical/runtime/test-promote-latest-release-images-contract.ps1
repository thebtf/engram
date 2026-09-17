param([Parameter(Mandatory)][string]$WorkflowPath)

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
    [switch]$InterruptAfterPendingState
  )

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

  $env:RECEIPT_DIR = 'contract-receipt'
  $env:REPOSITORY_NAME = 'thebtf/engram'
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
    return
  }
  throw "unexpected docker subcommand: $($Arguments -join ' ')"
}

function global:gh
{
  param([Parameter(ValueFromRemainingArguments)][string[]]$Arguments)
  $global:LASTEXITCODE = 0
  $request = $Arguments -join ' '
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
  $workflow = [System.IO.File]::ReadAllText($WorkflowPath)
  $match = [regex]::Match($workflow, '(?ms)^      - name: ' + [regex]::Escape($Name) + '\r?\n.*?^        run: \|\r?\n(?<body>.*?)(?=^      - name:|\z)')
  if (-not $match.Success)
  { throw "missing workflow step: $Name"
  }
  $body = $match.Groups['body'].Value -replace '(?m)^ {10}', ''
  & ([scriptblock]::Create($body))
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

$prewrite = 'Final revalidate official GitHub Release before registry login'
$promotion = 'Promote each official release image to latest as a recoverable set'
$firstRepository = 'ghcr.io/thebtf/engram'
$secondRepository = 'ghcr.io/thebtf/engram-operator-console'

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
Assert-ReceiptMatchesRegistryOrTypedState -Receipt $successReceipt

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
Assert-ReceiptMatchesRegistryOrTypedState -Receipt $readbackReceipt

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
Assert-ReceiptMatchesState -Promotion $rollbackFailure -Receipt $rollbackReceipt
Assert-ReceiptMatchesRegistryOrTypedState -Receipt $rollbackReceipt

'PASS: stale abort, success, bootstrap-required no-write, inspection rejection, pending-write abort, readback rollback, create rollback, latest receipt rollback, persistent state receipt, phase-anchored interruption, and rollback failure'