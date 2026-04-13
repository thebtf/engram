#!/usr/bin/env node
// ensure-binary.js — Downloads the engram binary if not present or outdated.
// Called by SessionStart hook. Caches binary in CLAUDE_PLUGIN_DATA/bin/.
//
// Environment (set by Claude Code):
//   CLAUDE_PLUGIN_ROOT — plugin installation directory
//   CLAUDE_PLUGIN_DATA — persistent data directory (~/.claude/plugins/data/{id}/)

const fs = require("fs");
const path = require("path");
const https = require("https");
const http = require("http");
const { execSync } = require("child_process");

const REPO = "thebtf/engram";

async function main() {
  const pluginRoot = process.env.CLAUDE_PLUGIN_ROOT;
  const pluginData = process.env.CLAUDE_PLUGIN_DATA;

  if (!pluginRoot || !pluginData) {
    // Not running inside Claude Code plugin context — skip silently
    return;
  }

  // Read desired version from plugin.json
  const pluginJsonPath = path.join(pluginRoot, ".claude-plugin", "plugin.json");
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

  // Check if correct version already installed
  if (fs.existsSync(binaryPath) && fs.existsSync(versionFile)) {
    try {
      const installed = fs.readFileSync(versionFile, "utf8").trim();
      if (installed === desiredVersion) {
        return; // Already up to date
      }
    } catch {
      // Version file unreadable — re-download
    }
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
    } catch {}
    return; // Non-fatal
  }

  // Atomic replace
  try {
    if (fs.existsSync(binaryPath)) fs.unlinkSync(binaryPath);
    fs.renameSync(tmpPath, binaryPath);
  } catch (err) {
    process.stderr.write(`[engram] install failed: ${err.message}\n`);
    return;
  }

  // Make executable (no-op on Windows)
  if (platform !== "win32") {
    try {
      fs.chmodSync(binaryPath, 0o755);
    } catch {}
  }

  fs.writeFileSync(versionFile, desiredVersion);
  process.stderr.write(`[engram] installed v${desiredVersion} → ${binaryPath}\n`);
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
        fs.unlinkSync(destPath);
        reject(err);
      });
    }).on("error", reject);
  });
}

main().catch((err) => {
  process.stderr.write(`[engram] ensure-binary error: ${err.message}\n`);
  // Non-fatal — plugin hooks still work, just no MCP daemon
});
