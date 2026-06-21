param(
  [string]$AppRoot = "apps\operator-console"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$root = Join-Path $repoRoot $AppRoot
$issuesPath = Join-Path $root "pages\issues.vue"
$issuesComposablePath = Join-Path $root "composables\useOperatorIssues.ts"

function Assert-Contains {
  param(
    [Parameter(Mandatory = $true)][string]$Content,
    [Parameter(Mandatory = $true)][string]$Needle,
    [Parameter(Mandatory = $true)][string]$Label
  )

  if (-not $Content.Contains($Needle)) {
    throw "ISSUES_SMOKE_FAILED: missing $Label ($Needle)"
  }
}

function Assert-NotContains {
  param(
    [Parameter(Mandatory = $true)][string]$Content,
    [Parameter(Mandatory = $true)][string]$Needle,
    [Parameter(Mandatory = $true)][string]$Label
  )

  if ($Content.Contains($Needle)) {
    throw "ISSUES_SMOKE_FAILED: forbidden $Label ($Needle)"
  }
}

function Assert-File {
  param([Parameter(Mandatory = $true)][string]$Path)
  if (-not (Test-Path -LiteralPath $Path)) {
    throw "ISSUES_SMOKE_FAILED: missing required file $Path"
  }
}

Assert-File $issuesPath
Assert-File $issuesComposablePath

$issues = Get-Content -LiteralPath $issuesPath -Raw
$issuesComposable = Get-Content -LiteralPath $issuesComposablePath -Raw

Assert-Contains $issues "useOperatorIssues" "issues live adapter"
Assert-Contains $issues "loadState" "load-state binding"
Assert-Contains $issues "pending" "pending state"
Assert-Contains $issues "error" "error state"
Assert-Contains $issues "kind === 'empty'" "empty state"
Assert-Contains $issues "createIssue" "create action"
Assert-Contains $issues "updateOpened" "update action"
Assert-Contains $issues "acknowledgeOpened" "acknowledge action"
Assert-Contains $issues "deleteOpened" "delete action"
Assert-Contains $issues "deleteConfirm" "delete confirmation"
Assert-Contains $issues "detailState" "detail load state"

Assert-Contains $issuesComposable "loadOperatorJson<ApiIssueList>('/api/issues?limit=100'" "issue list endpoint"
Assert-Contains $issuesComposable 'operatorFetchJson<ApiIssueDetail>(`/api/issues/${id}`' "issue detail endpoint"
Assert-Contains $issuesComposable "operatorFetchJson<ApiIssueCreateReceipt>('/api/issues'" "issue create endpoint"
Assert-Contains $issuesComposable 'operatorFetchJson(`/api/issues/${id}`' "issue update/delete endpoint"
Assert-Contains $issuesComposable "operatorFetchJson<ApiIssueAcknowledgeReceipt>('/api/issues/acknowledge'" "acknowledge endpoint"
Assert-Contains $issuesComposable "runOperatorMutation" "mutation seam"
Assert-Contains $issuesComposable "source_project" "source project provenance"
Assert-Contains $issuesComposable "target_project" "target project field"

Assert-NotContains $issues "useIssues" "old mock issue composable"
Assert-NotContains $issues "Кросс-проектный трекер" "old hardcoded Russian heading"
Assert-NotContains $issues "DEVELOPER:" "design-only implementation note"

Write-Host "ISSUES_SMOKE=passed"
