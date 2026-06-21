param(
  [string]$AppRoot = "apps\operator-console"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$root = Join-Path $repoRoot $AppRoot
$rulesPath = Join-Path $root "pages\rules.vue"
$rulesComposablePath = Join-Path $root "composables\useOperatorRules.ts"

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

$rules = Get-Content -LiteralPath $rulesPath -Raw
$rulesComposable = Get-Content -LiteralPath $rulesComposablePath -Raw

Assert-Contains $rules "useOperatorRules" "rules live adapter"
Assert-Contains $rules "loadState" "load-state binding"
Assert-Contains $rules "pending" "pending state"
Assert-Contains $rules "error" "error state"
Assert-Contains $rules "kind === 'empty'" "empty state"
Assert-Contains $rules "createRule" "create action"
Assert-Contains $rules "updateOpened" "update action"
Assert-Contains $rules "deleteOpened" "delete action"
Assert-Contains $rules "enableGap.evidence.endpoint" "unsupported enable evidence"
Assert-Contains $rules "priorityDraft" "priority edit field"

Assert-Contains $rulesComposable "loadOperatorJson<ApiRuleRow[]>('/api/rules?limit=100'" "rules list endpoint"
Assert-Contains $rulesComposable "operatorFetchJson<ApiRuleRow>('/api/rules'" "rules create endpoint"
Assert-Contains $rulesComposable 'operatorFetchJson<ApiRuleRow>(`/api/rules/${id}`' "rules update endpoint"
Assert-Contains $rulesComposable 'operatorFetchJson(`/api/rules/${id}`' "rules delete endpoint"
Assert-Contains $rulesComposable "runOperatorMutation" "mutation seam"
Assert-Contains $rulesComposable "unsupportedOperatorAction" "mustbuild helper"
Assert-Contains $rulesComposable "PATCH /api/rules/{id}/enabled" "enable endpoint evidence"
Assert-Contains $rulesComposable "edited_by" "operator edit provenance"

Assert-NotContains $rules "const rules = [" "hardcoded rules fixture"
Assert-NotContains $rules "Прод unleashed" "old hardcoded Russian rule"
Assert-NotContains $rules "DEVELOPER:" "design-only implementation note"

Write-Host "RULES_SMOKE=passed"
