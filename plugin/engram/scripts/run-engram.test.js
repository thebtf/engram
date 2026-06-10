const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");
const { execFileSync } = require("node:child_process");

const {
  appendStartupDiagnosticLog,
  configuredEnvValue,
  describeEnvValue,
  formatStartupDiagnostic,
  inferCodexPluginDataDir,
  resolvePluginData,
  spawnFailureMessage,
  trimStartupDiagnosticLog,
} = require("./run-engram.js");

test("MCP config launches wrapper via Codex plugin-root interpolation", () => {
  const mcpPath = path.resolve(__dirname, "..", ".mcp.json");
  const payload = JSON.parse(fs.readFileSync(mcpPath, "utf8"));
  const server = payload.mcpServers.engram;

  assert.equal(server.command, "node");
  assert.deepEqual(server.args, ["${CLAUDE_PLUGIN_ROOT}/scripts/run-engram.js"]);
  assert.equal(server.cwd, ".");
});

test("MCP config never interpolates user_config in the env block", () => {
  // Regression guard: ${user_config.*} inside a plugin .mcp.json env block
  // makes Claude Code silently skip spawning the MCP server
  // (anthropics/claude-code#51573). userConfig values reach plugin
  // subprocesses as CLAUDE_PLUGIN_OPTION_<KEY> instead.
  const mcpPath = path.resolve(__dirname, "..", ".mcp.json");
  const raw = fs.readFileSync(mcpPath, "utf8");
  assert.doesNotMatch(raw, /\$\{user_config\./);

  const payload = JSON.parse(raw);
  const server = payload.mcpServers.engram;
  assert.equal(server.env, undefined);
  assert.ok(server.env_vars.includes("ENGRAM_URL"));
  assert.ok(server.env_vars.includes("ENGRAM_TOKEN"));
});

test("MCP config does not fall back to workspace cwd when launching wrapper", () => {
  const mcpPath = path.resolve(__dirname, "..", ".mcp.json");
  const payload = JSON.parse(fs.readFileSync(mcpPath, "utf8"));
  const server = payload.mcpServers.engram;
  const tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "engram-mcp-entrypoint-"));
  const scriptsDir = path.join(tmpRoot, "scripts");
  const markerPath = path.join(tmpRoot, "main-ran.txt");

  fs.mkdirSync(scriptsDir, { recursive: true });
  fs.writeFileSync(
    path.join(scriptsDir, "run-engram.js"),
    [
      "const fs = require('node:fs');",
      "fs.writeFileSync(process.env.ENGRAM_TEST_MARKER, process.argv.slice(1).join('|'));",
      "",
    ].join("\n")
  );

  const args = expandMcpArgsForTest(server.args, tmpRoot);
  execFileSync(process.execPath, args, {
    cwd: os.tmpdir(),
    env: {
      ...process.env,
      CLAUDE_PLUGIN_ROOT: "",
      ENGRAM_TEST_MARKER: markerPath,
      PLUGIN_ROOT: "",
    },
  });

  assert.equal(
    fs.readFileSync(markerPath, "utf8"),
    path.join(tmpRoot, "scripts", "run-engram.js")
  );
});

test("MCP config preserves host-provided argv", () => {
  const mcpPath = path.resolve(__dirname, "..", ".mcp.json");
  const payload = JSON.parse(fs.readFileSync(mcpPath, "utf8"));
  const server = payload.mcpServers.engram;
  const tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "engram-mcp-argv-"));
  const scriptsDir = path.join(tmpRoot, "scripts");
  const markerPath = path.join(tmpRoot, "argv.txt");

  fs.mkdirSync(scriptsDir, { recursive: true });
  fs.writeFileSync(
    path.join(scriptsDir, "run-engram.js"),
    [
      "const fs = require('node:fs');",
      "fs.writeFileSync(process.env.ENGRAM_TEST_MARKER, process.argv.slice(1).join('|'));",
      "",
    ].join("\n")
  );

  const args = expandMcpArgsForTest(server.args, tmpRoot);
  execFileSync(process.execPath, [...args, "--from-host", "value"], {
    cwd: os.tmpdir(),
    env: {
      ...process.env,
      CLAUDE_PLUGIN_ROOT: "",
      ENGRAM_TEST_MARKER: markerPath,
      PLUGIN_ROOT: "",
    },
  });

  assert.equal(
    fs.readFileSync(markerPath, "utf8"),
    [path.join(tmpRoot, "scripts", "run-engram.js"), "--from-host", "value"].join("|")
  );
});

test("infers Codex plugin data dir from installed cache root", () => {
  const codexHome = path.join(os.tmpdir(), "codex-home");
  const pluginRoot = path.join(
    codexHome,
    "plugins",
    "cache",
    "engram-marketplace",
    "engram",
    "6.4.4"
  );

  assert.equal(
    inferCodexPluginDataDir(pluginRoot),
    path.join(codexHome, "plugins", "data", "engram-marketplace-engram")
  );
});

test("explicit plugin data env takes precedence over inferred Codex path", () => {
  const previousPluginData = process.env.PLUGIN_DATA;
  const previousClaudePluginData = process.env.CLAUDE_PLUGIN_DATA;
  const explicit = path.join(os.tmpdir(), "explicit-engram-data");

  try {
    process.env.PLUGIN_DATA = explicit;
    delete process.env.CLAUDE_PLUGIN_DATA;

    const pluginRoot = path.join(
      os.tmpdir(),
      "plugins",
      "cache",
      "engram-marketplace",
      "engram",
      "6.4.4"
    );

    assert.equal(resolvePluginData(pluginRoot), explicit);
  } finally {
    restoreEnv("PLUGIN_DATA", previousPluginData);
    restoreEnv("CLAUDE_PLUGIN_DATA", previousClaudePluginData);
  }
});

test("Claude plugin data env takes precedence when PLUGIN_DATA is absent", () => {
  const previousPluginData = process.env.PLUGIN_DATA;
  const previousClaudePluginData = process.env.CLAUDE_PLUGIN_DATA;
  const explicit = path.join(os.tmpdir(), "explicit-claude-engram-data");

  try {
    delete process.env.PLUGIN_DATA;
    process.env.CLAUDE_PLUGIN_DATA = explicit;

    const pluginRoot = path.join(
      os.tmpdir(),
      "plugins",
      "cache",
      "engram-marketplace",
      "engram",
      "6.4.4"
    );

    assert.equal(resolvePluginData(pluginRoot), explicit);
  } finally {
    restoreEnv("PLUGIN_DATA", previousPluginData);
    restoreEnv("CLAUDE_PLUGIN_DATA", previousClaudePluginData);
  }
});

test("falls back to plugin-local data dir outside Codex cache layout", () => {
  const previousPluginData = process.env.PLUGIN_DATA;
  const previousClaudePluginData = process.env.CLAUDE_PLUGIN_DATA;

  try {
    delete process.env.PLUGIN_DATA;
    delete process.env.CLAUDE_PLUGIN_DATA;

    const pluginRoot = path.join(os.tmpdir(), "engram-plugin-root");

    assert.equal(resolvePluginData(pluginRoot), path.join(pluginRoot, ".data"));
  } finally {
    restoreEnv("PLUGIN_DATA", previousPluginData);
    restoreEnv("CLAUDE_PLUGIN_DATA", previousClaudePluginData);
  }
});

test("reports spawnSync launch errors instead of treating them as exit status", () => {
  const message = spawnFailureMessage(
    { error: new Error("access denied"), status: null, signal: null },
    "engram exec"
  );

  assert.equal(message, "[engram] engram exec failed: access denied\n");
});

test("reports signal termination from spawnSync results", () => {
  const message = spawnFailureMessage(
    { error: undefined, status: null, signal: "SIGTERM" },
    "ensure-binary"
  );

  assert.equal(message, "[engram] ensure-binary terminated by signal SIGTERM\n");
});

test("does not report spawn failure for normal numeric exit status", () => {
  const message = spawnFailureMessage(
    { error: undefined, status: 2, signal: null },
    "ensure-binary"
  );

  assert.equal(message, "");
});

test("startup diagnostic classifies env values without leaking token contents", () => {
  const diagnostic = formatStartupDiagnostic({
    ENGRAM_URL: " http://example.test:37777 ",
    ENGRAM_TOKEN: "engram_secret_keycard_value",
    ENGRAM_SERVER_URL: "",
    ENGRAM_CLAUDE_USERCONFIG_URL: "${user_config.server_url}",
    CLAUDE_PLUGIN_OPTION_api_token: "engram_secret_keycard_value",
  });

  assert.match(diagnostic, /ENGRAM_URL=present\(len=25\)/);
  assert.match(diagnostic, /ENGRAM_TOKEN=redacted\(len=27\)/);
  assert.match(diagnostic, /ENGRAM_SERVER_URL=empty/);
  assert.match(diagnostic, /ENGRAM_CLAUDE_USERCONFIG_URL=placeholder/);
  assert.match(diagnostic, /CLAUDE_PLUGIN_OPTION_api_token=redacted\(len=27\)/);
  assert.doesNotMatch(diagnostic, /engram_secret_keycard_value/);
});

test("wrapper falls back to CLAUDE_PLUGIN_OPTION userConfig env names", () => {
  const previousToken = process.env.ENGRAM_TOKEN;
  const previousOption = process.env.CLAUDE_PLUGIN_OPTION_api_token;

  try {
    delete process.env.ENGRAM_TOKEN;
    process.env.CLAUDE_PLUGIN_OPTION_api_token = "engram_from_user_config";

    assert.equal(
      configuredEnvValue("ENGRAM_TOKEN", "CLAUDE_PLUGIN_OPTION_api_token"),
      "engram_from_user_config"
    );

    process.env.ENGRAM_TOKEN = "engram_explicit_env_wins";
    assert.equal(
      configuredEnvValue("ENGRAM_TOKEN", "CLAUDE_PLUGIN_OPTION_api_token"),
      "engram_explicit_env_wins"
    );
  } finally {
    restoreEnv("ENGRAM_TOKEN", previousToken);
    restoreEnv("CLAUDE_PLUGIN_OPTION_api_token", previousOption);
  }
});

test("describeEnvValue reports missing and placeholder states", () => {
  assert.equal(describeEnvValue("MISSING", {}), "MISSING=missing");
  assert.equal(
    describeEnvValue("PLACEHOLDER", { PLACEHOLDER: "${secret.value}" }, true),
    "PLACEHOLDER=placeholder"
  );
});

test("appendStartupDiagnosticLog writes bounded plugin-data log", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "engram-startup-log-"));
  appendStartupDiagnosticLog(dir, "[engram] startup env: ENGRAM_TOKEN=redacted(len=10)", new Date("2026-06-03T18:00:00Z"));

  const logPath = path.join(dir, "logs", "startup-env.log");
  const content = fs.readFileSync(logPath, "utf8");
  assert.match(content, /2026-06-03T18:00:00\.000Z pid=\d+ \[engram\] startup env:/);
  assert.doesNotMatch(content, /secret/);
});

test("trimStartupDiagnosticLog keeps complete log entries after truncation", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "engram-trim-log-"));
  const logPath = path.join(dir, "startup-env.log");

  fs.writeFileSync(
    logPath,
    [
      "2026-06-03T18:00:00.000Z pid=1 [engram] startup env: ENGRAM_URL=present(len=26)",
      "2026-06-03T18:00:01.000Z pid=2 [engram] startup env: ENGRAM_URL=present(len=26)",
      "2026-06-03T18:00:02.000Z pid=3 [engram] startup env: ENGRAM_URL=present(len=26)",
    ].join("\n") + "\n",
    "utf8"
  );

  trimStartupDiagnosticLog(logPath, 220);

  const content = fs.readFileSync(logPath, "utf8");
  assert.match(content, /^2026-06-03T18:00:02\.000Z pid=3 /);
});

function restoreEnv(key, value) {
  if (value === undefined) {
    delete process.env[key];
    return;
  }
  process.env[key] = value;
}

function expandMcpArgsForTest(args, pluginRoot) {
  return args.map((arg) => arg.replace("${CLAUDE_PLUGIN_ROOT}", pluginRoot.replaceAll("\\", "/")));
}
