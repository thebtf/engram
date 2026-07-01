param(
  [string]$AppRoot = "apps\operator-console"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$root = Join-Path $repoRoot $AppRoot
$searchPath = Join-Path $root "pages\search.vue"
$noisePath = Join-Path $root "pages\noise.vue"
$composablePath = Join-Path $root "composables\useOperatorSearchNoise.ts"

function Assert-Contains {
  param(
    [Parameter(Mandatory = $true)][string]$Content,
    [Parameter(Mandatory = $true)][string]$Needle,
    [Parameter(Mandatory = $true)][string]$Label
  )

  if (-not $Content.Contains($Needle)) {
    throw "SEARCH_NOISE_SMOKE_FAILED: missing $Label ($Needle)"
  }
}

function Assert-NotContains {
  param(
    [Parameter(Mandatory = $true)][string]$Content,
    [Parameter(Mandatory = $true)][string]$Needle,
    [Parameter(Mandatory = $true)][string]$Label
  )

  if ($Content.Contains($Needle)) {
    throw "SEARCH_NOISE_SMOKE_FAILED: forbidden $Label ($Needle)"
  }
}

function Assert-File {
  param([Parameter(Mandatory = $true)][string]$Path)
  if (-not (Test-Path -LiteralPath $Path)) {
    throw "SEARCH_NOISE_SMOKE_FAILED: missing required file $Path"
  }
}

Assert-File $searchPath
Assert-File $noisePath
Assert-File $composablePath

$search = Get-Content -LiteralPath $searchPath -Raw
$noise = Get-Content -LiteralPath $noisePath -Raw
$composable = Get-Content -LiteralPath $composablePath -Raw

Assert-Contains $search "useOperatorSearchNoise" "search live adapter"
Assert-Contains $noise "useOperatorSearchNoise" "noise live adapter"
Assert-Contains $search "searchState" "search load-state binding"
Assert-Contains $search "recentState" "recent search load-state binding"
Assert-Contains $noise "analyticsState" "analytics load-state binding"
Assert-Contains $noise "retrievalState" "retrieval load-state binding"
Assert-Contains $noise "vnextState" "vnext load-state binding"
Assert-Contains $search "runSearch" "explicit context search action"
Assert-Contains $search "tombstoneGap.evidence.endpoint" "tombstone evidence"
Assert-Contains $noise "noiseTrendGap.evidence.endpoint" "noise trend gap evidence"

Assert-Contains $composable "loadOperatorJson<string[]>('/api/projects'" "project list endpoint"
Assert-Contains $composable "operatorFetchJson<ApiContextSearch>" "context search endpoint"
Assert-Contains $composable "'/api/context/search?" "context search path"
Assert-Contains $composable "loadOperatorJson<ApiSearchRecent>('/api/search/recent'" "recent search endpoint"
Assert-Contains $composable "loadOperatorJson<ApiSearchAnalytics>('/api/search/analytics'" "search analytics endpoint"
Assert-Contains $composable "loadOperatorJson<ApiRetrievalStats>('/api/stats/retrieval'" "retrieval stats endpoint"
Assert-Contains $composable "loadOperatorJson<ApiStatsVNext>('/api/stats/vnext'" "vnext stats endpoint"
Assert-Contains $composable "staleState" "deprecated result guard"
Assert-Contains $composable "deprecated" "deprecated payload guard"
Assert-Contains $composable "search_collection" "search_collection tombstone evidence"
Assert-Contains $composable "GET /api/stats/noise-trend" "noise trend endpoint evidence"
Assert-Contains $composable "unsupportedOperatorAction" "mustbuild helper"

Assert-NotContains $search "const scopes = [" "inline mock scopes"
Assert-NotContains $search "Что ищем" "hardcoded search placeholder"
Assert-NotContains $noise "const ratio = 0.41" "static noise ratio"
Assert-NotContains $noise "3589" "static shown count"
Assert-NotContains $noise "2117" "static used count"
Assert-NotContains $noise "1472" "static unused count"
Assert-NotContains $composable "search_collection(" "calling tombstone search_collection"

Write-Host "SEARCH_NOISE_SMOKE=passed"
