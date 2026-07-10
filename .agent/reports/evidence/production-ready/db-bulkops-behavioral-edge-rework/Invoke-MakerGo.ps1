param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^engram_mkr_bedge_[a-z0-9_]+$')]
    [string]$DatabaseName,

    [Parameter(Mandatory = $true)]
    [string]$LogPath,

    [Parameter(Mandatory = $true, ValueFromRemainingArguments = $true)]
    [string[]]$GoArgs
)

$ErrorActionPreference = 'Stop'
$container = 'engram-prc-postgres'
$worktree = 'D:\Dev\engram\.agent\worktrees\prc-db-bulkops'
$baseCommit = '68b2ce5835c7c6efdf1c68da9eedcb8d9c3837ef'
$logDirectory = Split-Path -Parent $LogPath
New-Item -ItemType Directory -Force -Path $logDirectory | Out-Null

$containerEnv = @{}
$inspectEnv = docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' $container
if ($LASTEXITCODE -ne 0) {
    throw "docker inspect failed for $container"
}
foreach ($line in $inspectEnv) {
    $parts = $line -split '=', 2
    if ($parts.Count -eq 2) {
        $containerEnv[$parts[0]] = $parts[1]
    }
}
$pgUser = $containerEnv['POSTGRES_USER']
$pgPassword = $containerEnv['POSTGRES_PASSWORD']
if ([string]::IsNullOrWhiteSpace($pgUser) -or [string]::IsNullOrWhiteSpace($pgPassword)) {
    throw 'POSTGRES_USER/POSTGRES_PASSWORD are unavailable from the maker container'
}

$portLine = docker port $container 5432/tcp | Select-Object -First 1
if ($LASTEXITCODE -ne 0 -or $portLine -notmatch ':(\d+)$') {
    throw "could not resolve host PostgreSQL port for $container"
}
$hostPort = $Matches[1]

function Invoke-MakerPsql {
    param([Parameter(Mandatory = $true)][string]$Sql)
    $output = docker exec -e "PGPASSWORD=$pgPassword" $container psql -v ON_ERROR_STOP=1 -U $pgUser -d postgres -Atc $Sql
    if ($LASTEXITCODE -ne 0) {
        throw "psql failed: $Sql"
    }
    return $output
}

function Write-MakerLog {
    param([Parameter(Mandatory = $true)][AllowEmptyString()][string]$Line)
    $Line | Tee-Object -FilePath $LogPath -Append
}

if (Test-Path -LiteralPath $LogPath) {
    Remove-Item -LiteralPath $LogPath -Force
}

$existing = Invoke-MakerPsql "SELECT count(*) FROM pg_database WHERE datname = '$DatabaseName';"
if ([int]$existing -ne 0) {
    throw "maker database already exists: $DatabaseName"
}

$currentHead = git -C $worktree rev-parse HEAD
if ($LASTEXITCODE -ne 0) {
    throw "could not resolve maker worktree HEAD"
}
Write-MakerLog "base_sha=$baseCommit"
Write-MakerLog "head_sha=$currentHead"
Write-MakerLog "database=$DatabaseName"
Write-MakerLog "command=go $($GoArgs -join ' ')"
Write-MakerLog "started_utc=$([DateTime]::UtcNow.ToString('o'))"

$testExit = 99
$cleanupFailed = $false
try {
    Invoke-MakerPsql "CREATE DATABASE `"$DatabaseName`";" | Out-Null
    $escapedUser = [uri]::EscapeDataString($pgUser)
    $escapedPassword = [uri]::EscapeDataString($pgPassword)
    $env:DATABASE_DSN = "postgres://${escapedUser}:${escapedPassword}@127.0.0.1:${hostPort}/${DatabaseName}?sslmode=disable"

    Push-Location $worktree
    try {
        & go @GoArgs 2>&1 | Tee-Object -FilePath $LogPath -Append
        $testExit = $LASTEXITCODE
    }
    finally {
        Pop-Location
    }
    Write-MakerLog "test_exit=$testExit"
}
finally {
    try {
        $activeBefore = Invoke-MakerPsql "SELECT count(*) FROM pg_stat_activity WHERE datname = '$DatabaseName' AND pid <> pg_backend_pid();"
        Write-MakerLog "active_sessions_before_terminate=$activeBefore"
        Invoke-MakerPsql "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$DatabaseName' AND pid <> pg_backend_pid();" | Out-Null
        Invoke-MakerPsql "DROP DATABASE IF EXISTS `"$DatabaseName`" WITH (FORCE);" | Out-Null
        $databaseResidue = Invoke-MakerPsql "SELECT count(*) FROM pg_database WHERE datname = '$DatabaseName';"
        $activityResidue = Invoke-MakerPsql "SELECT count(*) FROM pg_stat_activity WHERE datname = '$DatabaseName';"
        Write-MakerLog "database_residue=$databaseResidue"
        Write-MakerLog "activity_residue=$activityResidue"
        Write-MakerLog "finished_utc=$([DateTime]::UtcNow.ToString('o'))"
        if ([int]$databaseResidue -ne 0 -or [int]$activityResidue -ne 0) {
            $cleanupFailed = $true
        }
    }
    catch {
        $cleanupFailed = $true
        Write-MakerLog "cleanup_error=$($_.Exception.Message)"
    }
}

if ($cleanupFailed) {
    exit 97
}
exit $testExit
