[CmdletBinding()]
param(
    [Parameter(Mandatory)] [string]$ComposeFile,
    [Parameter(Mandatory)] [string]$EnvFile,
    [Parameter(Mandatory)] [ValidatePattern('^[a-z0-9][a-z0-9_-]+$')] [string]$ProjectName,
    [switch]$ConfigOnly
)

$ErrorActionPreference = 'Stop'
$digestPattern = '^.+@sha256:[0-9a-f]{64}$'

function Require([bool]$condition, [string]$message) {
    if (-not $condition) { throw $message }
}

function Get-EnvValue([string]$name) {
    foreach ($line in Get-Content -LiteralPath $EnvFile) {
        if ($line -match '^\s*(?:#|$)') { continue }
        $parts = $line -split '=', 2
        if ($parts.Count -eq 2 -and $parts[0].Trim() -eq $name) { return $parts[1].Trim() }
    }
    return $null
}

function Get-Service([object]$config, [string]$name) {
    $property = $config.services.PSObject.Properties[$name]
    Require ($null -ne $property) "compose service '$name' is missing"
    return $property.Value
}

function Require-Hardened([object]$service, [string]$name) {
    Require ($service.read_only -eq $true) "$name root filesystem is writable"
    Require (@($service.cap_drop) -contains 'ALL') "$name does not drop ALL capabilities"
    Require ((@($service.security_opt) -join ' ') -match 'no-new-privileges') "$name lacks no-new-privileges"
    Require ([int64]$service.mem_limit -gt 0) "$name has no memory limit"
    Require ([double]$service.cpus -gt 0) "$name has no CPU limit"
    Require ([int]$service.pids_limit -gt 0) "$name has no PID limit"
    Require ($service.restart -eq 'unless-stopped') "$name restart policy is not unless-stopped"
}

foreach ($name in 'ENGRAM_SERVER_IMAGE', 'ENGRAM_OPERATOR_IMAGE', 'ENGRAM_POSTGRES_IMAGE') {
    $value = Get-EnvValue $name
    Require (-not [string]::IsNullOrWhiteSpace($value)) "$name is missing"
    Require ($value -match $digestPattern) "$name must be an immutable @sha256 digest reference"
}

$rendered = & docker compose --project-name $ProjectName --env-file $EnvFile -f $ComposeFile config --format json 2>&1
if ($LASTEXITCODE -ne 0) { throw 'docker compose config rejected the runtime manifest' }
$config = ($rendered -join "`n") | ConvertFrom-Json

$postgres = Get-Service $config 'postgres'
$server = Get-Service $config 'server'
$operator = Get-Service $config 'operator-console'
Require-Hardened $postgres 'postgres'
Require-Hardened $server 'server'
Require-Hardened $operator 'operator-console'
Require ($null -eq $postgres.PSObject.Properties['ports'] -or @($postgres.ports).Count -eq 0) 'PostgreSQL publishes a host port'
Require ((@($postgres.healthcheck.test) -join ' ') -match 'pg_isready') 'PostgreSQL healthcheck is not pg_isready'
Require ((@($server.healthcheck.test) -join ' ') -match 'http://127\.0\.0\.1:37777/api/ready') 'server healthcheck does not use /api/ready'
Require ((@($operator.healthcheck.test) -join ' ') -match 'http://127\.0\.0\.1:3000/api/ready') 'operator healthcheck does not proxy /api/ready'
Require ($postgres.environment.POSTGRES_PASSWORD_FILE -eq '/run/secrets/postgres_password') 'PostgreSQL does not consume its password secret'
Require ($server.environment.ENGRAM_ENCRYPTION_KEY_FILE -eq '/run/secrets/vault_key') 'server does not consume its vault-key secret'
Require (-not [string]::IsNullOrWhiteSpace($server.environment.DATABASE_DSN)) 'DATABASE_DSN is empty'
Require (-not [string]::IsNullOrWhiteSpace($server.environment.ENGRAM_AUTH_ADMIN_TOKEN)) 'ENGRAM_AUTH_ADMIN_TOKEN is empty'

if ($ConfigOnly) {
    [pscustomobject]@{ status = 'CONFIG_VALID'; project = $ProjectName; compose = (Resolve-Path $ComposeFile).Path }
    return
}

foreach ($name in 'postgres', 'server', 'operator-console') {
    $containerID = (& docker compose --project-name $ProjectName --env-file $EnvFile -f $ComposeFile ps -q $name).Trim()
    Require ($LASTEXITCODE -eq 0 -and $containerID) "$name container is absent"
    $state = (& docker inspect --format '{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' $containerID).Trim()
    Require ($LASTEXITCODE -eq 0 -and $state -eq 'running|healthy') "$name is not running and healthy"
}

$serverPort = (& docker compose --project-name $ProjectName --env-file $EnvFile -f $ComposeFile port server 37777).Trim()
Require ($LASTEXITCODE -eq 0 -and $serverPort) 'server port is not published'
$serverURL = 'http://127.0.0.1:' + (($serverPort -split ':')[-1]) + '/api/ready'
$ready = Invoke-RestMethod -Uri $serverURL -TimeoutSec 10
Require ($ready.status -eq 'ready') 'server /api/ready response is not ready'

[pscustomobject]@{ status = 'RUNTIME_READY'; project = $ProjectName; endpoint = $serverURL }
