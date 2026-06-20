param(
  [Parameter(Mandatory = $true)]
  [string]$BaseUrl,
  [string]$WorkerBaseUrl = "http://unleashed.lan:37777",
  [string]$ExpectedTitle = "engram · консоль оператора",
  [ValidateSet("disabled", "token", "anonymous")]
  [string]$Mode = "anonymous",
  [string]$AdminToken = ""
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Write-Step {
  param([string]$Message)
  Write-Host "[operator-console-remote-smoke] $Message"
}

function Invoke-Http {
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

function Get-Title {
  param(
    [Parameter(Mandatory = $true)]
    [string]$Html
  )

  $match = [regex]::Match($Html, '<title>(.*?)</title>', 'IgnoreCase')
  if (-not $match.Success) {
    return ""
  }

  return $match.Groups[1].Value.Trim()
}

$normalizedBaseUrl = $BaseUrl.TrimEnd("/")
$normalizedWorkerBaseUrl = $WorkerBaseUrl.TrimEnd("/")
$rootUrl = "$normalizedBaseUrl/"
$authMeUrl = "$normalizedBaseUrl/api/auth/me"
$authLoginUrl = "$normalizedBaseUrl/api/auth/login"
$selfcheckUrl = "$normalizedBaseUrl/api/selfcheck"
$statsUrl = "$normalizedBaseUrl/api/stats"
$statsVnextUrl = "$normalizedBaseUrl/api/stats/vnext"
$workerHealthUrl = "$normalizedWorkerBaseUrl/health"

$session = New-Object Microsoft.PowerShell.Commands.WebRequestSession

Write-Step "Checking public operator-console root"
$rootResponse = Invoke-Http -Method GET -Url $rootUrl
Assert-Status -Response $rootResponse -ExpectedStatus @(200) -Step "root"
$title = Get-Title -Html $rootResponse.Content
if ($title -ne $ExpectedTitle) {
  throw "Root does not look like the promoted operator-console. Expected title '$ExpectedTitle', got '$title'."
}

Write-Step "Checking proxied stats endpoints"
$statsResponse = Invoke-Http -Method GET -Url $statsUrl -Session $session
Assert-Status -Response $statsResponse -ExpectedStatus @(200) -Step "stats"
$statsVnextResponse = Invoke-Http -Method GET -Url $statsVnextUrl -Session $session
Assert-Status -Response $statsVnextResponse -ExpectedStatus @(200) -Step "stats/vnext"

Write-Step "Checking worker health endpoint"
$workerHealthResponse = Invoke-Http -Method GET -Url $workerHealthUrl
Assert-Status -Response $workerHealthResponse -ExpectedStatus @(200) -Step "worker health"

if ($Mode -eq "disabled") {
  Write-Step "Expecting synthetic admin auth/me response"
  $authMeResponse = Invoke-Http -Method GET -Url $authMeUrl -Session $session
  Assert-Status -Response $authMeResponse -ExpectedStatus @(200) -Step "auth/me disabled mode"
}
elseif ($Mode -eq "token") {
  if (-not $AdminToken) {
    throw "AdminToken is required in token mode."
  }

  Write-Step "Expecting pre-login auth/me unauthorized"
  $preLoginResponse = Invoke-Http -Method GET -Url $authMeUrl -Session $session
  Assert-Status -Response $preLoginResponse -ExpectedStatus @(401) -Step "pre-login auth/me"

  Write-Step "Logging in with admin token"
  $loginResponse = Invoke-Http -Method POST -Url $authLoginUrl -Session $session -Body @{ token = $AdminToken }
  Assert-Status -Response $loginResponse -ExpectedStatus @(200) -Step "auth login"

  Write-Step "Checking authenticated selfcheck"
  $selfcheckResponse = Invoke-Http -Method GET -Url $selfcheckUrl -Session $session
  Assert-Status -Response $selfcheckResponse -ExpectedStatus @(200) -Step "authenticated selfcheck"

  Write-Step "Checking authenticated auth/me"
  $authMeResponse = Invoke-Http -Method GET -Url $authMeUrl -Session $session
  Assert-Status -Response $authMeResponse -ExpectedStatus @(200) -Step "auth/me token mode"
}
else {
  Write-Step "Expecting anonymous auth/me unauthorized"
  $authMeResponse = Invoke-Http -Method GET -Url $authMeUrl -Session $session
  Assert-Status -Response $authMeResponse -ExpectedStatus @(401) -Step "auth/me anonymous mode"
}

Write-Step "Remote smoke passed"
Write-Host ("MODE=" + $Mode)
Write-Host ("ROOT_STATUS=" + $rootResponse.StatusCode)
Write-Host ("ROOT_TITLE=" + $title)
Write-Host ("STATS_STATUS=" + $statsResponse.StatusCode)
Write-Host ("STATS_VNEXT_STATUS=" + $statsVnextResponse.StatusCode)
Write-Host ("WORKER_HEALTH_STATUS=" + $workerHealthResponse.StatusCode)
Write-Host ("AUTH_ME_STATUS=" + $authMeResponse.StatusCode)
Write-Host ("AUTH_ME_BODY=" + $authMeResponse.Content)
if ($Mode -eq "token") {
  Write-Host ("AUTH_LOGIN_STATUS=" + $loginResponse.StatusCode)
  Write-Host ("SELFCHECK_STATUS=" + $selfcheckResponse.StatusCode)
}
