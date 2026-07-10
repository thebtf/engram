[CmdletBinding()]
param(
    [ValidateSet('openclaw')][string]$Surface = 'openclaw',
    [string]$ArtifactRoot = '.agent/reports/evidence/production-ready/node/openclaw',
    [string]$RunId,
    [switch]$Audit,
    [switch]$SelfTest,
    [switch]$Help
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$script:CommandRecords = [System.Collections.Generic.List[object]]::new()

function Write-Utf8NoBom {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][AllowEmptyString()][string]$Content)
    $parent = Split-Path -Parent $Path
    if ($parent) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }
    [System.IO.File]::WriteAllText([System.IO.Path]::GetFullPath($Path), $Content, [System.Text.UTF8Encoding]::new($false))
}

function Get-Sha256 {
    param([Parameter(Mandatory)][string]$Path)
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash
}

function Get-ObjectProperty {
    param([AllowNull()]$Object, [Parameter(Mandatory)][AllowEmptyString()][string]$Name)
    if ($null -eq $Object) { return $null }
    if ($Object -is [System.Collections.IDictionary]) {
        if ($Object.Contains($Name)) { return $Object[$Name] }
        return $null
    }
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) { return $null }
    return $property.Value
}

function Get-StringMap {
    param([AllowNull()]$Object)
    $result = [ordered]@{}
    if ($null -eq $Object) { return $result }
    if ($Object -is [System.Collections.IDictionary]) {
        foreach ($key in $Object.Keys) { $result[[string]$key] = [string]$Object[$key] }
        return $result
    }
    foreach ($property in $Object.PSObject.Properties) { $result[$property.Name] = [string]$property.Value }
    return $result
}

function Compare-StringMaps {
    param(
        [Parameter(Mandatory)][System.Collections.IDictionary]$Expected,
        [Parameter(Mandatory)][System.Collections.IDictionary]$Actual,
        [Parameter(Mandatory)][string]$Label
    )
    $errors = [System.Collections.Generic.List[string]]::new()
    $expectedKeys = @($Expected.Keys | ForEach-Object { [string]$_ } | Sort-Object)
    $actualKeys = @($Actual.Keys | ForEach-Object { [string]$_ } | Sort-Object)
    if (($expectedKeys -join "`n") -cne ($actualKeys -join "`n")) {
        $errors.Add("$Label keys differ: package=[$($expectedKeys -join ',')] lock=[$($actualKeys -join ',')]")
        return @($errors)
    }
    foreach ($key in $expectedKeys) {
        if ([string]$Expected[$key] -cne [string]$Actual[$key]) { $errors.Add("$Label '$key' differs: package='$($Expected[$key])' lock='$($Actual[$key])'") }
    }
    return @($errors)
}

function Test-OpenClawManifestParity {
    param(
        [Parameter(Mandatory)]$Package,
        [Parameter(Mandatory)]$Lock,
        [Parameter(Mandatory)]$Plugin
    )
    $errors = [System.Collections.Generic.List[string]]::new()
    $packageName = [string](Get-ObjectProperty $Package 'name')
    $packageVersion = [string](Get-ObjectProperty $Package 'version')
    $lockRoot = Get-ObjectProperty (Get-ObjectProperty $Lock 'packages') ''
    $lockVersion = Get-ObjectProperty $Lock 'lockfileVersion'
    if ($packageName -cne 'openclaw-engram') { $errors.Add("package name must be exact 'openclaw-engram', got '$packageName'") }
    if ([string](Get-ObjectProperty $Plugin 'name') -cne 'engram' -or [string](Get-ObjectProperty $Plugin 'id') -cne 'engram') { $errors.Add('openclaw.plugin.json name/id must both be exact engram') }
    if ($null -eq $lockRoot) { $errors.Add("package-lock.json packages[''] root is missing") }
    if ([string]$lockVersion -cne '3') { $errors.Add("package-lock.json lockfileVersion must be 3, got '$lockVersion'") }
    if ([string]::IsNullOrWhiteSpace($packageVersion)) { $errors.Add('package.json version is missing') }
    if ($null -ne $lockRoot) {
        if ([string](Get-ObjectProperty $Lock 'name') -cne $packageName -or [string](Get-ObjectProperty $lockRoot 'name') -cne $packageName) { $errors.Add('package name differs between package.json and package-lock root/top-level') }
        if ([string](Get-ObjectProperty $Lock 'version') -cne $packageVersion -or [string](Get-ObjectProperty $lockRoot 'version') -cne $packageVersion) { $errors.Add('package version differs between package.json and package-lock root/top-level') }
        foreach ($errorText in @(Compare-StringMaps (Get-StringMap (Get-ObjectProperty $Package 'dependencies')) (Get-StringMap (Get-ObjectProperty $lockRoot 'dependencies')) 'dependencies')) { $errors.Add($errorText) }
        foreach ($errorText in @(Compare-StringMaps (Get-StringMap (Get-ObjectProperty $Package 'devDependencies')) (Get-StringMap (Get-ObjectProperty $lockRoot 'devDependencies')) 'devDependencies')) { $errors.Add($errorText) }
    }
    if ([string](Get-ObjectProperty $Plugin 'version') -cne $packageVersion) { $errors.Add('openclaw.plugin.json version differs from package.json') }
    return [pscustomobject]@{ Pass = $errors.Count -eq 0; Errors = @($errors); PackageName = $packageName; Version = $packageVersion }
}

function Test-OpenClawPackContents {
    param([Parameter(Mandatory)][AllowEmptyString()][string]$Json)
    $errors = [System.Collections.Generic.List[string]]::new()
    $files = @()
    try {
        $entries = @(ConvertFrom-Json -InputObject $Json -Depth 100)
        if ($entries.Count -ne 1) { $errors.Add("npm pack JSON must contain exactly one package entry, got $($entries.Count)") }
        if ($entries.Count -gt 0) {
            $files = @((Get-ObjectProperty $entries[0] 'files') | ForEach-Object {
                [string](Get-ObjectProperty $_ 'path') -replace '\\', '/'
            } | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
        }
    }
    catch { $errors.Add("npm pack output is not valid JSON: $($_.Exception.Message)") }

    foreach ($required in @('package.json', 'openclaw.plugin.json', 'dist/index.js', 'dist/index.d.ts', 'scripts/install.sh')) {
        if ($files -cnotcontains $required) { $errors.Add("npm pack is missing required file '$required'") }
    }
    foreach ($file in $files) {
        if ($file -eq 'src' -or $file.StartsWith('src/', [System.StringComparison]::Ordinal) -or
            $file -eq 'test' -or $file.StartsWith('test/', [System.StringComparison]::Ordinal) -or
            $file -eq 'tests' -or $file.StartsWith('tests/', [System.StringComparison]::Ordinal) -or
            $file -eq 'node_modules' -or $file.StartsWith('node_modules/', [System.StringComparison]::Ordinal)) {
            $errors.Add("npm pack contains forbidden path '$file'")
        }
    }
    return [pscustomobject]@{ Pass = $errors.Count -eq 0; Errors = @($errors); Files = @($files) }
}

function Test-GitPathNonIgnored {
    param([Parameter(Mandatory)][int]$CheckIgnoreExitCode)
    return $CheckIgnoreExitCode -eq 1
}

function Test-CleanNodeSurface {
    param([Parameter(Mandatory)][string]$SurfaceRoot)
    return -not (Test-Path -LiteralPath (Join-Path $SurfaceRoot 'node_modules')) -and -not (Test-Path -LiteralPath (Join-Path $SurfaceRoot 'dist'))
}

function Remove-NodeArtifacts {
    param([Parameter(Mandatory)][string]$SurfaceRoot)
    $root = [System.IO.Path]::GetFullPath($SurfaceRoot).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
    $removed = [System.Collections.Generic.List[string]]::new()
    foreach ($name in @('node_modules', 'dist')) {
        $target = [System.IO.Path]::GetFullPath((Join-Path $root $name))
        $requiredPrefix = $root + [System.IO.Path]::DirectorySeparatorChar
        if (-not $target.StartsWith($requiredPrefix, [System.StringComparison]::OrdinalIgnoreCase)) { throw "refusing cleanup outside node surface: $target" }
        if (Test-Path -LiteralPath $target) { Remove-Item -LiteralPath $target -Recurse -Force; $removed.Add($target) }
    }
    return @($removed)
}

function Get-NodeMatrixCommandPlan {
    param(
        [Parameter(Mandatory)][string]$NpmPath,
        [Parameter(Mandatory)][string]$SurfaceRoot,
        [switch]$AuditEnabled
    )
    if (-not $AuditEnabled) { throw 'release node matrix requires -Audit so HIGH findings are fail-closed' }
    return @(
        [pscustomobject][ordered]@{ name = 'npm-ci'; executable = $NpmPath; arguments = @('ci'); working_directory = $SurfaceRoot },
        [pscustomobject][ordered]@{ name = 'npm-typecheck'; executable = $NpmPath; arguments = @('run', 'typecheck'); working_directory = $SurfaceRoot },
        [pscustomobject][ordered]@{ name = 'npm-test'; executable = $NpmPath; arguments = @('test'); working_directory = $SurfaceRoot },
        [pscustomobject][ordered]@{ name = 'npm-audit-high'; executable = $NpmPath; arguments = @('audit', '--audit-level=high'); working_directory = $SurfaceRoot },
        [pscustomobject][ordered]@{ name = 'npm-pack-dry-run'; executable = $NpmPath; arguments = @('pack', '--dry-run', '--json'); working_directory = $SurfaceRoot }
    )
}

function Invoke-CapturedProcess {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$FilePath,
        [Parameter(Mandatory)][AllowEmptyCollection()][string[]]$ArgumentList,
        [Parameter(Mandatory)][string]$WorkingDirectory,
        [Parameter(Mandatory)][string]$StdoutPath,
        [Parameter(Mandatory)][string]$StderrPath,
        [ValidateRange(1, 3600)][int]$TimeoutSeconds = 900
    )
    $start = [DateTimeOffset]::UtcNow; $process = $null; $stdout = ''; $stderr = ''; $timedOut = $false; $exitCode = 127
    try {
        $info = [System.Diagnostics.ProcessStartInfo]::new()
        $info.FileName = $FilePath; $info.WorkingDirectory = $WorkingDirectory; $info.UseShellExecute = $false; $info.CreateNoWindow = $true
        $info.RedirectStandardOutput = $true; $info.RedirectStandardError = $true
        foreach ($argument in $ArgumentList) { [void]$info.ArgumentList.Add($argument) }
        $process = [System.Diagnostics.Process]::new(); $process.StartInfo = $info
        if (-not $process.Start()) { throw "process '$FilePath' did not start" }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync(); $stderrTask = $process.StandardError.ReadToEndAsync()
        $timedOut = -not $process.WaitForExit($TimeoutSeconds * 1000)
        if ($timedOut) { try { $process.Kill($true) } catch {}; $process.WaitForExit() }
        $stdout = $stdoutTask.GetAwaiter().GetResult(); $stderr = $stderrTask.GetAwaiter().GetResult(); $exitCode = if ($timedOut) { 124 } else { $process.ExitCode }
        if ($timedOut) { $stderr += "`nPROCESS_TIMEOUT after $TimeoutSeconds seconds`n" }
    }
    catch { $stderr = "PROCESS_START_OR_CAPTURE_ERROR: $($_.Exception.Message)`n"; $exitCode = 127 }
    finally { if ($null -ne $process) { $process.Dispose() } }
    Write-Utf8NoBom $StdoutPath $stdout; Write-Utf8NoBom $StderrPath $stderr
    $finished = [DateTimeOffset]::UtcNow
    $record = [pscustomobject][ordered]@{
        name = $Name; executable = $FilePath; arguments = @($ArgumentList); command = (@($FilePath) + @($ArgumentList)) -join ' '
        working_directory = [System.IO.Path]::GetFullPath($WorkingDirectory)
        started_at = $start.ToString('O'); finished_at = $finished.ToString('O'); duration_seconds = [math]::Round(($finished - $start).TotalSeconds, 3)
        exit_code = $exitCode; timed_out = $timedOut; stdout = [System.IO.Path]::GetFullPath($StdoutPath); stderr = [System.IO.Path]::GetFullPath($StderrPath)
    }
    $script:CommandRecords.Add($record)
    return [pscustomobject]@{ ExitCode = $exitCode; Stdout = $stdout; Stderr = $stderr; Record = $record }
}

function Assert-SelfTestCondition {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw "SELFTEST FAIL: $Message" }
}

function Invoke-SelfTest {
    $root = Join-Path ([System.IO.Path]::GetTempPath()) ('run-node-matrix-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $root -Force | Out-Null
    try {
        $package = [ordered]@{ name = 'openclaw-engram'; version = '3.7.5'; dependencies = [ordered]@{ zod = '^3.25.76' }; devDependencies = [ordered]@{ typescript = '^5.9.3' } }
        $lock = [ordered]@{ name = 'openclaw-engram'; version = '3.7.5'; lockfileVersion = 3; packages = [ordered]@{ '' = [ordered]@{ name = 'openclaw-engram'; version = '3.7.5'; dependencies = [ordered]@{ zod = '^3.25.76' }; devDependencies = [ordered]@{ typescript = '^5.9.3' } } } }
        $plugin = [ordered]@{ name = 'engram'; id = 'engram'; version = '3.7.5' }
        $valid = Test-OpenClawManifestParity -Package $package -Lock $lock -Plugin $plugin
        Assert-SelfTestCondition $valid.Pass ("matching manifests were rejected: " + ($valid.Errors -join '; '))
        $mismatch = [ordered]@{ name = 'engram'; id = 'engram'; version = '3.7.4' }
        Assert-SelfTestCondition (-not (Test-OpenClawManifestParity -Package $package -Lock $lock -Plugin $mismatch).Pass) 'plugin version mismatch was accepted'
        $missingLockRoot = [ordered]@{ name = 'openclaw-engram'; version = '3.7.5'; lockfileVersion = 3; packages = [ordered]@{} }
        Assert-SelfTestCondition (-not (Test-OpenClawManifestParity -Package $package -Lock $missingLockRoot -Plugin $plugin).Pass) 'missing lock root was accepted'

        $packJson = @([ordered]@{ files = @(
            [ordered]@{ path = 'package.json' },
            [ordered]@{ path = 'openclaw.plugin.json' },
            [ordered]@{ path = 'dist/index.js' },
            [ordered]@{ path = 'dist/index.d.ts' },
            [ordered]@{ path = 'scripts/install.sh' },
            [ordered]@{ path = 'README.md' }
        ) }) | ConvertTo-Json -Depth 8
        Assert-SelfTestCondition (Test-OpenClawPackContents $packJson).Pass 'valid package dry-run contents were rejected'
        $forbiddenPackJson = @([ordered]@{ files = @(
            [ordered]@{ path = 'package.json' },
            [ordered]@{ path = 'openclaw.plugin.json' },
            [ordered]@{ path = 'dist/index.js' },
            [ordered]@{ path = 'dist/index.d.ts' },
            [ordered]@{ path = 'scripts/install.sh' },
            [ordered]@{ path = 'src/index.ts' }
        ) }) | ConvertTo-Json -Depth 8
        Assert-SelfTestCondition (-not (Test-OpenClawPackContents $forbiddenPackJson).Pass) 'forbidden source path in package dry-run was accepted'
        $missingPackJson = @([ordered]@{ files = @([ordered]@{ path = 'package.json' }) }) | ConvertTo-Json -Depth 8
        Assert-SelfTestCondition (-not (Test-OpenClawPackContents $missingPackJson).Pass) 'missing required package dry-run contents were accepted'
        Assert-SelfTestCondition (Test-GitPathNonIgnored 1) 'git check-ignore exit 1 was not accepted as non-ignored'
        Assert-SelfTestCondition (-not (Test-GitPathNonIgnored 0)) 'ignored lockfile was accepted'
        Assert-SelfTestCondition (-not (Test-GitPathNonIgnored 2)) 'git check-ignore error was accepted'

        $surface = Join-Path $root 'surface'; New-Item -ItemType Directory -Path $surface | Out-Null
        $outsideSentinel = Join-Path $root 'outside-sentinel'; New-Item -ItemType Directory -Path $outsideSentinel | Out-Null
        Assert-SelfTestCondition (Test-CleanNodeSurface $surface) 'clean surface was rejected'
        New-Item -ItemType Directory -Path (Join-Path $surface 'node_modules') | Out-Null
        New-Item -ItemType Directory -Path (Join-Path $surface 'dist') | Out-Null
        Assert-SelfTestCondition (-not (Test-CleanNodeSurface $surface)) 'pre-existing node artifacts were accepted as clean-checkout evidence'
        [void](Remove-NodeArtifacts $surface)
        Assert-SelfTestCondition (Test-CleanNodeSurface $surface) 'node cleanup left node_modules or dist behind'
        Assert-SelfTestCondition (Test-Path -LiteralPath $outsideSentinel -PathType Container) 'node cleanup escaped the exact surface'

        $plan = @(Get-NodeMatrixCommandPlan -NpmPath 'npm' -SurfaceRoot $surface -AuditEnabled)
        $names = @($plan | ForEach-Object name)
        Assert-SelfTestCondition (($names -join ' -> ') -ceq 'npm-ci -> npm-typecheck -> npm-test -> npm-audit-high -> npm-pack-dry-run') 'node command order drifted'
        Assert-SelfTestCondition ((($plan | Where-Object name -eq 'npm-audit-high').arguments -join ' ') -ceq 'audit --audit-level=high') 'high-severity audit command drifted'
        Assert-SelfTestCondition ((($plan | Where-Object name -eq 'npm-pack-dry-run').arguments -join ' ') -ceq 'pack --dry-run --json') 'package dry-run command drifted'
        Write-Output 'SELFTEST PASS: run-node-matrix.ps1'
    }
    finally { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
}

if ($Help) {
    Write-Output 'run-node-matrix.ps1 -Surface openclaw -Audit [-ArtifactRoot <path>] [-RunId <id>]'
    Write-Output 'Requires a clean tracked surface and runs: npm ci -> typecheck -> tests -> HIGH audit -> npm pack dry-run.'
    exit 0
}
if ($SelfTest) { Invoke-SelfTest; exit 0 }

$repoRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..'))
$surfaceRoot = [System.IO.Path]::GetFullPath((Join-Path $repoRoot 'plugin\openclaw-engram'))
$surfaceRelative = 'plugin/openclaw-engram'
$startedAt = [DateTimeOffset]::UtcNow
if ([string]::IsNullOrWhiteSpace($RunId)) { $RunId = $startedAt.ToString('yyyyMMddTHHmmssZ') + '-' + [guid]::NewGuid().ToString('N').Substring(0, 10) }
if ($RunId -notmatch '^[A-Za-z0-9._-]+$') { throw '-RunId may contain only letters, digits, dot, underscore, and hyphen.' }
$artifactDirectory = if ([System.IO.Path]::IsPathRooted($ArtifactRoot)) { Join-Path $ArtifactRoot $RunId } else { Join-Path (Join-Path $repoRoot $ArtifactRoot) $RunId }
if (Test-Path -LiteralPath $artifactDirectory) { throw "node matrix artifact directory already exists: $artifactDirectory" }
New-Item -ItemType Directory -Path $artifactDirectory -Force | Out-Null
$script:CommandRecords.Clear()
$errors = [System.Collections.Generic.List[string]]::new()
$cleanupErrors = [System.Collections.Generic.List[string]]::new()
$manifestParity = $null
$packContents = $null
$manifestHashes = [ordered]@{}
$manifestsTracked = $false
$lockNonIgnored = $false
$removedArtifacts = @()
$preSurfaceClean = $false
$postSurfaceClean = $false
$plannedCommands = @()
$npmPath = $null
$gitPath = $null

try {
    if (-not $Audit) { throw '-Audit is mandatory for the production node matrix' }
    if (-not (Test-Path -LiteralPath $surfaceRoot -PathType Container)) { throw "OpenClaw surface is missing: $surfaceRoot" }
    $gitPath = (Get-Command git -ErrorAction Stop).Source
    $npmCommand = Get-Command npm.cmd -ErrorAction SilentlyContinue
    if ($null -eq $npmCommand) { $npmCommand = Get-Command npm -ErrorAction Stop }
    $npmPath = $npmCommand.Source
    $plannedCommands = @(Get-NodeMatrixCommandPlan -NpmPath $npmPath -SurfaceRoot $surfaceRoot -AuditEnabled)

    $preStatus = Invoke-CapturedProcess 'openclaw-pre-status' $gitPath @('status', '--porcelain=v1', '--untracked-files=all', '--', $surfaceRelative) $repoRoot (Join-Path $artifactDirectory 'pre-status.stdout.log') (Join-Path $artifactDirectory 'pre-status.stderr.log') 30
    if ($preStatus.ExitCode -ne 0) { throw "git pre-status failed with exit $($preStatus.ExitCode)" }
    $preSurfaceClean = [string]::IsNullOrWhiteSpace($preStatus.Stdout) -and (Test-CleanNodeSurface $surfaceRoot)
    if (-not $preSurfaceClean) { throw 'OpenClaw node matrix requires a clean surface with no node_modules or dist' }

    $packagePath = Join-Path $surfaceRoot 'package.json'; $lockPath = Join-Path $surfaceRoot 'package-lock.json'; $pluginPath = Join-Path $surfaceRoot 'openclaw.plugin.json'
    foreach ($requiredPath in @($packagePath, $lockPath, $pluginPath)) { if (-not (Test-Path -LiteralPath $requiredPath -PathType Leaf)) { throw "required OpenClaw manifest is missing: $requiredPath" } }
    $tracked = Invoke-CapturedProcess 'openclaw-manifests-tracked' $gitPath @('ls-files', '--error-unmatch', '--', 'plugin/openclaw-engram/package.json', 'plugin/openclaw-engram/package-lock.json', 'plugin/openclaw-engram/openclaw.plugin.json') $repoRoot (Join-Path $artifactDirectory 'tracked.stdout.log') (Join-Path $artifactDirectory 'tracked.stderr.log') 30
    if ($tracked.ExitCode -ne 0) { throw 'OpenClaw package, lock, and plugin manifests must all be tracked' }
    $manifestsTracked = $true
    $ignored = Invoke-CapturedProcess 'openclaw-lock-non-ignored' $gitPath @('check-ignore', '--no-index', '--quiet', '--', 'plugin/openclaw-engram/package-lock.json') $repoRoot (Join-Path $artifactDirectory 'check-ignore.stdout.log') (Join-Path $artifactDirectory 'check-ignore.stderr.log') 30
    if ($ignored.ExitCode -eq 0) { throw 'OpenClaw package-lock.json is tracked but still matched by an ignore rule' }
    if ($ignored.ExitCode -ne 1) { throw "git check-ignore failed with unexpected exit $($ignored.ExitCode)" }
    $lockNonIgnored = Test-GitPathNonIgnored $ignored.ExitCode

    $package = Get-Content -LiteralPath $packagePath -Raw | ConvertFrom-Json -AsHashtable -Depth 100
    $lock = Get-Content -LiteralPath $lockPath -Raw | ConvertFrom-Json -AsHashtable -Depth 100
    $plugin = Get-Content -LiteralPath $pluginPath -Raw | ConvertFrom-Json -AsHashtable -Depth 100
    $manifestParity = Test-OpenClawManifestParity -Package $package -Lock $lock -Plugin $plugin
    if (-not $manifestParity.Pass) { foreach ($parityError in $manifestParity.Errors) { $errors.Add($parityError) } }
    $manifestHashes = [ordered]@{ package_json = Get-Sha256 $packagePath; package_lock_json = Get-Sha256 $lockPath; openclaw_plugin_json = Get-Sha256 $pluginPath }

    if ($errors.Count -eq 0) {
        foreach ($command in $plannedCommands) {
            $result = Invoke-CapturedProcess $command.name $command.executable @($command.arguments) $command.working_directory (Join-Path $artifactDirectory "$($command.name).stdout.log") (Join-Path $artifactDirectory "$($command.name).stderr.log") 1200
            if ($result.ExitCode -ne 0) { $errors.Add("$($command.name) failed with exit $($result.ExitCode)"); break }
            if ($command.name -eq 'npm-pack-dry-run') {
                $packContents = Test-OpenClawPackContents $result.Stdout
                if (-not $packContents.Pass) { foreach ($packError in $packContents.Errors) { $errors.Add($packError) } }
            }
        }
    }
}
catch { $errors.Add($_.Exception.Message) }
finally {
    try { $removedArtifacts = @(Remove-NodeArtifacts $surfaceRoot) } catch { $cleanupErrors.Add($_.Exception.Message) }
    try {
        if ($null -eq $gitPath) { $gitPath = (Get-Command git -ErrorAction Stop).Source }
        $postStatus = Invoke-CapturedProcess 'openclaw-post-cleanup-status' $gitPath @('status', '--porcelain=v1', '--untracked-files=all', '--', $surfaceRelative) $repoRoot (Join-Path $artifactDirectory 'post-status.stdout.log') (Join-Path $artifactDirectory 'post-status.stderr.log') 30
        if ($postStatus.ExitCode -ne 0) { $cleanupErrors.Add("git post-status failed with exit $($postStatus.ExitCode)") }
        else { $postSurfaceClean = [string]::IsNullOrWhiteSpace($postStatus.Stdout) -and (Test-CleanNodeSurface $surfaceRoot) }
        if (-not $postSurfaceClean) { $cleanupErrors.Add('OpenClaw surface is not clean after unconditional cleanup') }
    }
    catch { $cleanupErrors.Add($_.Exception.Message) }
}

foreach ($cleanupError in $cleanupErrors) { $errors.Add("cleanup: $cleanupError") }
$commandsPath = Join-Path $artifactDirectory 'commands.json'; Write-Utf8NoBom $commandsPath ((ConvertTo-Json -InputObject @($script:CommandRecords.ToArray()) -Depth 14) + "`n")
$cleanupPath = Join-Path $artifactDirectory 'cleanup.json'; Write-Utf8NoBom $cleanupPath (([pscustomobject][ordered]@{ removed = @($removedArtifacts); errors = @($cleanupErrors); surface_clean = $postSurfaceClean } | ConvertTo-Json -Depth 8) + "`n")
$finishedAt = [DateTimeOffset]::UtcNow
$summary = [pscustomobject][ordered]@{
    schema_version = 1; gate = 'node-release-matrix'; surface = $Surface; run_id = $RunId
    started_at = $startedAt.ToString('O'); finished_at = $finishedAt.ToString('O'); duration_seconds = [math]::Round(($finishedAt - $startedAt).TotalSeconds, 3)
    verdict = if ($errors.Count -eq 0) { 'PASS' } else { 'FAIL' }
    surface_root = $surfaceRoot; pre_surface_clean = $preSurfaceClean; post_surface_clean = $postSurfaceClean
    manifests_tracked_and_present = $manifestsTracked -and $null -ne $manifestParity; lock_non_ignored = $lockNonIgnored
    manifest_parity = if ($null -ne $manifestParity) { $manifestParity.Pass } else { $false }
    manifest_hashes = $manifestHashes
    required_sequence = @('npm-ci', 'npm-typecheck', 'npm-test', 'npm-audit-high', 'npm-pack-dry-run')
    planned_sequence = @($plannedCommands | ForEach-Object name)
    executed_sequence = @($script:CommandRecords | Where-Object { $_.name -like 'npm-*' } | ForEach-Object name)
    audit_level = 'high'; package_dry_run = $null -ne $packContents
    package_contents_valid = if ($null -ne $packContents) { $packContents.Pass } else { $false }
    package_files = if ($null -ne $packContents) { @($packContents.Files) } else { @() }
    cleanup = [System.IO.Path]::GetFullPath($cleanupPath); commands = [System.IO.Path]::GetFullPath($commandsPath)
    errors = @($errors); artifact_directory = [System.IO.Path]::GetFullPath($artifactDirectory)
}
$summaryPath = Join-Path $artifactDirectory 'summary.json'; Write-Utf8NoBom $summaryPath (($summary | ConvertTo-Json -Depth 16) + "`n")
Write-Host ("node-matrix surface={0} verdict={1} executed={2} cleanup={3}" -f $Surface, $summary.verdict, $summary.executed_sequence.Count, $summary.post_surface_clean)
Write-Host "summary=$([System.IO.Path]::GetFullPath($summaryPath))"
if ($errors.Count -ne 0) { exit 1 }
exit 0
