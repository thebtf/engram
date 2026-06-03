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
const { execSync } = require("child_process");

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
        try { fs.renameSync(oldPath, binaryPath); } catch {}
      }
      return;
    }
  }

  // Make executable (no-op on Windows)
  if (platform !== "win32") {
    try {
      fs.chmodSync(binaryPath, 0o755);
    } catch {}
  }

  fs.writeFileSync(versionFile, desiredVersion);
  process.stderr.write(`[engram] installed v${desiredVersion} → ${binaryPath}\n`);

  // Signal the running daemon to gracefully restart so it picks up the new
  // binary without waiting for the next Claude Code session start.
  await notifyDaemonRestart(pluginData, platform);
}

function firstExistingPath(candidates) {
  for (const candidate of candidates) {
    if (fs.existsSync(candidate)) {
      return candidate;
    }
  }
  return candidates[0];
}

// notifyDaemonRestart sends the "graceful-restart" command to the running
// daemon via the engram control socket, then waits for ACK.
//
// Failure modes are all non-fatal (return without throwing) so a failed
// notification never prevents the plugin from continuing.
//
// Platform support:
//   - Linux / macOS: connects to ${ENGRAM_DATA_DIR}/run/engram.sock
//   - Windows: named-pipe support deferred to v4.4.0 — logs WARN and returns
async function notifyDaemonRestart(pluginData, platform) {
  // The ENGRAM_DATA_DIR env var controls where the daemon stores its files.
  // Fall back to ${pluginData} (plugin's persistent data dir) if unset —
  // this mirrors the daemon's own dataDir() fallback logic in main.go.
  const engramDataDir = process.env.ENGRAM_DATA_DIR || pluginData;
  const pidPath = path.join(engramDataDir, "run", "engram.pid");
  const sockPath = path.join(engramDataDir, "run", "engram.sock");

  // Windows: named-pipe support deferred to v4.4.0 (no dependency on
  // third-party named-pipe helpers). The daemon will be restarted by the
  // supervisor on the next Claude Code session start.
  if (platform === "win32") {
    process.stderr.write(
      "[engram] graceful-restart skipped on Windows (named-pipe support deferred to v4.4.0)\n"
    );
    return;
  }

  // If the PID file is missing the daemon is not running — first install.
  if (!fs.existsSync(pidPath)) {
    return; // First install — nothing to restart.
  }

  try {
    const ack = await sendControlCommand(sockPath, "graceful-restart");
    if (ack === "ACK") {
      process.stderr.write("[engram] graceful-restart acknowledged by daemon\n");
    } else {
      process.stderr.write(`[engram] warn: unexpected daemon response: ${ack}\n`);
    }
  } catch (err) {
    // ECONNREFUSED or ENOENT → daemon already shut down or socket gone.
    // This is expected during first-time installs or if the daemon crashed.
    process.stderr.write(
      `[engram] warn: could not reach daemon control socket (${err.code || err.message}) — daemon will restart on next session\n`
    );
  }
}

// sendControlCommand connects to the Unix domain socket at sockPath, sends
// command + "\n", reads the response line, and returns it (without "\n").
// Rejects on connect failure, timeout, or socket errors.
function sendControlCommand(sockPath, command) {
  return new Promise((resolve, reject) => {
    const net = require("net");
    const conn = net.createConnection(sockPath);

    // 3-second timeout — the daemon should respond almost instantly.
    conn.setTimeout(3000);

    let buf = "";

    conn.on("connect", () => {
      conn.write(command + "\n");
    });

    conn.on("data", (chunk) => {
      buf += chunk.toString();
      const newlineIdx = buf.indexOf("\n");
      if (newlineIdx !== -1) {
        const line = buf.slice(0, newlineIdx).replace(/\r$/, "");
        conn.destroy();
        resolve(line);
      }
    });

    conn.on("timeout", () => {
      conn.destroy(new Error("socket timeout"));
    });

    conn.on("error", (err) => {
      reject(err);
    });
  });
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
        try { fs.unlinkSync(destPath); } catch {}
        reject(err);
      });
    }).on("error", reject);
  });
}

main().catch((err) => {
  process.stderr.write(`[engram] ensure-binary error: ${err.message}\n`);
  // Non-fatal — plugin hooks still work, just no MCP daemon
});
