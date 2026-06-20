param(
  [ValidateSet("disabled", "token")]
  [string]$Mode = "disabled",
  [string]$AdminToken = "dev-admin-token",
  [int]$PostgresPort = 55434,
  [int]$WorkerPort = 47779,
  [int]$OperatorConsolePort = 43002,
  [switch]$KeepStackUp
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$composeProject = "engram-operator-console-smoke"

function Write-Step {
  param([string]$Message)
  Write-Host "[operator-console-smoke] $Message"
}

function Invoke-Compose {
  param(
    [Parameter(Mandatory = $true)]
    [string[]]$Arguments
  )

  $oldPostgresPort = $env:POSTGRES_PORT
  $oldWorkerPort = $env:WORKER_PORT
  $oldOperatorConsolePort = $env:OPERATOR_CONSOLE_PORT
  $oldAuthDisabled = $env:ENGRAM_AUTH_DISABLED
  $oldAdminToken = $env:ENGRAM_AUTH_ADMIN_TOKEN
  $oldApiTarget = $env:NUXT_OPERATOR_API_TARGET

  try {
    $env:POSTGRES_PORT = [string]$PostgresPort
    $env:WORKER_PORT = [string]$WorkerPort
    $env:OPERATOR_CONSOLE_PORT = [string]$OperatorConsolePort
    $env:NUXT_OPERATOR_API_TARGET = "http://server:37777"

    if ($Mode -eq "disabled") {
      $env:ENGRAM_AUTH_DISABLED = "true"
      $env:ENGRAM_AUTH_ADMIN_TOKEN = ""
    }
    else {
      $env:ENGRAM_AUTH_DISABLED = "false"
      $env:ENGRAM_AUTH_ADMIN_TOKEN = $AdminToken
    }

    & docker compose -p $composeProject @Arguments
    if ($LASTEXITCODE -ne 0) {
      throw "docker compose $($Arguments -join ' ') failed with exit code $LASTEXITCODE"
    }
  }
  finally {
    $env:POSTGRES_PORT = $oldPostgresPort
    $env:WORKER_PORT = $oldWorkerPort
    $env:OPERATOR_CONSOLE_PORT = $oldOperatorConsolePort
    $env:ENGRAM_AUTH_DISABLED = $oldAuthDisabled
    $env:ENGRAM_AUTH_ADMIN_TOKEN = $oldAdminToken
    $env:NUXT_OPERATOR_API_TARGET = $oldApiTarget
  }
}

function Wait-Http {
  param(
    [Parameter(Mandatory = $true)]
    [string]$Url,
    [int[]]$ExpectedStatus = @(200),
    [int]$Attempts = 60,
    [int]$DelayMs = 1000
  )

  for ($i = 0; $i -lt $Attempts; $i++) {
    try {
      $response = Invoke-WebRequest -Uri $Url -MaximumRedirection 0 -SkipHttpErrorCheck
      if ($ExpectedStatus -contains [int]$response.StatusCode) {
        return $response
      }
    }
    catch {
      # Retry until attempts exhausted.
    }

    Start-Sleep -Milliseconds $DelayMs
  }

  throw "Timed out waiting for $($ExpectedStatus -join ', ') from $Url"
}

function Invoke-OperatorRequest {
  param(
    [Parameter(Mandatory = $true)]
    [string]$Method,
    [Parameter(Mandatory = $true)]
    [string]$Url,
    [Microsoft.PowerShell.Commands.WebRequestSession]$Session,
    [object]$Body = $null
  )

  $request = @{
    Uri = $Url
    Method = $Method
    MaximumRedirection = 0
    SkipHttpErrorCheck = $true
  }

  if ($null -ne $Session) {
    $request.WebSession = $Session
  }

  if ($null -ne $Body) {
    $request.ContentType = "application/json"
    $request.Body = $Body | ConvertTo-Json -Compress -Depth 10
  }

  Invoke-WebRequest @request
}

function Assert-Status {
  param(
    [Parameter(Mandatory = $true)]
    [object]$Response,
    [Parameter(Mandatory = $true)]
    [int[]]$ExpectedStatus,
    [Parameter(Mandatory = $true)]
    [string]$Step
  )

  if ($ExpectedStatus -notcontains [int]$Response.StatusCode) {
    throw "Expected $Step status $($ExpectedStatus -join ', '), got $($Response.StatusCode). Body: $($Response.Content)"
  }
}

function Read-JsonBody {
  param(
    [Parameter(Mandatory = $true)]
    [object]$Response,
    [Parameter(Mandatory = $true)]
    [string]$Step
  )

  try {
    $Response.Content | ConvertFrom-Json
  }
  catch {
    throw "$Step returned invalid JSON. Body: $($Response.Content)"
  }
}

$origin = "http://127.0.0.1:$OperatorConsolePort"
$workerOrigin = "http://127.0.0.1:$WorkerPort"
$rootUrl = "$origin/"
$readyUrl = "$origin/api/ready"
$selfcheckUrl = "$origin/api/selfcheck"
$statsUrl = "$origin/api/stats"
$statsVnextUrl = "$origin/api/stats/vnext"
$authMeUrl = "$origin/api/auth/me"
$authLoginUrl = "$origin/api/auth/login"
$issuesUrl = "$origin/api/issues"
$acknowledgeIssuesUrl = "$origin/api/issues/acknowledge"
$workerAuthMeUrl = "$workerOrigin/api/auth/me"

$session = New-Object Microsoft.PowerShell.Commands.WebRequestSession
$createdIssueId = $null
$smokeIssueId = $null
$smokeRunId = [guid]::NewGuid().ToString("N")
$smokeComment = "operator-console smoke resolved mutation $smokeRunId"

try {
  Write-Step "Building current-source server + operator-console images"
  Invoke-Compose -Arguments @("build", "server", "operator-console")

  Write-Step "Bringing up postgres + server + operator-console on pg=$PostgresPort worker=$WorkerPort web=$OperatorConsolePort mode=$Mode"
  Invoke-Compose -Arguments @("up", "-d", "--no-build", "postgres", "server", "operator-console")

  Write-Step "Waiting for operator-console root"
  $rootResponse = Wait-Http -Url $rootUrl -ExpectedStatus @(200)

  if ($Mode -eq "disabled") {
    Write-Step "Waiting for proxied selfcheck"
    $selfcheckResponse = Wait-Http -Url $selfcheckUrl -ExpectedStatus @(200)

    Write-Step "Waiting for backend ready through proxied /api/ready"
    $readyResponse = Wait-Http -Url $readyUrl -ExpectedStatus @(200)

    Write-Step "Waiting for proxied stats endpoints"
    $statsResponse = Wait-Http -Url $statsUrl -ExpectedStatus @(200)
    $statsVnextResponse = Wait-Http -Url $statsVnextUrl -ExpectedStatus @(200)

    Write-Step "Checking auth-disabled auth/me semantics"
    $workerAuthResponse = Invoke-OperatorRequest -Method GET -Url $workerAuthMeUrl
    Assert-Status -Response $workerAuthResponse -ExpectedStatus @(200) -Step "worker auth/me"
    $proxyAuthResponse = Invoke-OperatorRequest -Method GET -Url $authMeUrl
    Assert-Status -Response $proxyAuthResponse -ExpectedStatus @(200) -Step "proxied auth/me"

    $workerAuthBody = Read-JsonBody -Response $workerAuthResponse -Step "worker auth/me"
    $proxyAuthBody = Read-JsonBody -Response $proxyAuthResponse -Step "proxied auth/me"

    if ($workerAuthBody.authenticated -ne $true -or $proxyAuthBody.authenticated -ne $true) {
      throw "Expected auth-disabled auth/me to return authenticated=true. worker=$($workerAuthResponse.Content) proxy=$($proxyAuthResponse.Content)"
    }
    if ($workerAuthBody.role -ne "admin" -or $proxyAuthBody.role -ne "admin") {
      throw "Expected auth-disabled auth/me role=admin. worker=$($workerAuthResponse.Content) proxy=$($proxyAuthResponse.Content)"
    }
  }
  else {
    Write-Step "Checking pre-login auth/me is unauthorized"
    $preLoginAuthResponse = Wait-Http -Url $authMeUrl -ExpectedStatus @(401)
    Assert-Status -Response $preLoginAuthResponse -ExpectedStatus @(401) -Step "pre-login auth/me"

    Write-Step "Logging in with admin token through proxied auth API"
    $loginResponse = Invoke-OperatorRequest `
      -Method POST `
      -Url $authLoginUrl `
      -Session $session `
      -Body @{ token = $AdminToken }
    Assert-Status -Response $loginResponse -ExpectedStatus @(200) -Step "auth login"

    Write-Step "Waiting for backend ready through proxied /api/ready"
    $readyResponse = Wait-Http -Url $readyUrl -ExpectedStatus @(200)

    $postLoginAuthResponse = Invoke-OperatorRequest -Method GET -Url $authMeUrl -Session $session
    Assert-Status -Response $postLoginAuthResponse -ExpectedStatus @(200) -Step "post-login auth/me"

    Write-Step "Checking authenticated selfcheck and stats"
    $selfcheckResponse = Invoke-OperatorRequest -Method GET -Url $selfcheckUrl -Session $session
    Assert-Status -Response $selfcheckResponse -ExpectedStatus @(200) -Step "authenticated selfcheck"
    $statsResponse = Invoke-OperatorRequest -Method GET -Url $statsUrl -Session $session
    Assert-Status -Response $statsResponse -ExpectedStatus @(200) -Step "authenticated stats"
    $statsVnextResponse = Invoke-OperatorRequest -Method GET -Url $statsVnextUrl -Session $session
    Assert-Status -Response $statsVnextResponse -ExpectedStatus @(200) -Step "authenticated stats/vnext"
  }

  Write-Step "Creating smoke issue through proxied operator-console API"
  $createIssueResponse = Invoke-OperatorRequest `
    -Method POST `
    -Url $issuesUrl `
    -Session $session `
    -Body @{
      title = "operator-console smoke $smokeRunId"
      body = "Created by scripts/smoke-operator-console.ps1 to prove proxied issue mutations."
      priority = "low"
      type = "task"
      source_project = "operator-console-smoke"
      target_project = "operator-console-smoke"
      source_agent = "smoke-script"
      labels = @("operator-console", "smoke")
    }
  Assert-Status -Response $createIssueResponse -ExpectedStatus @(201) -Step "issue create"

  $createIssueBody = Read-JsonBody -Response $createIssueResponse -Step "issue create"
  if ($null -eq $createIssueBody.id) {
    throw "Issue create response did not include id. Body: $($createIssueResponse.Content)"
  }

  $createdIssueId = [int64]$createIssueBody.id
  $smokeIssueId = $createdIssueId
  $issueUrl = "$issuesUrl/$createdIssueId"

  Write-Step "Acknowledging smoke issue"
  $acknowledgeResponse = Invoke-OperatorRequest `
    -Method POST `
    -Url $acknowledgeIssuesUrl `
    -Session $session `
    -Body @{ ids = @($createdIssueId) }
  Assert-Status -Response $acknowledgeResponse -ExpectedStatus @(200) -Step "issue acknowledge"

  Write-Step "Resolving smoke issue with a comment"
  $resolveIssueResponse = Invoke-OperatorRequest `
    -Method PATCH `
    -Url $issueUrl `
    -Session $session `
    -Body @{
      status = "resolved"
      comment = $smokeComment
      source_project = "operator-console-smoke"
      source_agent = "smoke-script"
    }
  Assert-Status -Response $resolveIssueResponse -ExpectedStatus @(200) -Step "issue resolve"

  Write-Step "Deleting smoke issue"
  $deleteIssueResponse = Invoke-OperatorRequest `
    -Method DELETE `
    -Url $issueUrl `
    -Session $session
  Assert-Status -Response $deleteIssueResponse -ExpectedStatus @(204) -Step "issue delete"

  Write-Step "Verifying smoke issue cleanup"
  $deletedIssueResponse = Invoke-OperatorRequest `
    -Method GET `
    -Url $issueUrl `
    -Session $session
  Assert-Status -Response $deletedIssueResponse -ExpectedStatus @(404) -Step "issue cleanup verification"
  $createdIssueId = $null

  Write-Step "Smoke passed"
  Write-Host ("MODE=" + $Mode)
  Write-Host ("ROOT_STATUS=" + $rootResponse.StatusCode)
  Write-Host ("READY_STATUS=" + $readyResponse.StatusCode)
  Write-Host ("SELFCHECK_STATUS=" + $selfcheckResponse.StatusCode)
  Write-Host ("STATS_STATUS=" + $statsResponse.StatusCode)
  Write-Host ("STATS_VNEXT_STATUS=" + $statsVnextResponse.StatusCode)
  if ($Mode -eq "disabled") {
    Write-Host ("AUTH_ME_STATUS=" + $proxyAuthResponse.StatusCode)
    Write-Host ("AUTH_ME_BODY=" + $proxyAuthResponse.Content)
  }
  else {
    Write-Host ("AUTH_LOGIN_STATUS=" + $loginResponse.StatusCode)
    Write-Host ("AUTH_ME_STATUS=" + $postLoginAuthResponse.StatusCode)
    Write-Host ("AUTH_ME_BODY=" + $postLoginAuthResponse.Content)
  }
  Write-Host ("ISSUE_ID=" + $smokeIssueId)
  Write-Host ("ISSUE_CREATE_STATUS=" + $createIssueResponse.StatusCode)
  Write-Host ("ISSUE_ACKNOWLEDGE_STATUS=" + $acknowledgeResponse.StatusCode)
  Write-Host ("ISSUE_RESOLVE_STATUS=" + $resolveIssueResponse.StatusCode)
  Write-Host ("ISSUE_DELETE_STATUS=" + $deleteIssueResponse.StatusCode)
  Write-Host ("ISSUE_CLEANUP_GET_STATUS=" + $deletedIssueResponse.StatusCode)
}
finally {
  if ($null -ne $createdIssueId) {
    Write-Step "Cleaning up leftover smoke issue #$createdIssueId"
    try {
      $cleanupIssueResponse = Invoke-OperatorRequest `
        -Method DELETE `
        -Url "$issuesUrl/$createdIssueId" `
        -Session $session
      if (@(204, 404) -notcontains [int]$cleanupIssueResponse.StatusCode) {
        Write-Warning "Cleanup for smoke issue #$createdIssueId returned $($cleanupIssueResponse.StatusCode). Body: $($cleanupIssueResponse.Content)"
      }
    }
    catch {
      Write-Warning "Cleanup for smoke issue #$createdIssueId failed: $_"
    }
  }

  if (-not $KeepStackUp) {
    Write-Step "Tearing down stack"
    try {
      Invoke-Compose -Arguments @("down", "-v")
    }
    catch {
      Write-Warning $_
    }
  }
  else {
    Write-Step "Keeping stack up by request"
  }
}
