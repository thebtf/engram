[CmdletBinding()]
param(
  [Parameter(Mandatory)]
  [string]$SourceRoot,
  [string]$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path,
  [switch]$DesignOnly,
  [switch]$AllowAppWrite
)

$ErrorActionPreference = 'Stop'
$ToolVersion = 'g1-design-only-3'

$Allowlist = @(
  @{ Source = 'PRODUCT.md'; Target = 'contracts/PRODUCT.md' },
  @{ Source = 'DESIGN.md'; Target = 'contracts/DESIGN.md' },
  @{ Source = 'DESIGN-SYNC-PROTOCOL.md'; Target = 'contracts/DESIGN-SYNC-PROTOCOL.md' },
  @{ Source = 'DEVELOPER-PLAYBOOK.md'; Target = 'contracts/DEVELOPER-PLAYBOOK.md' },
  @{ Source = 'HANDOFF-data-integration.md'; Target = 'contracts/HANDOFF-data-integration.md' },
  @{ Source = 'RUNTIME-SUBSTRATE-MAP.md'; Target = 'contracts/RUNTIME-SUBSTRATE-MAP.md' },
  @{ Source = 'INTEGRATION-AGENT-PROMPT.md'; Target = 'contracts/INTEGRATION-AGENT-PROMPT.md' },
  @{ Source = 'ACCESS-ADMIN-spec.md'; Target = 'contracts/ACCESS-ADMIN-spec.md' },
  @{ Source = 'DESIGNER-endpoints-brief.md'; Target = 'contracts/DESIGNER-endpoints-brief.md' },
  @{ Source = 'index.html'; Target = 'mockups/index.html' },
  @{ Source = 'access-admin.html'; Target = 'mockups/access-admin.html' },
  @{ Source = 'saas-admin.html'; Target = 'mockups/saas-admin.html' },
  @{ Source = 'components.html'; Target = 'mockups/components.html' }
)

$RouteFrames = @(
  [ordered]@{ id='overview'; kind='route'; route='/'; mockup='mockups/index.html#overview' },
  [ordered]@{ id='search'; kind='route'; route='/search'; mockup='mockups/index.html#search' },
  [ordered]@{ id='memory'; kind='route'; route='/memory'; mockup='mockups/index.html#memories' },
  [ordered]@{ id='queue'; kind='route'; route='/queue'; mockup='mockups/index.html#queue' },
  [ordered]@{ id='noise'; kind='route'; route='/noise'; mockup='mockups/index.html#noise' },
  [ordered]@{ id='graph'; kind='route'; route='/graph'; mockup='mockups/index.html#graph' },
  [ordered]@{ id='books'; kind='route'; route='/books'; mockup='mockups/index.html#books' },
  [ordered]@{ id='rules'; kind='route'; route='/rules'; mockup='mockups/index.html#rules' },
  [ordered]@{ id='issues'; kind='route'; route='/issues'; mockup='mockups/index.html#issues' },
  [ordered]@{ id='projects'; kind='route'; route='/projects'; mockup='mockups/index.html#projects' },
  [ordered]@{ id='secrets'; kind='route'; route='/secrets'; mockup='mockups/index.html#secrets' },
  [ordered]@{ id='documents'; kind='route'; route='/documents'; mockup='mockups/index.html#documents' },
  [ordered]@{ id='access'; kind='route'; route='/access'; mockup='mockups/access-admin.html' },
  [ordered]@{ id='settings'; kind='route'; route='/settings'; mockup='mockups/index.html#settings' },
  [ordered]@{ id='health'; kind='route'; route='/health'; mockup='mockups/index.html#health' },
  [ordered]@{ id='shell'; kind='frame'; route=$null; mockup='mockups/index.html (app shell)' },
  [ordered]@{ id='settings-modal'; kind='frame'; route=$null; mockup='mockups/index.html#settings-modal' }
)

function Resolve-ExistingPath([string]$Path, [string]$Label) {
  if (-not (Test-Path -LiteralPath $Path -PathType Container)) { throw "$Label does not exist: $Path" }
  return (Resolve-Path -LiteralPath $Path).Path
}

function Assert-ChildPath([string]$Root, [string]$Candidate, [string]$Label) {
  $fullRoot = [System.IO.Path]::GetFullPath($Root).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
  $fullCandidate = [System.IO.Path]::GetFullPath($Candidate)
  $prefix = $fullRoot + [System.IO.Path]::DirectorySeparatorChar
  if (-not $fullCandidate.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "$Label escapes root: $fullCandidate (root: $fullRoot)"
  }
  return $fullCandidate
}

function Get-CanonicalBytes([string]$Path) {
  [byte[]]$raw = [System.IO.File]::ReadAllBytes($Path)
  $canonical = [System.Collections.Generic.List[byte]]::new($raw.Length)
  for ($index = 0; $index -lt $raw.Length; $index++) {
    if ($raw[$index] -eq 13 -and $index + 1 -lt $raw.Length -and $raw[$index + 1] -eq 10) {
      $canonical.Add(10)
      $index++
    } else {
      $canonical.Add($raw[$index])
    }
  }
  return $canonical.ToArray()
}

function Get-Sha256([byte[]]$Bytes) {
  $sha256 = [System.Security.Cryptography.SHA256]::Create()
  try { return ([System.BitConverter]::ToString($sha256.ComputeHash($Bytes))).Replace('-', '').ToLowerInvariant() }
  finally { $sha256.Dispose() }
}

function Write-BytesIfChanged([string]$Path, [byte[]]$Bytes) {
  if (Test-Path -LiteralPath $Path -PathType Leaf) {
    [byte[]]$existing = [System.IO.File]::ReadAllBytes($Path)
    if ([System.Linq.Enumerable]::SequenceEqual[byte]($existing, $Bytes)) { return $false }
  }
  New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Path) | Out-Null
  [System.IO.File]::WriteAllBytes($Path, $Bytes)
  return $true
}

$SourceRoot = Resolve-ExistingPath $SourceRoot 'SourceRoot'
$RepoRoot = Resolve-ExistingPath $RepoRoot 'RepoRoot'
$DesignTarget = Join-Path $RepoRoot 'design/operator-console'
$AppTarget = Join-Path $RepoRoot 'apps/operator-console'
Assert-ChildPath $RepoRoot $DesignTarget 'design target' | Out-Null
Assert-ChildPath $RepoRoot $AppTarget 'app target' | Out-Null
$DesignSource = Assert-ChildPath $SourceRoot (Join-Path $SourceRoot 'DESIGN.md') 'design version source'
if (-not (Test-Path -LiteralPath $DesignSource -PathType Leaf)) { throw 'Missing allowlisted source: DESIGN.md' }
$DesignText = [System.Text.UTF8Encoding]::new($false, $true).GetString((Get-CanonicalBytes $DesignSource))
$DesignVersionMatch = [regex]::Match($DesignText, '(?m)^design_version:\s*["'']?([0-9.]+)["'']?')
if (-not $DesignVersionMatch.Success) { throw 'DESIGN.md has no design_version stamp' }
$DesignVersion = $DesignVersionMatch.Groups[1].Value

if ($AllowAppWrite) {
  $Port = Join-Path $SourceRoot 'nuxt-port'
  if (-not (Test-Path -LiteralPath $Port -PathType Container)) { throw "App preflight needs nuxt-port under SourceRoot: $Port" }
  $Conflicts = Get-ChildItem -LiteralPath $Port -Recurse -File | ForEach-Object {
    $relative = [System.IO.Path]::GetRelativePath($Port, $_.FullName)
    $target = Assert-ChildPath $AppTarget (Join-Path $AppTarget $relative) 'app candidate'
    if (Test-Path -LiteralPath $target) { $relative }
  }
  Write-Host 'APP PROMOTION PREFLIGHT (no files written):'
  if ($Conflicts) { $Conflicts | Sort-Object | ForEach-Object { Write-Host "  conflict: $_" } } else { Write-Host '  no existing targets' }
  throw 'Runtime promotion is intentionally disabled in G1. Review the conflict report and implement a separately approved runtime slice.'
}

$Changes = @()
$ManifestFiles = @()
foreach ($entry in $Allowlist) {
  $source = Assert-ChildPath $SourceRoot (Join-Path $SourceRoot $entry.Source) 'allowlisted source'
  if (-not (Test-Path -LiteralPath $source -PathType Leaf)) { throw "Missing allowlisted source: $($entry.Source)" }
  $target = Assert-ChildPath $DesignTarget (Join-Path $DesignTarget $entry.Target) 'allowlisted target'
  [byte[]]$sourceBytes = Get-CanonicalBytes $source
  $sourceHash = Get-Sha256 $sourceBytes
  $targetHash = if (Test-Path -LiteralPath $target -PathType Leaf) { Get-Sha256 (Get-CanonicalBytes $target) } else { $null }
  $changed = $sourceHash -ne $targetHash
  $Changes += [pscustomobject]@{ Path=$entry.Target; Status=if($changed){'changed'}else{'unchanged'}; SourceHash=$sourceHash; TargetHash=$targetHash; CanonicalBytes=$sourceBytes }
  $ManifestFiles += [ordered]@{ path=$entry.Target; sha256=$sourceHash; source_classification='private-authoring-snapshot' }
}

Write-Host "Promotion check: $($Changes.Count) allowlisted files from $SourceRoot"
$Changes | ForEach-Object { Write-Host "  $($_.Status): $($_.Path) $($_.SourceHash)" }

$manifestPath = Assert-ChildPath $DesignTarget (Join-Path $DesignTarget 'PROMOTION-MANIFEST.json') 'manifest target'
$ExistingManifest = if (Test-Path -LiteralPath $manifestPath -PathType Leaf) {
  Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
} else { $null }

if ($ExistingManifest -and $ExistingManifest.design_version -eq $DesignVersion -and ($Changes | Where-Object { $_.Status -eq 'changed' })) {
  $changedPaths = ($Changes | Where-Object { $_.Status -eq 'changed' } | ForEach-Object { $_.Path }) -join ', '
  throw "Refusing promotion: allowlisted content changed ($changedPaths) but design_version is still $DesignVersion. Bump design_version in DESIGN.md before promoting."
}

$VersionDateMatch = [regex]::Match($DesignVersion, '^(\d{4})\.(\d{2})\.(\d{2})$')
if ($VersionDateMatch.Success) {
  $PromotedAtUtc = "{0}-{1}-{2}T00:00:00Z" -f $VersionDateMatch.Groups[1].Value, $VersionDateMatch.Groups[2].Value, $VersionDateMatch.Groups[3].Value
} elseif ($ExistingManifest -and $ExistingManifest.design_version -eq $DesignVersion -and $ExistingManifest.promoted_at_utc) {
  $PromotedAtUtc = $ExistingManifest.promoted_at_utc
} else {
  throw "design_version '$DesignVersion' is not in YYYY.MM.DD form and no prior promotion exists to preserve promoted_at_utc from."
}

if (-not $DesignOnly) {
  Write-Host 'Check mode only: no files written. Re-run with -DesignOnly to promote this allowlist.'
  exit 0
}

foreach ($change in $Changes) {
  $target = Assert-ChildPath $DesignTarget (Join-Path $DesignTarget $change.Path) 'allowlisted target'
  Write-BytesIfChanged $target $change.CanonicalBytes | Out-Null
}

$snapshotInput = "$DesignVersion`n" + (($ManifestFiles | ForEach-Object { "$($_.path)`0$($_.sha256)" }) -join "`n")
$SnapshotSha256 = Get-Sha256 ([System.Text.UTF8Encoding]::new($false).GetBytes($snapshotInput))
$manifest = [ordered]@{
  schema_version = 1
  design_version = $DesignVersion
  promoted_at_utc = $PromotedAtUtc
  content_sha256 = $SnapshotSha256
  source_classification = 'private-authoring-snapshot'
  promotion_tool = [ordered]@{ path='scripts/promote-od-operator-console.ps1'; version=$ToolVersion }
  files = $ManifestFiles
  route_frames = $RouteFrames
  historical_frames = @(
    [ordered]@{ mockup='mockups/saas-admin.html'; classification='historical-non-routed-reference' },
    [ordered]@{ mockup='mockups/components.html'; classification='component-reference-not-a-route' }
  )
  private_material_excluded = @('images', 'prompts', 'agent state', 'artifact metadata', 'nested .git', 'intermediate files', '.od-skills', '.nuxt', '.output', 'node_modules')
  exclusions_statement = 'Only the explicit allowlist is promoted. No private OpenDesign workspace material is tracked.'
}
$manifestJson = (($manifest | ConvertTo-Json -Depth 8) -replace "`r`n", "`n") + "`n"
Write-BytesIfChanged $manifestPath ([System.Text.UTF8Encoding]::new($false).GetBytes($manifestJson)) | Out-Null
Write-Host "Design-only promotion complete: $manifestPath"
