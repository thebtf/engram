const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");

const {
  inferCodexPluginDataDir,
  resolvePluginData,
} = require("./run-engram.js");

test("Codex MCP config launches the wrapper from the plugin root", () => {
  const mcpPath = path.resolve(__dirname, "..", ".mcp.json");
  const payload = JSON.parse(fs.readFileSync(mcpPath, "utf8"));
  const server = payload.mcpServers.engram;

  assert.equal(server.command, "node");
  assert.deepEqual(server.args, ["./scripts/run-engram.js"]);
  assert.equal(server.cwd, ".");
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

function restoreEnv(key, value) {
  if (value === undefined) {
    delete process.env[key];
    return;
  }
  process.env[key] = value;
}
