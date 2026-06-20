param(
  [string]$BaseUrl = "http://unleashed.lan:3000",
  [string]$WorkerBaseUrl = "http://unleashed.lan:37777",
  [string]$Email = "",
  [string]$Password = ""
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Write-Step {
  param([string]$Message)
  Write-Host "[operator-web-remote-smoke] $Message"
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

$normalizedBaseUrl = $BaseUrl.TrimEnd("/")
$normalizedWorkerBaseUrl = $WorkerBaseUrl.TrimEnd("/")
$loginUrl = "$normalizedBaseUrl/login"
$setupNeededUrl = "$normalizedBaseUrl/api/auth/setup-needed"
$selfcheckUrl = "$normalizedBaseUrl/api/selfcheck"
$versionUrl = "$normalizedWorkerBaseUrl/api/version"

$session = New-Object Microsoft.PowerShell.Commands.WebRequestSession

Write-Step "Checking public login shell"
$loginResponse = Invoke-Http -Method GET -Url $loginUrl
Assert-Status -Response $loginResponse -ExpectedStatus @(200) -Step "login shell"

Write-Step "Checking proxied setup-needed endpoint"
$setupNeededResponse = Invoke-Http -Method GET -Url $setupNeededUrl
Assert-Status -Response $setupNeededResponse -ExpectedStatus @(200) -Step "setup-needed"
$setupNeededBody = Read-JsonBody -Response $setupNeededResponse -Step "setup-needed"

Write-Step "Checking worker version endpoint"
$versionResponse = Invoke-Http -Method GET -Url $versionUrl
Assert-Status -Response $versionResponse -ExpectedStatus @(200) -Step "worker version"
$versionBody = Read-JsonBody -Response $versionResponse -Step "worker version"

$didAuthenticatedFlow = $false
if ($Email -or $Password) {
  if (-not $Email -or -not $Password) {
    throw "Email and Password must be provided together for authenticated smoke."
  }

  $loginApiUrl = "$normalizedBaseUrl/api/auth/user-login"
  Write-Step "Logging in with operator credentials"
  $authLoginResponse = Invoke-Http -Method POST -Url $loginApiUrl -Session $session -Body @{
    email = $Email
    password = $Password
  }
  Assert-Status -Response $authLoginResponse -ExpectedStatus @(200) -Step "user-login"

  Write-Step "Checking authenticated selfcheck"
  $selfcheckResponse = Invoke-Http -Method GET -Url $selfcheckUrl -Session $session
  Assert-Status -Response $selfcheckResponse -ExpectedStatus @(200) -Step "authenticated selfcheck"
  $selfcheckBody = Read-JsonBody -Response $selfcheckResponse -Step "authenticated selfcheck"
  $didAuthenticatedFlow = $true
}

Write-Step "Remote smoke passed"
Write-Host ("BASE_URL=" + $normalizedBaseUrl)
Write-Host ("WORKER_BASE_URL=" + $normalizedWorkerBaseUrl)
Write-Host ("LOGIN_STATUS=" + $loginResponse.StatusCode)
Write-Host ("SETUP_STATUS=" + $setupNeededResponse.StatusCode)
Write-Host ("SETUP_BODY=" + $setupNeededResponse.Content)
Write-Host ("WORKER_VERSION_STATUS=" + $versionResponse.StatusCode)
Write-Host ("WORKER_VERSION_BODY=" + $versionResponse.Content)
Write-Host ("AUTHENTICATED_FLOW=" + $didAuthenticatedFlow.ToString().ToLowerInvariant())
if ($didAuthenticatedFlow) {
  Write-Host ("SELFCHECK_STATUS=" + $selfcheckResponse.StatusCode)
  Write-Host ("SELFCHECK_BODY=" + $selfcheckResponse.Content)
}
