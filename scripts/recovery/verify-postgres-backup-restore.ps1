[CmdletBinding()]
param(
    [string]$DockerCommand = "docker",
    [string]$PostgresImage = $(if ($env:ENGRAM_RECOVERY_POSTGRES_IMAGE) { $env:ENGRAM_RECOVERY_POSTGRES_IMAGE } else { "engram:r2-accepted-65837cc7-postgres" }),
    [string]$BaselineRef = "v6.42.0"
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$runID = [Guid]::NewGuid().ToString("N").Substring(0, 10)
$prefix = "engram-recovery-$runID"
$network = "$prefix-net"
$password = "recovery-${runID}-password"
$containers = [System.Collections.Generic.List[string]]::new()
$volumes = [System.Collections.Generic.List[string]]::new()
$tempRoot = Join-Path ([IO.Path]::GetTempPath()) $prefix

function New-RandomHexKey {
    $bytes = [byte[]]::new(32)
    [Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
    [Convert]::ToHexString($bytes).ToLowerInvariant()
}

$vaultKey = New-RandomHexKey
$wrongVaultKey = New-RandomHexKey

function Protect-RecoveryOutput {
    param([AllowEmptyString()][string]$Text)
    $redacted = $Text
    foreach ($secret in @($password, $vaultKey, $wrongVaultKey)) {
        if ($secret) { $redacted = $redacted.Replace($secret, "<redacted>") }
    }
    $redacted
}

function Require-Command {
    param([Parameter(Mandatory)][string]$Name)
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "required dependency is unavailable: $Name"
    }
}

function Invoke-Native {
    param(
        [Parameter(Mandatory)][string]$FilePath,
        [Parameter(Mandatory)][string[]]$Arguments,
        [switch]$AllowFailure
    )
    $output = & $FilePath @Arguments 2>&1
    $exitCode = $LASTEXITCODE
    if ($exitCode -ne 0 -and -not $AllowFailure) {
        $failure = "$FilePath $($Arguments -join ' ') failed with exit code ${exitCode}: $($output -join [Environment]::NewLine)"
        throw (Protect-RecoveryOutput -Text $failure)
    }
    [pscustomobject]@{ ExitCode = $exitCode; Output = ($output -join [Environment]::NewLine) }
}

function Invoke-Docker {
    param([Parameter(Mandatory)][string[]]$Arguments, [switch]$AllowFailure)
    Invoke-Native -FilePath $DockerCommand -Arguments $Arguments -AllowFailure:$AllowFailure
}

function Wait-Postgres {
    param([Parameter(Mandatory)][string]$Name, [Parameter(Mandatory)][string]$User)
    foreach ($attempt in 1..90) {
        $probe = Invoke-Docker -Arguments @("exec", "-e", "PGPASSWORD=$password", $Name, "pg_isready", "-U", $User, "-d", "engram") -AllowFailure
        if ($probe.ExitCode -eq 0) {
            Start-Sleep -Milliseconds 500
            $stable = Invoke-Docker -Arguments @("exec", "-e", "PGPASSWORD=$password", $Name, "pg_isready", "-U", $User, "-d", "engram") -AllowFailure
            if ($stable.ExitCode -eq 0) { return }
        }
        Start-Sleep -Milliseconds 500
    }
    throw "PostgreSQL did not become ready: $Name"
}

function Remove-Postgres {
    param([Parameter(Mandatory)][string]$Name)
    [void](Invoke-Docker -Arguments @("rm", "--force", $Name) -AllowFailure)
    [void](Invoke-Docker -Arguments @("volume", "rm", "--force", "$Name-data") -AllowFailure)
}

function Start-Postgres {
    param([Parameter(Mandatory)][string]$Suffix, [Parameter(Mandatory)][string]$User)
    $name = "$prefix-$Suffix"
    $volume = "$name-data"
    [void](Invoke-Docker -Arguments @("volume", "create", $volume))
    $volumes.Add($volume)
    [void](Invoke-Docker -Arguments @(
        "run", "--detach", "--name", $name, "--network", $network,
        "--publish", "127.0.0.1::5432",
        "--env", "POSTGRES_USER=$User", "--env", "POSTGRES_PASSWORD=$password", "--env", "POSTGRES_DB=engram",
        "--volume", "${volume}:/var/lib/postgresql/data",
        $PostgresImage
    ))
    $containers.Add($name)
    Wait-Postgres -Name $name -User $User
    $name
}

function Get-DSN {
    param([Parameter(Mandatory)][string]$Name, [Parameter(Mandatory)][string]$User)
    $binding = (Invoke-Docker -Arguments @("port", $Name, "5432/tcp")).Output.Trim()
    if ($binding -notmatch ":(?<port>\d+)$") { throw "cannot parse PostgreSQL port binding: $binding" }
    "postgres://${User}:${password}@127.0.0.1:$($Matches.port)/engram?sslmode=disable"
}

function Invoke-Fixture {
    param(
        [Parameter(Mandatory)][string]$SourceRoot,
        [Parameter(Mandatory)][ValidateSet("seed", "assert")][string]$Action,
        [Parameter(Mandatory)][string]$DSN,
        [Parameter(Mandatory)][string]$Key
    )
    Push-Location $SourceRoot
    try {
        Invoke-Native -FilePath "go" -Arguments @(
            "run", "./tests/fixtures/recovery/fixture", "-action", $Action, "-dsn", $DSN, "-key", $Key
        )
    } finally {
        Pop-Location
    }
}

function Assert-DatabaseEmpty {
    param([Parameter(Mandatory)][string]$Name, [Parameter(Mandatory)][string]$User)
    $query = "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('r','p','v','m','S');"
    $count = (Invoke-Docker -Arguments @(
        "exec", "-e", "PGPASSWORD=$password", $Name, "psql", "-X", "-At", "-v", "ON_ERROR_STOP=1", "-U", $User, "-d", "engram", "-c", $query
    )).Output.Trim()
    if ($count -ne "0") { throw "restore target must be empty; found $count user objects" }
}

function Restore-Globals {
    param([Parameter(Mandatory)][string]$Name, [Parameter(Mandatory)][string]$User)
    [void](Invoke-Docker -Arguments @("cp", (Join-Path $tempRoot "globals.sql"), "${Name}:/tmp/globals.sql"))
    [void](Invoke-Docker -Arguments @(
        "exec", "-e", "PGPASSWORD=$password", $Name,
        "psql", "-X", "-v", "ON_ERROR_STOP=1", "-U", $User, "-d", "postgres", "-f", "/tmp/globals.sql"
    ))
}

function Restore-Database {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$User,
        [Parameter(Mandatory)][string]$Archive,
        [switch]$Clean
    )
    if (-not $Clean) { Assert-DatabaseEmpty -Name $Name -User $User }
    [void](Invoke-Docker -Arguments @("cp", $Archive, "${Name}:/tmp/engram.dump"))
    $arguments = @(
        "exec", "-e", "PGPASSWORD=$password", $Name,
        "pg_restore", "--exit-on-error", "--single-transaction"
    )
    if ($Clean) { $arguments += @("--clean", "--if-exists") }
    $arguments += @("-U", $User, "-d", "engram", "/tmp/engram.dump")
    Invoke-Docker -Arguments $arguments
}

function Remove-RecoveryResources {
    $cleanupFailures = [System.Collections.Generic.List[string]]::new()
    foreach ($container in $containers) {
        $result = Invoke-Docker -Arguments @("rm", "--force", "--volumes", $container) -AllowFailure
        if ($result.ExitCode -ne 0 -and $result.Output -notmatch "No such container") { $cleanupFailures.Add($result.Output) }
    }
    foreach ($volume in $volumes) {
        $result = Invoke-Docker -Arguments @("volume", "rm", "--force", $volume) -AllowFailure
        if ($result.ExitCode -ne 0 -and $result.Output -notmatch "no such volume") { $cleanupFailures.Add($result.Output) }
    }
    $result = Invoke-Docker -Arguments @("network", "rm", $network) -AllowFailure
    if ($result.ExitCode -ne 0 -and $result.Output -notmatch "not found") { $cleanupFailures.Add($result.Output) }
    if (Test-Path $tempRoot) { Remove-Item -LiteralPath $tempRoot -Recurse -Force }

    $containerResidue = (Invoke-Docker -Arguments @("ps", "--all", "--quiet", "--filter", "name=$prefix") -AllowFailure).Output.Trim()
    $volumeResidue = (Invoke-Docker -Arguments @("volume", "ls", "--quiet", "--filter", "name=$prefix") -AllowFailure).Output.Trim()
    $networkResidue = (Invoke-Docker -Arguments @("network", "ls", "--quiet", "--filter", "name=$prefix") -AllowFailure).Output.Trim()
    if ($containerResidue -or $volumeResidue -or $networkResidue) {
        $cleanupFailures.Add("Docker residue remains: containers=$containerResidue volumes=$volumeResidue networks=$networkResidue")
    }
    if ($cleanupFailures.Count -gt 0) { throw "recovery cleanup failed: $($cleanupFailures -join '; ')" }
}

$completed = $false
try {
    Write-Output "RECOVERY STAGE dependency-preflight"
    Require-Command -Name $DockerCommand
    Require-Command -Name "git"
    Require-Command -Name "go"
    Require-Command -Name "tar"

    $missingDependencyRejected = $false
    try { Require-Command -Name "$prefix-deliberately-missing" } catch { $missingDependencyRejected = $true }
    if (-not $missingDependencyRejected) { throw "missing dependency probe did not fail closed" }

    if ((Invoke-Docker -Arguments @("image", "inspect", $PostgresImage) -AllowFailure).ExitCode -ne 0) {
        throw "PostgreSQL 17 recovery image is unavailable locally: $PostgresImage"
    }
    $major = (Invoke-Docker -Arguments @("run", "--rm", $PostgresImage, "postgres", "--version")).Output
    if ($major -notmatch "PostgreSQL\) 17\.") { throw "recovery requires PostgreSQL 17, got: $major" }

    New-Item -ItemType Directory -Path $tempRoot | Out-Null
    [void](Invoke-Docker -Arguments @("network", "create", $network))

    Write-Output "RECOVERY STAGE baseline-upgrade-seed"
    $source = Start-Postgres -Suffix "source" -User "source_admin"
    $sourceDSN = Get-DSN -Name $source -User "source_admin"

    $legacyRoot = Join-Path $tempRoot "baseline-source"
    New-Item -ItemType Directory -Path $legacyRoot | Out-Null
    $legacyTar = Join-Path $tempRoot "baseline.tar"
    [void](Invoke-Native -FilePath "git" -Arguments @("archive", "--format=tar", "--output=$legacyTar", $BaselineRef))
    [void](Invoke-Native -FilePath "tar" -Arguments @("-xf", $legacyTar, "-C", $legacyRoot))
    $legacyFixture = Join-Path $legacyRoot "tests\fixtures\recovery\fixture"
    New-Item -ItemType Directory -Path $legacyFixture -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $repoRoot "tests\fixtures\recovery\fixture\main.go") -Destination $legacyFixture

    [void](Invoke-Fixture -SourceRoot $legacyRoot -Action "seed" -DSN $sourceDSN -Key $vaultKey)
    # Opening with the candidate source runs all pending migrations and proves the
    # supported v6.42.0 -> candidate upgrade before the backup is taken.
    [void](Invoke-Fixture -SourceRoot $repoRoot -Action "assert" -DSN $sourceDSN -Key $vaultKey)

    Write-Output "RECOVERY STAGE logical-backup"
    [void](Invoke-Docker -Arguments @(
        "exec", "-e", "PGPASSWORD=$password", $source,
        "pg_dump", "-U", "source_admin", "-d", "engram", "--format=custom", "--file=/tmp/engram.dump"
    ))
    [void](Invoke-Docker -Arguments @(
        "exec", "-e", "PGPASSWORD=$password", $source,
        "pg_dumpall", "-U", "source_admin", "--globals-only", "--no-role-passwords", "--file=/tmp/globals.sql"
    ))
    [void](Invoke-Docker -Arguments @("cp", "${source}:/tmp/engram.dump", (Join-Path $tempRoot "engram.dump")))
    [void](Invoke-Docker -Arguments @("cp", "${source}:/tmp/globals.sql", (Join-Path $tempRoot "globals.sql")))

    $corruptArchive = Join-Path $tempRoot "engram-corrupt.dump"
    Copy-Item -LiteralPath (Join-Path $tempRoot "engram.dump") -Destination $corruptArchive
    $stream = [IO.File]::Open($corruptArchive, [IO.FileMode]::Open, [IO.FileAccess]::Write)
    try { $stream.SetLength(128) } finally { $stream.Dispose() }
    [void](Invoke-Docker -Arguments @("cp", $corruptArchive, "${source}:/tmp/engram-corrupt.dump"))
    $corruptProbe = Invoke-Docker -Arguments @("exec", $source, "pg_restore", "--list", "/tmp/engram-corrupt.dump") -AllowFailure
    if ($corruptProbe.ExitCode -eq 0) { throw "corrupted archive was accepted" }
    Remove-Postgres -Name $source

    Write-Output "RECOVERY STAGE nonempty-target-negative"
    $nonEmpty = Start-Postgres -Suffix "nonempty" -User "target_admin"
    Restore-Globals -Name $nonEmpty -User "target_admin"
    [void](Invoke-Docker -Arguments @(
        "exec", "-e", "PGPASSWORD=$password", $nonEmpty,
        "psql", "-X", "-v", "ON_ERROR_STOP=1", "-U", "target_admin", "-d", "engram", "-c", "CREATE TABLE must_block_restore(id integer);"
    ))
    $nonEmptyRejected = $false
    try { [void](Restore-Database -Name $nonEmpty -User "target_admin" -Archive (Join-Path $tempRoot "engram.dump")) } catch { $nonEmptyRejected = $true }
    if (-not $nonEmptyRejected) { throw "non-empty target was not rejected" }
    Remove-Postgres -Name $nonEmpty

    Write-Output "RECOVERY STAGE corrupt-archive-negative"
    $corruptTarget = Start-Postgres -Suffix "corrupt" -User "target_admin"
    Restore-Globals -Name $corruptTarget -User "target_admin"
    $corruptRejected = $false
    try { [void](Restore-Database -Name $corruptTarget -User "target_admin" -Archive $corruptArchive) } catch { $corruptRejected = $true }
    if (-not $corruptRejected) { throw "corrupted restore was not rejected" }
    Assert-DatabaseEmpty -Name $corruptTarget -User "target_admin"
    Remove-Postgres -Name $corruptTarget

    Write-Output "RECOVERY STAGE interrupted-restore-negative"
    $target = Start-Postgres -Suffix "target" -User "target_admin"
    Restore-Globals -Name $target -User "target_admin"
    [void](Invoke-Docker -Arguments @("cp", (Join-Path $tempRoot "engram.dump"), "${target}:/tmp/engram.dump"))

    $restoreArgs = @(
        "exec", "-e", "PGPASSWORD=$password", $target,
        "pg_restore", "--exit-on-error", "--single-transaction", "-U", "target_admin", "-d", "engram", "/tmp/engram.dump"
    )
    $restoreProcess = Start-Process -FilePath $DockerCommand -ArgumentList $restoreArgs -PassThru -NoNewWindow
    Start-Sleep -Milliseconds 25
    [void](Invoke-Docker -Arguments @("stop", "--time", "0", $target) -AllowFailure)
    $restoreProcess.WaitForExit()
    [void](Invoke-Docker -Arguments @("start", $target))
    Wait-Postgres -Name $target -User "target_admin"
    Start-Sleep -Milliseconds 500
    Assert-DatabaseEmpty -Name $target -User "target_admin"

    Write-Output "RECOVERY STAGE clean-restore-readback"
    [void](Restore-Database -Name $target -User "target_admin" -Archive (Join-Path $tempRoot "engram.dump"))
    $targetDSN = Get-DSN -Name $target -User "target_admin"
    [void](Invoke-Fixture -SourceRoot $repoRoot -Action "assert" -DSN $targetDSN -Key $vaultKey)

    $wrongKeyRejected = $false
    try { [void](Invoke-Fixture -SourceRoot $repoRoot -Action "assert" -DSN $targetDSN -Key $wrongVaultKey) } catch { $wrongKeyRejected = $true }
    if (-not $wrongKeyRejected) { throw "wrong vault key was accepted" }

    Write-Output "RECOVERY STAGE idempotent-clean-retry"
    [void](Restore-Database -Name $target -User "target_admin" -Archive (Join-Path $tempRoot "engram.dump") -Clean)
    [void](Invoke-Fixture -SourceRoot $repoRoot -Action "assert" -DSN $targetDSN -Key $vaultKey)

    $completed = $true
} finally {
    Remove-RecoveryResources
}

if (-not $completed) { throw "recovery flow did not reach success" }
Write-Output "RECOVERY PASS: baseline=$BaselineRef candidate=$(git -C $repoRoot rev-parse HEAD) postgres_image=$PostgresImage"
