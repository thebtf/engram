# engram Windows installer (PowerShell)
#
# One-shot install:
#   irm https://raw.githubusercontent.com/thebtf/engram/main/scripts/install.ps1 | iex
#
# Pin a specific release:
#   $env:ENGRAM_VERSION = "v1.0.0"
#   irm https://raw.githubusercontent.com/thebtf/engram/main/scripts/install.ps1 | iex

param(
 [string]$Version  = $env:ENGRAM_VERSION,
 [switch]$Uninstall
)

$ErrorActionPreference = "Stop"

# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------
$GitHubRepo       = "thebtf/engram"
$InstallDir       = "$env:USERPROFILE\.claude\plugins\marketplaces\engram"
$CacheDir         = "$env:USERPROFILE\.claude\plugins\cache\engram\engram"
$PluginsFile      = "$env:USERPROFILE\.claude\plugins\installed_plugins.json"
$SettingsFile     = "$env:USERPROFILE\.claude\settings.json"
$MarketplacesFile = "$env:USERPROFILE\.claude\plugins\known_marketplaces.json"
$PluginKey        = "engram@engram"

# ---------------------------------------------------------------------------
# Output helpers
# ---------------------------------------------------------------------------
function Write-Info
{ param($Message) Write-Host "[INFO] $Message"  -ForegroundColor Blue   
}
function Write-Success
{ param($Message) Write-Host "[OK] $Message"    -ForegroundColor Green  
}
function Write-Warn
{ param($Message) Write-Host "[WARN] $Message"  -ForegroundColor Yellow 
}
function Write-Err
{ param($Message) Write-Host "[ERROR] $Message" -ForegroundColor Red; exit 1 
}

# The release policy is validated before any release files are installed.
function Assert-Node
{
 $Node = Get-Command node -ErrorAction SilentlyContinue
 if (-not $Node)
 {
  Write-Err "Node.js 18+ is required to validate the release bootstrap policy. Install Node.js 18 or newer and re-run this installer."
 }
 $NodeVersion = & $Node.Source --version 2>$null
 if ($LASTEXITCODE -ne 0 -or $NodeVersion -notmatch "^v?([0-9]+)\." -or [int]$Matches[1] -lt 18)
 {
  Write-Err "Node.js 18+ is required to validate the release bootstrap policy. Found $NodeVersion; install Node.js 18 or newer and re-run this installer."
 }
}

# ---------------------------------------------------------------------------
# Fetch latest release tag
# ---------------------------------------------------------------------------
function Get-LatestVersion
{
 try
 {
  $headers = @{}
  if ($env:GITHUB_TOKEN)
  {
   $headers["Authorization"] = "token $env:GITHUB_TOKEN"
  }
  $release = Invoke-RestMethod `
   -Uri "https://api.github.com/repos/$GitHubRepo/releases/latest" `
   -Headers $headers
  return $release.tag_name
 } catch
 {
  $msg = $_.Exception.Message
  if ($msg -match "rate limit" -or $_.Exception.Response.StatusCode -eq 403)
  {
   Write-Host ""
   Write-Host "[ERROR] GitHub API rate limit exceeded." -ForegroundColor Red
   Write-Host ""
   Write-Host "Options:" -ForegroundColor Yellow
   Write-Host "  1. Wait ~1 hour for the limit to reset"
   Write-Host "  2. Pin a version:"
   Write-Host "       `$env:ENGRAM_VERSION = 'v0.6.1'; irm https://raw.githubusercontent.com/$GitHubRepo/main/scripts/install.ps1 | iex" `
    -ForegroundColor Cyan
   Write-Host "  3. Set `$env:GITHUB_TOKEN to raise the limit"
   exit 1
  }
  Write-Err "Failed to fetch latest version from GitHub: $_"
 }
}

# ---------------------------------------------------------------------------
# Download release zip and lay out files into $InstallDir
# ---------------------------------------------------------------------------
function Install-Release
{
 param([string]$Ver)

 $TempDir = New-Item -ItemType Directory -Path "$env:TEMP\engram-$(Get-Random)" -Force

 try
 {
  $VersionClean = $Ver -replace "^v", ""
  $ArchiveName  = "engram_${VersionClean}_windows_amd64.zip"
  $DownloadUrl  = "https://github.com/$GitHubRepo/releases/download/$Ver/$ArchiveName"

  Write-Info "Downloading $ArchiveName..."
  $ZipPath = Join-Path $TempDir "release.zip"
  Invoke-WebRequest -Uri $DownloadUrl -OutFile $ZipPath -UseBasicParsing

  Write-Info "Extracting archive..."
  Expand-Archive -Path $ZipPath -DestinationPath $TempDir -Force

  if (-not (Test-Path "$TempDir\scripts\*.js"))
  {
   Write-Err "Release archive is missing required JS scripts in $TempDir\scripts"
  }
  $PolicyPath = Join-Path $TempDir "bootstrap-targets.json"
  if (-not (Test-Path -LiteralPath $PolicyPath -PathType Leaf))
  {
   Write-Err "Release archive is missing required bootstrap-targets.json"
  }

  # This validator is part of the trusted installer, not the release archive.
  # A release payload must never be allowed to validate its own policy.
  $ValidatorScript = @'
const fs = require('node:fs');
const [file, version] = process.argv.slice(2);
const assets = { 'win32-x64': 'engram-windows-amd64.exe', 'linux-x64': 'engram-linux-amd64', 'darwin-arm64': 'engram-darwin-arm64' };
const semver = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-(?:0|[1-9A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9A-Za-z-][0-9A-Za-z-]*))*)?$/;
const sha256 = /^[0-9a-f]{64}$/;
const exact = (value, fields) => {
  if (!value || typeof value !== 'object' || Array.isArray(value) || Object.keys(value).sort().join('\0') !== [...fields].sort().join('\0')) throw new Error('unknown or missing fields');
};
const assertNoDuplicateProperties = (text) => {
  let cursor = 0;
  const skip = () => { while (/\s/.test(text[cursor])) cursor += 1; };
  const string = () => {
    if (text[cursor] !== '"') throw new Error('bootstrap policy is not valid JSON');
    const start = cursor++;
    while (cursor < text.length) {
      const character = text[cursor++];
      if (character === '"') return JSON.parse(text.slice(start, cursor));
      if (character === '\\') { if (text[cursor++] === 'u') cursor += 4; }
      else if (character < ' ') throw new Error('bootstrap policy is not valid JSON');
    }
    throw new Error('bootstrap policy is not valid JSON');
  };
  const value = () => {
    skip();
    if (text[cursor] === '"') { string(); return; }
    if (text[cursor] === '{') {
      cursor += 1; skip();
      const keys = new Set();
      if (text[cursor] === '}') { cursor += 1; return; }
      for (;;) {
        skip(); const key = string();
        if (keys.has(key)) throw new Error('bootstrap policy has duplicate fields');
        keys.add(key); skip();
        if (text[cursor++] !== ':') throw new Error('bootstrap policy is not valid JSON');
        value(); skip();
        if (text[cursor] === '}') { cursor += 1; return; }
        if (text[cursor++] !== ',') throw new Error('bootstrap policy is not valid JSON');
      }
    }
    if (text[cursor] === '[') {
      cursor += 1; skip();
      if (text[cursor] === ']') { cursor += 1; return; }
      for (;;) {
        value(); skip();
        if (text[cursor] === ']') { cursor += 1; return; }
        if (text[cursor++] !== ',') throw new Error('bootstrap policy is not valid JSON');
      }
    }
    const token = /^(?:true|false|null|-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?)/.exec(text.slice(cursor));
    if (!token) throw new Error('bootstrap policy is not valid JSON');
    cursor += token[0].length;
  };
  value(); skip();
  if (cursor !== text.length) throw new Error('bootstrap policy is not valid JSON');
};
const text = fs.readFileSync(file, 'utf8');
assertNoDuplicateProperties(text);
const policy = JSON.parse(text);
exact(policy, ['schema_version', 'launcher_security_epoch', 'package_version', 'daemon_compat_epoch', 'targets', 'revoked_sha256', 'build_contract']);
if (policy.schema_version !== 1 || policy.launcher_security_epoch !== 1 || policy.daemon_compat_epoch !== 1 || policy.package_version !== version || !semver.test(version)) throw new Error('schema or version mismatch');
exact(policy.build_contract, ['go_version', 'trimpath', 'buildvcs', 'client_cgo', 'daemon_version_ldflag']);
const supportedGoVersions = version === '6.47.6' ? new Set(['1.26.6']) : new Set(['1.25.12']);
if (!supportedGoVersions.has(policy.build_contract.go_version) || policy.build_contract.trimpath !== true || policy.build_contract.buildvcs !== false || policy.build_contract.client_cgo !== false || policy.build_contract.daemon_version_ldflag !== `v${version}`) throw new Error('unsupported build contract');
if (!Array.isArray(policy.revoked_sha256) || policy.revoked_sha256.some((hash) => typeof hash !== 'string' || !sha256.test(hash)) || new Set(policy.revoked_sha256).size !== policy.revoked_sha256.length) throw new Error('invalid revocation list');
exact(policy.targets, Object.keys(assets));
for (const [key, asset] of Object.entries(assets)) {
  const target = policy.targets[key];
  exact(target, ['desired', 'predecessor']);
  if (target.predecessor !== null) throw new Error('predecessor is not allowed');
  exact(target.desired, ['version', 'asset', 'size', 'sha256']);
  if (target.desired.version !== version || target.desired.asset !== asset || !Number.isSafeInteger(target.desired.size) || target.desired.size <= 0 || target.desired.size > 128 * 1024 * 1024 || !sha256.test(target.desired.sha256) || policy.revoked_sha256.includes(target.desired.sha256)) throw new Error(`invalid target ${key}`);
}
'@
  try
  {
   $ValidatorScript | & node - $PolicyPath $VersionClean
   if ($LASTEXITCODE -ne 0)
   {
    throw 'schema or target matrix mismatch'
   }
  } catch
  {
   Write-Err "Release archive has an invalid bootstrap policy: $_"
  }

  Write-Info "Installing to $InstallDir..."
  New-Item -ItemType Directory -Path "$InstallDir\hooks"         -Force | Out-Null
  New-Item -ItemType Directory -Path "$InstallDir\scripts"       -Force | Out-Null
  New-Item -ItemType Directory -Path "$InstallDir\.claude-plugin" -Force | Out-Null
  New-Item -ItemType Directory -Path "$InstallDir\commands"       -Force | Out-Null

  # Server binary — present in all archives; let callers decide whether to use it
  Copy-Item "$TempDir\engram-server.exe" "$InstallDir\" -Force -ErrorAction SilentlyContinue

  # JS hooks are mandatory — stop on error so the failure is visible
  Copy-Item "$TempDir\hooks\*.js"      "$InstallDir\hooks\" -Force -ErrorAction Stop
  if (Test-Path "$TempDir\hooks\*.cjs")
  {
   Copy-Item "$TempDir\hooks\*.cjs" "$InstallDir\hooks\" -Force -ErrorAction Stop
  }
  Copy-Item "$TempDir\hooks\hooks.json" "$InstallDir\hooks\" -Force -ErrorAction Stop

  Copy-Item "$TempDir\scripts\*.js" "$InstallDir\scripts\" -Force -ErrorAction Stop
  Copy-Item $PolicyPath "$InstallDir\bootstrap-targets.json" -Force -ErrorAction Stop

  Copy-Item "$TempDir\.claude-plugin\*" "$InstallDir\.claude-plugin\" -Force

  if (Test-Path "$TempDir\commands")
  {
   Copy-Item "$TempDir\commands\*" "$InstallDir\commands\" -Force -ErrorAction SilentlyContinue
  }

  if (Test-Path "$TempDir\skills")
  {
   New-Item -ItemType Directory -Path "$InstallDir\skills" -Force | Out-Null
   Copy-Item "$TempDir\skills\*" "$InstallDir\skills\" -Recurse -Force -ErrorAction SilentlyContinue
  }

  if (Test-Path "$TempDir\.mcp.json")
  {
   Copy-Item "$TempDir\.mcp.json" "$InstallDir\.mcp.json" -Force
  }

  # Claude-specific transport config (referenced by .claude-plugin/plugin.json)
  if (Test-Path "$TempDir\claude\.mcp.json")
  {
   New-Item -ItemType Directory -Path "$InstallDir\claude" -Force | Out-Null
   Copy-Item "$TempDir\claude\.mcp.json" "$InstallDir\claude\.mcp.json" -Force
  }

  Write-Success "Files installed to $InstallDir"
 } finally
 {
  Remove-Item -Recurse -Force $TempDir -ErrorAction SilentlyContinue
 }
}

# ---------------------------------------------------------------------------
# Write plugin metadata into Claude Code's JSON registry files
# ---------------------------------------------------------------------------
function Register-Plugin
{
 param([string]$Ver)

 $Timestamp    = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.000Z")
 $VersionClean = $Ver -replace "^v", ""
 $CachePath    = "$CacheDir\$VersionClean"

 New-Item -ItemType Directory -Path "$env:USERPROFILE\.claude\plugins" -Force | Out-Null

 # Preserve stale cache versions: already-running sessions may have hook
 # commands pointing at their versioned cache path until they restart.
 # installed_plugins.json below points new sessions at $CachePath, so old
 # cache slots no longer shadow the new plugin.
 $CacheBase = Split-Path $CachePath -Parent
 if (Test-Path $CacheBase)
 {
  Write-Info "Preserving old cache versions for running-session hook compatibility..."
 }

 New-Item -ItemType Directory -Path $CachePath -Force | Out-Null

 # Bootstrap JSON files when they do not exist yet
 if (-not (Test-Path $PluginsFile))
 { '{"version": 2, "plugins": {}}' | Out-File -Encoding UTF8 $PluginsFile 
 }
 if (-not (Test-Path $SettingsFile))
 { '{}' | Out-File -Encoding UTF8 $SettingsFile 
 }
 if (-not (Test-Path $MarketplacesFile))
 { '{}' | Out-File -Encoding UTF8 $MarketplacesFile 
 }

 New-Item -ItemType Directory -Path "$CachePath\.claude-plugin" -Force | Out-Null
 New-Item -ItemType Directory -Path "$CachePath\hooks"          -Force | Out-Null
 Copy-Item "$InstallDir\*" $CachePath -Recurse -Force -ErrorAction SilentlyContinue

 try
 {
  # installed_plugins.json
  $Plugins     = Get-Content $PluginsFile -Raw | ConvertFrom-Json
  $PluginEntry = @(
   @{
    scope       = "user"
    installPath = $CachePath
    version     = $VersionClean
    installedAt = $Timestamp
    lastUpdated = $Timestamp
    isLocal     = $true
   }
  )
  if (-not $Plugins.plugins)
  {
   $Plugins | Add-Member -NotePropertyName "plugins" -NotePropertyValue ([PSCustomObject]@{}) -Force
  }
  $Plugins.plugins | Add-Member -NotePropertyName $PluginKey -NotePropertyValue $PluginEntry -Force
  $Plugins | ConvertTo-Json -Depth 10 | Out-File -Encoding UTF8 $PluginsFile
  Write-Success "Plugin registered in installed_plugins.json"

  # settings.json — enable plugin and configure statusline
  $Settings = Get-Content $SettingsFile -Raw | ConvertFrom-Json
  if (-not $Settings.enabledPlugins)
  {
   $Settings | Add-Member -NotePropertyName "enabledPlugins" -NotePropertyValue ([PSCustomObject]@{}) -Force
  }
  $Settings.enabledPlugins | Add-Member -NotePropertyName $PluginKey -NotePropertyValue $true -Force

  $StatuslineCmd   = "node `"$InstallDir\hooks\statusline.js`""
  $StatuslineEntry = @{ type = "command"; command = $StatuslineCmd; padding = 0 }
  $Settings | Add-Member -NotePropertyName "statusLine" -NotePropertyValue $StatuslineEntry -Force

  $Settings | ConvertTo-Json -Depth 10 | Out-File -Encoding UTF8 $SettingsFile
  Write-Success "Plugin enabled in settings.json"
  Write-Success "Statusline configured in settings.json"

  # MCP transport is declared in the plugin's .mcp.json — no settings.json edit needed

  # known_marketplaces.json
  $Marketplaces     = Get-Content $MarketplacesFile -Raw | ConvertFrom-Json
  $MarketplaceEntry = @{
   source          = @{ source = "directory"; path = $InstallDir }
   installLocation = $InstallDir
   lastUpdated     = $Timestamp
  }
  $Marketplaces | Add-Member -NotePropertyName "engram" -NotePropertyValue $MarketplaceEntry -Force
  $Marketplaces | ConvertTo-Json -Depth 10 | Out-File -Encoding UTF8 $MarketplacesFile
  Write-Success "Marketplace registered in known_marketplaces.json"
 } catch
 {
  Write-Host "[ERROR] Plugin registration failed: $_" -ForegroundColor Red
  Write-Host "[ERROR] installed_plugins.json / settings.json / known_marketplaces.json may not have been updated." -ForegroundColor Red
  exit 1
 }
}

# ---------------------------------------------------------------------------
# Interactive prompt for server URL and API token
# ---------------------------------------------------------------------------
function Setup-Connection
{
 Write-Host ""
 Write-Info "Engram stores memories on a remote server. Configure the connection:"
 Write-Host ""

 $DefaultUrl = "http://localhost:37777/mcp"
 $ServerUrl  = Read-Host "  Server URL [$DefaultUrl]"
 if ([string]::IsNullOrWhiteSpace($ServerUrl))
 { $ServerUrl = $DefaultUrl 
 }

 # Callers sometimes omit the MCP path segment
 if (-not $ServerUrl.EndsWith("/mcp"))
 {
  $ServerUrl = $ServerUrl.TrimEnd("/") + "/mcp"
  Write-Info "Added /mcp suffix: $ServerUrl"
 }

 $ApiToken = Read-Host "  API Token (leave blank for no auth)"

 # Persist to user environment so new shells inherit the settings.
 # ENGRAM_TOKEN is the canonical runtime name read by .mcp.json; ENGRAM_API_TOKEN
 # is kept for backward compatibility.
 [Environment]::SetEnvironmentVariable("ENGRAM_URL",       $ServerUrl, "User")
 [Environment]::SetEnvironmentVariable("ENGRAM_API_TOKEN", $ApiToken,  "User")
 [Environment]::SetEnvironmentVariable("ENGRAM_TOKEN",     $ApiToken,  "User")
 Write-Success "Environment variables set (ENGRAM_URL, ENGRAM_API_TOKEN, ENGRAM_TOKEN)"

 # Also active in the current process
 $env:ENGRAM_URL       = $ServerUrl
 $env:ENGRAM_API_TOKEN = $ApiToken
 $env:ENGRAM_TOKEN     = $ApiToken
}

# ---------------------------------------------------------------------------
# Sanity-check that the server is reachable
# ---------------------------------------------------------------------------
function Test-ServerHealth
{
 $HealthUrl = $env:ENGRAM_URL -replace "/mcp$", "/health"
 Write-Info "Checking server health at $HealthUrl..."
 try
 {
  Invoke-WebRequest -Uri $HealthUrl -UseBasicParsing -TimeoutSec 5 | Out-Null
  Write-Success "Server is reachable"
 } catch
 {
  Write-Warn "Could not reach server at $HealthUrl"
  Write-Warn "Ensure your Engram server is running. See docs/DEPLOYMENT.md for setup."
 }
}

# ---------------------------------------------------------------------------
# Remove all installed artefacts
# ---------------------------------------------------------------------------
function Uninstall-Engram
{
 param([switch]$KeepData)

 Write-Info "Uninstalling Engram..."

 # Stop any running engram process to release file locks before deletion
 $engProcs = Get-Process -Name 'engram*' -ErrorAction SilentlyContinue
 if ($engProcs)
 {
  Write-Info "Stopping engram process(es)..."
  $engProcs | Stop-Process -Force -ErrorAction SilentlyContinue
  $waited = 0
  while ((Get-Process -Name 'engram*' -ErrorAction SilentlyContinue) -and $waited -lt 10)
  {
   Start-Sleep -Seconds 1
   $waited++
  }
 }

 Remove-Item -Recurse -Force $InstallDir -ErrorAction SilentlyContinue
 Remove-Item -Recurse -Force $CacheDir   -ErrorAction SilentlyContinue

 try
 {
  if (Test-Path $PluginsFile)
  {
   $Plugins = Get-Content $PluginsFile -Raw | ConvertFrom-Json
   $Plugins.plugins.PSObject.Properties.Remove($PluginKey)
   $Plugins | ConvertTo-Json -Depth 10 | Out-File -Encoding UTF8 $PluginsFile
  }

  if (Test-Path $SettingsFile)
  {
   $Settings  = Get-Content $SettingsFile -Raw | ConvertFrom-Json
   $modified  = $false
   if ($Settings.enabledPlugins -and $Settings.enabledPlugins.PSObject.Properties[$PluginKey])
   {
    $Settings.enabledPlugins.PSObject.Properties.Remove($PluginKey)
    $modified = $true
   }
   if ($Settings.statusLine -and $Settings.statusLine.command -match "engram")
   {
    $Settings.PSObject.Properties.Remove("statusLine")
    $modified = $true
   }
   if ($modified)
   {
    $Settings | ConvertTo-Json -Depth 10 | Out-File -Encoding UTF8 $SettingsFile
    Write-Success "Removed from settings.json (including statusline)"
   }
  }

  if (Test-Path $MarketplacesFile)
  {
   $Marketplaces = Get-Content $MarketplacesFile -Raw | ConvertFrom-Json
   if ($Marketplaces.PSObject.Properties["engram"])
   {
    $Marketplaces.PSObject.Properties.Remove("engram")
    $Marketplaces | ConvertTo-Json -Depth 10 | Out-File -Encoding UTF8 $MarketplacesFile
    Write-Success "Removed from known_marketplaces.json"
   }
  }
 } catch
 {
  Write-Warn "Error cleaning up configuration files: $_"
 }

 # Clear persisted env vars (all names the installer may have written)
 [Environment]::SetEnvironmentVariable("ENGRAM_URL",       $null, "User")
 [Environment]::SetEnvironmentVariable("ENGRAM_API_TOKEN", $null, "User")
 [Environment]::SetEnvironmentVariable("ENGRAM_TOKEN",     $null, "User")

 $DataDir = "$env:USERPROFILE\.engram"
 if (Test-Path $DataDir)
 {
  if ($KeepData)
  {
   Write-Warn "Keeping data directory: $DataDir"
   Write-Warn "Remove manually later: Remove-Item -Recurse -Force $DataDir"
  } else
  {
   Remove-Item -Recurse -Force $DataDir -ErrorAction SilentlyContinue
   Write-Success "Data directory removed"
  }
 }

 Write-Success "Engram uninstalled successfully"
}

# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------
Write-Host ""
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host "         Engram - Windows Installation Script                  " -ForegroundColor Cyan
Write-Host "       Persistent Memory System for Claude Code                " -ForegroundColor Cyan
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host ""

if ($Uninstall)
{
 Uninstall-Engram
 exit 0
}

Assert-Node

if (-not $Version)
{
 Write-Info "Fetching latest release..."
 $Version = Get-LatestVersion
}
Write-Info "Installing version: $Version"

Install-Release  -Ver $Version
Register-Plugin  -Ver $Version
Setup-Connection
Test-ServerHealth

Write-Host ""
Write-Host "================================================================" -ForegroundColor Green
Write-Host "                  Installation Complete!                       " -ForegroundColor Green
Write-Host "================================================================" -ForegroundColor Green
Write-Host "  Restart Claude Code to activate the engram plugin."           -ForegroundColor White
Write-Host "  Then run /engram:doctor to verify the connection."            -ForegroundColor White
Write-Host ""
Write-Host "  Server setup: docs/DEPLOYMENT.md"                             -ForegroundColor White
Write-Host "================================================================" -ForegroundColor Green
Write-Host ""
