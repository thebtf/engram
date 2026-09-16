[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$FixtureRoot,
    [string]$FixtureDatabaseDsn = 'postgres://fixture@127.0.0.1:55432/engram_fixture?sslmode=disable',
    [string]$FixturePsqlContainer = ''
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'recovery-fixture-common.ps1')
$null = Set-RecoveryFixturePsqlTransport -FixturePsqlContainer $FixturePsqlContainer
$context = Get-RecoveryFixtureContext -FixtureRoot $FixtureRoot
New-RecoveryFixtureRoot -Context $context -FixtureDatabaseDsn $FixtureDatabaseDsn
$owner = Assert-RecoveryFixtureOwner -Context $context
$databaseIdentity = Get-RecoveryFixtureDatabaseIdentity -FixtureDatabaseDsn $FixtureDatabaseDsn
$exports = Join-Path $context.FixtureRoot 'exports'
$restored = Join-Path $context.FixtureRoot 'restored'
$receipts = Join-Path $context.FixtureRoot 'receipts'
foreach ($directory in @($exports, $restored, $receipts))
{
    New-Item -ItemType Directory -Path $directory | Out-Null
    [void](Assert-RecoveryContainedPath -Path $directory -RepositoryRoot $context.RepositoryRoot)
    [void](Assert-RecoverySafeExistingPath -Path $directory)
}
# These values are intentionally synthetic fingerprints, never live selectors or payloads.
$fixtureExport = [ordered]@{
    schema_version = 'engram.recovery.synthetic-export.v1'
    fixture_class = 'synthetic_redacted_legacy'
    records = @(
        [ordered]@{ family = 'projects'; fixture_record = 'project-a'; selector_fingerprint = 'sha256:7d7de0a9cc82e77e0300d53d9a8f281d8407192f2bc5467810a1a50c272439f1'; provenance = 'synthetic' },
        [ordered]@{ family = 'projects'; fixture_record = 'project-b'; selector_fingerprint = 'sha256:2e5c67f03c96f5da605ec63026d0081845cb29e907af76b4a183eb3e9ce4ba5d'; provenance = 'synthetic' },
        [ordered]@{ family = 'legacy_payloads'; fixture_record = 'payload-a'; payload_fingerprint = 'sha256:52b3a9111a92de26493503aa19d028b1e742a0d682823ecc6ef838172ab6c6a9'; provenance = 'synthetic_redacted' }
    )
}
$exportPath = Join-Path $exports 'legacy-fixture.json'
Write-RecoveryJson -Path $exportPath -Value $fixtureExport -Context $context
$exportFingerprint = Get-RecoverySha256 -Path $exportPath
$preparedExport = Read-RecoveryJson -Path $exportPath -Context $context -Label 'synthetic fixture export'
$structuralFingerprints = Get-RecoveryFixtureStructuralFingerprints -FixtureExport $preparedExport
$restorePath = Join-Path $restored 'legacy-fixture.json'
Copy-Item -LiteralPath $exportPath -Destination $restorePath
[void](Assert-RecoveryContainedPath -Path $restorePath -RepositoryRoot $context.RepositoryRoot)
[void](Assert-RecoverySafeExistingPath -Path $restorePath)
$restoreFingerprint = Get-RecoverySha256 -Path $restorePath
if ($exportFingerprint -cne $restoreFingerprint)
{
    throw 'synthetic fixture restore fingerprint mismatch'
}
$backupReference = 'urn:engram:fixture-export:' + $exportFingerprint.Substring('sha256:'.Length)
$restoreReference = 'urn:engram:fixture-restore:' + $restoreFingerprint.Substring('sha256:'.Length)
$manifest = [ordered]@{
    schema_version = $script:RecoveryFixtureSchema
    fixture_id = $script:RecoveryFixtureID
    fixture_root = $context.RelativeRoot
    run_id = $owner.run_id
    fixture_class = 'synthetic_redacted_legacy'
    created_at_utc = Get-RecoveryUtcNow
    database = [ordered]@{
        schema_version = $script:RecoveryFixtureDatabaseSchema
        identity = [ordered]@{
            cluster_identifier = $databaseIdentity.ClusterIdentifier
            database = $databaseIdentity.Database
            database_oid = $databaseIdentity.DatabaseOid
        }
        identity_fingerprint = $databaseIdentity.Fingerprint
    }
    export = [ordered]@{
        reference = $backupReference
        fingerprint = $exportFingerprint
        schema_version = $preparedExport.schema_version
    }
    restore = [ordered]@{
        reference = $restoreReference
        fingerprint = $restoreFingerprint
        result = 'verified_equal_to_export'
    }
    selector_inventory = @(
        [ordered]@{ selector_fingerprint = $preparedExport.records[0].selector_fingerprint; record_count = 1 },
        [ordered]@{ selector_fingerprint = $preparedExport.records[1].selector_fingerprint; record_count = 1 }
    )
    structural_fingerprints = $structuralFingerprints
}
$manifestPath = Join-Path $context.FixtureRoot 'fixture-manifest.json'
Write-RecoveryJson -Path $manifestPath -Value $manifest -Context $context
$manifest = Read-RecoveryJson -Path $manifestPath -Context $context -Label 'fixture manifest'
$restoredExport = Read-RecoveryJson -Path $restorePath -Context $context -Label 'synthetic fixture restore'
Assert-RecoveryFixtureStructuralFingerprints -Manifest $manifest -FixtureExport $preparedExport -FixtureRestore $restoredExport
[void](Set-RecoveryFixtureDatabaseBinding -Context $context -Manifest $manifest -ManifestPath $manifestPath -FixtureDatabaseDsn $FixtureDatabaseDsn)
Assert-RecoveryFixtureTreeSafe -FixtureRoot $context.FixtureRoot
$output = [ordered]@{
    schema_version = 'engram.recovery.fixture-preparation-result.v1'
    result = 'prepared'
    fixture_id = $manifest.fixture_id
    run_id = $manifest.run_id
    database_identity_fingerprint = $manifest.database.identity_fingerprint
    fixture_class = $manifest.fixture_class
    fixture_schema_version = $manifest.schema_version
    export_reference = $manifest.export.reference
    restore_reference = $manifest.restore.reference
    selector_inventory = $manifest.selector_inventory
    structural_fingerprints = $manifest.structural_fingerprints
}
$text = $output | ConvertTo-Json -Depth 16
Assert-RecoverySecretSafeText -Text $text
Write-Output $text
