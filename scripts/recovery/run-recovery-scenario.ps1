[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidateSet('AR-1')][string]$Release,
    [Parameter(Mandatory)][ValidateSet('baseline')][string]$Scenario,
    [Parameter(Mandatory)][string]$FixtureRoot
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'recovery-fixture-common.ps1')

function Invoke-FixturePost
{
    param([Parameter(Mandatory)][string]$Uri, [Parameter(Mandatory)][string]$Body)

    $client = [Net.Http.HttpClient]::new()
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
    param([Parameter(Mandatory)][string]$Uri)

    $client = [Net.Http.HttpClient]::new()
    try
    {
        $client.Timeout = [TimeSpan]::FromSeconds(5)
        $response = $client.GetAsync($Uri).GetAwaiter().GetResult()
        try
        {
            $body = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
            Assert-RecoverySecretSafeText -Text $body
            $health = $body | ConvertFrom-Json -Depth 8
            if (-not $response.IsSuccessStatusCode -or $health.status -cne 'ready')
            { throw 'fixture health did not remain ready after behavior checks'
            }
            [pscustomobject]@{ status_code = [int]$response.StatusCode; status = [string]$health.status }
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
    { throw 'retired outcome callback violated its versioned 410 contract'
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
Assert-RecoveryExactProperties -Object $health -Names @('schema_version', 'fixture_id', 'checked_at_utc', 'endpoint', 'health_status', 'server_version', 'server_fingerprint', 'source_commit', 'scope') -Label 'fixture health receipt'
if ($health.schema_version -cne 'engram.recovery.fixture-health.v1' -or $health.fixture_id -cne $script:RecoveryFixtureID -or
    $health.endpoint -cnotmatch '^http://127\.0\.0\.1:[0-9]{4,5}/api/health$' -or $health.health_status -cne 'ready' -or
    $health.server_fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or $health.source_commit -cnotmatch '^[0-9a-f]{40}$' -or
    $health.scope -cne 'isolated_fixture_only')
{
    throw 'fixture health receipt is invalid'
}

$exportPath = Join-Path $context.FixtureRoot 'exports/legacy-fixture.json'
$restorePath = Join-Path $context.FixtureRoot 'restored/legacy-fixture.json'
if ((Get-RecoverySha256 -Path $exportPath) -cne $manifest.export.fingerprint -or (Get-RecoverySha256 -Path $restorePath) -cne $manifest.restore.fingerprint)
{
    throw 'fixture export and restore do not match their manifest fingerprints'
}

$sourceCommit = [string]$health.source_commit
$healthUri = [Uri]$health.endpoint
$fixtureBaseUri = $healthUri.GetLeftPart([UriPartial]::Authority)
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
{ throw 'unknown selector-only context request did not fail closed'
}
$postBehaviorHealth = Invoke-FixtureHealth -Uri $health.endpoint

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
    behavior = [ordered]@{
        retired_outcome_callbacks = $retiredCallbacks
        selector_only_context_inject = [ordered]@{
            path = '/api/context/inject'
            status_code = $selectorResponse.status_code
            error_code = $selectorError.error.code
            upgrade_action = $selectorError.error.upgrade_action
            canonical_project_returned = $false
        }
        health_after_behavior = $postBehaviorHealth
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
