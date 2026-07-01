param(
  [string]$AppRoot = "apps\operator-console"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$root = Join-Path $repoRoot $AppRoot
$healthPath = Join-Path $root "pages\health.vue"
$settingsPath = Join-Path $root "pages\settings.vue"
$composablePath = Join-Path $root "composables\useOperatorHealthSettings.ts"

function Assert-Contains {
  param(
    [Parameter(Mandatory = $true)][string]$Content,
    [Parameter(Mandatory = $true)][string]$Needle,
    [Parameter(Mandatory = $true)][string]$Label
  )

  if (-not $Content.Contains($Needle)) {
    throw "HEALTH_SETTINGS_SMOKE_FAILED: missing $Label ($Needle)"
  }
}

function Assert-NotContains {
  param(
    [Parameter(Mandatory = $true)][string]$Content,
    [Parameter(Mandatory = $true)][string]$Needle,
    [Parameter(Mandatory = $true)][string]$Label
  )

  if ($Content.Contains($Needle)) {
    throw "HEALTH_SETTINGS_SMOKE_FAILED: forbidden $Label ($Needle)"
  }
}

function Assert-File {
  param([Parameter(Mandatory = $true)][string]$Path)
  if (-not (Test-Path -LiteralPath $Path)) {
    throw "HEALTH_SETTINGS_SMOKE_FAILED: missing required file $Path"
  }
}

Assert-File $healthPath
Assert-File $settingsPath
Assert-File $composablePath

$health = Get-Content -LiteralPath $healthPath -Raw
$settings = Get-Content -LiteralPath $settingsPath -Raw
$composable = Get-Content -LiteralPath $composablePath -Raw

Assert-Contains $health "useOperatorHealthSettings" "health live adapter"
Assert-Contains $settings "useOperatorHealthSettings" "settings live adapter"
Assert-Contains $health "selfcheckState" "selfcheck load-state binding"
Assert-Contains $health "readyState" "readiness load-state binding"
Assert-Contains $settings "configState" "config load-state binding"
Assert-Contains $health "vectorState" "vector metrics load-state binding"
Assert-Contains $settings "restartRequired" "restart-required state"
Assert-Contains $settings "restartConfirm" "restart confirmation"
Assert-Contains $settings "updateRestartConfirm" "update restart confirmation"
Assert-Contains $settings "configSaveGap.evidence.endpoint" "config save mustbuild evidence"
Assert-Contains $settings "flagsGap.evidence.endpoint" "flags mustbuild evidence"

Assert-Contains $composable "loadOperatorJson<ApiSelfcheck>('/api/selfcheck'" "selfcheck endpoint"
Assert-Contains $composable "loadOperatorJson<ApiReady>('/api/ready'" "ready endpoint"
Assert-Contains $composable "loadOperatorJson<ApiConfig>('/api/config'" "config endpoint"
Assert-Contains $composable "loadOperatorJson<ApiStatsVNext>('/api/stats/vnext'" "vnext stats endpoint"
Assert-Contains $composable "loadOperatorJson<ApiVectorMetrics>('/api/vector/metrics'" "vector metrics endpoint"
Assert-Contains $composable "loadOperatorJson<ApiUpdateStatus>('/api/update/status'" "update status endpoint"
Assert-Contains $composable "loadOperatorJson<ApiUpdateCheck>('/api/update/check'" "update check endpoint"
Assert-Contains $composable "operatorFetchJson('/api/restart'" "general restart endpoint"
Assert-Contains $composable "operatorFetchJson('/api/update/restart'" "update restart endpoint"
Assert-Contains $composable "runOperatorMutation" "mutation seam"
Assert-Contains $composable "unsupportedOperatorAction" "mustbuild helper"
Assert-Contains $composable "PATCH /api/config" "config save endpoint evidence"
Assert-Contains $composable "GET /api/flags" "flags endpoint evidence"

Assert-NotContains $health "useModels" "mock model rows"
Assert-NotContains $health "2837" "static embedding count"
Assert-NotContains $health "PostgreSQL','ok" "static subsystem row"
Assert-NotContains $settings "const quiet = ref(true)" "static settings switch"
Assert-NotContains $settings "vNext F" "hardcoded setting label"
Assert-NotContains $settings '@click="restartServer' "restart without confirmation guard"
Assert-NotContains $settings '@click="restartAfterUpdate' "update restart without confirmation guard"

Write-Host "HEALTH_SETTINGS_SMOKE=passed"
