Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$script:RecoveryFixtureOwnerSchema = 'engram.recovery.fixture-owner.v2'
$script:RecoveryFixtureSchema = 'engram.recovery.fixture.v1'
$script:RecoveryFixtureOwner = 'engram-recovery-fixture'
$script:RecoveryFixtureID = 'synthetic-redacted-legacy'
$script:RecoveryFixtureScriptRoot = $PSScriptRoot
$script:RecoveryFixtureDatabaseSchema = 'engram.recovery.fixture-database.v2'
$script:RecoveryFixtureServerSchema = 'engram.recovery.fixture-server.v2'
$script:RecoveryFixtureDatabaseTable = 'engram_recovery_fixture_ownership'
$script:RecoveryFixturePsqlTransport = $null
$script:RecoveryFixtureDatabaseDsn = 'postgres://fixture@127.0.0.1:55432/engram_fixture?sslmode=disable'
$script:RecoveryFixturePsqlRoutingEnvironmentNames = @(
    'PGHOST', 'PGHOSTADDR', 'PGPORT', 'PGDATABASE', 'PGUSER', 'PGSERVICE', 'PGSERVICEFILE', 'PGOPTIONS',
    'PGSSLMODE', 'PGREQUIRESSL', 'PGSSLCOMPRESSION', 'PGSSLCERT', 'PGSSLKEY', 'PGSSLROOTCERT', 'PGSSLCRL',
    'PGSSLMINPROTOCOLVERSION', 'PGSSLMAXPROTOCOLVERSION', 'PGSSLSNI', 'PGCHANNELBINDING', 'PGGSSENCMODE',
    'PGTARGETSESSIONATTRS'
)

function New-RecoveryFixtureLoopbackHttpClient
{
    $handler = [Net.Http.HttpClientHandler]::new()
    $handler.UseProxy = $false
    $handler.AllowAutoRedirect = $false
    [Net.Http.HttpClient]::new($handler)
}

function Get-RecoveryRepositoryRoot
{
    $commonDirectory = & git -C $script:RecoveryFixtureScriptRoot rev-parse --path-format=absolute --git-common-dir
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($commonDirectory))
    { throw 'unable to resolve the primary Git common directory'
    }
    (Resolve-Path -LiteralPath (Join-Path $commonDirectory '..')).Path.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
}

function Test-RecoveryReparsePoint
{
    param([Parameter(Mandatory)][IO.FileSystemInfo]$Item)

    (($Item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) -or
    ($Item.PSObject.Properties.Name -contains 'LinkType' -and -not [string]::IsNullOrWhiteSpace([string]$Item.LinkType))
}

function Assert-RecoverySafeExistingPath
{
    param([Parameter(Mandatory)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path))
    { throw 'fixture path component is missing'
    }
    $item = Get-Item -Force -LiteralPath $Path
    if (Test-RecoveryReparsePoint -Item $item)
    { throw 'fixture path contains a symlink or reparse point'
    }
    $item
}

function Assert-RecoveryContainedPath
{
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$RepositoryRoot,
        [switch]$LeafMayNotExist
    )

    $root = [IO.Path]::GetFullPath($RepositoryRoot).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
    $candidate = [IO.Path]::GetFullPath($Path).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
    $comparison = if ($IsWindows)
    { [StringComparison]::OrdinalIgnoreCase
    } else
    { [StringComparison]::Ordinal
    }
    $prefix = $root + [IO.Path]::DirectorySeparatorChar
    if ([string]::Equals($candidate, $root, $comparison) -or -not $candidate.StartsWith($prefix, $comparison))
    {
        throw 'fixture path must be a descendant of the repository root'
    }

    $relative = [IO.Path]::GetRelativePath($root, $candidate)
    if ([IO.Path]::IsPathRooted($relative) -or ($relative -split '[\\/]') -contains '..')
    {
        throw 'fixture path escapes the repository after normalization'
    }

    $current = $root
    [void](Assert-RecoverySafeExistingPath -Path $current)
    foreach ($component in $relative -split '[\\/]')
    {
        if ([string]::IsNullOrWhiteSpace($component) -or $component -ceq '.')
        { continue
        }
        $current = Join-Path $current $component
        if (-not (Test-Path -LiteralPath $current))
        {
            if ($LeafMayNotExist)
            { break
            }
            throw 'fixture path component is missing'
        }
        [void](Assert-RecoverySafeExistingPath -Path $current)
    }
    $candidate
}

function Assert-RecoveryWritableLeaf
{
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)]$Context,
        [switch]$RequireExists
    )

    $candidate = Assert-RecoveryContainedPath -Path $Path -RepositoryRoot $Context.RepositoryRoot -LeafMayNotExist
    $parent = Split-Path -Parent $candidate
    [void](Assert-RecoveryContainedPath -Path $parent -RepositoryRoot $Context.RepositoryRoot)
    [void](Assert-RecoverySafeExistingPath -Path $parent)
    if (-not (Test-Path -LiteralPath $candidate))
    {
        if ($RequireExists)
        { throw 'fixture writable leaf is missing'
        }
        return $candidate
    }

    if (-not (Test-Path -LiteralPath $candidate -PathType Leaf))
    { throw 'fixture writable leaf is not a regular file'
    }
    $item = Assert-RecoverySafeExistingPath -Path $candidate
    if ($item -isnot [IO.FileInfo] -or $item.PSIsContainer)
    { throw 'fixture writable leaf is not a regular file'
    }
    $candidate
}

function Get-RecoveryFixtureContext
{
    param([Parameter(Mandatory)][string]$FixtureRoot)

    if ([string]::IsNullOrWhiteSpace($FixtureRoot))
    { throw 'FixtureRoot is required'
    }
    if (($FixtureRoot -split '[\\/]') -contains '..')
    { throw 'FixtureRoot traversal is forbidden'
    }

    $repositoryRoot = Get-RecoveryRepositoryRoot
    $candidate = if ([IO.Path]::IsPathRooted($FixtureRoot))
    {
        $FixtureRoot
    } else
    {
        Join-Path $repositoryRoot $FixtureRoot
    }
    $fixturePath = Assert-RecoveryContainedPath -Path $candidate -RepositoryRoot $repositoryRoot -LeafMayNotExist
    [pscustomobject]@{
        RepositoryRoot = $repositoryRoot
        FixtureRoot = $fixturePath
        RelativeRoot = [IO.Path]::GetRelativePath($repositoryRoot, $fixturePath).Replace('\', '/')
    }
}

function Enter-RecoveryFixtureMutationLock
{
    param([Parameter(Mandatory)]$Context)

    $parent = Split-Path -Parent $Context.FixtureRoot
    [void](Assert-RecoveryContainedPath -Path $parent -RepositoryRoot $Context.RepositoryRoot -LeafMayNotExist)
    [void](Assert-RecoverySafeExistingPath -Path $parent)
    $lockPath = Join-Path $parent ('.' + [IO.Path]::GetFileName($Context.FixtureRoot) + '.engram-recovery-fixture.lock')
    [void](Assert-RecoveryContainedPath -Path $lockPath -RepositoryRoot $Context.RepositoryRoot -LeafMayNotExist)
    if (Test-Path -LiteralPath $lockPath)
    {
        [void](Assert-RecoverySafeExistingPath -Path $lockPath)
    }
    $deadline = [DateTime]::UtcNow.AddSeconds(15)
    while ($true)
    {
        try
        {
            return [IO.File]::Open($lockPath, [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
        } catch [IO.IOException]
        {
            if ([DateTime]::UtcNow -ge $deadline)
            { throw 'fixture mutation lock is held by another recovery run'
            }
            Start-Sleep -Milliseconds 100
        }
    }
}

function Assert-RecoveryFixtureTreeSafe
{
    param([Parameter(Mandatory)][string]$FixtureRoot)

    [void](Assert-RecoverySafeExistingPath -Path $FixtureRoot)
    foreach ($item in @(Get-ChildItem -Force -Recurse -LiteralPath $FixtureRoot))
    {
        if (Test-RecoveryReparsePoint -Item $item)
        { throw 'fixture tree contains a symlink or reparse point'
        }
    }
}

function Assert-RecoveryFixtureOwner
{
    param([Parameter(Mandatory)]$Context)

    $markerPath = Join-Path $Context.FixtureRoot '.engram-recovery-fixture-owner.json'
    if (-not (Test-Path -LiteralPath $markerPath -PathType Leaf))
    { throw 'fixture ownership marker is missing'
    }
    $raw = [IO.File]::ReadAllText($markerPath)
    Assert-RecoverySecretSafeText -Text $raw
    try
    { $marker = $raw | ConvertFrom-Json -Depth 16
    } catch
    { throw 'fixture ownership marker is malformed'
    }
    Assert-RecoveryExactProperties -Object $marker -Names @('schema_version', 'owner', 'fixture_id', 'fixture_root', 'run_id', 'creator_process_id', 'creator_process_start_utc_ticks') -Label 'fixture ownership marker'
    if ($marker.schema_version -isnot [string] -or $marker.owner -isnot [string] -or $marker.fixture_id -isnot [string] -or
        $marker.fixture_root -isnot [string] -or $marker.run_id -isnot [string] -or
        (($marker.creator_process_id -isnot [int]) -and ($marker.creator_process_id -isnot [long])) -or
        (($marker.creator_process_start_utc_ticks -isnot [int]) -and ($marker.creator_process_start_utc_ticks -isnot [long])) -or
        $marker.creator_process_id -lt 1 -or $marker.creator_process_start_utc_ticks -lt 1)
    {
        throw 'fixture ownership marker is malformed'
    }
    if ($marker.schema_version -cne $script:RecoveryFixtureOwnerSchema -or $marker.owner -cne $script:RecoveryFixtureOwner -or
        $marker.fixture_id -cne $script:RecoveryFixtureID -or $marker.fixture_root -cne $Context.RelativeRoot -or
        $marker.run_id -cnotmatch '^[0-9a-f]{32}$')
    {
        throw 'fixture ownership marker is foreign'
    }
    $marker
}

function New-RecoveryFixtureRoot
{
    param(
        [Parameter(Mandatory)]$Context,
        [string]$FixtureDatabaseDsn
    )

    $parent = Split-Path -Parent $Context.FixtureRoot
    if ($parent -ceq $Context.RepositoryRoot)
    {
        [void](Assert-RecoverySafeExistingPath -Path $parent)
    } else
    {
        [void](Assert-RecoveryContainedPath -Path $parent -RepositoryRoot $Context.RepositoryRoot -LeafMayNotExist)
    }
    $fixtureLock = Enter-RecoveryFixtureMutationLock -Context $Context
    try
    {
        if (-not [string]::IsNullOrWhiteSpace($FixtureDatabaseDsn))
        {
            [void](Assert-RecoveryFixtureDatabaseMutationTarget -Context $Context -FixtureDatabaseDsn $FixtureDatabaseDsn -RequireInactiveBinding)
        }
        if (Test-Path -LiteralPath $Context.FixtureRoot)
        {
            Assert-RecoveryFixtureTreeSafe -FixtureRoot $Context.FixtureRoot
            $owner = Assert-RecoveryFixtureOwner -Context $Context
            Assert-RecoveryFixtureServerRunInactive -Context $Context -Owner $owner
            if (-not [string]::IsNullOrWhiteSpace($FixtureDatabaseDsn))
            {
                Assert-RecoveryFixtureDatabaseRunInactive -Context $Context -Owner $owner -FixtureDatabaseDsn $FixtureDatabaseDsn
            }
            Remove-Item -LiteralPath $Context.FixtureRoot -Recurse -Force
        }

        New-Item -ItemType Directory -Path $Context.FixtureRoot -Force | Out-Null
        [void](Assert-RecoveryContainedPath -Path $Context.FixtureRoot -RepositoryRoot $Context.RepositoryRoot)
        [void](Assert-RecoverySafeExistingPath -Path $Context.FixtureRoot)

        $process = Get-RecoveryCurrentProcessIdentity
        $marker = [ordered]@{
            schema_version = $script:RecoveryFixtureOwnerSchema
            owner = $script:RecoveryFixtureOwner
            fixture_id = $script:RecoveryFixtureID
            fixture_root = $Context.RelativeRoot
            run_id = [Guid]::NewGuid().ToString('N')
            creator_process_id = $process.ProcessId
            creator_process_start_utc_ticks = $process.StartUtcTicks
        }
        Write-RecoveryJson -Path (Join-Path $Context.FixtureRoot '.engram-recovery-fixture-owner.json') -Value $marker -Context $Context
    } finally
    {
        $fixtureLock.Dispose()
    }
}

function Assert-RecoveryExactProperties
{
    param(
        [Parameter(Mandatory)]$Object,
        [Parameter(Mandatory)][string[]]$Names,
        [Parameter(Mandatory)][string]$Label
    )

    if ($null -eq $Object)
    { throw "$Label is missing"
    }
    $actual = @($Object.PSObject.Properties.Name | Sort-Object)
    $expected = @($Names | Sort-Object)
    if (($actual -join "`n") -cne ($expected -join "`n"))
    { throw "$Label has an invalid shape"
    }
}

function Assert-RecoverySecretSafeText
{
    param([AllowEmptyString()][string]$Text)

    if ($Text -match '(?i)(password|secret|credential|authorization|api[_-]?key|access[_-]?key|\btoken\b|://[^/@\s]+:[^/@\s]+@)')
    {
        throw 'fixture artifact contains a forbidden secret-bearing value'
    }
}

function Get-RecoverySha256
{
    param([Parameter(Mandatory)][string]$Path)

    'sha256:' + (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Write-RecoveryJson
{
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)]$Value,
        [Parameter(Mandatory)]$Context
    )

    $target = Assert-RecoveryWritableLeaf -Path $Path -Context $Context
    $text = $Value | ConvertTo-Json -Depth 32
    Assert-RecoverySecretSafeText -Text $text
    [IO.File]::WriteAllText($target, ($text + [Environment]::NewLine), [Text.UTF8Encoding]::new($false))
    [void](Assert-RecoveryWritableLeaf -Path $target -Context $Context -RequireExists)
}

function Read-RecoveryJson
{
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)]$Context,
        [Parameter(Mandatory)][string]$Label
    )

    [void](Assert-RecoveryContainedPath -Path $Path -RepositoryRoot $Context.RepositoryRoot)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf))
    { throw "$Label is missing"
    }
    [void](Assert-RecoverySafeExistingPath -Path $Path)
    $text = [IO.File]::ReadAllText($Path)
    Assert-RecoverySecretSafeText -Text $text
    try
    { $text | ConvertFrom-Json -Depth 32
    } catch
    { throw "$Label is malformed"
    }
}

function Get-RecoveryUtcNow
{ [DateTime]::UtcNow.ToString('o')
}

function Get-RecoveryCurrentProcessIdentity
{
    $process = [Diagnostics.Process]::GetCurrentProcess()
    try
    {
        [pscustomobject]@{
            ProcessId = $process.Id
            StartUtcTicks = $process.StartTime.ToUniversalTime().Ticks
        }
    } finally
    {
        $process.Dispose()
    }
}

function Import-RecoveryFixtureNativeLaunch
{
    if (-not $IsWindows)
    {
        throw 'fixture native launch prerequisite is missing: Windows is required'
    }
    $nativeType = 'Engram.Recovery.FixtureNativeLaunch' -as [type]
    if ($null -ne $nativeType)
    { return $nativeType
    }
    $nativeSource = Join-Path $script:RecoveryFixtureScriptRoot 'recovery-native-launch.cs'
    if (-not (Test-Path -LiteralPath $nativeSource -PathType Leaf))
    {
        throw 'fixture native launch prerequisite is missing: recovery-native-launch.cs'
    }
    [void](Assert-RecoverySafeExistingPath -Path $nativeSource)
    try
    { Add-Type -Path $nativeSource -ErrorAction Stop
    } catch
    { throw "fixture native launch prerequisite cannot be compiled: $($_.Exception.Message)"
    }
    $nativeType = 'Engram.Recovery.FixtureNativeLaunch' -as [type]
    if ($null -eq $nativeType)
    { throw 'fixture native launch prerequisite did not define FixtureNativeLaunch'
    }
    $nativeType
}

function Start-RecoveryFixtureNativeSuspendedProcess
{
    param(
        [Parameter(Mandatory)][string]$StagedExecutable,
        [Parameter(Mandatory)][string]$WorkingDirectory,
        [Parameter(Mandatory)][System.Collections.Generic.IDictionary[string, string]]$Environment
    )

    $nativeType = Import-RecoveryFixtureNativeLaunch
    $nativeType::CreateSuspended($StagedExecutable, $WorkingDirectory, $Environment)
}

function Test-RecoveryProcessIdentity
{
    param([Parameter(Mandatory)][int]$ProcessId, [Parameter(Mandatory)][int64]$StartUtcTicks)

    if ($ProcessId -lt 1 -or $StartUtcTicks -lt 1)
    { return $false
    }
    try
    {
        $process = Get-Process -Id $ProcessId -ErrorAction Stop
    } catch
    {
        if ($_.FullyQualifiedErrorId -clike 'NoProcessFoundForGivenId,*')
        { return $false
        }
        throw 'fixture process identity cannot be determined'
    }
    try
    {
        try
        { return $process.StartTime.ToUniversalTime().Ticks -eq $StartUtcTicks
        } catch
        { throw 'fixture process identity cannot be determined'
        }
        } finally
        { $process.Dispose()
    }
}

function Assert-RecoveryProcessIdentityAbsent
{
    param(
        [Parameter(Mandatory)][int]$ProcessId,
        [Parameter(Mandatory)][int64]$StartUtcTicks,
        [string]$Label = 'fixture launcher process'
    )

    if ($ProcessId -lt 1 -or $StartUtcTicks -lt 1)
    { throw "$Label identity is malformed"
    }
    try
    {
        $process = Get-Process -Id $ProcessId -ErrorAction Stop
    } catch
    {
        if ($_.FullyQualifiedErrorId -clike 'NoProcessFoundForGivenId,*')
        { return
        }
        throw "$Label identity cannot be determined"
    }
    try
    {
        try
        { $actualStartUtcTicks = $process.StartTime.ToUniversalTime().Ticks
        } catch
        { throw "$Label identity cannot be determined"
        }
    } finally
    {
        $process.Dispose()
    }
    if ($actualStartUtcTicks -eq $StartUtcTicks)
    { throw "$Label is active"
    }
    return
}

function Stop-RecoveryFixtureExactProcessAndAwaitAbsence
{
    param(
        [Parameter(Mandatory)][int]$ProcessId,
        [Parameter(Mandatory)][int64]$StartUtcTicks,
        [string]$Label = 'fixture server process'
    )

    $nativeType = Import-RecoveryFixtureNativeLaunch
    [void]$nativeType::TerminateExactProcessAndWait($ProcessId, $StartUtcTicks, 10000)
    Assert-RecoveryProcessIdentityAbsent -ProcessId $ProcessId -StartUtcTicks $StartUtcTicks -Label $Label
}

function Assert-RecoveryFixtureServerMarker
{
    param([Parameter(Mandatory)]$Context, [Parameter(Mandatory)]$Marker)

    $owner = Assert-RecoveryFixtureOwner -Context $Context
    Assert-RecoveryExactProperties -Object $Marker -Names @('schema_version', 'fixture_id', 'fixture_root', 'run_id', 'manifest_fingerprint', 'database_identity_fingerprint', 'source_commit', 'server_fingerprint', 'staged_payload_filename', 'process_id', 'process_start_utc_ticks', 'port') -Label 'fixture server marker'
    foreach ($name in @('schema_version', 'fixture_id', 'fixture_root', 'run_id', 'manifest_fingerprint', 'database_identity_fingerprint', 'source_commit', 'server_fingerprint', 'staged_payload_filename'))
    {
        if ($Marker.$name -isnot [string])
        { throw 'fixture server marker is malformed'
        }
    }
    if ((($Marker.process_id -isnot [int]) -and ($Marker.process_id -isnot [long])) -or
        (($Marker.process_start_utc_ticks -isnot [int]) -and ($Marker.process_start_utc_ticks -isnot [long])) -or
        (($Marker.port -isnot [int]) -and ($Marker.port -isnot [long])) -or
        $Marker.process_id -lt 1 -or $Marker.process_start_utc_ticks -lt 1 -or $Marker.port -lt 1024 -or $Marker.port -gt 65535)
    {
        throw 'fixture server marker is malformed'
    }
    if ($Marker.schema_version -cne $script:RecoveryFixtureServerSchema -or $Marker.fixture_id -cne $script:RecoveryFixtureID -or
        $Marker.fixture_root -cne $Context.RelativeRoot -or $Marker.run_id -cne $owner.run_id -or
        $Marker.manifest_fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or $Marker.database_identity_fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or
        $Marker.source_commit -cnotmatch '^[0-9a-f]{40}$' -or $Marker.server_fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or
        $Marker.staged_payload_filename -cnotmatch '^[^\\/]+$')
    {
        throw 'fixture server marker is foreign'
    }
    $Marker
}

function Assert-RecoveryFixtureServerRunInactive
{
    param([Parameter(Mandatory)]$Context, [Parameter(Mandatory)]$Owner)

    $markerPath = Join-Path $Context.FixtureRoot 'server/fixture-server.json'
    if (-not (Test-Path -LiteralPath $markerPath))
    { return
    }
    if (-not (Test-Path -LiteralPath $markerPath -PathType Leaf))
    { throw 'fixture server marker is malformed'
    }
    $marker = Read-RecoveryJson -Path $markerPath -Context $Context -Label 'fixture server marker'
    [void](Assert-RecoveryFixtureServerMarker -Context $Context -Marker $marker)
    if (Test-RecoveryProcessIdentity -ProcessId ([int]$marker.process_id) -StartUtcTicks ([int64]$marker.process_start_utc_ticks))
    {
        throw 'fixture reset refused while the owned fixture server is active'
    }
}

function Assert-RecoveryFixtureDatabaseDsn
{
    param([Parameter(Mandatory)][string]$Value)

    Assert-RecoverySecretSafeText -Text $Value
    if ($Value -cne $script:RecoveryFixtureDatabaseDsn)
    {
        throw 'fixture database address must be the isolated non-secret fixture address'
    }
}

function Assert-RecoveryFixtureDockerAuthority
{
    param([Parameter(Mandatory)][string]$DockerPath)

    $environmentNames = @([Environment]::GetEnvironmentVariables([EnvironmentVariableTarget]::Process).Keys | ForEach-Object { [string]$_ })
    foreach ($name in @('DOCKER_HOST', 'DOCKER_CONTEXT'))
    {
        if (@($environmentNames | Where-Object { $_ -ceq $name }).Count -ne 0)
        {
            throw 'fixture Docker authority must not be ambient'
        }
    }
    $endpoint = @(& $DockerPath '--context' 'default' 'context' 'inspect' 'default' '--format' '{{.Endpoints.docker.Host}}')
    if ($LASTEXITCODE -ne 0)
    {
        throw 'fixture Docker default context cannot be inspected'
    }
    $endpoint = @($endpoint | ForEach-Object { ([string]$_).Trim() })
    $expectedEndpoint = if ($IsWindows)
    { 'npipe:////./pipe/docker_engine'
    } else
    { 'unix:///var/run/docker.sock'
    }
    if ($endpoint.Count -ne 1 -or $endpoint[0] -cne $expectedEndpoint)
    {
        throw 'fixture Docker default context is not a verified local endpoint'
    }
}

function Set-RecoveryFixturePsqlTransport
{
    param([AllowEmptyString()][string]$FixturePsqlContainer = '')

    if (-not [string]::IsNullOrWhiteSpace($FixturePsqlContainer) -and $FixturePsqlContainer -cnotmatch '^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$')
    {
        throw 'fixture PostgreSQL container name is invalid'
    }
    if (-not [string]::IsNullOrWhiteSpace($FixturePsqlContainer))
    {
        $docker = Get-Command -Name 'docker' -CommandType Application -ErrorAction SilentlyContinue
        if ($null -eq $docker)
        {
            throw 'fixture database prerequisite is missing: docker must be available for the explicit fixture container psql transport'
        }
        Assert-RecoveryFixtureDockerAuthority -DockerPath $docker.Source
        $script:RecoveryFixturePsqlTransport = [pscustomobject]@{ FilePath = $docker.Source; Prefix = @('--context', 'default', 'exec'); ContainerName = $FixturePsqlContainer; IsDockerContainerPsql = $true }
        return
    }
    $psql = Get-Command -Name 'psql' -CommandType Application -ErrorAction SilentlyContinue
    if ($null -eq $psql)
    {
        throw 'fixture database prerequisite is missing: psql must be available on PATH or an explicit fixture container name must be supplied'
    }
    $script:RecoveryFixturePsqlTransport = [pscustomobject]@{ FilePath = $psql.Source; Prefix = @(); IsDockerContainerPsql = $false }
}

function Assert-RecoveryFixtureDockerPsqlTarget
{
    param([Parameter(Mandatory)]$Transport)
    Assert-RecoveryFixtureDockerAuthority -DockerPath $Transport.FilePath

    if ($Transport.PSObject.Properties.Match('ContainerName').Count -ne 1 -or $Transport.ContainerName -isnot [string] -or
        $Transport.ContainerName -cnotmatch '^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$')
    {
        throw 'fixture Docker psql transport container is invalid'
    }
    $template = '{{println .Id}}{{println .Name}}{{println .State.Running}}{{json (index .NetworkSettings.Ports "5432/tcp")}}'
    $values = @(& $Transport.FilePath '--context' 'default' 'inspect' '--type' 'container' '--format' $template $Transport.ContainerName)
    if ($LASTEXITCODE -ne 0)
    {
        throw 'fixture Docker psql transport container cannot be inspected'
    }
    $values = @($values | ForEach-Object { ([string]$_).Trim() })
    if ($values.Count -ne 4 -or [string]::IsNullOrWhiteSpace($values[0]) -or [string]::IsNullOrWhiteSpace($values[1]) -or
        $values[0] -cnotmatch '^[0-9a-f]{64}$' -or $values[1] -cne ('/' + $Transport.ContainerName) -or $values[2] -cne 'true')
    {
        throw 'fixture Docker psql transport container is not the exact named running container'
    }
    try
    {
        $portBindings = @($values[3] | ConvertFrom-Json -Depth 8)
    } catch
    {
        throw 'fixture Docker psql transport endpoint mapping is malformed'
    }
    $expectedBindings = @($portBindings | Where-Object {
            $null -ne $_ -and $_.PSObject.Properties.Match('HostIp').Count -eq 1 -and $_.PSObject.Properties.Match('HostPort').Count -eq 1 -and
            $_.HostIp -is [string] -and $_.HostPort -is [string] -and $_.HostIp -ceq '127.0.0.1' -and $_.HostPort -ceq '55432'
        })
    if ($portBindings.Count -ne 1 -or $expectedBindings.Count -ne 1)
    {
        throw 'fixture Docker psql transport does not own the required isolated endpoint mapping'
    }
    $values[0]
}

function Get-RecoveryFixtureStructuralFingerprints
{
    param([Parameter(Mandatory)]$FixtureExport)

    Assert-RecoveryExactProperties -Object $FixtureExport -Names @('schema_version', 'fixture_class', 'records') -Label 'synthetic fixture export'
    if ($FixtureExport.schema_version -isnot [string] -or $FixtureExport.fixture_class -isnot [string] -or $FixtureExport.schema_version -cne 'engram.recovery.synthetic-export.v1' -or $FixtureExport.fixture_class -cne 'synthetic_redacted_legacy')
    { throw 'synthetic fixture export is malformed'
    }
    $records = @($FixtureExport.records)
    if ($records.Count -ne 3)
    { throw 'synthetic fixture export has an invalid record count'
    }
    $families = @($records | ForEach-Object { [string]$_.family } | Sort-Object -Unique)
    if (($families -join "`n") -cne ('legacy_payloads' + "`n" + 'projects'))
    { throw 'synthetic fixture export has unexpected structural families'
    }
    $fingerprints = [ordered]@{}
    foreach ($family in $families)
    {
        $familyRecords = @($records | Where-Object { $_.family -ceq $family })
        $canonicalRecords = @()
        foreach ($record in $familyRecords)
        {
            $properties = if ($family -ceq 'projects')
            { @('family', 'fixture_record', 'provenance', 'selector_fingerprint')
            } else
            { @('family', 'fixture_record', 'payload_fingerprint', 'provenance')
            }
            Assert-RecoveryExactProperties -Object $record -Names $properties -Label "$family fixture record"
            foreach ($name in $properties)
            {
                if ($record.$name -isnot [string])
                { throw 'synthetic fixture record is malformed'
                }
            }
            if ($record.family -cne $family -or $record.provenance -cnotin @('synthetic', 'synthetic_redacted') -or
                ($family -ceq 'projects' -and $record.selector_fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$') -or
                ($family -ceq 'legacy_payloads' -and $record.payload_fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$'))
            {
                throw 'synthetic fixture record is foreign'
            }
            $canonical = [ordered]@{}
            foreach ($name in $properties)
            { $canonical[$name] = [string]$record.$name
            }
            $canonicalRecords += [pscustomobject]$canonical
        }
        $serialized = @($canonicalRecords | Sort-Object fixture_record | ConvertTo-Json -Depth 8 -Compress) -join "`n"
        $fingerprints[$family] = Get-RecoveryStringSha256 -Text $serialized
    }
    [pscustomobject]$fingerprints
}

function Assert-RecoveryFixtureStructuralFingerprints
{
    param([Parameter(Mandatory)]$Manifest, [Parameter(Mandatory)]$FixtureExport, [Parameter(Mandatory)]$FixtureRestore)

    $exportFingerprints = Get-RecoveryFixtureStructuralFingerprints -FixtureExport $FixtureExport
    $restoreFingerprints = Get-RecoveryFixtureStructuralFingerprints -FixtureExport $FixtureRestore
    Assert-RecoveryExactProperties -Object $Manifest.structural_fingerprints -Names @('projects', 'legacy_payloads') -Label 'fixture manifest structural fingerprints'
    foreach ($family in @('projects', 'legacy_payloads'))
    {
        if ($Manifest.structural_fingerprints.$family -isnot [string] -or $Manifest.structural_fingerprints.$family -cnotmatch '^sha256:[0-9a-f]{64}$' -or
            $Manifest.structural_fingerprints.$family -cne $exportFingerprints.$family -or $Manifest.structural_fingerprints.$family -cne $restoreFingerprints.$family)
        {
            throw 'fixture manifest structural fingerprints do not match the synthetic export and restore'
        }
    }
}


function Get-RecoveryStringSha256
{
    param([Parameter(Mandatory)][string]$Text)

    $algorithm = [Security.Cryptography.SHA256]::Create()
    try
    {
        $bytes = [Text.UTF8Encoding]::new($false).GetBytes($Text)
        try
        { 'sha256:' + ([Convert]::ToHexString($algorithm.ComputeHash($bytes))).ToLowerInvariant()
        } finally
        { [Array]::Clear($bytes, 0, $bytes.Length)
        }
    } finally
    { $algorithm.Dispose()
    }
}

function Invoke-RecoveryFixturePsqlCommand
{
    param([Parameter(Mandatory)][string]$FilePath, [Parameter(Mandatory)][string[]]$Arguments)

    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $FilePath
    $startInfo.UseShellExecute = $false
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    foreach ($name in $script:RecoveryFixturePsqlRoutingEnvironmentNames)
    {
        [void]$startInfo.Environment.Remove($name)
    }
    foreach ($argument in $Arguments)
    {
        [void]$startInfo.ArgumentList.Add($argument)
    }
    $process = $null
    try
    {
        $process = [Diagnostics.Process]::Start($startInfo)
        if ($null -eq $process)
        {
            throw 'fixture database ownership command could not start'
        }
        $outputTask = $process.StandardOutput.ReadToEndAsync()
        $errorTask = $process.StandardError.ReadToEndAsync()
        $process.WaitForExit()
        $output = $outputTask.GetAwaiter().GetResult()
        [void]$errorTask.GetAwaiter().GetResult()
        if ($process.ExitCode -ne 0)
        {
            throw 'fixture database ownership command failed'
        }
        $output
    } finally
    {
        if ($null -ne $process)
        {
            $process.Dispose()
        }
    }
}

function Invoke-RecoveryFixturePsql
{
    param(
        [Parameter(Mandatory)][string]$FixtureDatabaseDsn,
        [Parameter(Mandatory)][string]$Query,
        [System.Collections.IDictionary]$Variables = @{}
    )

    Assert-RecoveryFixtureDatabaseDsn -Value $FixtureDatabaseDsn
    $transport = $script:RecoveryFixturePsqlTransport
    if ($null -eq $transport)
    {
        throw 'fixture database transport is not configured'
    }
    $commandPrefix = @($transport.Prefix)
    if ($transport.IsDockerContainerPsql)
    {
        $containerId = Assert-RecoveryFixtureDockerPsqlTarget -Transport $transport
        foreach ($name in $script:RecoveryFixturePsqlRoutingEnvironmentNames)
        {
            $commandPrefix += @('--env', ($name + '='))
        }
        $commandPrefix += @($containerId, 'psql')
    }
    $effectiveFixtureDatabaseDsn = $FixtureDatabaseDsn
    if ($transport.IsDockerContainerPsql)
    {
        $containerUri = [UriBuilder]$FixtureDatabaseDsn
        $containerUri.Host = '127.0.0.1'
        $containerUri.Port = 5432
        $effectiveFixtureDatabaseDsn = $containerUri.Uri.AbsoluteUri
    }
    $arguments = [Collections.Generic.List[string]]::new()
    foreach ($argument in @('-X', '-w', '-q', '-t', '-A', '--set=ON_ERROR_STOP=1', "--dbname=$effectiveFixtureDatabaseDsn"))
    { [void]$arguments.Add($argument)
    }
    foreach ($name in @($Variables.Keys | Sort-Object))
    {
        $value = [string]$Variables[$name]
        if ($name -cnotmatch '^[a-z_]+$')
        { throw 'fixture database parameter name is invalid'
        }
        Assert-RecoverySecretSafeText -Text $value
        [void]$arguments.Add("--set=$name=$value")
    }
    [void]$arguments.Add('--command')
    [void]$arguments.Add($Query)
    $commandArguments = [Collections.Generic.List[string]]::new()
    foreach ($argument in @($commandPrefix))
    {
        [void]$commandArguments.Add([string]$argument)
    }
    foreach ($argument in $arguments)
    {
        [void]$commandArguments.Add([string]$argument)
    }
    $output = @(Invoke-RecoveryFixturePsqlCommand -FilePath $transport.FilePath -Arguments $commandArguments.ToArray())
    $text = (@($output | ForEach-Object { [string]$_ }) -join "`n").Trim()
    Assert-RecoverySecretSafeText -Text $text
    $text
}

function Get-RecoveryFixtureDatabaseState
{
    param([Parameter(Mandatory)][string]$FixtureDatabaseDsn)

  $query = @"
SELECT json_build_object(
    'identity', json_build_object(
        'cluster_identifier', (pg_control_system()).system_identifier::text,
        'database', current_database(),
        'database_oid', (SELECT oid::text FROM pg_database WHERE datname = current_database()),
        'owner', current_user
    ),
    'relations', coalesce((
        SELECT json_agg(json_build_object(
            'schema', namespace.nspname,
            'name', relation.relname,
            'kind', relation.relkind::text,
            'owner', pg_get_userbyid(relation.relowner)
        ) ORDER BY namespace.nspname, relation.relname)
        FROM pg_class relation
        INNER JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
        WHERE namespace.nspname NOT IN ('pg_catalog', 'information_schema')
          AND namespace.nspname NOT LIKE 'pg_toast%'
          AND relation.relkind IN ('r', 'p', 'm', 'v', 'f')
    ), '[]'::json)
)::text;
"@
    $raw = Invoke-RecoveryFixturePsql -FixtureDatabaseDsn $FixtureDatabaseDsn -Query $query
    try
    { $state = $raw | ConvertFrom-Json -Depth 16
    } catch
    { throw 'fixture database state readback is malformed'
    }
    Assert-RecoveryExactProperties -Object $state -Names @('identity', 'relations') -Label 'fixture database state'
    Assert-RecoveryExactProperties -Object $state.identity -Names @('cluster_identifier', 'database', 'database_oid', 'owner') -Label 'fixture database identity'
    if ($state.identity.cluster_identifier -isnot [string] -or $state.identity.database -isnot [string] -or
        $state.identity.database_oid -isnot [string] -or $state.identity.owner -isnot [string] -or
        [string]::IsNullOrWhiteSpace($state.identity.database) -or [string]::IsNullOrWhiteSpace($state.identity.owner) -or
        $state.identity.cluster_identifier -cnotmatch '^[0-9]+$' -or $state.identity.database_oid -cnotmatch '^[1-9][0-9]*$')
    {
        throw 'fixture database identity is malformed'
    }
    if ($state.identity.database -cne 'engram_fixture')
    {
        throw 'fixture database current database is not engram_fixture'
    }
    $relations = @($state.relations)
    foreach ($relation in $relations)
    {
        Assert-RecoveryExactProperties -Object $relation -Names @('schema', 'name', 'kind', 'owner') -Label 'fixture database relation'
        if ($relation.schema -isnot [string] -or $relation.name -isnot [string] -or $relation.kind -isnot [string] -or $relation.owner -isnot [string])
        { throw 'fixture database relation is malformed'
        }
    }
    $identity = [pscustomobject]@{
        ClusterIdentifier = [string]$state.identity.cluster_identifier
        Database = [string]$state.identity.database
        DatabaseOid = [string]$state.identity.database_oid
        Owner = [string]$state.identity.owner
    }
    $identity | Add-Member -NotePropertyName Fingerprint -NotePropertyValue (Get-RecoveryStringSha256 -Text ($identity.ClusterIdentifier + "`n" + $identity.DatabaseOid + "`n" + $identity.Database))
    [pscustomobject]@{ Identity = $identity; Relations = $relations }
}

function Assert-RecoveryFixtureLiveDatabaseName
{
    param([Parameter(Mandatory)][string]$FixtureDatabaseDsn)

    if ((Invoke-RecoveryFixturePsql -FixtureDatabaseDsn $FixtureDatabaseDsn -Query 'SELECT current_database();') -cne 'engram_fixture')
    {
        throw 'fixture database current database is not engram_fixture'
    }
}

function Assert-RecoveryFixtureDatabaseMutationTarget
{
    param(
        [Parameter(Mandatory)]$Context,
        [Parameter(Mandatory)][string]$FixtureDatabaseDsn,
        [switch]$RequireInactiveBinding
    )

    $state = Get-RecoveryFixtureDatabaseState -FixtureDatabaseDsn $FixtureDatabaseDsn
    if ($state.Relations.Count -eq 0)
    { return $state
    }
    if ($state.Relations.Count -ne 1 -or $state.Relations[0].schema -cne 'public' -or
        $state.Relations[0].name -cne $script:RecoveryFixtureDatabaseTable -or $state.Relations[0].kind -cne 'r' -or
        $state.Relations[0].owner -cne $state.Identity.Owner)
    {
        throw 'fixture database contains unrelated application relations'
    }
    $binding = Get-RecoveryFixtureDatabaseBinding -FixtureDatabaseDsn $FixtureDatabaseDsn
    if ($null -eq $binding -or (Invoke-RecoveryFixturePsql -FixtureDatabaseDsn $FixtureDatabaseDsn -Query "SELECT count(*)::text FROM public.$script:RecoveryFixtureDatabaseTable;") -cne '1')
    {
        throw 'fixture database ownership state is not a verified prior fixture run'
    }
    Assert-RecoveryFixtureDatabaseBindingShape -Binding $binding
    if ($binding.fixture_root -cne $Context.RelativeRoot -or $binding.database_identity_fingerprint -cne $state.Identity.Fingerprint)
    {
        throw 'fixture database ownership state is foreign'
    }
    if ($RequireInactiveBinding -and $binding.server_state -cin @('launching', 'starting', 'running'))
    {
        if (Test-RecoveryProcessIdentity -ProcessId ([int]$binding.owner_process_id) -StartUtcTicks ([int64]$binding.owner_process_start_utc_ticks))
    {
        throw 'fixture database ownership is held by an active fixture server'
        }
        Assert-RecoveryFixturePortUnbound -Port ([int]$binding.port)
    }
    $state
}

function Ensure-RecoveryFixtureDatabaseOwnershipTable
{
    param([Parameter(Mandatory)][string]$FixtureDatabaseDsn)

  $query = @"
CREATE TABLE IF NOT EXISTS public.engram_recovery_fixture_ownership (
    fixture_id text PRIMARY KEY,
    schema_version text NOT NULL,
    fixture_root text NOT NULL,
    run_id text NOT NULL,
    manifest_fingerprint text NOT NULL,
    database_identity_fingerprint text NOT NULL,
    server_state text NOT NULL,
    owner_process_id integer NULL,
    owner_process_start_utc_ticks bigint NULL,
    server_fingerprint text NULL,
    source_commit text NULL,
    port integer NULL,
    updated_at_utc timestamptz NOT NULL
);
"@
    Assert-RecoveryFixtureLiveDatabaseName -FixtureDatabaseDsn $FixtureDatabaseDsn
    [void](Invoke-RecoveryFixturePsql -FixtureDatabaseDsn $FixtureDatabaseDsn -Query $query)
}

function Get-RecoveryFixtureDatabaseIdentity
{
    param([Parameter(Mandatory)][string]$FixtureDatabaseDsn)

    (Get-RecoveryFixtureDatabaseState -FixtureDatabaseDsn $FixtureDatabaseDsn).Identity
}

function Get-RecoveryFixtureDatabaseBinding
{
    param([Parameter(Mandatory)][string]$FixtureDatabaseDsn)

    $exists = Invoke-RecoveryFixturePsql -FixtureDatabaseDsn $FixtureDatabaseDsn -Query "SELECT CASE WHEN to_regclass('public.engram_recovery_fixture_ownership') IS NULL THEN '0' ELSE '1' END;"
    if ($exists -cne '1')
    { return $null
    }
  $query = @"
SELECT coalesce((
    SELECT json_build_object(
        'schema_version', schema_version,
        'fixture_id', fixture_id,
        'fixture_root', fixture_root,
        'run_id', run_id,
        'manifest_fingerprint', manifest_fingerprint,
        'database_identity_fingerprint', database_identity_fingerprint,
        'server_state', server_state,
        'owner_process_id', owner_process_id,
        'owner_process_start_utc_ticks', owner_process_start_utc_ticks,
        'server_fingerprint', server_fingerprint,
        'source_commit', source_commit,
        'port', port
    )::text
    FROM public.engram_recovery_fixture_ownership
    WHERE fixture_id = :'fixture_id'
), '');
"@
    $raw = Invoke-RecoveryFixturePsql -FixtureDatabaseDsn $FixtureDatabaseDsn -Query $query -Variables @{ fixture_id = $script:RecoveryFixtureID }
    if ([string]::IsNullOrWhiteSpace($raw))
    { return $null
    }
    try
    { $raw | ConvertFrom-Json -Depth 16
    } catch
    { throw 'fixture database ownership binding is malformed'
    }
}

function Assert-RecoveryFixtureDatabaseBindingShape
{
    param([Parameter(Mandatory)]$Binding)

    Assert-RecoveryExactProperties -Object $Binding -Names @('schema_version', 'fixture_id', 'fixture_root', 'run_id', 'manifest_fingerprint', 'database_identity_fingerprint', 'server_state', 'owner_process_id', 'owner_process_start_utc_ticks', 'server_fingerprint', 'source_commit', 'port') -Label 'fixture database ownership binding'
    foreach ($name in @('schema_version', 'fixture_id', 'fixture_root', 'run_id', 'manifest_fingerprint', 'database_identity_fingerprint', 'server_state'))
    {
        if ($Binding.$name -isnot [string])
        { throw 'fixture database ownership binding is malformed'
        }
    }
    if ($Binding.schema_version -cne $script:RecoveryFixtureDatabaseSchema -or $Binding.fixture_id -cne $script:RecoveryFixtureID -or
        $Binding.run_id -cnotmatch '^[0-9a-f]{32}$' -or $Binding.manifest_fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or
        $Binding.database_identity_fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or $Binding.server_state -cnotin @('prepared', 'launching', 'starting', 'running'))
    {
        throw 'fixture database ownership binding is foreign'
    }
    if ($Binding.server_state -ceq 'prepared')
    {
        if ($null -ne $Binding.owner_process_id -or $null -ne $Binding.owner_process_start_utc_ticks -or $null -ne $Binding.server_fingerprint -or $null -ne $Binding.source_commit -or $null -ne $Binding.port)
        { throw 'fixture database prepared binding is malformed'
        }
        return
    }
    if ((($Binding.owner_process_id -isnot [int]) -and ($Binding.owner_process_id -isnot [long])) -or
        (($Binding.owner_process_start_utc_ticks -isnot [int]) -and ($Binding.owner_process_start_utc_ticks -isnot [long])) -or
        (($Binding.port -isnot [int]) -and ($Binding.port -isnot [long])) -or $Binding.server_fingerprint -isnot [string] -or $Binding.source_commit -isnot [string] -or
        $Binding.owner_process_id -lt 1 -or $Binding.owner_process_start_utc_ticks -lt 1 -or $Binding.port -lt 1024 -or $Binding.port -gt 65535 -or
        $Binding.server_fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or $Binding.source_commit -cnotmatch '^[0-9a-f]{40}$')
    {
        throw 'fixture database active binding is malformed'
    }
}

function Get-RecoveryFixtureManifestDatabaseIdentity
{
    param([Parameter(Mandatory)]$Context, [Parameter(Mandatory)]$Manifest, [Parameter(Mandatory)][string]$FixtureDatabaseDsn)

    $owner = Assert-RecoveryFixtureOwner -Context $Context
    if ($Manifest.run_id -isnot [string] -or $Manifest.run_id -cne $owner.run_id)
    { throw 'fixture manifest is not bound to the current fixture run'
    }
    Assert-RecoveryExactProperties -Object $Manifest.database -Names @('schema_version', 'identity', 'identity_fingerprint') -Label 'fixture manifest database binding'
    Assert-RecoveryExactProperties -Object $Manifest.database.identity -Names @('cluster_identifier', 'database', 'database_oid') -Label 'fixture manifest database identity'
    if ($Manifest.database.schema_version -isnot [string] -or $Manifest.database.identity_fingerprint -isnot [string] -or
        $Manifest.database.identity.cluster_identifier -isnot [string] -or $Manifest.database.identity.database -isnot [string] -or
        $Manifest.database.identity.database_oid -isnot [string] -or $Manifest.database.schema_version -cne $script:RecoveryFixtureDatabaseSchema -or
        $Manifest.database.identity_fingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or
        $Manifest.database.identity.cluster_identifier -cnotmatch '^[0-9]+$' -or
        $Manifest.database.identity.database_oid -cnotmatch '^[1-9][0-9]*$' -or [string]::IsNullOrWhiteSpace($Manifest.database.identity.database))
    {
        throw 'fixture manifest database binding is malformed'
    }
    $identity = Get-RecoveryFixtureDatabaseIdentity -FixtureDatabaseDsn $FixtureDatabaseDsn
    if ($identity.ClusterIdentifier -cne $Manifest.database.identity.cluster_identifier -or $identity.Database -cne $Manifest.database.identity.database -or
        $identity.DatabaseOid -cne $Manifest.database.identity.database_oid -or $identity.Fingerprint -cne $Manifest.database.identity_fingerprint)
    { throw 'fixture manifest database identity does not match the live fixture database'
    }
    [pscustomobject]@{ Owner = $owner; Identity = $identity }
}

function Assert-RecoveryFixtureDatabaseBinding
{
    param(
        [Parameter(Mandatory)]$Context,
        [Parameter(Mandatory)]$Manifest,
        [Parameter(Mandatory)][string]$ManifestPath,
        [Parameter(Mandatory)][string]$FixtureDatabaseDsn
    )

    $fixture = Get-RecoveryFixtureManifestDatabaseIdentity -Context $Context -Manifest $Manifest -FixtureDatabaseDsn $FixtureDatabaseDsn
    $binding = Get-RecoveryFixtureDatabaseBinding -FixtureDatabaseDsn $FixtureDatabaseDsn
    if ($null -eq $binding)
    { throw 'fixture database ownership binding is missing'
    }
    Assert-RecoveryFixtureDatabaseBindingShape -Binding $binding
    if ($binding.fixture_root -cne $Context.RelativeRoot -or $binding.run_id -cne $fixture.Owner.run_id -or
        $binding.manifest_fingerprint -cne (Get-RecoverySha256 -Path $ManifestPath) -or $binding.database_identity_fingerprint -cne $fixture.Identity.Fingerprint)
    {
        throw 'fixture database ownership binding does not match the current fixture manifest'
    }
    $binding
}

function Assert-RecoveryFixtureDatabaseRunInactive
{
    param([Parameter(Mandatory)]$Context, [Parameter(Mandatory)]$Owner, [Parameter(Mandatory)][string]$FixtureDatabaseDsn)

    $binding = Get-RecoveryFixtureDatabaseBinding -FixtureDatabaseDsn $FixtureDatabaseDsn
    if ($null -eq $binding)
    { return
    }
    Assert-RecoveryFixtureDatabaseBindingShape -Binding $binding
    if ($binding.server_state -cin @('launching', 'starting', 'running'))
    {
        if (Test-RecoveryProcessIdentity -ProcessId ([int]$binding.owner_process_id) -StartUtcTicks ([int64]$binding.owner_process_start_utc_ticks))
    {
        throw 'fixture reset refused while the database binding reports an active fixture server'
        }
        Assert-RecoveryFixturePortUnbound -Port ([int]$binding.port)
    }
}

function Set-RecoveryFixtureDatabaseBinding
{
    param(
        [Parameter(Mandatory)]$Context,
        [Parameter(Mandatory)]$Manifest,
        [Parameter(Mandatory)][string]$ManifestPath,
        [Parameter(Mandatory)][string]$FixtureDatabaseDsn
    )

    [void](Assert-RecoveryFixtureDatabaseMutationTarget -Context $Context -FixtureDatabaseDsn $FixtureDatabaseDsn -RequireInactiveBinding)
    Ensure-RecoveryFixtureDatabaseOwnershipTable -FixtureDatabaseDsn $FixtureDatabaseDsn
    $fixture = Get-RecoveryFixtureManifestDatabaseIdentity -Context $Context -Manifest $Manifest -FixtureDatabaseDsn $FixtureDatabaseDsn
    $manifestFingerprint = Get-RecoverySha256 -Path $ManifestPath
    $previous = Get-RecoveryFixtureDatabaseBinding -FixtureDatabaseDsn $FixtureDatabaseDsn
    if ($null -ne $previous)
    {
        Assert-RecoveryFixtureDatabaseBindingShape -Binding $previous
        if ($previous.server_state -cin @('launching', 'starting', 'running'))
        {
            if (Test-RecoveryProcessIdentity -ProcessId ([int]$previous.owner_process_id) -StartUtcTicks ([int64]$previous.owner_process_start_utc_ticks))
        {
            throw 'fixture database ownership is held by an active fixture server'
            }
            Assert-RecoveryFixturePortUnbound -Port ([int]$previous.port)
        }
    }
    $variables = @{
        fixture_id = $script:RecoveryFixtureID
        schema_version = $script:RecoveryFixtureDatabaseSchema
        fixture_root = $Context.RelativeRoot
        run_id = $fixture.Owner.run_id
        manifest_fingerprint = $manifestFingerprint
        database_identity_fingerprint = $fixture.Identity.Fingerprint
    }
    if ($null -eq $previous)
    {
    $query = @"
WITH inserted AS (
    INSERT INTO public.engram_recovery_fixture_ownership (fixture_id, schema_version, fixture_root, run_id, manifest_fingerprint, database_identity_fingerprint, server_state, updated_at_utc)
    VALUES (:'fixture_id', :'schema_version', :'fixture_root', :'run_id', :'manifest_fingerprint', :'database_identity_fingerprint', 'prepared', CURRENT_TIMESTAMP)
    ON CONFLICT (fixture_id) DO NOTHING
    RETURNING fixture_id
)
SELECT CASE WHEN EXISTS (SELECT 1 FROM inserted) THEN 'bound' ELSE 'conflict' END;
"@
    } else
    {
        $variables.previous_run_id = [string]$previous.run_id
        $variables.previous_server_state = [string]$previous.server_state
    $query = @"
WITH rebound AS (
    UPDATE public.engram_recovery_fixture_ownership
    SET schema_version = :'schema_version', fixture_root = :'fixture_root', run_id = :'run_id', manifest_fingerprint = :'manifest_fingerprint', database_identity_fingerprint = :'database_identity_fingerprint', server_state = 'prepared', owner_process_id = NULL, owner_process_start_utc_ticks = NULL, server_fingerprint = NULL, source_commit = NULL, port = NULL, updated_at_utc = CURRENT_TIMESTAMP
    WHERE fixture_id = :'fixture_id' AND run_id = :'previous_run_id' AND server_state = :'previous_server_state'
    RETURNING fixture_id
)
SELECT CASE WHEN EXISTS (SELECT 1 FROM rebound) THEN 'bound' ELSE 'conflict' END;
"@
    }
    Assert-RecoveryFixtureLiveDatabaseName -FixtureDatabaseDsn $FixtureDatabaseDsn
    if ((Invoke-RecoveryFixturePsql -FixtureDatabaseDsn $FixtureDatabaseDsn -Query $query -Variables $variables) -cne 'bound')
    { throw 'fixture database ownership changed concurrently'
    }
    Assert-RecoveryFixtureDatabaseBinding -Context $Context -Manifest $Manifest -ManifestPath $ManifestPath -FixtureDatabaseDsn $FixtureDatabaseDsn
}

function Claim-RecoveryFixtureDatabaseServerRun
{
    param(
        [Parameter(Mandatory)]$Context,
        [Parameter(Mandatory)]$Manifest,
        [Parameter(Mandatory)][string]$ManifestPath,
        [Parameter(Mandatory)][string]$FixtureDatabaseDsn,
        [Parameter(Mandatory)][string]$ServerFingerprint,
        [Parameter(Mandatory)][string]$SourceCommit,
        [Parameter(Mandatory)][int]$Port
    )

    $binding = Assert-RecoveryFixtureDatabaseBinding -Context $Context -Manifest $Manifest -ManifestPath $ManifestPath -FixtureDatabaseDsn $FixtureDatabaseDsn
    if ($binding.server_state -cne 'prepared' -or $ServerFingerprint -cnotmatch '^sha256:[0-9a-f]{64}$' -or $SourceCommit -cnotmatch '^[0-9a-f]{40}$')
    { throw 'fixture database is not available for a new owned server run'
    }
    $launcher = Get-RecoveryCurrentProcessIdentity
  $query = @"
WITH claimed AS (
    UPDATE public.engram_recovery_fixture_ownership
    SET server_state = 'launching', owner_process_id = :'owner_process_id'::integer, owner_process_start_utc_ticks = :'owner_process_start_utc_ticks'::bigint, server_fingerprint = :'server_fingerprint', source_commit = :'source_commit', port = :'port'::integer, updated_at_utc = CURRENT_TIMESTAMP
    WHERE fixture_id = :'fixture_id' AND run_id = :'run_id' AND manifest_fingerprint = :'manifest_fingerprint' AND database_identity_fingerprint = :'database_identity_fingerprint' AND server_state = 'prepared'
    RETURNING fixture_id
)
SELECT CASE WHEN EXISTS (SELECT 1 FROM claimed) THEN 'claimed' ELSE 'conflict' END;
"@
    Assert-RecoveryFixtureLiveDatabaseName -FixtureDatabaseDsn $FixtureDatabaseDsn
    $result = Invoke-RecoveryFixturePsql -FixtureDatabaseDsn $FixtureDatabaseDsn -Query $query -Variables @{
        fixture_id = $script:RecoveryFixtureID
        run_id = [string]$binding.run_id
        manifest_fingerprint = [string]$binding.manifest_fingerprint
        database_identity_fingerprint = [string]$binding.database_identity_fingerprint
        owner_process_id = [string]$launcher.ProcessId
        owner_process_start_utc_ticks = [string]$launcher.StartUtcTicks
        server_fingerprint = $ServerFingerprint
        source_commit = $SourceCommit
        port = [string]$Port
    }
    if ($result -cne 'claimed')
    { throw 'fixture database server run was claimed concurrently'
    }
    [pscustomobject]@{ Binding = $binding; Launcher = $launcher; ServerFingerprint = $ServerFingerprint; SourceCommit = $SourceCommit; Port = $Port }
}

function Arm-RecoveryFixtureDatabaseServerRun
{
    param(
        [Parameter(Mandatory)]$Context,
        [Parameter(Mandatory)]$Claim,
        [Parameter(Mandatory)]$ServerMarker,
        [Parameter(Mandatory)][string]$FixtureDatabaseDsn
    )

    [void](Assert-RecoveryFixtureServerMarker -Context $Context -Marker $ServerMarker)
    $query = @"
WITH armed AS (
    UPDATE public.engram_recovery_fixture_ownership
    SET server_state = 'starting', owner_process_id = :'server_process_id'::integer, owner_process_start_utc_ticks = :'server_process_start_utc_ticks'::bigint, updated_at_utc = CURRENT_TIMESTAMP
    WHERE fixture_id = :'fixture_id' AND schema_version = :'schema_version' AND fixture_root = :'fixture_root' AND run_id = :'run_id' AND manifest_fingerprint = :'manifest_fingerprint' AND database_identity_fingerprint = :'database_identity_fingerprint' AND server_state = 'launching' AND owner_process_id = :'launcher_process_id'::integer AND owner_process_start_utc_ticks = :'launcher_process_start_utc_ticks'::bigint AND server_fingerprint = :'server_fingerprint' AND source_commit = :'source_commit' AND port = :'port'::integer
    RETURNING fixture_id
)
SELECT CASE WHEN EXISTS (SELECT 1 FROM armed) THEN 'armed' ELSE 'conflict' END;
"@
    Assert-RecoveryFixtureLiveDatabaseName -FixtureDatabaseDsn $FixtureDatabaseDsn
    $result = Invoke-RecoveryFixturePsql -FixtureDatabaseDsn $FixtureDatabaseDsn -Query $query -Variables @{
        fixture_id = $script:RecoveryFixtureID
        schema_version = [string]$Claim.Binding.schema_version
        fixture_root = [string]$Claim.Binding.fixture_root
        run_id = [string]$ServerMarker.run_id
        manifest_fingerprint = [string]$ServerMarker.manifest_fingerprint
        database_identity_fingerprint = [string]$ServerMarker.database_identity_fingerprint
        launcher_process_id = [string]$Claim.Launcher.ProcessId
        launcher_process_start_utc_ticks = [string]$Claim.Launcher.StartUtcTicks
        server_process_id = [string]$ServerMarker.process_id
        server_process_start_utc_ticks = [string]$ServerMarker.process_start_utc_ticks
        server_fingerprint = [string]$ServerMarker.server_fingerprint
        source_commit = [string]$ServerMarker.source_commit
        port = [string]$ServerMarker.port
    }
    if ($result -cne 'armed')
    { throw 'fixture database server ownership could not be armed'
    }
}

function Confirm-RecoveryFixtureDatabaseServerRun
{
    param(
        [Parameter(Mandatory)]$Context,
        [Parameter(Mandatory)]$Claim,
        [Parameter(Mandatory)]$ServerMarker,
        [Parameter(Mandatory)][string]$FixtureDatabaseDsn
    )

    [void](Assert-RecoveryFixtureServerMarker -Context $Context -Marker $ServerMarker)
    $query = @"
WITH confirmed AS (
    UPDATE public.engram_recovery_fixture_ownership
    SET server_state = 'running', updated_at_utc = CURRENT_TIMESTAMP
    WHERE fixture_id = :'fixture_id' AND schema_version = :'schema_version' AND fixture_root = :'fixture_root' AND run_id = :'run_id' AND manifest_fingerprint = :'manifest_fingerprint' AND database_identity_fingerprint = :'database_identity_fingerprint' AND server_state = 'starting' AND owner_process_id = :'server_process_id'::integer AND owner_process_start_utc_ticks = :'server_process_start_utc_ticks'::bigint AND server_fingerprint = :'server_fingerprint' AND source_commit = :'source_commit' AND port = :'port'::integer
    RETURNING fixture_id
)
SELECT CASE WHEN EXISTS (SELECT 1 FROM confirmed) THEN 'confirmed' ELSE 'conflict' END;
"@
    Assert-RecoveryFixtureLiveDatabaseName -FixtureDatabaseDsn $FixtureDatabaseDsn
    $result = Invoke-RecoveryFixturePsql -FixtureDatabaseDsn $FixtureDatabaseDsn -Query $query -Variables @{
        fixture_id = $script:RecoveryFixtureID
        schema_version = [string]$Claim.Binding.schema_version
        fixture_root = [string]$Claim.Binding.fixture_root
        run_id = [string]$ServerMarker.run_id
        manifest_fingerprint = [string]$ServerMarker.manifest_fingerprint
        database_identity_fingerprint = [string]$ServerMarker.database_identity_fingerprint
        server_process_id = [string]$ServerMarker.process_id
        server_process_start_utc_ticks = [string]$ServerMarker.process_start_utc_ticks
        server_fingerprint = [string]$ServerMarker.server_fingerprint
        source_commit = [string]$ServerMarker.source_commit
        port = [string]$ServerMarker.port
    }
    if ($result -cne 'confirmed')
    { throw 'fixture database server ownership could not be confirmed'
    }
}

function Release-RecoveryFixtureDatabaseServerRun
{
    param([Parameter(Mandatory)]$Context, [Parameter(Mandatory)]$Claim, [Parameter(Mandatory)][string]$FixtureDatabaseDsn)

    $query = @"
WITH released AS (
    UPDATE public.engram_recovery_fixture_ownership
    SET server_state = 'prepared', owner_process_id = NULL, owner_process_start_utc_ticks = NULL, server_fingerprint = NULL, source_commit = NULL, port = NULL, updated_at_utc = CURRENT_TIMESTAMP
    WHERE fixture_id = :'fixture_id' AND schema_version = :'schema_version' AND fixture_root = :'fixture_root' AND run_id = :'run_id' AND manifest_fingerprint = :'manifest_fingerprint' AND database_identity_fingerprint = :'database_identity_fingerprint' AND server_state = 'launching' AND owner_process_id = :'launcher_process_id'::integer AND owner_process_start_utc_ticks = :'launcher_process_start_utc_ticks'::bigint AND server_fingerprint = :'server_fingerprint' AND source_commit = :'source_commit' AND port = :'port'::integer
    RETURNING fixture_id
)
SELECT CASE WHEN EXISTS (SELECT 1 FROM released) THEN 'released' ELSE 'conflict' END;
"@
    Assert-RecoveryFixtureLiveDatabaseName -FixtureDatabaseDsn $FixtureDatabaseDsn
    $result = Invoke-RecoveryFixturePsql -FixtureDatabaseDsn $FixtureDatabaseDsn -Query $query -Variables @{
        fixture_id = $script:RecoveryFixtureID
        schema_version = [string]$Claim.Binding.schema_version
        fixture_root = [string]$Claim.Binding.fixture_root
        run_id = [string]$Claim.Binding.run_id
        manifest_fingerprint = [string]$Claim.Binding.manifest_fingerprint
        database_identity_fingerprint = [string]$Claim.Binding.database_identity_fingerprint
        launcher_process_id = [string]$Claim.Launcher.ProcessId
        launcher_process_start_utc_ticks = [string]$Claim.Launcher.StartUtcTicks
        server_fingerprint = [string]$Claim.ServerFingerprint
        source_commit = [string]$Claim.SourceCommit
        port = [string]$Claim.Port
    }
    if ($result -cne 'released')
    { throw 'fixture database server ownership could not be released'
    }
}

function Reset-RecoveryFixtureDatabaseServerRun
{
    param([Parameter(Mandatory)]$Context, [Parameter(Mandatory)]$ServerMarker, [Parameter(Mandatory)][string]$FixtureDatabaseDsn)

    [void](Assert-RecoveryFixtureServerMarker -Context $Context -Marker $ServerMarker)
    Assert-RecoveryFixturePortUnbound -Port ([int]$ServerMarker.port)
    $query = @"
WITH released AS (
    UPDATE public.engram_recovery_fixture_ownership
    SET server_state = 'prepared', owner_process_id = NULL, owner_process_start_utc_ticks = NULL, server_fingerprint = NULL, source_commit = NULL, port = NULL, updated_at_utc = CURRENT_TIMESTAMP
    WHERE fixture_id = :'fixture_id' AND schema_version = :'schema_version' AND fixture_root = :'fixture_root' AND run_id = :'run_id' AND manifest_fingerprint = :'manifest_fingerprint' AND database_identity_fingerprint = :'database_identity_fingerprint' AND server_state IN ('starting', 'running') AND owner_process_id = :'server_process_id'::integer AND owner_process_start_utc_ticks = :'server_process_start_utc_ticks'::bigint AND server_fingerprint = :'server_fingerprint' AND source_commit = :'source_commit' AND port = :'port'::integer
    RETURNING fixture_id
)
SELECT CASE WHEN EXISTS (SELECT 1 FROM released) THEN 'released' ELSE 'conflict' END;
"@
    Assert-RecoveryFixtureLiveDatabaseName -FixtureDatabaseDsn $FixtureDatabaseDsn
    $result = Invoke-RecoveryFixturePsql -FixtureDatabaseDsn $FixtureDatabaseDsn -Query $query -Variables @{
        fixture_id = $script:RecoveryFixtureID
        schema_version = $script:RecoveryFixtureDatabaseSchema
        fixture_root = $Context.RelativeRoot
        run_id = [string]$ServerMarker.run_id
        manifest_fingerprint = [string]$ServerMarker.manifest_fingerprint
        database_identity_fingerprint = [string]$ServerMarker.database_identity_fingerprint
        server_process_id = [string]$ServerMarker.process_id
        server_process_start_utc_ticks = [string]$ServerMarker.process_start_utc_ticks
        server_fingerprint = [string]$ServerMarker.server_fingerprint
        source_commit = [string]$ServerMarker.source_commit
        port = [string]$ServerMarker.port
    }
    if ($result -cne 'released')
    { throw 'fixture database server ownership could not be reset'
    }
}

function Reset-RecoveryFixtureDatabaseBindingToPrepared
{
    param(
        [Parameter(Mandatory)]$Binding,
        [Parameter(Mandatory)][ValidateSet('launching', 'starting')][string]$ExpectedState,
        [Parameter(Mandatory)][string]$FixtureDatabaseDsn
    )

    $query = @"
WITH recovered AS (
    UPDATE public.engram_recovery_fixture_ownership
    SET server_state = 'prepared', owner_process_id = NULL, owner_process_start_utc_ticks = NULL, server_fingerprint = NULL, source_commit = NULL, port = NULL, updated_at_utc = CURRENT_TIMESTAMP
    WHERE fixture_id = :'fixture_id' AND schema_version = :'schema_version' AND fixture_root = :'fixture_root' AND run_id = :'run_id' AND manifest_fingerprint = :'manifest_fingerprint' AND database_identity_fingerprint = :'database_identity_fingerprint' AND server_state = :'server_state' AND owner_process_id = :'owner_process_id'::integer AND owner_process_start_utc_ticks = :'owner_process_start_utc_ticks'::bigint AND server_fingerprint = :'server_fingerprint' AND source_commit = :'source_commit' AND port = :'port'::integer
    RETURNING fixture_id
)
SELECT CASE WHEN EXISTS (SELECT 1 FROM recovered) THEN 'recovered' ELSE 'conflict' END;
"@
    Assert-RecoveryFixturePortUnbound -Port ([int]$Binding.port)
    Assert-RecoveryFixtureLiveDatabaseName -FixtureDatabaseDsn $FixtureDatabaseDsn
    $result = Invoke-RecoveryFixturePsql -FixtureDatabaseDsn $FixtureDatabaseDsn -Query $query -Variables @{
        fixture_id = $script:RecoveryFixtureID
        schema_version = [string]$Binding.schema_version
        fixture_root = [string]$Binding.fixture_root
        run_id = [string]$Binding.run_id
        manifest_fingerprint = [string]$Binding.manifest_fingerprint
        database_identity_fingerprint = [string]$Binding.database_identity_fingerprint
        server_state = $ExpectedState
        owner_process_id = [string]$Binding.owner_process_id
        owner_process_start_utc_ticks = [string]$Binding.owner_process_start_utc_ticks
        server_fingerprint = [string]$Binding.server_fingerprint
        source_commit = [string]$Binding.source_commit
        port = [string]$Binding.port
    }
    if ($result -cne 'recovered')
    { throw "fixture database $ExpectedState claim changed concurrently"
    }
}

function Reset-RecoveryFixtureStaleDatabaseLauncherClaim
{
    param(
        [Parameter(Mandatory)]$Context,
        [Parameter(Mandatory)]$Manifest,
        [Parameter(Mandatory)][string]$ManifestPath,
        [Parameter(Mandatory)][string]$FixtureDatabaseDsn
    )

    $binding = Assert-RecoveryFixtureDatabaseBinding -Context $Context -Manifest $Manifest -ManifestPath $ManifestPath -FixtureDatabaseDsn $FixtureDatabaseDsn
    if ($binding.server_state -cne 'launching')
    { throw 'fixture database binding is not a recoverable launching claim'
    }
    Assert-RecoveryProcessIdentityAbsent -ProcessId ([int]$binding.owner_process_id) -StartUtcTicks ([int64]$binding.owner_process_start_utc_ticks) -Label 'fixture launcher process'
    Assert-RecoveryFixturePortUnbound -Port ([int]$binding.port)
    Reset-RecoveryFixtureDatabaseBindingToPrepared -Binding $binding -ExpectedState 'launching' -FixtureDatabaseDsn $FixtureDatabaseDsn
}

function Reset-RecoveryFixtureStaleDatabaseStartingServerRun
{
    param(
        [Parameter(Mandatory)]$Context,
        [Parameter(Mandatory)]$Manifest,
        [Parameter(Mandatory)][string]$ManifestPath,
        [Parameter(Mandatory)][string]$FixtureDatabaseDsn
    )

    $binding = Assert-RecoveryFixtureDatabaseBinding -Context $Context -Manifest $Manifest -ManifestPath $ManifestPath -FixtureDatabaseDsn $FixtureDatabaseDsn
    if ($binding.server_state -cne 'starting')
    { throw 'fixture database binding is not a recoverable starting child run'
    }
    Stop-RecoveryFixtureExactProcessAndAwaitAbsence -ProcessId ([int]$binding.owner_process_id) -StartUtcTicks ([int64]$binding.owner_process_start_utc_ticks) -Label 'fixture server process'
    Assert-RecoveryFixturePortUnbound -Port ([int]$binding.port)
    Reset-RecoveryFixtureDatabaseBindingToPrepared -Binding $binding -ExpectedState 'starting' -FixtureDatabaseDsn $FixtureDatabaseDsn
}

function Get-RecoveryFixtureListenerProcessId
{
    param([Parameter(Mandatory)][int]$Port)

    if ($IsWindows)
    {
        $connections = @(Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction Stop | Where-Object { $_.LocalAddress -cin @('127.0.0.1', '::1') })
        $processIds = @($connections | ForEach-Object { [int]$_.OwningProcess } | Sort-Object -Unique)
    } else
    {
        $lsof = Get-Command -Name 'lsof' -CommandType Application -ErrorAction SilentlyContinue
        if ($null -eq $lsof)
        { throw 'fixture process ownership prerequisite is missing: lsof must be available on non-Windows hosts'
        }
        $lines = @(& $lsof.Source '-nP' "-iTCP:$Port" '-sTCP:LISTEN' '-t')
        if ($LASTEXITCODE -ne 0)
        { throw 'fixture listener ownership could not be determined'
        }
        $processIds = @($lines | ForEach-Object {
                $value = ([string]$_).Trim()
                if ($value -cnotmatch '^[0-9]+$')
                { throw 'fixture listener ownership output is malformed'
                }
                [int]$value
            } | Sort-Object -Unique)
    }
    if ($processIds.Count -ne 1)
    { throw 'fixture listener ownership is ambiguous'
    }
    $processIds[0]
}

function Assert-RecoveryFixturePortUnbound
{
    param([Parameter(Mandatory)][int]$Port)

    if ($Port -lt 1024 -or $Port -gt 65535)
    { throw 'fixture listener port is malformed'
    }
    if ($IsWindows)
    {
        try
        { $listeners = @(Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction Stop)
        } catch
        { throw 'fixture listener state cannot be determined'
        }
        if ($listeners.Count -ne 0)
        { throw 'fixture server port is actively listening'
        }
        return
    }
    $lsof = Get-Command -Name 'lsof' -CommandType Application -ErrorAction SilentlyContinue
    if ($null -eq $lsof)
    { throw 'fixture process ownership prerequisite is missing: lsof must be available on non-Windows hosts'
    }
    $lines = @(& $lsof.Source '-nP' "-iTCP:$Port" '-sTCP:LISTEN' '-t')
    $exitCode = $LASTEXITCODE
    if ($exitCode -eq 1 -and $lines.Count -eq 0)
    { return
    }
    if ($exitCode -ne 0)
    { throw 'fixture listener state cannot be determined'
    }
    $processIds = @($lines | ForEach-Object {
            $value = ([string]$_).Trim()
            if ($value -cnotmatch '^[0-9]+$')
            { throw 'fixture listener ownership output is malformed'
            }
            [int]$value
        } | Sort-Object -Unique)
    if ($processIds.Count -eq 0)
    { throw 'fixture listener state cannot be determined'
    }
    throw 'fixture server port is actively listening'
}

function Assert-RecoveryFixtureLiveServer
{
    param(
        [Parameter(Mandatory)]$Context,
        [Parameter(Mandatory)]$Manifest,
        [Parameter(Mandatory)][string]$ManifestPath,
        [Parameter(Mandatory)][string]$FixtureDatabaseDsn,
        [Parameter(Mandatory)]$ServerMarker,
        [Parameter(Mandatory)]$Health
    )

    [void](Assert-RecoveryFixtureServerMarker -Context $Context -Marker $ServerMarker)
    if (-not (Test-RecoveryProcessIdentity -ProcessId ([int]$ServerMarker.process_id) -StartUtcTicks ([int64]$ServerMarker.process_start_utc_ticks)))
    { throw 'fixture server process is no longer the marked owned process'
    }
    $listenerProcessId = Get-RecoveryFixtureListenerProcessId -Port ([int]$ServerMarker.port)
    if ($listenerProcessId -ne [int]$ServerMarker.process_id)
    { throw 'fixture server health endpoint is not owned by the marked fixture process'
    }
    if (-not (Test-RecoveryProcessIdentity -ProcessId ([int]$ServerMarker.process_id) -StartUtcTicks ([int64]$ServerMarker.process_start_utc_ticks)))
    { throw 'fixture server process changed after listener ownership validation'
    }
    $payloadPath = Join-Path (Join-Path $Context.FixtureRoot 'payload') ([string]$ServerMarker.staged_payload_filename)
    [void](Assert-RecoveryContainedPath -Path $payloadPath -RepositoryRoot $Context.RepositoryRoot)
    if (-not (Test-Path -LiteralPath $payloadPath -PathType Leaf))
    { throw 'staged fixture server payload is missing'
    }
    [void](Assert-RecoverySafeExistingPath -Path $payloadPath)
    if ((Get-RecoverySha256 -Path $payloadPath) -cne $ServerMarker.server_fingerprint -or (Get-RecoverySha256 -Path $ManifestPath) -cne $ServerMarker.manifest_fingerprint)
    { throw 'staged fixture server payload or manifest does not match the owned process marker'
    }
    if ($Health.status -isnot [string] -or $Health.version -isnot [string] -or $Health.source_commit -isnot [string] -or
        $Health.status -cne 'ready' -or [string]::IsNullOrWhiteSpace($Health.version) -or $Health.source_commit -cne $ServerMarker.source_commit)
    {
        throw 'live fixture health does not match the owned staged server provenance'
    }
    $binding = Assert-RecoveryFixtureDatabaseBinding -Context $Context -Manifest $Manifest -ManifestPath $ManifestPath -FixtureDatabaseDsn $FixtureDatabaseDsn
    if ($binding.server_state -cne 'running' -or $binding.owner_process_id -ne $ServerMarker.process_id -or
        $binding.owner_process_start_utc_ticks -ne $ServerMarker.process_start_utc_ticks -or $binding.server_fingerprint -cne $ServerMarker.server_fingerprint -or
        $binding.source_commit -cne $ServerMarker.source_commit -or $binding.port -ne $ServerMarker.port)
    {
        throw 'fixture database binding does not match the live owned server process'
    }
    if (-not (Test-RecoveryProcessIdentity -ProcessId ([int]$ServerMarker.process_id) -StartUtcTicks ([int64]$ServerMarker.process_start_utc_ticks)))
    { throw 'fixture server process changed before final live-server validation'
    }
    $listenerProcessId = Get-RecoveryFixtureListenerProcessId -Port ([int]$ServerMarker.port)
    if ($listenerProcessId -ne [int]$ServerMarker.process_id)
    { throw 'fixture server listener changed before final live-server validation'
    }
    if (-not (Test-RecoveryProcessIdentity -ProcessId ([int]$ServerMarker.process_id) -StartUtcTicks ([int64]$ServerMarker.process_start_utc_ticks)))
    { throw 'fixture server process changed after final listener validation'
    }
    [pscustomobject]@{ Binding = $binding; PayloadPath = $payloadPath }
}
