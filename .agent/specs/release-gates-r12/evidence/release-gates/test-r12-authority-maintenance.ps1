[CmdletBinding()]
param(
    [string]$Repository = '.',
    [string]$Artifact = '.agent/specs/release-gates-r12/evidence/release-gates/maintenance-simulation.json'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Write-Utf8NoBom {
    param([string]$Path, [string]$Text)
    $parent = Split-Path -Parent $Path
    if ($parent) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }
    [System.IO.File]::WriteAllText([System.IO.Path]::GetFullPath($Path), $Text, [System.Text.UTF8Encoding]::new($false))
}

function Invoke-Captured {
    param([string]$FilePath, [string[]]$Arguments, [string]$WorkingDirectory)
    $start = [System.Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $FilePath
    $start.UseShellExecute = $false
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    if ($WorkingDirectory) { $start.WorkingDirectory = $WorkingDirectory }
    foreach ($argument in $Arguments) { [void]$start.ArgumentList.Add($argument) }
    $process = [System.Diagnostics.Process]::new()
    $process.StartInfo = $start
    if (-not $process.Start()) { throw "could not start '$FilePath'" }
    $stdout = $process.StandardOutput.ReadToEndAsync()
    $stderr = $process.StandardError.ReadToEndAsync()
    $process.WaitForExit()
    return [pscustomobject][ordered]@{
        exit_code = $process.ExitCode
        stdout = $stdout.GetAwaiter().GetResult()
        stderr = $stderr.GetAwaiter().GetResult()
    }
}

function Invoke-Git {
    param([string]$WorkingTree, [string[]]$Arguments, [switch]$AllowFailure)
    $result = Invoke-Captured git (@('-C', $WorkingTree) + $Arguments)
    if (-not $AllowFailure -and $result.exit_code -ne 0) {
        throw "git $($Arguments -join ' ') failed: $($result.stderr) $($result.stdout)"
    }
    return $result
}

function Get-GitLine {
    param([string]$WorkingTree, [string[]]$Arguments)
    $result = Invoke-Git $WorkingTree $Arguments
    return @($result.stdout -split "`r?`n" | Where-Object { $_ })[-1].Trim()
}

function Copy-FixtureFile {
    param([string]$SourceRoot, [string]$TargetRoot, [string]$RelativePath)
    $source = Join-Path $SourceRoot $RelativePath
    $target = Join-Path $TargetRoot $RelativePath
    if (-not (Test-Path -LiteralPath $source -PathType Leaf)) { throw "fixture source is missing: $RelativePath" }
    $parent = Split-Path -Parent $target
    if ($parent) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }
    [System.IO.File]::Copy($source, $target, $true)
}

function Export-GitBlob {
    param([string]$WorkingTree, [string]$Spec, [string]$Destination)
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
    if (-not $process.Start()) { throw "could not export '$Spec'" }
    $stream = [System.IO.File]::Open($Destination, [System.IO.FileMode]::Create, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
    try { $process.StandardOutput.BaseStream.CopyTo($stream) } finally { $stream.Dispose() }
    $stderr = $process.StandardError.ReadToEnd()
    $process.WaitForExit()
    if ($process.ExitCode -ne 0) { throw "git cat-file failed for '$Spec': $stderr" }
}

function Get-CanonicalJsonText {
    param([AllowNull()]$Value)
    if ($null -eq $Value) { return 'null' }
    if ($Value -is [string]) { return ($Value | ConvertTo-Json -Compress) }
    if ($Value -is [bool]) { return $(if ($Value) { 'true' } else { 'false' }) }
    if ($Value -is [pscustomobject]) {
        [string[]]$names = @($Value.PSObject.Properties.Name)
        [Array]::Sort($names, [System.StringComparer]::Ordinal)
        $parts = foreach ($name in $names) { "$(($name | ConvertTo-Json -Compress)):$(Get-CanonicalJsonText $Value.$name)" }
        return '{' + ($parts -join ',') + '}'
    }
    if ($Value -is [System.Collections.IDictionary]) {
        [string[]]$names = @($Value.Keys | ForEach-Object { [string]$_ })
        [Array]::Sort($names, [System.StringComparer]::Ordinal)
        $parts = foreach ($name in $names) { "$(($name | ConvertTo-Json -Compress)):$(Get-CanonicalJsonText $Value[$name])" }
        return '{' + ($parts -join ',') + '}'
    }
    if ($Value -is [System.Collections.IEnumerable]) {
        $parts = foreach ($item in $Value) { Get-CanonicalJsonText $item }
        return '[' + ($parts -join ',') + ']'
    }
    return ($Value | ConvertTo-Json -Compress)
}

function Get-SemanticDigest {
    param([AllowNull()]$Value)
    $bytes = [System.Text.UTF8Encoding]::new($false).GetBytes((Get-CanonicalJsonText $Value))
    return [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant()
}

function Get-CanonicalFileSha256 {
    param([Parameter(Mandatory)][string]$Path)
    $text = [System.IO.File]::ReadAllText([System.IO.Path]::GetFullPath($Path))
    $canonical = ($text -replace "`r`n", "`n") -replace "`r", "`n"
    return [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData([System.Text.UTF8Encoding]::new($false).GetBytes($canonical))).ToLowerInvariant()
}

function Invoke-Scenario {
    param(
        [string]$Name,
        [string]$Validator,
        [string]$AuditRepository,
        [string]$Remote,
        [string]$BaseSha,
        [int]$PullRequestNumber,
        [string]$HeadSha,
        [string]$ValidatorBlob,
        [bool]$ExpectPass,
        [string]$OutputDirectory,
        [switch]$Maintenance,
        [string]$ActorLogin = 'thebtf',
        [string]$ApprovalEpoch,
        [string]$ExpectedErrorPattern
    )
    $scenarioArtifact = Join-Path $OutputDirectory "$Name.json"
    $arguments = @(
        '-NoProfile', '-File', $Validator,
        '-Repository', $AuditRepository,
        '-Remote', $Remote,
        '-BaseRemoteRef', 'refs/heads/main',
        '-BaseSha', $BaseSha,
        '-HeadRemoteRef', "refs/pull/$PullRequestNumber/head",
        '-HeadSha', $HeadSha,
        '-ExpectedDefaultBranch', 'main',
        '-ExpectedValidatorGitBlob', $ValidatorBlob,
        '-Artifact', $scenarioArtifact
    )
    if ($Maintenance) {
        if ($ApprovalEpoch -notmatch '^r12-[0-9]{4}$') { throw "scenario '$Name' requires one exact R12 approval epoch" }
        $arguments += @(
            '-EventRepositoryFullName', 'thebtf/engram',
            '-EventHeadRepositoryFullName', 'thebtf/engram',
            '-ActorLogin', $ActorLogin,
            '-ActorId', '7106373',
            '-ActorType', 'User',
            '-AuthorAssociation', 'OWNER',
            '-ApprovalLabel', "authority-maintenance:$ApprovalEpoch",
            '-ApprovalEpoch', $ApprovalEpoch
        )
    }
    $process = Invoke-Captured pwsh $arguments
    if (-not (Test-Path -LiteralPath $scenarioArtifact -PathType Leaf)) { throw "scenario '$Name' produced no artifact: $($process.stderr)" }
    $record = Get-Content -LiteralPath $scenarioArtifact -Raw | ConvertFrom-Json -Depth 100
    $passed = $process.exit_code -eq 0 -and [string]$record.verdict -ceq 'PASS'
    if ($passed -ne $ExpectPass) { throw "scenario '$Name' expected pass=$ExpectPass, got exit=$($process.exit_code) verdict=$($record.verdict): $(@($record.errors) -join '; ')" }
    $errorText = @($record.errors) -join '; '
    if (-not $ExpectPass -and $ExpectedErrorPattern -and $errorText -notmatch $ExpectedErrorPattern) { throw "scenario '$Name' failed for wrong reason: $errorText" }
    if (-not [bool]$record.head.treated_as_data_only -or [bool]$record.head.executed -or [bool]$record.head.checked_out) { throw "scenario '$Name' violated data-only head handling" }
    return [pscustomobject][ordered]@{
        name = $Name
        expected = if ($ExpectPass) { 'PASS' } else { 'FAIL' }
        observed = [string]$record.verdict
        exit_code = $process.exit_code
        errors = @($record.errors)
    }
}

function Reset-Fixture {
    param([string]$FixtureRepository, [string]$BaseSha, [string]$Name)
    [void](Invoke-Git $FixtureRepository @('checkout', '-f', '-B', "fixture/$Name", $BaseSha))
    [void](Invoke-Git $FixtureRepository @('reset', '--hard', $BaseSha))
    [void](Invoke-Git $FixtureRepository @('clean', '-f', '-d', '-x'))
}

function Push-Head {
    param([string]$FixtureRepository, [int]$PullRequestNumber, [string]$Message)
    [void](Invoke-Git $FixtureRepository @('add', '-A'))
    [void](Invoke-Git $FixtureRepository @('commit', '-m', $Message))
    $head = Get-GitLine $FixtureRepository @('rev-parse', 'HEAD')
    [void](Invoke-Git $FixtureRepository @('push', '--force', 'origin', "HEAD:refs/pull/$PullRequestNumber/head"))
    return $head
}

function New-MaintenanceHead {
    param(
        [string]$FixtureRepository,
        [string]$BaseSha,
        [int]$PullRequestNumber,
        [string]$Name,
        [scriptblock]$ManifestMutation,
        [scriptblock]$ContractMutation,
        [scriptblock]$FileMutation
    )
    Reset-Fixture $FixtureRepository $BaseSha $Name
    $contractPath = '.agent/plans/2026-07-10-engram-production-ready-active-diff-contracts.json'
    $contractFile = Join-Path $FixtureRepository $contractPath
    $contract = Get-Content -LiteralPath $contractFile -Raw | ConvertFrom-Json -Depth 100
    $maintenance = $contract.control_plane_maintenance
    $baseEpoch = [string]$maintenance.active_epoch.epoch
    $epochMatch = [regex]::Match($baseEpoch, '^r12-([0-9]{4})$')
    if (-not $epochMatch.Success) { throw "fixture base has invalid epoch '$baseEpoch'" }
    $successorNumber = [int]$epochMatch.Groups[1].Value + 1
    if ($successorNumber -gt 9999) { throw 'fixture successor epoch overflow' }
    $successorEpoch = 'r12-{0:D4}' -f $successorNumber
    [object[]]$changes = @($maintenance.active_epoch.exact_changes)
    [string[]]$nonContainerPaths = @($changes | ForEach-Object { [string]$_.path } | Where-Object { $_ -cne $contractPath })
    $planPath = '.agent/plans/2026-07-10-engram-production-ready-master-plan.md'
    $statePath = '.agent/plans/2026-07-10-engram-production-ready-ownership-state.json'
    $scopeMapPath = '.agent/plans/2026-07-10-engram-production-ready-scope-map.json'
    if ($planPath -cin $nonContainerPaths) {
        [System.IO.File]::AppendAllText((Join-Path $FixtureRepository $planPath), "`n<!-- R12 successor fixture $Name -->`n", [System.Text.UTF8Encoding]::new($false))
    }
    if ($scopeMapPath -cin $nonContainerPaths) {
        [System.IO.File]::AppendAllText((Join-Path $FixtureRepository $scopeMapPath), "`n ", [System.Text.UTF8Encoding]::new($false))
    }
    if ($statePath -cin $nonContainerPaths) {
        $stateObject = Get-Content -LiteralPath (Join-Path $FixtureRepository $statePath) -Raw | ConvertFrom-Json -Depth 100
        $stateObject.plan.sha256 = Get-CanonicalFileSha256 (Join-Path $FixtureRepository $planPath)
        $stateObject.scope_map.sha256 = Get-CanonicalFileSha256 (Join-Path $FixtureRepository $scopeMapPath)
        Write-Utf8NoBom (Join-Path $FixtureRepository $statePath) (($stateObject | ConvertTo-Json -Depth 100) + "`n")
    }
    elseif ($planPath -cin $nonContainerPaths -or $scopeMapPath -cin $nonContainerPaths) {
        throw 'fixture cannot change plan/scope without the ownership-state binding path'
    }
    foreach ($path in @($nonContainerPaths | Where-Object { $_ -cnotin @($planPath, $statePath, $scopeMapPath) })) {
        $entry = @($changes | Where-Object { [string]$_.path -ceq $path })[0]
        $target = Join-Path $FixtureRepository $path
        if ([string]$entry.status -ceq 'A') {
            if (Test-Path -LiteralPath $target) { throw "fixture add path already exists: $path" }
            Write-Utf8NoBom $target "# R12 successor fixture $Name`n"
        }
        else {
            if (-not (Test-Path -LiteralPath $target -PathType Leaf)) { throw "fixture modify path is missing: $path" }
            [System.IO.File]::AppendAllText($target, "`n# R12 successor fixture $Name`n", [System.Text.UTF8Encoding]::new($false))
        }
    }
    if ($FileMutation) { & $FileMutation $FixtureRepository }
    [string[]]$validatorPaths = @(
        'scripts/production-gates/assert-pr-authority-guard.ps1',
        'scripts/production-gates/assert-active-candidate-path-authority.ps1',
        'scripts/production-gates/assert-pr-authority-maintenance.ps1',
        'scripts/production-gates/assert-plan-path-ownership.ps1'
    )
    [object[]]$currentValidatorBlobs = @($validatorPaths | ForEach-Object {
        [pscustomobject][ordered]@{ path = $_; git_blob = Get-GitLine $FixtureRepository @('rev-parse', "$BaseSha`:$($_)") }
    })
    [object[]]$successorValidatorBlobs = @($validatorPaths | ForEach-Object {
        [pscustomobject][ordered]@{ path = $_; git_blob = Get-GitLine $FixtureRepository @('hash-object', '--no-filters', (Join-Path $FixtureRepository $_)) }
    })
    [object[]]$protectedBlobs = @($nonContainerPaths | ForEach-Object {
        $path = $_
        $entry = @($changes | Where-Object { [string]$_.path -ceq $path })[0]
        $mode = '100644'
        if ([string]$entry.status -ceq 'M') {
            $treeLine = Get-GitLine $FixtureRepository @('ls-tree', $BaseSha, '--', $path)
            if ($treeLine -notmatch '^(100644|100755) ') { throw "fixture base mode is not regular for '$path': $treeLine" }
            $mode = [string]$Matches[1]
        }
        [pscustomobject][ordered]@{ status = [string]$entry.status; path = $path; mode = $mode; git_blob = Get-GitLine $FixtureRepository @('hash-object', '--no-filters', (Join-Path $FixtureRepository $path)) }
    })
    $created = [DateTimeOffset]::UtcNow
    $manifest = [pscustomobject][ordered]@{
        schema_version = 2
        reason = "synthetic accepted R12 transition $Name"
        created_at = $created.ToString('yyyy-MM-ddTHH:mm:ssZ')
        expires_at = $created.AddHours(1).ToString('yyyy-MM-ddTHH:mm:ssZ')
        event_base_sha = $BaseSha
        current_validator_blobs = $currentValidatorBlobs
        successor_validator_blobs = $successorValidatorBlobs
        protected_head_blobs_except_manifest_container = $protectedBlobs
        exact_changes = $changes
        approval_epoch = $baseEpoch
        successor_epoch = $successorEpoch
    }
    if ($ManifestMutation) { & $ManifestMutation $manifest }
    $manifestSha = Get-SemanticDigest $manifest
    $successor = $maintenance.active_epoch | ConvertTo-Json -Depth 100 | ConvertFrom-Json -Depth 100
    $successor.epoch = $successorEpoch
    $successor.reason = 'bounded successor fixture'
    $successor.not_before = $created.ToString('yyyy-MM-ddTHH:mm:ssZ')
    $successor.expires_at = $created.AddDays(30).ToString('yyyy-MM-ddTHH:mm:ssZ')
    $successor.approval.label = "authority-maintenance:$successorEpoch"
    $successor.approval.approval_epoch = $successorEpoch
    $maintenance.transition_manifest = $manifest
    $maintenance.consumed_epochs = @($maintenance.consumed_epochs) + @([pscustomobject][ordered]@{
        epoch = $baseEpoch
        state = 'consumed'
        consumed_at = $manifest.created_at
        event_base_sha = $BaseSha
        manifest_sha256 = $manifestSha
        successor_epoch = $successorEpoch
    })
    $maintenance.active_epoch = $successor
    if ($ContractMutation) { & $ContractMutation $contract }
    Write-Utf8NoBom $contractFile (($contract | ConvertTo-Json -Depth 100) + "`n")
    $head = Push-Head $FixtureRepository $PullRequestNumber "fixture: $Name"
    return [pscustomobject][ordered]@{ head=$head; approval_epoch=$baseEpoch; successor_epoch=$successorEpoch }
}

$root = [System.IO.Path]::GetFullPath($Repository)
$artifactPath = [System.IO.Path]::GetFullPath((Join-Path $root $Artifact))
$startedAt = [DateTimeOffset]::UtcNow
$tempRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('engram-r12-authority-' + [guid]::NewGuid().ToString('N'))
$results = [System.Collections.Generic.List[object]]::new()
$errorText = $null
$cleanupVerified = $false
$ordinaryAuthorizationSelfTestPassed = $false

try {
    $ordinarySelfTest = Invoke-Captured pwsh @('-NoProfile', '-File', (Join-Path $root 'scripts/production-gates/assert-pr-authority-guard.ps1'), '-SelfTest')
    if ($ordinarySelfTest.exit_code -ne 0 -or ($ordinarySelfTest.stdout -join "`n") -notmatch 'SELFTEST PASS') { throw "ordinary authorization self-test failed: $($ordinarySelfTest.stderr -join '; ')" }
    $ordinaryAuthorizationSelfTestPassed = $true
    $fixture = Join-Path $tempRoot 'fixture'
    $remote = Join-Path $tempRoot 'origin.git'
    $audit = Join-Path $tempRoot 'audit'
    $output = Join-Path $tempRoot 'results'
    New-Item -ItemType Directory -Path $fixture, $audit, $output -Force | Out-Null
    [void](Invoke-Git $fixture @('init', '--initial-branch=main'))
    [void](Invoke-Git $fixture @('config', 'user.name', 'R12 Fixture'))
    [void](Invoke-Git $fixture @('config', 'user.email', 'r12-fixture@example.invalid'))
    [void](Invoke-Git $fixture @('config', 'core.autocrlf', 'false'))
    [string[]]$fixtureFiles = @(
        'scripts/production-gates/assert-pr-authority-guard.ps1',
        'scripts/production-gates/assert-active-candidate-path-authority.ps1',
        'scripts/production-gates/assert-pr-authority-maintenance.ps1',
        'scripts/production-gates/assert-plan-path-ownership.ps1',
        '.github/workflows/authority-guard.yml',
        '.github/workflows/test.yml',
        '.agent/plans/2026-07-10-engram-production-ready-active-diff-contracts.json',
        '.agent/plans/2026-07-10-engram-production-ready-master-plan.md',
        '.agent/plans/2026-07-10-engram-production-ready-ownership-state.json',
        '.agent/plans/2026-07-10-engram-production-ready-scope-map.json',
        '.agent/specs/release-gates-r12/evidence/plan-governance/test-r12-plan-governance.ps1',
        '.agent/specs/release-gates-r12/evidence/plan-governance/path-envelope.json',
        '.agent/specs/release-gates-r12/evidence/plan-governance/fixed-point-proof.json',
        '.agent/specs/release-gates-r12/evidence/plan-governance/authority-snapshot.json'
    )
    foreach ($path in $fixtureFiles) { Copy-FixtureFile $root $fixture $path }
    foreach ($workflowPath in @('.github/workflows/authority-guard.yml', '.github/workflows/test.yml')) {
        $workflowFile = Join-Path $fixture $workflowPath
        $workflowText = [System.IO.File]::ReadAllText($workflowFile)
        $workflowText = [regex]::Replace($workflowText, '(?m)^on:\s*$', 'on: # parser fixture root-key comment', 1)
        $workflowText = [regex]::Replace($workflowText, '(?m)^(?<indent>\s*)contents:\s*read\s*$', {
            param($match)
            return $match.Value + "`n" + $match.Groups['indent'].Value + '# parser fixture permission comment'
        })
        $workflowText = [regex]::Replace($workflowText, '(?m)^  authority-successor-selftest:\s*$', '  authority-successor-selftest: # parser fixture job comment', 1)
        Write-Utf8NoBom $workflowFile $workflowText
    }
    Write-Utf8NoBom (Join-Path $fixture 'internal/worker/handlers_hooks_crystallization_integration_test.go') "package worker`n`n// base fixture`n"
    [void](Invoke-Git $fixture @('add', '-A'))
    [void](Invoke-Git $fixture @('commit', '-m', 'fixture: trusted R12 base'))
    $baseSha = Get-GitLine $fixture @('rev-parse', 'HEAD')
    $bare = Invoke-Captured git @('init', '--bare', $remote)
    if ($bare.exit_code -ne 0) { throw "bare remote init failed: $($bare.stderr)" }
    [void](Invoke-Git $fixture @('remote', 'add', 'origin', $remote))
    [void](Invoke-Git $fixture @('push', 'origin', 'main:refs/heads/main'))
    [void](Invoke-Git $audit @('init', '--initial-branch=audit'))

    $ordinaryPath = 'scripts/production-gates/assert-pr-authority-guard.ps1'
    $maintenancePath = 'scripts/production-gates/assert-pr-authority-maintenance.ps1'
    $ordinaryValidator = Join-Path $tempRoot 'trusted-ordinary.ps1'
    $maintenanceValidator = Join-Path $tempRoot 'trusted-maintenance.ps1'
    Export-GitBlob $fixture "$baseSha`:$ordinaryPath" $ordinaryValidator
    Export-GitBlob $fixture "$baseSha`:$maintenancePath" $maintenanceValidator
    $ordinaryBlob = Get-GitLine $fixture @('rev-parse', "$baseSha`:$ordinaryPath")
    $maintenanceBlob = Get-GitLine $fixture @('rev-parse', "$baseSha`:$maintenancePath")

    Reset-Fixture $fixture $baseSha 'ordinary-authorized'
    [System.IO.File]::AppendAllText((Join-Path $fixture 'internal/worker/handlers_hooks_crystallization_integration_test.go'), "// authorized change`n", [System.Text.UTF8Encoding]::new($false))
    $ordinaryHead = Push-Head $fixture 201 'fixture: ordinary authorized'
    $results.Add((Invoke-Scenario 'ordinary-authorized' $ordinaryValidator $audit $remote $baseSha 201 $ordinaryHead $ordinaryBlob $true $output))

    Reset-Fixture $fixture $baseSha 'ordinary-protected-rejected'
    [System.IO.File]::AppendAllText((Join-Path $fixture '.github/workflows/test.yml'), "`n# hostile protected mutation`n", [System.Text.UTF8Encoding]::new($false))
    $protectedHead = Push-Head $fixture 202 'fixture: protected mutation'
    $results.Add((Invoke-Scenario 'ordinary-protected-rejected' $ordinaryValidator $audit $remote $baseSha 202 $protectedHead $ordinaryBlob $false $output -ExpectedErrorPattern 'protected authority'))

    Reset-Fixture $fixture $baseSha 'ordinary-plan-governance-evidence-rejected'
    [System.IO.File]::AppendAllText((Join-Path $fixture '.agent/specs/release-gates-r12/evidence/plan-governance/path-envelope.json'), "`n ", [System.Text.UTF8Encoding]::new($false))
    $planGovernanceHead = Push-Head $fixture 219 'fixture: protected plan-governance evidence mutation'
    $results.Add((Invoke-Scenario 'ordinary-plan-governance-evidence-rejected' $ordinaryValidator $audit $remote $baseSha 219 $planGovernanceHead $ordinaryBlob $false $output -ExpectedErrorPattern 'protected authority'))

    Reset-Fixture $fixture $baseSha 'ordinary-type-base'
    $candidatePath = 'internal/worker/handlers_hooks_crystallization_integration_test.go'
    $linkPayload = Join-Path $tempRoot 'link-payload.txt'
    Write-Utf8NoBom $linkPayload "target.txt`n"
    $linkBlob = Get-GitLine $fixture @('hash-object', '-w', $linkPayload)
    [void](Invoke-Git $fixture @('update-index', '--add', '--cacheinfo', '120000', $linkBlob, $candidatePath))
    [void](Invoke-Git $fixture @('commit', '-m', 'fixture: candidate symlink base'))
    $typeBaseSha = Get-GitLine $fixture @('rev-parse', 'HEAD')
    [void](Invoke-Git $fixture @('push', '--force', 'origin', "$typeBaseSha`:refs/heads/main"))
    [void](Invoke-Git $fixture @('checkout', '-f', '-B', 'fixture/ordinary-type-change', $typeBaseSha))
    $candidateFile = Join-Path $fixture $candidatePath
    if (Test-Path -LiteralPath $candidateFile) { Remove-Item -LiteralPath $candidateFile -Force }
    Write-Utf8NoBom $candidateFile "package worker`n`n// hostile regular replacement`n"
    $typeChangeHead = Push-Head $fixture 207 'fixture: type change to regular blob'
    $results.Add((Invoke-Scenario 'ordinary-type-change-rejected' $ordinaryValidator $audit $remote $typeBaseSha 207 $typeChangeHead $ordinaryBlob $false $output -ExpectedErrorPattern 'only literal add/modify'))
    [void](Invoke-Git $fixture @('push', '--force', 'origin', "$baseSha`:refs/heads/main"))

    $validHead = New-MaintenanceHead $fixture $baseSha 203 'maintenance-valid'
    $results.Add((Invoke-Scenario 'maintenance-valid' $maintenanceValidator $audit $remote $baseSha 203 $validHead.head $maintenanceBlob $true $output -Maintenance -ApprovalEpoch $validHead.approval_epoch))
    $results.Add((Invoke-Scenario 'maintenance-wrong-actor' $maintenanceValidator $audit $remote $baseSha 203 $validHead.head $maintenanceBlob $false $output -Maintenance -ApprovalEpoch $validHead.approval_epoch -ActorLogin 'attacker' -ExpectedErrorPattern 'actor identity'))

    $selfReferenceHead = New-MaintenanceHead $fixture $baseSha 204 'maintenance-self-reference' {
        param($manifest)
        $manifest | Add-Member -NotePropertyName event_head_sha -NotePropertyValue ('0' * 40)
    }
    $results.Add((Invoke-Scenario 'maintenance-self-reference-rejected' $maintenanceValidator $audit $remote $baseSha 204 $selfReferenceHead.head $maintenanceBlob $false $output -Maintenance -ApprovalEpoch $selfReferenceHead.approval_epoch -ExpectedErrorPattern 'property set drifted'))

    $wrongBlobHead = New-MaintenanceHead $fixture $baseSha 205 'maintenance-wrong-blob' {
        param($manifest)
        $manifest.protected_head_blobs_except_manifest_container[0].git_blob = '0000000000000000000000000000000000000000'
    }
    $results.Add((Invoke-Scenario 'maintenance-wrong-blob-rejected' $maintenanceValidator $audit $remote $baseSha 205 $wrongBlobHead.head $maintenanceBlob $false $output -Maintenance -ApprovalEpoch $wrongBlobHead.approval_epoch -ExpectedErrorPattern 'blob inventory differs'))

    $topLevelRewriteHead = New-MaintenanceHead $fixture $baseSha 206 'maintenance-top-level-rewrite' -ContractMutation {
        param($contract)
        $contract.status_classes.current = @($contract.status_classes.current | Select-Object -Skip 1)
    }
    $results.Add((Invoke-Scenario 'maintenance-top-level-rewrite-rejected' $maintenanceValidator $audit $remote $baseSha 206 $topLevelRewriteHead.head $maintenanceBlob $false $output -Maintenance -ApprovalEpoch $topLevelRewriteHead.approval_epoch -ExpectedErrorPattern 'status class|status_classes'))

    $checkoutWorkflowHead = New-MaintenanceHead $fixture $baseSha 209 'maintenance-authority-checkout' -FileMutation {
        param($repository)
        [System.IO.File]::AppendAllText((Join-Path $repository '.github/workflows/authority-guard.yml'), "`n      - uses: actions/checkout@34e114876b0b11c390a56381ad16ebd13914f8d5`n", [System.Text.UTF8Encoding]::new($false))
    }
    $results.Add((Invoke-Scenario 'maintenance-authority-checkout-rejected' $maintenanceValidator $audit $remote $baseSha 209 $checkoutWorkflowHead.head $maintenanceBlob $false $output -Maintenance -ApprovalEpoch $checkoutWorkflowHead.approval_epoch -ExpectedErrorPattern 'forbidden privileged surface.*actions/checkout'))

    $secretWorkflowHead = New-MaintenanceHead $fixture $baseSha 210 'maintenance-test-secret' -FileMutation {
        param($repository)
        [System.IO.File]::AppendAllText((Join-Path $repository '.github/workflows/test.yml'), "`n# `${{ secrets.HOSTILE_FIXTURE }}`n", [System.Text.UTF8Encoding]::new($false))
    }
    $results.Add((Invoke-Scenario 'maintenance-test-secret-rejected' $maintenanceValidator $audit $remote $baseSha 210 $secretWorkflowHead.head $maintenanceBlob $false $output -Maintenance -ApprovalEpoch $secretWorkflowHead.approval_epoch -ExpectedErrorPattern 'may not reference repository secrets'))

    $unpinnedActionHead = New-MaintenanceHead $fixture $baseSha 211 'maintenance-unpinned-action' -FileMutation {
        param($repository)
        [System.IO.File]::AppendAllText((Join-Path $repository '.github/workflows/authority-guard.yml'), "`n      - uses: hostile/example@main`n", [System.Text.UTF8Encoding]::new($false))
    }
    $results.Add((Invoke-Scenario 'maintenance-unpinned-shorthand-action-rejected' $maintenanceValidator $audit $remote $baseSha 211 $unpinnedActionHead.head $maintenanceBlob $false $output -Maintenance -ApprovalEpoch $unpinnedActionHead.approval_epoch -ExpectedErrorPattern 'not pinned to a full immutable SHA'))

    $unapprovedPinnedActionHead = New-MaintenanceHead $fixture $baseSha 217 'maintenance-unapproved-pinned-action' -FileMutation {
        param($repository)
        [System.IO.File]::AppendAllText((Join-Path $repository '.github/workflows/authority-guard.yml'), "`n      - uses: hostile/example@aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa`n", [System.Text.UTF8Encoding]::new($false))
    }
    $results.Add((Invoke-Scenario 'maintenance-unapproved-pinned-action-rejected' $maintenanceValidator $audit $remote $baseSha 217 $unapprovedPinnedActionHead.head $maintenanceBlob $false $output -Maintenance -ApprovalEpoch $unapprovedPinnedActionHead.approval_epoch -ExpectedErrorPattern 'outside the audited allowlist'))

    $runInjectionHead = New-MaintenanceHead $fixture $baseSha 220 'maintenance-privileged-run-injection' -FileMutation {
        param($repository)
        $path = Join-Path $repository '.github/workflows/authority-guard.yml'
        $text = [System.IO.File]::ReadAllText($path)
        $needle = "          Set-StrictMode -Version Latest"
        $mutated = $text.Replace($needle, $needle + "`n          Invoke-Expression 'hostile candidate-controlled command'")
        if ($mutated -ceq $text) { throw 'fixture did not inject a privileged run command' }
        Write-Utf8NoBom $path $mutated
    }
    $results.Add((Invoke-Scenario 'maintenance-privileged-run-injection-rejected' $maintenanceValidator $audit $remote $baseSha 220 $runInjectionHead.head $maintenanceBlob $false $output -Maintenance -ApprovalEpoch $runInjectionHead.approval_epoch -ExpectedErrorPattern 'privileged workflow skeleton or run commands differ'))

    $writePermissionHead = New-MaintenanceHead $fixture $baseSha 212 'maintenance-write-permission' -FileMutation {
        param($repository)
        $path = Join-Path $repository '.github/workflows/test.yml'
        $text = [System.IO.File]::ReadAllText($path)
        $mutated = [regex]::Replace($text, '(?m)^permissions:\r?\n  contents: read[ \t]*\r?$', 'permissions: write-all', 1)
        if ($mutated -ceq $text) { throw 'fixture did not mutate the workflow-level permissions block' }
        Write-Utf8NoBom $path $mutated
    }
    $results.Add((Invoke-Scenario 'maintenance-test-write-permission-rejected' $maintenanceValidator $audit $remote $baseSha 212 $writePermissionHead.head $maintenanceBlob $false $output -Maintenance -ApprovalEpoch $writePermissionHead.approval_epoch -ExpectedErrorPattern 'workflow-level contents: read|permissions must use block form'))

    $privilegedTriggerHead = New-MaintenanceHead $fixture $baseSha 213 'maintenance-test-target-trigger' -FileMutation {
        param($repository)
        $path = Join-Path $repository '.github/workflows/test.yml'
        $text = [System.IO.File]::ReadAllText($path)
        $mutated = [regex]::Replace($text, '(?m)^  pull_request:\s*$', '  pull_request_target:', 1)
        if ($mutated -ceq $text) { throw 'fixture did not mutate the pull_request trigger' }
        Write-Utf8NoBom $path $mutated
    }
    $results.Add((Invoke-Scenario 'maintenance-test-target-trigger-rejected' $maintenanceValidator $audit $remote $baseSha 213 $privilegedTriggerHead.head $maintenanceBlob $false $output -Maintenance -ApprovalEpoch $privilegedTriggerHead.approval_epoch -ExpectedErrorPattern 'may not use pull_request_target'))

    $commentSeparatedTriggerHead = New-MaintenanceHead $fixture $baseSha 221 'maintenance-test-comment-separated-target-trigger' -FileMutation {
        param($repository)
        $path = Join-Path $repository '.github/workflows/test.yml'
        $text = [System.IO.File]::ReadAllText($path)
        $mutated = [regex]::Replace($text, '(?m)^(  pull_request:\s*(?:#.*)?\r?\n)', "`$1# hostile root comment separator`n  pull_request_target:`n", 1)
        if ($mutated -ceq $text) { throw 'fixture did not insert the comment-separated pull_request_target trigger' }
        Write-Utf8NoBom $path $mutated
    }
    $results.Add((Invoke-Scenario 'maintenance-test-comment-separated-target-trigger-rejected' $maintenanceValidator $audit $remote $baseSha 221 $commentSeparatedTriggerHead.head $maintenanceBlob $false $output -Maintenance -ApprovalEpoch $commentSeparatedTriggerHead.approval_epoch -ExpectedErrorPattern 'may not use pull_request_target'))

    $wrongTypeHead = New-MaintenanceHead $fixture $baseSha 214 'maintenance-wrong-json-type' {
        param($manifest)
        $manifest.schema_version = '2'
    }
    $results.Add((Invoke-Scenario 'maintenance-json-numeric-string-rejected' $maintenanceValidator $audit $remote $baseSha 214 $wrongTypeHead.head $maintenanceBlob $false $output -Maintenance -ApprovalEpoch $wrongTypeHead.approval_epoch -ExpectedErrorPattern 'must be the JSON integer 2'))

    $doubleSlashPathHead = New-MaintenanceHead $fixture $baseSha 222 'maintenance-double-slash-path' {
        param($manifest)
        $manifest.exact_changes[0].path = ([string]$manifest.exact_changes[0].path).Replace('/', '//')
    }
    $results.Add((Invoke-Scenario 'maintenance-double-slash-path-rejected' $maintenanceValidator $audit $remote $baseSha 222 $doubleSlashPathHead.head $maintenanceBlob $false $output -Maintenance -ApprovalEpoch $doubleSlashPathHead.approval_epoch -ExpectedErrorPattern 'non-canonical path'))

    $duplicateSuccessorPathHead = New-MaintenanceHead $fixture $baseSha 215 'maintenance-duplicate-successor-path' -ContractMutation {
        param($contract)
        $first = $contract.control_plane_maintenance.active_epoch.exact_changes[0]
        $opposite = if ([string]$first.status -ceq 'M') { 'A' } else { 'M' }
        $contract.control_plane_maintenance.active_epoch.exact_changes = @($contract.control_plane_maintenance.active_epoch.exact_changes) + @([pscustomobject][ordered]@{ status=$opposite; path=[string]$first.path })
    }
    $results.Add((Invoke-Scenario 'maintenance-duplicate-successor-path-rejected' $maintenanceValidator $audit $remote $baseSha 215 $duplicateSuccessorPathHead.head $maintenanceBlob $false $output -Maintenance -ApprovalEpoch $duplicateSuccessorPathHead.approval_epoch -ExpectedErrorPattern 'duplicate path'))

    $shortSuccessorHead = New-MaintenanceHead $fixture $baseSha 216 'maintenance-short-successor-window' -ContractMutation {
        param($contract)
        $contract.control_plane_maintenance.active_epoch.expires_at = [DateTimeOffset]::UtcNow.AddMinutes(10).ToString('yyyy-MM-ddTHH:mm:ssZ')
    }
    $results.Add((Invoke-Scenario 'maintenance-short-successor-window-rejected' $maintenanceValidator $audit $remote $baseSha 216 $shortSuccessorHead.head $maintenanceBlob $false $output -Maintenance -ApprovalEpoch $shortSuccessorHead.approval_epoch -ExpectedErrorPattern 'lacks the required post-validation rotation window'))

    [void](Invoke-Git $fixture @('push', '--force', 'origin', "$($validHead.head)`:refs/heads/main"))
    $successorOrdinaryValidator = Join-Path $tempRoot 'trusted-ordinary-successor.ps1'
    Export-GitBlob $fixture "$($validHead.head)`:$ordinaryPath" $successorOrdinaryValidator
    $successorOrdinaryBlob = Get-GitLine $fixture @('rev-parse', "$($validHead.head)`:$ordinaryPath")
    Reset-Fixture $fixture $validHead.head 'ordinary-after-first-epoch'
    [System.IO.File]::AppendAllText((Join-Path $fixture 'internal/worker/handlers_hooks_crystallization_integration_test.go'), "// authorized after first epoch`n", [System.Text.UTF8Encoding]::new($false))
    $ordinaryAfterFirstEpochHead = Push-Head $fixture 218 'fixture: ordinary after first epoch'
    $results.Add((Invoke-Scenario 'ordinary-after-first-epoch' $successorOrdinaryValidator $audit $remote $validHead.head 218 $ordinaryAfterFirstEpochHead $successorOrdinaryBlob $true $output))
    $successorMaintenanceValidator = Join-Path $tempRoot 'trusted-maintenance-successor.ps1'
    Export-GitBlob $fixture "$($validHead.head)`:$maintenancePath" $successorMaintenanceValidator
    $successorMaintenanceBlob = Get-GitLine $fixture @('rev-parse', "$($validHead.head)`:$maintenancePath")
    $secondHead = New-MaintenanceHead $fixture $validHead.head 208 'maintenance-second-consecutive'
    $results.Add((Invoke-Scenario 'maintenance-second-consecutive' $successorMaintenanceValidator $audit $remote $validHead.head 208 $secondHead.head $successorMaintenanceBlob $true $output -Maintenance -ApprovalEpoch $secondHead.approval_epoch))
    [void](Invoke-Git $fixture @('push', '--force', 'origin', "$($secondHead.head)`:refs/heads/main"))
    $results.Add((Invoke-Scenario 'maintenance-stale-replay-rejected' $maintenanceValidator $audit $remote $baseSha 203 $validHead.head $maintenanceBlob $false $output -Maintenance -ApprovalEpoch $validHead.approval_epoch -ExpectedErrorPattern 'stale'))
}
catch {
    $errorText = $_.Exception.Message
}
finally {
    if (Test-Path -LiteralPath $tempRoot) {
        $tempPrefix = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath()).TrimEnd('\','/') + [System.IO.Path]::DirectorySeparatorChar
        $target = [System.IO.Path]::GetFullPath($tempRoot)
        if (-not $target.StartsWith($tempPrefix, [System.StringComparison]::OrdinalIgnoreCase) -or -not ([System.IO.Path]::GetFileName($target)).StartsWith('engram-r12-authority-', [System.StringComparison]::Ordinal)) {
            $errorText = "refusing unsafe cleanup '$target'"
        }
        else {
            try {
                Remove-Item -LiteralPath $target -Recurse -Force
                $cleanupVerified = -not (Test-Path -LiteralPath $target)
            }
            catch {
                $cleanupError = "temp cleanup failed: $($_.Exception.Message)"
                $errorText = if ([string]::IsNullOrWhiteSpace($errorText)) { $cleanupError } else { "$errorText; $cleanupError" }
            }
        }
    }
}

$finishedAt = [DateTimeOffset]::UtcNow
$expectedPass = @($results | Where-Object expected -CEQ 'PASS').Count
$expectedFail = @($results | Where-Object expected -CEQ 'FAIL').Count
    $verdict = if ($null -eq $errorText -and $results.Count -eq 24 -and $expectedPass -eq 4 -and $expectedFail -eq 20 -and $cleanupVerified) { 'PASS' } else { 'FAIL' }
$summary = [ordered]@{
    schema_version = 1
    suite = 'R12 trusted-base authority and self-reference-free maintenance simulation'
    verdict = $verdict
    started_at = $startedAt.ToString('O')
    finished_at = $finishedAt.ToString('O')
    duration_seconds = [math]::Round(($finishedAt - $startedAt).TotalSeconds, 3)
    execution_contract = [ordered]@{
        head_treated_as_data_only = $true
        head_executed = $false
        head_checked_out = $false
        secrets_used = $false
        committed_manifest_contains_head_or_container_self_reference = $false
        trusted_artifact_binds_head_and_container = $true
        ordinary_forbidden_final_paths_self_test = $ordinaryAuthorizationSelfTestPassed
        workflow_parser_comments_exercised = $true
    }
    counts = [ordered]@{ scenarios = $results.Count; expected_pass = $expectedPass; expected_fail = $expectedFail }
    scenarios = @($results)
    temp_cleanup_verified = $cleanupVerified
    errors = @($errorText | Where-Object { $null -ne $_ })
}
Write-Utf8NoBom $artifactPath (($summary | ConvertTo-Json -Depth 100) + "`n")
Write-Output "R12 authority simulation verdict=$verdict scenarios=$($results.Count) artifact=$artifactPath"
if ($verdict -ne 'PASS') { throw "R12 authority simulation failed: $errorText" }
