param(
  [string]$AppRoot = "apps\operator-console"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$pagePath = Join-Path $repoRoot "$AppRoot\pages\graph.vue"
$overviewPath = Join-Path $repoRoot "$AppRoot\pages\index.vue"
$overviewComposablePath = Join-Path $repoRoot "$AppRoot\composables\useOperatorOverview.ts"
$graphComposablePath = Join-Path $repoRoot "$AppRoot\composables\useOperatorGraph.ts"

foreach ($path in @($pagePath, $overviewPath, $overviewComposablePath, $graphComposablePath)) {
  if (-not (Test-Path -LiteralPath $path)) {
    throw "GRAPH_SMOKE_FAILED: missing required file $path"
  }
}

$page = Get-Content -LiteralPath $pagePath -Raw
$overview = Get-Content -LiteralPath $overviewPath -Raw
$overviewComposable = Get-Content -LiteralPath $overviewComposablePath -Raw
$graphComposable = Get-Content -LiteralPath $graphComposablePath -Raw

foreach ($id in @(
  "graph-project-filter", "graph-node-type", "graph-node-external-ref", "graph-node-privacy",
  "graph-edge-source-node", "graph-edge-target-node", "graph-edge-type", "graph-edge-reasoning",
  "graph-traverse-memory-id", "graph-traverse-depth", "graph-path-source-id", "graph-path-target-id",
  "graph-path-max-depth", "graph-delete-cascade"
)) {
  if (-not $page.Contains("id=`"$id`"") -or -not $page.Contains("name=`"$id`"")) {
    throw "GRAPH_SMOKE_FAILED: control $id must have matching id and name"
  }
}

foreach ($needle in @(
  'role="status" aria-live="polite" aria-atomic="true"',
  "graphPage.actions.closeNotice",
  "nodesState.value.kind !== 'live' && nodesState.value.kind !== 'empty'",
  ':disabled="graphDisabled"'
)) {
  if (-not $page.Contains($needle)) {
    throw "GRAPH_SMOKE_FAILED: missing page contract $needle"
  }
}

if (-not $overview.Contains('graphFlagBadge') -or -not $overviewComposable.Contains('ENGRAM_GRAPH_ENABLED')) {
  throw "GRAPH_SMOKE_FAILED: overview graph status is not derived from ENGRAM_GRAPH_ENABLED"
}
if (-not $graphComposable.Contains("flags.flags?.[GRAPH_FLAG] !== true")) {
  throw "GRAPH_SMOKE_FAILED: graph requests are not strictly gated by a true flag"
}

Write-Host "GRAPH_SMOKE=passed"
