param(
    [string]$Container = "engram-prc-postgres",
    [string]$DatabaseUser = "engram",
    [string]$DatabasePassword = "engram",
    [string]$HostName = "127.0.0.1",
    [int]$Port = 55432
)

$ErrorActionPreference = "Stop"
$repo = (Resolve-Path (Join-Path $PSScriptRoot "..\..\..")).Path
$fixture = Join-Path $repo "tests\fixtures\pre-v5\engram-v4.5.0.sql"
$manifest = Get-Content -Raw (Join-Path $repo "tests\fixtures\pre-v5\manifest.json") | ConvertFrom-Json
$actualHash = (Get-FileHash -Algorithm SHA256 $fixture).Hash.ToLowerInvariant()
if ($actualHash -ne $manifest.fixture_sha256) {
    throw "pre-v5 fixture checksum mismatch: got $actualHash"
}
$tagObject = (git -C $repo rev-parse $manifest.source_tag).Trim()
$sourceCommit = (git -C $repo rev-parse "$($manifest.source_tag)^{}" ).Trim()
$sourceMigrationsBlob = (git -C $repo rev-parse "$($manifest.source_tag):internal/db/gorm/migrations.go").Trim()
if ($tagObject -ne $manifest.tag_object -or $sourceCommit -ne $manifest.source_commit -or $sourceMigrationsBlob -ne $manifest.source_migrations_blob) {
    throw "pre-v5 source tag provenance mismatch"
}

$stamp = "{0}_{1}" -f $PID, [DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds()
$containerFixture = "/tmp/engram-pre-v5-$stamp.sql"
docker cp $fixture "${Container}:$containerFixture" | Out-Null
if ($LASTEXITCODE -ne 0) { throw "docker cp fixture failed" }

try {
    foreach ($mode in @("happy", "interrupted")) {
        $database = "engram_pre_v5_${mode}_$stamp"
        docker exec $Container psql -U $DatabaseUser -d postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE $database OWNER $DatabaseUser" | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "create $database failed" }
        try {
            docker exec $Container psql -U $DatabaseUser -d $database -v ON_ERROR_STOP=1 -f $containerFixture | Out-Null
            if ($LASTEXITCODE -ne 0) { throw "restore $database failed" }
            $env:DATABASE_DSN = "postgres://${DatabaseUser}:${DatabasePassword}@${HostName}:${Port}/${database}?sslmode=disable"
            $env:ENGRAM_PRE_V5_TEST_MODE = $mode
            go test -tags=legacyupgrade ./internal/grpcserver -run '^TestCredentialDecryptRoundTripAfterMigration$' -count=1 -v
            if ($LASTEXITCODE -ne 0) { throw "legacyupgrade $mode gate failed" }
        }
        finally {
            docker exec $Container psql -U $DatabaseUser -d postgres -v ON_ERROR_STOP=1 -c "DROP DATABASE IF EXISTS $database WITH (FORCE)" | Out-Null
        }
    }
}
finally {
    docker exec -u 0 $Container rm -f $containerFixture | Out-Null
    Remove-Item Env:DATABASE_DSN -ErrorAction SilentlyContinue
    Remove-Item Env:ENGRAM_PRE_V5_TEST_MODE -ErrorAction SilentlyContinue
}

$residue = docker exec $Container psql -U $DatabaseUser -d postgres -Atc "SELECT count(*) FROM pg_database WHERE datname LIKE 'engram_pre_v5_%_$stamp'"
if ($residue.Trim() -ne "0") { throw "pre-v5 database residue remains: $residue" }
Write-Output "PRE_V5_UPGRADE_PASS fixture=$actualHash modes=happy,interrupted residue=0"
