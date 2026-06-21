param(
  [string]$AppRoot = "apps\operator-console"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$root = Join-Path $repoRoot $AppRoot
$rulesPath = Join-Path $root "pages\rules.vue"
$rulesComposablePath = Join-Path $root "composables\useOperatorRules.ts"
$rulesHandlerPath = Join-Path $repoRoot "internal\worker\handlers_rules.go"

function Assert-Contains {
  param(
    [Parameter(Mandatory = $true)][string]$Content,
    [Parameter(Mandatory = $true)][string]$Needle,
    [Parameter(Mandatory = $true)][string]$Label
  )

  if (-not $Content.Contains($Needle)) {
    throw "RULES_SMOKE_FAILED: missing $Label ($Needle)"
  }
}

function Assert-NotContains {
  param(
    [Parameter(Mandatory = $true)][string]$Content,
    [Parameter(Mandatory = $true)][string]$Needle,
    [Parameter(Mandatory = $true)][string]$Label
  )

  if ($Content.Contains($Needle)) {
    throw "RULES_SMOKE_FAILED: forbidden $Label ($Needle)"
  }
}

function Assert-File {
  param([Parameter(Mandatory = $true)][string]$Path)
  if (-not (Test-Path -LiteralPath $Path)) {
    throw "RULES_SMOKE_FAILED: missing required file $Path"
  }
}

Assert-File $rulesPath
Assert-File $rulesComposablePath
Assert-File $rulesHandlerPath

$rules = Get-Content -LiteralPath $rulesPath -Raw
$rulesComposable = Get-Content -LiteralPath $rulesComposablePath -Raw
$rulesHandler = Get-Content -LiteralPath $rulesHandlerPath -Raw

Assert-Contains $rules "useOperatorRules" "rules live adapter"
Assert-Contains $rules "scopeFilter" "scope filter"
Assert-Contains $rules "loadState" "load-state binding"
Assert-Contains $rules "pending" "pending state"
Assert-Contains $rules "error" "error state"
Assert-Contains $rules "kind === 'empty'" "empty state"
Assert-Contains $rules "createRule" "create action"
Assert-Contains $rules "createOpen" "create modal state"
Assert-Contains $rules "startEdit" "inline editor entry"
Assert-Contains $rules "saveEdit" "inline editor save"
Assert-Contains $rules "confirmingDeleteId" "two-step delete confirmation"
Assert-Contains $rules "reorderRules" "drag reorder action"
Assert-Contains $rules "moveRule" "keyboard reorder action"
Assert-Contains $rules "rule-grip" "reorder grip"
Assert-Contains $rules "rule-editor" "inline editor"
Assert-Contains $rules "modal-backdrop" "create modal"
Assert-Contains $rules "enableGap.evidence.endpoint" "unsupported enable evidence"
Assert-Contains $rules "scopeChangeGap.evidence.endpoint" "unsupported scope-change evidence"
Assert-Contains $rules "priorityForNewRule" "computed priority"

Assert-Contains $rulesComposable "loadOperatorJson<ApiRuleRow[]>('/api/rules?all=true&limit=200'" "rules all-scope list endpoint"
Assert-Contains $rulesComposable "loadOperatorJson<string[]>('/api/projects'" "rules scope options endpoint"
Assert-Contains $rulesComposable "operatorFetchJson<ApiRuleRow>('/api/rules'" "rules create endpoint"
Assert-Contains $rulesComposable 'operatorFetchJson<ApiRuleRow>(`/api/rules/${id}`' "rules update endpoint"
Assert-Contains $rulesComposable 'operatorFetchJson(`/api/rules/${id}`' "rules delete endpoint"
Assert-Contains $rulesComposable "Promise.all(changed.map" "batch priority reorder"
Assert-Contains $rulesComposable "version" "rule version mapping"
Assert-Contains $rulesComposable "runOperatorMutation" "mutation seam"
Assert-Contains $rulesComposable "unsupportedOperatorAction" "mustbuild helper"
Assert-Contains $rulesComposable "PATCH /api/rules/{id}/enabled" "enable endpoint evidence"
Assert-Contains $rulesComposable "PATCH /api/rules/{id}/project" "scope-change endpoint evidence"
Assert-Contains $rulesComposable "edited_by" "operator edit provenance"

Assert-Contains $rulesHandler "all=true" "all-scope query documentation"
Assert-Contains $rulesHandler "ListAll(r.Context(), limit)" "all-scope backend list"

Assert-NotContains $rules "const rules = [" "hardcoded rules fixture"
Assert-NotContains $rules "Прод unleashed" "old hardcoded Russian rule"
Assert-NotContains $rules "DEVELOPER:" "design-only implementation note"
Assert-NotContains $rules "updateOpened" "old side-panel update action"
Assert-NotContains $rules "deleteOpened" "old side-panel delete action"
Assert-NotContains $rules "priorityDraft" "manual priority edit field"
Assert-NotContains $rules 'v-model.number="createPriority"' "manual create priority field"
Assert-NotContains $rules "area-body" "old side-panel layout"

Write-Host "RULES_SMOKE=passed"
