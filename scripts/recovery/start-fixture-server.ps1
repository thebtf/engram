[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$FixtureRoot,
    [string]$ServerPath,
    [ValidateRange(1024, 65535)][int]$Port = 38877,
    [string]$FixtureDatabaseDsn = 'postgres://fixture@127.0.0.1:55432/engram_fixture?sslmode=disable',
    [string]$FixturePsqlContainer = ''
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'recovery-fixture-common.ps1')
$null = Set-RecoveryFixturePsqlTransport -FixturePsqlContainer $FixturePsqlContainer

function Assert-FixtureManifest
{
    param([Parameter(Mandatory)]$Manifest, [Parameter(Mandatory)]$Context)

    Assert-RecoveryExactProperties -Object $Manifest -Names @('schema_version', 'fixture_id', 'fixture_root', 'run_id', 'fixture_class', 'created_at_utc', 'database', 'export', 'restore', 'selector_inventory', 'structural_fingerprints') -Label 'fixture manifest'
    if ($Manifest.schema_version -cne $script:RecoveryFixtureSchema -or $Manifest.fixture_id -cne $script:RecoveryFixtureID -or
        $Manifest.fixture_root -cne $Context.RelativeRoot -or $Manifest.run_id -cnotmatch '^[0-9a-f]{32}$' -or $Manifest.fixture_class -cne 'synthetic_redacted_legacy')
    {
        throw 'fixture manifest is foreign'
    }
    Assert-RecoveryExactProperties -Object $Manifest.export -Names @('reference', 'fingerprint', 'schema_version') -Label 'fixture export metadata'
    Assert-RecoveryExactProperties -Object $Manifest.restore -Names @('reference', 'fingerprint', 'result') -Label 'fixture restore metadata'
    if ($Manifest.export.fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or $Manifest.restore.fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or
        $Manifest.restore.result -cne 'verified_equal_to_export')
    {
        throw 'fixture manifest has invalid integrity metadata'
    }
}

function Get-FixtureHealth
{
    param([Parameter(Mandatory)][int]$HealthPort, [Parameter(Mandatory)][string]$ExpectedSourceCommit)

    $client = New-RecoveryFixtureLoopbackHttpClient
    try
    {
        $client.Timeout = [TimeSpan]::FromSeconds(2)
        $response = $client.GetAsync("http://127.0.0.1:$HealthPort/api/health").GetAwaiter().GetResult()
        if (-not $response.IsSuccessStatusCode)
        {
            return $null
        }
        $body = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
        Assert-RecoverySecretSafeText -Text $body
        try
        {
            $health = $body | ConvertFrom-Json -Depth 8
        } catch
        {
            return $null
        }
        if ($health.status -isnot [string] -or $health.version -isnot [string] -or $health.source_commit -isnot [string] -or
            $health.status -cne 'ready' -or [string]::IsNullOrWhiteSpace($health.version) -or $health.source_commit -cne $ExpectedSourceCommit)
        {
            return $null
        }
        [pscustomobject]@{
            status = [string]$health.status
            version = [string]$health.version
            source_commit = [string]$health.source_commit
        }
    } catch
    {
        return $null
    } finally
    {
        $client.Dispose()
    }
}

function Wait-FixtureOwnedReadiness
{
    param([Parameter(Mandatory)]$ServerMarker, [Parameter(Mandatory)][string]$ExpectedSourceCommit)

    $deadline = [DateTime]::UtcNow.AddSeconds(10)
    while ([DateTime]::UtcNow -lt $deadline)
    {
        if (-not (Test-RecoveryProcessIdentity -ProcessId ([int]$ServerMarker.process_id) -StartUtcTicks ([int64]$ServerMarker.process_start_utc_ticks)))
        {
            return $null
        }
        $health = Get-FixtureHealth -HealthPort ([int]$ServerMarker.port) -ExpectedSourceCommit $ExpectedSourceCommit
        if ($null -ne $health)
        {
            return $health
        }
        Start-Sleep -Milliseconds 250
    }
    $null
}

function Get-FixtureSourceCommit
{
    param([Parameter(Mandatory)][string]$SourceServer, [Parameter(Mandatory)]$Context)

    $sourceRepository = (& git -C (Split-Path -Parent $SourceServer) rev-parse --show-toplevel).Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($sourceRepository))
    {
        throw 'fixture server source repository cannot be determined'
    }
    $sourceRepository = (Resolve-Path -LiteralPath $sourceRepository).Path.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
    $sourceCommonDirectory = (& git -C $sourceRepository rev-parse --path-format=absolute --git-common-dir).Trim()
    $sourceCommonExitCode = $LASTEXITCODE
    $primaryCommonDirectory = (& git -C $Context.RepositoryRoot rev-parse --path-format=absolute --git-common-dir).Trim()
    if ($sourceCommonExitCode -ne 0 -or $LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($sourceCommonDirectory) -or [string]::IsNullOrWhiteSpace($primaryCommonDirectory))
    {
        throw 'fixture server source repository common Git directory cannot be determined'
    }
    $sourceCommonDirectory = (Resolve-Path -LiteralPath $sourceCommonDirectory).Path.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
    $primaryCommonDirectory = (Resolve-Path -LiteralPath $primaryCommonDirectory).Path.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
    $comparison = if ($IsWindows)
    { [StringComparison]::OrdinalIgnoreCase
    } else
    { [StringComparison]::Ordinal
    }
    if (-not [string]::Equals($sourceRepository, $Context.RepositoryRoot, $comparison))
    {
        [void](Assert-RecoveryContainedPath -Path $sourceRepository -RepositoryRoot $Context.RepositoryRoot)
        if (-not [string]::Equals($sourceCommonDirectory, $primaryCommonDirectory, $comparison))
        {
            throw 'fixture server must be built from the primary repository or an authorized linked worktree'
        }
    }
    $sourceChanges = @(& git -C $sourceRepository status --porcelain=v1 --untracked-files=all)
    if ($LASTEXITCODE -ne 0)
    {
        throw 'fixture server source repository status cannot be determined'
    }
    if ($sourceChanges.Count -ne 0)
    {
        throw 'fixture server source repository has uncommitted changes'
    }
    $sourceIndexEntries = @(& git -C $sourceRepository ls-files -v)
    if ($LASTEXITCODE -ne 0)
    {
        throw 'fixture server source repository index flags cannot be determined'
    }
    if (@($sourceIndexEntries | Where-Object { $_ -cmatch '^[hS] ' }).Count -ne 0)
    {
        throw 'fixture server source repository has hidden index entries'
    }
    $sourceCommit = (& git -C $sourceRepository rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0 -or $sourceCommit -cnotmatch '^[0-9a-f]{40}$')
    {
        throw 'fixture server source commit cannot be determined'
    }
    $sourceCommit
}

function Remove-OwnedFixtureServerArtifacts
{
    param([Parameter(Mandatory)]$Context, [Parameter(Mandatory)][string[]]$Paths)

    Assert-RecoveryFixtureTreeSafe -FixtureRoot $Context.FixtureRoot
    [void](Assert-RecoveryFixtureOwner -Context $Context)
    foreach ($path in $Paths)
    {
        if (-not (Test-Path -LiteralPath $path))
        {
            continue
        }
        if (-not (Test-Path -LiteralPath $path -PathType Container))
        {
            throw 'fixture server artifact is malformed'
        }
        [void](Assert-RecoveryContainedPath -Path $path -RepositoryRoot $Context.RepositoryRoot)
        [void](Assert-RecoverySafeExistingPath -Path $path)
        Remove-Item -LiteralPath $path -Recurse -Force
    }
}

$context = Get-RecoveryFixtureContext -FixtureRoot $FixtureRoot
Assert-RecoveryFixtureTreeSafe -FixtureRoot $context.FixtureRoot
$owner = Assert-RecoveryFixtureOwner -Context $context
$manifestPath = Join-Path $context.FixtureRoot 'fixture-manifest.json'
$manifest = Read-RecoveryJson -Path $manifestPath -Context $context -Label 'fixture manifest'
Assert-FixtureManifest -Manifest $manifest -Context $context
$export = Read-RecoveryJson -Path (Join-Path $context.FixtureRoot 'exports/legacy-fixture.json') -Context $context -Label 'synthetic fixture export'
$restore = Read-RecoveryJson -Path (Join-Path $context.FixtureRoot 'restored/legacy-fixture.json') -Context $context -Label 'synthetic fixture restore'
Assert-RecoveryFixtureStructuralFingerprints -Manifest $manifest -FixtureExport $export -FixtureRestore $restore
Assert-RecoveryFixtureDatabaseDsn -Value $FixtureDatabaseDsn
[void](Assert-RecoveryFixtureDatabaseBinding -Context $context -Manifest $manifest -ManifestPath $manifestPath -FixtureDatabaseDsn $FixtureDatabaseDsn)

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
{
    throw 'built fixture server is missing'
}
[void](Assert-RecoverySafeExistingPath -Path $sourceServer)
$sourceCommit = Get-FixtureSourceCommit -SourceServer $sourceServer -Context $context
$sourceFingerprint = Get-RecoverySha256 -Path $sourceServer
$serverDirectory = Join-Path $context.FixtureRoot 'server'
$payloadDirectory = Join-Path $context.FixtureRoot 'payload'
$serverMarkerPath = Join-Path $serverDirectory 'fixture-server.json'
$healthPath = Join-Path $serverDirectory 'fixture-server-health.json'
$serverMarker = $null
$health = $null

$fixtureLock = Enter-RecoveryFixtureMutationLock -Context $context
try
{
$hasServerArtifacts = (Test-Path -LiteralPath $serverMarkerPath) -or (Test-Path -LiteralPath $serverDirectory) -or (Test-Path -LiteralPath $payloadDirectory)
$markerIsUsable = $false
$matchesCandidate = $false
if (Test-Path -LiteralPath $serverMarkerPath)
{
    try
    {
        if (-not (Test-Path -LiteralPath $serverMarkerPath -PathType Leaf))
        {
            throw 'fixture server marker is malformed'
        }
        $serverMarker = Read-RecoveryJson -Path $serverMarkerPath -Context $context -Label 'fixture server marker'
        [void](Assert-RecoveryFixtureServerMarker -Context $context -Marker $serverMarker)
        $markerIsUsable = $true
    } catch
    {
        $serverMarker = $null
    }
}
if ($markerIsUsable)
{
    $matchesCandidate = $serverMarker.source_commit -ceq $sourceCommit -and $serverMarker.server_fingerprint -ceq $sourceFingerprint -and $serverMarker.port -eq $Port
    if ($matchesCandidate)
    {
        $health = Get-FixtureHealth -HealthPort $Port -ExpectedSourceCommit $sourceCommit
        if ($null -ne $health)
        {
            [void](Assert-RecoveryFixtureLiveServer -Context $context -Manifest $manifest -ManifestPath $manifestPath -FixtureDatabaseDsn $FixtureDatabaseDsn -ServerMarker $serverMarker -Health $health)
        }
    }
}
if ($null -eq $health)
{
    $binding = Assert-RecoveryFixtureDatabaseBinding -Context $context -Manifest $manifest -ManifestPath $manifestPath -FixtureDatabaseDsn $FixtureDatabaseDsn
    if ($binding.server_state -ceq 'launching')
    {
        Reset-RecoveryFixtureStaleDatabaseLauncherClaim -Context $context -Manifest $manifest -ManifestPath $manifestPath -FixtureDatabaseDsn $FixtureDatabaseDsn
    } elseif ($binding.server_state -ceq 'starting')
    {
        Reset-RecoveryFixtureStaleDatabaseStartingServerRun -Context $context -Manifest $manifest -ManifestPath $manifestPath -FixtureDatabaseDsn $FixtureDatabaseDsn
    } elseif ($binding.server_state -ceq 'running')
    {
        if (-not $markerIsUsable)
        {
            throw 'fixture database reports a running server but the server marker is missing or malformed'
        }
        if ($binding.owner_process_id -ne $serverMarker.process_id -or $binding.owner_process_start_utc_ticks -ne $serverMarker.process_start_utc_ticks -or
            $binding.server_fingerprint -cne $serverMarker.server_fingerprint -or $binding.source_commit -cne $serverMarker.source_commit -or $binding.port -ne $serverMarker.port)
        {
            throw 'fixture database running binding does not match the owned server marker'
        }
        if (Test-RecoveryProcessIdentity -ProcessId ([int]$serverMarker.process_id) -StartUtcTicks ([int64]$serverMarker.process_start_utc_ticks))
        {
            if (-not $matchesCandidate)
        {
            throw 'fixture server belongs to an active owned run and cannot be replaced'
        }
            $health = Wait-FixtureOwnedReadiness -ServerMarker $serverMarker -ExpectedSourceCommit $sourceCommit
            if ($null -ne $health)
            {
                [void](Assert-RecoveryFixtureLiveServer -Context $context -Manifest $manifest -ManifestPath $manifestPath -FixtureDatabaseDsn $FixtureDatabaseDsn -ServerMarker $serverMarker -Health $health)
            } else
            {
                Stop-RecoveryFixtureExactProcessAndAwaitAbsence -ProcessId ([int]$serverMarker.process_id) -StartUtcTicks ([int64]$serverMarker.process_start_utc_ticks) -Label 'fixture server process'
                Assert-RecoveryFixturePortUnbound -Port ([int]$serverMarker.port)
        Reset-RecoveryFixtureDatabaseServerRun -Context $context -ServerMarker $serverMarker -FixtureDatabaseDsn $FixtureDatabaseDsn
            }
        } else
        {
            Assert-RecoveryProcessIdentityAbsent -ProcessId ([int]$serverMarker.process_id) -StartUtcTicks ([int64]$serverMarker.process_start_utc_ticks) -Label 'fixture server process'
            Assert-RecoveryFixturePortUnbound -Port ([int]$serverMarker.port)
            Reset-RecoveryFixtureDatabaseServerRun -Context $context -ServerMarker $serverMarker -FixtureDatabaseDsn $FixtureDatabaseDsn
        }
    } elseif ($binding.server_state -ceq 'prepared')
    {
        if ($markerIsUsable)
        {
            if (Test-RecoveryProcessIdentity -ProcessId ([int]$serverMarker.process_id) -StartUtcTicks ([int64]$serverMarker.process_start_utc_ticks))
        {
            throw 'fixture server marker names an active process while the database is prepared'
            }
            Assert-RecoveryProcessIdentityAbsent -ProcessId ([int]$serverMarker.process_id) -StartUtcTicks ([int64]$serverMarker.process_start_utc_ticks) -Label 'fixture server process'
            Assert-RecoveryFixturePortUnbound -Port ([int]$serverMarker.port)
        }
    } else
    {
        throw 'fixture database ownership binding has an unsupported server state'
    }
    if ($null -eq $health)
    {
    if ($hasServerArtifacts)
    {
        Remove-OwnedFixtureServerArtifacts -Context $context -Paths @($serverDirectory, $payloadDirectory)
    }
    $serverMarker = $null
    }
}

if ($null -eq $health)
{
    New-Item -ItemType Directory -Path $serverDirectory | Out-Null
    New-Item -ItemType Directory -Path $payloadDirectory | Out-Null
    [void](Assert-RecoveryContainedPath -Path $serverDirectory -RepositoryRoot $context.RepositoryRoot)
    [void](Assert-RecoveryContainedPath -Path $payloadDirectory -RepositoryRoot $context.RepositoryRoot)
    [void](Assert-RecoverySafeExistingPath -Path $serverDirectory)
    [void](Assert-RecoverySafeExistingPath -Path $payloadDirectory)
    $stagedServer = Join-Path $payloadDirectory ([IO.Path]::GetFileName($sourceServer))
    Copy-Item -LiteralPath $sourceServer -Destination $stagedServer
    [void](Assert-RecoverySafeExistingPath -Path $stagedServer)
    $serverFingerprint = Get-RecoverySha256 -Path $stagedServer
    if ($serverFingerprint -cne $sourceFingerprint)
    {
        throw 'staged fixture server payload does not match the supplied source candidate'
    }

    $claim = $null
    $nativeLaunch = $null
    $childIdentity = $null
    $serverMarker = $null
    $armed = $false
    $mayRemoveArtifacts = $true
    try
    {
        $claim = Claim-RecoveryFixtureDatabaseServerRun -Context $context -Manifest $manifest -ManifestPath $manifestPath -FixtureDatabaseDsn $FixtureDatabaseDsn -ServerFingerprint $serverFingerprint -SourceCommit $sourceCommit -Port $Port
        $startEnvironment = [Collections.Generic.Dictionary[string, string]]::new([StringComparer]::OrdinalIgnoreCase)
        $startEnvironment['PATH'] = $env:PATH
        foreach ($name in @('SystemRoot', 'WINDIR', 'ComSpec'))
        {
            $value = [Environment]::GetEnvironmentVariable($name)
            if ($null -ne $value)
            {
                $startEnvironment[$name] = $value
            }
        }
        $fixtureHome = Join-Path $context.FixtureRoot 'runtime-home'
        $fixtureTemp = Join-Path $context.FixtureRoot 'runtime-temp'
        foreach ($directory in @($fixtureHome, $fixtureTemp))
        {
            New-Item -ItemType Directory -Path $directory -Force | Out-Null
            [void](Assert-RecoveryContainedPath -Path $directory -RepositoryRoot $context.RepositoryRoot)
            [void](Assert-RecoverySafeExistingPath -Path $directory)
        }
        $startEnvironment['HOME'] = $fixtureHome
        $startEnvironment['USERPROFILE'] = $fixtureHome
        $startEnvironment['TEMP'] = $fixtureTemp
        $startEnvironment['TMP'] = $fixtureTemp
        $startEnvironment['DATABASE_DSN'] = $FixtureDatabaseDsn
        $startEnvironment['ENGRAM_AUTH_DISABLED'] = 'true'
        $startEnvironment['ENGRAM_WORKER_HOST'] = '127.0.0.1'
        $startEnvironment['ENGRAM_WORKER_PORT'] = [string]$Port
        $startEnvironment['ENGRAM_TELEMETRY_ENABLED'] = 'false'
        $nativeLaunch = Start-RecoveryFixtureNativeSuspendedProcess -StagedExecutable $stagedServer -WorkingDirectory $context.FixtureRoot -Environment $startEnvironment
        $childIdentity = [pscustomobject]@{
            ProcessId = [int]$nativeLaunch.ProcessId
            StartUtcTicks = [int64]$nativeLaunch.ProcessStartUtcTicks
        }
        $serverMarker = [pscustomobject]@{
            schema_version = $script:RecoveryFixtureServerSchema
            fixture_id = $script:RecoveryFixtureID
            fixture_root = $context.RelativeRoot
            run_id = $owner.run_id
            manifest_fingerprint = Get-RecoverySha256 -Path $manifestPath
            database_identity_fingerprint = $manifest.database.identity_fingerprint
            source_commit = $sourceCommit
            server_fingerprint = $serverFingerprint
            staged_payload_filename = [IO.Path]::GetFileName($stagedServer)
            process_id = $childIdentity.ProcessId
            process_start_utc_ticks = $childIdentity.StartUtcTicks
            port = $Port
        }
        Arm-RecoveryFixtureDatabaseServerRun -Context $context -Claim $claim -ServerMarker $serverMarker -FixtureDatabaseDsn $FixtureDatabaseDsn
        $armed = $true
        Write-RecoveryJson -Path $serverMarkerPath -Value $serverMarker -Context $context
        $serverMarker = Read-RecoveryJson -Path $serverMarkerPath -Context $context -Label 'fixture server marker'
        [void](Assert-RecoveryFixtureServerMarker -Context $context -Marker $serverMarker)
        Confirm-RecoveryFixtureDatabaseServerRun -Context $context -Claim $claim -ServerMarker $serverMarker -FixtureDatabaseDsn $FixtureDatabaseDsn
        $nativeLaunch.Resume()
        $nativeLaunch.DisableKillOnJobClose()
        $nativeLaunch.Dispose()
        $nativeLaunch = $null
        $deadline = [DateTime]::UtcNow.AddSeconds(60)
        while ([DateTime]::UtcNow -lt $deadline -and $null -eq $health)
        {
            if (-not (Test-RecoveryProcessIdentity -ProcessId $childIdentity.ProcessId -StartUtcTicks $childIdentity.StartUtcTicks))
            {
                break
            }
            $health = Get-FixtureHealth -HealthPort $Port -ExpectedSourceCommit $sourceCommit
            if ($null -eq $health)
            {
                Start-Sleep -Milliseconds 250
            }
        }
        if ($null -eq $health)
        {
            throw 'fixture server did not become ready with the isolated fixture configuration'
        }
        [void](Assert-RecoveryFixtureLiveServer -Context $context -Manifest $manifest -ManifestPath $manifestPath -FixtureDatabaseDsn $FixtureDatabaseDsn -ServerMarker $serverMarker -Health $health)
    } catch
    {
        $failure = $_
        if ($null -ne $nativeLaunch)
        {
            $nativeLaunch.Dispose()
            $nativeLaunch = $null
        }
        try
        {
            if ($null -ne $childIdentity)
            {
                Stop-RecoveryFixtureExactProcessAndAwaitAbsence -ProcessId $childIdentity.ProcessId -StartUtcTicks $childIdentity.StartUtcTicks -Label 'fixture server process'
            }
            if ($null -ne $claim)
            {
                if ($armed)
                {
                    Reset-RecoveryFixtureDatabaseServerRun -Context $context -ServerMarker $serverMarker -FixtureDatabaseDsn $FixtureDatabaseDsn
                } else
                {
                    Release-RecoveryFixtureDatabaseServerRun -Context $context -Claim $claim -FixtureDatabaseDsn $FixtureDatabaseDsn
                }
            }
        } catch
        {
            $mayRemoveArtifacts = $false
        }
        if ($mayRemoveArtifacts)
        {
            try
            {
                Remove-OwnedFixtureServerArtifacts -Context $context -Paths @($serverDirectory, $payloadDirectory)
            } catch
            {
            }
        }
        throw $failure
    } finally
    {
        if ($null -ne $nativeLaunch)
        {
            $nativeLaunch.Dispose()
        }
    }
    }

} finally
{
    $fixtureLock.Dispose()
}

$healthReceipt = [ordered]@{
    schema_version = 'engram.recovery.fixture-health.v2'
    fixture_id = $script:RecoveryFixtureID
    fixture_root = $context.RelativeRoot
    run_id = $serverMarker.run_id
    manifest_fingerprint = $serverMarker.manifest_fingerprint
    database_identity_fingerprint = $serverMarker.database_identity_fingerprint
    checked_at_utc = Get-RecoveryUtcNow
    endpoint = "http://127.0.0.1:$Port/api/health"
    health_status = $health.status
    server_version = $health.version
    server_fingerprint = $serverMarker.server_fingerprint
    source_commit = $health.source_commit
    process_id = $serverMarker.process_id
    process_start_utc_ticks = $serverMarker.process_start_utc_ticks
    port = $serverMarker.port
    scope = 'isolated_fixture_only'
}
Write-RecoveryJson -Path $healthPath -Value $healthReceipt -Context $context
Assert-RecoveryFixtureTreeSafe -FixtureRoot $context.FixtureRoot
$output = $healthReceipt | ConvertTo-Json -Depth 16
Assert-RecoverySecretSafeText -Text $output
Write-Output $output
