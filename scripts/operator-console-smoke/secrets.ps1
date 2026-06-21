param(
  [string]$AppRoot = "apps\operator-console"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$root = Join-Path $repoRoot $AppRoot
$secretsPath = Join-Path $root "pages\secrets.vue"
$secretsComposablePath = Join-Path $root "composables\useOperatorSecrets.ts"

function Assert-Contains {
  param(
    [Parameter(Mandatory = $true)][string]$Content,
    [Parameter(Mandatory = $true)][string]$Needle,
    [Parameter(Mandatory = $true)][string]$Label
  )

  if (-not $Content.Contains($Needle)) {
    throw "SECRETS_SMOKE_FAILED: missing $Label ($Needle)"
  }
}

function Assert-NotContains {
  param(
    [Parameter(Mandatory = $true)][string]$Content,
    [Parameter(Mandatory = $true)][string]$Needle,
    [Parameter(Mandatory = $true)][string]$Label
  )

  if ($Content.Contains($Needle)) {
    throw "SECRETS_SMOKE_FAILED: forbidden $Label ($Needle)"
  }
}

function Assert-File {
  param([Parameter(Mandatory = $true)][string]$Path)
  if (-not (Test-Path -LiteralPath $Path)) {
    throw "SECRETS_SMOKE_FAILED: missing required file $Path"
  }
}

Assert-File $secretsPath
Assert-File $secretsComposablePath

$secrets = Get-Content -LiteralPath $secretsPath -Raw
$secretsComposable = Get-Content -LiteralPath $secretsComposablePath -Raw

Assert-Contains $secrets "useOperatorSecrets" "secrets live adapter"
Assert-Contains $secrets "loadState" "credential load-state binding"
Assert-Contains $secrets "vaultState" "vault status binding"
Assert-Contains $secrets "pending" "pending state"
Assert-Contains $secrets "error" "error state"
Assert-Contains $secrets "revealSecret" "one-shot reveal action"
Assert-Contains $secrets "createSecret" "store credential action"
Assert-Contains $secrets "deleteOpened" "delete credential action"
Assert-Contains $secrets "cleanupOrphans" "orphan cleanup action"
Assert-Contains $secrets "rotationGap.evidence.endpoint" "rotation mustbuild evidence"

Assert-Contains $secretsComposable "loadOperatorJson<ApiVaultStatus>('/api/vault/status'" "vault status endpoint"
Assert-Contains $secretsComposable "loadOperatorJson<ApiVaultCredential[]>('/api/vault/credentials'" "credentials list endpoint"
Assert-Contains $secretsComposable "operatorFetchJson<ApiVaultReveal>" "credential reveal endpoint"
Assert-Contains $secretsComposable "operatorFetchJson<ApiVaultStoreReceipt>('/api/vault/credentials'" "credential store endpoint"
Assert-Contains $secretsComposable 'operatorFetchJson(`/api/vault/credentials/${encodeURIComponent(name)}`' "credential delete endpoint"
Assert-Contains $secretsComposable "operatorFetchJson<ApiVaultOrphanReceipt>('/api/vault/orphaned-credentials'" "orphan cleanup endpoint"
Assert-Contains $secretsComposable "POST /api/vault/rotate" "rotation endpoint evidence"
Assert-Contains $secretsComposable "runOperatorMutation" "mutation seam"
Assert-Contains $secretsComposable "unsupportedOperatorAction" "mustbuild helper"

Assert-NotContains $secrets "sk-live-" "mock revealed secret"
Assert-NotContains $secrets "localStorage" "secret persistence"
Assert-NotContains $secrets "fingerprint: Array.from" "client-generated fingerprint"
Assert-NotContains $secrets "onRotate(_keys" "fake rotation handler"
Assert-NotContains $secrets "KeyRotationModal" "operable rotation modal without backend"
Assert-NotContains $secrets "ref<Record<string, string>>" "global revealed-value map"

Write-Host "SECRETS_SMOKE=passed"
