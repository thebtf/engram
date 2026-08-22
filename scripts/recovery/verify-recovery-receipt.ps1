[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Receipt,
    [string]$FixtureDatabaseDsn = 'postgres://fixture@127.0.0.1:55432/engram_fixture?sslmode=disable',
    [string]$FixturePsqlContainer = ''
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'recovery-fixture-common.ps1')
$null = Set-RecoveryFixturePsqlTransport -FixturePsqlContainer $FixturePsqlContainer

function Invoke-FixtureHealth
{
    param([Parameter(Mandatory)][string]$Uri, [Parameter(Mandatory)][string]$ExpectedSourceCommit)

    $client = New-RecoveryFixtureLoopbackHttpClient
    try
    {
        $client.Timeout = [TimeSpan]::FromSeconds(5)
        $response = $client.GetAsync($Uri).GetAwaiter().GetResult()
        try
        {
            $body = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
            Assert-RecoverySecretSafeText -Text $body
            $health = $body | ConvertFrom-Json -Depth 8
            if (-not $response.IsSuccessStatusCode -or $health.status -isnot [string] -or $health.version -isnot [string] -or $health.source_commit -isnot [string] -or
                $health.status -cne 'ready' -or [string]::IsNullOrWhiteSpace($health.version) -or $health.source_commit -cne $ExpectedSourceCommit)
            {
                throw 'live fixture health does not match the candidate source commit'
            }
            [pscustomobject]@{
                status_code = [int]$response.StatusCode
                status = [string]$health.status
                version = [string]$health.version
                source_commit = [string]$health.source_commit
            }
        } finally
        {
            $response.Dispose()
        }
    } finally
    {
        $client.Dispose()
    }
}

if ([string]::IsNullOrWhiteSpace($Receipt) -or ($Receipt -split '[\\/]') -contains '..')
{
    throw 'receipt traversal is forbidden'
}
$repositoryRoot = Get-RecoveryRepositoryRoot
$receiptCandidate = if ([IO.Path]::IsPathRooted($Receipt))
{
    $Receipt
} else
{
    Join-Path $repositoryRoot $Receipt
}
$receiptPath = Assert-RecoveryContainedPath -Path $receiptCandidate -RepositoryRoot $repositoryRoot
$fixtureRoot = Split-Path -Parent (Split-Path -Parent $receiptPath)
$context = Get-RecoveryFixtureContext -FixtureRoot $fixtureRoot
$comparison = if ($IsWindows)
{ [StringComparison]::OrdinalIgnoreCase
} else
{ [StringComparison]::Ordinal
}
if (-not [string]::Equals($context.RepositoryRoot, $repositoryRoot, $comparison))
{
    throw 'receipt repository scope is invalid'
}
$expectedReceiptPrefix = $context.FixtureRoot.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar + 'receipts' + [IO.Path]::DirectorySeparatorChar
if (-not $receiptPath.StartsWith($expectedReceiptPrefix, $comparison))
{
    throw 'scenario receipt is outside the owned receipt directory'
}
$fixtureLock = Enter-RecoveryFixtureMutationLock -Context $context
try
{
if (-not (Test-Path -LiteralPath $receiptPath -PathType Leaf))
{
    throw 'scenario receipt is missing'
}
[void](Assert-RecoverySafeExistingPath -Path $receiptPath)
$receiptText = [IO.File]::ReadAllText($receiptPath)
Assert-RecoverySecretSafeText -Text $receiptText
try
{
    $evidence = $receiptText | ConvertFrom-Json -Depth 32
} catch
{
    throw 'scenario receipt is malformed'
}

Assert-RecoveryExactProperties -Object $evidence -Names @('schema_version', 'evidence_kind', 'release', 'scenario', 'observed_at_utc', 'scope', 'fixture', 'health', 'candidate', 'behavior', 'observations') -Label 'scenario receipt'
$parsedObservedAtUtc = [DateTimeOffset]::MinValue
if ($evidence.observed_at_utc -isnot [string] -or [string]::IsNullOrWhiteSpace($evidence.observed_at_utc) -or
    $evidence.observed_at_utc -notmatch '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,9})?(?:Z|[+-][0-9]{2}:[0-9]{2})$' -or
    -not [DateTimeOffset]::TryParseExact(($evidence.observed_at_utc -replace '(?<=\.[0-9]{7})[0-9]+(?=(?:Z|[+-][0-9]{2}:[0-9]{2})$)', ''), @("yyyy-MM-dd'T'HH:mm:ssK", "yyyy-MM-dd'T'HH:mm:ss.FFFFFFFK"), [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::None, [ref]$parsedObservedAtUtc))
{
    throw 'scenario receipt observed_at_utc is invalid'
}

Assert-RecoveryExactProperties -Object $evidence.fixture -Names @('fixture_id', 'fixture_root', 'run_id', 'manifest_fingerprint', 'database_identity_fingerprint', 'export_reference', 'export_fingerprint', 'restore_reference', 'selector_inventory_count', 'structural_fingerprints', 'server_marker_fingerprint') -Label 'scenario fixture evidence'
Assert-RecoveryExactProperties -Object $evidence.fixture.structural_fingerprints -Names @('projects', 'legacy_payloads') -Label 'scenario fixture structural fingerprints'
Assert-RecoveryExactProperties -Object $evidence.health -Names @('receipt_fingerprint', 'server_fingerprint', 'source_commit', 'status', 'run_id', 'process_id', 'process_start_utc_ticks', 'port') -Label 'scenario health evidence'
Assert-RecoveryExactProperties -Object $evidence.candidate -Names @('source_commit', 'built_payload_fingerprint', 'staged_payload_fingerprint') -Label 'scenario candidate evidence'
Assert-RecoveryExactProperties -Object $evidence.behavior -Names @('retired_outcome_callbacks', 'selector_only_context_inject', 'health_after_behavior') -Label 'scenario behavior evidence'
Assert-RecoveryExactProperties -Object $evidence.observations -Names @('fixture_containment', 'synthetic_restore', 'fixture_database_binding', 'owned_live_process', 'staged_payload_provenance', 'runtime_health_provenance', 'fixture_server_health', 'live_data_observed', 'installed_release_authority', 'ar1_baseline_receipt_authority') -Label 'scenario observations'

if ($evidence.schema_version -cne 'engram.recovery.scenario-evidence.v2' -or $evidence.evidence_kind -cne 'fixture_scenario' -or
    $evidence.release -cne 'AR-1' -or $evidence.scenario -cne 'baseline' -or $evidence.scope -cne 'synthetic_fixture_only' -or
    $evidence.fixture.fixture_id -cne $script:RecoveryFixtureID -or [string]::IsNullOrWhiteSpace([string]$evidence.fixture.fixture_root) -or
    $evidence.fixture.run_id -cnotmatch '^[0-9a-f]{32}$' -or $evidence.fixture.manifest_fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or
    $evidence.fixture.database_identity_fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or $evidence.fixture.export_fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or
    $evidence.fixture.server_marker_fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or $evidence.health.receipt_fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or
    $evidence.health.server_fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or $evidence.health.source_commit -cnotmatch '^[0-9a-f]{40}$' -or
    $evidence.health.status -cne 'ready' -or $evidence.health.run_id -cne $evidence.fixture.run_id -or
    (($evidence.health.process_id -isnot [int]) -and ($evidence.health.process_id -isnot [long])) -or
    (($evidence.health.process_start_utc_ticks -isnot [int]) -and ($evidence.health.process_start_utc_ticks -isnot [long])) -or
    (($evidence.health.port -isnot [int]) -and ($evidence.health.port -isnot [long])) -or $evidence.health.process_id -lt 1 -or
    $evidence.health.process_start_utc_ticks -lt 1 -or $evidence.health.port -lt 1024 -or $evidence.health.port -gt 65535 -or
    $evidence.candidate.source_commit -cne $evidence.health.source_commit -or $evidence.candidate.built_payload_fingerprint -cne $evidence.health.server_fingerprint -or
    $evidence.candidate.staged_payload_fingerprint -cne $evidence.health.server_fingerprint -or
    $evidence.observations.fixture_containment -cne 'validated' -or $evidence.observations.synthetic_restore -cne 'validated' -or
    $evidence.observations.fixture_database_binding -cne 'validated' -or $evidence.observations.owned_live_process -cne 'validated' -or
    $evidence.observations.staged_payload_provenance -cne 'validated' -or $evidence.observations.runtime_health_provenance -cne 'validated' -or
    $evidence.observations.fixture_server_health -cne 'ready' -or $evidence.observations.live_data_observed -ne $false -or
    $evidence.observations.installed_release_authority -cne 'not_claimed' -or $evidence.observations.ar1_baseline_receipt_authority -cne 'not_claimed')
{
    throw 'scenario receipt violates the fixture-only evidence contract'
}

$expectedCallbackPaths = @('/api/sessions/claude-session/propagate-outcome', '/api/sessions/openclaw-session/outcome')
$callbacks = @($evidence.behavior.retired_outcome_callbacks)
if ($callbacks.Count -ne $expectedCallbackPaths.Count)
{
    throw 'scenario receipt lacks retired outcome callback evidence'
}
for ($index = 0; $index -lt $expectedCallbackPaths.Count; $index++)
{
    $callback = $callbacks[$index]
    Assert-RecoveryExactProperties -Object $callback -Names @('path', 'status_code', 'content_type', 'contract_version', 'code', 'action') -Label 'retired outcome callback evidence'
    if ($callback.path -cne $expectedCallbackPaths[$index] -or $callback.status_code -ne 410 -or $callback.content_type -cne 'application/json' -or
        $callback.contract_version -cne 'engram.outcome-retirement.v1' -or $callback.code -cne 'OUTCOME_CALLBACK_RETIRED' -or $callback.action -cne 'upgrade_outcome_adapter')
    {
        throw 'scenario receipt records a false retired outcome callback success'
    }
}

$selector = $evidence.behavior.selector_only_context_inject
Assert-RecoveryExactProperties -Object $selector -Names @('path', 'status_code', 'error_code', 'upgrade_action', 'canonical_project_returned') -Label 'selector-only context evidence'
if ($selector.path -cne '/api/context/inject' -or $selector.status_code -ne 409 -or
    $selector.error_code -cne 'PROJECT_IDENTITY_AMBIGUOUS' -or $selector.upgrade_action -cne 'send_project_identity_v2' -or $selector.canonical_project_returned -ne $false)
{
    throw 'scenario receipt records a false selector-only context success'
}

Assert-RecoveryExactProperties -Object $evidence.behavior.health_after_behavior -Names @('status_code', 'status', 'source_commit') -Label 'post-behavior health evidence'
if ($evidence.behavior.health_after_behavior.status_code -ne 200 -or $evidence.behavior.health_after_behavior.status -cne 'ready' -or
    $evidence.behavior.health_after_behavior.source_commit -cne $evidence.candidate.source_commit)
{
    throw 'scenario receipt does not prove provenance-bearing health after behavior checks'
}

if ($context.RelativeRoot -cne [string]$evidence.fixture.fixture_root)
{
    throw 'scenario receipt fixture root is invalid'
}
$comparison = if ($IsWindows)
{ [StringComparison]::OrdinalIgnoreCase
} else
{ [StringComparison]::Ordinal
}
if (-not [string]::Equals($context.RepositoryRoot, $repositoryRoot, $comparison))
{
    throw 'receipt repository scope is invalid'
}
$expectedReceiptPrefix = $context.FixtureRoot.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar + 'receipts' + [IO.Path]::DirectorySeparatorChar
if (-not $receiptPath.StartsWith($expectedReceiptPrefix, $comparison))
{
    throw 'scenario receipt is outside the owned receipt directory'
}
Assert-RecoveryFixtureTreeSafe -FixtureRoot $context.FixtureRoot
$owner = Assert-RecoveryFixtureOwner -Context $context
if ($owner.run_id -cne $evidence.fixture.run_id)
{
    throw 'scenario receipt run does not match the current fixture owner marker'
}

$manifestPath = Join-Path $context.FixtureRoot 'fixture-manifest.json'
$manifest = Read-RecoveryJson -Path $manifestPath -Context $context -Label 'fixture manifest'
Assert-RecoveryExactProperties -Object $manifest -Names @('schema_version', 'fixture_id', 'fixture_root', 'run_id', 'fixture_class', 'created_at_utc', 'database', 'export', 'restore', 'selector_inventory', 'structural_fingerprints') -Label 'fixture manifest'
Assert-RecoveryExactProperties -Object $manifest.export -Names @('reference', 'fingerprint', 'schema_version') -Label 'fixture export metadata'
Assert-RecoveryExactProperties -Object $manifest.restore -Names @('reference', 'fingerprint', 'result') -Label 'fixture restore metadata'
if ($manifest.schema_version -cne $script:RecoveryFixtureSchema -or $manifest.fixture_id -cne $script:RecoveryFixtureID -or
    $manifest.fixture_root -cne $context.RelativeRoot -or $manifest.run_id -cne $owner.run_id -or $manifest.fixture_class -cne 'synthetic_redacted_legacy' -or
    $manifest.export.schema_version -cne 'engram.recovery.synthetic-export.v1' -or $manifest.restore.result -cne 'verified_equal_to_export' -or $manifest.restore.fingerprint -cne $manifest.export.fingerprint -or
    (Get-RecoverySha256 -Path $manifestPath) -cne $evidence.fixture.manifest_fingerprint -or
    $manifest.database.identity_fingerprint -cne $evidence.fixture.database_identity_fingerprint -or
    $manifest.export.reference -cne $evidence.fixture.export_reference -or $manifest.export.fingerprint -cne $evidence.fixture.export_fingerprint -or
    $manifest.restore.reference -cne $evidence.fixture.restore_reference -or @($manifest.selector_inventory).Count -ne [int]$evidence.fixture.selector_inventory_count)
{
    throw 'scenario receipt does not match the owned fixture manifest'
}

$exportPath = Join-Path $context.FixtureRoot 'exports/legacy-fixture.json'
$restorePath = Join-Path $context.FixtureRoot 'restored/legacy-fixture.json'
$export = Read-RecoveryJson -Path $exportPath -Context $context -Label 'synthetic fixture export'
$restore = Read-RecoveryJson -Path $restorePath -Context $context -Label 'synthetic fixture restore'
if ((Get-RecoverySha256 -Path $exportPath) -cne $manifest.export.fingerprint -or (Get-RecoverySha256 -Path $restorePath) -cne $manifest.restore.fingerprint)
{
    throw 'fixture export or restore fingerprint mismatch'
}
Assert-RecoveryFixtureStructuralFingerprints -Manifest $manifest -FixtureExport $export -FixtureRestore $restore
foreach ($family in @('projects', 'legacy_payloads'))
{
    if ($evidence.fixture.structural_fingerprints.$family -isnot [string] -or $evidence.fixture.structural_fingerprints.$family -cne $manifest.structural_fingerprints.$family)
    {
        throw 'scenario receipt structural fingerprints do not match the fixture manifest'
    }
}

$serverMarkerPath = Join-Path $context.FixtureRoot 'server/fixture-server.json'
$serverMarker = Read-RecoveryJson -Path $serverMarkerPath -Context $context -Label 'fixture server marker'
[void](Assert-RecoveryFixtureServerMarker -Context $context -Marker $serverMarker)
if ((Get-RecoverySha256 -Path $serverMarkerPath) -cne $evidence.fixture.server_marker_fingerprint -or
    $serverMarker.run_id -cne $evidence.fixture.run_id -or $serverMarker.manifest_fingerprint -cne $evidence.fixture.manifest_fingerprint -or
    $serverMarker.database_identity_fingerprint -cne $evidence.fixture.database_identity_fingerprint -or $serverMarker.source_commit -cne $evidence.candidate.source_commit -or
    $serverMarker.server_fingerprint -cne $evidence.candidate.staged_payload_fingerprint -or $serverMarker.process_id -ne $evidence.health.process_id -or
    $serverMarker.process_start_utc_ticks -ne $evidence.health.process_start_utc_ticks -or $serverMarker.port -ne $evidence.health.port)
{
    throw 'scenario receipt does not match the current owned fixture server marker'
}

$healthPath = Join-Path $context.FixtureRoot 'server/fixture-server-health.json'
$health = Read-RecoveryJson -Path $healthPath -Context $context -Label 'fixture health receipt'
Assert-RecoveryExactProperties -Object $health -Names @('schema_version', 'fixture_id', 'fixture_root', 'run_id', 'manifest_fingerprint', 'database_identity_fingerprint', 'checked_at_utc', 'endpoint', 'health_status', 'server_version', 'server_fingerprint', 'source_commit', 'process_id', 'process_start_utc_ticks', 'port', 'scope') -Label 'fixture health receipt'
$parsedCheckedAtUtc = [DateTimeOffset]::MinValue
if ($health.checked_at_utc -isnot [string] -or [string]::IsNullOrWhiteSpace($health.checked_at_utc) -or
    $health.checked_at_utc -notmatch '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,9})?(?:Z|[+-][0-9]{2}:[0-9]{2})$' -or
    -not [DateTimeOffset]::TryParseExact(($health.checked_at_utc -replace '(?<=\.[0-9]{7})[0-9]+(?=(?:Z|[+-][0-9]{2}:[0-9]{2})$)', ''), @("yyyy-MM-dd'T'HH:mm:ssK", "yyyy-MM-dd'T'HH:mm:ss.FFFFFFFK"), [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::None, [ref]$parsedCheckedAtUtc))
{
    throw 'fixture health receipt checked_at_utc is invalid'
}
if ($health.schema_version -cne 'engram.recovery.fixture-health.v2' -or $health.fixture_id -cne $script:RecoveryFixtureID -or
    $health.fixture_root -cne $context.RelativeRoot -or $health.run_id -cne $serverMarker.run_id -or
    $health.manifest_fingerprint -cne $serverMarker.manifest_fingerprint -or $health.database_identity_fingerprint -cne $serverMarker.database_identity_fingerprint -or
    $health.endpoint -cne "http://127.0.0.1:$($serverMarker.port)/api/health" -or $health.health_status -cne 'ready' -or
    [string]::IsNullOrWhiteSpace([string]$health.server_version) -or $health.server_fingerprint -cne $serverMarker.server_fingerprint -or
    $health.source_commit -cne $serverMarker.source_commit -or $health.process_id -ne $serverMarker.process_id -or
    $health.process_start_utc_ticks -ne $serverMarker.process_start_utc_ticks -or $health.port -ne $serverMarker.port -or
    $health.scope -cne 'isolated_fixture_only' -or (Get-RecoverySha256 -Path $healthPath) -cne $evidence.health.receipt_fingerprint)
{
    throw 'scenario receipt does not match the persisted health provenance'
}

$liveHealth = Invoke-FixtureHealth -Uri $health.endpoint -ExpectedSourceCommit $serverMarker.source_commit
[void](Assert-RecoveryFixtureLiveServer -Context $context -Manifest $manifest -ManifestPath $manifestPath -FixtureDatabaseDsn $FixtureDatabaseDsn -ServerMarker $serverMarker -Health $liveHealth)

$output = [ordered]@{
    schema_version = 'engram.recovery.scenario-evidence-verification.v2'
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
} finally
{
    $fixtureLock.Dispose()
}
