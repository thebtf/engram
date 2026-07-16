[CmdletBinding()]
param(
    [string]$Repository = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path,
    [string]$Artifact = '.agent/e/default-branch-authority/tests.json'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$checkerRelative = 'scripts/production-gates/assert-pr-authority-data-only.ps1'
$workflowRelative = '.github/workflows/authority-guard.yml'
$policyRelative = '.github/authority-policy.json'
$repositoryFullName = 'thebtf/engram'
$trustedActor = [ordered]@{ login='thebtf'; id=7106373; type='User' }

function Write-Utf8NoBom {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$Text)
    $parent = Split-Path -Parent $Path
    if ($parent) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }
    [System.IO.File]::WriteAllText([System.IO.Path]::GetFullPath($Path), $Text, [System.Text.UTF8Encoding]::new($false))
}

function Invoke-Git {
    param([Parameter(Mandatory)][string]$WorkingTree, [Parameter(Mandatory)][string[]]$Arguments)
    $output = @(& git -C $WorkingTree @Arguments 2>&1)
    if ($LASTEXITCODE -ne 0) { throw "git $($Arguments -join ' ') failed: $($output -join ' ')" }
    return @($output | ForEach-Object { [string]$_ })
}

function Get-GitLine {
    param([Parameter(Mandatory)][string]$WorkingTree, [Parameter(Mandatory)][string[]]$Arguments)
    $lines = @(Invoke-Git $WorkingTree $Arguments | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    if ($lines.Count -ne 1) { throw "git $($Arguments -join ' ') returned $($lines.Count) lines" }
    return [string]$lines[0].Trim()
}

function Export-GitBlob {
    param([Parameter(Mandatory)][string]$WorkingTree, [Parameter(Mandatory)][string]$Spec, [Parameter(Mandatory)][string]$Destination)
    $parent = Split-Path -Parent $Destination
    if ($parent) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }
    $start = [System.Diagnostics.ProcessStartInfo]::new()
    $start.FileName = 'git'
    $start.UseShellExecute = $false
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    foreach ($argument in @('-C', $WorkingTree, 'cat-file', 'blob', $Spec)) { [void]$start.ArgumentList.Add($argument) }
    $process = [System.Diagnostics.Process]::new()
    $process.StartInfo = $start
    if (-not $process.Start()) { throw "could not start git cat-file for '$Spec'" }
    $stream = [System.IO.File]::Open($Destination, [System.IO.FileMode]::Create, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
    try { $process.StandardOutput.BaseStream.CopyTo($stream) } finally { $stream.Dispose() }
    $stderr = $process.StandardError.ReadToEnd()
    $process.WaitForExit()
    if ($process.ExitCode -ne 0) { throw "git cat-file failed for '$Spec': $stderr" }
}

function New-Policy {
    param(
        [Parameter(Mandatory)][int]$EpochNumber,
        [AllowNull()][string]$ConsumedEpoch,
        [AllowNull()][string]$EventBaseSha,
        [Parameter(Mandatory)][object[]]$Changes,
        [Parameter(Mandatory)][AllowEmptyCollection()][object[]]$ExpectedBlobs
    )
    $epoch = 'authority-{0:d4}' -f $EpochNumber
    $normalizedConsumedEpoch = if ([string]::IsNullOrEmpty($ConsumedEpoch)) { $null } else { $ConsumedEpoch }
    $normalizedEventBaseSha = if ([string]::IsNullOrEmpty($EventBaseSha)) { $null } else { $EventBaseSha }
    return [ordered]@{
        schema_version = 1
        kind = 'engram-default-branch-authority-policy'
        repository = $repositoryFullName
        default_branch = 'main'
        protected_paths = [ordered]@{
            exact = @($policyRelative)
            prefixes = @('.github/workflows/','scripts/production-gates/')
        }
        trusted_actor = $trustedActor
        transition = [ordered]@{ consumed_epoch=$normalizedConsumedEpoch; event_base_sha=$normalizedEventBaseSha }
        active_epoch = [ordered]@{
            id = $epoch
            label = "authority-maintenance:$epoch"
            exact_changes = @($Changes)
            expected_head_blobs = @($ExpectedBlobs)
        }
    }
}

function Write-Policy {
    param([Parameter(Mandatory)][string]$Repo, [Parameter(Mandatory)]$Policy)
    Write-Utf8NoBom (Join-Path $Repo $policyRelative) (($Policy | ConvertTo-Json -Depth 100) + "`n")
}

function Get-BlobForText {
    param([Parameter(Mandatory)][string]$Repo, [Parameter(Mandatory)][string]$Text, [Parameter(Mandatory)][string]$Name)
    $file = Join-Path $Repo $Name
    Write-Utf8NoBom $file $Text
    try { return Get-GitLine $Repo @('hash-object','--no-filters',$file) }
    finally { Remove-Item -LiteralPath $file -Force }
}

function Invoke-Scenario {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][int]$PrNumber,
        [Parameter(Mandatory)][scriptblock]$BasePolicyFactory,
        [Parameter(Mandatory)][scriptblock]$HeadMutation,
        [Parameter(Mandatory)][string]$EventAction,
        [AllowEmptyString()][string]$EventLabel = '',
        [string]$ActorLogin = 'thebtf',
        [long]$ActorId = 7106373,
        [string]$ActorType = 'User',
        [AllowNull()][string]$BaseShaOverride,
        [Parameter(Mandatory)][ValidateSet('PASS','FAIL')][string]$ExpectedVerdict,
        [AllowEmptyString()][string]$ExpectedError = '',
        [switch]$UseSentinel
    )
    $scenarioRoot = Join-Path $script:tempRoot $Name
    $sourceRepo = Join-Path $scenarioRoot 'source'
    $remoteRepo = Join-Path $scenarioRoot 'remote.git'
    $runnerRepo = Join-Path $scenarioRoot 'runner'
    $trustedChecker = Join-Path $scenarioRoot 'trusted-base-checker.ps1'
    $resultArtifact = Join-Path $scenarioRoot 'result.json'
    $sentinel = Join-Path $scenarioRoot 'candidate-head-executed.txt'
    try {
        New-Item -ItemType Directory -Path $sourceRepo,$runnerRepo -Force | Out-Null
        [void](Invoke-Git $scenarioRoot @('init','--bare',$remoteRepo))
        [void](Invoke-Git $sourceRepo @('init','--initial-branch=main'))
        [void](Invoke-Git $sourceRepo @('config','user.name','Authority Test'))
        [void](Invoke-Git $sourceRepo @('config','user.email','authority@example.invalid'))
        [void](Invoke-Git $sourceRepo @('config','core.autocrlf','false'))
        [void](Invoke-Git $sourceRepo @('remote','add','origin',$remoteRepo))

        $checkerSource = Join-Path $script:repoRoot $checkerRelative
        $workflowSource = Join-Path $script:repoRoot $workflowRelative
        Write-Utf8NoBom (Join-Path $sourceRepo $checkerRelative) ([System.IO.File]::ReadAllText($checkerSource))
        Write-Utf8NoBom (Join-Path $sourceRepo $workflowRelative) ([System.IO.File]::ReadAllText($workflowSource))
        Write-Utf8NoBom (Join-Path $sourceRepo 'README.md') "fixture`n"
        $basePolicy = & $BasePolicyFactory $sourceRepo
        Write-Policy $sourceRepo $basePolicy
        [void](Invoke-Git $sourceRepo @('add','-A'))
        [void](Invoke-Git $sourceRepo @('commit','-m','fixture base'))
        $baseSha = Get-GitLine $sourceRepo @('rev-parse','HEAD')
        [void](Invoke-Git $sourceRepo @('push','origin','HEAD:refs/heads/main'))

        [void](Invoke-Git $sourceRepo @('checkout','-b',"pr-$PrNumber"))
        & $HeadMutation $sourceRepo $baseSha $sentinel
        [void](Invoke-Git $sourceRepo @('add','-A'))
        [void](Invoke-Git $sourceRepo @('commit','-m',"scenario $Name"))
        $headSha = Get-GitLine $sourceRepo @('rev-parse','HEAD')
        [void](Invoke-Git $sourceRepo @('push','origin',"HEAD:refs/pull/$PrNumber/head"))

        [void](Invoke-Git $runnerRepo @('init'))
        [void](Invoke-Git $runnerRepo @('remote','add','origin',$remoteRepo))
        [void](Invoke-Git $runnerRepo @('fetch','--no-tags','origin','+refs/heads/main:refs/authority/materialize-base'))
        $validatorBlob = Get-GitLine $runnerRepo @('rev-parse',"$baseSha`:$checkerRelative")
        Export-GitBlob $runnerRepo "$baseSha`:$checkerRelative" $trustedChecker

        $eventBaseSha = if ([string]::IsNullOrEmpty($BaseShaOverride)) { $baseSha } else { $BaseShaOverride }
        $oldSentinel = $env:AUTHORITY_SENTINEL
        $env:AUTHORITY_SENTINEL = $sentinel
        try {
            $output = @(& pwsh -NoProfile -File $trustedChecker `
                -Repository $runnerRepo `
                -Remote origin `
                -BaseRemoteRef refs/heads/main `
                -BaseSha $eventBaseSha `
                -HeadRemoteRef "refs/pull/$PrNumber/head" `
                -HeadSha $headSha `
                -ExpectedDefaultBranch main `
                -EventAction $EventAction `
                -EventLabel $EventLabel `
                -EventRepositoryFullName $repositoryFullName `
                -EventHeadRepositoryFullName $repositoryFullName `
                -ActorLogin $ActorLogin `
                -ActorId $ActorId `
                -ActorType $ActorType `
                -ExpectedValidatorGitBlob $validatorBlob `
                -Artifact $resultArtifact 2>&1)
            $exitCode = $LASTEXITCODE
        }
        finally { $env:AUTHORITY_SENTINEL = $oldSentinel }

        if (-not (Test-Path -LiteralPath $resultArtifact -PathType Leaf)) { throw "scenario did not write result artifact: $($output -join ' ')" }
        $result = Get-Content -LiteralPath $resultArtifact -Raw | ConvertFrom-Json -Depth 100
        if ([string]$result.verdict -cne $ExpectedVerdict) { throw "expected $ExpectedVerdict, observed $([string]$result.verdict): $($result.errors -join '; ')" }
        if (($ExpectedVerdict -ceq 'PASS' -and $exitCode -ne 0) -or ($ExpectedVerdict -ceq 'FAIL' -and $exitCode -eq 0)) { throw "unexpected checker exit code $exitCode for $ExpectedVerdict" }
        if (-not [bool]$result.head.treated_as_data_only -or [bool]$result.head.executed -or [bool]$result.head.checked_out) { throw 'checker artifact does not prove data-only head handling' }
        if ($ExpectedError -and (($result.errors -join "`n") -notmatch $ExpectedError)) { throw "expected error /$ExpectedError/, observed '$($result.errors -join '; ')'" }
        if ($UseSentinel -and (Test-Path -LiteralPath $sentinel)) { throw 'candidate-head validator payload executed' }
        return [ordered]@{ name=$Name; verdict='PASS'; checker_verdict=$ExpectedVerdict; exit_code=$exitCode; errors=@($result.errors) }
    }
    catch {
        return [ordered]@{ name=$Name; verdict='FAIL'; checker_verdict=$null; exit_code=$null; errors=@($_.Exception.Message) }
    }
}

$repoRoot = [System.IO.Path]::GetFullPath($Repository)
if (-not (Test-Path -LiteralPath (Join-Path $repoRoot $checkerRelative) -PathType Leaf)) { throw 'checker source is missing' }
if (-not (Test-Path -LiteralPath (Join-Path $repoRoot $workflowRelative) -PathType Leaf)) { throw 'workflow source is missing' }
if (-not (Test-Path -LiteralPath (Join-Path $repoRoot $policyRelative) -PathType Leaf)) { throw 'policy source is missing' }

$tempRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('engram-data-only-authority-tests-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tempRoot -Force | Out-Null
$results = [System.Collections.Generic.List[object]]::new()
try {
    $candidatePayload = @'
if ($env:AUTHORITY_SENTINEL) {
    [System.IO.File]::WriteAllText($env:AUTHORITY_SENTINEL, 'candidate head executed')
}
exit 0
'@
    $candidateBlob = Get-BlobForText $repoRoot $candidatePayload '.authority-candidate-payload.tmp'
    $unexpectedPayload = $candidatePayload + "# unexpected bytes`n"

    $policyOnlyBase = {
        param($Repo)
        New-Policy 1 $null $null @([ordered]@{status='M';path=$policyRelative}) @()
    }

    $results.Add((Invoke-Scenario -Name 'ordinary-product-change-passes' -PrNumber 101 -BasePolicyFactory $policyOnlyBase -HeadMutation {
        param($Repo,$BaseSha,$Sentinel)
        Write-Utf8NoBom (Join-Path $Repo 'README.md') "fixture changed`n"
    } -EventAction synchronize -ExpectedVerdict PASS))

    $results.Add((Invoke-Scenario -Name 'unapproved-protected-change-fails' -PrNumber 102 -BasePolicyFactory $policyOnlyBase -HeadMutation {
        param($Repo,$BaseSha,$Sentinel)
        Add-Content -LiteralPath (Join-Path $Repo $checkerRelative) -Value '# unapproved protected change'
    } -EventAction synchronize -ExpectedVerdict FAIL -ExpectedError 'protected authority paths require'))

    $results.Add((Invoke-Scenario -Name 'policy-only-maintenance-passes' -PrNumber 103 -BasePolicyFactory $policyOnlyBase -HeadMutation {
        param($Repo,$BaseSha,$Sentinel)
        Write-Policy $Repo (New-Policy 2 'authority-0001' $BaseSha @([ordered]@{status='M';path=$policyRelative}) @())
    } -EventAction labeled -EventLabel 'authority-maintenance:authority-0001' -ExpectedVerdict PASS))

    $results.Add((Invoke-Scenario -Name 'false-topology-claim-fails' -PrNumber 104 -BasePolicyFactory $policyOnlyBase -HeadMutation {
        param($Repo,$BaseSha,$Sentinel)
        Write-Policy $Repo (New-Policy 2 'authority-0001' ('0' * 40) @([ordered]@{status='M';path=$policyRelative}) @())
    } -EventAction labeled -EventLabel 'authority-maintenance:authority-0001' -ExpectedVerdict FAIL -ExpectedError 'event_base_sha differs'))

    $results.Add((Invoke-Scenario -Name 'coordinated-self-accept-fails' -PrNumber 105 -BasePolicyFactory $policyOnlyBase -HeadMutation {
        param($Repo,$BaseSha,$Sentinel)
        Write-Utf8NoBom (Join-Path $Repo $checkerRelative) $candidatePayload
        Write-Utf8NoBom (Join-Path $Repo $workflowRelative) "name: attacker-controlled`n"
        Write-Policy $Repo (New-Policy 2 'authority-0001' $BaseSha @(
            [ordered]@{status='M';path=$policyRelative},
            [ordered]@{status='M';path=$workflowRelative},
            [ordered]@{status='M';path=$checkerRelative}
        ) @(
            [ordered]@{path=$workflowRelative;git_blob=(Get-GitLine $Repo @('hash-object','--no-filters',(Join-Path $Repo $workflowRelative)))},
            [ordered]@{path=$checkerRelative;git_blob=(Get-GitLine $Repo @('hash-object','--no-filters',(Join-Path $Repo $checkerRelative)))}
        ))
    } -EventAction labeled -EventLabel 'authority-maintenance:authority-0001' -ExpectedVerdict FAIL -ExpectedError 'trusted base epoch status/path set' -UseSentinel))

    $predeclaredBase = {
        param($Repo)
        New-Policy 1 $null $null @(
            [ordered]@{status='M';path=$policyRelative},
            [ordered]@{status='M';path=$checkerRelative}
        ) @([ordered]@{path=$checkerRelative;git_blob=$candidateBlob})
    }
    $results.Add((Invoke-Scenario -Name 'predeclared-head-code-is-data-only' -PrNumber 106 -BasePolicyFactory $predeclaredBase -HeadMutation {
        param($Repo,$BaseSha,$Sentinel)
        Write-Utf8NoBom (Join-Path $Repo $checkerRelative) $candidatePayload
        Write-Policy $Repo (New-Policy 2 'authority-0001' $BaseSha @([ordered]@{status='M';path=$policyRelative}) @())
    } -EventAction labeled -EventLabel 'authority-maintenance:authority-0001' -ExpectedVerdict PASS -UseSentinel))

    $results.Add((Invoke-Scenario -Name 'unexpected-predeclared-blob-fails' -PrNumber 107 -BasePolicyFactory $predeclaredBase -HeadMutation {
        param($Repo,$BaseSha,$Sentinel)
        Write-Utf8NoBom (Join-Path $Repo $checkerRelative) $unexpectedPayload
        Write-Policy $Repo (New-Policy 2 'authority-0001' $BaseSha @([ordered]@{status='M';path=$policyRelative}) @())
    } -EventAction labeled -EventLabel 'authority-maintenance:authority-0001' -ExpectedVerdict FAIL -ExpectedError 'preapproval'))

    $results.Add((Invoke-Scenario -Name 'head-policy-cannot-widen-authority' -PrNumber 108 -BasePolicyFactory $policyOnlyBase -HeadMutation {
        param($Repo,$BaseSha,$Sentinel)
        Write-Policy $Repo (New-Policy 2 'authority-0001' $BaseSha @(
            [ordered]@{status='M';path=$policyRelative},
            [ordered]@{status='M';path='README.md'}
        ) @([ordered]@{path='README.md';git_blob=('1' * 40)}))
    } -EventAction labeled -EventLabel 'authority-maintenance:authority-0001' -ExpectedVerdict FAIL -ExpectedError 'outside protected authority'))

    $results.Add((Invoke-Scenario -Name 'stale-event-base-fails' -PrNumber 109 -BasePolicyFactory $policyOnlyBase -HeadMutation {
        param($Repo,$BaseSha,$Sentinel)
        Write-Utf8NoBom (Join-Path $Repo 'README.md') "stale base event`n"
    } -EventAction synchronize -BaseShaOverride ('0' * 40) -ExpectedVerdict FAIL -ExpectedError 'fetched default-branch base differs'))
}
finally {
    $tempPrefix = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath()).TrimEnd('\','/') + [System.IO.Path]::DirectorySeparatorChar
    $resolved = [System.IO.Path]::GetFullPath($tempRoot)
    if (-not $resolved.StartsWith($tempPrefix, [System.StringComparison]::OrdinalIgnoreCase) -or -not ([System.IO.Path]::GetFileName($resolved)).StartsWith('engram-data-only-authority-tests-', [System.StringComparison]::Ordinal)) {
        throw "refusing unsafe test cleanup '$resolved'"
    }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}

$failures = @($results | Where-Object { [string]$_.verdict -cne 'PASS' })
$summary = [ordered]@{
    schema_version = 1
    test = 'default-branch-data-only-authority'
    verdict = $(if ($failures.Count -eq 0) { 'PASS' } else { 'FAIL' })
    scenario_count = $results.Count
    passed = $results.Count - $failures.Count
    failed = $failures.Count
    scenarios = @($results)
}
Write-Utf8NoBom $Artifact (($summary | ConvertTo-Json -Depth 100) + "`n")
Write-Output "default-branch-data-only-authority-tests verdict=$($summary.verdict) passed=$($summary.passed)/$($summary.scenario_count) artifact=$Artifact"
if ($failures.Count -gt 0) {
    foreach ($failure in $failures) { Write-Error "$([string]$failure.name): $($failure.errors -join '; ')" }
    exit 1
}
exit 0
