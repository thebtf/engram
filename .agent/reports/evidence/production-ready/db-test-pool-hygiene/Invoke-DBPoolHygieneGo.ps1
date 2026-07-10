param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^engram_mkr_dbph_[a-z0-9_]+$')]
    [string]$DatabaseName,

    [Parameter(Mandatory = $true)]
    [string]$LogPath,

    [Parameter(Mandatory = $true, ValueFromRemainingArguments = $true)]
    [string[]]$GoArgs
)

$ErrorActionPreference = 'Stop'
$container = 'engram-prc-postgres'
$worktree = 'D:\Dev\engram\.agent\worktrees\dbph'
$baseCommit = 'bd68c05baf4b7250096dd84f56bebea2aa555970'
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
    throw 'POSTGRES_USER/POSTGRES_PASSWORD are unavailable from the test container'
}

$portLine = docker port $container 5432/tcp | Select-Object -First 1
if ($LASTEXITCODE -ne 0 -or $portLine -notmatch ':(\d+)$') {
    throw "could not resolve host PostgreSQL port for $container"
}
$hostPort = $Matches[1]

function Invoke-TestPsql {
    param(
        [Parameter(Mandatory = $true)][string]$Database,
        [Parameter(Mandatory = $true)][string]$Sql
    )
    $output = docker exec -e "PGPASSWORD=$pgPassword" $container psql -v ON_ERROR_STOP=1 -U $pgUser -d $Database -Atc $Sql
    if ($LASTEXITCODE -ne 0) {
        throw "psql failed against $Database"
    }
    return $output
}

function Write-Evidence {
    param([Parameter(Mandatory = $true)][AllowEmptyString()][string]$Line)
    $Line | Tee-Object -FilePath $LogPath -Append
}

if (Test-Path -LiteralPath $LogPath) {
    Remove-Item -LiteralPath $LogPath -Force
}

$existing = Invoke-TestPsql -Database postgres -Sql "SELECT count(*) FROM pg_database WHERE datname = '$DatabaseName';"
if ([int]$existing -ne 0) {
    throw "test database already exists: $DatabaseName"
}

$currentHead = git -C $worktree rev-parse HEAD
if ($LASTEXITCODE -ne 0) {
    throw 'could not resolve maker worktree HEAD'
}
Write-Evidence "base_sha=$baseCommit"
Write-Evidence "head_sha=$currentHead"
Write-Evidence "database=$DatabaseName"
Write-Evidence "command=go $($GoArgs -join ' ')"
Write-Evidence "max_connections=$(Invoke-TestPsql -Database postgres -Sql 'SHOW max_connections;')"
Write-Evidence "superuser_reserved_connections=$(Invoke-TestPsql -Database postgres -Sql 'SHOW superuser_reserved_connections;')"
Write-Evidence "started_utc=$([DateTime]::UtcNow.ToString('o'))"

$testExit = 99
$cleanupFailed = $false
try {
    Invoke-TestPsql -Database postgres -Sql "CREATE DATABASE `"$DatabaseName`";" | Out-Null
    $escapedUser = [uri]::EscapeDataString($pgUser)
    $escapedPassword = [uri]::EscapeDataString($pgPassword)
    $env:DATABASE_DSN = "postgres://${escapedUser}:${escapedPassword}@127.0.0.1:${hostPort}/${DatabaseName}?sslmode=disable"
    Write-Evidence "activity_before_test=$(Invoke-TestPsql -Database postgres -Sql "SELECT count(*) FROM pg_stat_activity WHERE datname = '$DatabaseName';")"

    Push-Location $worktree
    try {
        & go @GoArgs 2>&1 | Tee-Object -FilePath $LogPath -Append
        $testExit = $LASTEXITCODE
    }
    finally {
        Pop-Location
    }
    Write-Evidence "test_exit=$testExit"
}
finally {
    try {
        $activeAfter = Invoke-TestPsql -Database postgres -Sql "SELECT count(*) FROM pg_stat_activity WHERE datname = '$DatabaseName';"
        Write-Evidence "activity_after_test_process=$activeAfter"
        Invoke-TestPsql -Database postgres -Sql "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$DatabaseName' AND pid <> pg_backend_pid();" | Out-Null
        Invoke-TestPsql -Database postgres -Sql "DROP DATABASE IF EXISTS `"$DatabaseName`" WITH (FORCE);" | Out-Null
        $databaseResidue = Invoke-TestPsql -Database postgres -Sql "SELECT count(*) FROM pg_database WHERE datname = '$DatabaseName';"
        $activityResidue = Invoke-TestPsql -Database postgres -Sql "SELECT count(*) FROM pg_stat_activity WHERE datname = '$DatabaseName';"
        Write-Evidence "database_residue=$databaseResidue"
        Write-Evidence "activity_residue=$activityResidue"
        Write-Evidence "finished_utc=$([DateTime]::UtcNow.ToString('o'))"
        if ([int]$databaseResidue -ne 0 -or [int]$activityResidue -ne 0) {
            $cleanupFailed = $true
        }
    }
    catch {
        $cleanupFailed = $true
        Write-Evidence "cleanup_error=$($_.Exception.Message)"
    }
}

if ($cleanupFailed) {
    exit 97
}
exit $testExit
