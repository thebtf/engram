#Requires -Version 7
<#
.SYNOPSIS
    Invariant checker for engram v5 cleanup (T006 / Phase 2) — Windows dev workstation variant.

.DESCRIPTION
    Verifies vault credentials have not drifted by calling the engram vault status API.
    Postgres entity checks are skipped (no Docker socket on Windows dev workstations).
    Uses the same safety-gate-baseline.json produced by T006a.

    Exit codes:
      0  All invariants satisfied
      1  One or more invariants violated
      2  Configuration or prerequisite error (missing token, unreachable server, etc.)

.PARAMETER Baseline
    Path to the baseline JSON file. Defaults to safety-gate-baseline.json in the same
    directory as this script.

.PARAMETER Phase
    Which credentials invariant to apply:
      auto     (default) Always uses pre-us3 on Windows (no DB access to detect migration state)
      pre-us3  Check vault.credential_count from the vault API
      post-us3 Check entities_post_us3.credentials.exact from the vault API

.PARAMETER Help
    Print usage information and exit 0.

.EXAMPLE
    $env:ENGRAM_API_TOKEN = 'mytoken'
    pwsh scripts/safety-gate.ps1

.EXAMPLE
    $env:ENGRAM_API_TOKEN = 'mytoken'
    pwsh scripts/safety-gate.ps1 -Phase post-us3 -Baseline /custom/baseline.json
#>

[CmdletBinding()]
param(
    [string]$Baseline = (Join-Path -Path $PSScriptRoot -ChildPath 'safety-gate-baseline.json'),

    [ValidateSet('auto', 'pre-us3', 'post-us3')]
    [string]$Phase = 'auto',

    [switch]$Help
)

Set-StrictMode -Version 3.0
$ErrorActionPreference = 'Stop'

# ---------------------------------------------------------------------------
# Usage
# ---------------------------------------------------------------------------

function Show-Usage {
    @'
Usage: $env:ENGRAM_API_TOKEN=<token> pwsh scripts/safety-gate.ps1 [OPTIONS]

Options:
  -Baseline <path>                  Path to baseline JSON file.
                                    Default: scripts/safety-gate-baseline.json
  -Phase auto|pre-us3|post-us3      Which credentials invariant to apply.
                                    auto     : always uses pre-us3 on Windows (no DB access)
                                    pre-us3  : check vault.credential_count
                                    post-us3 : check entities_post_us3.credentials.exact
  -Help                             Show this message.

Environment:
  ENGRAM_API_TOKEN                  Bearer token for the engram server (required).

Notes:
  Postgres entity checks are skipped on Windows (no Docker socket access).
  Run scripts/safety-gate.sh on CI or Unraid for full entity validation.
'@
}

if ($Help) {
    Show-Usage
    exit 0
}

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

function Write-ConfigError {
    param([string]$Message)
    [Console]::Error.WriteLine("SAFETY-GATE CONFIG ERROR: $Message")
    exit 2
}

function Write-InvariantFail {
    param([string]$Message)
    [Console]::Error.WriteLine("  FAIL: $Message")
}

# ---------------------------------------------------------------------------
# Prerequisites and config validation
# ---------------------------------------------------------------------------

$token = $env:ENGRAM_API_TOKEN
if ([string]::IsNullOrWhiteSpace($token)) {
    Write-ConfigError 'ENGRAM_API_TOKEN environment variable is required but not set'
}

if (-not (Test-Path -LiteralPath $Baseline -PathType Leaf)) {
    Write-ConfigError "Baseline file not found: $Baseline"
}

# ---------------------------------------------------------------------------
# Load baseline
# ---------------------------------------------------------------------------

$baselineJson = Get-Content -LiteralPath $Baseline -Raw | ConvertFrom-Json

$serverUrl         = $baselineJson.server_url
$vaultFp           = $baselineJson.vault.fingerprint
$vaultCredCount    = $baselineJson.vault.credential_count
$vaultMismatchMax  = $baselineJson.vault.mismatch_count_max
$postUs3CredCount  = $baselineJson.entities_post_us3.credentials.exact

# ---------------------------------------------------------------------------
# Phase resolution
# ---------------------------------------------------------------------------

# On Windows there is no Docker socket, so auto always resolves to pre-us3.
$resolvedPhase = switch ($Phase) {
    'post-us3' { 'post-us3' }
    'pre-us3'  { 'pre-us3' }
    default    { 'pre-us3' }  # auto → pre-us3 (Windows: no DB access)
}

$expectedCredCount = if ($resolvedPhase -eq 'post-us3') { $postUs3CredCount } else { $vaultCredCount }

# ---------------------------------------------------------------------------
# Gate 1: Vault status API call
# ---------------------------------------------------------------------------

Write-Output "Checking vault status at ${serverUrl}/api/vault/status ..."

$vaultUri = "${serverUrl}/api/vault/status"
$headers  = @{ Authorization = "Bearer $token" }

$vaultResponse = $null
try {
    $vaultResponse = Invoke-RestMethod `
        -Uri $vaultUri `
        -Method Get `
        -Headers $headers `
        -ErrorAction Stop
}
catch {
    $msg = $_.Exception.Message
    [Console]::Error.WriteLine("SAFETY-GATE ERROR: unable to reach ${vaultUri}: $msg")
    [Console]::Error.WriteLine('  Verify the server is running and ENGRAM_API_TOKEN is valid.')
    exit 2
}

# Validate response shape — fingerprint + credential_count are mandatory.
# mismatch_count is emitted by the vault API ONLY when > 0; treat absence as 0 (healthy).
# Use PSObject.Properties safe lookup: StrictMode 3.0 throws on missing-property direct access.
$respProps = $vaultResponse.PSObject.Properties

if ($null -eq $respProps['fingerprint'] -or
    $null -eq $respProps['credential_count']) {
    [Console]::Error.WriteLine('SAFETY-GATE ERROR: Unexpected vault response - missing required fields (fingerprint or credential_count)')
    exit 2
}

$gotFp        = [string]$respProps['fingerprint'].Value
$gotCredCount = [int]$respProps['credential_count'].Value
$gotMismatch  = if ($null -eq $respProps['mismatch_count']) { 0 } else { [int]$respProps['mismatch_count'].Value }

# ---------------------------------------------------------------------------
# Evaluate invariants
# ---------------------------------------------------------------------------

$failures     = [System.Collections.Generic.List[string]]::new()
$summaryParts = [System.Collections.Generic.List[string]]::new()

# Fingerprint
if ($gotFp -ne $vaultFp) {
    $msg = "fingerprint: expected $vaultFp, got $gotFp"
    $failures.Add($msg)
    Write-InvariantFail $msg
}
else {
    $summaryParts.Add("fingerprint=$gotFp")
}

# Credential count
if ($gotCredCount -ne $expectedCredCount) {
    $msg = "credential_count: expected $expectedCredCount, got $gotCredCount (phase=$resolvedPhase)"
    $failures.Add($msg)
    Write-InvariantFail $msg
}
else {
    $summaryParts.Add("credentials=$gotCredCount")
}

# Mismatch count
if ($gotMismatch -gt $vaultMismatchMax) {
    $msg = "mismatch_count: expected <=$vaultMismatchMax, got $gotMismatch"
    $failures.Add($msg)
    Write-InvariantFail $msg
}
else {
    $summaryParts.Add("mismatches=$gotMismatch")
}

# ---------------------------------------------------------------------------
# Report
# ---------------------------------------------------------------------------

if ($failures.Count -gt 0) {
    [Console]::Error.WriteLine('')
    [Console]::Error.WriteLine("SAFETY-GATE: FAIL - $($failures.Count) invariant(s) violated:")
    foreach ($f in $failures) {
        [Console]::Error.WriteLine("  - $f")
    }
    exit 1
}

$summary = $summaryParts -join ', '
Write-Output "SAFETY-GATE: OK ($summary)"
exit 0
