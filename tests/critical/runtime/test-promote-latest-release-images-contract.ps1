param(
  [Parameter(Mandatory)][string]$WorkflowPath,
  [Parameter(Mandatory)][string]$RecoveryWorkflowPath,
  [Parameter(Mandatory)][string]$GatePath
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

$global:repositories = @('ghcr.io/thebtf/engram','ghcr.io/thebtf/engram-operator-console','ghcr.io/thebtf/engram-postgres')
$global:runId = '8675309'
$global:attempt = '3'
$global:head = '1' * 40 -join ''
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
  $previous = if ($State -ceq 'unknown') { New-Identity } else { New-Identity -State 'present' -Repository $repository -Digest $previousDigest }
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
    run = [ordered]@{ id=$global:runId; attempt=$global:attempt; head_sha=$global:head; external_id="latest-promotion:$($global:runId):$($global:attempt)"; details_url=$global:details }
    release = [ordered]@{ tag=$global:releaseTag; source_commit=$global:head }
    phase = $Phase
    outcome = $Outcome
    targets = @($targets)
    revalidation = [ordered]@{ status='not_started'; observed_release_tag=$null; observed_source_commit=$null; failure=$null }
    rollback = [ordered]@{ attempted=$false; outcome='not_needed'; failures=@() }
    failure = $null
  }
}
function New-TerminalSnapshot([string]$Phase, [string]$Outcome)
{
  switch ("$Phase/$Outcome")
  {
    'completed/success'
    {
      $snapshot = New-Snapshot -Phase $Phase -Outcome $Outcome -States @('updated','updated','updated')
      $snapshot.revalidation.status = 'passed'
      return $snapshot
    }
    'failed/bootstrap_required_no_write'
    {
      $snapshot = New-Snapshot -Phase $Phase -Outcome $Outcome
      $snapshot.targets[0].previous = New-Identity -State 'absent'
      $snapshot.targets[0].observed = New-Identity -State 'absent'
      return $snapshot
    }
    'failed/failed_before_write'
    {
      return New-Snapshot -Phase $Phase -Outcome $Outcome -States @('unknown','unknown','unknown')
    }
    'failed/rolled_back'
    {
      $snapshot = New-Snapshot -Phase $Phase -Outcome $Outcome -States @('restored','restored','restored')
      $snapshot.rollback.attempted = $true
      $snapshot.rollback.outcome = 'succeeded'
      return $snapshot
    }
    'failed/rollback_failed'
    {
      $snapshot = New-Snapshot -Phase $Phase -Outcome $Outcome -States @('rollback_failed','restored','restored')
      $snapshot.rollback.attempted = $true
      $snapshot.rollback.outcome = 'failed'
      $snapshot.rollback.failures = @([ordered]@{ target=$snapshot.targets[0].reference; failure='rollback command failed' })
      return $snapshot
    }
    'failed/rollback_readback_error'
    {
      $snapshot = New-Snapshot -Phase $Phase -Outcome $Outcome -States @('readback_error','restored','restored')
      $snapshot.targets[0].observed = [ordered]@{ state='readback_error'; immutable_reference=$null; manifest_digest=$null; failure='readback failed' }
      $snapshot.rollback.attempted = $true
      $snapshot.rollback.outcome = 'readback_error'
      $snapshot.rollback.failures = @([ordered]@{ target=$snapshot.targets[0].reference; failure='readback failed' })
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
    id='41'; name='latest-promotion-journal'; head_sha=$global:head; external_id="latest-promotion:$($global:runId):$($global:attempt)"; details_url=$global:details
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

function Reset-Scenario
{
  param([switch]$WithSources)
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
  $global:postFault = 'none'
  $global:patchFault = 'none'
  $global:rollbackCommandFailure = $false
  $global:rollbackReadbackFailure = $false
  $global:rollbackGuardFault = 'none'
  $global:releaseHead = $global:head
  $global:releaseVersion = $global:releaseTag
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
    [ordered]@{ release_tag=$global:releaseTag; source_commit=$global:head; images=@($images) } | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $global:receipt 'sources.json') -Encoding utf8NoBOM
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
function Invoke-Gate([string]$Mode, [switch]$AllowMissing)
{
  $arguments = @{
    Mode = $Mode
    RepositoryName = 'thebtf/engram'
    RunId = $global:runId
    RunAttempt = $global:attempt
    HeadSha = $global:head
    ReleaseTag = $global:releaseTag
    ReceiptDir = $global:receipt
    AllowMissingJournal = [bool]$AllowMissing
  }
  return & $GatePath @arguments
}
function Assert-Terminal([string]$Outcome, [string]$Conclusion)
{
  $journal = Get-Journal
  Assert-That ($journal.status -ceq 'completed' -and $journal.conclusion -ceq $Conclusion) "journal must complete as $Conclusion, got status=$($journal.status) conclusion=$($journal.conclusion) outcome=$((Get-JournalSnapshot).outcome) failure=$((Get-JournalSnapshot).failure)"
  $snapshot = Get-JournalSnapshot
  Assert-That ($snapshot.outcome -ceq $Outcome) "journal outcome must be $Outcome, got $($snapshot.outcome)"
  Assert-That ($global:patchAfterCompleted -eq 0) 'no PATCH may occur after completion was observed'
}
function Assert-NoUnsafeMutation
{ Assert-That ($global:registryWrites.Count -eq 0) 'scenario performed an unsafe registry mutation'
}

function global:gh
{
  param([Parameter(ValueFromRemainingArguments)][string[]]$Arguments)
  $global:LASTEXITCODE = 0
  $request = $Arguments -join ' '
  if ($request -match '/commits/[0-9a-f]{40}/check-runs\?')
  { return ([ordered]@{ total_count=$global:runs.Count; check_runs=@($global:runs.Values) } | ConvertTo-Json -Compress -Depth 14)
  }
  if ($request -match 'releases/latest')
  { return $global:releaseVersion
  }
  if ($request -match '/commits/')
  { return $global:releaseHead
  }
  if ($request -match 'check-runs/([1-9][0-9]*)$' -and $request -notmatch '--method')
  { return ($global:runs[$Matches[1]] | ConvertTo-Json -Compress -Depth 14)
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
    $fault = $global:patchFault
    $global:patchFault = 'none'
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
    if ($run.status -ceq 'in_progress' -and $global:rollbackGuardFault -cne 'none')
    {
      $snapshot = $run.output.text | ConvertFrom-Json
      $pending = @($snapshot.targets | Where-Object { [string]$_.state -ceq 'rollback_pending' })
      if ($pending.Count -eq 1)
      {
        $fault = $global:rollbackGuardFault
        $global:rollbackGuardFault = 'none'
        if ($fault -ceq 'release_changed')
        { $global:releaseHead = '4' * 40 -join ''
        } elseif ($fault -ceq 'tag_changed')
        { $global:tags[[string]$pending[0].reference] = [pscustomobject]@{ manifest_digest=(New-Digest 'f') }
        }
      }
    }
    if ($fault -eq 'ambiguous')
    { $global:LASTEXITCODE=1; return 'lost patch response'
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
    $rollback = $source -match ('@sha256:[a-c]')
    $global:registryWrites.Add([pscustomobject]@{ target=$target; source=$source; rollback=$rollback })
    if ($rollback -and $global:rollbackCommandFailure)
    { $global:rollbackCommandFailure=$false; $global:LASTEXITCODE=1; return
    }
    if (-not $global:immutable.ContainsKey($source))
    { $global:LASTEXITCODE=1; return
    }
    $global:tags[$target] = $global:immutable[$source]
    return
  }
  throw "unexpected docker invocation: $($Arguments -join ' ')"
}

$workflow = [System.IO.File]::ReadAllText($WorkflowPath)
$recovery = [System.IO.File]::ReadAllText($RecoveryWorkflowPath)
$gate = [System.IO.File]::ReadAllText($GatePath)
foreach ($required in @('Check out trusted production-gate implementation','latest-promotion-journal.ps1','-Mode EnsureJournal','-Mode Promote','-Mode Reconcile','Reconcile latest-promotion journal on the same runner'))
{ Assert-That ($workflow.Contains($required)) "promotion workflow lacks $required"
}
foreach ($required in @('workflow_run:','workflows: ["Promote Latest Release Images"]','workflow_dispatch:','original_run_id','Check out trusted default-branch recovery code','Validate original promotion run identity','Discover owned promotion journal','Login to GHCR for owned rollback only','Independently reconcile promotion journal'))
{ Assert-That ($recovery.Contains($required)) "recovery workflow lacks $required"
}
foreach ($required in @('Find-Journal','output = New-CheckOutput','text = ConvertTo-SnapshotJson','Invoke-JournalPatch','Get-TerminalConclusion','official release identity changed; registry mutation is forbidden','latest-promotion ambiguous PATCH observed a contradictory completed journal'))
{ Assert-That ($gate.Contains($required)) "shared gate lacks $required"
}
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
    Assert-That ($global:createCount -eq 1 -and $global:runs.Count -eq 1) 'uncertain POST must not create a duplicate'
    Remove-Scenario
  }

  Reset-Scenario -WithSources; $global:scenarioCount++
  $id = Invoke-Gate -Mode EnsureJournal | Select-Object -Last 1
  Assert-That ([string]$id -ceq '41') 'journal setup failed'
  $exportFailed = $true
  Assert-That $exportFailed 'simulated GITHUB_ENV export failure must be active'
  Invoke-Gate -Mode Reconcile | Out-Null
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
    $patches = $global:patchCount
    Invoke-Gate -Mode Reconcile | Out-Null
    Assert-That ($global:patchCount -eq $patches) 'completed terminal replay must be idempotent'
    Remove-Scenario
  }

  Reset-Scenario; $global:scenarioCount++
  $snapshot = New-Snapshot -Phase 'capturing_previous_latest' -Outcome 'pending' -States @('captured','unknown','unknown')
  Seed-Journal $snapshot
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
  Invoke-Gate -Mode Reconcile | Out-Null
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

  Reset-Scenario; $global:scenarioCount++
  $global:runs['41'] = New-CheckRun -Text '{malformed'
  Invoke-Gate -Mode Reconcile | Out-Null
  Assert-Terminal -Outcome 'contradiction' -Conclusion 'failure'; Assert-NoUnsafeMutation
  Remove-Scenario

  Reset-Scenario; $global:scenarioCount++
  $snapshot = New-Snapshot -Phase 'writing_latest' -Outcome 'pending'
  $snapshot.run.attempt = '99'
  Seed-Journal $snapshot
  Invoke-Gate -Mode Reconcile | Out-Null
  Assert-Terminal -Outcome 'contradiction' -Conclusion 'failure'; Assert-NoUnsafeMutation
  Remove-Scenario

  Reset-Scenario; $global:scenarioCount++
  $snapshot = New-Snapshot -Phase 'capturing_previous_latest' -Outcome 'pending' -States @('updated','captured','captured')
  Seed-Journal $snapshot
  Invoke-Gate -Mode Reconcile | Out-Null
  Assert-Terminal -Outcome 'contradiction' -Conclusion 'failure'; Assert-NoUnsafeMutation
  Remove-Scenario

  Reset-Scenario; $global:scenarioCount++
  $snapshot = New-Snapshot -Phase 'writing_latest' -Outcome 'pending' -States @('updated','captured','captured')
  $global:tags[$snapshot.targets[0].reference] = $global:immutable[$snapshot.targets[0].intended.immutable_reference]
  $global:releaseHead = '4' * 40 -join ''
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
  Invoke-Gate -Mode Reconcile | Out-Null
  Assert-That ($global:patchCount -eq 0) 'completed journal replay must issue no PATCH'
  Remove-Scenario

  Reset-Scenario -WithSources; $global:scenarioCount++
  Invoke-Gate -Mode EnsureJournal | Out-Null
  $global:patchFault = 'malformed'
  Invoke-Gate -Mode Promote | Out-Null
  Assert-Terminal -Outcome 'success' -Conclusion 'success'
  Assert-That ($global:registryWrites.Count -eq 3) 'ambiguous success completion must not trigger rollback'
  Remove-Scenario

  Assert-That ($global:scenarioCount -eq 26) "expected 26 exhaustive scenarios, got $($global:scenarioCount)"
  "PASS: successor E journal fault matrix proves C1-C8, all terminal/interstitial states, runner-loss recovery, just-in-time safe rollback, contradictions, stale/unexpected guards, ambiguous writes, and idempotence; scenarios=$($global:scenarioCount)"
} finally
{
  if ($null -ne (Get-Variable temp -Scope Global -ErrorAction SilentlyContinue))
  { Remove-Scenario
  }
}
