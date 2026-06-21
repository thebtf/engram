param(
  [string]$AppRoot = "apps\operator-console"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$root = Join-Path $repoRoot $AppRoot
$issuesPath = Join-Path $root "pages\issues.vue"
$issuesComposablePath = Join-Path $root "composables\useOperatorIssues.ts"
$issuesMockupPath = Join-Path $repoRoot "design\operator-console\mockups\index.html"

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
Assert-File $issuesMockupPath

$issues = Get-Content -LiteralPath $issuesPath -Raw
$issuesComposable = Get-Content -LiteralPath $issuesComposablePath -Raw
$issuesMockup = Get-Content -LiteralPath $issuesMockupPath -Raw

Assert-Contains $issues "useOperatorIssues" "issues live adapter"
Assert-Contains $issues "loadState" "load-state binding"
Assert-Contains $issues "pending" "pending state"
Assert-Contains $issues "showInitialPending" "initial pending state only before rows exist"
Assert-Contains $issues "error" "error state"
Assert-Contains $issues "kind === 'empty'" "empty state"
Assert-Contains $issues "issue-row" "design-contract issue rows"
Assert-Contains $issues "issues-grid" "design-contract issue grid"
Assert-Contains $issues "issue-workspace" "full issue workspace"
Assert-Contains $issues "showCreate" "create modal"
Assert-Contains $issues "bulkbar" "bulk bar"
Assert-Contains $issues "renderMarkdown" "markdown rendering"
Assert-Contains $issues "routeChangeAction" "honest mustbuild route change"
Assert-Contains $issues "createNewIssue" "create action"
Assert-Contains $issues "updateCurrentField" "field-level update action"
Assert-Contains $issues "toggleIssueLabel" "label update action"
Assert-Contains $issues "acknowledgeCurrent" "acknowledge action"
Assert-Contains $issues "deleteCurrent" "delete action"
Assert-Contains $issues "showDelete" "delete confirmation"
Assert-Contains $issues "rejectCurrent" "reject action"
Assert-Contains $issues "applyBulkField" "bulk field action"
Assert-Contains $issues "applyBulkLabels" "bulk labels action"
Assert-Contains $issues "detailState" "detail load state"
Assert-Contains $issues "thread-actions" "design-contract thread action bar"
Assert-Contains $issues "showIssueHover" "design-contract hover preview"
Assert-Contains $issues "scheduleIssueHoverHide" "delayed hover preview close"
Assert-Contains $issues "cancelIssueHoverClose" "hover preview keeps open over popover"
Assert-Contains $issues "clampIssueHoverTop" "viewport-clamped hover preview position"
Assert-Contains $issues "getBoundingClientRect().height" "measured hover preview height"
Assert-Contains $issues "@mouseenter=`"cancelIssueHoverClose`"" "interactive hover preview mouse enter"
Assert-Contains $issues "pointer-events:auto" "interactive hover preview pointer events"
Assert-Contains $issues "registryTotal" "server registry total binding"
Assert-Contains $issues "usePersistentPageSize('issues'" "persistent issues page size"
Assert-Contains $issues "pageSizeOptions" "design-contract page-size options"
Assert-Contains $issues "issues.list.allRows" "design-contract all rows option"
Assert-Contains $issues "md-toolbar" "markdown toolbar"
Assert-Contains $issues "createSourceProject" "create source project control"
Assert-Contains $issues "issues.thread.mustBuildEvidence" "honest thread mustbuild evidence"
Assert-Contains $issuesMockup "clampIssueHoverTop" "mockup viewport-clamped hover preview position"
Assert-Contains $issuesMockup "pointer-events:auto" "mockup interactive hover preview pointer events"

Assert-Contains $issuesComposable "loadOperatorJson<ApiIssueList>('/api/issues?limit=100'" "issue list endpoint"
Assert-Contains $issuesComposable "registryTotal" "server total computed"
Assert-Contains $issuesComposable "totalCountState" "server total state"
Assert-Contains $issuesComposable 'operatorFetchJson<ApiIssueDetail>(`/api/issues/${id}`' "issue detail endpoint"
Assert-Contains $issuesComposable "operatorFetchJson<ApiIssueCreateReceipt>('/api/issues'" "issue create endpoint"
Assert-Contains $issuesComposable 'operatorFetchJson(`/api/issues/${id}`' "issue update/delete endpoint"
Assert-Contains $issuesComposable "operatorFetchJson<ApiIssueAcknowledgeReceipt>('/api/issues/acknowledge'" "acknowledge endpoint"
Assert-Contains $issuesComposable "operatorFetchJson<ApiTrackedProjects>('/api/issues/tracked-projects'" "tracked projects endpoint"
Assert-Contains $issuesComposable "commentsState" "issue comments state"
Assert-Contains $issuesComposable "unsupportedOperatorAction" "mustbuild unsupported action"
Assert-Contains $issuesComposable "runOperatorMutation" "mutation seam"
Assert-Contains $issuesComposable "source_project" "source project provenance"
Assert-Contains $issuesComposable "target_project" "target project field"

Assert-NotContains $issues "useIssues" "old mock issue composable"
Assert-NotContains $issues "<EntityRow" "old EntityRow list"
Assert-NotContains $issues "create-card" "old inline create card"
Assert-NotContains $issues "updateOpened" "old aside update action"
Assert-NotContains $issues "acknowledgeOpened" "old aside acknowledge action"
Assert-NotContains $issues "Кросс-проектный трекер" "old hardcoded Russian heading"
Assert-NotContains $issues "DEVELOPER:" "design-only implementation note"

Write-Host "ISSUES_SMOKE=passed"
