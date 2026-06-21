param(
  [string]$BaseUrl = "http://unleashed.lan:37777",
  [string]$WorkerBaseUrl = "http://unleashed.lan:37777",
  [string]$ExpectedTitle = "engram · консоль оператора",
  [ValidateSet("disabled", "token", "anonymous")]
  [string]$Mode = "anonymous",
  [string]$AdminToken = "",
  [switch]$ValidateOnly
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Write-Step {
  param([string]$Message)
  Write-Host "[operator-console-remote-smoke] $Message"
}

function Assert-RemoteSmokeBaseUrl {
  param(
    [Parameter(Mandatory = $true)]
    [string]$Value,
    [Parameter(Mandatory = $true)]
    [string]$Name
  )

  if ([string]::IsNullOrWhiteSpace($Value)) {
    throw "$Name must not be blank. Expected http://unleashed.lan:37777."
  }

  $uri = $null
  if (-not [uri]::TryCreate($Value, [uriKind]::Absolute, [ref]$uri)) {
    throw "$Name must be an absolute URL. Expected http://unleashed.lan:37777, got '$Value'."
  }

  if ($uri.Port -eq 3000) {
    throw "$Name points at :3000, which is the old/dev target. Use http://unleashed.lan:37777."
  }

  $normalized = $uri.AbsoluteUri.TrimEnd("/")
  if ($normalized -ne "http://unleashed.lan:37777") {
    throw "$Name must be http://unleashed.lan:37777 for OC-1 remote proof. Got '$normalized'."
  }
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

function Assert-JsonContent {
  param(
    [Parameter(Mandatory = $true)]
    [object]$Response,
    [Parameter(Mandatory = $true)]
    [string]$Step
  )

  $contentType = @($Response.Headers['Content-Type']) -join ','
  if ($contentType -notmatch 'application/json') {
    throw "$Step returned unexpected content-type '$contentType'."
  }

  $content = [string]$Response.Content
  if ([string]::IsNullOrWhiteSpace($content)) {
    throw "$Step returned an empty body where JSON is required."
  }

  if ($content.TrimStart().StartsWith('<!DOCTYPE html>')) {
    throw "$Step returned the SPA shell instead of JSON."
  }

  try {
    $content | ConvertFrom-Json
  }
  catch {
    throw "$Step returned invalid JSON: $($_.Exception.Message). Body prefix: $($content.Substring(0, [Math]::Min(240, $content.Length)))"
  }
}

function Assert-JsonArray {
  param(
    [Parameter(Mandatory = $true)]
    [object]$Response,
    [Parameter(Mandatory = $true)]
    [string]$Step
  )

  $trimmed = ([string]$Response.Content).TrimStart()
  if (-not $trimmed.StartsWith('[')) {
    throw "$Step returned JSON, but not an array. Body prefix: $($trimmed.Substring(0, [Math]::Min(240, $trimmed.Length)))"
  }

  @(Assert-JsonContent -Response $Response -Step $Step)
}

function Assert-JsonObject {
  param(
    [Parameter(Mandatory = $true)]
    [object]$Response,
    [Parameter(Mandatory = $true)]
    [string]$Step
  )

  $trimmed = ([string]$Response.Content).TrimStart()
  if (-not $trimmed.StartsWith('{')) {
    throw "$Step returned JSON, but not an object. Body prefix: $($trimmed.Substring(0, [Math]::Min(240, $trimmed.Length)))"
  }

  Assert-JsonContent -Response $Response -Step $Step
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

function Get-LocaleProbePath {
  param(
    [Parameter(Mandatory = $true)]
    [string]$Html
  )

  $match = [regex]::Match($Html, 'path:"([^"]*i18n/locales/[^"]+\.json)"')
  if (-not $match.Success) {
    throw "Locale asset path not found in operator-console HTML."
  }

  return $match.Groups[1].Value
}

function Get-CspDirective {
  param(
    [Parameter(Mandatory = $true)]
    [string]$Csp,
    [Parameter(Mandatory = $true)]
    [string]$Name
  )

  foreach ($directive in ($Csp -split '[;,]')) {
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

  $scriptMatches = [regex]::Matches(
    $Response.Content,
    '<script\b([^>]*)>(.*?)</script>',
    [System.Text.RegularExpressions.RegexOptions]'IgnoreCase,Singleline'
  )
  $csp = @($Response.Headers['Content-Security-Policy']) -join ','
  $inlineScripts = @()
  foreach ($scriptMatch in $scriptMatches) {
    $attrs = $scriptMatch.Groups[1].Value
    $content = $scriptMatch.Groups[2].Value
    if ($attrs -match '\ssrc\s*=' -or $content.Length -eq 0) {
      continue
    }

    $inlineScripts += [pscustomobject]@{
      Attrs = $attrs
      Content = $content
    }
  }

  if ($inlineScripts.Count -eq 0 -or $csp -eq "") {
    return [pscustomobject]@{
      InlineScriptCount = $inlineScripts.Count
      ScriptSrc = ""
    }
  }

  $scriptSrc = Get-CspDirective -Csp $csp -Name "script-src"
  if ($scriptSrc -eq "") {
    $scriptSrc = Get-CspDirective -Csp $csp -Name "default-src"
  }

  $allowsInline = (
    $scriptSrc -match "'unsafe-inline'"
  )
  if ($allowsInline) {
    return [pscustomobject]@{
      InlineScriptCount = $inlineScripts.Count
      ScriptSrc = $scriptSrc
    }
  }

  for ($i = 0; $i -lt $inlineScripts.Count; $i++) {
    $script = $inlineScripts[$i]
    $nonceMatch = [regex]::Match($script.Attrs, '\snonce\s*=\s*["'']?([^"''\s>]+)', 'IgnoreCase')
    if ($nonceMatch.Success -and $scriptSrc.Contains("'nonce-$($nonceMatch.Groups[1].Value)'")) {
      continue
    }

    $sha256 = [System.Security.Cryptography.SHA256]::Create()
    try {
      $bytes = [System.Text.Encoding]::UTF8.GetBytes($script.Content)
      $hash = [Convert]::ToBase64String($sha256.ComputeHash($bytes))
    }
    finally {
      $sha256.Dispose()
    }
    if ($scriptSrc.Contains("'sha256-$hash'")) {
      continue
    }

    throw "$Step inline script #$($i + 1) is not covered by CSP '$scriptSrc'."
  }

  return [pscustomobject]@{
    InlineScriptCount = $inlineScripts.Count
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
  $localeResponse = Invoke-Http -Method GET -Url $localeUrl
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

Assert-RemoteSmokeBaseUrl -Value $BaseUrl -Name "BaseUrl"
Assert-RemoteSmokeBaseUrl -Value $WorkerBaseUrl -Name "WorkerBaseUrl"

$normalizedBaseUrl = $BaseUrl.TrimEnd("/")
$normalizedWorkerBaseUrl = $WorkerBaseUrl.TrimEnd("/")
$rootUrl = "$normalizedBaseUrl/"
$authMeUrl = "$normalizedBaseUrl/api/auth/me"
$authLoginUrl = "$normalizedBaseUrl/api/auth/login"
$selfcheckUrl = "$normalizedBaseUrl/api/selfcheck"
$statsUrl = "$normalizedBaseUrl/api/stats"
$statsVnextUrl = "$normalizedBaseUrl/api/stats/vnext"
$projectsUrl = "$normalizedBaseUrl/api/projects"
$rulesUrl = "$normalizedBaseUrl/api/rules?limit=5"
$issuesUrl = "$normalizedBaseUrl/api/issues?limit=5"
$vaultStatusUrl = "$normalizedBaseUrl/api/vault/status"
$configUrl = "$normalizedBaseUrl/api/config"
$workerHealthUrl = "$normalizedWorkerBaseUrl/health"

$session = New-Object Microsoft.PowerShell.Commands.WebRequestSession

if ($ValidateOnly) {
  Write-Step "ValidateOnly requested; target contract is valid and no remote request was sent"
  Write-Host ("BASE_URL=" + $normalizedBaseUrl)
  Write-Host ("WORKER_BASE_URL=" + $normalizedWorkerBaseUrl)
  Write-Host "REMOTE_TARGET_STATUS=validated"
  return
}

Write-Step "Checking public operator-console root"
$rootResponse = Invoke-Http -Method GET -Url $rootUrl
Assert-Status -Response $rootResponse -ExpectedStatus @(200) -Step "root"
$title = Get-Title -Html $rootResponse.Content
if ($title -ne $ExpectedTitle) {
  throw "Root does not look like the promoted operator-console. Expected title '$ExpectedTitle', got '$title'."
}
$rootCspProbe = Assert-NuxtInlineScriptCsp -Response $rootResponse -Step "root"
$localePath = Get-LocaleProbePath -Html $rootResponse.Content

Write-Step "Checking locale asset serving for the SPA shell"
$localeResponse = Assert-LocaleAsset -Origin $normalizedBaseUrl -LocalePath $localePath

Write-Step "Checking proxied stats endpoints"
$statsResponse = Invoke-Http -Method GET -Url $statsUrl -Session $session
Assert-Status -Response $statsResponse -ExpectedStatus @(200) -Step "stats"
$statsJson = Assert-JsonObject -Response $statsResponse -Step "stats"
$statsVnextResponse = Invoke-Http -Method GET -Url $statsVnextUrl -Session $session
Assert-Status -Response $statsVnextResponse -ExpectedStatus @(200) -Step "stats/vnext"
$statsVnextJson = Assert-JsonObject -Response $statsVnextResponse -Step "stats/vnext"

Write-Step "Checking read-only operator-console data endpoints"
$projectsResponse = Invoke-Http -Method GET -Url $projectsUrl -Session $session
Assert-Status -Response $projectsResponse -ExpectedStatus @(200) -Step "projects"
$projects = Assert-JsonArray -Response $projectsResponse -Step "projects"
if ($projects.Count -eq 0) {
  throw "projects returned an empty array; memory and projects pages cannot prove live data."
}

$memoryProjectCount = 0
$memoryRowCount = 0
foreach ($project in $projects) {
  $projectName = [string]$project
  if ([string]::IsNullOrWhiteSpace($projectName)) {
    continue
  }

  $encodedProject = [uri]::EscapeDataString($projectName)
  $memoryResponse = Invoke-Http -Method GET -Url "$normalizedBaseUrl/api/memories?project=$encodedProject&limit=200" -Session $session
  Assert-Status -Response $memoryResponse -ExpectedStatus @(200) -Step "memories[$projectName]"
  $memoryRows = Assert-JsonArray -Response $memoryResponse -Step "memories[$projectName]"
  $memoryProjectCount += 1
  $memoryRowCount += @($memoryRows).Count
}

if ($memoryProjectCount -eq 0) {
  throw "No non-empty project names were available for memory endpoint smoke."
}

$rulesResponse = Invoke-Http -Method GET -Url $rulesUrl -Session $session
Assert-Status -Response $rulesResponse -ExpectedStatus @(200) -Step "rules"
$rulesJson = Assert-JsonArray -Response $rulesResponse -Step "rules"

$issuesResponse = Invoke-Http -Method GET -Url $issuesUrl -Session $session
Assert-Status -Response $issuesResponse -ExpectedStatus @(200) -Step "issues"
$issuesJson = Assert-JsonObject -Response $issuesResponse -Step "issues"
if (-not $issuesJson.PSObject.Properties.Name.Contains('issues')) {
  throw "issues response is missing required 'issues' array."
}

$vaultStatusResponse = Invoke-Http -Method GET -Url $vaultStatusUrl -Session $session
Assert-Status -Response $vaultStatusResponse -ExpectedStatus @(200) -Step "vault/status"
$vaultStatusJson = Assert-JsonObject -Response $vaultStatusResponse -Step "vault/status"
foreach ($field in @('credential_count', 'key_configured')) {
  if (-not $vaultStatusJson.PSObject.Properties.Name.Contains($field)) {
    throw "vault/status response is missing required '$field'."
  }
}

$configResponse = Invoke-Http -Method GET -Url $configUrl -Session $session
Assert-Status -Response $configResponse -ExpectedStatus @(200) -Step "config"
$configJson = Assert-JsonObject -Response $configResponse -Step "config"
foreach ($field in @('features', 'memory', 'storage')) {
  if (-not $configJson.PSObject.Properties.Name.Contains($field)) {
    throw "config response is missing required '$field'."
  }
}

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
Write-Host ("ROOT_INLINE_SCRIPT_COUNT=" + $rootCspProbe.InlineScriptCount)
Write-Host ("ROOT_CSP_SCRIPT_SRC=" + $rootCspProbe.ScriptSrc)
Write-Host ("LOCALE_ASSET_URL=" + $localeResponse.Url)
Write-Host ("LOCALE_ASSET_STATUS=" + $localeResponse.StatusCode)
Write-Host ("LOCALE_ASSET_CONTENT_TYPE=" + $localeResponse.ContentType)
Write-Host ("STATS_STATUS=" + $statsResponse.StatusCode)
Write-Host ("STATS_VNEXT_STATUS=" + $statsVnextResponse.StatusCode)
Write-Host ("PROJECTS_STATUS=" + $projectsResponse.StatusCode)
Write-Host ("PROJECTS_COUNT=" + $projects.Count)
Write-Host ("MEMORY_PROJECTS_CHECKED=" + $memoryProjectCount)
Write-Host ("MEMORY_ROWS_CHECKED=" + $memoryRowCount)
Write-Host ("RULES_STATUS=" + $rulesResponse.StatusCode)
Write-Host ("RULES_SAMPLE_COUNT=" + @($rulesJson).Count)
Write-Host ("ISSUES_STATUS=" + $issuesResponse.StatusCode)
Write-Host ("ISSUES_SAMPLE_COUNT=" + @($issuesJson.issues).Count)
Write-Host ("VAULT_STATUS=" + $vaultStatusResponse.StatusCode)
Write-Host ("VAULT_CREDENTIAL_COUNT=" + $vaultStatusJson.credential_count)
Write-Host ("CONFIG_STATUS=" + $configResponse.StatusCode)
Write-Host ("WORKER_HEALTH_STATUS=" + $workerHealthResponse.StatusCode)
Write-Host ("AUTH_ME_STATUS=" + $authMeResponse.StatusCode)
Write-Host ("AUTH_ME_BODY=" + $authMeResponse.Content)
if ($Mode -eq "token") {
  Write-Host ("AUTH_LOGIN_STATUS=" + $loginResponse.StatusCode)
  Write-Host ("SELFCHECK_STATUS=" + $selfcheckResponse.StatusCode)
}
