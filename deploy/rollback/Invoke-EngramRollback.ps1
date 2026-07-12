[CmdletBinding()]
param(
    [Parameter(Mandatory)] [string]$ComposeFile,
    [Parameter(Mandatory)] [string]$CurrentEnvFile,
    [Parameter(Mandatory)] [string]$PreviousEnvFile,
    [Parameter(Mandatory)] [ValidatePattern('^[a-z0-9][a-z0-9_-]+$')] [string]$ProjectName,
    [Parameter(Mandatory)] [string]$EvidenceFile,
    [string]$CompatibilityFile = (Join-Path $PSScriptRoot 'compatibility.json'),
    [string]$MemoryProject,
    [string]$MemoryContent
)

$ErrorActionPreference = 'Stop'
$healthcheck = Join-Path $PSScriptRoot '..\healthcheck\Test-EngramRuntime.ps1'
$startedAt = Get-Date

function Get-EnvValue([string]$path, [string]$name) {
    foreach ($line in Get-Content -LiteralPath $path) {
        if ($line -match '^\s*(?:#|$)') { continue }
        $parts = $line -split '=', 2
        if ($parts.Count -eq 2 -and $parts[0].Trim() -eq $name) { return $parts[1].Trim() }
    }
    return $null
}

function Get-PostgresMajor([string]$image) {
    $version = (& docker run --rm --entrypoint postgres $image --version 2>&1) -join ' '
    if ($LASTEXITCODE -ne 0 -or $version -notmatch 'PostgreSQL\)\s+(\d+)\.') {
        throw 'cannot establish PostgreSQL image major version'
    }
    return [int]$Matches[1]
}

function Get-Digest([string]$image) {
    if ($image -notmatch '@sha256:([0-9a-f]{64})$') { throw 'image is not an immutable digest reference' }
    return $Matches[1]
}

function Write-Evidence([string]$status, [string]$decision, [string]$failure = '') {
    $directory = Split-Path -Parent $EvidenceFile
    if ($directory) { New-Item -ItemType Directory -Force -Path $directory | Out-Null }
    [ordered]@{
        schema_version = 1
        task = 'OPS-DEPLOY-ROLLBACK-E1'
        status = $status
        decision = $decision
        failure = $failure
        project = $ProjectName
        current_server_image = Get-EnvValue $CurrentEnvFile 'ENGRAM_SERVER_IMAGE'
        previous_server_image = Get-EnvValue $PreviousEnvFile 'ENGRAM_SERVER_IMAGE'
        started_at = $startedAt.ToString('o')
        finished_at = (Get-Date).ToString('o')
    } | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $EvidenceFile -Encoding utf8NoBOM
}

try {
    & $healthcheck -ComposeFile $ComposeFile -EnvFile $CurrentEnvFile -ProjectName $ProjectName -ConfigOnly | Out-Null
    & $healthcheck -ComposeFile $ComposeFile -EnvFile $PreviousEnvFile -ProjectName $ProjectName -ConfigOnly | Out-Null

    $compatibility = Get-Content -Raw -LiteralPath $CompatibilityFile | ConvertFrom-Json
    foreach ($side in 'current', 'previous') {
        $envPath = if ($side -eq 'current') { $CurrentEnvFile } else { $PreviousEnvFile }
        $record = $compatibility.$side
        foreach ($pair in @(
            @('ENGRAM_SERVER_IMAGE', 'server_digest'),
            @('ENGRAM_OPERATOR_IMAGE', 'operator_digest'),
            @('ENGRAM_POSTGRES_IMAGE', 'postgres_digest')
        )) {
            $actual = Get-Digest (Get-EnvValue $envPath $pair[0])
            if ($actual -ne $record.($pair[1])) { throw "$side $($pair[0]) is not in the audited rollback pair" }
        }
    }
    if ($compatibility.application_schema -ne 'byte-identical') { throw 'rollback pair lacks byte-identical application-schema evidence' }

    $currentPostgres = Get-EnvValue $CurrentEnvFile 'ENGRAM_POSTGRES_IMAGE'
    $previousPostgres = Get-EnvValue $PreviousEnvFile 'ENGRAM_POSTGRES_IMAGE'
    $currentMajor = Get-PostgresMajor $currentPostgres
    $previousMajor = Get-PostgresMajor $previousPostgres
    if ($currentMajor -ne $previousMajor) {
        throw "PostgreSQL major-version rollback is forbidden ($currentMajor -> $previousMajor); restore from a tested backup instead"
    }
    if ($currentMajor -ne [int]$compatibility.postgres_major) { throw 'runtime PostgreSQL major does not match compatibility evidence' }

    & docker compose --project-name $ProjectName --env-file $PreviousEnvFile -f $ComposeFile up -d --wait --wait-timeout 180
    if ($LASTEXITCODE -ne 0) { throw 'previous release did not become healthy' }
    $runtime = & $healthcheck -ComposeFile $ComposeFile -EnvFile $PreviousEnvFile -ProjectName $ProjectName

    if ($MemoryProject -or $MemoryContent) {
        if (-not $MemoryProject -or -not $MemoryContent) { throw 'MemoryProject and MemoryContent must be supplied together' }
        $token = Get-EnvValue $PreviousEnvFile 'ENGRAM_AUTH_ADMIN_TOKEN'
        $port = (& docker compose --project-name $ProjectName --env-file $PreviousEnvFile -f $ComposeFile port server 37777).Trim()
        $uri = 'http://127.0.0.1:' + (($port -split ':')[-1]) + '/api/memories?project=' + [uri]::EscapeDataString($MemoryProject) + '&limit=50'
        $memories = @(Invoke-RestMethod -Uri $uri -Headers @{ Authorization = "Bearer $token" } -TimeoutSec 15)
        if (-not ($memories.content -contains $MemoryContent)) { throw 'customer memory was not readable after rollback' }
    }

    Write-Evidence 'ROLLED_BACK' 'previous release healthy; customer readback preserved'
    $runtime
} catch {
    $failure = $_.Exception.Message
    & docker compose --project-name $ProjectName --env-file $CurrentEnvFile -f $ComposeFile up -d --wait --wait-timeout 180 | Out-Null
    if ($LASTEXITCODE -eq 0) {
        try {
            & $healthcheck -ComposeFile $ComposeFile -EnvFile $CurrentEnvFile -ProjectName $ProjectName | Out-Null
            Write-Evidence 'ROLLBACK_FAILED_CURRENT_RESTORED' 'retain current release and investigate before retry' $failure
        } catch {
            Write-Evidence 'ROLLBACK_AND_RESTORE_FAILED' 'stop automation; operator recovery required' ($failure + '; restore: ' + $_.Exception.Message)
        }
    } else {
        Write-Evidence 'ROLLBACK_AND_RESTORE_FAILED' 'stop automation; operator recovery required' $failure
    }
    throw
}
