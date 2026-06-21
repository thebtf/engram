param(
  [string]$AppRoot = "apps\operator-console"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$root = Join-Path $repoRoot $AppRoot
$layoutPath = Join-Path $root "layouts\default.vue"
$nuxtConfigPath = Join-Path $root "nuxt.config.ts"
$baseCssPath = Join-Path $root "assets\base.css"
$shellComposablePath = Join-Path $root "composables\useOperatorShell.ts"
$navComposablePath = Join-Path $root "composables\useNav.ts"

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
    throw "SHELL_SMOKE_FAILED: missing $Label ($Needle)"
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
    throw "SHELL_SMOKE_FAILED: forbidden $Label ($Needle)"
  }
}

function Assert-File {
  param([Parameter(Mandatory = $true)][string]$Path)
  if (-not (Test-Path -LiteralPath $Path)) {
    throw "SHELL_SMOKE_FAILED: missing required file $Path"
  }
}

Assert-File $layoutPath
Assert-File $nuxtConfigPath
Assert-File $baseCssPath
Assert-File $shellComposablePath
Assert-File $navComposablePath

$layout = Get-Content -LiteralPath $layoutPath -Raw
$nuxtConfig = Get-Content -LiteralPath $nuxtConfigPath -Raw
$baseCss = Get-Content -LiteralPath $baseCssPath -Raw
$shellComposable = Get-Content -LiteralPath $shellComposablePath -Raw
$navComposable = Get-Content -LiteralPath $navComposablePath -Raw

Assert-Contains $nuxtConfig "engram · консоль оператора" "operator console title"
Assert-Contains $nuxtConfig "~/assets/base.css" "document-level design reset"
Assert-Contains $baseCss "html," "html reset selector"
Assert-Contains $baseCss "body," "body reset selector"
Assert-Contains $baseCss "#__nuxt" "nuxt root height reset"
Assert-Contains $baseCss "margin: 0;" "browser body margin reset"
Assert-Contains $baseCss "overflow: hidden;" "outer document scroll suppression"
Assert-Contains $baseCss "background: var(--bg);" "document background token bridge"
Assert-Contains $navComposable "grpKey: 'workspace'" "workspace nav group"
Assert-Contains $navComposable "grpKey: 'memoryProduct'" "memory/product nav group"
Assert-Contains $layout "statusbar" "status bar"
Assert-Contains $layout "useOperatorShellStatus" "shell live-status composable"
Assert-Contains $layout "authPostureLabel" "visible auth posture label"
Assert-Contains $layout "info.version" "server version binding"
Assert-Contains $layout "info.uptime" "server uptime binding"
Assert-Contains $layout "info.health" "server health binding"

Assert-Contains $shellComposable "loadOperatorJson<AuthMe>('/api/auth/me'" "auth endpoint seam"
Assert-Contains $shellComposable "loadOperatorJson<ApiSelfcheck>('/api/selfcheck'" "selfcheck endpoint seam"
Assert-Contains $shellComposable "loadOperatorJson<ApiStats>('/api/stats'" "stats endpoint seam"
Assert-Contains $shellComposable "loadOperatorJson<ApiStatsVnext>('/api/stats/vnext'" "stats vnext endpoint seam"
Assert-Contains $shellComposable "auth_disabled" "auth-disabled posture signal"
Assert-Contains $shellComposable "preferKnownUptime(next.uptime, statsResult.value.data.uptime)" "stats uptime fallback guard"
Assert-NotContains $shellComposable "next.uptime = statsResult.value.data.uptime || next.uptime" "raw stats uptime override"

if ($layout.Contains("operator SESSION") -or $layout.Contains("Auth disabled")) {
  throw "SHELL_SMOKE_FAILED: legacy dashboard shell/auth literal is still present"
}

Write-Host "SHELL_SMOKE=passed"
