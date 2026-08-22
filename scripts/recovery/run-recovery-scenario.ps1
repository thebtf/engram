[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidateSet('AR-1')][string]$Release,
    [Parameter(Mandatory)][ValidateSet('baseline')][string]$Scenario,
    [Parameter(Mandatory)][string]$FixtureRoot,
    [string]$FixtureDatabaseDsn = 'postgres://fixture@127.0.0.1:55432/engram_fixture?sslmode=disable',
    [string]$FixturePsqlContainer = ''
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'recovery-fixture-common.ps1')
$null = Set-RecoveryFixturePsqlTransport -FixturePsqlContainer $FixturePsqlContainer

function Invoke-FixturePost
{
    param([Parameter(Mandatory)][string]$Uri, [Parameter(Mandatory)][string]$Body)

    $client = New-RecoveryFixtureLoopbackHttpClient
    $content = [Net.Http.StringContent]::new($Body, [Text.Encoding]::UTF8, 'application/json')
    try
    {
        $client.Timeout = [TimeSpan]::FromSeconds(5)
        $response = $client.PostAsync($Uri, $content).GetAwaiter().GetResult()
        try
        {
            $responseBody = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
            Assert-RecoverySecretSafeText -Text $responseBody
            [pscustomobject]@{
                status_code = [int]$response.StatusCode
                content_type = [string]$response.Content.Headers.ContentType.MediaType
                body = $responseBody
            }
        } finally
        {
            $response.Dispose()
        }
    } finally
    {
        $content.Dispose()
        $client.Dispose()
    }
}

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
                throw 'fixture health does not identify the current owned source candidate'
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

function Assert-FixtureOutcomeRetirement
{
    param([Parameter(Mandatory)][string]$BaseUri, [Parameter(Mandatory)][string]$Path)

    $response = Invoke-FixturePost -Uri "$BaseUri$Path" -Body '{}'
    $contract = $response.body | ConvertFrom-Json -Depth 8
    Assert-RecoveryExactProperties -Object $contract -Names @('contract_version', 'code', 'action') -Label 'retired outcome callback response'
    if ($response.status_code -ne 410 -or $response.content_type -cne 'application/json' -or
        $contract.contract_version -cne 'engram.outcome-retirement.v1' -or $contract.code -cne 'OUTCOME_CALLBACK_RETIRED' -or
        $contract.action -cne 'upgrade_outcome_adapter')
    {
        throw 'retired outcome callback violated its versioned 410 contract'
    }
    [ordered]@{
        path = $Path
        status_code = $response.status_code
        content_type = $response.content_type
        contract_version = $contract.contract_version
        code = $contract.code
        action = $contract.action
    }
}

$evidence = $null
$receiptPath = $null
$context = Get-RecoveryFixtureContext -FixtureRoot $FixtureRoot
$fixtureLock = Enter-RecoveryFixtureMutationLock -Context $context
try
{
Assert-RecoveryFixtureTreeSafe -FixtureRoot $context.FixtureRoot
$owner = Assert-RecoveryFixtureOwner -Context $context
$manifestPath = Join-Path $context.FixtureRoot 'fixture-manifest.json'
$manifest = Read-RecoveryJson -Path $manifestPath -Context $context -Label 'fixture manifest'
Assert-RecoveryExactProperties -Object $manifest -Names @('schema_version', 'fixture_id', 'fixture_root', 'run_id', 'fixture_class', 'created_at_utc', 'database', 'export', 'restore', 'selector_inventory', 'structural_fingerprints') -Label 'fixture manifest'
if ($manifest.schema_version -cne $script:RecoveryFixtureSchema -or $manifest.fixture_id -cne $script:RecoveryFixtureID -or
    $manifest.fixture_root -cne $context.RelativeRoot -or $manifest.run_id -cne $owner.run_id -or $manifest.fixture_class -cne 'synthetic_redacted_legacy' -or
    $manifest.export.fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or $manifest.restore.fingerprint -cne $manifest.export.fingerprint -or
    $manifest.restore.result -cne 'verified_equal_to_export')
{
    throw 'fixture manifest does not prove a valid synthetic restore'
}

$exportPath = Join-Path $context.FixtureRoot 'exports/legacy-fixture.json'
$restorePath = Join-Path $context.FixtureRoot 'restored/legacy-fixture.json'
$export = Read-RecoveryJson -Path $exportPath -Context $context -Label 'synthetic fixture export'
$restore = Read-RecoveryJson -Path $restorePath -Context $context -Label 'synthetic fixture restore'
if ((Get-RecoverySha256 -Path $exportPath) -cne $manifest.export.fingerprint -or (Get-RecoverySha256 -Path $restorePath) -cne $manifest.restore.fingerprint)
{
    throw 'fixture export and restore do not match their manifest fingerprints'
}
Assert-RecoveryFixtureStructuralFingerprints -Manifest $manifest -FixtureExport $export -FixtureRestore $restore

$serverMarkerPath = Join-Path $context.FixtureRoot 'server/fixture-server.json'
$serverMarker = Read-RecoveryJson -Path $serverMarkerPath -Context $context -Label 'fixture server marker'
[void](Assert-RecoveryFixtureServerMarker -Context $context -Marker $serverMarker)
$healthPath = Join-Path $context.FixtureRoot 'server/fixture-server-health.json'
$health = Read-RecoveryJson -Path $healthPath -Context $context -Label 'fixture health receipt'
Assert-RecoveryExactProperties -Object $health -Names @('schema_version', 'fixture_id', 'fixture_root', 'run_id', 'manifest_fingerprint', 'database_identity_fingerprint', 'checked_at_utc', 'endpoint', 'health_status', 'server_version', 'server_fingerprint', 'source_commit', 'process_id', 'process_start_utc_ticks', 'port', 'scope') -Label 'fixture health receipt'
if ($health.schema_version -cne 'engram.recovery.fixture-health.v2' -or $health.fixture_id -cne $script:RecoveryFixtureID -or
    $health.fixture_root -cne $context.RelativeRoot -or $health.run_id -cne $serverMarker.run_id -or
    $health.manifest_fingerprint -cne $serverMarker.manifest_fingerprint -or $health.database_identity_fingerprint -cne $serverMarker.database_identity_fingerprint -or
    $health.endpoint -cne "http://127.0.0.1:$($serverMarker.port)/api/health" -or $health.health_status -cne 'ready' -or
    $health.server_fingerprint -cne $serverMarker.server_fingerprint -or $health.source_commit -cne $serverMarker.source_commit -or
    $health.process_id -ne $serverMarker.process_id -or $health.process_start_utc_ticks -ne $serverMarker.process_start_utc_ticks -or
    $health.port -ne $serverMarker.port -or $health.scope -cne 'isolated_fixture_only')
{
    throw 'fixture health receipt is not bound to the current owned fixture server'
}

$sourceCommit = [string]$serverMarker.source_commit
$fixtureBaseUri = "http://127.0.0.1:$($serverMarker.port)"
$liveBeforeBehavior = Invoke-FixtureHealth -Uri $health.endpoint -ExpectedSourceCommit $sourceCommit
$liveServer = Assert-RecoveryFixtureLiveServer -Context $context -Manifest $manifest -ManifestPath $manifestPath -FixtureDatabaseDsn $FixtureDatabaseDsn -ServerMarker $serverMarker -Health $liveBeforeBehavior
$binding = $liveServer.Binding

$retiredCallbacks = @(
    Assert-FixtureOutcomeRetirement -BaseUri $fixtureBaseUri -Path '/api/sessions/claude-session/propagate-outcome'
    Assert-FixtureOutcomeRetirement -BaseUri $fixtureBaseUri -Path '/api/sessions/openclaw-session/outcome'
)
$selectorResponse = Invoke-FixturePost -Uri "$fixtureBaseUri/api/context/inject" -Body '{"project":"ar1-fence-http-unknown","identity_only":true}'
$selectorError = $selectorResponse.body | ConvertFrom-Json -Depth 8
Assert-RecoveryExactProperties -Object $selectorError -Names @('error') -Label 'selector-only context response'
Assert-RecoveryExactProperties -Object $selectorError.error -Names @('code', 'message', 'upgrade_action') -Label 'selector-only context error'
if ($selectorResponse.status_code -ne 409 -or $selectorError.error.code -cne 'PROJECT_IDENTITY_AMBIGUOUS' -or
    $selectorError.error.message -cne 'project identity selector is ambiguous' -or $selectorError.error.upgrade_action -cne 'send_project_identity_v2')
{
    throw 'unknown selector-only context request did not fail closed'
}
$postBehaviorHealth = Invoke-FixtureHealth -Uri $health.endpoint -ExpectedSourceCommit $sourceCommit
[void](Assert-RecoveryFixtureLiveServer -Context $context -Manifest $manifest -ManifestPath $manifestPath -FixtureDatabaseDsn $FixtureDatabaseDsn -ServerMarker $serverMarker -Health $postBehaviorHealth)

$receiptsDirectory = Join-Path $context.FixtureRoot 'receipts'
[void](Assert-RecoveryContainedPath -Path $receiptsDirectory -RepositoryRoot $context.RepositoryRoot)
[void](Assert-RecoverySafeExistingPath -Path $receiptsDirectory)

$evidence = [ordered]@{
    schema_version = 'engram.recovery.scenario-evidence.v2'
    evidence_kind = 'fixture_scenario'
    release = $Release
    scenario = $Scenario
    observed_at_utc = Get-RecoveryUtcNow
    scope = 'synthetic_fixture_only'
    fixture = [ordered]@{
        fixture_id = $manifest.fixture_id
        fixture_root = $context.RelativeRoot
        run_id = $serverMarker.run_id
        manifest_fingerprint = Get-RecoverySha256 -Path $manifestPath
        database_identity_fingerprint = $manifest.database.identity_fingerprint
        export_reference = $manifest.export.reference
        export_fingerprint = $manifest.export.fingerprint
        restore_reference = $manifest.restore.reference
        selector_inventory_count = @($manifest.selector_inventory).Count
        structural_fingerprints = $manifest.structural_fingerprints
        server_marker_fingerprint = Get-RecoverySha256 -Path $serverMarkerPath
    }
    health = [ordered]@{
        receipt_fingerprint = Get-RecoverySha256 -Path $healthPath
        server_fingerprint = $serverMarker.server_fingerprint
        source_commit = $sourceCommit
        status = $postBehaviorHealth.status
        run_id = $serverMarker.run_id
        process_id = $serverMarker.process_id
        process_start_utc_ticks = $serverMarker.process_start_utc_ticks
        port = $serverMarker.port
    }
    candidate = [ordered]@{
        source_commit = $sourceCommit
        built_payload_fingerprint = $serverMarker.server_fingerprint
        staged_payload_fingerprint = Get-RecoverySha256 -Path $liveServer.PayloadPath
    }
    behavior = [ordered]@{
        retired_outcome_callbacks = $retiredCallbacks
        selector_only_context_inject = [ordered]@{
            path = '/api/context/inject'
            status_code = $selectorResponse.status_code
            error_code = $selectorError.error.code
            upgrade_action = $selectorError.error.upgrade_action
            canonical_project_returned = $false
        }
        health_after_behavior = [ordered]@{
            status_code = $postBehaviorHealth.status_code
            status = $postBehaviorHealth.status
            source_commit = $postBehaviorHealth.source_commit
        }
    }
    observations = [ordered]@{
        fixture_containment = 'validated'
        synthetic_restore = 'validated'
        fixture_database_binding = 'validated'
        owned_live_process = 'validated'
        staged_payload_provenance = 'validated'
        runtime_health_provenance = 'validated'
        fixture_server_health = 'ready'
        live_data_observed = $false
        installed_release_authority = 'not_claimed'
        ar1_baseline_receipt_authority = 'not_claimed'
    }
}

$receiptPath = Join-Path $receiptsDirectory 'ar-1-baseline.json'
Write-RecoveryJson -Path $receiptPath -Value $evidence -Context $context
$latestPath = Assert-RecoveryWritableLeaf -Path (Join-Path $receiptsDirectory 'latest.json') -Context $context
$receiptPath = Assert-RecoveryWritableLeaf -Path $receiptPath -Context $context -RequireExists
Copy-Item -LiteralPath $receiptPath -Destination $latestPath -Force
[void](Assert-RecoveryWritableLeaf -Path $latestPath -Context $context -RequireExists)

Assert-RecoveryFixtureTreeSafe -FixtureRoot $context.FixtureRoot
} finally
{
    $fixtureLock.Dispose()
}

# The standalone verifier reacquires the fixture lock; a changed fixture must fail verification.
$null = & (Join-Path $PSScriptRoot 'verify-recovery-receipt.ps1') -Receipt $receiptPath -FixtureDatabaseDsn $FixtureDatabaseDsn -FixturePsqlContainer $FixturePsqlContainer
$output = $evidence | ConvertTo-Json -Depth 20
Assert-RecoverySecretSafeText -Text $output
Write-Output $output
