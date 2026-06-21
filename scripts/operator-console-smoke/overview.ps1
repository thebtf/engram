param(
  [string]$AppRoot = "apps\operator-console"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$root = Join-Path $repoRoot $AppRoot
$overviewPath = Join-Path $root "pages\index.vue"
$overviewComposablePath = Join-Path $root "composables\useOperatorOverview.ts"
$compatibilityPath = Join-Path $root "composables\useMockData.ts"

function Assert-Contains {
  param(
    [Parameter(Mandatory = $true)]
    [string]$Content,
    [Parameter(Mandatory = $true)]
    [string]$Needle,
    [Parameter(Mandatory = $true)]
    [string]$Label
  )

  if (-not $Content.Contains($Needle)) {
    throw "OVERVIEW_SMOKE_FAILED: missing $Label ($Needle)"
  }
}

function Assert-NotContains {
  param(
    [Parameter(Mandatory = $true)]
    [string]$Content,
    [Parameter(Mandatory = $true)]
    [string]$Needle,
    [Parameter(Mandatory = $true)]
    [string]$Label
  )

  if ($Content.Contains($Needle)) {
    throw "OVERVIEW_SMOKE_FAILED: forbidden $Label ($Needle)"
  }
}

function Assert-File {
  param([Parameter(Mandatory = $true)][string]$Path)
  if (-not (Test-Path -LiteralPath $Path)) {
    throw "OVERVIEW_SMOKE_FAILED: missing required file $Path"
  }
}

Assert-File $overviewPath
Assert-File $overviewComposablePath
Assert-File $compatibilityPath

$overview = Get-Content -LiteralPath $overviewPath -Raw
$overviewComposable = Get-Content -LiteralPath $overviewComposablePath -Raw
$compatibility = Get-Content -LiteralPath $compatibilityPath -Raw

Assert-Contains $overview "useOperatorOverview" "overview live-state composable"
Assert-Contains $overview "modelHealthGap" "honest model-health gap binding"
Assert-Contains $overview "queueGap" "honest review-queue gate binding"
Assert-Contains $overview "accessGap" "honest access gap binding"
Assert-Contains $overview "nav.items.memory" "memory card"
Assert-Contains $overview "nav.items.issues" "issues card"
Assert-Contains $overview "nav.items.secrets" "vault card"
Assert-Contains $overview "overview.cards.projects.sub" "projects card"
Assert-Contains $overview "big: String(info.value.noise)" "ref-safe noise card value binding"
Assert-Contains $overview "memoryPending" "memory pending binding"
Assert-Contains $overview "issuesPending" "issues pending binding"
Assert-Contains $overview "memoryCountDisplay" "memory pending-safe count display"
Assert-Contains $overview "issueCountDisplay" "issues pending-safe count display"
Assert-Contains $overview "ruleCountDisplay" "rules pending-safe count display"
Assert-Contains $overview "projectCountDisplay" "projects pending-safe count display"
Assert-Contains $overview "t('common.loadingShort')" "keyed loading count placeholder"
Assert-Contains $overviewComposable "unsupportedOperatorAction" "unsupported action helper"
Assert-Contains $overviewComposable "'model-health'" "model health mustbuild descriptor"
Assert-Contains $overviewComposable "'access-summary'" "access mustbuild descriptor"
Assert-Contains $overviewComposable "VNEXT_F" "queue gate evidence"
Assert-Contains $overviewComposable "useOperatorShellStatus" "overview server status seam"
Assert-Contains $overviewComposable "useOperatorMemoryLab" "overview memory load-state seam"
Assert-Contains $overviewComposable "useIssuesState" "overview issues load-state seam"
Assert-Contains $overviewComposable "memoryLab.pending.value && memories.length === 0" "overview must not show false zero while memory is pending"
Assert-Contains $overviewComposable "issuesState.pending.value && issues.length === 0" "overview must not show false zero while issues are pending"
Assert-Contains $overviewComposable "rules.pending.value && rules.rows.value.length === 0" "overview must not show false zero while rules are pending"
Assert-Contains $overviewComposable "projects.pending.value && projects.rows.value.length === 0" "overview must not show false zero while projects are pending"
Assert-Contains $compatibility "useOperatorMemoryLab" "shared memory live seam"
Assert-Contains $compatibility "useOperatorMemoryLab().rows" "overview/layout memory count shares Memory Lab rows"
Assert-Contains $compatibility "useIssuesState" "shared issues live-state seam"

Assert-NotContains $overview "useModels" "mock model rows"
Assert-NotContains $overview "const queueCount = 7" "static queue count"
Assert-NotContains $overview "const accessCount = 5" "static access count"
Assert-NotContains $overview "big: '4'" "static rules count"
Assert-NotContains $overview "big: '3'" "static projects count"
Assert-NotContains $overview "big: String(info.noise)" "ref object noise access"
Assert-NotContains $overview "vault ok" "unkeyed vault status literal"
Assert-NotContains $compatibility "live:memories" "separate overview/layout memory cache"

Write-Host "OVERVIEW_SMOKE=passed"
