Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$script:RecoveryFixtureOwnerSchema = 'engram.recovery.fixture-owner.v1'
$script:RecoveryFixtureSchema = 'engram.recovery.fixture.v1'
$script:RecoveryFixtureOwner = 'engram-recovery-fixture'
$script:RecoveryFixtureID = 'synthetic-redacted-legacy'
$script:RecoveryFixtureScriptRoot = $PSScriptRoot

function Get-RecoveryRepositoryRoot {
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
    $prefix = $root + [IO.Path]::DirectorySeparatorChar
    if ($candidate -eq $root -or -not $candidate.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase))
    {
        throw 'fixture path must be a descendant of the repository root'
    }

    $relative = [IO.Path]::GetRelativePath($root, $candidate)
    $current = $root
    [void](Assert-RecoverySafeExistingPath -Path $current)
    foreach ($component in $relative -split '[\\/]')
    {
        if ([string]::IsNullOrWhiteSpace($component) -or $component -eq '.')
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
    $candidate = if ([IO.Path]::IsPathRooted($FixtureRoot)) {
        $FixtureRoot
    } else {
        Join-Path $repositoryRoot $FixtureRoot
    }
    $fixturePath = Assert-RecoveryContainedPath -Path $candidate -RepositoryRoot $repositoryRoot -LeafMayNotExist
    [pscustomobject]@{
        RepositoryRoot = $repositoryRoot
        FixtureRoot = $fixturePath
        RelativeRoot = [IO.Path]::GetRelativePath($repositoryRoot, $fixturePath).Replace('\', '/')
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
    Assert-RecoveryExactProperties -Object $marker -Names @('schema_version', 'owner', 'fixture_id', 'fixture_root') -Label 'fixture ownership marker'
    if ($marker.schema_version -isnot [string] -or $marker.owner -isnot [string] -or
        $marker.fixture_id -isnot [string] -or $marker.fixture_root -isnot [string]) {
        throw 'fixture ownership marker is malformed'
    }
    if ($marker.schema_version -cne $script:RecoveryFixtureOwnerSchema -or
        $marker.owner -cne $script:RecoveryFixtureOwner -or
        $marker.fixture_id -cne $script:RecoveryFixtureID -or
        $marker.fixture_root -cne $Context.RelativeRoot) {
        throw 'fixture ownership marker is foreign'
    }
    $marker
}

function New-RecoveryFixtureRoot
{
    param([Parameter(Mandatory)]$Context)

    $parent = Split-Path -Parent $Context.FixtureRoot
    if ($parent -ceq $Context.RepositoryRoot) {
        [void](Assert-RecoverySafeExistingPath -Path $parent)
    } else {
        [void](Assert-RecoveryContainedPath -Path $parent -RepositoryRoot $Context.RepositoryRoot -LeafMayNotExist)
    }
    if (Test-Path -LiteralPath $Context.FixtureRoot)
    {
        Assert-RecoveryFixtureTreeSafe -FixtureRoot $Context.FixtureRoot
        [void](Assert-RecoveryFixtureOwner -Context $Context)
        Remove-Item -LiteralPath $Context.FixtureRoot -Recurse -Force
    }

    New-Item -ItemType Directory -Path $Context.FixtureRoot -Force | Out-Null
    [void](Assert-RecoveryContainedPath -Path $Context.FixtureRoot -RepositoryRoot $Context.RepositoryRoot)
    [void](Assert-RecoverySafeExistingPath -Path $Context.FixtureRoot)

    $marker = [ordered]@{
        schema_version = $script:RecoveryFixtureOwnerSchema
        owner = $script:RecoveryFixtureOwner
        fixture_id = $script:RecoveryFixtureID
        fixture_root = $Context.RelativeRoot
    }
    Write-RecoveryJson -Path (Join-Path $Context.FixtureRoot '.engram-recovery-fixture-owner.json') -Value $marker -Context $Context
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

    $parent = Split-Path -Parent $Path
    [void](Assert-RecoveryContainedPath -Path $parent -RepositoryRoot $Context.RepositoryRoot)
    [void](Assert-RecoverySafeExistingPath -Path $parent)
    $text = $Value | ConvertTo-Json -Depth 32
    Assert-RecoverySecretSafeText -Text $text
    [IO.File]::WriteAllText($Path, ($text + [Environment]::NewLine), [Text.UTF8Encoding]::new($false))
    [void](Assert-RecoveryContainedPath -Path $Path -RepositoryRoot $Context.RepositoryRoot)
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
