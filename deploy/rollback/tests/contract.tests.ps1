[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..\..\..')).Path
$runtime = Join-Path $repo 'deploy\docker-compose.runtime.yml'
$standalone = Join-Path $repo 'deploy\docker-compose.operator-web-standalone.yml'
$healthcheck = Join-Path $repo 'deploy\healthcheck\Test-EngramRuntime.ps1'
$rollback = Join-Path $repo 'deploy\rollback\Invoke-EngramRollback.ps1'
$compatibility = Join-Path $repo 'deploy\rollback\compatibility.json'

function Assert-True([bool]$condition, [string]$message) {
    if (-not $condition) { throw $message }
}

function Assert-Match([string]$content, [string]$pattern, [string]$message) {
    Assert-True ([regex]::IsMatch($content, $pattern, 'Multiline')) $message
}

$runtimeText = Get-Content -Raw $runtime
$standaloneText = Get-Content -Raw $standalone

Assert-True (Test-Path $healthcheck) 'runtime health preflight helper is missing'
Assert-True (Test-Path $rollback) 'rollback helper is missing'
Assert-True (Test-Path $compatibility) 'rollback compatibility allowlist is missing'
Assert-Match $runtimeText '(?m)^\s+POSTGRES_PASSWORD_FILE:\s+/run/secrets/postgres_password\s*$' 'PostgreSQL password must be a mounted secret'
Assert-Match $runtimeText '(?m)^\s+ENGRAM_ENCRYPTION_KEY_FILE:\s+/run/secrets/vault_key\s*$' 'vault key must be a mounted secret'
Assert-Match $runtimeText '(?m)^\s+DATABASE_DSN_FILE:\s+/run/secrets/database_dsn\s*$' 'database DSN must be a mounted secret'
Assert-Match $runtimeText '(?m)^\s+ENGRAM_AUTH_ADMIN_TOKEN_FILE:\s+/run/secrets/admin_token\s*$' 'admin token must be a mounted secret'
Assert-True ($runtimeText -cnotmatch '(?m)^\s+DATABASE_DSN:') 'DATABASE_DSN must not be stored in container environment'
Assert-True ($runtimeText -cnotmatch '(?m)^\s+ENGRAM_AUTH_ADMIN_TOKEN:') 'admin token must not be stored in container environment'
Assert-Match $runtimeText 'http://127\.0\.0\.1:37777/api/ready' 'server healthcheck must use semantic readiness'
Assert-Match $runtimeText '(?m)^\s+mem_limit:' 'services must declare memory limits'
Assert-Match $runtimeText '(?m)^\s+pids_limit:' 'services must declare PID limits'
$postgresBlock = (($runtimeText -split '(?m)^  server:\s*$')[0] -split '(?m)^  postgres:\s*$')[-1]
Assert-True ($postgresBlock -notmatch '(?m)^\s{4}ports:') 'PostgreSQL must not publish a host port'
Assert-Match $standaloneText '\$\{ENGRAM_OPERATOR_IMAGE:\?' 'standalone operator image must be supplied explicitly'
Assert-Match $standaloneText 'http://127\.0\.0\.1:3000/api/ready' 'standalone operator healthcheck must use backend readiness'

$missing = & docker compose -f $runtime config --quiet 2>&1
Assert-True ($LASTEXITCODE -ne 0) 'compose config without required release/secrets variables must fail closed'

$suffix = [guid]::NewGuid().ToString('N')
$taggedEnv = Join-Path ([System.IO.Path]::GetTempPath()) ("engram-tagged-$suffix.env")
$validEnv = Join-Path ([System.IO.Path]::GetTempPath()) ("engram-valid-$suffix.env")
$duplicateEnv = Join-Path ([System.IO.Path]::GetTempPath()) ("engram-duplicate-$suffix.env")
$tamperedCompatibility = Join-Path ([System.IO.Path]::GetTempPath()) ("engram-compatibility-$suffix.json")
$preflightEvidence = Join-Path ([System.IO.Path]::GetTempPath()) ("engram-preflight-$suffix.json")
$secretFiles = @('postgres-password','vault-key','database-dsn','admin-token') | ForEach-Object { Join-Path ([System.IO.Path]::GetTempPath()) ("engram-$($_)-$suffix") }
try {
    Set-Content -LiteralPath $secretFiles[0] -Value 'test-password' -NoNewline
    Set-Content -LiteralPath $secretFiles[1] -Value ('a' * 64) -NoNewline
    Set-Content -LiteralPath $secretFiles[2] -Value 'postgres://engram:test-password@postgres:5432/engram?sslmode=disable' -NoNewline
    Set-Content -LiteralPath $secretFiles[3] -Value 'test-token-not-for-production' -NoNewline
    @(
        'ENGRAM_SERVER_IMAGE=engram:latest'
        'ENGRAM_OPERATOR_IMAGE=engram:latest'
        'ENGRAM_POSTGRES_IMAGE=postgres:17'
        "ENGRAM_POSTGRES_PASSWORD_SECRET_FILE=$($secretFiles[0].Replace('\','/'))"
        "ENGRAM_VAULT_KEY_SECRET_FILE=$($secretFiles[1].Replace('\','/'))"
        "ENGRAM_DATABASE_DSN_SECRET_FILE=$($secretFiles[2].Replace('\','/'))"
        "ENGRAM_AUTH_ADMIN_TOKEN_SECRET_FILE=$($secretFiles[3].Replace('\','/'))"
    ) | Set-Content -LiteralPath $taggedEnv -Encoding utf8NoBOM
    & pwsh -NoProfile -File $healthcheck -ComposeFile $runtime -EnvFile $taggedEnv -ProjectName engram-contract-red -ConfigOnly 2>$null
    Assert-True ($LASTEXITCODE -ne 0) 'tagged image references must be rejected before deployment'

    @(
        'ENGRAM_SERVER_IMAGE=engram@sha256:cf0090ef9915ba5b3f8675cf9bb0fb273497d6f6ffb72b0860219a14b7c43664'
        'ENGRAM_OPERATOR_IMAGE=engram@sha256:bf959a088be3e607d483b3d64b1747f7e65f91ea9dfbc90baa3029544e728400'
        'ENGRAM_POSTGRES_IMAGE=engram@sha256:78780f7a04ce28fcdd33ff9fcd3b4de400bc510e39a06ff3223a164dbdb4eee7'
        "ENGRAM_POSTGRES_PASSWORD_SECRET_FILE=$($secretFiles[0].Replace('\','/'))"
        "ENGRAM_VAULT_KEY_SECRET_FILE=$($secretFiles[1].Replace('\','/'))"
        "ENGRAM_DATABASE_DSN_SECRET_FILE=$($secretFiles[2].Replace('\','/'))"
        "ENGRAM_AUTH_ADMIN_TOKEN_SECRET_FILE=$($secretFiles[3].Replace('\','/'))"
    ) | Set-Content -LiteralPath $validEnv -Encoding utf8NoBOM
    Copy-Item -LiteralPath $validEnv -Destination $duplicateEnv
    Add-Content -LiteralPath $duplicateEnv -Value 'engram_server_image=engram@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
    & pwsh -NoProfile -File $healthcheck -ComposeFile $runtime -EnvFile $duplicateEnv -ProjectName engram-contract-duplicate -ConfigOnly 2>$null
    Assert-True ($LASTEXITCODE -ne 0) 'case-alias duplicate env key was accepted'

    Copy-Item -LiteralPath $compatibility -Destination $tamperedCompatibility
    Add-Content -LiteralPath $tamperedCompatibility -Value ' '
    & pwsh -NoProfile -File $rollback -ComposeFile $runtime -CurrentEnvFile $validEnv -PreviousEnvFile $validEnv -ProjectName engram-contract-preflight -EvidenceFile $preflightEvidence -CompatibilityFile $tamperedCompatibility *> $null
    Assert-True ($LASTEXITCODE -ne 0) 'tampered compatibility artifact was accepted'
    $preflight = Get-Content -Raw -LiteralPath $preflightEvidence | ConvertFrom-Json
    Assert-True ($preflight.status -eq 'ROLLBACK_REJECTED_PREFLIGHT') 'preflight rejection attempted runtime rollback or lost its decision evidence'
    Assert-True (@(& docker ps -aq --filter 'label=com.docker.compose.project=engram-contract-preflight').Count -eq 0) 'preflight rejection created a container'
} finally {
    foreach ($path in @($taggedEnv, $validEnv, $duplicateEnv, $tamperedCompatibility, $preflightEvidence) + $secretFiles) {
        Remove-Item -LiteralPath $path -Force -ErrorAction SilentlyContinue
    }
}

Write-Output 'PASS deployment and rollback contract tests'
