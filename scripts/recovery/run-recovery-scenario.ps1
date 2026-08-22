[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidateSet('AR-1')][string]$Release,
    [Parameter(Mandatory)][ValidateSet('baseline')][string]$Scenario,
    [Parameter(Mandatory)][string]$FixtureRoot
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'recovery-fixture-common.ps1')

$context = Get-RecoveryFixtureContext -FixtureRoot $FixtureRoot
Assert-RecoveryFixtureTreeSafe -FixtureRoot $context.FixtureRoot
[void](Assert-RecoveryFixtureOwner -Context $context)
$manifestPath = Join-Path $context.FixtureRoot 'fixture-manifest.json'
$manifest = Read-RecoveryJson -Path $manifestPath -Context $context -Label 'fixture manifest'
if ($manifest.schema_version -cne $script:RecoveryFixtureSchema -or $manifest.fixture_id -cne $script:RecoveryFixtureID -or
    $manifest.fixture_root -cne $context.RelativeRoot -or $manifest.fixture_class -cne 'synthetic_redacted_legacy' -or
    $manifest.export.fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or $manifest.restore.fingerprint -cne $manifest.export.fingerprint -or
    $manifest.restore.result -cne 'verified_equal_to_export')
{
    throw 'fixture manifest does not prove a valid synthetic restore'
}

$healthPath = Join-Path $context.FixtureRoot 'server/fixture-server-health.json'
$health = Read-RecoveryJson -Path $healthPath -Context $context -Label 'fixture health receipt'
Assert-RecoveryExactProperties -Object $health -Names @('schema_version', 'fixture_id', 'checked_at_utc', 'endpoint', 'health_status', 'server_version', 'server_fingerprint', 'scope') -Label 'fixture health receipt'
if ($health.schema_version -cne 'engram.recovery.fixture-health.v1' -or $health.fixture_id -cne $script:RecoveryFixtureID -or
    $health.endpoint -cnotmatch '^http://127\.0\.0\.1:[0-9]{4,5}/api/health$' -or $health.health_status -cne 'ready' -or
    $health.server_fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or $health.scope -cne 'isolated_fixture_only')
{
    throw 'fixture health receipt is invalid'
}

$exportPath = Join-Path $context.FixtureRoot 'exports/legacy-fixture.json'
$restorePath = Join-Path $context.FixtureRoot 'restored/legacy-fixture.json'
if ((Get-RecoverySha256 -Path $exportPath) -cne $manifest.export.fingerprint -or (Get-RecoverySha256 -Path $restorePath) -cne $manifest.restore.fingerprint)
{
    throw 'fixture export and restore do not match their manifest fingerprints'
}

$sourceCommit = (& git -C $context.RepositoryRoot rev-parse HEAD).Trim()
if ($LASTEXITCODE -ne 0 -or $sourceCommit -cnotmatch '^[0-9a-f]{40}$')
{ throw 'current source commit cannot be determined' 
}
$receiptsDirectory = Join-Path $context.FixtureRoot 'receipts'
[void](Assert-RecoveryContainedPath -Path $receiptsDirectory -RepositoryRoot $context.RepositoryRoot)
[void](Assert-RecoverySafeExistingPath -Path $receiptsDirectory)

$evidence = [ordered]@{
    schema_version = 'engram.recovery.scenario-evidence.v1'
    evidence_kind = 'fixture_scenario'
    release = $Release
    scenario = $Scenario
    observed_at_utc = Get-RecoveryUtcNow
    scope = 'synthetic_fixture_only'
    fixture = [ordered]@{
        fixture_id = $manifest.fixture_id
        fixture_root = $context.RelativeRoot
        manifest_fingerprint = Get-RecoverySha256 -Path $manifestPath
        export_reference = $manifest.export.reference
        export_fingerprint = $manifest.export.fingerprint
        restore_reference = $manifest.restore.reference
        selector_inventory_count = @($manifest.selector_inventory).Count
    }
    health = [ordered]@{
        receipt_fingerprint = Get-RecoverySha256 -Path $healthPath
        server_fingerprint = $health.server_fingerprint
        status = $health.health_status
    }
    candidate = [ordered]@{
        source_commit = $sourceCommit
        built_payload_fingerprint = $health.server_fingerprint
    }
    observations = [ordered]@{
        fixture_containment = 'validated'
        synthetic_restore = 'validated'
        fixture_server_health = 'ready'
        live_data_observed = $false
        installed_release_authority = 'not_claimed'
        ar1_baseline_receipt_authority = 'not_claimed'
    }
}

$receiptPath = Join-Path $receiptsDirectory 'ar-1-baseline.json'
Write-RecoveryJson -Path $receiptPath -Value $evidence -Context $context
$latestPath = Join-Path $receiptsDirectory 'latest.json'
Copy-Item -LiteralPath $receiptPath -Destination $latestPath -Force
[void](Assert-RecoverySafeExistingPath -Path $latestPath)

# The runner validates the just-written envelope but never promotes it to the AR-1 baseline receipt.
$null = & (Join-Path $PSScriptRoot 'verify-recovery-receipt.ps1') -Receipt $receiptPath
Assert-RecoveryFixtureTreeSafe -FixtureRoot $context.FixtureRoot
$output = $evidence | ConvertTo-Json -Depth 16
Assert-RecoverySecretSafeText -Text $output
Write-Output $output
