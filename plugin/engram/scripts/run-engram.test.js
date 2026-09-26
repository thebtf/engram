const assert = require("node:assert/strict");
const { spawnSync } = require("node:child_process");
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
  formatHap01bDiagnostic,
  formatStartupDiagnostic,
  inferCodexPluginDataDir,
  isConfiguredValue,
  isInvalidHap01bConfig,
  parseHap01bConfig,
  readEngramConfigFile,
  resolveConfigFilePath,
  resolvePluginData,
  resolvePluginRoot,
  resolveAndSpawn,
  spawnFailureMessage,
  trimStartupDiagnosticLog,
} = require("./run-engram.js");

test("missing keycard points to the real access console", () => {
  const result = spawnSync(process.execPath, [path.join(__dirname, "run-engram.js")], {
    encoding: "utf8",
    env: {
      PATH: process.env.PATH,
      SystemRoot: process.env.SystemRoot,
      ENGRAM_URL: "http://127.0.0.1:65535",
      ENGRAM_CONFIG_FILE: path.join(os.tmpdir(), "engram-nonexistent-profile-config.json"),
    },
  });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /127\.0\.0\.1:65535\/access/);
  assert.doesNotMatch(result.stderr, /\/tokens/);
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

test("OMP manifest resolves its script from the plugin root independently of the host cwd", () => {
  const repoRoot = path.resolve(__dirname, "..", "..", "..");
  const readJson = (...segments) => JSON.parse(fs.readFileSync(path.join(repoRoot, ...segments), "utf8"));
  const claudePlugin = readJson("plugin", "engram", ".claude-plugin", "plugin.json");
  const ompPlugin = readJson("plugin", "engram", ".omp-plugin", "plugin.json");
  const codexPlugin = readJson("plugin", "engram", ".codex-plugin", "plugin.json");
  const rootPlugin = readJson(".claude-plugin", "plugin.json");
  const claudeMarketplace = readJson(".claude-plugin", "marketplace.json");
  const ompMarketplace = readJson(".omp-plugin", "marketplace.json");

  assert.match(claudePlugin.version, /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/);
  assert.equal(ompPlugin.version, claudePlugin.version);
  const ompServer = ompPlugin.mcpServers.engram;
  assert.equal(Object.hasOwn(ompServer, "cwd"), false);
  assert.deepEqual(ompServer, {
    type: "stdio",
    command: "node",
    args: ["${OMP_PLUGIN_ROOT}/scripts/run-engram.js"],
    timeout: 720000,
  });
  const ompArgs = expandMcpArgsForTest(ompServer.args, path.resolve(repoRoot, "plugin", "engram"));
  assert.deepEqual(ompArgs.map(path.normalize), [path.join(repoRoot, "plugin", "engram", "scripts", "run-engram.js")]);
  assert.equal(rootPlugin.version, claudePlugin.version);
  assert.equal(codexPlugin.version, claudePlugin.version);
  assert.equal(claudeMarketplace.version, claudePlugin.version);
  assert.equal(claudeMarketplace.plugins[0].version, claudePlugin.version);
  assert.equal(ompMarketplace.version, claudePlugin.version);
  assert.equal(ompMarketplace.plugins[0].version, claudePlugin.version);
});

test("Claude MCP config preserves host argv via the package-root wrapper path", () => {
  const mcpPath = path.resolve(__dirname, "..", "claude", ".mcp.json");
  const payload = JSON.parse(fs.readFileSync(mcpPath, "utf8"));
  const args = expandMcpArgsForTest(payload.mcpServers.engram.args, path.resolve(__dirname, ".."));
  assert.deepEqual(args.map(path.normalize), [path.resolve(__dirname, "..", "scripts", "run-engram.js")]);
});

test("OMP marketplace wrapper fails closed from project cwd with simulated host package-root expansion", () => {
  const pluginRoot = path.resolve(__dirname, "..");
  const project = fs.mkdtempSync(path.join(os.tmpdir(), "engram-omp-project-"));
  try {
    const config = JSON.parse(fs.readFileSync(path.join(pluginRoot, ".omp-plugin", "plugin.json"), "utf8")).mcpServers.engram;
    const args = config.args.map((arg) => arg.replace("${OMP_PLUGIN_ROOT}", pluginRoot.replaceAll("\\", "/")));
    const result = spawnSync(config.command, args, {
      cwd: project,
      encoding: "utf8",
      env: { PATH: process.env.PATH, SystemRoot: process.env.SystemRoot, PLUGIN_DATA: project, ENGRAM_CONFIG_FILE: path.join(project, "absent.json") },
    });
    assert.equal(result.status, 1, result.error?.message || result.stderr);
    assert.match(result.stderr, /FATAL: ENGRAM_URL is empty/);
    assert.match(result.stderr, /Config file checked: .*absent\.json/);
  } finally {
    fs.rmSync(project, { recursive: true, force: true });
  }
});

test("Codex manifest invokes its package wrapper from the plugin directory", () => {
  const pluginRoot = path.resolve(__dirname, "..");
  const server = JSON.parse(fs.readFileSync(path.join(pluginRoot, ".codex-plugin", "plugin.json"), "utf8"));
  const config = JSON.parse(fs.readFileSync(path.join(pluginRoot, server.mcpServers), "utf8")).mcpServers.engram;
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), "engram-codex-launch-"));
  try {
    const result = spawnSync(config.command, config.args, {
      cwd: path.resolve(pluginRoot, config.cwd),
      encoding: "utf8",
      env: { PATH: process.env.PATH, SystemRoot: process.env.SystemRoot, PLUGIN_DATA: dataDir, ENGRAM_CONFIG_FILE: path.join(dataDir, "absent.json") },
    });
    assert.equal(result.status, 1, result.error?.message || result.stderr);
    assert.match(result.stderr, /FATAL: ENGRAM_URL is empty/);
    assert.match(result.stderr, /Config file checked: .*absent\.json/);
  } finally {
    fs.rmSync(dataDir, { recursive: true, force: true });
  }
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

test("HAP-01B config is normalized, immutable, and applied only to the child", () => {
  const raw = validHap01bConfig({
    project_tokens: {
      "00000000-0000-0000-0000-000000000002": `engram_${"2".repeat(32)}`,
      "00000000-0000-0000-0000-000000000001": `engram_${"1".repeat(32)}`,
    },
  });
  const sourceProjectTokens = { ...raw.project_tokens };
  const normalized = parseHap01bConfig(raw);

  assert.ok(normalized);
  assert.deepEqual(Object.keys(normalized.project_tokens), [
    "00000000-0000-0000-0000-000000000001",
    "00000000-0000-0000-0000-000000000002",
  ]);
  assert.ok(Object.isFrozen(normalized));
  assert.ok(Object.isFrozen(normalized.project_tokens));
  assert.deepEqual(raw.project_tokens, sourceProjectTokens, "normalization must not mutate caller input");

  const inherited = {
    ENGRAM_TOKEN: "base-keycard",
    ENGRAM_URL: "https://engram.example.test/mcp",
    ENGRAM_AUTH_ADMIN_TOKEN: "operator-secret",
    ENGRAM_HAP_01B_RELAY_ENABLED: "false",
    ENGRAM_HAP_01B_RELAY_REVISION: "stale",
    ENGRAM_HAP_01B_LEGACY_DIRECT_ENFORCEMENT: "true",
    ENGRAM_HAP_01B_ADAPTER_SHA256: "stale",
    ENGRAM_HAP_01B_REGISTRATION_TOKEN: "stale",
    engram_auth_admin_token: "lowercase-operator-secret",
    Engram_Hap_01B_Relay_Enabled: "mixed-case-stale",
    ENGRAM_HAP_01B_UNRECOGNIZED: "unknown-stale",
    ENGRAM_HAP_01B_PROJECT_TOKENS_JSON: "{\"stale\":true}",
  };
  const child = childEnvForEngram(inherited, normalized);

  assert.equal(inherited.ENGRAM_HAP_01B_RELAY_ENABLED, "false", "helper must not mutate source env");
  assert.equal(child.ENGRAM_TOKEN, "base-keycard");
  assert.equal(child.ENGRAM_URL, "https://engram.example.test/mcp");
  assert.equal(Object.keys(child).some((key) => key.toUpperCase() === "ENGRAM_AUTH_ADMIN_TOKEN"), false);
  assert.equal(Object.keys(child).some((key) => key.toUpperCase().startsWith("ENGRAM_HAP_01B_") && !key.startsWith("ENGRAM_HAP_01B_")), false);
  assert.equal(child.ENGRAM_AUTH_ADMIN_TOKEN, undefined);
  assert.deepEqual(
    Object.fromEntries(Object.entries(child).filter(([key]) => key.startsWith("ENGRAM_HAP_01B_"))),
    {
      ENGRAM_HAP_01B_RELAY_ENABLED: "true",
      ENGRAM_HAP_01B_RELAY_REVISION: "omp-hap-01b/1",
      ENGRAM_HAP_01B_LEGACY_DIRECT_ENFORCEMENT: "false",
      ENGRAM_HAP_01B_ADAPTER_SHA256: "a".repeat(64),
      ENGRAM_HAP_01B_REGISTRATION_TOKEN: `engram_${"b".repeat(32)}`,
      ENGRAM_HAP_01B_PROJECT_TOKENS_JSON: JSON.stringify({
        "00000000-0000-0000-0000-000000000001": `engram_${"1".repeat(32)}`,
        "00000000-0000-0000-0000-000000000002": `engram_${"2".repeat(32)}`,
      }),
    }
  );
});

test("HAP-01B inherited credentials are stripped when config is absent", () => {
  const child = childEnvForEngram({
    ENGRAM_TOKEN: "base-keycard",
    ENGRAM_HAP_01B_RELAY_ENABLED: "true",
    ENGRAM_HAP_01B_RELAY_REVISION: "omp-hap-01b/1",
    ENGRAM_HAP_01B_LEGACY_DIRECT_ENFORCEMENT: "true",
    ENGRAM_HAP_01B_ADAPTER_SHA256: "a".repeat(64),
    ENGRAM_HAP_01B_REGISTRATION_TOKEN: `engram_${"b".repeat(32)}`,
    engram_auth_admin_token: "lowercase-operator-secret",
    Engram_Hap_01B_Relay_Revision: "mixed-case-stale",
    ENGRAM_HAP_01B_UNRECOGNIZED: "unknown-stale",
    ENGRAM_HAP_01B_PROJECT_TOKENS_JSON: "{}",
  });

  assert.deepEqual(child, { ENGRAM_TOKEN: "base-keycard" });
});

test("HAP-01B parser rejects every malformed nested block", () => {
  const cases = [
    ["unknown field", { extra: true }],
    ["missing field", { relay_enabled: undefined }],
    ["bad revision", { relay_revision: "omp-hap-01b/2" }],
    ["bad relay enabled", { relay_enabled: false }],
    ["bad enforcement type", { legacy_direct_enforcement: "false" }],
    ["bad SHA", { adapter_sha256: "A".repeat(64) }],
    ["bad registration token", { registration_token: `engram_${"g".repeat(32)}` }],
    ["noncanonical project key", { project_tokens: { "00000000-0000-0000-0000-00000000000A": `engram_${"1".repeat(32)}` } }],
    ["bad project token", { project_tokens: { "00000000-0000-0000-0000-000000000001": "not-a-keycard" } }],
    ["empty project map", { project_tokens: {} }],
    ["non-object project map", { project_tokens: [] }],
    ["oversize project map", { project_tokens: oversizedProjectTokens() }],
  ];

  for (const [name, overrides] of cases) {
    assert.equal(parseHap01bConfig(validHap01bConfig(overrides)), null, name);
  }
  const missingField = validHap01bConfig();
  delete missingField.relay_enabled;
  assert.equal(parseHap01bConfig(missingField), null, "missing field");
  const uppercaseHex = parseHap01bConfig(validHap01bConfig({
    registration_token: `engram_${"A".repeat(32)}`,
    project_tokens: { "00000000-0000-0000-0000-000000000001": `engram_${"B".repeat(32)}` },
  }));
  assert.ok(uppercaseHex, "keycard hex acceptance must mirror Go hex.DecodeString");
});


test("HAP-01B config file distinguishes absent, valid, and invalid blocks without diagnostic leaks", () => {
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "engram-hap-01b-"));
  const cfPath = path.join(tmpDir, "config.json");
  const registrationToken = `engram_${"b".repeat(32)}`;
  const projectKey = "00000000-0000-0000-0000-000000000001";
  const projectToken = `engram_${"1".repeat(32)}`;
  try {
    fs.writeFileSync(cfPath, JSON.stringify({ server_url: "http://cfg.test", api_token: "base-keycard" }), "utf8");
    const absent = readEngramConfigFile(cfPath);
    assert.equal(absent.hap_01b, undefined);
    assert.equal(isInvalidHap01bConfig(absent), false);
    assert.equal(formatHap01bDiagnostic(absent), "hap_01b=absent");

    fs.writeFileSync(cfPath, JSON.stringify({
      server_url: "http://cfg.test",
      api_token: "base-keycard",
      hap_01b: validHap01bConfig(),
    }), "utf8");
    const valid = readEngramConfigFile(cfPath);
    assert.equal(valid.server_url, "http://cfg.test");
    assert.equal(valid.api_token, "base-keycard");
    assert.ok(valid.hap_01b);
    assert.equal(isInvalidHap01bConfig(valid), false);

    const diagnostic = formatStartupDiagnostic({}, cfPath, valid);
    assert.match(diagnostic, /hap_01b=present\(relay_enabled=true,relay_revision_len=13,legacy_direct_enforcement=false,adapter_sha256_len=64,registration_token_len=39,project_token_count=1\)/);
    assert.doesNotMatch(diagnostic, new RegExp(registrationToken));
    assert.doesNotMatch(diagnostic, new RegExp(projectToken));
    assert.doesNotMatch(diagnostic, new RegExp(projectKey));

    fs.writeFileSync(cfPath, JSON.stringify({
      server_url: "http://cfg.test",
      api_token: "base-keycard",
      hap_01b: { relay_enabled: true },
    }), "utf8");
    const invalid = readEngramConfigFile(cfPath);
    assert.equal(invalid.server_url, "http://cfg.test");
    assert.equal(invalid.api_token, "base-keycard");
    assert.equal(invalid.hap_01b, undefined);
    assert.equal(isInvalidHap01bConfig(invalid), true);
    assert.equal(formatHap01bDiagnostic(invalid), "hap_01b=invalid");
  } finally {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }
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
      env: { ENGRAM_CLIENT_INSTANCE_ID: "fixture-client" },
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

test("Go-only launch retains an installation-scoped identity and does not claim a parser", async () => {
  const pluginData = fs.mkdtempSync(path.join(os.tmpdir(), "engram-go-only-start-"));
  try {
    const environments = [];
    for (let attempt = 0; attempt < 2; attempt++) {
      assert.equal(await resolveAndSpawn({
        pluginRoot: "root", pluginData, args: [],
        env: { ENGRAM_UCI_PARSER_EXECUTABLE: "foreign", ENGRAM_UCI_PARSER_BUNDLE_DIGEST: "foreign" },
        resolve: async () => ({ path: "client", target: { sha256: "b".repeat(64) }, parserTarget: null }),
        roots: () => ({ objects: "objects" }), hash: () => true,
        spawnSync: (_, __, options) => { environments.push(options.env); return { status: 0 }; },
      }), 0);
    }
    assert.match(environments[0].ENGRAM_CLIENT_INSTANCE_ID, /^engram-[0-9a-f]{32}$/);
    assert.equal(environments[1].ENGRAM_CLIENT_INSTANCE_ID, environments[0].ENGRAM_CLIENT_INSTANCE_ID);
    assert.equal(environments[0].ENGRAM_UCI_PARSER_EXECUTABLE, undefined);
    assert.equal(environments[0].ENGRAM_UCI_PARSER_BUNDLE_DIGEST, undefined);
  } finally {
    fs.rmSync(pluginData, { recursive: true, force: true });
  }
});

test("hooks and daemon use the same persisted identity without manual configuration", () => {
  const pluginData = fs.mkdtempSync(path.join(os.tmpdir(), "engram-hook-install-"));
  const keys = ["PLUGIN_DATA", "ENGRAM_URL", "ENGRAM_TOKEN", "ENGRAM_CLIENT_INSTANCE_ID", "ENGRAM_CONFIG_FILE"];
  const previous = Object.fromEntries(keys.map((key) => [key, process.env[key]]));
  try {
    process.env.PLUGIN_DATA = pluginData;
    process.env.ENGRAM_URL = "http://127.0.0.1:1";
    process.env.ENGRAM_TOKEN = "engram_test";
    delete process.env.ENGRAM_CLIENT_INSTANCE_ID;
    process.env.ENGRAM_CONFIG_FILE = path.join(pluginData, "missing-config.json");
    const fromHook = require("../hooks/lib.js").getEngramConfig().clientInstanceID;
    assert.match(fromHook, /^engram-[0-9a-f]{32}$/);
    assert.equal(fs.readFileSync(path.join(pluginData, "client-instance-id"), "utf8"), `${fromHook}\n`);
    assert.equal(require("./client-instance.js").installationClientInstanceID(pluginData), fromHook);
    fs.writeFileSync(path.join(pluginData, "client-instance-id"), "bad\n");
    assert.throws(() => require("./client-instance.js").installationClientInstanceID(pluginData), /identity is invalid/);
  } finally {
    for (const key of keys) restoreEnv(key, previous[key]);
    fs.rmSync(pluginData, { recursive: true, force: true });
  }
});

test("verified parser identity reaches daemon without inherited parser overrides", async () => {
  const digest = `sha256:${"a".repeat(64)}`;
  const calls = [];
  const status = await resolveAndSpawn({
    pluginRoot: "root", pluginData: "data", args: ["serve"],
    env: { SYSTEMROOT: "C:\\Windows", SECRET: "not-for-parser", ENGRAM_CLIENT_INSTANCE_ID: "fixture-client", ENGRAM_UCI_PARSER_EXECUTABLE: "foreign" },
    resolve: async () => ({ path: "client", target: { sha256: "b".repeat(64) }, parserPath: "parser", parserTarget: { sha256: "c".repeat(64) } }),
    roots: () => ({ objects: "objects" }), hash: () => true,
    spawnSync: (file, args, options) => {
      calls.push({ file, args, options });
      return file === "parser" ? { status: 0, stdout: `${digest}\n` } : { status: 0 };
    },
  });
  assert.equal(status, 0);
  assert.deepEqual(calls[0].options.env, { SYSTEMROOT: "C:\\Windows" });
  assert.equal(calls[1].options.env.ENGRAM_UCI_PARSER_BUNDLE_DIGEST, digest);
  assert.equal(calls[1].options.env.ENGRAM_UCI_PARSER_EXECUTABLE, "parser");
});

function restoreEnv(key, value) {
  if (value === undefined) {
    delete process.env[key];
    return;
  }
  process.env[key] = value;
}

function validHap01bConfig(overrides = {}) {
  return {
    relay_enabled: true,
    relay_revision: "omp-hap-01b/1",
    legacy_direct_enforcement: false,
    adapter_sha256: "a".repeat(64),
    registration_token: `engram_${"b".repeat(32)}`,
    project_tokens: { "00000000-0000-0000-0000-000000000001": `engram_${"1".repeat(32)}` },
    ...overrides,
  };
}

function oversizedProjectTokens() {
  return Object.fromEntries(
    Array.from({ length: 257 }, (_, index) => [
      `00000000-0000-0000-0000-${index.toString(16).padStart(12, "0")}`,
      `engram_${(index % 16).toString(16).repeat(32)}`,
    ])
  );
}

function expandMcpArgsForTest(args, pluginRoot) {
  return args.map((arg) => arg.replaceAll("${CLAUDE_PLUGIN_ROOT}", pluginRoot.replaceAll("\\", "/")).replaceAll("${OMP_PLUGIN_ROOT}", pluginRoot.replaceAll("\\", "/")));
}

test("HAP-01 source diagnostic keeps launcher credential resolution out of installed proof", () => {
  const source = fs.readFileSync(path.join(__dirname, "run-engram.js"), "utf8");
  assert.match(source, /"ENGRAM_TOKEN"/);
  assert.match(source, /configFile\.api_token/);
  assert.match(source, /process\.env\.ENGRAM_TOKEN = token/);
  assert.match(source, /childEnvForEngram\(process\.env, configFile\?\.hap_01b\)/);
  assert.match(source, /ENGRAM_HAP_01B_REGISTRATION_TOKEN/);
});
