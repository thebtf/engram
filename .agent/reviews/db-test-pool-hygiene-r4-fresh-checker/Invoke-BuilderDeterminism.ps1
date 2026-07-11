param(
    [Parameter(Mandatory = $true)]
    [string]$RepositoryRoot,

    [Parameter(Mandatory = $true)]
    [string]$OutputPath
)

$ErrorActionPreference = 'Stop'
$utf8NoBom = [Text.UTF8Encoding]::new($false)
$utf8Strict = [Text.UTF8Encoding]::new($false, $true)
$maker = 'ce6a40d72fc39932ccbc4b949647f321b91f70c3'
$evidenceParent = '331b5b195a967e7f27dca94038a3480c9afcc84f'
$manifestPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/MANIFEST.json'
$sumsPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/SHA256SUMS.txt'
$adversarialPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/adversarial-proof.json'
$verifierPath = '.agent/reports/evidence/production-ready/db-test-pool-hygiene/verifier-proof.json'
$resolvedRepository = (Resolve-Path -LiteralPath $RepositoryRoot).Path
$builder = Join-Path $resolvedRepository '.agent/reports/evidence/production-ready/db-test-pool-hygiene/Build-DBPoolHygieneEvidence.ps1'
$tempBase = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
$tempPrefix = 'engram-dbph-r4-builder-check-'
$tempRoot = Join-Path $tempBase ($tempPrefix + [Guid]::NewGuid().ToString('N'))

if (-not $tempRoot.StartsWith($tempBase, [StringComparison]::OrdinalIgnoreCase) -or
    -not [IO.Path]::GetFileName($tempRoot).StartsWith($tempPrefix, [StringComparison]::Ordinal)) {
    throw "unsafe temp root: $tempRoot"
}
[IO.Directory]::CreateDirectory($tempRoot) | Out-Null

function Invoke-GitRaw {
    param(
        [Parameter(Mandatory = $true)][string]$WorkingDirectory,
        [Parameter(Mandatory = $true)][string[]]$Arguments
    )
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = 'git'
    $start.WorkingDirectory = $WorkingDirectory
    $start.UseShellExecute = $false
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    foreach ($argument in $Arguments) { $start.ArgumentList.Add($argument) }
    $process = [Diagnostics.Process]::Start($start)
    $stream = [IO.MemoryStream]::new()
    $process.StandardOutput.BaseStream.CopyTo($stream)
    $stderr = $process.StandardError.ReadToEnd()
    $process.WaitForExit()
    if ($process.ExitCode -ne 0) { throw "git $($Arguments -join ' ') failed: $stderr" }
    return $stream.ToArray()
}

function Invoke-GitText {
    param([Parameter(Mandatory = $true)][string]$WorkingDirectory, [Parameter(Mandatory = $true)][string[]]$Arguments)
    return $utf8Strict.GetString((Invoke-GitRaw -WorkingDirectory $WorkingDirectory -Arguments $Arguments)).Trim()
}

function Get-Hash {
    param([Parameter(Mandatory = $true)][byte[]]$Bytes)
    return [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($Bytes)).ToLowerInvariant()
}

function Get-FileHashStrict {
    param([Parameter(Mandatory = $true)][string]$Path)
    return Get-Hash -Bytes ([IO.File]::ReadAllBytes($Path))
}

function New-CaseRepository {
    param([Parameter(Mandatory = $true)][string]$Name)
    $path = Join-Path $tempRoot $Name
    [IO.Directory]::CreateDirectory($path) | Out-Null
    Invoke-GitRaw -WorkingDirectory $path -Arguments @('init', '-q') | Out-Null
    Invoke-GitRaw -WorkingDirectory $path -Arguments @('config', 'core.autocrlf', 'false') | Out-Null
    Invoke-GitRaw -WorkingDirectory $path -Arguments @('config', 'core.longpaths', 'true') | Out-Null
    $gitDir = Invoke-GitText -WorkingDirectory $path -Arguments @('rev-parse', '--absolute-git-dir')
    $info = Join-Path $gitDir 'objects/info'
    [IO.Directory]::CreateDirectory($info) | Out-Null
    [IO.File]::WriteAllText((Join-Path $info 'alternates'), $script:sourceObjects + "`n", $utf8NoBom)
    Invoke-GitRaw -WorkingDirectory $path -Arguments @('update-ref', 'refs/heads/evidence', $evidenceParent) | Out-Null
    Invoke-GitRaw -WorkingDirectory $path -Arguments @('symbolic-ref', 'HEAD', 'refs/heads/evidence') | Out-Null
    Invoke-GitRaw -WorkingDirectory $path -Arguments @('read-tree', $script:makerTree) | Out-Null
    Invoke-GitRaw -WorkingDirectory $path -Arguments @('checkout-index', '-a', '-f') | Out-Null
    return $path
}

function Invoke-Builder {
    param([Parameter(Mandatory = $true)][string]$CaseRepository, [switch]$Seed)
    $arguments = @('-NoProfile', '-File', $builder, '-RepositoryRoot', $CaseRepository)
    if ($Seed) { $arguments += '-SeedDynamicProofs' }
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = 'pwsh'
    $start.WorkingDirectory = $CaseRepository
    $start.UseShellExecute = $false
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    foreach ($argument in $arguments) { $start.ArgumentList.Add($argument) }
    $process = [Diagnostics.Process]::Start($start)
    $stdout = $process.StandardOutput.ReadToEnd()
    $stderr = $process.StandardError.ReadToEnd()
    $process.WaitForExit()
    if ($process.ExitCode -ne 0) { throw "builder failed exit $($process.ExitCode): $stderr`n$stdout" }
    return ($stdout.Trim() | ConvertFrom-Json)
}

$fatal = $null
$cleanupError = $null
$result = $null
try {
    $script:makerTree = Invoke-GitText -WorkingDirectory $resolvedRepository -Arguments @('rev-parse', "$maker^{tree}")
    $objectsText = Invoke-GitText -WorkingDirectory $resolvedRepository -Arguments @('rev-parse', '--git-path', 'objects')
    $script:sourceObjects = if ([IO.Path]::IsPathRooted($objectsText)) {
        [IO.Path]::GetFullPath($objectsText)
    } else {
        [IO.Path]::GetFullPath((Join-Path $resolvedRepository $objectsText))
    }

    $seedRepository = New-CaseRepository -Name 'seed'
    $seedManifest = Join-Path $seedRepository $manifestPath
    $seedSums = Join-Path $seedRepository $sumsPath
    $seedAdversarial = Join-Path $seedRepository $adversarialPath
    $seedVerifier = Join-Path $seedRepository $verifierPath
    $manifestBeforeSeed = Get-FileHashStrict $seedManifest
    $sumsBeforeSeed = Get-FileHashStrict $seedSums
    $seedRun1 = Invoke-Builder -CaseRepository $seedRepository -Seed
    $seedAdversarial1 = Get-FileHashStrict $seedAdversarial
    $seedVerifier1 = Get-FileHashStrict $seedVerifier
    $seedRun2 = Invoke-Builder -CaseRepository $seedRepository -Seed
    $seedAdversarial2 = Get-FileHashStrict $seedAdversarial
    $seedVerifier2 = Get-FileHashStrict $seedVerifier
    $manifestAfterSeed = Get-FileHashStrict $seedManifest
    $sumsAfterSeed = Get-FileHashStrict $seedSums

    $buildRepository = New-CaseRepository -Name 'build'
    $buildManifest = Join-Path $buildRepository $manifestPath
    $buildSums = Join-Path $buildRepository $sumsPath
    $expectedManifest = Get-Hash -Bytes (Invoke-GitRaw -WorkingDirectory $buildRepository -Arguments @('show', ":$manifestPath"))
    $expectedSums = Get-Hash -Bytes (Invoke-GitRaw -WorkingDirectory $buildRepository -Arguments @('show', ":$sumsPath"))
    $buildRun1 = Invoke-Builder -CaseRepository $buildRepository
    $builtManifest1 = Get-FileHashStrict $buildManifest
    $builtSums1 = Get-FileHashStrict $buildSums
    $buildRun2 = Invoke-Builder -CaseRepository $buildRepository
    $builtManifest2 = Get-FileHashStrict $buildManifest
    $builtSums2 = Get-FileHashStrict $buildSums

    $checks = [ordered]@{
        seed_status_run1 = [string]$seedRun1.status -ceq 'SEEDED_DYNAMIC_PROOFS'
        seed_status_run2 = [string]$seedRun2.status -ceq 'SEEDED_DYNAMIC_PROOFS'
        seed_adversarial_byte_deterministic = $seedAdversarial1 -ceq $seedAdversarial2
        seed_verifier_byte_deterministic = $seedVerifier1 -ceq $seedVerifier2
        seed_manifest_unchanged = $manifestBeforeSeed -ceq $manifestAfterSeed
        seed_checksum_unchanged = $sumsBeforeSeed -ceq $sumsAfterSeed
        build_status_run1 = [string]$buildRun1.status -ceq 'BUILT'
        build_status_run2 = [string]$buildRun2.status -ceq 'BUILT'
        build_manifest_matches_committed = $builtManifest1 -ceq $expectedManifest
        build_checksum_matches_committed = $builtSums1 -ceq $expectedSums
        build_manifest_byte_deterministic = $builtManifest1 -ceq $builtManifest2
        build_checksum_byte_deterministic = $builtSums1 -ceq $builtSums2
    }
    $failed = @($checks.GetEnumerator() | Where-Object { -not $_.Value } | ForEach-Object { $_.Key })
    $result = [ordered]@{
        schema_version = 1
        target = $maker
        target_tree = $script:makerTree
        status = if ($failed.Count -eq 0) { 'PASS' } else { 'FAIL' }
        checks = $checks
        failed_checks = [object[]]$failed
        hashes = [ordered]@{
            seed_adversarial = $seedAdversarial1
            seed_verifier = $seedVerifier1
            committed_manifest = $expectedManifest
            committed_checksum = $expectedSums
            built_manifest = $builtManifest1
            built_checksum = $builtSums1
        }
    }
}
catch {
    $fatal = $_.Exception.Message
}
finally {
    try {
        $resolvedTempRoot = [IO.Path]::GetFullPath($tempRoot)
        if (-not $resolvedTempRoot.StartsWith($tempBase, [StringComparison]::OrdinalIgnoreCase) -or
            -not [IO.Path]::GetFileName($resolvedTempRoot).StartsWith($tempPrefix, [StringComparison]::Ordinal)) {
            throw "unsafe cleanup target: $resolvedTempRoot"
        }
        if ([IO.Directory]::Exists($resolvedTempRoot)) {
            Get-ChildItem -LiteralPath $resolvedTempRoot -Recurse -Force | ForEach-Object { $_.Attributes = [IO.FileAttributes]::Normal }
            [IO.Directory]::Delete($resolvedTempRoot, $true)
        }
    }
    catch { $cleanupError = $_.Exception.Message }
}

$tempRemoved = -not [IO.Directory]::Exists($tempRoot)
if ($null -eq $result) {
    $result = [ordered]@{
        schema_version = 1
        target = $maker
        target_tree = $script:makerTree
        status = 'FAIL'
        checks = [ordered]@{}
        failed_checks = @('fatal_error')
        hashes = [ordered]@{}
    }
}
$result['fatal_error'] = $fatal
$result['temp_root_removed'] = $tempRemoved
$result['cleanup_error'] = $cleanupError
if ($null -ne $fatal -or $null -ne $cleanupError -or -not $tempRemoved) { $result['status'] = 'FAIL' }
$json = (($result | ConvertTo-Json -Depth 12) -replace "`r`n", "`n") + "`n"
$resolvedOutput = [IO.Path]::GetFullPath($OutputPath)
[IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($resolvedOutput)) | Out-Null
[IO.File]::WriteAllText($resolvedOutput, $json, $utf8NoBom)
$json
if ([string]$result.status -cne 'PASS') { exit 1 }
exit 0
