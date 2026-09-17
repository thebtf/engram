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

function Get-WorkflowRun
{
  param([Parameter(Mandatory)][string]$Name)
  $workflow = [System.IO.File]::ReadAllText($WorkflowPath)
  $pattern = '(?ms)^      - name: ' + [regex]::Escape($Name) + '\r?\n.*?^        run: \|\r?\n(?<body>.*?)(?=^      - name:|\z)'
  $match = [regex]::Match($workflow, $pattern)
  if (-not $match.Success)
  { throw "could not find workflow step: $Name"
  }
  return ($match.Groups['body'].Value -replace '(?m)^ {10}', '')
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
  param([string]$PostWriteReleaseTag = 'v1.2.3', [string]$PostWriteSourceCommit = ('1' * 40 -join ''))

  $script:releaseTag = 'v1.2.3'
  $script:sourceCommit = '1' * 40 -join ''
  $script:postWriteReleaseTag = $PostWriteReleaseTag
  $script:postWriteSourceCommit = $PostWriteSourceCommit
  $script:files = @{}
  $script:tags = @{}
  $script:immutable = @{}
  $script:oldByRepository = @{}
  $script:createCalls = [System.Collections.Generic.List[object]]::new()
  $script:failCreateTarget = $null
  $script:failCreateSource = $null
  $script:failReadbackTarget = $null
  $script:failReadbackManifest = $null
  $script:readbackFailureInjected = $false

  $env:RECEIPT_DIR = 'contract-receipt'
  $env:REPOSITORY_NAME = 'thebtf/engram'

  $repositories = @(
    'ghcr.io/thebtf/engram',
    'ghcr.io/thebtf/engram-operator-console',
    'ghcr.io/thebtf/engram-postgres'
  )
  $sourceImages = [System.Collections.Generic.List[object]]::new()
  for ($index = 0; $index -lt $repositories.Count; $index++)
  {
    $repository = $repositories[$index]
    $old = New-Identity -Repository $repository -ManifestCharacter ([char]([int][char]'a' + $index)) -ConfigCharacter ([char]([int][char]'d' + $index)) -Version 'v1.2.2' -Revision ('2' * 40 -join '')
    $new = New-Identity -Repository $repository -ManifestCharacter ([char]([int][char]'7' + $index)) -ConfigCharacter ([char]([int][char]'4' + $index)) -Version $script:releaseTag -Revision $script:sourceCommit
    $oldReference = "$repository@$($old.manifest_digest)"
    $newReference = "$repository@$($new.manifest_digest)"
    $script:tags["$repository`:latest"] = $old
    $script:immutable[$oldReference] = $old
    $script:immutable[$newReference] = $new
    $script:oldByRepository[$repository] = $old
    $sourceImages.Add([pscustomobject]@{
        repository = $repository
        immutable_reference = $newReference
        manifest_digest = $new.manifest_digest
        config_digest = $new.config_digest
        source = $new.source
        version = $new.version
        revision = $new.revision
      })
  }

  $script:files[(Join-Path $env:RECEIPT_DIR 'release.json')] = ([ordered]@{
      release_tag = $script:releaseTag
      source_commit = $script:sourceCommit
      triggering_workflow_name = $null
      triggering_workflow_head_sha = $null
      triggering_workflow_run_id = $null
      triggering_workflow_job_conclusion = $null
    } | ConvertTo-Json -Compress)
  $script:files[(Join-Path $env:RECEIPT_DIR 'sources.json')] = ([ordered]@{
      release_tag = $script:releaseTag
      source_commit = $script:sourceCommit
      images = $sourceImages.ToArray()
    } | ConvertTo-Json -Depth 5)
}

function global:Get-Content
{
  param([Parameter(Mandatory)][string]$LiteralPath, [switch]$Raw)
  if (-not $script:files.ContainsKey($LiteralPath))
  { throw "missing simulated file: $LiteralPath"
  }
  return [string]$script:files[$LiteralPath]
}

function global:Set-Content
{
  param(
    [Parameter(Mandatory, ValueFromPipeline)][AllowEmptyString()][string]$Value,
    [Parameter(Mandatory)][string]$LiteralPath,
    [string]$Encoding
  )
  process
  { $script:files[$LiteralPath] = $Value
  }
}
function global:Test-Path
{
  param([Parameter(Mandatory)][string]$LiteralPath)
  return $script:files.ContainsKey($LiteralPath)
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
    if (-not $script:tags.ContainsKey($reference))
    {
      $global:LASTEXITCODE = 1
      return
    }
    $identity = $script:tags[$reference]
    if ($reference -eq $script:failReadbackTarget -and $identity.manifest_digest -eq $script:failReadbackManifest -and -not $script:readbackFailureInjected)
    {
      $script:readbackFailureInjected = $true
      $global:LASTEXITCODE = 1
      return
    }

    if ($Arguments -contains '--raw')
    {
      return (@{ config = @{ digest = $identity.config_digest } } | ConvertTo-Json -Compress)
    }
    $formatIndex = [array]::IndexOf($Arguments, '--format')
    if ($formatIndex -lt 0)
    { throw "missing simulated inspect format: $reference"
    }
    if ($Arguments[$formatIndex + 1] -eq '{{json .Manifest}}')
    {
      return (@{ digest = $identity.manifest_digest } | ConvertTo-Json -Compress)
    }
    if ($Arguments[$formatIndex + 1] -eq '{{json .Image}}')
    {
      return (@{ config = @{ Labels = @{ 'org.opencontainers.image.source' = $identity.source; 'org.opencontainers.image.version' = $identity.version; 'org.opencontainers.image.revision' = $identity.revision } } } | ConvertTo-Json -Compress)
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

function Invoke-WorkflowRun
{
  param([Parameter(Mandatory)][string]$Name)
  & ([scriptblock]::Create((Get-WorkflowRun -Name $Name)))
}

function Assert-AllLatestAre
{
  param([Parameter(Mandatory)][hashtable]$Expected)
  foreach ($repository in $Expected.Keys)
  {
    Assert-That ($script:tags["$repository`:latest"].manifest_digest -ceq $Expected[$repository].manifest_digest) "latest tag did not match expected digest: $repository"
  }
}

$prewriteName = 'Final revalidate official GitHub Release before registry login'
$promotionName = 'Promote each official release image to latest as a recoverable set'
$receiptName = 'Write latest-promotion receipt'

Set-Scenario -PostWriteReleaseTag 'v1.2.4' -PostWriteSourceCommit ('4' * 40 -join '')
$staleAbort = $false
try
{ Invoke-WorkflowRun -Name $prewriteName
} catch
{ $staleAbort = $true
}
$prewrite = $script:files[(Join-Path $env:RECEIPT_DIR 'prewrite-release-revalidation.json')] | ConvertFrom-Json
Assert-That $staleAbort 'stale prewrite release must abort'
Assert-That ($prewrite.status -ceq 'mismatch') 'stale prewrite release must record mismatch'
Assert-That ($script:createCalls.Count -eq 0) 'stale prewrite release must perform no tag writes'
Invoke-WorkflowRun -Name $receiptName
$staleReceipt = $script:files[(Join-Path $env:RECEIPT_DIR 'latest-promotion-receipt.json')] | ConvertFrom-Json
Assert-That ($staleReceipt.outcome -ceq 'no_write_stale_abort') 'stale prewrite receipt must distinguish the no-write abort'

Set-Scenario
Invoke-WorkflowRun -Name $promotionName
$success = $script:files[(Join-Path $env:RECEIPT_DIR 'promotion.json')] | ConvertFrom-Json
Assert-That ($success.outcome -ceq 'success') 'all verified writes must record success'
Assert-That ($success.rollback.attempted -eq $false) 'success must not roll back'
Assert-That ($script:createCalls.Count -eq 3) 'success must write each latest tag once'
foreach ($image in $success.updated_latest_images)
{
  Assert-That ($script:tags[$image.reference].manifest_digest -ceq $image.manifest_digest) "success latest readback mismatch: $($image.reference)"
}

Set-Scenario
$firstRepository = 'ghcr.io/thebtf/engram'
$script:failReadbackTarget = "$firstRepository`:latest"
$script:failReadbackManifest = (($script:immutable.Values | Where-Object { $_.version -ceq $script:releaseTag -and $_.repository -ceq $firstRepository })[0]).manifest_digest
$readbackFailure = $false
try
{ Invoke-WorkflowRun -Name $promotionName
} catch
{ $readbackFailure = $true
}
$readbackRollback = $script:files[(Join-Path $env:RECEIPT_DIR 'promotion.json')] | ConvertFrom-Json
Assert-That $readbackFailure 'readback failure must fail the promotion step'
Assert-That ($readbackRollback.outcome -ceq 'rolled_back') 'readback failure must record successful rollback'
Assert-That ($readbackRollback.rollback.restored_images.Count -eq 1) 'readback failure must restore the affected tag'
Assert-AllLatestAre -Expected $script:oldByRepository

Set-Scenario
$secondRepository = 'ghcr.io/thebtf/engram-operator-console'
$script:failCreateTarget = "$secondRepository`:latest"
$script:failCreateSource = "$secondRepository@$((($script:immutable.Values | Where-Object { $_.version -ceq $script:releaseTag -and $_.repository -ceq $secondRepository })[0]).manifest_digest)"
$createFailure = $false
try
{ Invoke-WorkflowRun -Name $promotionName
} catch
{ $createFailure = $true
}
$createRollback = $script:files[(Join-Path $env:RECEIPT_DIR 'promotion.json')] | ConvertFrom-Json
Assert-That $createFailure 'create failure must fail the promotion step'
Assert-That ($createRollback.outcome -ceq 'rolled_back') 'create failure must record successful rollback'
Assert-That ($createRollback.rollback.restored_images.Count -eq 2) 'create failure must restore the attempted and prior tags'
Assert-AllLatestAre -Expected $script:oldByRepository

Set-Scenario -PostWriteReleaseTag 'v1.2.4' -PostWriteSourceCommit ('4' * 40 -join '')
$postWriteMismatch = $false
try
{ Invoke-WorkflowRun -Name $promotionName
} catch
{ $postWriteMismatch = $true
}
$mismatchRollback = $script:files[(Join-Path $env:RECEIPT_DIR 'promotion.json')] | ConvertFrom-Json
Assert-That $postWriteMismatch 'post-write release mismatch must fail the promotion step'
Assert-That ($mismatchRollback.outcome -ceq 'rolled_back') 'post-write mismatch must record successful rollback'
Assert-That ($mismatchRollback.post_write_release_revalidation.status -ceq 'mismatch') 'post-write mismatch must be recorded'
Assert-That ($mismatchRollback.rollback.restored_images.Count -eq 3) 'post-write mismatch must restore all tags'
Assert-AllLatestAre -Expected $script:oldByRepository

Set-Scenario -PostWriteReleaseTag 'v1.2.4' -PostWriteSourceCommit ('4' * 40 -join '')
$firstRepository = 'ghcr.io/thebtf/engram'
$script:failCreateTarget = "$firstRepository`:latest"
$script:failCreateSource = "$firstRepository@$($script:oldByRepository[$firstRepository].manifest_digest)"
$rollbackFailure = $false
try
{ Invoke-WorkflowRun -Name $promotionName
} catch
{ $rollbackFailure = $true
}
$failedRollback = $script:files[(Join-Path $env:RECEIPT_DIR 'promotion.json')] | ConvertFrom-Json
Assert-That $rollbackFailure 'rollback failure must fail the promotion step loudly'
Assert-That ($failedRollback.outcome -ceq 'rollback_failed') 'rollback failure must be recorded distinctly'
Assert-That ($failedRollback.rollback.failures.Count -eq 1) 'rollback failure must record the failed tag'
Assert-That ($failedRollback.rollback.restored_images.Count -eq 2) 'rollback failure must continue restoring other tags'

'PASS: stale-abort receipt, success, readback rollback, create-failure rollback, post-write mismatch rollback, and rollback failure'
