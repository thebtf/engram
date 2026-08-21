[CmdletBinding()]
param(
    [string]$ScannerCommand = 'sonar-scanner',
    [ValidateRange(1, 2147483647)]
    [int]$QualityGateTimeoutSeconds = 600
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Invoke-Git {
    param(
        [Parameter(Mandatory = $true)][string]$WorkingDirectory,
        [Parameter(Mandatory = $true)][string[]]$Arguments
    )

    $output = @(& git -C $WorkingDirectory @Arguments 2>&1 | ForEach-Object { $_.ToString() })
    $exitCode = $LASTEXITCODE
    if ($exitCode -ne 0) {
        throw "git failed with exit code ${exitCode}: $($output -join [Environment]::NewLine)"
    }
    return $output
}

function Get-GitSingleLine {
    param(
        [Parameter(Mandatory = $true)][string]$WorkingDirectory,
        [Parameter(Mandatory = $true)][string[]]$Arguments
    )

    $lines = @(Invoke-Git -WorkingDirectory $WorkingDirectory -Arguments $Arguments |
        Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    if ($lines.Count -ne 1) {
        throw "git $($Arguments -join ' ') returned $($lines.Count) non-empty lines; expected one."
    }
    return $lines[0].Trim()
}

function Invoke-CheckedProcess {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [Parameter(Mandatory = $true)][string]$Name
    )

    & $FilePath @Arguments
    $exitCode = $LASTEXITCODE
    if ($exitCode -ne 0) {
        throw "$Name failed with exit code $exitCode."
    }
}

function Get-ReportTaskMetadata {
    param([Parameter(Mandatory = $true)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "SonarScanner did not write report-task metadata: $Path"
    }

    $metadata = [ordered]@{}
    foreach ($line in Get-Content -LiteralPath $Path) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        $separator = $line.IndexOf('=')
        if ($separator -le 0) {
            throw "Malformed report-task metadata line: $line"
        }

        $key = $line.Substring(0, $separator).Trim()
        if ([string]::IsNullOrWhiteSpace($key) -or $metadata.Contains($key)) {
            throw "Malformed report-task metadata key: $line"
        }
        $metadata[$key] = $line.Substring($separator + 1).Trim()
    }

    foreach ($requiredKey in @('ceTaskUrl', 'dashboardUrl')) {
        if (-not $metadata.Contains($requiredKey) -or [string]::IsNullOrWhiteSpace([string]$metadata[$requiredKey])) {
            throw "SonarScanner report-task metadata is missing $requiredKey."
        }
    }
    return $metadata
}

if ([string]::IsNullOrWhiteSpace($ScannerCommand)) {
    throw 'ScannerCommand must not be empty.'
}

$repoRoot = [IO.Path]::GetFullPath((Get-GitSingleLine -WorkingDirectory $PSScriptRoot -Arguments @('rev-parse', '--show-toplevel')))
$head = (Get-GitSingleLine -WorkingDirectory $repoRoot -Arguments @('rev-parse', '--verify', 'HEAD^{commit}')).ToLowerInvariant()
if ($head -notmatch '^[0-9a-f]{40}$') {
    throw "git returned a non-canonical HEAD commit: $head"
}
$commonGitDirectory = [IO.Path]::GetFullPath((Get-GitSingleLine -WorkingDirectory $repoRoot -Arguments @('rev-parse', '--path-format=absolute', '--git-common-dir')))
$coordinationRoot = [IO.Path]::GetFullPath((Split-Path -Parent $commonGitDirectory))
$receiptDirectory = Join-Path $coordinationRoot '.agent/e/sonarqube'
New-Item -ItemType Directory -Path $receiptDirectory -Force | Out-Null
$receiptPath = Join-Path $receiptDirectory "$head.json"
$reportTaskPath = Join-Path $repoRoot '.scannerwork/report-task.txt'
if (Test-Path -LiteralPath $receiptPath -PathType Leaf) {
    Remove-Item -LiteralPath $receiptPath -Force
}
if (Test-Path -LiteralPath $reportTaskPath -PathType Leaf) {
    Remove-Item -LiteralPath $reportTaskPath -Force
}

$status = @(Invoke-Git -WorkingDirectory $repoRoot -Arguments @('status', '--porcelain'))
if ($status.Count -ne 0) {
    throw 'Refusing to run SonarQube against a dirty working tree.'
}

if ([string]::IsNullOrWhiteSpace($env:SONAR_TOKEN)) {
    throw 'SONAR_TOKEN is required.'
}
$sonarHostUrl = if ([string]::IsNullOrWhiteSpace($env:SONAR_HOST_URL)) {
    'http://unleashed.lan:9000'
} else {
    $env:SONAR_HOST_URL
}

$go = Get-Command -Name 'go' -CommandType Application -ErrorAction SilentlyContinue
if ($null -eq $go) {
    throw 'Go executable is required to generate coverage.'
}
$scanner = Get-Command -Name $ScannerCommand -CommandType Application -ErrorAction SilentlyContinue
$scannerPath = if ($null -ne $scanner) { $scanner.Source } else { $null }
if ($null -eq $scannerPath -and $ScannerCommand -ceq 'sonar-scanner' -and $IsWindows -and -not [string]::IsNullOrWhiteSpace($env:LOCALAPPDATA)) {
    $perUserScanner = Join-Path $env:LOCALAPPDATA 'SonarScanner/8.1.0.6389/sonar-scanner-8.1.0.6389-windows-x64/bin/sonar-scanner.bat'
    if (Test-Path -LiteralPath $perUserScanner -PathType Leaf) {
        $scannerPath = $perUserScanner
    }
}
if ($null -eq $scannerPath) {
    throw "SonarScanner command was not found: $ScannerCommand"
}

$scannerArguments = @(
    "-Dsonar.host.url=$sonarHostUrl",
    "-Dsonar.scm.revision=$head",
    "-Dsonar.buildString=$head",
    '-Dsonar.qualitygate.wait=true',
    "-Dsonar.qualitygate.timeout=$QualityGateTimeoutSeconds"
)


Push-Location -LiteralPath $repoRoot
try {
    Invoke-CheckedProcess -FilePath $go.Path -Arguments @('test', './...', '-race', '-covermode=atomic', '-coverprofile=coverage.out') -Name 'Go coverage tests'
    Invoke-CheckedProcess -FilePath $scannerPath -Arguments $scannerArguments -Name 'SonarScanner quality gate'
} finally {
    Pop-Location
}

$reportTask = Get-ReportTaskMetadata -Path $reportTaskPath
$receipt = [ordered]@{
    schema_version = 1
    gate = 'sonarqube'
    project_key = 'thebtf_engram'
    sonar_host_url = $sonarHostUrl
    verdict = 'PASS'
    head = $head
    completed_at_utc = [DateTimeOffset]::UtcNow.ToString('o')
    ce_task_url = [string]$reportTask['ceTaskUrl']
    dashboard_url = [string]$reportTask['dashboardUrl']
}
$receiptJson = ($receipt | ConvertTo-Json -Depth 10) + [Environment]::NewLine
if ($receiptJson.IndexOf($env:SONAR_TOKEN, [StringComparison]::Ordinal) -ge 0) {
    throw 'Refusing to write a receipt that contains SONAR_TOKEN.'
}

$tempReceiptPath = Join-Path $receiptDirectory ('.' + $head + '.' + [guid]::NewGuid().ToString('N') + '.tmp')
try {
    [IO.File]::WriteAllText($tempReceiptPath, $receiptJson, [Text.UTF8Encoding]::new($false))
    Move-Item -LiteralPath $tempReceiptPath -Destination $receiptPath -Force
} finally {
    if (Test-Path -LiteralPath $tempReceiptPath -PathType Leaf) {
        Remove-Item -LiteralPath $tempReceiptPath -Force
    }
}

Write-Output $receiptPath
