# engram Windows uninstaller (PowerShell)
#
# Removes plugin files, cache, and Claude Code registry entries.
# Optionally preserves the data directory.
#
# Usage:
#   irm https://raw.githubusercontent.com/thebtf/engram/main/scripts/uninstall.ps1 | iex
#
# Options:
#   -KeepData    Preserve %USERPROFILE%\.engram\ (database, embeddings)
#   -Purge       Remove everything including data (default)

param(
    [switch]$KeepData,
    [switch]$Purge
)

$ErrorActionPreference = "Stop"

# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------
$InstallDir       = "$env:USERPROFILE\.claude\plugins\marketplaces\engram"
$CacheDir         = "$env:USERPROFILE\.claude\plugins\cache\engram"
$DataDir          = "$env:USERPROFILE\.engram"
$PluginsFile      = "$env:USERPROFILE\.claude\plugins\installed_plugins.json"
$SettingsFile     = "$env:USERPROFILE\.claude\settings.json"
$MarketplacesFile = "$env:USERPROFILE\.claude\plugins\known_marketplaces.json"
$PluginKey        = "engram@engram"

# ---------------------------------------------------------------------------
# Output helpers
# ---------------------------------------------------------------------------
function Write-Info    { param($Message) Write-Host "[INFO] $Message"  -ForegroundColor Blue   }
function Write-Success { param($Message) Write-Host "[OK] $Message"    -ForegroundColor Green  }
function Write-Warn    { param($Message) Write-Host "[WARN] $Message"  -ForegroundColor Yellow }

Write-Host ""
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host "       Engram - Windows Uninstallation Script                  " -ForegroundColor Cyan
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host ""

# ---------------------------------------------------------------------------
# Stop any running server process before removing files
# ---------------------------------------------------------------------------
Write-Info "Stopping server processes..."
Get-Process | Where-Object {
    $_.ProcessName -like "*engram*" -and $_.Path -like "*engram*"
} | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 1
Write-Success "Server processes stopped"

# ---------------------------------------------------------------------------
# Remove plugin and cache directories
# ---------------------------------------------------------------------------
Write-Info "Removing plugin directories..."

if (Test-Path $InstallDir) {
    Remove-Item -Recurse -Force $InstallDir -ErrorAction SilentlyContinue
    Write-Success "Removed $InstallDir"
}
else {
    Write-Info "Plugin directory not found (already removed)"
}

if (Test-Path $CacheDir) {
    Remove-Item -Recurse -Force $CacheDir -ErrorAction SilentlyContinue
    Write-Success "Removed $CacheDir"
}

# ---------------------------------------------------------------------------
# Clean up Claude Code registry entries
# ---------------------------------------------------------------------------
Write-Info "Cleaning up Claude Code configuration..."

try {
    if (Test-Path $PluginsFile) {
        $Plugins = Get-Content $PluginsFile -Raw | ConvertFrom-Json
        if ($Plugins.plugins.PSObject.Properties[$PluginKey]) {
            $Plugins.plugins.PSObject.Properties.Remove($PluginKey)
            $Plugins | ConvertTo-Json -Depth 10 | Out-File -Encoding UTF8 $PluginsFile
            Write-Success "Removed from installed_plugins.json"
        }
    }

    if (Test-Path $SettingsFile) {
        $Settings = Get-Content $SettingsFile -Raw | ConvertFrom-Json
        $modified = $false
        if ($Settings.enabledPlugins -and $Settings.enabledPlugins.PSObject.Properties[$PluginKey]) {
            $Settings.enabledPlugins.PSObject.Properties.Remove($PluginKey)
            $modified = $true
        }
        if ($Settings.statusLine -and $Settings.statusLine.command -match "engram") {
            $Settings.PSObject.Properties.Remove("statusLine")
            $modified = $true
        }
        if ($modified) {
            $Settings | ConvertTo-Json -Depth 10 | Out-File -Encoding UTF8 $SettingsFile
            Write-Success "Removed from settings.json (including statusline)"
        }
    }

    if (Test-Path $MarketplacesFile) {
        $Marketplaces = Get-Content $MarketplacesFile -Raw | ConvertFrom-Json
        if ($Marketplaces.PSObject.Properties["engram"]) {
            $Marketplaces.PSObject.Properties.Remove("engram")
            $Marketplaces | ConvertTo-Json -Depth 10 | Out-File -Encoding UTF8 $MarketplacesFile
            Write-Success "Removed from known_marketplaces.json"
        }
    }
}
catch {
    Write-Warn "Error cleaning up configuration files: $_"
}

# ---------------------------------------------------------------------------
# Clear persisted environment variables
# ---------------------------------------------------------------------------
[Environment]::SetEnvironmentVariable("ENGRAM_URL",       $null, "User")
[Environment]::SetEnvironmentVariable("ENGRAM_API_TOKEN", $null, "User")

# ---------------------------------------------------------------------------
# Handle data directory
# ---------------------------------------------------------------------------
if (Test-Path $DataDir) {
    if ($KeepData) {
        Write-Warn "Keeping data directory: $DataDir"
        Write-Warn "Remove manually later: Remove-Item -Recurse -Force '$DataDir'"
    }
    else {
        Write-Info "Removing data directory..."
        Remove-Item -Recurse -Force $DataDir -ErrorAction SilentlyContinue
        Write-Success "Data directory removed"
    }
}

Write-Host ""
Write-Host "================================================================" -ForegroundColor Green
Write-Host "                Uninstallation Complete!                       " -ForegroundColor Green
Write-Host "================================================================" -ForegroundColor Green
Write-Host ""

if ($KeepData) {
    Write-Host "  Data preserved at: $DataDir" -ForegroundColor White
    Write-Host "  To reinstall: irm .../install.ps1 | iex" -ForegroundColor White
    Write-Host ""
}

Write-Success "Engram has been uninstalled"
