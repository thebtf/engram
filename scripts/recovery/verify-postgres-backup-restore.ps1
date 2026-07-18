[CmdletBinding()]
param(
    [string]$DockerCommand = "docker",
    [string]$PostgresImage = $(if ($env:ENGRAM_RECOVERY_POSTGRES_IMAGE) { $env:ENGRAM_RECOVERY_POSTGRES_IMAGE } else { "engram:r2-postgres" }),
    [string]$BaselineRef = "v6.42.0",
    [switch]$ScavengeOnly
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
$dockerReady = $false
$passwordFile = $null
$vaultKeyFile = $null
$wrongVaultKeyFile = $null
$securitySamples = [System.Collections.Generic.List[string]]::new()
$dynamicSecrets = [System.Collections.Generic.List[string]]::new()
$ownerLabel = "engram.recovery.owner"
$ownerMarkerName = ".engram-recovery-owner.json"

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
    foreach ($secret in $dynamicSecrets) {
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

function Protect-PrivatePath {
    param([Parameter(Mandatory)][string]$Path, [switch]$Directory)
    if ($IsWindows) {
        $identity = [Security.Principal.WindowsIdentity]::GetCurrent().User
        if ($Directory) {
            $acl = [Security.AccessControl.DirectorySecurity]::new()
            $acl.SetAccessRuleProtection($true, $false)
            $inherit = [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit
            $rule = [Security.AccessControl.FileSystemAccessRule]::new(
                $identity,
                [Security.AccessControl.FileSystemRights]::FullControl,
                $inherit,
                [Security.AccessControl.PropagationFlags]::None,
                [Security.AccessControl.AccessControlType]::Allow
            )
            [void]$acl.AddAccessRule($rule)
            [IO.FileSystemAclExtensions]::SetAccessControl([IO.DirectoryInfo]::new($Path), $acl)
        } else {
            $acl = [Security.AccessControl.FileSecurity]::new()
            $acl.SetAccessRuleProtection($true, $false)
            $rule = [Security.AccessControl.FileSystemAccessRule]::new(
                $identity,
                [Security.AccessControl.FileSystemRights]::FullControl,
                [Security.AccessControl.AccessControlType]::Allow
            )
            [void]$acl.AddAccessRule($rule)
            [IO.FileSystemAclExtensions]::SetAccessControl([IO.FileInfo]::new($Path), $acl)
        }
        return
    }

    if ($Directory) {
        [IO.Directory]::SetUnixFileMode($Path, [IO.UnixFileMode]::UserRead -bor [IO.UnixFileMode]::UserWrite -bor [IO.UnixFileMode]::UserExecute)
    } else {
        [IO.File]::SetUnixFileMode($Path, [IO.UnixFileMode]::UserRead -bor [IO.UnixFileMode]::UserWrite)
    }
}

function New-SecretFile {
    param([Parameter(Mandatory)][string]$Name, [Parameter(Mandatory)][string]$Value)
    $path = Join-Path $tempRoot $Name
    [IO.File]::WriteAllText($path, $Value, [Text.UTF8Encoding]::new($false))
    Protect-PrivatePath -Path $path
    $path
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

function Get-DockerResultLines {
    param([Parameter(Mandatory)]$Result)
    if ($Result.ExitCode -ne 0) { throw "Docker ownership query failed: $($Result.Output)" }
    @($Result.Output -split "`r?`n" | ForEach-Object { $_.Trim() } | Where-Object { $_ })
}

function Test-RecoveryOwnerActive {
    param([Parameter(Mandatory)]$Marker)
    try {
        $ownerPID = [int]$Marker.pid
        $ownerTicks = [int64]$Marker.process_start_time_utc_ticks
        if ($ownerPID -le 0 -or $ownerTicks -le 0) { return $false }
        $owner = Get-Process -Id $ownerPID -ErrorAction SilentlyContinue
        if ($null -eq $owner) { return $false }
        return $owner.StartTime.ToUniversalTime().Ticks -eq $ownerTicks
    } catch {
        # An unreadable live process is not evidence that its recovery run is stale.
        return $true
    }
}

function Remove-StaleRecoveryRuns {
    $mutex = [Threading.Mutex]::new($false, "EngramRecoveryCleanup")
    $locked = $false
    try {
        try {
            $locked = $mutex.WaitOne([TimeSpan]::FromSeconds(30))
        } catch [Threading.AbandonedMutexException] {
            # WaitOne grants ownership when reporting an abandoned mutex.
            $locked = $true
        }
        if (-not $locked) { throw "timed out waiting for recovery cleanup ownership" }
        foreach ($directory in Get-ChildItem -LiteralPath ([IO.Path]::GetTempPath()) -Directory -Filter "engram-recovery-*" -ErrorAction SilentlyContinue) {
            if ($directory.Name -cnotmatch '^engram-recovery-[0-9a-f]{10}$') { continue }
            $markerPath = Join-Path $directory.FullName $ownerMarkerName
            if (-not (Test-Path -LiteralPath $markerPath -PathType Leaf)) { continue }
            # Parse the marker and validate it under StrictMode: accessing a missing
            # property on a PSCustomObject throws PropertyNotFoundException.  Treat any
            # parse failure or schema mismatch as no live owner and fall through to cleanup.
            $hasLiveOwner = $false
            try {
                $marker = Get-Content -LiteralPath $markerPath -Raw | ConvertFrom-Json -Depth 8
                # Read properties only via PSObject.Properties to avoid StrictMode throw.
                $markerSchemaVersion = $null
                $markerPrefix = $null
                $markerPid = $null
                $markerTicks = $null
                if ($null -ne $marker.PSObject.Properties['schema_version']) { $markerSchemaVersion = $marker.schema_version }
                if ($null -ne $marker.PSObject.Properties['prefix'])         { $markerPrefix = $marker.prefix }
                if ($null -ne $marker.PSObject.Properties['pid'])            { $markerPid = $marker.pid }
                if ($null -ne $marker.PSObject.Properties['process_start_time_utc_ticks']) { $markerTicks = $marker.process_start_time_utc_ticks }
                if ($markerSchemaVersion -is [int] -and [int]$markerSchemaVersion -eq 1 -and
                    $markerPrefix -is [string] -and [string]$markerPrefix -ceq $directory.Name -and
                    $markerPid -is [int] -and $markerTicks -is [long]) {
                    # Confirmed valid marker; check whether the owner process is still live.
                    $ownerProcess = Get-Process -Id ([int]$markerPid) -ErrorAction SilentlyContinue
                    if ($null -ne $ownerProcess -and $ownerProcess.StartTime.ToUniversalTime().Ticks -eq [long]$markerTicks) {
                        $hasLiveOwner = $true
                    }
                }
            } catch {
                # Malformed JSON, type coercion failure, or StrictMode property access —
                # cannot confirm a live owner; fall through to cleanup.
            }
            if ($hasLiveOwner) { continue }
            $filter = "label=$ownerLabel=$($directory.Name)"
            $containerIDs = Get-DockerResultLines (Invoke-Docker -Arguments @("ps", "--all", "--quiet", "--filter", $filter) -AllowFailure)
            foreach ($id in $containerIDs) { [void](Invoke-Docker -Arguments @("rm", "--force", "--volumes", $id)) }
            $volumeIDs = Get-DockerResultLines (Invoke-Docker -Arguments @("volume", "ls", "--quiet", "--filter", $filter) -AllowFailure)
            foreach ($id in $volumeIDs) { [void](Invoke-Docker -Arguments @("volume", "rm", "--force", $id)) }
            $networkIDs = Get-DockerResultLines (Invoke-Docker -Arguments @("network", "ls", "--quiet", "--filter", $filter) -AllowFailure)
            foreach ($id in $networkIDs) { [void](Invoke-Docker -Arguments @("network", "rm", $id)) }
            Remove-Item -LiteralPath $directory.FullName -Recurse -Force
            Write-Output "RECOVERY STALE CLEANUP: owner=$($directory.Name) status=removed"
        }
    } finally {
        if ($locked) { $mutex.ReleaseMutex() }
        $mutex.Dispose()
    }
}

function Invoke-NativeWithInput {
    param(
        [Parameter(Mandatory)][string]$FilePath,
        [Parameter(Mandatory)][string[]]$Arguments,
        [Parameter(Mandatory)][string]$InputText,
        [switch]$AllowFailure
    )
    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $FilePath
    $startInfo.UseShellExecute = $false
    $startInfo.RedirectStandardInput = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    foreach ($argument in $Arguments) { [void]$startInfo.ArgumentList.Add($argument) }
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    [void]$process.Start()
    $stdout = $process.StandardOutput.ReadToEndAsync()
    $stderr = $process.StandardError.ReadToEndAsync()
    $process.StandardInput.WriteLine($InputText)
    $process.StandardInput.Close()
    $process.WaitForExit()
    $output = (($stdout.Result, $stderr.Result) -join [Environment]::NewLine).Trim()
    if ($process.ExitCode -ne 0 -and -not $AllowFailure) {
        throw (Protect-RecoveryOutput -Text "$FilePath failed with exit code $($process.ExitCode): $output")
    }
    [pscustomobject]@{ ExitCode = $process.ExitCode; Output = $output }
}

function Get-ProcessCommandLines {
    if ($IsWindows) {
        @(Get-CimInstance Win32_Process -ErrorAction Stop | ForEach-Object { $_.CommandLine }) -join "`n"
        return
    }
    $lines = foreach ($entry in Get-ChildItem -Path "/proc" -Directory -ErrorAction SilentlyContinue) {
        if ($entry.Name -notmatch '^\d+$') { continue }
        $cmdline = Join-Path $entry.FullName "cmdline"
        try {
            $bytes = [IO.File]::ReadAllBytes($cmdline)
            for ($index = 0; $index -lt $bytes.Length; $index++) {
                if ($bytes[$index] -eq 0) { $bytes[$index] = 32 }
            }
            [Text.Encoding]::UTF8.GetString($bytes)
        } catch { }
    }
    $lines -join "`n"
}

function Assert-NoSecretExposure {
    param([Parameter(Mandatory)][string]$Phase, [string[]]$AdditionalSecrets = @())
    $secrets = @($password, $vaultKey, $wrongVaultKey) + $AdditionalSecrets | Where-Object { $_ }
    $processInventory = Get-ProcessCommandLines
    foreach ($secret in $secrets) {
        if ($processInventory.Contains($secret, [StringComparison]::Ordinal)) {
            throw "secret exposure detected in process arguments during $Phase"
        }
    }

    if ($containers.Count -gt 0) {
        $inspect = (Invoke-Docker -Arguments (@("inspect") + @($containers)) -AllowFailure).Output
        $logs = ($containers | ForEach-Object { (Invoke-Docker -Arguments @("logs", $_) -AllowFailure).Output }) -join "`n"
        foreach ($secret in $secrets) {
            if ($inspect.Contains($secret, [StringComparison]::Ordinal)) {
                throw "secret exposure detected in Docker metadata during $Phase"
            }
            if ($logs.Contains($secret, [StringComparison]::Ordinal)) {
                throw "secret exposure detected in Docker logs during $Phase"
            }
        }
        if ($inspect -match 'POSTGRES_PASSWORD=') {
            throw "direct POSTGRES_PASSWORD exposure detected in Docker metadata during $Phase"
        }
    }
    $securitySamples.Add($Phase)
    Write-Output "RECOVERY SECURITY NEGATIVE: phase=$Phase process_args=clean docker_metadata=clean logs=clean samples=$($securitySamples -join ',')"
}

function Wait-Postgres {
    param([Parameter(Mandatory)][string]$Name, [Parameter(Mandatory)][string]$User)
    foreach ($attempt in 1..90) {
        $probe = Invoke-Docker -Arguments @("exec", $Name, "pg_isready", "-U", $User, "-d", "engram") -AllowFailure
        if ($probe.ExitCode -eq 0) {
            Start-Sleep -Milliseconds 500
            $stable = Invoke-Docker -Arguments @("exec", $Name, "pg_isready", "-U", $User, "-d", "engram") -AllowFailure
            if ($stable.ExitCode -eq 0) { return }
        }
        Start-Sleep -Milliseconds 500
    }
    throw "PostgreSQL did not become ready: $Name"
}

function Set-ContainerPgPass {
    param([Parameter(Mandatory)][string]$Name)
    [void](Invoke-NativeWithInput -FilePath $DockerCommand -Arguments @(
        "exec", "--interactive", $Name, "sh", "-c", "umask 077; cat > /tmp/engram-recovery.pgpass"
    ) -InputText "*:*:*:*:$password")
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
    [void](Invoke-Docker -Arguments @("volume", "create", "--label", "$ownerLabel=$prefix", $volume))
    $volumes.Add($volume)
    [void](Invoke-Docker -Arguments @(
        "run", "--detach", "--name", $name, "--network", $network, "--label", "$ownerLabel=$prefix",
        "--publish", "127.0.0.1::5432",
        "--env", "POSTGRES_USER=$User", "--env", "POSTGRES_PASSWORD_FILE=/run/secrets/postgres-password", "--env", "POSTGRES_DB=engram",
        "--mount", "type=bind,source=$passwordFile,target=/run/secrets/postgres-password,readonly",
        "--volume", "${volume}:/var/lib/postgresql/data",
        $PostgresImage
    ))
    $containers.Add($name)
    Wait-Postgres -Name $name -User $User
    Set-ContainerPgPass -Name $name
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
        [Parameter(Mandatory)][string]$DSNFile,
        [Parameter(Mandatory)][string]$KeyFile
    )
    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = "go"
    $startInfo.WorkingDirectory = $SourceRoot
    $startInfo.UseShellExecute = $false
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    foreach ($argument in @(
        "run", "./tests/fixtures/recovery/fixture", "-action", $Action,
        "-dsn-file", $DSNFile, "-key-file", $KeyFile
    )) { [void]$startInfo.ArgumentList.Add($argument) }
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    [void]$process.Start()
    $stdout = $process.StandardOutput.ReadToEndAsync()
    $stderr = $process.StandardError.ReadToEndAsync()
    $process.WaitForExit()
    $output = (($stdout.Result, $stderr.Result) -join [Environment]::NewLine).Trim()
    Assert-NoSecretExposure -Phase "fixture-$Action"
    if ($process.ExitCode -ne 0) {
        throw (Protect-RecoveryOutput -Text "recovery fixture $Action failed with exit code $($process.ExitCode): $output")
    }
    [pscustomobject]@{ ExitCode = $process.ExitCode; Output = $output }
}

function Assert-DatabaseEmpty {
    param([Parameter(Mandatory)][string]$Name, [Parameter(Mandatory)][string]$User)
    $query = "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('r','p','v','m','S');"
    $count = (Invoke-Docker -Arguments @(
        "exec", "--env", "PGPASSFILE=/tmp/engram-recovery.pgpass", $Name, "psql", "-X", "-At", "-v", "ON_ERROR_STOP=1", "-U", $User, "-d", "engram", "-c", $query
    )).Output.Trim()
    if ($count -ne "0") { throw "restore target must be empty; found $count user objects" }
}

function Restore-Globals {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$User,
        [string]$GlobalsPath = (Join-Path $tempRoot "globals.sql")
    )
    $sourceText = [IO.File]::ReadAllText($GlobalsPath)
    if ($sourceText -match '(?im)^\s*(CREATE|ALTER|DROP)\s+TABLESPACE\b') {
        throw "unsupported globals object: recovery accepts roles and role memberships only"
    }

    $existingRoles = (Invoke-Docker -Arguments @(
        "exec", "--env", "PGPASSFILE=/tmp/engram-recovery.pgpass", $Name,
        "psql", "-X", "-At", "-v", "ON_ERROR_STOP=1", "-U", $User, "-d", "postgres",
        "-c", "SELECT quote_ident(rolname) FROM pg_roles ORDER BY 1"
    )).Output -split "`r?`n"
    $existing = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($role in $existingRoles) { if ($role) { [void]$existing.Add($role.Trim()) } }

    $filtered = Join-Path $tempRoot ("globals-{0}.sql" -f [Guid]::NewGuid().ToString("N"))
    $lines = foreach ($line in [IO.File]::ReadAllLines($GlobalsPath)) {
        if ($line -match '^CREATE ROLE (?<role>.+);$' -and $existing.Contains($Matches.role)) { continue }
        $line
    }
    [IO.File]::WriteAllLines($filtered, $lines, [Text.UTF8Encoding]::new($false))
    [void](Invoke-Docker -Arguments @("cp", $filtered, "${Name}:/tmp/globals.sql"))
    [void](Invoke-Docker -Arguments @(
        "exec", "--env", "PGPASSFILE=/tmp/engram-recovery.pgpass", $Name,
        "psql", "-X", "--single-transaction", "-v", "ON_ERROR_STOP=1", "-U", $User, "-d", "postgres", "-f", "/tmp/globals.sql"
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
        "exec", "--env", "PGPASSFILE=/tmp/engram-recovery.pgpass", $Name,
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
    $dockerReady = $true
    Remove-StaleRecoveryRuns
    if ($ScavengeOnly) {
        $completed = $true
        Write-Output "RECOVERY STALE CLEANUP PASS"
        return
    }
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
    Protect-PrivatePath -Path $tempRoot -Directory
    $ownerProcess = Get-Process -Id $PID
    $ownerMarker = [ordered]@{
        schema_version = 1
        prefix = $prefix
        pid = $PID
        process_start_time_utc_ticks = $ownerProcess.StartTime.ToUniversalTime().Ticks
    } | ConvertTo-Json -Compress
    $ownerMarkerPath = Join-Path $tempRoot $ownerMarkerName
    [IO.File]::WriteAllText($ownerMarkerPath, $ownerMarker, [Text.UTF8Encoding]::new($false))
    Protect-PrivatePath -Path $ownerMarkerPath
    $passwordFile = New-SecretFile -Name "postgres-password" -Value $password
    $vaultKeyFile = New-SecretFile -Name "vault-key" -Value $vaultKey
    $wrongVaultKeyFile = New-SecretFile -Name "wrong-vault-key" -Value $wrongVaultKey
    [void](Invoke-Docker -Arguments @("network", "create", "--label", "$ownerLabel=$prefix", $network))

    Write-Output "RECOVERY STAGE baseline-upgrade-seed"
    $source = Start-Postgres -Suffix "source" -User "source_admin"
    $sourceDSN = Get-DSN -Name $source -User "source_admin"
    $dynamicSecrets.Add($sourceDSN)
    $sourceDSNFile = New-SecretFile -Name "source-dsn" -Value $sourceDSN

    $legacyRoot = Join-Path $tempRoot "baseline-source"
    New-Item -ItemType Directory -Path $legacyRoot | Out-Null
    $legacyTar = Join-Path $tempRoot "baseline.tar"
    # Resolve $BaselineRef to a commit before git archive so shallow clones or
    # repos without the tag emit an actionable error rather than a cryptic failure.
    $baselineResolve = Invoke-Native -FilePath "git" -Arguments @("-C", $repoRoot, "rev-parse", "--verify", "$BaselineRef^{commit}") -AllowFailure
    if ($baselineResolve.ExitCode -ne 0) {
        throw "baseline ref '$BaselineRef' cannot be resolved to a commit in this repository; run 'git fetch --tags' or 'git fetch --unshallow' to fetch history, or pass -BaselineRef with a locally available ref"
    }
    $baselineCommit = $baselineResolve.Output.Trim()
    Write-Output "RECOVERY STAGE baseline-upgrade-seed resolved $BaselineRef=$baselineCommit"

    [void](Invoke-Native -FilePath "git" -Arguments @("archive", "--format=tar", "--output=$legacyTar", $baselineCommit))
    [void](Invoke-Native -FilePath "tar" -Arguments @("-xf", $legacyTar, "-C", $legacyRoot))
    $legacyFixture = Join-Path $legacyRoot "tests\fixtures\recovery\fixture"
    New-Item -ItemType Directory -Path $legacyFixture -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $repoRoot "tests\fixtures\recovery\fixture\main.go") -Destination $legacyFixture

    [void](Invoke-Fixture -SourceRoot $legacyRoot -Action "seed" -DSNFile $sourceDSNFile -KeyFile $vaultKeyFile)
    # Opening with the candidate source runs all pending migrations and proves the
    # supported v6.42.0 -> candidate upgrade before the backup is taken.
    [void](Invoke-Fixture -SourceRoot $repoRoot -Action "assert" -DSNFile $sourceDSNFile -KeyFile $vaultKeyFile)

    Write-Output "RECOVERY STAGE logical-backup"
    [void](Invoke-Docker -Arguments @(
        "exec", "--env", "PGPASSFILE=/tmp/engram-recovery.pgpass", $source,
        "pg_dump", "-U", "source_admin", "-d", "engram", "--format=custom", "--file=/tmp/engram.dump"
    ))
    [void](Invoke-Docker -Arguments @(
        "exec", "--env", "PGPASSFILE=/tmp/engram-recovery.pgpass", $source,
        "pg_dumpall", "-U", "source_admin", "--roles-only", "--no-role-passwords", "--file=/tmp/globals.sql"
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
        "exec", "--env", "PGPASSFILE=/tmp/engram-recovery.pgpass", $nonEmpty,
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

    # Unique application name lets pg_stat_activity identify this exact backend.
    $restoreAppName = "engram-restore-probe-$runID"
    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $DockerCommand
    $startInfo.UseShellExecute = $false
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    foreach ($arg in @(
        "exec", "--env", "PGPASSFILE=/tmp/engram-recovery.pgpass",
        "--env", "PGAPPNAME=$restoreAppName",
        $target,
        "pg_restore", "--exit-on-error", "--single-transaction",
        "-U", "target_admin", "-d", "engram", "/tmp/engram.dump"
    )) { [void]$startInfo.ArgumentList.Add($arg) }
    $restoreProcess = [Diagnostics.Process]::new()
    $restoreProcess.StartInfo = $startInfo
    [void]$restoreProcess.Start()
    $restoreStdout = $restoreProcess.StandardOutput.ReadToEndAsync()
    $restoreStderr = $restoreProcess.StandardError.ReadToEndAsync()

    # Poll pg_stat_activity until the restore backend has entered a write transaction.
    $xidQuery = "SELECT backend_xid::text FROM pg_stat_activity WHERE application_name='" + $restoreAppName + "' AND backend_xid IS NOT NULL LIMIT 1"
    $mutationObserved = $false
    $pollDeadline = [DateTime]::UtcNow.AddSeconds(120)
    while ([DateTime]::UtcNow -lt $pollDeadline) {
        if ($restoreProcess.HasExited) {
            throw "pg_restore exited (code=$($restoreProcess.ExitCode)) before an in-flight write transaction was observed; interrupted-restore-negative requires mutation proof"
        }
        $probe = Invoke-Docker -Arguments @(
            "exec", "--env", "PGPASSFILE=/tmp/engram-recovery.pgpass", $target,
            "psql", "-X", "-At", "-v", "ON_ERROR_STOP=1", "-U", "target_admin", "-d", "engram", "-c", $xidQuery
        ) -AllowFailure
        if ($probe.ExitCode -eq 0 -and $probe.Output.Trim() -match '^\d+$') {
            Write-Output "RECOVERY MUTATION PROOF: application_name=$restoreAppName backend_xid=$($probe.Output.Trim())"
            $mutationObserved = $true
            break
        }
        Start-Sleep -Milliseconds 100
    }
    if (-not $mutationObserved) {
        $restoreProcess.Kill()
        $restoreProcess.WaitForExit()
        throw "no in-flight write transaction observed for application_name='$restoreAppName' within 120 s; interrupted-restore-negative cannot be proven"
    }

    # Confirmed write transaction in progress; stop the container hard to simulate power loss.
    [void](Invoke-Docker -Arguments @("stop", "--time", "0", $target) -AllowFailure)
    $restoreProcess.WaitForExit()
    if ($restoreProcess.ExitCode -eq 0) {
        throw "pg_restore reported success after container was stopped; interrupted-restore-negative failed"
    }

    [void](Invoke-Docker -Arguments @("start", $target))
    Wait-Postgres -Name $target -User "target_admin"
    Assert-DatabaseEmpty -Name $target -User "target_admin"

    Write-Output "RECOVERY STAGE clean-restore-readback"
    [void](Restore-Database -Name $target -User "target_admin" -Archive (Join-Path $tempRoot "engram.dump"))
    $targetDSN = Get-DSN -Name $target -User "target_admin"
    $dynamicSecrets.Add($targetDSN)
    $targetDSNFile = New-SecretFile -Name "target-dsn" -Value $targetDSN
    [void](Invoke-Fixture -SourceRoot $repoRoot -Action "assert" -DSNFile $targetDSNFile -KeyFile $vaultKeyFile)

    $wrongKeyRejected = $false
    try { [void](Invoke-Fixture -SourceRoot $repoRoot -Action "assert" -DSNFile $targetDSNFile -KeyFile $wrongVaultKeyFile) } catch { $wrongKeyRejected = $true }
    if (-not $wrongKeyRejected) { throw "wrong vault key was accepted" }

    Write-Output "RECOVERY STAGE atomic-globals-negative"
    $hostileGlobals = Join-Path $tempRoot "globals-hostile.sql"
    [IO.File]::WriteAllText(
        $hostileGlobals,
        ([IO.File]::ReadAllText((Join-Path $tempRoot "globals.sql")) + "`nCREATE ROLE engram_recovery_atomic_probe NOLOGIN;`nSELECT * FROM engram_recovery_deliberately_missing;`n"),
        [Text.UTF8Encoding]::new($false)
    )
    $hostileGlobalsRejected = $false
    try { Restore-Globals -Name $target -User "target_admin" -GlobalsPath $hostileGlobals } catch { $hostileGlobalsRejected = $true }
    if (-not $hostileGlobalsRejected) { throw "hostile globals script was accepted" }
    $partialRole = (Invoke-Docker -Arguments @(
        "exec", "--env", "PGPASSFILE=/tmp/engram-recovery.pgpass", $target,
        "psql", "-X", "-At", "-v", "ON_ERROR_STOP=1", "-U", "target_admin", "-d", "postgres",
        "-c", "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='engram_recovery_atomic_probe')"
    )).Output.Trim()
    if ($partialRole -ne "f") { throw "failed globals transaction left a partial role" }

    Write-Output "RECOVERY STAGE idempotent-whole-recovery-retry"
    Restore-Globals -Name $target -User "target_admin"
    [void](Restore-Database -Name $target -User "target_admin" -Archive (Join-Path $tempRoot "engram.dump") -Clean)
    [void](Invoke-Fixture -SourceRoot $repoRoot -Action "assert" -DSNFile $targetDSNFile -KeyFile $vaultKeyFile)
    Assert-NoSecretExposure -Phase "final" -AdditionalSecrets @($sourceDSN, $targetDSN)

    $completed = $true
} finally {
    if ($dockerReady) {
        Remove-RecoveryResources
    } elseif (Test-Path $tempRoot) {
        Remove-Item -LiteralPath $tempRoot -Recurse -Force
    }
}

if (-not $completed) { throw "recovery flow did not reach success" }
Write-Output "RECOVERY PASS: baseline=$BaselineRef candidate=$(git -C $repoRoot rev-parse HEAD) postgres_image=$PostgresImage"
