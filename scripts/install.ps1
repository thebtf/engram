# Engram - Windows Installation Script
# Usage: irm https://raw.githubusercontent.com/thebtf/engram/main/scripts/install.ps1 | iex
#
# Or with a specific version:
# $env:ENGRAM_VERSION = "v1.0.0"; irm https://raw.githubusercontent.com/thebtf/engram/main/scripts/install.ps1 | iex

param(
    [string]$Version = $env:ENGRAM_VERSION,
    [switch]$Uninstall
)

$ErrorActionPreference = "Stop"

# Configuration
$GitHubRepo = "thebtf/engram"
$InstallDir = "$env:USERPROFILE\.claude\plugins\marketplaces\engram"
$CacheDir = "$env:USERPROFILE\.claude\plugins\cache\engram\engram"
$PluginsFile = "$env:USERPROFILE\.claude\plugins\installed_plugins.json"
$SettingsFile = "$env:USERPROFILE\.claude\settings.json"
$MarketplacesFile = "$env:USERPROFILE\.claude\plugins\known_marketplaces.json"
$PluginKey = "engram@engram"

function Write-Info { param($Message) Write-Host "[INFO] $Message" -ForegroundColor Blue }
function Write-Success { param($Message) Write-Host "[OK] $Message" -ForegroundColor Green }
function Write-Warn { param($Message) Write-Host "[WARN] $Message" -ForegroundColor Yellow }
function Write-Error { param($Message) Write-Host "[ERROR] $Message" -ForegroundColor Red; exit 1 }

function Get-LatestVersion {
    try {
        $headers = @{}
        if ($env:GITHUB_TOKEN) {
            $headers["Authorization"] = "token $env:GITHUB_TOKEN"
        }
        $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$GitHubRepo/releases/latest" -Headers $headers
        return $release.tag_name
    } catch {
        $errorMsg = $_.Exception.Message
        if ($errorMsg -match "rate limit" -or $_.Exception.Response.StatusCode -eq 403) {
            Write-Host ""
            Write-Host "[ERROR] GitHub API rate limit exceeded." -ForegroundColor Red
            Write-Host ""
            Write-Host "You have a few options:" -ForegroundColor Yellow
            Write-Host "  1. Wait ~1 hour for the rate limit to reset"
            Write-Host "  2. Specify a version manually:"
            Write-Host "     `$env:ENGRAM_VERSION = 'v0.6.1'; irm https://raw.githubusercontent.com/$GitHubRepo/main/scripts/install.ps1 | iex" -ForegroundColor Cyan
            Write-Host "  3. Use a GitHub token (set `$env:GITHUB_TOKEN)"
            exit 1
        }
        Write-Error "Failed to fetch latest version from GitHub: $_"
    }
}

function Install-Release {
    param([string]$Version)

    $TempDir = New-Item -ItemType Directory -Path "$env:TEMP\engram-$(Get-Random)" -Force

    try {
        # Construct download URL
        $VersionClean = $Version -replace "^v", ""
        $ArchiveName = "engram_${VersionClean}_windows_amd64.zip"
        $DownloadUrl = "https://github.com/$GitHubRepo/releases/download/$Version/$ArchiveName"

        Write-Info "Downloading $ArchiveName..."
        $ZipPath = Join-Path $TempDir "release.zip"
        Invoke-WebRequest -Uri $DownloadUrl -OutFile $ZipPath -UseBasicParsing

        Write-Info "Extracting archive..."
        Expand-Archive -Path $ZipPath -DestinationPath $TempDir -Force

        # Create installation directories
        Write-Info "Installing to $InstallDir..."
        New-Item -ItemType Directory -Path "$InstallDir\hooks" -Force | Out-Null
        New-Item -ItemType Directory -Path "$InstallDir\.claude-plugin" -Force | Out-Null
        New-Item -ItemType Directory -Path "$InstallDir\commands" -Force | Out-Null

        # Copy binaries (goreleaser names: engram-server, engram-mcp, engram-mcp-stdio-proxy)
        Copy-Item "$TempDir\engram-server.exe" "$InstallDir\" -Force -ErrorAction SilentlyContinue
        Copy-Item "$TempDir\engram-mcp.exe" "$InstallDir\" -Force -ErrorAction SilentlyContinue
        Copy-Item "$TempDir\engram-mcp-stdio-proxy.exe" "$InstallDir\" -Force -ErrorAction SilentlyContinue
        # Copy JS hooks (required for plugin — stop on error)
        Copy-Item "$TempDir\hooks\*.js" "$InstallDir\hooks\" -Force -ErrorAction Stop
        Copy-Item "$TempDir\hooks\hooks.json" "$InstallDir\hooks\" -Force -ErrorAction Stop

        # Copy plugin configuration
        Copy-Item "$TempDir\.claude-plugin\*" "$InstallDir\.claude-plugin\" -Force

        # Copy slash commands if they exist in the release
        if (Test-Path "$TempDir\commands") {
            Copy-Item "$TempDir\commands\*" "$InstallDir\commands\" -Force -ErrorAction SilentlyContinue
        }

        # Copy skills if they exist in the release
        if (Test-Path "$TempDir\skills") {
            New-Item -ItemType Directory -Path "$InstallDir\skills" -Force | Out-Null
            Copy-Item "$TempDir\skills\*" "$InstallDir\skills\" -Recurse -Force -ErrorAction SilentlyContinue
        }

        # Copy MCP config if it exists in the release
        if (Test-Path "$TempDir\.mcp.json") {
            Copy-Item "$TempDir\.mcp.json" "$InstallDir\.mcp.json" -Force
        }

        Write-Success "Binaries installed to $InstallDir"
    } finally {
        Remove-Item -Recurse -Force $TempDir -ErrorAction SilentlyContinue
    }
}

function Register-Plugin {
    param([string]$Version)

    $Timestamp = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.000Z")
    $VersionClean = $Version -replace "^v", ""
    $CachePath = "$CacheDir\$VersionClean"

    # Ensure directories exist
    New-Item -ItemType Directory -Path "$env:USERPROFILE\.claude\plugins" -Force | Out-Null

    # Clean up old cache versions to prevent stale binaries
    $CacheBase = Split-Path $CachePath -Parent
    if (Test-Path $CacheBase) {
        Write-Info "Cleaning up old cache versions..."
        Get-ChildItem -Path $CacheBase -Directory | Where-Object { $_.Name -ne $VersionClean } | Remove-Item -Recurse -Force -ErrorAction SilentlyContinue
    }

    New-Item -ItemType Directory -Path $CachePath -Force | Out-Null

    # Create JSON files if they don't exist
    if (-not (Test-Path $PluginsFile)) {
        '{"version": 2, "plugins": {}}' | Out-File -Encoding UTF8 $PluginsFile
    }
    if (-not (Test-Path $SettingsFile)) {
        '{}' | Out-File -Encoding UTF8 $SettingsFile
    }
    if (-not (Test-Path $MarketplacesFile)) {
        '{}' | Out-File -Encoding UTF8 $MarketplacesFile
    }

    # Copy files to cache directory
    New-Item -ItemType Directory -Path "$CachePath\.claude-plugin" -Force | Out-Null
    New-Item -ItemType Directory -Path "$CachePath\hooks" -Force | Out-Null
    Copy-Item "$InstallDir\*" $CachePath -Recurse -Force -ErrorAction SilentlyContinue

    try {
        # Update installed_plugins.json
        $Plugins = Get-Content $PluginsFile -Raw | ConvertFrom-Json
        $PluginEntry = @(
            @{
                scope = "user"
                installPath = $CachePath
                version = $VersionClean
                installedAt = $Timestamp
                lastUpdated = $Timestamp
                isLocal = $true
            }
        )
        if (-not $Plugins.plugins) {
            $Plugins | Add-Member -NotePropertyName "plugins" -NotePropertyValue @{} -Force
        }
        $Plugins.plugins | Add-Member -NotePropertyName $PluginKey -NotePropertyValue $PluginEntry -Force
        $Plugins | ConvertTo-Json -Depth 10 | Out-File -Encoding UTF8 $PluginsFile
        Write-Success "Plugin registered in installed_plugins.json"

        # Update settings.json
        $Settings = Get-Content $SettingsFile -Raw | ConvertFrom-Json
        if (-not $Settings.enabledPlugins) {
            $Settings | Add-Member -NotePropertyName "enabledPlugins" -NotePropertyValue @{} -Force
        }
        $Settings.enabledPlugins | Add-Member -NotePropertyName $PluginKey -NotePropertyValue $true -Force

        # Configure statusline
        $StatuslineCmd = "node `"$InstallDir\hooks\statusline.js`""
        $StatuslineEntry = @{
            type = "command"
            command = $StatuslineCmd
            padding = 0
        }
        $Settings | Add-Member -NotePropertyName "statusLine" -NotePropertyValue $StatuslineEntry -Force

        $Settings | ConvertTo-Json -Depth 10 | Out-File -Encoding UTF8 $SettingsFile
        Write-Success "Plugin enabled in settings.json"
        Write-Success "Statusline configured in settings.json"

        # Note: MCP server registration is handled by the plugin's .mcp.json file.

        # Update known_marketplaces.json
        $Marketplaces = Get-Content $MarketplacesFile -Raw | ConvertFrom-Json
        $MarketplaceEntry = @{
            source = @{
                source = "directory"
                path = $InstallDir
            }
            installLocation = $InstallDir
            lastUpdated = $Timestamp
        }
        $Marketplaces | Add-Member -NotePropertyName "engram" -NotePropertyValue $MarketplaceEntry -Force
        $Marketplaces | ConvertTo-Json -Depth 10 | Out-File -Encoding UTF8 $MarketplacesFile
        Write-Success "Marketplace registered in known_marketplaces.json"
    } catch {
        Write-Warn "Plugin registration encountered an error: $_"
    }
}

function Setup-Connection {
    Write-Host ""
    Write-Info "Engram uses a remote server for storage. Configure your connection:"
    Write-Host ""

    $DefaultUrl = "http://localhost:37777/mcp"
    $ServerUrl = Read-Host "  Server URL [$DefaultUrl]"
    if ([string]::IsNullOrWhiteSpace($ServerUrl)) { $ServerUrl = $DefaultUrl }

    # Ensure URL ends with /mcp
    if (-not $ServerUrl.EndsWith("/mcp")) {
        $ServerUrl = $ServerUrl.TrimEnd("/") + "/mcp"
        Write-Info "Added /mcp suffix: $ServerUrl"
    }

    $ApiToken = Read-Host "  API Token (empty for no auth)"

    # Set persistent user environment variables
    [Environment]::SetEnvironmentVariable("ENGRAM_URL", $ServerUrl, "User")
    [Environment]::SetEnvironmentVariable("ENGRAM_API_TOKEN", $ApiToken, "User")
    Write-Success "Environment variables set (ENGRAM_URL, ENGRAM_API_TOKEN)"

    # Also set for current process
    $env:ENGRAM_URL = $ServerUrl
    $env:ENGRAM_API_TOKEN = $ApiToken
}

function Test-ServerHealth {
    $HealthUrl = $env:ENGRAM_URL -replace "/mcp$", "/health"
    Write-Info "Checking server health at $HealthUrl..."

    try {
        $response = Invoke-WebRequest -Uri $HealthUrl -UseBasicParsing -TimeoutSec 5
        Write-Success "Server is reachable"
    } catch {
        Write-Warn "Could not reach server at $HealthUrl"
        Write-Warn "Make sure your Engram server is running. See docs/DEPLOYMENT.md for setup."
    }
}

function Uninstall-Engram {
    param([switch]$KeepData)

    Write-Info "Uninstalling Engram..."

    # Remove directories
    Remove-Item -Recurse -Force $InstallDir -ErrorAction SilentlyContinue
    Remove-Item -Recurse -Force $CacheDir -ErrorAction SilentlyContinue

    # Remove from JSON files
    try {
        if (Test-Path $PluginsFile) {
            $Plugins = Get-Content $PluginsFile -Raw | ConvertFrom-Json
            $Plugins.plugins.PSObject.Properties.Remove($PluginKey)
            $Plugins | ConvertTo-Json -Depth 10 | Out-File -Encoding UTF8 $PluginsFile
        }
        if (Test-Path $SettingsFile) {
            $Settings = Get-Content $SettingsFile -Raw | ConvertFrom-Json
            if ($Settings.enabledPlugins) {
                $Settings.enabledPlugins.PSObject.Properties.Remove($PluginKey)
            }
            # Remove statusline if it's ours
            if ($Settings.statusLine -and $Settings.statusLine.command -match "engram") {
                $Settings.PSObject.Properties.Remove("statusLine")
            }
            $Settings | ConvertTo-Json -Depth 10 | Out-File -Encoding UTF8 $SettingsFile
        }
        if (Test-Path $MarketplacesFile) {
            $Marketplaces = Get-Content $MarketplacesFile -Raw | ConvertFrom-Json
            $Marketplaces.PSObject.Properties.Remove("engram")
            $Marketplaces | ConvertTo-Json -Depth 10 | Out-File -Encoding UTF8 $MarketplacesFile
        }
    } catch {
        Write-Warn "Error cleaning up JSON files: $_"
    }

    # Remove environment variables
    [Environment]::SetEnvironmentVariable("ENGRAM_URL", $null, "User")
    [Environment]::SetEnvironmentVariable("ENGRAM_API_TOKEN", $null, "User")

    # Handle data directory
    $DataDir = "$env:USERPROFILE\.engram"
    if (Test-Path $DataDir) {
        if ($KeepData) {
            Write-Warn "Keeping data directory: $DataDir"
        } else {
            Remove-Item -Recurse -Force $DataDir -ErrorAction SilentlyContinue
            Write-Success "Data directory removed"
        }
    }

    Write-Success "Engram uninstalled successfully"
}

# Main
Write-Host ""
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host "         Engram - Windows Installation Script          " -ForegroundColor Cyan
Write-Host "       Persistent Memory System for Claude Code                 " -ForegroundColor Cyan
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host ""

if ($Uninstall) {
    Uninstall-Engram
    exit 0
}

# Get version
if (-not $Version) {
    Write-Info "Fetching latest release..."
    $Version = Get-LatestVersion
}
Write-Info "Installing version: $Version"

# Install
Install-Release -Version $Version
Register-Plugin -Version $Version

# Configure server connection
Setup-Connection

# Verify server health
Test-ServerHealth

Write-Host ""
Write-Host "================================================================" -ForegroundColor Green
Write-Host "                  Installation Complete!                        " -ForegroundColor Green
Write-Host "================================================================" -ForegroundColor Green
Write-Host "  Restart Claude Code to activate the engram plugin." -ForegroundColor White
Write-Host "  Then run /engram:doctor to verify the connection." -ForegroundColor White
Write-Host ""
Write-Host "  Server setup: docs/DEPLOYMENT.md" -ForegroundColor White
Write-Host "================================================================" -ForegroundColor Green
Write-Host ""
