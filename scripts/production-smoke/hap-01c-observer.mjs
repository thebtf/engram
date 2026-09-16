import fs from "node:fs";
import path from "node:path";

const URL_ALIASES = Object.freeze([
  "ENGRAM_URL",
  "ENGRAM_SERVER_URL",
  "CLAUDE_PLUGIN_OPTION_server_url",
  "CLAUDE_PLUGIN_OPTION_SERVER_URL",
  "ENGRAM_CLAUDE_USERCONFIG_URL",
]);
const TOKEN_ALIASES = Object.freeze([
  "ENGRAM_TOKEN",
  "CLAUDE_PLUGIN_OPTION_api_token",
  "CLAUDE_PLUGIN_OPTION_API_TOKEN",
  "ENGRAM_CLAUDE_USERCONFIG_TOKEN",
]);
const CONFIG_ALIASES = Object.freeze([
  "ENGRAM_CONFIG_FILE",
  "CLAUDE_PLUGIN_OPTION_config_file",
  "CLAUDE_PLUGIN_OPTION_CONFIG_FILE",
]);

function present(keys) {
  return keys.some((key) => typeof process.env[key] === "string" && process.env[key].trim() !== "");
}

function scratchCwd(ctx) {
  const expected = process.env.HAP_01C_OBSERVER_SCRATCH_CWD;
  const actual = typeof ctx?.cwd === "string" && ctx.cwd ? ctx.cwd : process.cwd();
  return typeof expected === "string" && expected !== "" && path.resolve(actual) === path.resolve(expected);
}

function record(kind, ctx) {
  const output = process.env.HAP_01C_OBSERVER_FILE;
  if (typeof output !== "string" || output === "") return;

  const observation = {
    kind,
    extension_url_present: present(URL_ALIASES),
    extension_token_present: present(TOKEN_ALIASES),
    extension_config_path_present: present(CONFIG_ALIASES),
    scratch_cwd: scratchCwd(ctx),
    timestamp: new Date().toISOString(),
  };

  try {
    fs.mkdirSync(path.dirname(output), { recursive: true, mode: 0o700 });
    fs.appendFileSync(output, `${JSON.stringify(observation)}\n`, { encoding: "utf8", mode: 0o600 });
  } catch {
    // Observability must not alter the host callback result.
  }
}

export default function hap01cObserver(pi) {
  pi.on("session_start", (_event, ctx) => record("session_start", ctx));
  pi.on("before_agent_start", (_event, ctx) => record("before_agent_start", ctx));
}

export { record };
