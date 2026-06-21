param(
  [string]$AppRoot = "apps\operator-console"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$root = Join-Path $repoRoot $AppRoot
$pagesRoot = Join-Path $root "pages"
$componentPath = Join-Path $root "components\SectionStub.vue"
$localePaths = @(
  Join-Path $root "i18n\locales\ru.json"
  Join-Path $root "i18n\locales\en.json"
  Join-Path $root "i18n\locales\zh.json"
)

function Assert-Contains {
  param(
    [Parameter(Mandatory = $true)][string]$Content,
    [Parameter(Mandatory = $true)][string]$Needle,
    [Parameter(Mandatory = $true)][string]$Label
  )

  if (-not $Content.Contains($Needle)) {
    throw "DEFERRED_SMOKE_FAILED: missing $Label ($Needle)"
  }
}

function Assert-NotContains {
  param(
    [Parameter(Mandatory = $true)][string]$Content,
    [Parameter(Mandatory = $true)][string]$Needle,
    [Parameter(Mandatory = $true)][string]$Label
  )

  if ($Content.Contains($Needle)) {
    throw "DEFERRED_SMOKE_FAILED: forbidden $Label ($Needle)"
  }
}

function Read-Required {
  param([Parameter(Mandatory = $true)][string]$Path)
  if (-not (Test-Path -LiteralPath $Path)) {
    throw "DEFERRED_SMOKE_FAILED: missing required file $Path"
  }
  return Get-Content -LiteralPath $Path -Raw
}

$expected = @(
  @{ Name = "queue";       Class = "dormant";   Evidence = "VNEXT_F" },
  @{ Name = "graph";       Class = "mustbuild"; Evidence = "GET /api/graph" },
  @{ Name = "books";       Class = "mustbuild"; Evidence = "POST /api/books/ingest" },
  @{ Name = "documents";   Class = "mustbuild"; Evidence = "GET /api/documents" },
  @{ Name = "collections"; Class = "stale";     Evidence = "search_collection" },
  @{ Name = "access";      Class = "mustbuild"; Evidence = "GET /api/access/users" }
)

foreach ($page in $expected) {
  $content = Read-Required (Join-Path $pagesRoot "$($page.Name).vue")
  Assert-Contains $content "useI18n" "$($page.Name) locale seam"
  Assert-Contains $content "SectionStub" "$($page.Name) honest section stub"
  Assert-Contains $content "cls=`"$($page.Class)`"" "$($page.Name) honesty class"
  Assert-Contains $content $page.Evidence "$($page.Name) endpoint/flag evidence"
  Assert-Contains $content "t('deferred.$($page.Name).title')" "$($page.Name) keyed title"
  Assert-Contains $content "t('deferred.$($page.Name).lead')" "$($page.Name) keyed lead"
  Assert-Contains $content "t('deferred.$($page.Name).next')" "$($page.Name) keyed next"
  Assert-NotContains $content "<button" "$($page.Name) fake button"
  Assert-NotContains $content "@click" "$($page.Name) fake action"
}

$access = Read-Required (Join-Path $pagesRoot "access.vue")
Assert-NotContains $access "const users = [" "mock access users"
Assert-NotContains $access "EntityRow" "fake live access users"

$sectionStub = Read-Required $componentPath
Assert-Contains $sectionStub "useI18n" "SectionStub i18n seam"
Assert-Contains $sectionStub "sectionStub.state.mustbuild" "SectionStub keyed mustbuild state"
Assert-Contains $sectionStub "sectionStub.state.dormant" "SectionStub keyed dormant state"
Assert-Contains $sectionStub "sectionStub.state.stale" "SectionStub keyed stale state"

foreach ($localePath in $localePaths) {
  $locale = Read-Required $localePath
  Assert-Contains $locale "`"sectionStub`"" "$localePath sectionStub namespace"
  Assert-Contains $locale "`"deferred`"" "$localePath deferred namespace"
}

Write-Host "DEFERRED_SMOKE=passed"
