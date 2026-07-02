param(
  [string]$AppRoot = "apps\operator-console"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$root = Join-Path $repoRoot $AppRoot
$queuePath = Join-Path $root "pages\queue.vue"
$queueComposablePath = Join-Path $root "composables\useOperatorQueue.ts"

function Assert-Contains {
  param(
    [Parameter(Mandatory = $true)][string]$Content,
    [Parameter(Mandatory = $true)][string]$Needle,
    [Parameter(Mandatory = $true)][string]$Label
  )

  if (-not $Content.Contains($Needle)) {
    throw "QUEUE_SMOKE_FAILED: missing $Label ($Needle)"
  }
}

function Assert-NotContains {
  param(
    [Parameter(Mandatory = $true)][string]$Content,
    [Parameter(Mandatory = $true)][string]$Needle,
    [Parameter(Mandatory = $true)][string]$Label
  )

  if ($Content.Contains($Needle)) {
    throw "QUEUE_SMOKE_FAILED: forbidden $Label ($Needle)"
  }
}

function Assert-File {
  param([Parameter(Mandatory = $true)][string]$Path)
  if (-not (Test-Path -LiteralPath $Path)) {
    throw "QUEUE_SMOKE_FAILED: missing required file $Path"
  }
}

Assert-File $queuePath
Assert-File $queueComposablePath

$queue = Get-Content -LiteralPath $queuePath -Raw
$queueComposable = Get-Content -LiteralPath $queueComposablePath -Raw

# Live candidate grid/detail wiring (T002 AC: candidate grid/detail/promote/reject/supersede
# call the existing REST surface; page shows honest gated state when the flag is off).
Assert-Contains $queue "useOperatorQueue" "queue live adapter"
Assert-Contains $queue "loadState" "load-state binding"
Assert-Contains $queue "pending" "pending state"
Assert-Contains $queue "error" "error state"
Assert-Contains $queue "loadState.kind === 'empty'" "empty state"
Assert-Contains $queue "loadState.kind === 'gated'" "gated state"
Assert-Contains $queue "queue.state.gated" "gated state copy key"
Assert-Contains $queue ':data-testid="`queue-row-${candidate.id}`"' "candidate row grid"
Assert-Contains $queue 'data-testid="queue-detail"' "candidate detail panel"
Assert-Contains $queue 'data-testid="queue-empty"' "empty-queue panel"
Assert-Contains $queue ':data-testid="`queue-action-promote-${candidate.id}`"' "promote action test id"
Assert-Contains $queue "runAction(candidate, 'promote')" "promote action wiring"
Assert-Contains $queue ':data-testid="`queue-action-reject-${candidate.id}`"' "reject action test id"
Assert-Contains $queue "runAction(candidate, 'reject')" "reject action wiring"
Assert-Contains $queue ':data-testid="`queue-action-supersede-${candidate.id}`"' "supersede action test id"
Assert-Contains $queue "runAction(candidate, 'supersede')" "supersede action wiring"
Assert-Contains $queue "promoteCandidate" "promote action destructured from composable"
Assert-Contains $queue "rejectCandidate" "reject action destructured from composable"
Assert-Contains $queue "supersedeCandidate" "supersede action destructured from composable"
Assert-Contains $queue "refresh" "refresh action wiring"

Assert-Contains $queueComposable "/api/memory/candidates" "candidate list/action REST surface"
Assert-Contains $queueComposable "ENGRAM_VNEXT_F_ENABLED" "vNext-F feature flag gate"
Assert-Contains $queueComposable "/api/flags" "flag-state endpoint used for gating"
Assert-Contains $queueComposable "gatedState(evidence, QUEUE_FLAG" "gated state emitted when flag is off"
Assert-Contains $queueComposable '/api/memory/candidates?project=${encodeURIComponent(apiProject)}&status=${QUEUE_STATUS}&limit=${QUEUE_LIMIT}' "candidate list GET path"
Assert-Contains $queueComposable "operatorFetchJson<ApiCandidateListResponse>(path, undefined, 'candidate-queue')" "candidate list live GET"
Assert-Contains $queueComposable "runCandidateAction(id, 'promote')" "promote action dispatch"
Assert-Contains $queueComposable "runCandidateAction(id, 'reject'" "reject action dispatch"
Assert-Contains $queueComposable "runCandidateAction(id, 'supersede')" "supersede action dispatch"
Assert-Contains $queueComposable '`/api/memory/candidates/${encodeURIComponent(id)}/${action}`' "candidate action REST path template"
Assert-Contains $queueComposable "operatorFetchJson<CandidateActionReceipt>(path, jsonInit('POST', body), 'candidate-queue-action')" "candidate action live POST"
Assert-Contains $queueComposable "runOperatorMutation" "mutation seam"
Assert-Contains $queueComposable "emptyState(evidence, rowsState.value)" "empty state result"
Assert-Contains $queueComposable "liveState(evidence, rowsState.value)" "live state result"
Assert-Contains $queueComposable "errorState(evidence, mapped" "error state result"

# Current SectionStub must be gone; no mock-data source may remain.
Assert-NotContains $queue "SectionStub" "leftover stub component"
Assert-NotContains $queue "useMockData" "mock data source"
Assert-NotContains $queue "const candidates = [" "literal mock candidate array"
Assert-NotContains $queueComposable "useMockData" "mock data source"
Assert-NotContains $queueComposable "mockCandidates" "hardcoded mock candidates"
Assert-NotContains $queueComposable "const candidates = [" "literal mock candidate array"

Write-Host "QUEUE_SMOKE=passed"
