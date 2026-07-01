param(
  [string]$OutputPath = ".agent\specs\operator-console\evidence\operator-console-release-evidence.json",
  [string]$ValidatePath = "",
  [string]$RemoteTarget = "http://unleashed.lan:37777",
  [ValidateSet("PASS", "FAIL", "NOT_RUN")]
  [string]$ParityResult = "PASS",
  [ValidateSet("PASS", "FAIL", "NOT_RUN")]
  [string]$GenerateResult = "PASS",
  [ValidateSet("PASS", "FAIL", "NOT_RUN")]
  [string]$SmokeResult = "PASS",
  [ValidateSet("PASS", "FAIL", "NOT_RUN")]
  [string]$RemoteSmokeResult = "NOT_RUN"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$parityPath = Join-Path $repoRoot "apps\operator-console\PARITY.json"

$requiredFields = @(
  "app_source_commit",
  "git_dirty_state",
  "design_version",
  "parity_result",
  "generate_result",
  "smoke_result",
  "remote_target",
  "honesty_summary",
  "timestamp"
)

function Resolve-RepoPath {
  param([Parameter(Mandatory = $true)][string]$Path)
  if ([System.IO.Path]::IsPathRooted($Path)) {
    return $Path
  }
  return Join-Path $repoRoot $Path
}

function Assert-RequiredEvidence {
  param([Parameter(Mandatory = $true)][object]$Evidence)

  foreach ($field in $requiredFields) {
    if (-not $Evidence.PSObject.Properties.Name.Contains($field)) {
      throw "EVIDENCE_VALIDATION_FAILED: missing required field $field"
    }
    $value = $Evidence.$field
    if ($null -eq $value -or ($value -is [string] -and [string]::IsNullOrWhiteSpace($value))) {
      throw "EVIDENCE_VALIDATION_FAILED: empty required field $field"
    }
  }

  $summary = $Evidence.honesty_summary
  foreach ($bucket in @("live", "gated", "stale", "mustbuild")) {
    $hasBucket = if ($summary -is [System.Collections.IDictionary]) {
      $summary.Contains($bucket)
    }
    else {
      $summary.PSObject.Properties.Name.Contains($bucket)
    }

    if (-not $hasBucket) {
      throw "EVIDENCE_VALIDATION_FAILED: honesty_summary missing $bucket"
    }
  }
}

if ($ValidatePath) {
  $resolvedValidatePath = Resolve-RepoPath $ValidatePath
  if (-not (Test-Path -LiteralPath $resolvedValidatePath)) {
    throw "EVIDENCE_VALIDATION_FAILED: file not found $resolvedValidatePath"
  }

  $existing = Get-Content -LiteralPath $resolvedValidatePath -Raw | ConvertFrom-Json
  Assert-RequiredEvidence -Evidence $existing
  Write-Host "OPERATOR_CONSOLE_EVIDENCE_VALID=passed"
  return
}

if (-not (Test-Path -LiteralPath $parityPath)) {
  throw "EVIDENCE_WRITE_FAILED: missing parity ledger $parityPath"
}

$parity = Get-Content -LiteralPath $parityPath -Raw | ConvertFrom-Json
$head = (& git -C $repoRoot rev-parse HEAD).Trim()
$dirty = @(& git -C $repoRoot status --short)

$evidence = [ordered]@{
  app_source_commit = $head
  git_dirty_state = [ordered]@{
    clean = ($dirty.Count -eq 0)
    entries = $dirty
  }
  design_version = $parity.design_version
  parity_result = $ParityResult
  generate_result = $GenerateResult
  smoke_result = $SmokeResult
  remote_smoke_result = $RemoteSmokeResult
  remote_target = $RemoteTarget
  honesty_summary = [ordered]@{
    live = @("shell", "overview", "memory", "rules", "issues", "secrets", "projects", "health", "settings", "search", "noise")
    gated = @("queue")
    stale = @("collections")
    mustbuild = @("graph", "books", "documents", "access")
  }
  parity_summary = [ordered]@{
    fidelity = ($parity.sections | Group-Object -Property fidelity | ForEach-Object { [ordered]@{ name = $_.Name; count = $_.Count } })
    i18n = ($parity.sections | Group-Object -Property i18n | ForEach-Object { [ordered]@{ name = $_.Name; count = $_.Count } })
    open_gaps = (($parity.sections | ForEach-Object { $_.gaps }).Count)
  }
  timestamp = (Get-Date).ToUniversalTime().ToString("o")
}

Assert-RequiredEvidence -Evidence ([pscustomobject]$evidence)

$resolvedOutputPath = Resolve-RepoPath $OutputPath
$parent = Split-Path -Parent $resolvedOutputPath
if ($parent -and -not (Test-Path -LiteralPath $parent)) {
  New-Item -ItemType Directory -Path $parent | Out-Null
}

$evidence | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath $resolvedOutputPath -Encoding UTF8
Write-Host "OPERATOR_CONSOLE_EVIDENCE=written"
Write-Host ("EVIDENCE_PATH=" + $resolvedOutputPath)
