[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$FixtureRoot,
    [string]$ServerPath,
    [ValidateRange(1024, 65535)][int]$Port = 38877,
    [string]$FixtureDatabaseDsn = 'postgres://fixture@127.0.0.1:55432/engram_fixture?sslmode=disable'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'recovery-fixture-common.ps1')

function Assert-FixtureManifest
{
    param([Parameter(Mandatory)]$Manifest, [Parameter(Mandatory)]$Context)

    Assert-RecoveryExactProperties -Object $Manifest -Names @('schema_version', 'fixture_id', 'fixture_root', 'fixture_class', 'created_at_utc', 'export', 'restore', 'selector_inventory', 'structural_fingerprints') -Label 'fixture manifest'
    if ($Manifest.schema_version -cne $script:RecoveryFixtureSchema -or $Manifest.fixture_id -cne $script:RecoveryFixtureID -or
        $Manifest.fixture_root -cne $Context.RelativeRoot -or $Manifest.fixture_class -cne 'synthetic_redacted_legacy')
    {
        throw 'fixture manifest is foreign'
    }
    Assert-RecoveryExactProperties -Object $Manifest.export -Names @('reference', 'fingerprint', 'schema_version') -Label 'fixture export metadata'
    Assert-RecoveryExactProperties -Object $Manifest.restore -Names @('reference', 'fingerprint', 'result') -Label 'fixture restore metadata'
    if ($Manifest.export.fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or $Manifest.restore.fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or
        $Manifest.restore.result -cne 'verified_equal_to_export')
    { throw 'fixture manifest has invalid integrity metadata'
    }
}

function Assert-FixtureDsn
{
    param([Parameter(Mandatory)][string]$Value)

    Assert-RecoverySecretSafeText -Text $Value
    try
    { $uri = [Uri]$Value
    } catch
    { throw 'fixture database address is invalid'
    }
    if ($uri.Scheme -cne 'postgres' -or $uri.UserInfo -cne 'fixture' -or $uri.Host -notin @('127.0.0.1', 'localhost', '::1') -or
        $uri.Port -le 0 -or $uri.AbsolutePath.Trim('/') -cne 'engram_fixture' -or $uri.Query -cne '?sslmode=disable')
    {
        throw 'fixture database address must be the isolated non-secret fixture address'
    }
}

function Get-FixtureHealth
{
    param([Parameter(Mandatory)][int]$HealthPort)

    $client = [Net.Http.HttpClient]::new()
    try
    {
        $client.Timeout = [TimeSpan]::FromSeconds(2)
        $response = $client.GetAsync("http://127.0.0.1:$HealthPort/api/health").GetAwaiter().GetResult()
        if (-not $response.IsSuccessStatusCode)
        { return $null
        }
        $body = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
        Assert-RecoverySecretSafeText -Text $body
        try
        { $health = $body | ConvertFrom-Json -Depth 8
        } catch
        { return $null
        }
        if ($health.status -cne 'ready' -or [string]::IsNullOrWhiteSpace([string]$health.version))
        { return $null
        }
        [pscustomobject]@{ status = [string]$health.status; version = [string]$health.version }
    } catch
    {
        return $null
    } finally
    {
        $client.Dispose()
    }
}

function Test-OwnedFixtureServer
{
    param([Parameter(Mandatory)]$ServerMarker, [Parameter(Mandatory)][int]$ExpectedPort)

    Assert-RecoveryExactProperties -Object $ServerMarker -Names @('schema_version', 'fixture_id', 'server_fingerprint', 'process_id', 'process_start_utc_ticks', 'port') -Label 'fixture server marker'
    if ($ServerMarker.schema_version -isnot [string] -or $ServerMarker.fixture_id -isnot [string] -or
        $ServerMarker.server_fingerprint -isnot [string] -or
        (($ServerMarker.process_id -isnot [int]) -and ($ServerMarker.process_id -isnot [long])) -or
        (($ServerMarker.process_start_utc_ticks -isnot [int]) -and ($ServerMarker.process_start_utc_ticks -isnot [long])) -or
        (($ServerMarker.port -isnot [int]) -and ($ServerMarker.port -isnot [long])) -or
        $ServerMarker.process_id -lt 1 -or $ServerMarker.process_id -gt [int]::MaxValue -or $ServerMarker.process_start_utc_ticks -lt 1)
    { throw 'fixture server marker is malformed'
    }
    if ($ServerMarker.schema_version -cne 'engram.recovery.fixture-server.v1' -or $ServerMarker.fixture_id -cne $script:RecoveryFixtureID -or
        $ServerMarker.server_fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or $ServerMarker.port -ne $ExpectedPort)
    { throw 'fixture server marker is foreign'
    }
    try
    {
        $process = Get-Process -Id ([int]$ServerMarker.process_id) -ErrorAction Stop
        $process.StartTime.ToUniversalTime().Ticks -eq [int64]$ServerMarker.process_start_utc_ticks
    } catch
    {
        $false
    }
}

function Remove-OwnedFixtureServerArtifacts
{
    param([Parameter(Mandatory)]$Context, [Parameter(Mandatory)][string[]]$Paths)

    Assert-RecoveryFixtureTreeSafe -FixtureRoot $Context.FixtureRoot
    [void](Assert-RecoveryFixtureOwner -Context $Context)
    foreach ($path in $Paths)
    {
        if (-not (Test-Path -LiteralPath $path))
        { continue
        }
        if (-not (Test-Path -LiteralPath $path -PathType Container))
        { throw 'fixture server artifact is malformed'
        }
        [void](Assert-RecoveryContainedPath -Path $path -RepositoryRoot $Context.RepositoryRoot)
        [void](Assert-RecoverySafeExistingPath -Path $path)
        Remove-Item -LiteralPath $path -Recurse -Force
    }
}

function Stop-OwnedFixtureServer
{
    param([Parameter(Mandatory)]$ServerMarker)

    $process = Get-Process -Id ([int]$ServerMarker.process_id) -ErrorAction SilentlyContinue
    if ($null -ne $process -and $process.StartTime.ToUniversalTime().Ticks -eq [int64]$ServerMarker.process_start_utc_ticks)
    { Stop-Process -InputObject $process -Force
    }
}

$context = Get-RecoveryFixtureContext -FixtureRoot $FixtureRoot
Assert-RecoveryFixtureTreeSafe -FixtureRoot $context.FixtureRoot
[void](Assert-RecoveryFixtureOwner -Context $context)
$manifest = Read-RecoveryJson -Path (Join-Path $context.FixtureRoot 'fixture-manifest.json') -Context $context -Label 'fixture manifest'
Assert-FixtureManifest -Manifest $manifest -Context $context
Assert-FixtureDsn -Value $FixtureDatabaseDsn

if ([string]::IsNullOrWhiteSpace($ServerPath))
{
    $ServerPath = Join-Path $context.RepositoryRoot ('bin/engram-server' + $(if ($IsWindows)
            { '.exe'
            } else
            { ''
            }))
}
Assert-RecoverySecretSafeText -Text $ServerPath
$sourceServer = Assert-RecoveryContainedPath -Path $ServerPath -RepositoryRoot $context.RepositoryRoot
if (-not (Test-Path -LiteralPath $sourceServer -PathType Leaf))
{ throw 'built fixture server is missing'
}
[void](Assert-RecoverySafeExistingPath -Path $sourceServer)

$serverDirectory = Join-Path $context.FixtureRoot 'server'
$payloadDirectory = Join-Path $context.FixtureRoot 'payload'
$serverMarkerPath = Join-Path $serverDirectory 'fixture-server.json'
$healthPath = Join-Path $serverDirectory 'fixture-server-health.json'
$health = $null

if (Test-Path -LiteralPath $serverMarkerPath)
{
    if (-not (Test-Path -LiteralPath $serverMarkerPath -PathType Leaf))
    { throw 'fixture server marker is malformed'
    }
    $serverMarker = Read-RecoveryJson -Path $serverMarkerPath -Context $context -Label 'fixture server marker'
    $isRunning = Test-OwnedFixtureServer -ServerMarker $serverMarker -ExpectedPort $Port
    if ($isRunning)
    {
        $stagedServer = Join-Path $payloadDirectory ([IO.Path]::GetFileName($sourceServer))
        if (-not (Test-Path -LiteralPath $stagedServer -PathType Leaf) -or (Get-RecoverySha256 -Path $stagedServer) -cne $serverMarker.server_fingerprint)
        {
            throw 'fixture server payload does not match its ownership marker'
        }
        $health = Get-FixtureHealth -HealthPort $Port
        if ($null -eq $health)
        {
            Stop-OwnedFixtureServer -ServerMarker $serverMarker
            Remove-OwnedFixtureServerArtifacts -Context $context -Paths @($serverDirectory, $payloadDirectory)
        }
    } else
    {
        Remove-OwnedFixtureServerArtifacts -Context $context -Paths @($serverDirectory, $payloadDirectory)
    }
} elseif ((Test-Path -LiteralPath $serverDirectory) -or (Test-Path -LiteralPath $payloadDirectory))
{
    Remove-OwnedFixtureServerArtifacts -Context $context -Paths @($serverDirectory, $payloadDirectory)
}

if ($null -eq $health)
{
    New-Item -ItemType Directory -Path $serverDirectory | Out-Null
    New-Item -ItemType Directory -Path $payloadDirectory | Out-Null
    [void](Assert-RecoveryContainedPath -Path $serverDirectory -RepositoryRoot $context.RepositoryRoot)
    [void](Assert-RecoveryContainedPath -Path $payloadDirectory -RepositoryRoot $context.RepositoryRoot)
    $stagedServer = Join-Path $payloadDirectory ([IO.Path]::GetFileName($sourceServer))
    Copy-Item -LiteralPath $sourceServer -Destination $stagedServer
    [void](Assert-RecoverySafeExistingPath -Path $stagedServer)
    $serverFingerprint = Get-RecoverySha256 -Path $stagedServer

    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $stagedServer
    $startInfo.UseShellExecute = $false
    $startInfo.Environment.Clear()
    $startInfo.Environment['PATH'] = $env:PATH
    foreach ($name in @('SystemRoot', 'WINDIR', 'ComSpec'))
    {
        $value = [Environment]::GetEnvironmentVariable($name)
        if ($null -ne $value)
        {
            $startInfo.Environment[$name] = $value
        }

    }
    $fixtureHome = Join-Path $context.FixtureRoot 'runtime-home'
    $fixtureTemp = Join-Path $context.FixtureRoot 'runtime-temp'
    New-Item -ItemType Directory -Path $fixtureHome -Force | Out-Null
    New-Item -ItemType Directory -Path $fixtureTemp -Force | Out-Null
    $startInfo.Environment['HOME'] = $fixtureHome
    $startInfo.Environment['USERPROFILE'] = $fixtureHome
    $startInfo.Environment['TEMP'] = $fixtureTemp
    $startInfo.Environment['TMP'] = $fixtureTemp
    $startInfo.Environment['DATABASE_DSN'] = $FixtureDatabaseDsn
    $startInfo.Environment['ENGRAM_AUTH_DISABLED'] = 'true'
    $startInfo.Environment['ENGRAM_WORKER_HOST'] = '127.0.0.1'
    $startInfo.Environment['ENGRAM_WORKER_PORT'] = [string]$Port
    $startInfo.Environment['ENGRAM_TELEMETRY_ENABLED'] = 'false'

    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    [void]$process.Start()
    $marker = [ordered]@{
        schema_version = 'engram.recovery.fixture-server.v1'
        fixture_id = $script:RecoveryFixtureID
        server_fingerprint = $serverFingerprint
        process_id = $process.Id
        process_start_utc_ticks = $process.StartTime.ToUniversalTime().Ticks
        port = $Port
    }
    Write-RecoveryJson -Path $serverMarkerPath -Value $marker -Context $context

    $deadline = [DateTime]::UtcNow.AddSeconds(60)
    while ([DateTime]::UtcNow -lt $deadline -and $null -eq $health)
    {
        if ($process.HasExited)
        { break
        }
        $health = Get-FixtureHealth -HealthPort $Port
        if ($null -eq $health)
        { Start-Sleep -Milliseconds 250
        }
    }
    if ($null -eq $health)
    {
        if (-not $process.HasExited)
        { Stop-Process -Id $process.Id -Force
        }
        throw 'fixture server did not become ready with the isolated fixture configuration'
    }
} else
{
    $serverFingerprint = $serverMarker.server_fingerprint
}

$healthReceipt = [ordered]@{
    schema_version = 'engram.recovery.fixture-health.v1'
    fixture_id = $script:RecoveryFixtureID
    checked_at_utc = Get-RecoveryUtcNow
    endpoint = "http://127.0.0.1:$Port/api/health"
    health_status = $health.status
    server_version = $health.version
    server_fingerprint = $serverFingerprint
    scope = 'isolated_fixture_only'
}
Write-RecoveryJson -Path $healthPath -Value $healthReceipt -Context $context
Assert-RecoveryFixtureTreeSafe -FixtureRoot $context.FixtureRoot
$output = $healthReceipt | ConvertTo-Json -Depth 16
Assert-RecoverySecretSafeText -Text $output
Write-Output $output
