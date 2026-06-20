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

function Get-HtmlTitle {
  param(
    [Parameter(Mandatory = $true)]
    [string]$Html
  )

  $match = [regex]::Match($Html, '<title>(.*?)</title>', 'IgnoreCase')
  if (-not $match.Success) {
    throw "HTML title not found in response body."
  }

  $match.Groups[1].Value.Trim()
}

function Get-LocaleProbePath {
  param(
    [Parameter(Mandatory = $true)]
    [string]$Html
  )

  $match = [regex]::Match($Html, 'path:"([^"]*i18n/locales/[^"]+\.json)"')
  if (-not $match.Success) {
    throw "Locale asset path not found in operator-console HTML."
  }

  $match.Groups[1].Value
}

function Get-CspDirective {
  param(
    [Parameter(Mandatory = $true)]
    [string]$Csp,
    [Parameter(Mandatory = $true)]
    [string]$Name
  )

  foreach ($directive in ($Csp -split ';')) {
    $trimmed = $directive.Trim()
    if ($trimmed.StartsWith("$Name ")) {
      return $trimmed
    }
  }

  return ""
}

function Assert-NuxtInlineScriptCsp {
  param(
    [Parameter(Mandatory = $true)]
    [object]$Response,
    [Parameter(Mandatory = $true)]
    [string]$Step
  )

  $inlineScriptCount = [regex]::Matches($Response.Content, '<script(?![^>]+src=)[^>]*>', 'IgnoreCase').Count
  $csp = @($Response.Headers['Content-Security-Policy']) -join ','
  if ($inlineScriptCount -eq 0 -or $csp -eq "") {
    return [pscustomobject]@{
      InlineScriptCount = $inlineScriptCount
      ScriptSrc = ""
    }
  }

  $scriptSrc = Get-CspDirective -Csp $csp -Name "script-src"
  if ($scriptSrc -eq "") {
    $scriptSrc = Get-CspDirective -Csp $csp -Name "default-src"
  }

  $allowsInline = (
    $scriptSrc -match "'unsafe-inline'" -or
    $scriptSrc -match "'nonce-[^']+'" -or
    $scriptSrc -match "'sha(256|384|512)-[^']+'"
  )
  if (-not $allowsInline) {
    throw "$Step contains $inlineScriptCount inline script tag(s), but CSP '$scriptSrc' does not allow Nuxt inline bootstrap."
  }

  return [pscustomobject]@{
    InlineScriptCount = $inlineScriptCount
    ScriptSrc = $scriptSrc
  }
}

function Assert-LocaleAsset {
  param(
    [Parameter(Mandatory = $true)]
    [string]$Origin,
    [Parameter(Mandatory = $true)]
    [string]$LocalePath
  )

  $normalizedOrigin = $Origin.TrimEnd("/")
  $normalizedLocalePath = $LocalePath.TrimStart("/")
  $localeUrl = "$normalizedOrigin/$normalizedLocalePath"
  $localeResponse = Invoke-OperatorRequest -Method GET -Url $localeUrl
  Assert-Status -Response $localeResponse -ExpectedStatus @(200) -Step "locale asset"

  $contentType = @($localeResponse.Headers['Content-Type']) -join ','
  if ($contentType -notmatch 'application/json') {
    throw "Locale asset returned unexpected content-type '$contentType' from $localeUrl"
  }

  if (-not $localeResponse.Content -or $localeResponse.Content.TrimStart().StartsWith('<!DOCTYPE html>')) {
    throw "Locale asset returned HTML or empty content instead of JSON from $localeUrl"
  }

  return [pscustomobject]@{
    Url = $localeUrl
    StatusCode = [int]$localeResponse.StatusCode
    ContentType = $contentType
  }
}

$origin = "http://127.0.0.1:$OperatorConsolePort"
$workerOrigin = "http://127.0.0.1:$WorkerPort"
$workerRootUrl = "$workerOrigin/"
$rootUrl = "$origin/"
$readyUrl = "$origin/api/ready"
$selfcheckUrl = "$origin/api/selfcheck"
$statsUrl = "$origin/api/stats"
$statsVnextUrl = "$origin/api/stats/vnext"
$authMeUrl = "$origin/api/auth/me"
$authLoginUrl = "$origin/api/auth/login"
$issuesUrl = "$origin/api/issues"
$acknowledgeIssuesUrl = "$origin/api/issues/acknowledge"
$rulesUrl = "$origin/api/rules"
$workerAuthMeUrl = "$workerOrigin/api/auth/me"

$session = New-Object Microsoft.PowerShell.Commands.WebRequestSession
$createdIssueId = $null
$smokeIssueId = $null
$createdRuleId = $null
$smokeRuleId = $null
$smokeRunId = [guid]::NewGuid().ToString("N")
$smokeComment = "operator-console smoke resolved mutation $smokeRunId"

try {
  Write-Step "Building current-source server + operator-console images"
  Invoke-Compose -Arguments @("build", "server", "operator-console")

  Write-Step "Bringing up postgres + server + operator-console on pg=$PostgresPort worker=$WorkerPort web=$OperatorConsolePort mode=$Mode"
  Invoke-Compose -Arguments @("up", "-d", "--no-build", "postgres", "server", "operator-console")

  Write-Step "Waiting for operator-console root"
  $rootResponse = Wait-Http -Url $rootUrl -ExpectedStatus @(200)
  $rootTitle = Get-HtmlTitle -Html $rootResponse.Content
  if ($rootTitle -ne "engram · консоль оператора") {
    throw "Expected promoted operator-console title on dedicated host, got '$rootTitle'"
  }
  $rootCspProbe = Assert-NuxtInlineScriptCsp -Response $rootResponse -Step "dedicated operator-console root"
  $rootLocalePath = Get-LocaleProbePath -Html $rootResponse.Content
  $rootLocaleResponse = Assert-LocaleAsset -Origin $origin -LocalePath $rootLocalePath

  Write-Step "Checking worker root is proxied to the promoted operator-console"
  $workerRootResponse = Wait-Http -Url $workerRootUrl -ExpectedStatus @(200)
  $workerRootTitle = Get-HtmlTitle -Html $workerRootResponse.Content
  if ($workerRootTitle -ne "engram · консоль оператора") {
    throw "Expected worker root to serve the promoted operator-console, got '$workerRootTitle'"
  }
  $workerRootCspProbe = Assert-NuxtInlineScriptCsp -Response $workerRootResponse -Step "worker root"
  $workerLocalePath = Get-LocaleProbePath -Html $workerRootResponse.Content
  $workerLocaleResponse = Assert-LocaleAsset -Origin $workerOrigin -LocalePath $workerLocalePath

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

  Write-Step "Creating smoke rule through proxied operator-console API"
  $createRuleResponse = Invoke-OperatorRequest `
    -Method POST `
    -Url $rulesUrl `
    -Session $session `
    -Body @{
      content = "operator-console smoke rule $smokeRunId"
      priority = 21
      edited_by = "smoke-script"
    }
  Assert-Status -Response $createRuleResponse -ExpectedStatus @(201) -Step "rule create"

  $createRuleBody = Read-JsonBody -Response $createRuleResponse -Step "rule create"
  if ($null -eq $createRuleBody.id) {
    throw "Rule create response did not include id. Body: $($createRuleResponse.Content)"
  }

  $createdRuleId = [int64]$createRuleBody.id
  $smokeRuleId = $createdRuleId
  $ruleUrl = "$rulesUrl/$createdRuleId"

  Write-Step "Updating smoke rule"
  $updateRuleResponse = Invoke-OperatorRequest `
    -Method PATCH `
    -Url $ruleUrl `
    -Session $session `
    -Body @{
      priority = 34
      edited_by = "smoke-script"
    }
  Assert-Status -Response $updateRuleResponse -ExpectedStatus @(200) -Step "rule update"

  Write-Step "Listing rules to verify smoke rule presence"
  $listRulesResponse = Invoke-OperatorRequest `
    -Method GET `
    -Url "${rulesUrl}?limit=200" `
    -Session $session
  Assert-Status -Response $listRulesResponse -ExpectedStatus @(200) -Step "rule list"
  $listRulesBody = Read-JsonBody -Response $listRulesResponse -Step "rule list"
  $listedRule = $listRulesBody | Where-Object { $_.id -eq $createdRuleId }
  if ($null -eq $listedRule) {
    throw "Smoke rule #$createdRuleId not found in list output. Body: $($listRulesResponse.Content)"
  }

  Write-Step "Deleting smoke rule"
  $deleteRuleResponse = Invoke-OperatorRequest `
    -Method DELETE `
    -Url $ruleUrl `
    -Session $session
  Assert-Status -Response $deleteRuleResponse -ExpectedStatus @(200) -Step "rule delete"

  Write-Step "Verifying smoke rule cleanup"
  $postDeleteRulesResponse = Invoke-OperatorRequest `
    -Method GET `
    -Url "${rulesUrl}?limit=200" `
    -Session $session
  Assert-Status -Response $postDeleteRulesResponse -ExpectedStatus @(200) -Step "rule cleanup verification"
  $postDeleteRulesBody = Read-JsonBody -Response $postDeleteRulesResponse -Step "rule cleanup verification"
  if ($postDeleteRulesBody | Where-Object { $_.id -eq $createdRuleId }) {
    throw "Smoke rule #$createdRuleId still present after delete. Body: $($postDeleteRulesResponse.Content)"
  }
  $createdRuleId = $null

  Write-Step "Smoke passed"
  Write-Host ("MODE=" + $Mode)
  Write-Host ("ROOT_STATUS=" + $rootResponse.StatusCode)
  Write-Host ("ROOT_TITLE=" + $rootTitle)
  Write-Host ("ROOT_INLINE_SCRIPT_COUNT=" + $rootCspProbe.InlineScriptCount)
  Write-Host ("ROOT_CSP_SCRIPT_SRC=" + $rootCspProbe.ScriptSrc)
  Write-Host ("ROOT_LOCALE_ASSET_URL=" + $rootLocaleResponse.Url)
  Write-Host ("ROOT_LOCALE_ASSET_STATUS=" + $rootLocaleResponse.StatusCode)
  Write-Host ("ROOT_LOCALE_ASSET_CONTENT_TYPE=" + $rootLocaleResponse.ContentType)
  Write-Host ("WORKER_ROOT_STATUS=" + $workerRootResponse.StatusCode)
  Write-Host ("WORKER_ROOT_TITLE=" + $workerRootTitle)
  Write-Host ("WORKER_INLINE_SCRIPT_COUNT=" + $workerRootCspProbe.InlineScriptCount)
  Write-Host ("WORKER_CSP_SCRIPT_SRC=" + $workerRootCspProbe.ScriptSrc)
  Write-Host ("WORKER_LOCALE_ASSET_URL=" + $workerLocaleResponse.Url)
  Write-Host ("WORKER_LOCALE_ASSET_STATUS=" + $workerLocaleResponse.StatusCode)
  Write-Host ("WORKER_LOCALE_ASSET_CONTENT_TYPE=" + $workerLocaleResponse.ContentType)
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
  Write-Host ("RULE_ID=" + $smokeRuleId)
  Write-Host ("RULE_CREATE_STATUS=" + $createRuleResponse.StatusCode)
  Write-Host ("RULE_UPDATE_STATUS=" + $updateRuleResponse.StatusCode)
  Write-Host ("RULE_LIST_STATUS=" + $listRulesResponse.StatusCode)
  Write-Host ("RULE_DELETE_STATUS=" + $deleteRuleResponse.StatusCode)
  Write-Host ("RULE_CLEANUP_LIST_STATUS=" + $postDeleteRulesResponse.StatusCode)
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

  if ($null -ne $createdRuleId) {
    Write-Step "Cleaning up leftover smoke rule #$createdRuleId"
    try {
      $cleanupRuleResponse = Invoke-OperatorRequest `
        -Method DELETE `
        -Url "$rulesUrl/$createdRuleId" `
        -Session $session
      if (@(200, 404) -notcontains [int]$cleanupRuleResponse.StatusCode) {
        Write-Warning "Cleanup for smoke rule #$createdRuleId returned $($cleanupRuleResponse.StatusCode). Body: $($cleanupRuleResponse.Content)"
      }
    }
    catch {
      Write-Warning "Cleanup for smoke rule #$createdRuleId failed: $_"
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
