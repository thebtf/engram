const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");

const {
  appendStartupDiagnosticLog,
  childEnvForEngram,
  configuredEnvValue,
  describeConfigFile,
  describeEnvValue,
  formatStartupDiagnostic,
  inferCodexPluginDataDir,
  isConfiguredValue,
  readEngramConfigFile,
  resolveConfigFilePath,
  resolvePluginData,
  resolvePluginRoot,
  resolveAndSpawn,
  spawnFailureMessage,
  trimStartupDiagnosticLog,
} = require("./run-engram.js");

test("Codex MCP config launches wrapper via plugin-root-relative path", () => {
  // Codex does NOT interpolate ${CLAUDE_PLUGIN_ROOT} in plugin .mcp.json args —
  // the literal string reaches node and the server dies with MODULE_NOT_FOUND.
  // Codex resolves relative args against the plugin root via cwd ".".
  const mcpPath = path.resolve(__dirname, "..", ".mcp.json");
  const payload = JSON.parse(fs.readFileSync(mcpPath, "utf8"));
  const server = payload.mcpServers.engram;

  assert.equal(server.command, "node");
  assert.deepEqual(server.args, ["./scripts/run-engram.js"]);
  assert.equal(server.cwd, ".");
});

test("Claude MCP config launches wrapper via CLAUDE_PLUGIN_ROOT interpolation", () => {
  // Claude Code interpolates ${CLAUDE_PLUGIN_ROOT} but does NOT resolve
  // relative args against the plugin root, so the Claude variant keeps the
  // interpolated absolute path. .claude-plugin/plugin.json points here.
  const manifestPath = path.resolve(__dirname, "..", ".claude-plugin", "plugin.json");
  const manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));
  assert.equal(manifest.mcpServers, "./claude/.mcp.json");

  const mcpPath = path.resolve(__dirname, "..", "claude", ".mcp.json");
  const payload = JSON.parse(fs.readFileSync(mcpPath, "utf8"));
  const server = payload.mcpServers.engram;

  assert.equal(server.command, "node");
  assert.deepEqual(server.args, ["${CLAUDE_PLUGIN_ROOT}/scripts/run-engram.js"]);
});

test("MCP configs never interpolate user_config in an env block", () => {
  // Regression guard: ${user_config.*} inside a plugin .mcp.json env block
  // makes Claude Code silently skip spawning the MCP server
  // (anthropics/claude-code#51573). userConfig values reach plugin
  // subprocesses as CLAUDE_PLUGIN_OPTION_<KEY> instead.
  for (const rel of ["../.mcp.json", "../claude/.mcp.json"]) {
    const mcpPath = path.resolve(__dirname, rel);
    const raw = fs.readFileSync(mcpPath, "utf8");
    assert.doesNotMatch(raw, /\$\{user_config\./, `user_config leak in ${rel}`);

    const payload = JSON.parse(raw);
    const server = payload.mcpServers.engram;
    assert.equal(server.env, undefined, `env block present in ${rel}`);
    assert.ok(server.env_vars.includes("ENGRAM_URL"), `ENGRAM_URL missing in ${rel}`);
    assert.ok(server.env_vars.includes("ENGRAM_TOKEN"), `ENGRAM_TOKEN missing in ${rel}`);
  }
});

test("release-facing plugin and marketplace versions stay aligned", () => {
  const repoRoot = path.resolve(__dirname, "..", "..", "..");
  const readJson = (...segments) => JSON.parse(fs.readFileSync(path.join(repoRoot, ...segments), "utf8"));
  const claudePlugin = readJson("plugin", "engram", ".claude-plugin", "plugin.json");
  const codexPlugin = readJson("plugin", "engram", ".codex-plugin", "plugin.json");
  const rootPlugin = readJson(".claude-plugin", "plugin.json");
  const claudeMarketplace = readJson(".claude-plugin", "marketplace.json");
  const ompMarketplace = readJson(".omp-plugin", "marketplace.json");

  assert.equal(claudePlugin.version, "6.47.0");
  assert.equal(codexPlugin.version, claudePlugin.version);
  assert.equal(rootPlugin.version, claudePlugin.version);
  assert.equal(claudeMarketplace.version, claudePlugin.version);
  assert.equal(claudeMarketplace.plugins[0].version, claudePlugin.version);
  assert.equal(ompMarketplace.version, claudePlugin.version);
  assert.equal(ompMarketplace.plugins[0].version, claudePlugin.version);
});

test("Codex MCP config resolves the wrapper from plugin-root cwd without executing it", () => {
  const mcpPath = path.resolve(__dirname, "..", ".mcp.json");
  const payload = JSON.parse(fs.readFileSync(mcpPath, "utf8"));
  const server = payload.mcpServers.engram;
  assert.equal(server.cwd, ".");
  assert.equal(path.resolve(path.dirname(mcpPath), server.args[0]), path.join(path.dirname(mcpPath), "scripts", "run-engram.js"));
});

test("Claude MCP config preserves host argv via the package-root wrapper path", () => {
  const mcpPath = path.resolve(__dirname, "..", "claude", ".mcp.json");
  const payload = JSON.parse(fs.readFileSync(mcpPath, "utf8"));
  const args = expandMcpArgsForTest(payload.mcpServers.engram.args, path.resolve(__dirname, ".."));
  assert.deepEqual(args.map(path.normalize), [path.resolve(__dirname, "..", "scripts", "run-engram.js")]);
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

test("infers OMP plugin data dir from installed cache root", () => {
  const ompHome = path.join(os.tmpdir(), "omp-home");
  const pluginRoot = path.join(
    ompHome,
    "plugins",
    "cache",
    "plugins",
    "engram___engram___6.47.0"
  );

  assert.equal(
    inferCodexPluginDataDir(pluginRoot),
    path.join(ompHome, "plugins", "data", "engram-engram")
  );
});

test("OMP cache root without a plugin slot fails open", () => {
  const ompCacheRoot = path.join(os.tmpdir(), "omp-home", "plugins", "cache", "plugins");
  assert.equal(inferCodexPluginDataDir(ompCacheRoot), "");
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
    CLAUDE_PLUGIN_OPTION_SERVER_URL: "https://uppercase.example.test/mcp",
    CLAUDE_PLUGIN_OPTION_API_TOKEN: "engram_uppercase_secret",
    ENGRAM_CONFIG_FILE: "/tmp/engram-config.json",
  });

  assert.match(diagnostic, /ENGRAM_URL=present\(len=25\)/);
  assert.match(diagnostic, /ENGRAM_TOKEN=redacted\(len=27\)/);
  assert.match(diagnostic, /ENGRAM_SERVER_URL=empty/);
  assert.match(diagnostic, /ENGRAM_CLAUDE_USERCONFIG_URL=placeholder/);
  assert.match(diagnostic, /CLAUDE_PLUGIN_OPTION_api_token=redacted\(len=27\)/);
  assert.match(diagnostic, /CLAUDE_PLUGIN_OPTION_SERVER_URL=present\(len=34\)/);
  assert.match(diagnostic, /CLAUDE_PLUGIN_OPTION_API_TOKEN=redacted\(len=23\)/);
  assert.match(diagnostic, /ENGRAM_CONFIG_FILE=present\(len=23\)/);
  assert.doesNotMatch(diagnostic, /engram_uppercase_secret/);
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

test("child environment drops the server-only operator token", () => {
  assert.deepEqual(
    childEnvForEngram({
      ENGRAM_AUTH_ADMIN_TOKEN: "operator-secret",
      ENGRAM_TOKEN: "worker-keycard",
      ENGRAM_URL: "https://engram.example.test/mcp",
    }),
    {
      ENGRAM_TOKEN: "worker-keycard",
      ENGRAM_URL: "https://engram.example.test/mcp",
    }
  );
});

test("resolvePluginRoot ignores unresolved placeholder values", () => {
  const previousPluginRoot = process.env.PLUGIN_ROOT;
  const previousClaudePluginRoot = process.env.CLAUDE_PLUGIN_ROOT;

  try {
    process.env.PLUGIN_ROOT = "${PLUGIN_ROOT}";
    process.env.CLAUDE_PLUGIN_ROOT = "${CLAUDE_PLUGIN_ROOT}";
    assert.equal(resolvePluginRoot(), path.resolve(__dirname, ".."));

    delete process.env.PLUGIN_ROOT;
    process.env.CLAUDE_PLUGIN_ROOT = path.join(os.tmpdir(), "engram-plugin-root");
    assert.equal(resolvePluginRoot(), process.env.CLAUDE_PLUGIN_ROOT);
  } finally {
    restoreEnv("PLUGIN_ROOT", previousPluginRoot);
    restoreEnv("CLAUDE_PLUGIN_ROOT", previousClaudePluginRoot);
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

// ── Config file credential tests ─────────────────────────────────────────────

test("config file is read when env vars are absent", () => {
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "engram-cfg-read-"));
  const cfPath = path.join(tmpDir, "config.json");
  fs.writeFileSync(cfPath, JSON.stringify({ server_url: "http://cfg-server:37777", api_token: "engram_cftoken" }), "utf8");

  try {
    const result = readEngramConfigFile(cfPath);
    assert.ok(result !== null, "expected non-null result for valid config file");
    assert.equal(result.server_url, "http://cfg-server:37777");
    assert.equal(result.api_token, "engram_cftoken");
  } finally {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }
});

test("env var wins over config file value", () => {
  // isConfiguredValue returns true for a real env value, so the env-wins
  // branch must be taken before touching the file.
  assert.equal(isConfiguredValue("http://env-wins:37777"), true, "env value is configured");
  // A placeholder value is NOT configured — file would win for that case.
  assert.equal(isConfiguredValue("${user_config.server_url}"), false, "placeholder is not configured");
  // Empty string is NOT configured.
  assert.equal(isConfiguredValue(""), false, "empty string is not configured");
  // Absent (undefined) is NOT configured.
  assert.equal(isConfiguredValue(undefined), false, "undefined is not configured");
});

test("malformed JSON in config file returns null (silent skip)", () => {
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "engram-cfg-bad-"));
  const cfPath = path.join(tmpDir, "config.json");
  fs.writeFileSync(cfPath, "{ this is not valid json", "utf8");

  try {
    const result = readEngramConfigFile(cfPath);
    assert.strictEqual(result, null, "expected null for malformed JSON");
  } finally {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }
});

test("non-object JSON in config file returns null (array root)", () => {
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "engram-cfg-arr-"));
  const cfPath = path.join(tmpDir, "config.json");
  fs.writeFileSync(cfPath, JSON.stringify(["not", "an", "object"]), "utf8");

  try {
    const result = readEngramConfigFile(cfPath);
    assert.strictEqual(result, null, "expected null for array root");
  } finally {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }
});

test("ENGRAM_CONFIG_FILE env override controls config file path", () => {
  const previousCfgFile = process.env.ENGRAM_CONFIG_FILE;
  const previousPluginData = process.env.PLUGIN_DATA;
  const previousClaudePluginData = process.env.CLAUDE_PLUGIN_DATA;
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "engram-cfg-env-"));
  const customPath = path.join(tmpDir, "custom-config.json");

  try {
    process.env.ENGRAM_CONFIG_FILE = customPath;
    delete process.env.PLUGIN_DATA;
    delete process.env.CLAUDE_PLUGIN_DATA;

    const resolved = resolveConfigFilePath(/* pluginData= */ "");
    assert.equal(resolved, customPath, "ENGRAM_CONFIG_FILE must take priority");
  } finally {
    restoreEnv("ENGRAM_CONFIG_FILE", previousCfgFile);
    restoreEnv("PLUGIN_DATA", previousPluginData);
    restoreEnv("CLAUDE_PLUGIN_DATA", previousClaudePluginData);
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }
});

test("config file path uses pluginData/config.json when that file exists and ENGRAM_CONFIG_FILE is absent", () => {
  const previousCfgFile = process.env.ENGRAM_CONFIG_FILE;
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "engram-plugin-data-test-"));
  const cfPath = path.join(tmpDir, "config.json");
  fs.writeFileSync(cfPath, JSON.stringify({ server_url: "http://test:37777", api_token: "tok" }), "utf8");

  try {
    delete process.env.ENGRAM_CONFIG_FILE;
    const resolved = resolveConfigFilePath(tmpDir);
    assert.equal(resolved, cfPath, "must pick pluginData/config.json when the file exists");
  } finally {
    restoreEnv("ENGRAM_CONFIG_FILE", previousCfgFile);
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }
});

test("config file path falls back to ~/.engram/config.json when pluginData config is absent", () => {
  const previousCfgFile = process.env.ENGRAM_CONFIG_FILE;
  const emptyTmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "engram-plugin-data-empty-"));

  try {
    delete process.env.ENGRAM_CONFIG_FILE;
    // pluginData is set but config.json does not exist inside it — should fall to home
    const resolved = resolveConfigFilePath(emptyTmpDir);
    assert.equal(resolved, path.join(os.homedir(), ".engram", "config.json"),
      "must fall through to home dir when pluginData/config.json is absent");
  } finally {
    restoreEnv("ENGRAM_CONFIG_FILE", previousCfgFile);
    fs.rmSync(emptyTmpDir, { recursive: true, force: true });
  }
});

test("config file path falls back to ~/.engram/config.json when pluginData is absent", () => {
  const previousCfgFile = process.env.ENGRAM_CONFIG_FILE;

  try {
    delete process.env.ENGRAM_CONFIG_FILE;
    const resolved = resolveConfigFilePath("");
    assert.equal(resolved, path.join(os.homedir(), ".engram", "config.json"));
  } finally {
    restoreEnv("ENGRAM_CONFIG_FILE", previousCfgFile);
  }
});

test("startup diagnostic config_file field does not expose token contents", () => {
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "engram-cfg-diag-"));
  const cfPath = path.join(tmpDir, "config.json");
  const secretToken = "engram_supersecret_keycard_abc123def456";
  fs.writeFileSync(cfPath, JSON.stringify({ server_url: "http://diag-server:37777", api_token: secretToken }), "utf8");

  try {
    const cfData = readEngramConfigFile(cfPath);
    const cfDesc = describeConfigFile(cfPath, cfData);

    assert.match(cfDesc, /config_file=present/, "must report config_file=present when file is valid");
    assert.doesNotMatch(cfDesc, new RegExp(secretToken), "token must not appear in config_file descriptor");
    assert.doesNotMatch(cfDesc, /supersecret/, "token must not appear in config_file descriptor (substring)");
  } finally {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }
});

test("startup diagnostic config_file=missing when file does not exist", () => {
  const nonexistentPath = path.join(os.tmpdir(), "engram-cfg-no-such-file.json");
  const cfDesc = describeConfigFile(nonexistentPath, null);
  assert.match(cfDesc, /config_file=missing/, "must report config_file=missing for absent file");
  assert.match(cfDesc, new RegExp(nonexistentPath.replace(/\\/g, "\\\\")), "must include path in missing descriptor");
});

test("startup diagnostic config_file=malformed when file exists but contains invalid JSON", () => {
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "engram-cfg-malformed-diag-"));
  const cfPath = path.join(tmpDir, "config.json");
  fs.writeFileSync(cfPath, "{ invalid json", "utf8");
  try {
    const cfData = readEngramConfigFile(cfPath);
    const cfDesc = describeConfigFile(cfPath, cfData);
    assert.match(cfDesc, /config_file=malformed/, "must report config_file=malformed for invalid JSON");
    assert.match(cfDesc, new RegExp(cfPath.replace(/\\/g, "\\\\")), "must include path in malformed descriptor");
  } finally {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }
});
test("rehashes the resolved object before one spawn and reports spawn failure without fallback", async () => {
  const events = [];
  const target = { size: 1, sha256: "a".repeat(64) };
  await assert.rejects(
    resolveAndSpawn({
      pluginRoot: "root",
      pluginData: "data",
      args: ["serve"],
      env: {},
      resolve: async () => { events.push("resolve"); return { path: "trusted-object", target }; },
      roots: () => ({ objects: "objects" }),
      hash: (candidate, actual, root) => { events.push(`hash:${candidate}:${actual.sha256}:${root}`); return true; },
      spawnSync: (command) => { events.push(`spawn:${command}`); return { error: new Error("permission denied"), status: null }; },
    }),
    /engram exec failed: permission denied/
  );
  assert.deepEqual(events, ["resolve", `hash:trusted-object:${target.sha256}:objects`, "spawn:trusted-object"]);
});

test("final rehash failure prevents spawn of resolved bytes", async () => {
  let spawned = false;
  await assert.rejects(resolveAndSpawn({
    pluginRoot: "root", pluginData: "data", args: [], env: {},
    resolve: async () => ({ path: "tampered", target: { size: 1, sha256: "b".repeat(64) } }),
    roots: () => ({ objects: "objects" }), hash: () => false,
    spawnSync: () => { spawned = true; return { status: 0 }; },
  }), /final integrity verification/);
  assert.equal(spawned, false);
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
