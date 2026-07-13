[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..\..\..')).Path
$replay = Join-Path $PSScriptRoot 'replay.ps1'
$replayRoot = Join-Path $repo '.agent\tmp\rollback-replay'
$evidence = Join-Path $repo '.agent\tmp\rollback-forced-kill-evidence.json'
$process = Start-Process -FilePath 'pwsh' -ArgumentList @(
    '-NoProfile', '-File', $replay, '-EvidenceFile', $evidence
) -PassThru -WindowStyle Hidden
$ownedDirectory = $null
$project = $null
$serverID = $null

function Invoke-Scavenger {
    & pwsh -NoProfile -File $replay -ScavengeOnly
    if ($LASTEXITCODE -ne 0) { throw 'rollback replay scavenger failed' }
}

try {
    $deadline = (Get-Date).AddMinutes(3)
    while ((Get-Date) -lt $deadline) {
        if (-not $project) {
            foreach ($directory in Get-ChildItem -LiteralPath $replayRoot -Directory -Filter 'engram-e1-*' -ErrorAction SilentlyContinue) {
                $markerPath = Join-Path $directory.FullName '.engram-rollback-replay-owner.json'
                if (-not (Test-Path -LiteralPath $markerPath -PathType Leaf)) { continue }
                try { $marker = Get-Content -Raw -LiteralPath $markerPath | ConvertFrom-Json } catch { continue }
                if ([int]$marker.pid -eq $process.Id) {
                    $ownedDirectory = $directory
                    $project = $directory.Name
                    break
                }
            }
        }
        if ($project) {
            $serverID = docker ps -aq --filter "label=com.docker.compose.project=$project" --filter 'label=com.docker.compose.service=server' | Select-Object -First 1
            if ($serverID) { break }
        }
        $process.Refresh()
        if ($process.HasExited) { throw "replay exited before server ownership proof, exit=$($process.ExitCode)" }
        Start-Sleep -Milliseconds 500
    }
    if (-not $project) { throw 'timed out waiting for replay owner marker' }
    if (-not $serverID) { throw 'timed out waiting for replay server container' }

    $containerEnvironment = @(docker inspect $serverID --format '{{range .Config.Env}}{{println .}}{{end}}')
    if ($LASTEXITCODE -ne 0) { throw 'cannot inspect replay server container' }
    $token = [IO.File]::ReadAllText((Join-Path $ownedDirectory.FullName 'admin_token')).Trim()
    $dsn = [IO.File]::ReadAllText((Join-Path $ownedDirectory.FullName 'database_dsn')).Trim()
    $joinedEnvironment = $containerEnvironment -join "`n"
    if ($joinedEnvironment.Contains($token) -or $joinedEnvironment.Contains($dsn)) { throw 'secret value leaked into Docker Config.Env' }
    if (-not ($containerEnvironment -contains 'ENGRAM_AUTH_ADMIN_TOKEN_FILE=/run/secrets/admin_token') -or
        -not ($containerEnvironment -contains 'DATABASE_DSN_FILE=/run/secrets/database_dsn')) {
        throw 'secret file contract missing from Docker Config.Env'
    }
    Write-Output "INSPECT_SECRET_METADATA_PASS project=$project"

    Stop-Process -Id $process.Id -Force
    Wait-Process -Id $process.Id -ErrorAction SilentlyContinue
    Invoke-Scavenger

    $remainingContainers = @(docker ps -aq --filter "label=com.docker.compose.project=$project")
    $remainingVolumes = @(docker volume ls -q --filter "label=com.docker.compose.project=$project")
    $remainingNetworks = @(docker network ls -q --filter "label=com.docker.compose.project=$project")
    if (Test-Path -LiteralPath $ownedDirectory.FullName) { throw 'stale replay directory remained after scavenger' }
    if ($remainingContainers.Count -or $remainingVolumes.Count -or $remainingNetworks.Count) {
        throw "Docker residue remained c=$($remainingContainers.Count) v=$($remainingVolumes.Count) n=$($remainingNetworks.Count)"
    }
    Write-Output "FORCED_KILL_SCAVENGE_PASS project=$project"
} finally {
    $process.Refresh()
    if (-not $process.HasExited) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        Wait-Process -Id $process.Id -ErrorAction SilentlyContinue
    }
    if ($project) { Invoke-Scavenger | Out-Null }
    Remove-Item -LiteralPath $evidence -Force -ErrorAction SilentlyContinue
}

Write-Output 'PASS rollback interruption and secret metadata tests'
exit 0
