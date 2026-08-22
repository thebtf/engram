[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Receipt
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'recovery-fixture-common.ps1')

if ([string]::IsNullOrWhiteSpace($Receipt) -or ($Receipt -split '[\\/]') -contains '..')
{ throw 'receipt traversal is forbidden' 
}
$repositoryRoot = Get-RecoveryRepositoryRoot
$receiptCandidate = if ([IO.Path]::IsPathRooted($Receipt)) {
    $Receipt
} else {
    Join-Path $repositoryRoot $Receipt
}
$receiptPath = Assert-RecoveryContainedPath -Path $receiptCandidate -RepositoryRoot $repositoryRoot
if (-not (Test-Path -LiteralPath $receiptPath -PathType Leaf))
{ throw 'scenario receipt is missing' 
}
[void](Assert-RecoverySafeExistingPath -Path $receiptPath)
$receiptText = [IO.File]::ReadAllText($receiptPath)
Assert-RecoverySecretSafeText -Text $receiptText
try
{ $evidence = $receiptText | ConvertFrom-Json -Depth 32 
} catch
{ throw 'scenario receipt is malformed' 
}

Assert-RecoveryExactProperties -Object $evidence -Names @('schema_version', 'evidence_kind', 'release', 'scenario', 'observed_at_utc', 'scope', 'fixture', 'health', 'candidate', 'behavior', 'observations') -Label 'scenario receipt'
Assert-RecoveryExactProperties -Object $evidence.fixture -Names @('fixture_id', 'fixture_root', 'manifest_fingerprint', 'export_reference', 'export_fingerprint', 'restore_reference', 'selector_inventory_count') -Label 'scenario fixture evidence'
Assert-RecoveryExactProperties -Object $evidence.health -Names @('receipt_fingerprint', 'server_fingerprint', 'status') -Label 'scenario health evidence'
Assert-RecoveryExactProperties -Object $evidence.candidate -Names @('source_commit', 'built_payload_fingerprint') -Label 'scenario candidate evidence'
Assert-RecoveryExactProperties -Object $evidence.observations -Names @('fixture_containment', 'synthetic_restore', 'fixture_server_health', 'live_data_observed', 'installed_release_authority', 'ar1_baseline_receipt_authority') -Label 'scenario observations'

if ($evidence.schema_version -cne 'engram.recovery.scenario-evidence.v1' -or $evidence.evidence_kind -cne 'fixture_scenario' -or
    $evidence.release -cne 'AR-1' -or $evidence.scenario -cne 'baseline' -or $evidence.scope -cne 'synthetic_fixture_only' -or
    $evidence.fixture.fixture_id -cne $script:RecoveryFixtureID -or [string]::IsNullOrWhiteSpace([string]$evidence.fixture.fixture_root) -or
    $evidence.fixture.manifest_fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or $evidence.fixture.export_fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or
    $evidence.health.receipt_fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or $evidence.health.server_fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or
    $evidence.candidate.source_commit -cnotmatch '^[0-9a-f]{40}$' -or $evidence.candidate.built_payload_fingerprint -cne $evidence.health.server_fingerprint -or
    $evidence.observations.fixture_containment -cne 'validated' -or $evidence.observations.synthetic_restore -cne 'validated' -or
    $evidence.observations.fixture_server_health -cne 'ready' -or $evidence.observations.live_data_observed -ne $false -or
    $evidence.observations.installed_release_authority -cne 'not_claimed' -or $evidence.observations.ar1_baseline_receipt_authority -cne 'not_claimed')
{
    throw 'scenario receipt violates the fixture-only evidence contract'
}

$expectedCallbackPaths = @('/api/sessions/claude-session/propagate-outcome', '/api/sessions/openclaw-session/outcome')
$callbacks = @($evidence.behavior.retired_outcome_callbacks)
if ($callbacks.Count -ne $expectedCallbackPaths.Count)
{ throw 'scenario receipt lacks retired outcome callback evidence'
}
for ($index = 0; $index -lt $expectedCallbackPaths.Count; $index++)
{
    $callback = $callbacks[$index]
    Assert-RecoveryExactProperties -Object $callback -Names @('path', 'status_code', 'content_type', 'contract_version', 'code', 'action') -Label 'retired outcome callback evidence'
    if ($callback.path -cne $expectedCallbackPaths[$index] -or $callback.status_code -ne 410 -or $callback.content_type -cne 'application/json' -or
        $callback.contract_version -cne 'engram.outcome-retirement.v1' -or $callback.code -cne 'OUTCOME_CALLBACK_RETIRED' -or
        $callback.action -cne 'upgrade_outcome_adapter')
    { throw 'scenario receipt records a false retired outcome callback success'
    }
}

$selector = $evidence.behavior.selector_only_context_inject
Assert-RecoveryExactProperties -Object $selector -Names @('path', 'status_code', 'error_code', 'upgrade_action', 'canonical_project_returned') -Label 'selector-only context evidence'
if ($selector.path -cne '/api/context/inject' -or $selector.status_code -ne 409 -or
    $selector.error_code -cne 'PROJECT_IDENTITY_AMBIGUOUS' -or $selector.upgrade_action -cne 'send_project_identity_v2' -or
    $selector.canonical_project_returned -ne $false)
{ throw 'scenario receipt records a false selector-only context success'
}

Assert-RecoveryExactProperties -Object $evidence.behavior.health_after_behavior -Names @('status_code', 'status') -Label 'post-behavior health evidence'
if ($evidence.behavior.health_after_behavior.status_code -ne 200 -or $evidence.behavior.health_after_behavior.status -cne 'ready')
{ throw 'scenario receipt does not prove health after behavior checks'
}


$context = Get-RecoveryFixtureContext -FixtureRoot ([string]$evidence.fixture.fixture_root)
if ($context.RepositoryRoot -cne $repositoryRoot)
{ throw 'receipt repository scope is invalid' 
}
$expectedReceiptPrefix = $context.FixtureRoot.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar + 'receipts' + [IO.Path]::DirectorySeparatorChar
if (-not $receiptPath.StartsWith($expectedReceiptPrefix, [StringComparison]::OrdinalIgnoreCase))
{ throw 'scenario receipt is outside the owned receipt directory' 
}
Assert-RecoveryFixtureTreeSafe -FixtureRoot $context.FixtureRoot
[void](Assert-RecoveryFixtureOwner -Context $context)

$manifestPath = Join-Path $context.FixtureRoot 'fixture-manifest.json'
$manifest = Read-RecoveryJson -Path $manifestPath -Context $context -Label 'fixture manifest'
Assert-RecoveryExactProperties -Object $manifest -Names @('schema_version', 'fixture_id', 'fixture_root', 'fixture_class', 'created_at_utc', 'export', 'restore', 'selector_inventory', 'structural_fingerprints') -Label 'fixture manifest'
Assert-RecoveryExactProperties -Object $manifest.export -Names @('reference', 'fingerprint', 'schema_version') -Label 'fixture export metadata'
Assert-RecoveryExactProperties -Object $manifest.restore -Names @('reference', 'fingerprint', 'result') -Label 'fixture restore metadata'
if ($manifest.schema_version -cne $script:RecoveryFixtureSchema -or $manifest.fixture_id -cne $script:RecoveryFixtureID -or
    $manifest.fixture_root -cne $context.RelativeRoot -or $manifest.fixture_class -cne 'synthetic_redacted_legacy' -or
    $manifest.export.schema_version -cne 'engram.recovery.synthetic-export.v1' -or $manifest.restore.result -cne 'verified_equal_to_export' -or
    (Get-RecoverySha256 -Path $manifestPath) -cne $evidence.fixture.manifest_fingerprint -or
    $manifest.export.reference -cne $evidence.fixture.export_reference -or $manifest.export.fingerprint -cne $evidence.fixture.export_fingerprint -or
    $manifest.restore.reference -cne $evidence.fixture.restore_reference -or @($manifest.selector_inventory).Count -ne [int]$evidence.fixture.selector_inventory_count)
{
    throw 'scenario receipt does not match the owned fixture manifest'
}

$exportPath = Join-Path $context.FixtureRoot 'exports/legacy-fixture.json'
$restorePath = Join-Path $context.FixtureRoot 'restored/legacy-fixture.json'
if ((Get-RecoverySha256 -Path $exportPath) -cne $manifest.export.fingerprint -or (Get-RecoverySha256 -Path $restorePath) -cne $manifest.restore.fingerprint)
{
    throw 'fixture export or restore fingerprint mismatch'
}

$healthPath = Join-Path $context.FixtureRoot 'server/fixture-server-health.json'
$health = Read-RecoveryJson -Path $healthPath -Context $context -Label 'fixture health receipt'
Assert-RecoveryExactProperties -Object $health -Names @('schema_version', 'fixture_id', 'checked_at_utc', 'endpoint', 'health_status', 'server_version', 'server_fingerprint', 'source_commit', 'scope') -Label 'fixture health receipt'
if ($health.schema_version -cne 'engram.recovery.fixture-health.v1' -or $health.fixture_id -cne $script:RecoveryFixtureID -or
    $health.endpoint -cnotmatch '^http://127\.0\.0\.1:[0-9]{4,5}/api/health$' -or $health.health_status -cne 'ready' -or
    [string]::IsNullOrWhiteSpace([string]$health.server_version) -or $health.server_fingerprint -cne $evidence.health.server_fingerprint -or
    $health.source_commit -cnotmatch '^[0-9a-f]{40}$' -or $health.source_commit -cne $evidence.candidate.source_commit -or
    $health.scope -cne 'isolated_fixture_only' -or (Get-RecoverySha256 -Path $healthPath) -cne $evidence.health.receipt_fingerprint)
{
    throw 'scenario receipt does not match a healthy fixture server and staged payload provenance'
}


$output = [ordered]@{
    schema_version = 'engram.recovery.scenario-evidence-verification.v1'
    result = 'valid'
    release = $evidence.release
    scenario = $evidence.scenario
    scope = $evidence.scope
    source_commit = $evidence.candidate.source_commit
    receipt_fingerprint = Get-RecoverySha256 -Path $receiptPath
}
$text = $output | ConvertTo-Json -Depth 8
Assert-RecoverySecretSafeText -Text $text
Write-Output $text
