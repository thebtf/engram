param(
  [Parameter(Mandatory)][string]$WorkflowPath,
  [Parameter(Mandatory)][string]$RecoveryWorkflowPath,
  [Parameter(Mandatory)][string]$GatePath
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Assert-That([bool]$Condition, [string]$Message)
{
  if (-not $Condition)
  { throw $Message
  }
}

$global:repositories = @('ghcr.io/thebtf/engram','ghcr.io/thebtf/engram-operator-console','ghcr.io/thebtf/engram-postgres')
$global:runId = '8675309'
$global:attempt = '3'
$global:workflowHead = '1' * 40 -join ''
$global:releaseCommit = '2' * 40 -join ''
$global:releaseTag = 'v1.2.3'
$global:details = 'https://github.com/thebtf/engram/actions/runs/8675309'

function New-Digest([char]$Character)
{ return "sha256:$(''.PadLeft(64, $Character))"
}
function New-Identity([string]$State = 'unknown', [string]$Repository = '', [string]$Digest = '')
{
  return [ordered]@{
    state = $State
    immutable_reference = if ($State -eq 'present')
    { "$Repository@$Digest"
    } else
    { $null
    }
    manifest_digest = if ($State -eq 'present')
    { $Digest
    } else
    { $null
    }
  }
}
function New-Target([int]$Index, [string]$State = 'unknown')
{
  $repository = $global:repositories[$Index]
  $previousDigest = New-Digest ([char]([int][char]'a' + $Index))
  $intendedDigest = New-Digest ([char]([int][char]'7' + $Index))
  $previous = if ($State -ceq 'unknown')
  { New-Identity
  } else
  { New-Identity -State 'present' -Repository $repository -Digest $previousDigest
  }
  $intended = New-Identity -State 'present' -Repository $repository -Digest $intendedDigest
  $observed = switch ($State)
  {
    { $_ -in @('updated','rollback_pending','rollback_failed') }
    { $intended
    }
    { $_ -in @('captured','mutation_pending','restored') }
    { $previous
    }
    default
    { New-Identity
    }
  }
  return [ordered]@{
    repository = $repository
    reference = "$repository`:latest"
    previous = $previous
    intended = $intended
    observed = $observed
    state = $State
    failure = $null
  }
}
function New-Snapshot([string]$Phase, [string]$Outcome, [string[]]$States = @('captured','captured','captured'))
{
  $targets = for ($index = 0; $index -lt 3; $index++)
  { New-Target -Index $index -State $States[$index]
  }
  return [ordered]@{
    schema_version = 1
    run = [ordered]@{ id=$global:runId; attempt=$global:attempt; head_sha=$global:workflowHead; external_id="latest-promotion:$($global:runId):$($global:attempt)"; details_url=$global:details }
    release = [ordered]@{ tag=$global:releaseTag; source_commit=$global:releaseCommit }
    phase = $Phase
    outcome = $Outcome
    targets = @($targets)
    revalidation = [ordered]@{ status='not_started'; observed_release_tag=$null; observed_source_commit=$null; failure=$null }
    rollback = [ordered]@{ attempted=$false; outcome='not_needed'; failures=@() }
    failure = $null
  }
}
function Set-PassedRevalidation($Snapshot)
{
  $Snapshot.revalidation = [ordered]@{ status='passed'; observed_release_tag=$global:releaseTag; observed_source_commit=$global:releaseCommit; failure=$null }
}
function New-TerminalSnapshot([string]$Phase, [string]$Outcome)
{
  switch ("$Phase/$Outcome")
  {
    'completed/success'
    {
      $snapshot = New-Snapshot -Phase $Phase -Outcome $Outcome -States @('updated','updated','updated')
      Set-PassedRevalidation $snapshot
      return $snapshot
    }
    'failed/bootstrap_required_no_write'
    {
      $snapshot = New-Snapshot -Phase $Phase -Outcome $Outcome
      $snapshot.targets[0].previous = New-Identity -State 'absent'
      $snapshot.targets[0].observed = New-Identity -State 'absent'
      $snapshot.failure = 'latest tag is absent and requires manual bootstrap'
      return $snapshot
    }
    'failed/failed_before_write'
    {
      $snapshot = New-Snapshot -Phase $Phase -Outcome $Outcome -States @('unknown','unknown','unknown')
      $snapshot.failure = 'promotion did not begin'
      return $snapshot
    }
    'failed/rolled_back'
    {
      $snapshot = New-Snapshot -Phase $Phase -Outcome $Outcome -States @('restored','restored','restored')
      Set-PassedRevalidation $snapshot
      $snapshot.rollback.attempted = $true
      $snapshot.rollback.outcome = 'succeeded'
      $snapshot.failure = 'interrupted promotion was safely rolled back'
      return $snapshot
    }
    'failed/rollback_failed'
    {
      $snapshot = New-Snapshot -Phase $Phase -Outcome $Outcome -States @('rollback_failed','restored','restored')
      Set-PassedRevalidation $snapshot
      $snapshot.targets[0].failure = 'rollback command failed'
      $snapshot.rollback.attempted = $true
      $snapshot.rollback.outcome = 'failed'
      $snapshot.rollback.failures = @([ordered]@{ target=$snapshot.targets[0].reference; failure='rollback command failed' })
      $snapshot.failure = 'one or more rollback commands failed'
      return $snapshot
    }
    'failed/rollback_readback_error'
    {
      $snapshot = New-Snapshot -Phase $Phase -Outcome $Outcome -States @('readback_error','restored','restored')
      Set-PassedRevalidation $snapshot
      $snapshot.targets[0].observed = [ordered]@{ state='readback_error'; immutable_reference=$null; manifest_digest=$null; failure='readback failed' }
      $snapshot.targets[0].failure = 'readback failed'
      $snapshot.rollback.attempted = $true
      $snapshot.rollback.outcome = 'readback_error'
      $snapshot.rollback.failures = @([ordered]@{ target=$snapshot.targets[0].reference; failure='readback failed' })
      $snapshot.failure = 'one or more rollback readbacks failed'
      return $snapshot
    }
    default
    { return New-Snapshot -Phase $Phase -Outcome $Outcome
    }
  }
}
function New-CheckRun([string]$Text, [string]$Status = 'in_progress', [string]$Conclusion = '')
{
  return [ordered]@{
    id='41'; name='latest-promotion-journal'; head_sha=$global:workflowHead; external_id="latest-promotion:$($global:runId):$($global:attempt)"; details_url=$global:details
    status=$Status; conclusion=if ([string]::IsNullOrEmpty($Conclusion))
    { $null
    } else
    { $Conclusion
    }
    output=[ordered]@{ title='Latest promotion journal'; summary='seed'; text=$Text }
  }
}
function Snapshot-Json($Snapshot)
{ return $Snapshot | ConvertTo-Json -Compress -Depth 12
}

function Reset-Scenario([switch]$WithSources)
{
  $global:temp = Join-Path ([System.IO.Path]::GetTempPath()) ("engram-promotion-matrix-" + [guid]::NewGuid().ToString('N'))
  $global:receipt = Join-Path $global:temp 'receipt'
  New-Item -ItemType Directory -Path $global:receipt -Force | Out-Null
  $env:RUNNER_TEMP = $global:temp
  $env:GITHUB_SERVER_URL = 'https://github.com'
  $env:GITHUB_REPOSITORY = 'thebtf/engram'
  $global:runs = @{}
  $global:tags = @{}
  $global:immutable = @{}
  $global:patchCount = 0
  $global:createCount = 0
  $global:patchAfterCompleted = 0
  $global:registryWrites = [System.Collections.Generic.List[object]]::new()
  $global:patchHistory = [System.Collections.Generic.List[object]]::new()
  $global:postFault = 'none'
  $global:patchFault = 'none'
  $global:patchFaults = [System.Collections.Generic.List[string]]::new()
  $global:patchFaultSummary = ''
  $global:patchFaultSummaryKind = 'none'
  $global:failReadAfterPatch = $false
  $global:failNextJournalRead = $false
  $global:interruptSummary = ''
  $global:interruptOccurrence = 0
  $global:interruptSeen = 0
  $global:rollbackCommandFailure = $false
  $global:rollbackReadbackFailure = $false
  $global:interruptAfterRollbackWrite = $false
  $global:rollbackGuardFault = 'none'
  $global:officialReleaseHead = $global:releaseCommit
  $global:releaseVersion = $global:releaseTag
  $global:releaseTagReadCount = 0
  $global:releaseCommitReadCount = 0
  $global:releaseTagFailReads = [System.Collections.Generic.List[int]]::new()
  $global:releaseCommitFailReads = [System.Collections.Generic.List[int]]::new()
  for ($index = 0; $index -lt 3; $index++)
  {
    $target = New-Target -Index $index -State 'captured'
    $old = [pscustomobject]@{ manifest_digest=$target.previous.manifest_digest }
    $new = [pscustomobject]@{ manifest_digest=$target.intended.manifest_digest }
    $global:tags[$target.reference] = $old
    $global:immutable[$target.previous.immutable_reference] = $old
    $global:immutable[$target.intended.immutable_reference] = $new
  }
  if ($WithSources)
  {
    $images = for ($index = 0; $index -lt 3; $index++)
    {
      $target = New-Target -Index $index -State 'captured'
      [ordered]@{ repository=$target.repository; immutable_reference=$target.intended.immutable_reference; manifest_digest=$target.intended.manifest_digest }
    }
    [ordered]@{ release_tag=$global:releaseTag; source_commit=$global:releaseCommit; images=@($images) } | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $global:receipt 'sources.json') -Encoding utf8NoBOM
  }
}
function Remove-Scenario
{ if (Test-Path -LiteralPath $global:temp)
  { Remove-Item -Recurse -Force -LiteralPath $global:temp
  }
}
function Seed-Journal($Snapshot)
{ $global:runs['41'] = New-CheckRun -Text (Snapshot-Json $Snapshot)
}
function Get-Journal
{ return $global:runs['41']
}
function Get-JournalSnapshot
{ return (Get-Journal).output.text | ConvertFrom-Json
}
function Invoke-Gate([string]$Mode, [switch]$AllowMissing, [switch]$RecoveryHandoff)
{
  $arguments = @{
    Mode = $Mode
    RepositoryName = 'thebtf/engram'
    RunId = $global:runId
    RunAttempt = $global:attempt
    HeadSha = $global:workflowHead
    ReleaseTag = if ($RecoveryHandoff)
    { ''
    } else
    { $global:releaseTag
    }
    ReleaseCommit = if ($RecoveryHandoff)
    { ''
    } else
    { $global:releaseCommit
    }
    ReceiptDir = if ($RecoveryHandoff)
    { ''
    } else
    { $global:receipt
    }
    AllowMissingJournal = [bool]$AllowMissing
  }
  return & $GatePath @arguments
}
function Assert-Terminal([string]$Outcome, [string]$Conclusion)
{
  $journal = Get-Journal
  Assert-That ($journal.status -ceq 'completed' -and $journal.conclusion -ceq $Conclusion) "journal must complete as $Conclusion, got status=$($journal.status) conclusion=$($journal.conclusion) outcome=$((Get-JournalSnapshot).outcome) failure=$((Get-JournalSnapshot).failure)"
  $snapshot = Get-JournalSnapshot
  Assert-That ($snapshot.outcome -ceq $Outcome) "journal outcome must be $Outcome, got $($snapshot.outcome): $($snapshot.failure)"
  Assert-That ($global:patchAfterCompleted -eq 0) 'no PATCH may occur after completion was observed'
  $patches = $global:patchCount
  $writes = $global:registryWrites.Count
  Invoke-Gate -Mode Reconcile -RecoveryHandoff | Out-Null
  Assert-That ($global:patchCount -eq $patches -and $global:registryWrites.Count -eq $writes) 'completed replay must issue no PATCH or registry mutation'
}
function Assert-NoUnsafeMutation
{ Assert-That ($global:registryWrites.Count -eq 0) 'scenario performed an unsafe registry mutation'
}
function Expect-GateFailure([scriptblock]$Action, [string]$Message)
{
  $failed = $false
  try
  { & $Action | Out-Null
  } catch
  { $failed = $true
  }
  Assert-That $failed $Message
}

function global:gh
{
  param([Parameter(ValueFromRemainingArguments)][string[]]$Arguments)
  $global:LASTEXITCODE = 0
  $request = $Arguments -join ' '
  if ($request -match '/commits/[0-9a-f]{40}/check-runs\?')
  {
    return ([ordered]@{ total_count=$global:runs.Count; check_runs=@($global:runs.Values) } | ConvertTo-Json -Compress -Depth 14)
  }
  if ($request -match 'releases/latest')
  {
    $global:releaseTagReadCount++
    if ($global:releaseTagFailReads.Contains($global:releaseTagReadCount))
    { $global:LASTEXITCODE=1; return 'release read failed'
    }
    return $global:releaseVersion
  }
  if ($request -match '/commits/')
  {
    $global:releaseCommitReadCount++
    if ($global:releaseCommitFailReads.Contains($global:releaseCommitReadCount))
    { $global:LASTEXITCODE=1; return 'commit read failed'
    }
    return $global:officialReleaseHead
  }
  if ($request -match 'check-runs/([1-9][0-9]*)$' -and $request -notmatch '--method')
  {
    if ($global:failNextJournalRead)
    { $global:failNextJournalRead=$false; $global:LASTEXITCODE=1; return 'journal reread failed'
    }
    return ($global:runs[$Matches[1]] | ConvertTo-Json -Compress -Depth 14)
  }
  $inputIndex = [array]::IndexOf($Arguments, '--input')
  if ($inputIndex -lt 0)
  { throw "unexpected gh invocation: $request"
  }
  $body = Get-Content -LiteralPath $Arguments[$inputIndex + 1] -Raw | ConvertFrom-Json
  if ($request -match '--method POST')
  {
    $global:createCount++
    $run = [ordered]@{ id='41'; name=$body.name; head_sha=$body.head_sha; external_id=$body.external_id; details_url=$body.details_url; status=$body.status; conclusion=$null; output=$body.output }
    $global:runs['41'] = $run
    switch ($global:postFault)
    {
      'lost'
      { $global:LASTEXITCODE=1; return 'lost create response'
      }
      'invalid-id'
      { $response=[ordered]@{}+$run; $response.id='invalid'; return ($response | ConvertTo-Json -Compress -Depth 14)
      }
      default
      { return ($run | ConvertTo-Json -Compress -Depth 14)
      }
    }
  }
  if ($request -match '--method PATCH' -and $request -match 'check-runs/([1-9][0-9]*)')
  {
    $id = $Matches[1]
    $run = $global:runs[$id]
    if ($run.status -eq 'completed')
    { $global:patchAfterCompleted++
    }
    $global:patchCount++
    $summary = [string]$body.output.summary
    $fault = 'none'
    if (-not [string]::IsNullOrEmpty($global:patchFaultSummary) -and $summary -ceq $global:patchFaultSummary)
    {
      $fault = $global:patchFaultSummaryKind
      $global:patchFaultSummary = ''
      $global:patchFaultSummaryKind = 'none'
    } elseif ($global:patchFaults.Count -gt 0)
    {
      $fault = $global:patchFaults[0]
      $global:patchFaults.RemoveAt(0)
    } else
    {
      $fault = $global:patchFault
      $global:patchFault = 'none'
    }
    if ($fault -eq 'reject')
    { $global:LASTEXITCODE=1; return 'rejected patch'
    }
    $run.status = [string]$body.status
    $run.conclusion = if ($null -eq $body.PSObject.Properties['conclusion'])
    { $null
    } else
    { [string]$body.conclusion
    }
    $run.output = $body.output
    $global:patchHistory.Add([pscustomobject]@{ summary=$summary; status=[string]$run.status; writes=$global:registryWrites.Count; text=[string]$body.output.text })
    if ($run.status -ceq 'in_progress' -and $global:rollbackGuardFault -cne 'none')
    {
      $snapshot = $run.output.text | ConvertFrom-Json
      $pending = @($snapshot.targets | Where-Object { [string]$_.state -ceq 'rollback_pending' })
      if ($pending.Count -eq 1)
      {
        $guardFault = $global:rollbackGuardFault
        $global:rollbackGuardFault = 'none'
        if ($guardFault -ceq 'release_changed')
        { $global:officialReleaseHead = '4' * 40 -join ''
        } elseif ($guardFault -ceq 'tag_changed')
        { $global:tags[[string]$pending[0].reference] = [pscustomobject]@{ manifest_digest=(New-Digest 'f') }
        }
      }
    }
    if (-not [string]::IsNullOrEmpty($global:interruptSummary) -and $summary -ceq $global:interruptSummary)
    {
      $global:interruptSeen++
      if ($global:interruptSeen -eq $global:interruptOccurrence)
      { throw 'simulated runner loss after durable PATCH'
      }
    }
    if ($fault -eq 'ambiguous')
    {
      if ($global:failReadAfterPatch)
      { $global:failReadAfterPatch=$false; $global:failNextJournalRead=$true
      }
      $global:LASTEXITCODE=1
      return 'lost patch response'
    }
    if ($fault -eq 'malformed')
    { return '{broken'
    }
    return ($run | ConvertTo-Json -Compress -Depth 14)
  }
  throw "unexpected gh invocation: $request"
}

function global:docker
{
  param([Parameter(ValueFromRemainingArguments)][string[]]$Arguments)
  $global:LASTEXITCODE = 0
  if ($Arguments[0] -ne 'buildx' -or $Arguments[1] -ne 'imagetools')
  { throw "unexpected docker invocation: $($Arguments -join ' ')"
  }
  if ($Arguments[2] -eq 'inspect')
  {
    $reference = $Arguments[3]
    if (-not $global:tags.ContainsKey($reference))
    { $global:LASTEXITCODE=1; return 'manifest unknown'
    }
    $identity = $global:tags[$reference]
    if ($global:rollbackReadbackFailure -and $global:registryWrites.Count -gt 0 -and $global:registryWrites[$global:registryWrites.Count-1].target -ceq $reference -and $global:registryWrites[$global:registryWrites.Count-1].rollback)
    {
      $global:rollbackReadbackFailure = $false
      $global:LASTEXITCODE=1
      return 'simulated rollback readback failure'
    }
    return (@{ digest=$identity.manifest_digest } | ConvertTo-Json -Compress)
  }
  if ($Arguments[2] -eq 'create')
  {
    $target = $Arguments[[array]::IndexOf($Arguments,'--tag')+1]
    $source = $Arguments[$Arguments.Count-1]
    $rollback = $source -match '@sha256:[a-c]'
    $global:registryWrites.Add([pscustomobject]@{ target=$target; source=$source; rollback=$rollback })
    if ($rollback -and $global:rollbackCommandFailure)
    { $global:rollbackCommandFailure=$false; $global:LASTEXITCODE=1; return
    }
    if (-not $global:immutable.ContainsKey($source))
    { $global:LASTEXITCODE=1; return
    }
    $global:tags[$target] = $global:immutable[$source]
    if ($rollback -and $global:interruptAfterRollbackWrite)
    {
      $global:interruptAfterRollbackWrite = $false
      throw 'simulated runner loss after rollback registry mutation'
    }
    return
  }
  throw "unexpected docker invocation: $($Arguments -join ' ')"
}

$workflow = [System.IO.File]::ReadAllText($WorkflowPath)
$recovery = [System.IO.File]::ReadAllText($RecoveryWorkflowPath)
$gate = [System.IO.File]::ReadAllText($GatePath)
foreach ($required in @('Check out trusted production-gate implementation','latest-promotion-journal.ps1','-Mode EnsureJournal','-Mode Promote','-Mode Reconcile','Reconcile latest-promotion journal on the same runner','-ReleaseCommit ([string]$release.source_commit)'))
{
  Assert-That ($workflow.Contains($required)) "promotion workflow lacks $required"
}
foreach ($required in @('workflow_run:','workflows: ["Promote Latest Release Images"]','workflow_dispatch:','original_run_id','Check out trusted default-branch recovery code','Validate original promotion run identity','Discover owned promotion journal','Login to GHCR for owned rollback only','Independently reconcile promotion journal',"-HeadSha `$env:ORIGINAL_HEAD_SHA"))
{
  Assert-That ($recovery.Contains($required)) "recovery workflow lacks $required"
}
foreach ($required in @('Find-Journal','output = New-CheckOutput','text = ConvertTo-SnapshotJson','Invoke-JournalPatch','Get-TerminalConclusion','official release identity changed; registry mutation is forbidden','latest-promotion ambiguous PATCH observed a contradictory completed journal','Complete-ActionRequired'))
{
  Assert-That ($gate.Contains($required)) "shared gate lacks $required"
}
Assert-That ($global:workflowHead -cne $global:releaseCommit) 'fixture must distinguish workflow-run head from release source commit'
Assert-That (([regex]::Matches($workflow, [regex]::Escape('-HeadSha $env:GITHUB_SHA'))).Count -eq 3) 'all promotion gate calls must use the recoverable workflow-run head'
Assert-That (-not $workflow.Contains('-HeadSha ([string]$release.source_commit)')) 'release source commit must not be used as the check-run anchor'
Assert-That ($recovery.Contains("Independently reconcile promotion journal`n        if: always() && steps.journal.outputs.journal_id != ''")) 'independent reconciliation must still run after GHCR login failure'
Assert-That (-not $workflow.Contains('Remove-CreatedLatestTag') -and -not $recovery.Contains('api --method DELETE') -and -not $gate.Contains('api --method DELETE')) 'promotion surfaces must never delete package versions'

$global:scenarioCount = 0
try
{
  foreach ($postFault in @('lost','invalid-id'))
  {
    Reset-Scenario -WithSources; $global:scenarioCount++; $global:postFault=$postFault
    $id = Invoke-Gate -Mode EnsureJournal | Select-Object -Last 1
    Assert-That ([string]$id -ceq '41') "accepted POST $postFault must rediscover journal"
    $again = Invoke-Gate -Mode EnsureJournal | Select-Object -Last 1
    Assert-That ([string]$again -ceq '41' -and $global:createCount -eq 1 -and $global:runs.Count -eq 1) 'uncertain POST replay must not create a duplicate'
    Invoke-Gate -Mode Promote | Out-Null
    Assert-Terminal -Outcome 'success' -Conclusion 'success'
    Remove-Scenario
  }

  Reset-Scenario -WithSources; $global:scenarioCount++
  $id = Invoke-Gate -Mode EnsureJournal | Select-Object -Last 1
  Assert-That ([string]$id -ceq '41') 'journal setup failed'
  Invoke-Gate -Mode Reconcile -RecoveryHandoff | Out-Null
  Assert-Terminal -Outcome 'failed_before_write' -Conclusion 'failure'
  Remove-Scenario

  $terminalCases = @(
    @('completed','success','success'),
    @('failed','bootstrap_required_no_write','neutral'),
    @('failed','rolled_back','neutral'),
    @('failed','failed_before_write','failure'),
    @('failed','rollback_failed','failure'),
    @('failed','rollback_readback_error','failure')
  )
  $caseIndex = 0
  foreach ($case in $terminalCases)
  {
    Reset-Scenario; $global:scenarioCount++
    Seed-Journal (New-TerminalSnapshot -Phase $case[0] -Outcome $case[1])
    $global:patchFault = if (($caseIndex++ % 2) -eq 0)
    { 'reject'
    } else
    { 'ambiguous'
    }
    Invoke-Gate -Mode Reconcile | Out-Null
    Assert-Terminal -Outcome $case[1] -Conclusion $case[2]
    Remove-Scenario
  }

  Reset-Scenario; $global:scenarioCount++
  Seed-Journal (New-Snapshot -Phase 'capturing_previous_latest' -Outcome 'pending' -States @('captured','unknown','unknown'))
  Invoke-Gate -Mode Reconcile | Out-Null
  Assert-Terminal -Outcome 'action_required' -Conclusion 'failure'; Assert-NoUnsafeMutation
  Remove-Scenario

  Reset-Scenario; $global:scenarioCount++
  Seed-Journal (New-Snapshot -Phase 'writing_latest' -Outcome 'pending')
  Invoke-Gate -Mode Reconcile | Out-Null
  Assert-Terminal -Outcome 'rolled_back' -Conclusion 'neutral'; Assert-NoUnsafeMutation
  Remove-Scenario

  Reset-Scenario; $global:scenarioCount++
  $snapshot = New-Snapshot -Phase 'writing_latest' -Outcome 'pending' -States @('mutation_pending','captured','captured')
  $global:tags[$snapshot.targets[0].reference] = $global:immutable[$snapshot.targets[0].intended.immutable_reference]
  Seed-Journal $snapshot
  Invoke-Gate -Mode Reconcile | Out-Null
  Assert-Terminal -Outcome 'rolled_back' -Conclusion 'neutral'
  Assert-That ($global:registryWrites.Count -eq 1 -and $global:registryWrites[0].rollback) 'partial forward mutation must rollback only the intended tag'
  Assert-That ($global:patchHistory[0].summary -ceq 'rolling_back/pending' -and $global:patchHistory[0].writes -eq 0) 'recovery must classify read-only before its first durable rolling-back PATCH'
  Remove-Scenario

  Reset-Scenario; $global:scenarioCount++
  $snapshot = New-Snapshot -Phase 'post_write_release_revalidation' -Outcome 'pending' -States @('updated','updated','updated')
  $snapshot.revalidation.status = 'started'
  foreach ($target in $snapshot.targets)
  { $global:tags[$target.reference] = $global:immutable[$target.intended.immutable_reference]
  }
  Seed-Journal $snapshot
  Invoke-Gate -Mode Reconcile | Out-Null
  Assert-Terminal -Outcome 'rolled_back' -Conclusion 'neutral'
  Assert-That ($global:registryWrites.Count -eq 3) 'post-write interstitial must rollback all owned intended tags'
  Remove-Scenario

  Reset-Scenario; $global:scenarioCount++
  $snapshot = New-Snapshot -Phase 'rolling_back' -Outcome 'pending' -States @('restored','rollback_pending','updated')
  Set-PassedRevalidation $snapshot
  $snapshot.rollback.attempted = $true
  $snapshot.rollback.outcome = 'pending'
  foreach ($index in 1,2)
  { $target=$snapshot.targets[$index]; $global:tags[$target.reference]=$global:immutable[$target.intended.immutable_reference]
  }
  Seed-Journal $snapshot
  Invoke-Gate -Mode Reconcile | Out-Null
  Assert-Terminal -Outcome 'rolled_back' -Conclusion 'neutral'
  Assert-That ($global:registryWrites.Count -eq 2) 'rolling_back interstitial must resume only remaining intended tags'
  Remove-Scenario

  Reset-Scenario; $global:scenarioCount++
  $snapshot = New-Snapshot -Phase 'writing_latest' -Outcome 'pending' -States @('mutation_pending','captured','captured')
  $global:tags[$snapshot.targets[0].reference] = $global:immutable[$snapshot.targets[0].intended.immutable_reference]
  Seed-Journal $snapshot
  Remove-Item -Recurse -Force -LiteralPath $global:receipt
  $global:receipt = ''
  $discovered = Invoke-Gate -Mode Discover -AllowMissing -RecoveryHandoff | Select-Object -Last 1
  Assert-That ([string]$discovered -ceq '41') 'recovery workflow handoff must discover the journal on the original run head'
  Invoke-Gate -Mode Reconcile -RecoveryHandoff | Out-Null
  Assert-Terminal -Outcome 'rolled_back' -Conclusion 'neutral'
  Remove-Scenario

  Reset-Scenario; $global:scenarioCount++
  $snapshot = New-Snapshot -Phase 'writing_latest' -Outcome 'pending' -States @('updated','captured','captured')
  $global:tags[$snapshot.targets[0].reference] = $global:immutable[$snapshot.targets[0].intended.immutable_reference]
  $global:rollbackCommandFailure = $true
  Seed-Journal $snapshot
  Invoke-Gate -Mode Reconcile | Out-Null
  Assert-Terminal -Outcome 'rollback_failed' -Conclusion 'failure'
  Remove-Scenario

  Reset-Scenario; $global:scenarioCount++
  $snapshot = New-Snapshot -Phase 'writing_latest' -Outcome 'pending' -States @('updated','captured','captured')
  $global:tags[$snapshot.targets[0].reference] = $global:immutable[$snapshot.targets[0].intended.immutable_reference]
  $global:rollbackReadbackFailure = $true
  Seed-Journal $snapshot
  Invoke-Gate -Mode Reconcile | Out-Null
  Assert-Terminal -Outcome 'rollback_readback_error' -Conclusion 'failure'
  Remove-Scenario

  foreach ($failureState in @('rollback_failed','readback_error'))
  {
    Reset-Scenario; $global:scenarioCount++
    $snapshot = New-Snapshot -Phase 'writing_latest' -Outcome 'pending' -States @('updated','captured','captured')
    $global:tags[$snapshot.targets[0].reference] = $global:immutable[$snapshot.targets[0].intended.immutable_reference]
    if ($failureState -ceq 'rollback_failed')
    { $global:rollbackCommandFailure = $true
    } else
    { $global:rollbackReadbackFailure = $true
    }
    Seed-Journal $snapshot
    $global:interruptSummary = 'rolling_back/pending'
    $global:interruptOccurrence = 3
    Expect-GateFailure { Invoke-Gate -Mode Reconcile -RecoveryHandoff } "recovery-emitted $failureState snapshot must be replayable after interruption"
    Assert-That ([string](Get-JournalSnapshot).targets[0].state -ceq $failureState) "interruption must preserve the recovery-emitted $failureState snapshot"
    Invoke-Gate -Mode Reconcile -RecoveryHandoff | Out-Null
    Assert-Terminal -Outcome 'rolled_back' -Conclusion 'neutral'
    $expectedWrites = if ($failureState -ceq 'rollback_failed')
    { 2
    } else
    { 1
    }
    Assert-That ($global:registryWrites.Count -eq $expectedWrites) "replay from $failureState must perform only the necessary rollback mutation"
    Remove-Scenario
  }

  Reset-Scenario; $global:scenarioCount++
  $snapshot = New-Snapshot -Phase 'writing_latest' -Outcome 'pending' -States @('updated','captured','captured')
  $global:tags[$snapshot.targets[0].reference] = $global:immutable[$snapshot.targets[0].intended.immutable_reference]
  Seed-Journal $snapshot
  $global:interruptAfterRollbackWrite = $true
  Expect-GateFailure { Invoke-Gate -Mode Reconcile -RecoveryHandoff } 'rollback registry mutation must expose the pre-journal runner-loss window'
  $interrupted = Get-JournalSnapshot
  Assert-That ([string]$interrupted.targets[0].state -ceq 'rollback_pending') 'runner loss before the observed-state PATCH must leave rollback_pending durable'
  Assert-That ([string]$global:tags[$snapshot.targets[0].reference].manifest_digest -ceq [string]$snapshot.targets[0].previous.manifest_digest) 'runner loss must occur after the rollback registry mutation'
  Invoke-Gate -Mode Reconcile -RecoveryHandoff | Out-Null
  $coherentReplay = $global:patchHistory[2].text | ConvertFrom-Json
  Assert-That ([string]$coherentReplay.phase -ceq 'rolling_back' -and [string]$coherentReplay.targets[0].state -ceq 'restored' -and [string]$coherentReplay.rollback.outcome -ceq 'pending') 'replay must persist the observed previous digest before completion'
  Assert-Terminal -Outcome 'rolled_back' -Conclusion 'neutral'
  Assert-That ($global:registryWrites.Count -eq 1) 'replay must detect the previous digest and avoid a duplicate rollback mutation'
  Remove-Scenario

  Reset-Scenario; $global:scenarioCount++
  $global:runs['41'] = New-CheckRun -Text '{malformed'
  Invoke-Gate -Mode Reconcile | Out-Null
  Assert-Terminal -Outcome 'contradiction' -Conclusion 'failure'; Assert-NoUnsafeMutation
  Remove-Scenario

  Reset-Scenario; $global:scenarioCount++
  $snapshot = New-Snapshot -Phase 'writing_latest' -Outcome 'pending'; $snapshot.run.attempt = '99'
  Seed-Journal $snapshot
  Invoke-Gate -Mode Reconcile | Out-Null
  Assert-Terminal -Outcome 'contradiction' -Conclusion 'failure'; Assert-NoUnsafeMutation
  Remove-Scenario

  Reset-Scenario; $global:scenarioCount++
  Seed-Journal (New-Snapshot -Phase 'capturing_previous_latest' -Outcome 'pending' -States @('updated','captured','captured'))
  Invoke-Gate -Mode Reconcile | Out-Null
  Assert-Terminal -Outcome 'contradiction' -Conclusion 'failure'; Assert-NoUnsafeMutation
  Remove-Scenario

  Reset-Scenario; $global:scenarioCount++
  $snapshot = New-Snapshot -Phase 'writing_latest' -Outcome 'pending' -States @('updated','captured','captured')
  $global:tags[$snapshot.targets[0].reference] = $global:immutable[$snapshot.targets[0].intended.immutable_reference]
  $global:officialReleaseHead = '4' * 40 -join ''
  Seed-Journal $snapshot
  Invoke-Gate -Mode Reconcile | Out-Null
  Assert-Terminal -Outcome 'action_required' -Conclusion 'failure'; Assert-NoUnsafeMutation
  Remove-Scenario

  Reset-Scenario; $global:scenarioCount++
  $snapshot = New-Snapshot -Phase 'writing_latest' -Outcome 'pending' -States @('updated','captured','captured')
  $global:tags[$snapshot.targets[0].reference] = [pscustomobject]@{ manifest_digest=(New-Digest 'f') }
  Seed-Journal $snapshot
  Invoke-Gate -Mode Reconcile | Out-Null
  Assert-Terminal -Outcome 'action_required' -Conclusion 'failure'; Assert-NoUnsafeMutation
  Remove-Scenario

  foreach ($guardFault in @('release_changed','tag_changed'))
  {
    Reset-Scenario; $global:scenarioCount++
    $snapshot = New-Snapshot -Phase 'writing_latest' -Outcome 'pending' -States @('updated','captured','captured')
    $global:tags[$snapshot.targets[0].reference] = $global:immutable[$snapshot.targets[0].intended.immutable_reference]
    $global:rollbackGuardFault = $guardFault
    Seed-Journal $snapshot
    Invoke-Gate -Mode Reconcile | Out-Null
    Assert-Terminal -Outcome 'action_required' -Conclusion 'failure'; Assert-NoUnsafeMutation
    Remove-Scenario
  }

  Reset-Scenario; $global:scenarioCount++
  $snapshot = New-TerminalSnapshot -Phase 'failed' -Outcome 'failed_before_write'
  $run = New-CheckRun -Text (Snapshot-Json $snapshot) -Status completed -Conclusion failure
  $run.output.summary = 'failed/failed_before_write'
  $global:runs['41'] = $run
  Invoke-Gate -Mode Reconcile -RecoveryHandoff | Out-Null
  Assert-That ($global:patchCount -eq 0) 'completed journal replay must issue no PATCH'
  Remove-Scenario

  Reset-Scenario -WithSources; $global:scenarioCount++
  Invoke-Gate -Mode EnsureJournal | Out-Null
  $global:patchFaultSummary = 'completed/success'
  $global:patchFaultSummaryKind = 'malformed'
  Invoke-Gate -Mode Promote | Out-Null
  Assert-Terminal -Outcome 'success' -Conclusion 'success'
  Assert-That ($global:registryWrites.Count -eq 3) 'ambiguous completed/success acknowledgement must not trigger rollback'
  Remove-Scenario

  foreach ($occurrence in 1..7)
  {
    Reset-Scenario; $global:scenarioCount++
    $snapshot = New-Snapshot -Phase 'post_write_release_revalidation' -Outcome 'pending' -States @('updated','updated','updated')
    $snapshot.revalidation.status = 'started'
    foreach ($target in $snapshot.targets)
    { $global:tags[$target.reference] = $global:immutable[$target.intended.immutable_reference]
    }
    Seed-Journal $snapshot
    $global:interruptSummary = 'rolling_back/pending'
    $global:interruptOccurrence = $occurrence
    Expect-GateFailure { Invoke-Gate -Mode Reconcile -RecoveryHandoff } "recovery PATCH occurrence $occurrence must simulate runner loss"
    Invoke-Gate -Mode Reconcile -RecoveryHandoff | Out-Null
    Assert-Terminal -Outcome 'rolled_back' -Conclusion 'neutral'
    Assert-That ($global:registryWrites.Count -eq 3) "replay after recovery PATCH $occurrence must complete each rollback exactly once"
    Remove-Scenario
  }

  Reset-Scenario; $global:scenarioCount++
  $snapshot = New-Snapshot -Phase 'post_write_release_revalidation' -Outcome 'pending' -States @('updated','updated','updated')
  $snapshot.revalidation.status = 'started'
  foreach ($target in $snapshot.targets)
  { $global:tags[$target.reference] = $global:immutable[$target.intended.immutable_reference]
  }
  Seed-Journal $snapshot
  $global:interruptSummary = 'failed/rolled_back'
  $global:interruptOccurrence = 1
  Expect-GateFailure { Invoke-Gate -Mode Reconcile -RecoveryHandoff } 'terminal recovery PATCH must simulate lost acknowledgement'
  Invoke-Gate -Mode Reconcile -RecoveryHandoff | Out-Null
  Assert-Terminal -Outcome 'rolled_back' -Conclusion 'neutral'
  Assert-That ($global:registryWrites.Count -eq 3) 'terminal replay must not repeat rollback mutations'
  Remove-Scenario

  foreach ($apiFailure in @('initial-tag','initial-commit','jit-tag','jit-commit'))
  {
    Reset-Scenario; $global:scenarioCount++
    $snapshot = New-Snapshot -Phase 'writing_latest' -Outcome 'pending' -States @('updated','captured','captured')
    $global:tags[$snapshot.targets[0].reference] = $global:immutable[$snapshot.targets[0].intended.immutable_reference]
    switch ($apiFailure)
    {
      'initial-tag'
      { $global:releaseTagFailReads.Add(1)
      }
      'initial-commit'
      { $global:releaseCommitFailReads.Add(1)
      }
      'jit-tag'
      { $global:releaseTagFailReads.Add(2)
      }
      'jit-commit'
      { $global:releaseCommitFailReads.Add(2)
      }
    }
    Seed-Journal $snapshot
    Invoke-Gate -Mode Reconcile -RecoveryHandoff | Out-Null
    Assert-Terminal -Outcome 'action_required' -Conclusion 'failure'
    Assert-NoUnsafeMutation
    Remove-Scenario
  }

  $terminalContradictions = @(
    @{ name='success rollback attempted'; snapshot={ $s=New-TerminalSnapshot completed success; $s.rollback.attempted=$true; $s.rollback.outcome='succeeded'; $s } ; conclusion='success' },
    @{ name='rolled back not attempted'; snapshot={ $s=New-TerminalSnapshot failed rolled_back; $s.rollback.attempted=$false; $s } ; conclusion='neutral' },
    @{ name='rollback failures omitted'; snapshot={ $s=New-TerminalSnapshot failed rollback_failed; $s.rollback.failures=@(); $s } ; conclusion='failure' },
    @{ name='terminal failure omitted'; snapshot={ $s=New-TerminalSnapshot failed failed_before_write; $s.failure=$null; $s } ; conclusion='failure' },
    @{ name='revalidation commit contradicted'; snapshot={ $s=New-TerminalSnapshot completed success; $s.revalidation.observed_source_commit='4' * 40 -join ''; $s } ; conclusion='success' },
    @{ name='action required claims no rollback after restoration'; snapshot={ $s=New-TerminalSnapshot failed rolled_back; $s.outcome='action_required'; $s.rollback.attempted=$false; $s.rollback.outcome='not_attempted'; $s.failure='manual action required'; $s } ; conclusion='failure' },
    @{ name='rolled back target contradicted'; snapshot={ $s=New-TerminalSnapshot failed rolled_back; $s.targets[0].state='updated'; $s.targets[0].observed=$s.targets[0].intended; $s } ; conclusion='neutral' }
  )
  foreach ($case in $terminalContradictions)
  {
    Reset-Scenario; $global:scenarioCount++
    $snapshot = & $case.snapshot
    $run = New-CheckRun -Text (Snapshot-Json $snapshot) -Status completed -Conclusion $case.conclusion
    $run.output.summary = "$($snapshot.phase)/$($snapshot.outcome)"
    $global:runs['41'] = $run
    $before = [string]$run.output.text
    Expect-GateFailure { Invoke-Gate -Mode Reconcile -RecoveryHandoff } "completed contradiction must be rejected: $($case.name)"
    Assert-That ($global:patchCount -eq 0 -and [string](Get-Journal).output.text -ceq $before -and [string](Get-Journal).status -ceq 'completed') "completed contradiction must never be overwritten: $($case.name)"
    Remove-Scenario
  }

  Reset-Scenario; $global:scenarioCount++
  $snapshot = New-Snapshot -Phase 'writing_latest' -Outcome 'pending' -States @('updated','captured','captured')
  $global:tags[$snapshot.targets[0].reference] = $global:immutable[$snapshot.targets[0].intended.immutable_reference]
  Seed-Journal $snapshot
  $global:patchFaults.Add('reject'); $global:patchFaults.Add('reject')
  Expect-GateFailure { Invoke-Gate -Mode Reconcile -RecoveryHandoff } 'two rejected PATCH attempts must expose a replay boundary'
  Assert-NoUnsafeMutation
  Invoke-Gate -Mode Reconcile -RecoveryHandoff | Out-Null
  Assert-Terminal -Outcome 'rolled_back' -Conclusion 'neutral'
  Assert-That ($global:registryWrites.Count -eq 1) 'replay after repeated PATCH rejection must rollback exactly once'
  Remove-Scenario

  Reset-Scenario; $global:scenarioCount++
  $snapshot = New-Snapshot -Phase 'writing_latest' -Outcome 'pending' -States @('updated','captured','captured')
  $global:tags[$snapshot.targets[0].reference] = $global:immutable[$snapshot.targets[0].intended.immutable_reference]
  Seed-Journal $snapshot
  $global:patchFaultSummary = 'rolling_back/pending'
  $global:patchFaultSummaryKind = 'ambiguous'
  $global:failReadAfterPatch = $true
  Invoke-Gate -Mode Reconcile -RecoveryHandoff | Out-Null
  Assert-Terminal -Outcome 'rolled_back' -Conclusion 'neutral'
  Assert-That ($global:registryWrites.Count -eq 1) 'one Reconcile must survive a transient post-PATCH reread failure and rollback exactly once'
  Remove-Scenario

  Assert-That ($global:scenarioCount -eq 50) "expected 50 bounded recovery scenarios, got $($global:scenarioCount)"
  "PASS: successor E journal matrix proves distinct workflow/release heads, terminal metadata closure, bounded ambiguous PATCH reread recovery, rollback failure-snapshot replay, rollback write-before-journal replay, API guard failures, uncertain writes, and completed second-pass idempotence; scenarios=$($global:scenarioCount)"
} finally
{
  if ($null -ne (Get-Variable temp -Scope Global -ErrorAction SilentlyContinue))
  { Remove-Scenario
  }
}
