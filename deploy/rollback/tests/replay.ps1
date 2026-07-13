[CmdletBinding()]
param(
    [string]$EvidenceFile = (Join-Path $PSScriptRoot '..\evidence\OPS-DEPLOY-ROLLBACK-E1.replay.json'),
    [switch]$ScavengeOnly
)

$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..\..\..')).Path
$compose = Join-Path $repo 'deploy\docker-compose.runtime.yml'
$healthcheck = Join-Path $repo 'deploy\healthcheck\Test-EngramRuntime.ps1'
$rollback = Join-Path $repo 'deploy\rollback\Invoke-EngramRollback.ps1'
$suffix = ([guid]::NewGuid().ToString('N')).Substring(0, 10)
$project = "engram-e1-$suffix"
$replayTempRoot = Join-Path $repo '.agent\tmp\rollback-replay'
$temp = Join-Path $replayTempRoot $project
$currentEnv = Join-Path $temp 'current.env'
$previousEnv = Join-Path $temp 'previous.env'
$badEnv = Join-Path $temp 'bad.env'
$rollbackEvidence = Join-Path $temp 'rollback.json'
$failedEvidence = Join-Path $temp 'rollback-failure.json'
$badDatabaseDsn = Join-Path $temp 'bad_database_dsn'
$memoryProject = "rollback-$suffix"
$memoryContent = "customer-memory-$suffix"
$token = ([guid]::NewGuid().ToString('N') + [guid]::NewGuid().ToString('N'))
$password = 'pg' + [guid]::NewGuid().ToString('N')
$startedAt = Get-Date
$steps = [ordered]@{}
$ownerMarkerName = '.engram-rollback-replay-owner.json'

function Get-DockerLines([string[]]$arguments) {
    $output = & docker @arguments 2>&1
    if ($LASTEXITCODE -ne 0) { throw "docker $($arguments -join ' ') failed: $($output -join ' ')" }
    return @($output | ForEach-Object { ([string]$_).Trim() } | Where-Object { $_ })
}

function Remove-LabeledProjectResources([string]$ownedProject) {
    if ($ownedProject -cnotmatch '^engram-e1-[0-9a-f]{10}$') { throw "unsafe replay project name: $ownedProject" }
    $filter = "label=com.docker.compose.project=$ownedProject"
    foreach ($id in @(Get-DockerLines @('ps', '--all', '--quiet', '--filter', $filter))) { & docker rm --force --volumes $id | Out-Null }
    foreach ($id in @(Get-DockerLines @('volume', 'ls', '--quiet', '--filter', $filter))) { & docker volume rm --force $id | Out-Null }
    foreach ($id in @(Get-DockerLines @('network', 'ls', '--quiet', '--filter', $filter))) { & docker network rm $id | Out-Null }
}

function Remove-ReplayDirectory([string]$path) {
    $root = [IO.Path]::GetFullPath($replayTempRoot)
    $target = [IO.Path]::GetFullPath($path)
    if ([IO.Path]::GetDirectoryName($target) -cne $root -or [IO.Path]::GetFileName($target) -cnotmatch '^engram-e1-[0-9a-f]{10}$') {
        throw "unsafe replay cleanup path: $target"
    }
    if ([IO.Directory]::Exists($target)) { [IO.Directory]::Delete($target, $true) }
}

function Test-ReplayOwnerActive($marker) {
    try {
        $owner = Get-Process -Id ([int]$marker.pid) -ErrorAction SilentlyContinue
        if ($null -eq $owner) { return $false }
        return $owner.StartTime.ToUniversalTime().Ticks -eq [int64]$marker.process_start_time_utc_ticks
    } catch { return $true }
}

function Remove-StaleReplayRuns {
    $mutex = [Threading.Mutex]::new($false, 'EngramRollbackReplayCleanup')
    $locked = $false
    try {
        try { $locked = $mutex.WaitOne([TimeSpan]::FromSeconds(30)) } catch [Threading.AbandonedMutexException] { $locked = $true }
        if (-not $locked) { throw 'timed out waiting for rollback replay cleanup ownership' }
        foreach ($directory in Get-ChildItem -LiteralPath $replayTempRoot -Directory -Filter 'engram-e1-*' -ErrorAction SilentlyContinue) {
            if ($directory.Name -cnotmatch '^engram-e1-[0-9a-f]{10}$') { continue }
            $markerPath = Join-Path $directory.FullName $ownerMarkerName
            if (-not (Test-Path -LiteralPath $markerPath -PathType Leaf)) { continue }
            try { $marker = Get-Content -Raw -LiteralPath $markerPath | ConvertFrom-Json } catch { continue }
            if ([int]$marker.schema_version -ne 1 -or [string]$marker.project -cne $directory.Name -or (Test-ReplayOwnerActive $marker)) { continue }
            Remove-LabeledProjectResources $directory.Name
            Remove-ReplayDirectory $directory.FullName
            Write-Output "ROLLBACK REPLAY STALE CLEANUP: project=$($directory.Name) status=removed"
        }
    } finally {
        if ($locked) { $mutex.ReleaseMutex() }
        $mutex.Dispose()
    }
}

function Get-FreePort {
    $listener = [System.Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $listener.Start()
    try { return ([Net.IPEndPoint]$listener.LocalEndpoint).Port } finally { $listener.Stop() }
}

function Write-Env([string]$path, [string]$server, [string]$operator, [string]$postgres, [int]$serverPort, [int]$operatorPort) {
    @(
        "ENGRAM_SERVER_IMAGE=$server"
        "ENGRAM_OPERATOR_IMAGE=$operator"
        "ENGRAM_POSTGRES_IMAGE=$postgres"
        "ENGRAM_DATABASE_DSN_SECRET_FILE=$((Join-Path $temp 'database_dsn').Replace('\','/'))"
        "ENGRAM_AUTH_ADMIN_TOKEN_SECRET_FILE=$((Join-Path $temp 'admin_token').Replace('\','/'))"
        "ENGRAM_POSTGRES_PASSWORD_SECRET_FILE=$((Join-Path $temp 'postgres_password').Replace('\','/'))"
        "ENGRAM_VAULT_KEY_SECRET_FILE=$((Join-Path $temp 'vault_key').Replace('\','/'))"
        'WORKER_BIND=127.0.0.1'
        "WORKER_PORT=$serverPort"
        'OPERATOR_CONSOLE_BIND=127.0.0.1'
        "OPERATOR_CONSOLE_PORT=$operatorPort"
    ) | Set-Content -LiteralPath $path -Encoding utf8NoBOM
}

function Invoke-Compose([string]$envFile, [string[]]$arguments) {
    & docker compose --project-name $project --env-file $envFile -f $compose @arguments
    if ($LASTEXITCODE -ne 0) { throw "docker compose failed: $($arguments -join ' ')" }
}

function Read-Memory([string]$envFile) {
    $port = (& docker compose --project-name $project --env-file $envFile -f $compose port server 37777).Trim()
    $uri = 'http://127.0.0.1:' + (($port -split ':')[-1]) + '/api/memories?project=' + [uri]::EscapeDataString($memoryProject) + '&limit=50'
    $rows = @(Invoke-RestMethod -Uri $uri -Headers @{ Authorization = "Bearer $token" } -TimeoutSec 15)
    return ($rows.content -contains $memoryContent)
}

New-Item -ItemType Directory -Force -Path $replayTempRoot | Out-Null
Remove-StaleReplayRuns
if ($ScavengeOnly) {
    Write-Output 'ROLLBACK REPLAY STALE CLEANUP PASS'
    return
}

New-Item -ItemType Directory -Force -Path $temp | Out-Null
$owner = Get-Process -Id $PID
[ordered]@{
    schema_version = 1
    project = $project
    pid = $PID
    process_start_time_utc_ticks = $owner.StartTime.ToUniversalTime().Ticks
} | ConvertTo-Json -Compress | Set-Content -LiteralPath (Join-Path $temp $ownerMarkerName) -Encoding utf8NoBOM
Set-Content -LiteralPath (Join-Path $temp 'postgres_password') -Value $password -NoNewline
Set-Content -LiteralPath (Join-Path $temp 'vault_key') -Value ('a' * 64) -NoNewline
Set-Content -LiteralPath (Join-Path $temp 'database_dsn') -Value "postgres://engram:$password@postgres:5432/engram?sslmode=disable" -NoNewline
Set-Content -LiteralPath (Join-Path $temp 'admin_token') -Value $token -NoNewline
Set-Content -LiteralPath $badDatabaseDsn -Value "postgres://engram:$password@missing-postgres:5432/engram?sslmode=disable" -NoNewline
$serverPort = Get-FreePort
$operatorPort = Get-FreePort
Write-Env $currentEnv 'engram@sha256:0f678c4316e8ac5a49d917f58477d2a4a5f886eab3084924ec1ef7e1c4d769c1' 'engram@sha256:bf959a088be3e607d483b3d64b1747f7e65f91ea9dfbc90baa3029544e728400' 'engram@sha256:78780f7a04ce28fcdd33ff9fcd3b4de400bc510e39a06ff3223a164dbdb4eee7' $serverPort $operatorPort
Write-Env $previousEnv 'engram@sha256:a00dd8dd2bd768335915fb9de7cf54f97ed1a72538397de2b8f0de1a1cf1f968' 'engram@sha256:7ebac00425f4da75453bc8d08ecbc0b9c351f87ce80ee586bba816207cf1080a' 'engram@sha256:950df543598a125b8bbcb551c5513718974c9ff73230685a35173b53e6d090bd' $serverPort $operatorPort
Write-Env $badEnv 'engram@sha256:a00dd8dd2bd768335915fb9de7cf54f97ed1a72538397de2b8f0de1a1cf1f968' 'engram@sha256:7ebac00425f4da75453bc8d08ecbc0b9c351f87ce80ee586bba816207cf1080a' 'engram@sha256:950df543598a125b8bbcb551c5513718974c9ff73230685a35173b53e6d090bd' $serverPort $operatorPort
$badEnvContent = (Get-Content -LiteralPath $badEnv) -replace '^ENGRAM_DATABASE_DSN_SECRET_FILE=.*$', "ENGRAM_DATABASE_DSN_SECRET_FILE=$($badDatabaseDsn.Replace('\','/'))"
$badEnvContent | Set-Content -LiteralPath $badEnv -Encoding utf8NoBOM

try {
    & $healthcheck -ComposeFile $compose -EnvFile $currentEnv -ProjectName $project -ConfigOnly | Out-Null
    $steps.config = 'PASS'
    Invoke-Compose $currentEnv @('up', '-d', '--wait', '--wait-timeout', '180')
    & $healthcheck -ComposeFile $compose -EnvFile $currentEnv -ProjectName $project | Out-Null
    $steps.current_start = 'PASS'

    $body = @{ project = $memoryProject; content = $memoryContent; tags = @('rollback-replay') } | ConvertTo-Json -Compress
    $response = Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:$serverPort/api/memories" -Headers @{ Authorization = "Bearer $token" } -ContentType 'application/json' -Body $body -TimeoutSec 15
    if ($response.content -ne $memoryContent) { throw 'unique customer memory write did not round-trip' }
    $steps.customer_write = 'PASS'

    foreach ($service in 'postgres', 'server', 'operator-console') {
        Invoke-Compose $currentEnv @('restart', $service)
        Invoke-Compose $currentEnv @('up', '-d', '--wait', '--wait-timeout', '180')
    }
    & $healthcheck -ComposeFile $compose -EnvFile $currentEnv -ProjectName $project | Out-Null
    if (-not (Read-Memory $currentEnv)) { throw 'customer memory was lost after service restart' }
    $steps.restart_persistence = 'PASS'

    Invoke-Compose $currentEnv @('stop', 'postgres')
    & pwsh -NoProfile -File $healthcheck -ComposeFile $compose -EnvFile $currentEnv -ProjectName $project *> $null
    if ($LASTEXITCODE -eq 0) { throw 'stack readiness stayed green while PostgreSQL was stopped' }
    $steps.db_outage_not_ready = 'PASS'
    Invoke-Compose $currentEnv @('start', 'postgres')
    Invoke-Compose $currentEnv @('up', '-d', '--wait', '--wait-timeout', '180')
    & $healthcheck -ComposeFile $compose -EnvFile $currentEnv -ProjectName $project | Out-Null
    if (-not (Read-Memory $currentEnv)) { throw 'customer memory was lost after database recovery' }
    $steps.db_recovery = 'PASS'

    & $rollback -ComposeFile $compose -CurrentEnvFile $currentEnv -PreviousEnvFile $previousEnv -ProjectName $project -EvidenceFile $rollbackEvidence -MemoryProject $memoryProject -MemoryContent $memoryContent | Out-Null
    if (-not (Read-Memory $previousEnv)) { throw 'customer memory was lost after previous-release rollback' }
    $steps.previous_release_rollback = 'PASS'

    Invoke-Compose $currentEnv @('up', '-d', '--wait', '--wait-timeout', '180')
    & $healthcheck -ComposeFile $compose -EnvFile $currentEnv -ProjectName $project | Out-Null
    if (-not (Read-Memory $currentEnv)) { throw 'customer memory was lost while restoring current before failure recovery proof' }
    $steps.current_restore_before_failure = 'PASS'

    & pwsh -NoProfile -File $rollback -ComposeFile $compose -CurrentEnvFile $currentEnv -PreviousEnvFile $badEnv -ProjectName $project -EvidenceFile $failedEvidence *> $null
    if ($LASTEXITCODE -eq 0) { throw 'runtime-invalid rollback candidate was accepted' }
    $failureDecision = Get-Content -Raw $failedEvidence | ConvertFrom-Json
    if ($failureDecision.status -ne 'ROLLBACK_FAILED_CURRENT_RESTORED') { throw 'rollback failure did not restore the current release' }
    & $healthcheck -ComposeFile $compose -EnvFile $currentEnv -ProjectName $project | Out-Null
    if (-not (Read-Memory $currentEnv)) { throw 'customer memory was lost during failed rollback recovery' }
    $steps.rollback_failure_recovery = 'PASS'

    [ordered]@{
        schema_version = 1
        task = 'OPS-DEPLOY-ROLLBACK-E1'
        status = 'PASS'
        project = $project
        started_at = $startedAt.ToString('o')
        finished_at = (Get-Date).ToString('o')
        current_server = 'engram@sha256:0f678c4316e8ac5a49d917f58477d2a4a5f886eab3084924ec1ef7e1c4d769c1'
        previous_server = 'engram@sha256:a00dd8dd2bd768335915fb9de7cf54f97ed1a72538397de2b8f0de1a1cf1f968'
        postgres_major = 17
        unique_memory_sha256 = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([Text.Encoding]::UTF8.GetBytes($memoryContent))).ToLowerInvariant()
        steps = $steps
        rollback_failure_decision = [ordered]@{
            status = $failureDecision.status
            decision = $failureDecision.decision
        }
        sources = @(
            'https://docs.docker.com/reference/compose-file/services/'
            'https://docs.docker.com/compose/how-tos/use-secrets/'
            'https://www.postgresql.org/support/versioning/'
        )
        tavily = 'BLOCKED: monthly keyless cap reached; Parallel official-source search completed'
    } | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $EvidenceFile -Encoding utf8NoBOM
} finally {
    & docker compose --project-name $project --env-file $currentEnv -f $compose down -v --remove-orphans --timeout 30 2>&1 | Out-Null
    Remove-LabeledProjectResources $project
    $filter = "label=com.docker.compose.project=$project"
    if (@(Get-DockerLines @('ps', '--all', '--quiet', '--filter', $filter)).Count -ne 0 -or
        @(Get-DockerLines @('volume', 'ls', '--quiet', '--filter', $filter)).Count -ne 0 -or
        @(Get-DockerLines @('network', 'ls', '--quiet', '--filter', $filter)).Count -ne 0) { throw "Docker residue remains for $project" }
    Remove-ReplayDirectory $temp
}

Write-Output "PASS rollback replay: $EvidenceFile"
