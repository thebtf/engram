param(
  [string]$AppRoot = "apps\operator-console"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$root = Join-Path $repoRoot $AppRoot
$overviewPath = Join-Path $root "pages\index.vue"
$overviewComposablePath = Join-Path $root "composables\useOperatorOverview.ts"

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

$overview = Get-Content -LiteralPath $overviewPath -Raw
$overviewComposable = Get-Content -LiteralPath $overviewComposablePath -Raw

Assert-Contains $overview "useOperatorOverview" "overview live-state composable"
Assert-Contains $overview "modelHealthGap" "honest model-health gap binding"
Assert-Contains $overview "queueGap" "honest review-queue gate binding"
Assert-Contains $overview "accessGap" "honest access gap binding"
Assert-Contains $overview "nav.items.memory" "memory card"
Assert-Contains $overview "nav.items.issues" "issues card"
Assert-Contains $overview "nav.items.secrets" "vault card"
Assert-Contains $overview "overview.cards.projects.sub" "projects card"
Assert-Contains $overview "big: String(info.value.noise)" "ref-safe noise card value binding"
Assert-Contains $overviewComposable "unsupportedOperatorAction" "unsupported action helper"
Assert-Contains $overviewComposable "'model-health'" "model health mustbuild descriptor"
Assert-Contains $overviewComposable "'access-summary'" "access mustbuild descriptor"
Assert-Contains $overviewComposable "VNEXT_F" "queue gate evidence"
Assert-Contains $overviewComposable "useOperatorShellStatus" "overview server status seam"

Assert-NotContains $overview "useModels" "mock model rows"
Assert-NotContains $overview "const queueCount = 7" "static queue count"
Assert-NotContains $overview "const accessCount = 5" "static access count"
Assert-NotContains $overview "big: '4'" "static rules count"
Assert-NotContains $overview "big: '3'" "static projects count"
Assert-NotContains $overview "big: String(info.noise)" "ref object noise access"
Assert-NotContains $overview "vault ok" "unkeyed vault status literal"

Write-Host "OVERVIEW_SMOKE=passed"
