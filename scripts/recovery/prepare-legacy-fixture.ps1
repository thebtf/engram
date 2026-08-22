[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$FixtureRoot
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'recovery-fixture-common.ps1')

$context = Get-RecoveryFixtureContext -FixtureRoot $FixtureRoot
New-RecoveryFixtureRoot -Context $context

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

$restorePath = Join-Path $restored 'legacy-fixture.json'
Copy-Item -LiteralPath $exportPath -Destination $restorePath
[void](Assert-RecoveryContainedPath -Path $restorePath -RepositoryRoot $context.RepositoryRoot)
[void](Assert-RecoverySafeExistingPath -Path $restorePath)
$restoreFingerprint = Get-RecoverySha256 -Path $restorePath
if ($exportFingerprint -cne $restoreFingerprint)
{ throw 'synthetic fixture restore fingerprint mismatch' 
}

$backupReference = 'urn:engram:fixture-export:' + $exportFingerprint.Substring('sha256:'.Length)
$restoreReference = 'urn:engram:fixture-restore:' + $restoreFingerprint.Substring('sha256:'.Length)
$manifest = [ordered]@{
    schema_version = $script:RecoveryFixtureSchema
    fixture_id = $script:RecoveryFixtureID
    fixture_root = $context.RelativeRoot
    fixture_class = 'synthetic_redacted_legacy'
    created_at_utc = Get-RecoveryUtcNow
    export = [ordered]@{
        reference = $backupReference
        fingerprint = $exportFingerprint
        schema_version = $fixtureExport.schema_version
    }
    restore = [ordered]@{
        reference = $restoreReference
        fingerprint = $restoreFingerprint
        result = 'verified_equal_to_export'
    }
    selector_inventory = @(
        [ordered]@{ selector_fingerprint = $fixtureExport.records[0].selector_fingerprint; record_count = 1 },
        [ordered]@{ selector_fingerprint = $fixtureExport.records[1].selector_fingerprint; record_count = 1 }
    )
    structural_fingerprints = [ordered]@{
        projects = 'sha256:949c27952cd67e50f2cf6c4e2e841d20c295ee79f8d04c59bb11ea89b2152a32'
        legacy_payloads = 'sha256:98789de53ada2328e19000c886b4eb7f5319e87e83b1186448153e0a1b2de493'
    }
}
Write-RecoveryJson -Path (Join-Path $context.FixtureRoot 'fixture-manifest.json') -Value $manifest -Context $context
Assert-RecoveryFixtureTreeSafe -FixtureRoot $context.FixtureRoot

$output = [ordered]@{
    schema_version = 'engram.recovery.fixture-preparation-result.v1'
    result = 'prepared'
    fixture_id = $manifest.fixture_id
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
