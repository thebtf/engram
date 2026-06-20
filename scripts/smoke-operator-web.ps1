param(
  [string]$AdminToken = "dev-admin-token",
  [int]$PostgresPort = 55432,
  [int]$WorkerPort = 47777,
  [int]$OperatorWebPort = 43000,
  [switch]$KeepStackUp
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot

function Write-Step {
  param([string]$Message)
  Write-Host "[operator-web-smoke] $Message"
}

function Invoke-Compose {
  param(
    [Parameter(Mandatory = $true)]
    [string[]]$Arguments
  )

  $oldAdminToken = $env:ENGRAM_AUTH_ADMIN_TOKEN
  $oldPostgresPort = $env:POSTGRES_PORT
  $oldWorkerPort = $env:WORKER_PORT
  $oldOperatorWebPort = $env:OPERATOR_WEB_PORT

  try {
    $env:ENGRAM_AUTH_ADMIN_TOKEN = $AdminToken
    $env:POSTGRES_PORT = [string]$PostgresPort
    $env:WORKER_PORT = [string]$WorkerPort
    $env:OPERATOR_WEB_PORT = [string]$OperatorWebPort

    & docker compose @Arguments
    if ($LASTEXITCODE -ne 0) {
      throw "docker compose $($Arguments -join ' ') failed with exit code $LASTEXITCODE"
    }
  }
  finally {
    $env:ENGRAM_AUTH_ADMIN_TOKEN = $oldAdminToken
    $env:POSTGRES_PORT = $oldPostgresPort
    $env:WORKER_PORT = $oldWorkerPort
    $env:OPERATOR_WEB_PORT = $oldOperatorWebPort
  }
}

function Wait-Http200 {
  param(
    [Parameter(Mandatory = $true)]
    [string]$Url,
    [int]$Attempts = 40,
    [int]$DelayMs = 1000
  )

  for ($i = 0; $i -lt $Attempts; $i++) {
    try {
      $response = Invoke-WebRequest -Uri $Url -MaximumRedirection 0 -SkipHttpErrorCheck
      if ($response.StatusCode -eq 200) {
        return $response
      }
    }
    catch {
      # Retry until attempts exhausted.
    }

    Start-Sleep -Milliseconds $DelayMs
  }

  throw "Timed out waiting for HTTP 200 from $Url"
}

function Invoke-OperatorRequest {
  param(
    [Parameter(Mandatory = $true)]
    [string]$Method,
    [Parameter(Mandatory = $true)]
    [string]$Url,
    [Parameter(Mandatory = $true)]
    [Microsoft.PowerShell.Commands.WebRequestSession]$Session,
    [object]$Body = $null
  )

  $request = @{
    Uri = $Url
    Method = $Method
    WebSession = $Session
    MaximumRedirection = 0
    SkipHttpErrorCheck = $true
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

$operatorWebOrigin = "http://127.0.0.1:$OperatorWebPort"
$loginUrl = "$operatorWebOrigin/login"
$setupNeededUrl = "$operatorWebOrigin/api/auth/setup-needed"
$authLoginUrl = "$operatorWebOrigin/api/auth/login"
$selfcheckUrl = "$operatorWebOrigin/api/selfcheck"
$issuesUrl = "$operatorWebOrigin/api/issues"
$acknowledgeIssuesUrl = "$operatorWebOrigin/api/issues/acknowledge"

$webSession = New-Object Microsoft.PowerShell.Commands.WebRequestSession
$createdIssueId = $null
$smokeIssueId = $null
$smokeRunId = [guid]::NewGuid().ToString("N")
$smokeComment = "operator-web smoke resolved mutation $smokeRunId"

try {
  Write-Step "Building local server + operator-web images for the smoke stack"
  Invoke-Compose -Arguments @("build", "server", "operator-web")

  Write-Step "Bringing up postgres + server + operator-web on ports pg=$PostgresPort worker=$WorkerPort web=$OperatorWebPort"
  Invoke-Compose -Arguments @("up", "-d", "--no-build", "postgres", "server", "operator-web")

  Write-Step "Waiting for operator-web login shell"
  $loginResponse = Wait-Http200 -Url $loginUrl

  Write-Step "Checking proxied setup-needed endpoint"
  $setupNeededResponse = Wait-Http200 -Url $setupNeededUrl

  Write-Step "Logging in through operator-web proxy"
  $loginApiResponse = Invoke-WebRequest `
    -Uri $authLoginUrl `
    -Method POST `
    -WebSession $webSession `
    -ContentType "application/json" `
    -Body (@{ token = $AdminToken } | ConvertTo-Json -Compress) `
    -MaximumRedirection 0 `
    -SkipHttpErrorCheck

  if ($loginApiResponse.StatusCode -ne 200) {
    throw "Expected login status 200, got $($loginApiResponse.StatusCode). Body: $($loginApiResponse.Content)"
  }

  Write-Step "Checking authenticated selfcheck through session cookie"
  $selfcheckResponse = Invoke-WebRequest `
    -Uri $selfcheckUrl `
    -WebSession $webSession `
    -MaximumRedirection 0 `
    -SkipHttpErrorCheck

  if ($selfcheckResponse.StatusCode -ne 200) {
    throw "Expected selfcheck status 200, got $($selfcheckResponse.StatusCode). Body: $($selfcheckResponse.Content)"
  }

  Write-Step "Creating smoke issue through operator-web proxy"
  $createIssueResponse = Invoke-OperatorRequest `
    -Method POST `
    -Url $issuesUrl `
    -Session $webSession `
    -Body @{
      title = "operator-web smoke $smokeRunId"
      body = "Created by scripts/smoke-operator-web.ps1 to prove proxied issue mutations."
      priority = "low"
      type = "task"
      source_project = "operator-web-smoke"
      target_project = "operator-web-smoke"
      source_agent = "smoke-script"
      labels = @("operator-web", "smoke")
    }
  Assert-Status -Response $createIssueResponse -ExpectedStatus @(201) -Step "issue create"

  $createIssueBody = Read-JsonBody -Response $createIssueResponse -Step "issue create"
  if ($null -eq $createIssueBody.id) {
    throw "Issue create response did not include id. Body: $($createIssueResponse.Content)"
  }

  $createdIssueId = [int64]$createIssueBody.id
  $smokeIssueId = $createdIssueId
  $issueUrl = "$issuesUrl/$createdIssueId"

  Write-Step "Verifying smoke issue was created through operator-web proxy"
  $createdIssueResponse = Invoke-OperatorRequest `
    -Method GET `
    -Url $issueUrl `
    -Session $webSession
  Assert-Status -Response $createdIssueResponse -ExpectedStatus @(200) -Step "issue detail after create"

  $createdIssueBody = Read-JsonBody -Response $createdIssueResponse -Step "issue detail after create"
  if ($createdIssueBody.issue.status -ne "open") {
    throw "Expected created issue status open, got $($createdIssueBody.issue.status)"
  }
  if ($createdIssueBody.issue.title -ne "operator-web smoke $smokeRunId") {
    throw "Created issue title mismatch. Body: $($createdIssueResponse.Content)"
  }

  Write-Step "Acknowledging smoke issue through operator-web proxy"
  $acknowledgeResponse = Invoke-OperatorRequest `
    -Method POST `
    -Url $acknowledgeIssuesUrl `
    -Session $webSession `
    -Body @{ ids = @($createdIssueId) }
  Assert-Status -Response $acknowledgeResponse -ExpectedStatus @(200) -Step "issue acknowledge"

  $acknowledgeBody = Read-JsonBody -Response $acknowledgeResponse -Step "issue acknowledge"
  if ([int64]$acknowledgeBody.acknowledged -ne 1) {
    throw "Expected acknowledged count 1, got $($acknowledgeBody.acknowledged). Body: $($acknowledgeResponse.Content)"
  }

  Write-Step "Verifying acknowledged issue status through operator-web proxy"
  $acknowledgedIssueResponse = Invoke-OperatorRequest `
    -Method GET `
    -Url $issueUrl `
    -Session $webSession
  Assert-Status -Response $acknowledgedIssueResponse -ExpectedStatus @(200) -Step "issue detail after acknowledge"

  $acknowledgedIssueBody = Read-JsonBody -Response $acknowledgedIssueResponse -Step "issue detail after acknowledge"
  if ($acknowledgedIssueBody.issue.status -ne "acknowledged") {
    throw "Expected acknowledged issue status acknowledged, got $($acknowledgedIssueBody.issue.status)"
  }

  Write-Step "Resolving smoke issue with a comment through operator-web proxy"
  $resolveIssueResponse = Invoke-OperatorRequest `
    -Method PATCH `
    -Url $issueUrl `
    -Session $webSession `
    -Body @{
      status = "resolved"
      comment = $smokeComment
      source_project = "operator-web-smoke"
      source_agent = "smoke-script"
    }
  Assert-Status -Response $resolveIssueResponse -ExpectedStatus @(200) -Step "issue resolve/comment"

  Write-Step "Verifying resolved status and comment through operator-web proxy"
  $resolvedIssueResponse = Invoke-OperatorRequest `
    -Method GET `
    -Url $issueUrl `
    -Session $webSession
  Assert-Status -Response $resolvedIssueResponse -ExpectedStatus @(200) -Step "issue detail after resolve/comment"

  $resolvedIssueBody = Read-JsonBody -Response $resolvedIssueResponse -Step "issue detail after resolve/comment"
  if ($resolvedIssueBody.issue.status -ne "resolved") {
    throw "Expected resolved issue status resolved, got $($resolvedIssueBody.issue.status)"
  }

  $matchingComments = @($resolvedIssueBody.comments) | Where-Object { $_.body -eq $smokeComment }
  if ($matchingComments.Count -ne 1) {
    throw "Expected one smoke comment on resolved issue, got $($matchingComments.Count). Body: $($resolvedIssueResponse.Content)"
  }

  Write-Step "Deleting smoke issue through operator-web proxy"
  $deleteIssueResponse = Invoke-OperatorRequest `
    -Method DELETE `
    -Url $issueUrl `
    -Session $webSession
  Assert-Status -Response $deleteIssueResponse -ExpectedStatus @(204) -Step "issue delete cleanup"

  Write-Step "Verifying smoke issue cleanup through operator-web proxy"
  $deletedIssueResponse = Invoke-OperatorRequest `
    -Method GET `
    -Url $issueUrl `
    -Session $webSession
  Assert-Status -Response $deletedIssueResponse -ExpectedStatus @(404) -Step "issue cleanup verification"
  $createdIssueId = $null

  Write-Step "Smoke passed"
  Write-Host ("LOGIN_STATUS=" + $loginResponse.StatusCode)
  Write-Host ("SETUP_STATUS=" + $setupNeededResponse.StatusCode)
  Write-Host ("SETUP_BODY=" + $setupNeededResponse.Content)
  Write-Host ("AUTH_LOGIN_STATUS=" + $loginApiResponse.StatusCode)
  Write-Host ("SELFCHECK_STATUS=" + $selfcheckResponse.StatusCode)
  Write-Host ("SELFCHECK_BODY=" + $selfcheckResponse.Content)
  Write-Host ("ISSUE_ID=" + $smokeIssueId)
  Write-Host ("ISSUE_CREATE_STATUS=" + $createIssueResponse.StatusCode)
  Write-Host ("ISSUE_ACKNOWLEDGE_STATUS=" + $acknowledgeResponse.StatusCode)
  Write-Host ("ISSUE_RESOLVE_COMMENT_STATUS=" + $resolveIssueResponse.StatusCode)
  Write-Host ("ISSUE_DELETE_STATUS=" + $deleteIssueResponse.StatusCode)
  Write-Host ("ISSUE_CLEANUP_GET_STATUS=" + $deletedIssueResponse.StatusCode)
}
finally {
  if ($null -ne $createdIssueId) {
    Write-Step "Cleaning up leftover smoke issue #$createdIssueId through operator-web proxy"
    try {
      $cleanupIssueResponse = Invoke-OperatorRequest `
        -Method DELETE `
        -Url "$issuesUrl/$createdIssueId" `
        -Session $webSession
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
      Invoke-Compose -Arguments @("down")
    }
    catch {
      Write-Warning $_
    }
  }
  else {
    Write-Step "Keeping stack up by request"
  }
}
