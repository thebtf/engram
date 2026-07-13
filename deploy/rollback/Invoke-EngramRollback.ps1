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
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$healthcheck = Join-Path $PSScriptRoot '..\healthcheck\Test-EngramRuntime.ps1'
$startedAt = Get-Date
$trustedCompatibilitySha256 = '3580475819e3df708aa3948de0d58f615659602824611af2ddea024d9aaa9ac5'
$rollbackStarted = $false

function Read-EnvValues([string]$path) {
    $values = [Collections.Generic.Dictionary[string,string]]::new([StringComparer]::OrdinalIgnoreCase)
    foreach ($line in Get-Content -LiteralPath $path) {
        if ($line -match '^\s*(?:#|$)') { continue }
        $parts = $line -split '=', 2
        if ($parts.Count -ne 2 -or $parts[0].Trim() -cnotmatch '^[A-Za-z_][A-Za-z0-9_]*$') { throw "invalid env entry in $path" }
        $name = $parts[0].Trim()
        if ($values.ContainsKey($name)) { throw "duplicate env key '$name' in $path" }
        $values.Add($name, $parts[1].Trim())
    }
    return $values
}

function Get-EnvValue([string]$path, [string]$name) { return (Read-EnvValues $path)[$name] }

function Get-CanonicalSha256([string]$path) {
    $text = [IO.File]::ReadAllText($path).Replace("`r`n", "`n").Replace("`r", "`n")
    return [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([Text.UTF8Encoding]::new($false).GetBytes($text))).ToLowerInvariant()
}

function Get-PathListSha256([string[]]$paths) {
    $text = if ($paths.Count -eq 0) { '' } else { ($paths -join "`n") + "`n" }
    return [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([Text.UTF8Encoding]::new($false).GetBytes($text))).ToLowerInvariant()
}

function Get-SecretValue([string]$envPath, [string]$fileVariable) {
    $secretPath = Get-EnvValue $envPath $fileVariable
    if ([string]::IsNullOrWhiteSpace($secretPath) -or -not (Test-Path -LiteralPath $secretPath -PathType Leaf)) { throw "$fileVariable does not name a readable secret file" }
    $value = (Get-Content -Raw -LiteralPath $secretPath).Trim()
    if ([string]::IsNullOrWhiteSpace($value)) { throw "$fileVariable secret file is empty" }
    return $value
}

function Get-EvidenceEnvValue([string]$envPath, [string]$name) {
    try { return Get-EnvValue $envPath $name } catch { return '<invalid-env>' }
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
        current_server_image = Get-EvidenceEnvValue $CurrentEnvFile 'ENGRAM_SERVER_IMAGE'
        previous_server_image = Get-EvidenceEnvValue $PreviousEnvFile 'ENGRAM_SERVER_IMAGE'
        compatibility_sha256 = $trustedCompatibilitySha256
        started_at = $startedAt.ToString('o')
        finished_at = (Get-Date).ToString('o')
    } | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $EvidenceFile -Encoding utf8NoBOM
}

try {
    [void](Read-EnvValues $CurrentEnvFile)
    [void](Read-EnvValues $PreviousEnvFile)
    & $healthcheck -ComposeFile $ComposeFile -EnvFile $CurrentEnvFile -ProjectName $ProjectName -ConfigOnly | Out-Null
    & $healthcheck -ComposeFile $ComposeFile -EnvFile $PreviousEnvFile -ProjectName $ProjectName -ConfigOnly | Out-Null

    if ((Get-CanonicalSha256 $CompatibilityFile) -cne $trustedCompatibilitySha256) { throw 'rollback compatibility file is not the trusted release artifact' }
    $compatibility = Get-Content -Raw -LiteralPath $CompatibilityFile | ConvertFrom-Json
    if ([int]$compatibility.schema_version -ne 2) { throw 'unsupported rollback compatibility schema' }
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

    $schemaEvidence = $compatibility.application_schema_evidence
    if ($schemaEvidence.base_revision -cne $compatibility.previous.revision -or $schemaEvidence.head_revision -cne $compatibility.current.revision) {
        throw 'application-schema evidence revision binding drifted'
    }
    foreach ($revision in @($schemaEvidence.base_revision, $schemaEvidence.head_revision)) {
        if ([string]$revision -cnotmatch '^[0-9a-f]{40}$') { throw 'compatibility revision is not a full commit SHA' }
        & git -C $repoRoot cat-file -e "${revision}^{commit}" 2>$null
        if ($LASTEXITCODE -ne 0) { throw "compatibility revision is unavailable: $revision" }
    }
    $schemaPaths = @(git -C $repoRoot diff --name-only "$($schemaEvidence.base_revision)..$($schemaEvidence.head_revision)")
    if ($LASTEXITCODE -ne 0) { throw 'cannot reproduce application-schema evidence' }
    if ((Get-PathListSha256 $schemaPaths) -cne [string]$schemaEvidence.path_list_sha256) { throw 'application-schema evidence path digest drifted' }
    if (@($schemaPaths | Where-Object { $_ -match '^(cmd|internal|pkg|migrations)/' }).Count -ne 0) { throw 'rollback pair changes application or schema paths' }

    $replayEvidence = $compatibility.customer_replay_evidence
    if ([string]$replayEvidence.path -notmatch '^[A-Za-z0-9._/-]+$' -or [string]$replayEvidence.path -match '(^|/)\.\.(/|$)') { throw 'customer replay evidence path is unsafe' }
    $replayPath = [IO.Path]::GetFullPath((Join-Path $repoRoot ([string]$replayEvidence.path)))
    if (-not $replayPath.StartsWith($repoRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) { throw 'customer replay evidence escapes repository root' }
    if (-not (Test-Path -LiteralPath $replayPath -PathType Leaf) -or (Get-CanonicalSha256 $replayPath) -cne [string]$replayEvidence.sha256) {
        throw 'customer replay evidence hash drifted'
    }

    $currentPostgres = Get-EnvValue $CurrentEnvFile 'ENGRAM_POSTGRES_IMAGE'
    $previousPostgres = Get-EnvValue $PreviousEnvFile 'ENGRAM_POSTGRES_IMAGE'
    $currentMajor = Get-PostgresMajor $currentPostgres
    $previousMajor = Get-PostgresMajor $previousPostgres
    if ($currentMajor -ne $previousMajor) {
        throw "PostgreSQL major-version rollback is forbidden ($currentMajor -> $previousMajor); restore from a tested backup instead"
    }
    if ($currentMajor -ne [int]$compatibility.postgres_major) { throw 'runtime PostgreSQL major does not match compatibility evidence' }

    $rollbackStarted = $true
    & docker compose --project-name $ProjectName --env-file $PreviousEnvFile -f $ComposeFile up -d --wait --wait-timeout 180
    if ($LASTEXITCODE -ne 0) { throw 'previous release did not become healthy' }
    $runtime = & $healthcheck -ComposeFile $ComposeFile -EnvFile $PreviousEnvFile -ProjectName $ProjectName

    if ($MemoryProject -or $MemoryContent) {
        if (-not $MemoryProject -or -not $MemoryContent) { throw 'MemoryProject and MemoryContent must be supplied together' }
        $token = Get-SecretValue $PreviousEnvFile 'ENGRAM_AUTH_ADMIN_TOKEN_SECRET_FILE'
        $port = (& docker compose --project-name $ProjectName --env-file $PreviousEnvFile -f $ComposeFile port server 37777).Trim()
        $uri = 'http://127.0.0.1:' + (($port -split ':')[-1]) + '/api/memories?project=' + [uri]::EscapeDataString($MemoryProject) + '&limit=50'
        $memories = @(Invoke-RestMethod -Uri $uri -Headers @{ Authorization = "Bearer $token" } -TimeoutSec 15)
        if (-not ($memories.content -contains $MemoryContent)) { throw 'customer memory was not readable after rollback' }
    }

    Write-Evidence 'ROLLED_BACK' 'previous release healthy; customer readback preserved'
    $runtime
} catch {
    $failure = $_.Exception.Message
    if (-not $rollbackStarted) {
        Write-Evidence 'ROLLBACK_REJECTED_PREFLIGHT' 'no runtime mutation; repair evidence or env input before retry' $failure
        throw
    }
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
