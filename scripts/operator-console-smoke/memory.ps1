param(
  [string]$AppRoot = "apps\operator-console"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$root = Join-Path $repoRoot $AppRoot
$memoryPath = Join-Path $root "pages\memory.vue"
$memoryComposablePath = Join-Path $root "composables\useOperatorMemoryLab.ts"

function Assert-Contains {
  param(
    [Parameter(Mandatory = $true)][string]$Content,
    [Parameter(Mandatory = $true)][string]$Needle,
    [Parameter(Mandatory = $true)][string]$Label
  )

  if (-not $Content.Contains($Needle)) {
    throw "MEMORY_SMOKE_FAILED: missing $Label ($Needle)"
  }
}

function Assert-NotContains {
  param(
    [Parameter(Mandatory = $true)][string]$Content,
    [Parameter(Mandatory = $true)][string]$Needle,
    [Parameter(Mandatory = $true)][string]$Label
  )

  if ($Content.Contains($Needle)) {
    throw "MEMORY_SMOKE_FAILED: forbidden $Label ($Needle)"
  }
}

function Assert-File {
  param([Parameter(Mandatory = $true)][string]$Path)
  if (-not (Test-Path -LiteralPath $Path)) {
    throw "MEMORY_SMOKE_FAILED: missing required file $Path"
  }
}

Assert-File $memoryPath
Assert-File $memoryComposablePath

$memory = Get-Content -LiteralPath $memoryPath -Raw
$memoryComposable = Get-Content -LiteralPath $memoryComposablePath -Raw

Assert-Contains $memory "useOperatorMemoryLab" "memory live adapter"
Assert-Contains $memory "loadState" "load-state binding"
Assert-Contains $memory "pending" "pending state"
Assert-Contains $memory "error" "error state"
Assert-Contains $memory "kind === 'empty'" "empty state"
Assert-Contains $memory "kind === 'gated'" "gated state"
Assert-Contains $memory "auditGap.evidence.endpoint" "audit mustbuild evidence"
Assert-Contains $memory "deleteOpened" "supported delete action"
Assert-Contains $memory "storeCopy" "supported store action"

Assert-Contains $memoryComposable "loadOperatorJson<string[]>('/api/projects'" "project list load state"
Assert-Contains $memoryComposable "empty: (rows) => !Array.isArray(rows) || !rows.length" "project list empty-body guard"
Assert-Contains $memoryComposable 'operatorFetchJson<unknown>(' "project-scoped memory list fetch"
Assert-Contains $memoryComposable '/api/memories?project=${encodeURIComponent(project)}&limit=200' "project-scoped memory endpoint"
Assert-Contains $memoryComposable "operatorFetchJson<ApiMemory>('/api/memories'" "store memory endpoint"
Assert-Contains $memoryComposable 'operatorFetchJson(`/api/memories/${id}`' "delete memory endpoint"
Assert-Contains $memoryComposable "runOperatorMutation" "mutation seam"
Assert-Contains $memoryComposable "unsupportedOperatorAction" "mustbuild helper"
Assert-Contains $memoryComposable "GET /api/memories/{id}/audit" "audit endpoint evidence"
Assert-Contains $memoryComposable "GET /api/memories/{id}/provenance" "provenance endpoint evidence"

Assert-NotContains $memory "function onBulk()" "no-op bulk action body"
Assert-NotContains $memory '@act="onBulk"' "fake bulk action wiring"
Assert-NotContains $memory "DEVELOPER:" "design-only implementation note"

Write-Host "MEMORY_SMOKE=passed"
