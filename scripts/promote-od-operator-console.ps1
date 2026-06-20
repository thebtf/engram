param(
  [string]$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path,
  [switch]$SkipApp,
  [switch]$SkipDesign,
  [switch]$NoBootstrap
)

$ErrorActionPreference = "Stop"

function Resolve-FullPath {
  param([string]$Path)
  return [System.IO.Path]::GetFullPath($Path)
}

function Assert-ChildPath {
  param(
    [string]$Root,
    [string]$Candidate,
    [string]$Label
  )

  $resolvedRoot = Resolve-FullPath $Root
  $resolvedCandidate = Resolve-FullPath $Candidate

  if (-not $resolvedCandidate.StartsWith($resolvedRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "$Label path escapes root: $resolvedCandidate (root: $resolvedRoot)"
  }
}

function Ensure-Directory {
  param([string]$Path)
  if (-not (Test-Path -LiteralPath $Path)) {
    New-Item -ItemType Directory -Path $Path | Out-Null
  }
}

function Copy-FileForced {
  param(
    [string]$Source,
    [string]$Target
  )

  $targetDir = Split-Path -Parent $Target
  Ensure-Directory $targetDir
  Copy-Item -LiteralPath $Source -Destination $Target -Force
}

function Copy-FileIfMissing {
  param(
    [string]$Source,
    [string]$Target
  )

  $targetDir = Split-Path -Parent $Target
  Ensure-Directory $targetDir

  if (-not (Test-Path -LiteralPath $Target)) {
    Copy-Item -LiteralPath $Source -Destination $Target -Force
    Write-Host "bootstrapped $Target"
  } else {
    Write-Host "kept developer-owned $Target"
  }
}

function Reset-DirectoryFromSource {
  param(
    [string]$Source,
    [string]$Target,
    [string]$Root
  )

  Assert-ChildPath -Root $Root -Candidate $Target -Label "target"
  if (Test-Path -LiteralPath $Target) {
    Remove-Item -LiteralPath $Target -Recurse -Force
  }

  Ensure-Directory $Target
  Copy-Item -Path (Join-Path $Source "*") -Destination $Target -Recurse -Force
}

$RepoRoot = Resolve-FullPath $RepoRoot
$SourceRoot = Join-Path $RepoRoot ".od"
$SourcePort = Join-Path $SourceRoot "nuxt-port"
$AppTarget = Join-Path $RepoRoot "apps\operator-console"
$DesignTarget = Join-Path $RepoRoot "design\operator-console"

if (-not (Test-Path -LiteralPath $SourceRoot)) {
  throw "OpenDesign root not found: $SourceRoot"
}

if (-not (Test-Path -LiteralPath $SourcePort)) {
  throw "OpenDesign Nuxt scaffold not found: $SourcePort"
}

$DesignDocs = @(
  "PRODUCT.md",
  "DESIGN.md",
  "DESIGN-SYNC-PROTOCOL.md",
  "DEVELOPER-PLAYBOOK.md",
  "HANDOFF-data-integration.md",
  "RUNTIME-SUBSTRATE-MAP.md",
  "INTEGRATION-AGENT-PROMPT.md",
  "ACCESS-ADMIN-spec.md",
  "DESIGNER-endpoints-brief.md"
)

$DesignMockups = @(
  "index.html",
  "access-admin.html",
  "saas-admin.html",
  "components.html"
)

$AppSyncedDirs = @(
  "assets",
  "components",
  "i18n",
  "layouts",
  "pages"
)

$AppSyncedFiles = @(
  "app.vue",
  "app.config.ts.example",
  "README.md",
  "NUXT-PORT-GUIDE.md",
  "PARITY.json",
  "PARITY.md",
  "PARITY.schema.json",
  "composables\useHonesty.ts",
  "composables\useNav.ts",
  "scripts\parity-check.mjs"
)

$AppBootstrapFiles = @(
  "composables\useMockData.ts",
  "nuxt.config.ts",
  "package.json",
  "package-lock.json"
)

if (-not $SkipDesign) {
  Ensure-Directory $DesignTarget
  Ensure-Directory (Join-Path $DesignTarget "contracts")
  Ensure-Directory (Join-Path $DesignTarget "mockups")

  foreach ($file in $DesignDocs) {
    Copy-FileForced `
      -Source (Join-Path $SourceRoot $file) `
      -Target (Join-Path $DesignTarget ("contracts\" + $file))
  }

  foreach ($file in $DesignMockups) {
    Copy-FileForced `
      -Source (Join-Path $SourceRoot $file) `
      -Target (Join-Path $DesignTarget ("mockups\" + $file))
  }
}

if (-not $SkipApp) {
  Ensure-Directory $AppTarget

  foreach ($dir in $AppSyncedDirs) {
    Reset-DirectoryFromSource `
      -Source (Join-Path $SourcePort $dir) `
      -Target (Join-Path $AppTarget $dir) `
      -Root $AppTarget
  }

  foreach ($file in $AppSyncedFiles) {
    Copy-FileForced `
      -Source (Join-Path $SourcePort $file) `
      -Target (Join-Path $AppTarget $file)
  }

  if (-not $NoBootstrap) {
    foreach ($file in $AppBootstrapFiles) {
      Copy-FileIfMissing `
        -Source (Join-Path $SourcePort $file) `
        -Target (Join-Path $AppTarget $file)
    }
  }
}

Write-Host ""
Write-Host "Promotion complete."
Write-Host "  source: $SourceRoot"
if (-not $SkipDesign) { Write-Host "  curated contract: $DesignTarget" }
if (-not $SkipApp) { Write-Host "  deployable app: $AppTarget" }
