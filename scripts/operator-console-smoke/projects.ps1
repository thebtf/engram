param(
  [string]$AppRoot = "apps\operator-console"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$root = Join-Path $repoRoot $AppRoot
$projectsPath = Join-Path $root "pages\projects.vue"
$projectsComposablePath = Join-Path $root "composables\useOperatorProjects.ts"

function Assert-Contains {
  param(
    [Parameter(Mandatory = $true)][string]$Content,
    [Parameter(Mandatory = $true)][string]$Needle,
    [Parameter(Mandatory = $true)][string]$Label
  )

  if (-not $Content.Contains($Needle)) {
    throw "PROJECTS_SMOKE_FAILED: missing $Label ($Needle)"
  }
}

function Assert-NotContains {
  param(
    [Parameter(Mandatory = $true)][string]$Content,
    [Parameter(Mandatory = $true)][string]$Needle,
    [Parameter(Mandatory = $true)][string]$Label
  )

  if ($Content.Contains($Needle)) {
    throw "PROJECTS_SMOKE_FAILED: forbidden $Label ($Needle)"
  }
}

function Assert-File {
  param([Parameter(Mandatory = $true)][string]$Path)
  if (-not (Test-Path -LiteralPath $Path)) {
    throw "PROJECTS_SMOKE_FAILED: missing required file $Path"
  }
}

Assert-File $projectsPath
Assert-File $projectsComposablePath

$projects = Get-Content -LiteralPath $projectsPath -Raw
$projectsComposable = Get-Content -LiteralPath $projectsComposablePath -Raw

Assert-Contains $projects "useOperatorProjects" "projects live adapter"
Assert-Contains $projects "projectsState" "projects load-state binding"
Assert-Contains $projects "sessionsState" "sessions load-state binding"
Assert-Contains $projects "detailState" "session detail load-state binding"
Assert-Contains $projects "pending" "pending state"
Assert-Contains $projects "kind === 'error'" "error state"
Assert-Contains $projects "kind === 'empty'" "empty state"
Assert-Contains $projects "sessionDetailGap.evidence.endpoint" "session transcript/detail gap evidence"
Assert-Contains $projects "deleteProject" "soft-delete project action"
Assert-Contains $projects "deleteConfirm" "delete confirmation"

Assert-Contains $projectsComposable "loadOperatorJson<string[]>('/api/projects'" "project list endpoint"
Assert-Contains $projectsComposable "loadOperatorJson<ApiSessionsList>" "sessions list load state"
Assert-Contains $projectsComposable 'project=${encodeURIComponent(project)}' "project-filtered sessions"
Assert-Contains $projectsComposable "operatorFetchJson<ApiSessionRow>" "session detail endpoint"
Assert-Contains $projectsComposable "claudeSessionId" "session detail query"
Assert-Contains $projectsComposable 'operatorFetchJson(`/api/projects/${encodeURIComponent(project)}`' "soft-delete project endpoint"
Assert-Contains $projectsComposable "runOperatorMutation" "mutation seam"
Assert-Contains $projectsComposable "unsupportedOperatorAction" "mustbuild helper"
Assert-Contains $projectsComposable "GET /api/sessions/{id}/transcript" "transcript endpoint evidence"
Assert-Contains $projectsComposable "GET /api/sessions/{id}/strategy" "strategy endpoint evidence"
Assert-Contains $projectsComposable "GET /api/code-intel/projects/{project}" "code-intel endpoint evidence"

Assert-NotContains $projects "const projects = [" "inline mock projects"
Assert-NotContains $projects "sessions: 12" "static session count"
Assert-NotContains $projects "nvmd-platform" "static project row"
Assert-NotContains $projects '@click="deleteProject' "delete without confirmation guard"

Write-Host "PROJECTS_SMOKE=passed"
