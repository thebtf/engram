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
Assert-Match $runtimeText '\$\{DATABASE_DSN:\?' 'DATABASE_DSN must fail closed'
Assert-Match $runtimeText '\$\{ENGRAM_AUTH_ADMIN_TOKEN:\?' 'admin token must fail closed'
Assert-Match $runtimeText 'http://127\.0\.0\.1:37777/api/ready' 'server healthcheck must use semantic readiness'
Assert-Match $runtimeText '(?m)^\s+mem_limit:' 'services must declare memory limits'
Assert-Match $runtimeText '(?m)^\s+pids_limit:' 'services must declare PID limits'
$postgresBlock = (($runtimeText -split '(?m)^  server:\s*$')[0] -split '(?m)^  postgres:\s*$')[-1]
Assert-True ($postgresBlock -notmatch '(?m)^\s{4}ports:') 'PostgreSQL must not publish a host port'
Assert-Match $standaloneText '\$\{ENGRAM_OPERATOR_IMAGE:\?' 'standalone operator image must be supplied explicitly'
Assert-Match $standaloneText 'http://127\.0\.0\.1:3000/api/ready' 'standalone operator healthcheck must use backend readiness'

$missing = & docker compose -f $runtime config --quiet 2>&1
Assert-True ($LASTEXITCODE -ne 0) 'compose config without required release/secrets variables must fail closed'

$taggedEnv = Join-Path ([System.IO.Path]::GetTempPath()) ('engram-tagged-' + [guid]::NewGuid().ToString('N') + '.env')
try {
    @(
        'ENGRAM_SERVER_IMAGE=engram:latest'
        'ENGRAM_OPERATOR_IMAGE=engram:latest'
        'ENGRAM_POSTGRES_IMAGE=postgres:17'
        'DATABASE_DSN=postgres://engram:test@postgres:5432/engram?sslmode=disable'
        'ENGRAM_AUTH_ADMIN_TOKEN=test-token-not-for-production'
        'POSTGRES_PASSWORD_FILE=C:/nonexistent/postgres_password'
        'ENGRAM_VAULT_KEY_FILE=C:/nonexistent/vault_key'
    ) | Set-Content -LiteralPath $taggedEnv -Encoding utf8NoBOM
    & pwsh -NoProfile -File $healthcheck -ComposeFile $runtime -EnvFile $taggedEnv -ProjectName engram-contract-red -ConfigOnly 2>$null
    Assert-True ($LASTEXITCODE -ne 0) 'tagged image references must be rejected before deployment'
} finally {
    Remove-Item -LiteralPath $taggedEnv -Force -ErrorAction SilentlyContinue
}

Write-Output 'PASS deployment and rollback contract tests'
