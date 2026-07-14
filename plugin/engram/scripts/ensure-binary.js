#!/usr/bin/env node
// ensure-binary.js - Downloads the engram binary if not present or outdated.
// Called by SessionStart hook and by the MCP wrapper when needed.
// Caches binary in PLUGIN_DATA/bin/ or CLAUDE_PLUGIN_DATA/bin/.
//
// Environment (set by Codex / Claude Code):
//   PLUGIN_ROOT / CLAUDE_PLUGIN_ROOT - plugin installation directory
//   PLUGIN_DATA / CLAUDE_PLUGIN_DATA - persistent plugin data directory

const fs = require("fs");
const path = require("path");
const https = require("https");
const http = require("http");
const { spawnSync } = require("child_process");

const REPO = "thebtf/engram";

async function main() {
  const pluginRoot = process.env.PLUGIN_ROOT || process.env.CLAUDE_PLUGIN_ROOT;
  const pluginData = process.env.PLUGIN_DATA || process.env.CLAUDE_PLUGIN_DATA;

  if (!pluginRoot || !pluginData) {
    // Not running inside plugin context - skip silently.
    return;
  }

  // Read desired version from plugin.json
  const pluginJsonPath = firstExistingPath([
    path.join(pluginRoot, ".codex-plugin", "plugin.json"),
    path.join(pluginRoot, ".claude-plugin", "plugin.json"),
  ]);
  let desiredVersion;
  try {
    const pluginJson = JSON.parse(fs.readFileSync(pluginJsonPath, "utf8"));
    desiredVersion = pluginJson.version;
  } catch {
    process.stderr.write("[engram] warning: could not read plugin.json\n");
    return;
  }

  if (!desiredVersion) return;

  // Detect platform
  const platform = process.platform; // win32, darwin, linux
  const arch = process.arch; // x64, arm64

  let suffix;
  let binaryName = "engram";

  if (platform === "win32") {
    suffix = "windows-amd64.exe";
    binaryName = "engram.exe";
  } else if (platform === "darwin") {
    suffix = arch === "arm64" ? "darwin-arm64" : "darwin-amd64";
  } else if (platform === "linux") {
    suffix = arch === "arm64" ? "linux-arm64" : "linux-amd64";
  } else {
    process.stderr.write(`[engram] unsupported platform: ${platform}\n`);
    return;
  }

  const binDir = path.join(pluginData, "bin");
  const binaryPath = path.join(binDir, binaryName);
  const versionFile = path.join(binDir, ".version");

  if (installedBinaryMatches(binaryPath, versionFile, desiredVersion)) {
    return; // Already up to date
  }

  process.stderr.write(
    `[engram] downloading v${desiredVersion} for ${platform}/${arch}...\n`
  );

  // Create bin directory
  fs.mkdirSync(binDir, { recursive: true });

  const url = `https://github.com/${REPO}/releases/download/v${desiredVersion}/engram-${suffix}`;
  const tmpPath = binaryPath + ".tmp";

  try {
    await download(url, tmpPath);
  } catch (err) {
    process.stderr.write(`[engram] download failed: ${err.message}\n`);
    try {
      fs.unlinkSync(tmpPath);
    } catch { }
    return; // Non-fatal
  }

  // Atomic swap: rename current → .old, then tmp → current.
  // This avoids deleting a running binary (fails on Windows).
  // The .old file is cleaned by upgrade.CleanStale on next daemon startup.
  let oldPath = null;
  try {
    if (fs.existsSync(binaryPath)) {
      oldPath = `${binaryPath}.old.${Date.now()}`;
      fs.renameSync(binaryPath, oldPath);
    }
    fs.renameSync(tmpPath, binaryPath);
  } catch (err) {
    // Try fallback: copy instead of rename (cross-device moves)
    try {
      fs.copyFileSync(tmpPath, binaryPath);
      fs.unlinkSync(tmpPath);
    } catch (copyErr) {
      process.stderr.write(`[engram] install failed: ${err.message}\n`);
      process.stderr.write(`[engram] fallback copy also failed: ${copyErr.message}\n`);
      // Rollback: restore old binary if we moved it away
      if (oldPath && !fs.existsSync(binaryPath) && fs.existsSync(oldPath)) {
        try { fs.renameSync(oldPath, binaryPath); } catch { }
      }
      return;
    }
  }

  // Make executable (no-op on Windows)
  if (platform !== "win32") {
    try {
      fs.chmodSync(binaryPath, 0o755);
    } catch { }
  }

  fs.writeFileSync(versionFile, desiredVersion);
  process.stderr.write(`[engram] installed v${desiredVersion} → ${binaryPath}\n`);

  // The newly installed binary reconciles any live muxcore daemon through
  // engine.RestartWithSuccessor during its normal launch. Restarting here used
  // a second product-local control path and never worked on Windows.
}

function firstExistingPath(candidates) {
  for (const candidate of candidates) {
    if (fs.existsSync(candidate)) {
      return candidate;
    }
  }
  return candidates[0];
}


// Follow redirects (GitHub releases redirect to S3)
function download(url, destPath) {
  return new Promise((resolve, reject) => {
    const get = url.startsWith("https") ? https.get : http.get;

    get(url, { headers: { "User-Agent": "engram-plugin" } }, (res) => {
      // Follow redirects (301, 302, 307)
      if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
        download(res.headers.location, destPath).then(resolve).catch(reject);
        res.resume();
        return;
      }

      if (res.statusCode !== 200) {
        res.resume();
        reject(new Error(`HTTP ${res.statusCode} from ${url}`));
        return;
      }

      const file = fs.createWriteStream(destPath);
      res.pipe(file);
      file.on("finish", () => {
        file.close(resolve);
      });
      file.on("error", (err) => {
        try { fs.unlinkSync(destPath); } catch { }
        reject(err);
      });
    }).on("error", reject);
  });
}

function installedBinaryMatches(
  binaryPath,
  versionFile,
  desiredVersion,
  readVersion = readBinaryVersion,
  probeRuns = binaryVersionProbeRuns
) {
  if (!fs.existsSync(binaryPath)) {
    return false;
  }

  let markerMatches = false;
  if (fs.existsSync(versionFile)) {
    try {
      const installed = fs.readFileSync(versionFile, "utf8").trim();
      if (installed !== desiredVersion) {
        return false;
      }
      markerMatches = true;
    } catch {
      return false;
    }
  }

  const actualVersion = readVersion(binaryPath);
  if (actualVersion) {
    return actualVersion === daemonVersionForPluginVersion(desiredVersion);
  }

  // Some Windows Codex/Desktop contexts deny Node child_process launches when
  // stdout/stderr are piped (EPERM), while the same binary launches normally
  // with inherited or ignored stdio. In that case, keep the previous stale-marker
  // protection: only trust the marker if it already matched the desired plugin
  // version and the executable at least starts for --version.
  return markerMatches && probeRuns(binaryPath);
}

function readBinaryVersion(binaryPath) {
  const result = spawnSync(binaryPath, ["--version"], {
    encoding: "utf8",
    timeout: 5000,
    windowsHide: true,
    env: versionProbeEnv(),
  });

  if (result.error || result.status !== 0) {
    return "";
  }

  const output = `${result.stdout || ""}\n${result.stderr || ""}`;
  const match = output.match(/\b(v?\d+\.\d+\.\d+(?:[-+][^\s]+)?)/);
  if (!match) {
    return "";
  }
  return daemonVersionForPluginVersion(match[1]);
}

function binaryVersionProbeRuns(binaryPath) {
  const result = spawnSync(binaryPath, ["--version"], {
    stdio: "ignore",
    timeout: 5000,
    windowsHide: true,
    env: versionProbeEnv(),
  });

  return !result.error && result.status === 0;
}

function versionProbeEnv() {
  return {
    ...process.env,
    ENGRAM_URL: "",
    ENGRAM_TOKEN: "",
  };
}

function daemonVersionForPluginVersion(version) {
  if (!version) {
    return "";
  }
  return version.startsWith("v") ? version : `v${version}`;
}

if (require.main === module) {
  main().catch((err) => {
    process.stderr.write(`[engram] ensure-binary error: ${err.message}\n`);
    // Non-fatal — plugin hooks still work, just no MCP daemon
  });
}

module.exports = {
  daemonVersionForPluginVersion,
  binaryVersionProbeRuns,
  installedBinaryMatches,
  readBinaryVersion,
};
