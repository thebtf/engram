#!/usr/bin/env node
const { randomUUID } = require("node:crypto");
const { spawn, spawnSync } = require("node:child_process");
const fs = require("node:fs");
const http = require("node:http");
const path = require("node:path");

const wrapper = require("../../plugin/engram/scripts/run-engram.js");
const hookLib = require("../../plugin/engram/hooks/lib.js");

const ExitCode = Object.freeze({ CONFIG: 10, READY: 11, MCP_INIT: 12, HEALTH: 13, STORE: 14, RECALL: 15, SESSION_SEED: 16, SESSION_INIT: 17, OUTCOME: 18, OUTCOME_READBACK: 19, STATS_CORROBORATION: 20, FAIL_OPEN: 21 });
const validRunID = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const validUUIDv7 = /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const repoRoot = path.resolve(__dirname, "../..");
const runnerTmpRoot = path.join(repoRoot, ".agent", "tmp");
const lifecycleTraceRoot = path.join(repoRoot, ".agent", "runs", "agent-memory-dogfooding");
const lifecycleChildBudgetMs = 85_000;
const lifecycleOmpBudgetMs = 75_000;
const lifecycleOutputCapBytes = 4_096;
const lifecycleRequestBodyCapBytes = 64 * 1024;
const sessionSeedPrompt = "Reply exactly R6_SESSION_SEED";
const ompProfile = "nvmd-selfhost";
const sessionSeedBudgetMs = 95_000;
const failOpenBudgetMs = 65_000;
const unavailableEngramURL = "http://127.0.0.1:9";
const safeProject = /^[A-Za-z0-9_.:/-]{1,128}$/;

class PhaseError extends Error {
 constructor(phase) {
  super(phase);
  this.phase = phase;
 }
}

function parseArgs(args) {
 let phase = "full";
 let runID = "";
 for (let index = 0; index < args.length; index += 1) {
  const arg = args[index];
  if (arg === "--phase" || arg === "--run-id") {
   const value = args[++index];
   if (value === undefined || value.startsWith("--")) throw new PhaseError("CONFIG");
   if (arg === "--phase") phase = value;
   else runID = value;
  } else {
   throw new PhaseError("CONFIG");
  }
 }
 if (!new Set(["write", "read", "full", "r6", "lifecycle-trace"]).has(phase) || (runID && !validRunID.test(runID))) {
  throw new PhaseError("CONFIG");
 }
 return { phase, runID: runID || randomUUID() };
}

function activeConfig() {
 const pluginRoot = wrapper.resolvePluginRoot();
 const configFile = wrapper.readEngramConfigFile(
  wrapper.resolveConfigFilePath(wrapper.resolvePluginData(pluginRoot))
 );
 const serverURL = wrapper.configuredEnvValue(
  "ENGRAM_URL", "ENGRAM_SERVER_URL", "CLAUDE_PLUGIN_OPTION_server_url",
  "CLAUDE_PLUGIN_OPTION_SERVER_URL", "ENGRAM_CLAUDE_USERCONFIG_URL"
 ) || (configFile && wrapper.isConfiguredValue(configFile.server_url) ? configFile.server_url : "");
 const token = wrapper.configuredEnvValue(
  "ENGRAM_TOKEN", "CLAUDE_PLUGIN_OPTION_api_token", "CLAUDE_PLUGIN_OPTION_API_TOKEN",
  "ENGRAM_CLAUDE_USERCONFIG_TOKEN"
 ) || (configFile && wrapper.isConfiguredValue(configFile.api_token) ? configFile.api_token : "");
 let endpoint;
 try {
  endpoint = new URL(serverURL);
 } catch {
  throw new PhaseError("CONFIG");
 }
 if (!token || !["http:", "https:"].includes(endpoint.protocol) || !endpoint.host || endpoint.username || endpoint.password || endpoint.search || endpoint.hash) {
  throw new PhaseError("CONFIG");
 }
 return { endpoint, endpointHost: endpoint.host, token };
}

async function checkReady(endpoint) {
 const readyURL = new URL("/api/ready", endpoint);
 let response;
 try {
  response = await fetch(readyURL, { headers: { accept: "application/json" }, redirect: "error", signal: AbortSignal.timeout(5_000) });
  const body = await response.json();
  if (response.status !== 200 || JSON.stringify(body) !== '{"status":"ready"}') throw new Error("not ready");
 } catch {
  throw new PhaseError("READY");
 }
}

class McpClient {
 constructor(spawnChild = spawn) {
  this.nextID = 1;
  this.pending = new Map();
  this.buffer = "";
  this.failed = null;
  const runner = path.resolve(__dirname, "../../plugin/engram/scripts/run-engram.js");
  this.child = spawnChild(process.execPath, [runner], { cwd: path.resolve(__dirname, "../.."), stdio: ["pipe", "pipe", "pipe"] });
  this.child.stdout.setEncoding("utf8");
  this.child.stdout.on("data", (chunk) => this.consume(chunk));
  this.child.stderr.resume();
  this.child.on("error", () => this.fail());
  this.child.on("close", () => this.fail());
 }

 consume(chunk) {
  this.buffer += chunk;
  let newline;
  while ((newline = this.buffer.indexOf("\n")) >= 0) {
   const line = this.buffer.slice(0, newline);
   this.buffer = this.buffer.slice(newline + 1);
   if (!line) continue;
   let response;
   try {
    response = JSON.parse(line);
   } catch {
    this.fail();
    return;
   }
   const pending = this.pending.get(response.id);
   if (!pending) continue;
   this.pending.delete(response.id);
   if (response.error || response.result?.isError) pending.reject(new Error("MCP request failed"));
   else pending.resolve(response.result);
  }
 }

 fail() {
  if (this.failed) return;
  this.failed = new Error("MCP process failed");
  for (const pending of this.pending.values()) pending.reject(this.failed);
  this.pending.clear();
 }

 request(method, params) {
  if (this.failed || !this.child.stdin.writable) return Promise.reject(this.failed || new Error("MCP process unavailable"));
  const id = this.nextID++;
  const message = JSON.stringify({ jsonrpc: "2.0", id, method, ...(params === undefined ? {} : { params }) });
  return new Promise((resolve, reject) => {
   const timeout = setTimeout(() => {
    this.pending.delete(id);
    reject(new Error("MCP request timed out"));
   }, 15_000);
   this.pending.set(id, {
    resolve: (value) => { clearTimeout(timeout); resolve(value); },
    reject: (error) => { clearTimeout(timeout); reject(error); },
   });
   this.child.stdin.write(`${message}\n`, (error) => {
    if (!error) return;
    const pending = this.pending.get(id);
    if (pending) {
     this.pending.delete(id);
     pending.reject(error);
    }
   });
  });
 }

 async initialize() {
  await this.request("initialize", {
   protocolVersion: "2024-11-05",
   capabilities: {},
   clientInfo: { name: "agent-memory-dogfood", version: "1" },
  });
  this.child.stdin.write(`${JSON.stringify({ jsonrpc: "2.0", method: "notifications/initialized" })}\n`);
 }

 close() {
  this.child.stdin.end();
 }
}

function contentText(result) {
 let text = result?.content?.find((item) => item && item.type === "text")?.text;
 if (typeof text !== "string") throw new Error("missing MCP content");
 let value = JSON.parse(text);
 if (Array.isArray(value?.content)) {
  text = value.content.find((item) => item && item.type === "text")?.text;
  if (typeof text !== "string") throw new Error("missing nested MCP content");
  value = JSON.parse(text);
 }
 return value;
}

async function timed(receipt, key, operation) {
 const started = performance.now();
 try {
  return await operation();
 } finally {
  receipt.durations_ms[key] = Math.round(performance.now() - started);
 }
}

async function mcpRun(kind, runID, receipt) {
 const client = new McpClient();
 try {
  await timed(receipt, "mcp_initialize", async () => {
   try { await client.initialize(); } catch { throw new PhaseError("MCP_INIT"); }
  });
  await timed(receipt, "health", async () => {
   try {
    const report = contentText(await client.request("tools/call", { name: "check_system_health", arguments: {} }));
    if (report.overall_status !== "healthy") throw new Error("unhealthy");
   } catch { throw new PhaseError("HEALTH"); }
  });
  const marker = `agent-memory-dogfood:${runID}`;
  if (kind === "write") {
   await timed(receipt, "store", async () => {
    try {
     const stored = contentText(await client.request("tools/call", {
      name: "store", arguments: {
       action: "create", content: marker, title: "Agent Memory Dogfooding", type: "operational", tags: ["agent-memory-dogfood"],
      }
     }));
     if (!Number.isInteger(stored.id) || stored.id <= 0) throw new Error("missing marker id");
     receipt.marker_id = stored.id;
     receipt.marker_content = marker;
    } catch { throw new PhaseError("STORE"); }
   });
  } else {
   await timed(receipt, "recall", async () => {
    try {
     const recalled = contentText(await client.request("tools/call", { name: "recall", arguments: { action: "search", project: "current", query: marker, limit: 10 } }));
     const recalledMarker = recalled.memories?.find((memory) => memory?.content === marker);
     if (!Number.isInteger(recalled.count) || !recalledMarker || !Number.isInteger(recalledMarker.id) || (receipt.marker_id !== undefined && recalledMarker.id !== receipt.marker_id)) {
      throw new Error("marker absent");
     }
     receipt.marker_count = recalled.count;
     receipt.recalled_marker_id = recalledMarker.id;
     receipt.recalled_marker_content = recalledMarker.content;
    } catch { throw new PhaseError("RECALL"); }
   });
  }
 } finally {
  client.close();
 }
}

function validSessionID(value) {
 return typeof value === "string" && !/^\d+$/.test(value) && validUUIDv7.test(value);
}

function r6Reason(runID) {
 return `R6 ${runID.slice(0, 96)} runner operational-correlation event completed`;
}

function sessionFiles(directory) {
 const files = [];
 for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
  const entryPath = path.join(directory, entry.name);
  if (entry.isDirectory()) files.push(...sessionFiles(entryPath));
  else if (entry.isFile() && entry.name.endsWith(".jsonl")) files.push(entryPath);
 }
 return files;
}

function sessionIDFromDirectory(directory) {
 const files = sessionFiles(directory);
 if (files.length !== 1) throw new Error("expected exactly one OMP session file");
 const header = fs.readFileSync(files[0], "utf8").split(/\r?\n/, 1)[0];
 let parsed;
 try {
  parsed = JSON.parse(header);
 } catch {
  throw new Error("invalid OMP session header");
 }
 if (!validSessionID(parsed?.id)) throw new Error("invalid OMP session id");
 return parsed.id;
}

function runnerSessionDirectory(runID, leg) {
 fs.mkdirSync(runnerTmpRoot, { recursive: true });
 const directory = path.join(runnerTmpRoot, `agent-memory-dogfood-r6-${runID}-${leg}-${randomUUID()}`);
 fs.mkdirSync(directory);
 return directory;
}

function removeRunnerSessionDirectory(directory) {
 const relative = path.relative(runnerTmpRoot, directory);
 if (!relative || relative.startsWith("..") || path.isAbsolute(relative) || !path.basename(directory).startsWith("agent-memory-dogfood-r6-")) {
  throw new Error("refusing to remove non-runner session directory");
 }
 fs.rmSync(directory, { recursive: true, force: true });
}

function runChild(command, args, options, spawnChild = spawn, terminate = terminateChildProcessTree, schedule = setTimeout, cancelSchedule = clearTimeout) {
 return new Promise((resolve, reject) => {
  let stdout = "";
  let settled = false;
  let timedOut = false;
  let hardTimeout;
  const child = spawnChild(command, args, { cwd: options.cwd, env: options.env, stdio: ["ignore", "pipe", "pipe"], detached: process.platform !== "win32", windowsHide: true });
  const finish = (callback, value) => {
   if (settled) return;
   settled = true;
   cancelSchedule(timer);
   cancelSchedule(hardTimeout);
   callback(value);
  };
  const timer = schedule(() => {
   if (settled) return;
   timedOut = true;
   try { terminate(child); } catch { }
   hardTimeout = schedule(() => finish(reject, new Error("child process exceeded budget")), Math.min(Math.max(options.timeoutMs, 100), 5_000));
   hardTimeout.unref?.();
  }, options.timeoutMs);
  timer.unref?.();
  child.stdout.setEncoding("utf8");
  child.stdout.on("data", (chunk) => { if (stdout.length < 128 * 1024) stdout += chunk.slice(0, 128 * 1024 - stdout.length); });
  child.stderr.on("data", () => { });
  child.on("error", () => finish(reject, timedOut ? new Error("child process exceeded budget") : new Error("child process failed")));
  child.on("close", (code) => timedOut ? finish(reject, new Error("child process exceeded budget")) : finish(resolve, { code, stdout }));
 });
}

function appendLifecycleTrace(tracePath, event, detail = {}) {
 const descriptor = fs.openSync(tracePath, "a");
 try {
  fs.writeSync(descriptor, `${JSON.stringify({ event, ...detail })}\n`);
  fs.fsyncSync(descriptor);
 } finally {
  fs.closeSync(descriptor);
 }
}

function createLifecycleTrace(runID) {
 fs.mkdirSync(lifecycleTraceRoot, { recursive: true });
 const tracePath = path.join(lifecycleTraceRoot, `r6-lifecycle-trace-${runID}-${randomUUID()}.jsonl`);
 fs.closeSync(fs.openSync(tracePath, "wx"));
 appendLifecycleTrace(tracePath, "trace-header", {
  schema: "r6-lifecycle-trace/1",
  run_id: runID,
  scope: "non-mutating loopback lifecycle diagnostic; not an R6 acceptance run",
 });
 return tracePath;
}

function lifecycleChildEnv(loopbackURL, token) {
 const env = { ...process.env };
 let cleared = 0;
 for (const key of Object.keys(env)) {
  const upper = key.toUpperCase();
  if (upper.startsWith("ENGRAM_") || upper.startsWith("CLAUDE_PLUGIN_OPTION_")) {
   delete env[key];
   cleared += 1;
  }
 }
 return {
  env: { ...env, ENGRAM_URL: loopbackURL, ENGRAM_TOKEN: token, ENGRAM_QUIET: "0" },
  cleared,
 };
}

function safeRequestShape(request, body, bodyTruncated) {
 const requestURL = new URL(request.url || "/", "http://127.0.0.1");
 let payload = null;
 try {
  payload = bodyTruncated ? null : JSON.parse(body);
 } catch { }
 const fields = {};
 if (payload && typeof payload === "object" && !Array.isArray(payload)) {
  for (const [key, value] of Object.entries(payload).slice(0, 16)) {
   fields[key] = typeof value === "string" ? value.length : Array.isArray(value) ? value.length : value && typeof value === "object" ? Object.keys(value).length : null;
  }
 }
 const contentLength = Number(request.headers["content-length"]);
 return {
  method: request.method || "",
  path: requestURL.pathname,
  content_length: Number.isSafeInteger(contentLength) && contentLength >= 0 ? contentLength : null,
  authorization_present: typeof request.headers.authorization === "string" && request.headers.authorization.length > 0,
  body_keys: Object.keys(fields),
  field_lengths: fields,
  body_truncated: bodyTruncated,
 };
}

function lifecycleFixtureResponse(pathname, sentinel) {
 if (pathname === "/api/context/inject") return { status: 200, body: { canonical_project: "r6_fixture_project" } };
 if (pathname === "/api/context/session-start") return { status: 200, body: { issues: [], rules: [], memories: [] } };
 if (pathname === "/api/hooks/ambient-candidates") return { status: 200, body: { hints: [{ title: sentinel, reason: "loopback fixture", score: 1 }] } };
 return { status: 404, body: {} };
}

function createLifecycleFixture(tracePath, sentinel) {
 const requests = [];
 const server = http.createServer((request, response) => {
  const chunks = [];
  let received = 0;
  request.on("data", (chunk) => {
   received += chunk.length;
   if (received - chunk.length < lifecycleRequestBodyCapBytes) chunks.push(chunk.subarray(0, lifecycleRequestBodyCapBytes - (received - chunk.length)));
  });
  request.once("end", () => {
   const shape = safeRequestShape(request, Buffer.concat(chunks).toString("utf8"), received > lifecycleRequestBodyCapBytes);
   requests.push(shape);
   const fixtureResponse = lifecycleFixtureResponse(shape.path, sentinel);
   appendLifecycleTrace(tracePath, "loopback-request", { ...shape, response_status: fixtureResponse.status });
   response.writeHead(fixtureResponse.status, { "content-type": "application/json" });
   response.end(JSON.stringify(fixtureResponse.body));
  });
  request.once("error", (error) => {
   appendLifecycleTrace(tracePath, "loopback-request-error", { error_name: error?.name || "Error" });
   response.destroy();
  });
 });
 return { requests, server };
}

function listenLifecycleFixture(server, tracePath) {
 appendLifecycleTrace(tracePath, "fixture-listening-await", { bind: "127.0.0.1", port: "ephemeral" });
 return new Promise((resolve, reject) => {
  const fail = (error) => {
   appendLifecycleTrace(tracePath, "fixture-listening-error", { error_name: error?.name || "Error" });
   reject(error);
  };
  server.once("error", fail);
  try {
   server.listen(0, "127.0.0.1", () => {
    server.off("error", fail);
    appendLifecycleTrace(tracePath, "fixture-listening", { bind: "127.0.0.1", port: "ephemeral", loopback_only: true });
    resolve(server.address());
   });
  } catch (error) {
   server.off("error", fail);
   appendLifecycleTrace(tracePath, "fixture-listening-error", { error_name: error?.name || "Error" });
   reject(error);
  }
 });
}

function closeLifecycleFixture(server, tracePath) {
 appendLifecycleTrace(tracePath, "fixture-close-await");
 return new Promise((resolve) => {
  server.close((error) => {
   appendLifecycleTrace(tracePath, "fixture-closed", { error: Boolean(error) });
   resolve();
  });
 });
}

function terminateChildProcessTree(child) {
 if (!Number.isInteger(child?.pid) || child.pid <= 0) return Boolean(child?.kill?.("SIGKILL"));
 if (process.platform === "win32") {
  const result = spawnSync("taskkill", ["/pid", String(child.pid), "/t", "/f"], { stdio: "ignore", windowsHide: true });
  return result.status === 0;
 }
 try {
  process.kill(-child.pid, "SIGKILL");
  return true;
 } catch {
  return Boolean(child.kill("SIGKILL"));
 }
}

function runLifecycleChild(spec, tracePath, sentinel, spawnChild = spawn, terminate = terminateChildProcessTree) {
 return new Promise((resolve) => {
  let child;
  let settled = false;
  let timeoutHandle;
  let killGraceHandle;
  let timedOut = false;
  let stdout = "";
  let stderr = "";
  const finish = (result) => {
   if (settled) return;
   settled = true;
   clearTimeout(timeoutHandle);
   clearTimeout(killGraceHandle);
   resolve({ ...result, stdout_sentinel_match: stdout.includes(sentinel), stdout_captured_bytes: Buffer.byteLength(stdout), stderr_captured_bytes: Buffer.byteLength(stderr) });
  };
  const capture = (stream, chunk) => {
   const text = Buffer.isBuffer(chunk) ? chunk.toString("utf8") : String(chunk);
   const current = stream === "stdout" ? stdout : stderr;
   const available = Math.max(0, lifecycleOutputCapBytes - Buffer.byteLength(current));
   const captured = available ? text.slice(0, available) : "";
   if (stream === "stdout") stdout += captured;
   else stderr += captured;
   appendLifecycleTrace(tracePath, `child-${stream}`, {
    received_bytes: Buffer.byteLength(text),
    captured_bytes: Buffer.byteLength(captured),
    cap_bytes: lifecycleOutputCapBytes,
    truncated: captured.length < text.length,
    sentinel_match: stream === "stdout" && stdout.includes(sentinel),
   });
  };
  appendLifecycleTrace(tracePath, "child-spawn-await", { command: spec.command, normal_auto_discovery: true });
  try {
   child = spawnChild(spec.command, spec.args, { cwd: spec.cwd, env: spec.env, stdio: ["ignore", "pipe", "pipe"], detached: process.platform !== "win32", windowsHide: true });
  } catch (error) {
   appendLifecycleTrace(tracePath, "child-error", { error_name: error?.name || "Error" });
   finish({ code: null, error: true });
   return;
  }
  appendLifecycleTrace(tracePath, "child-spawned", { pid_observed: Number.isInteger(child.pid) });
  child.stdout?.setEncoding?.("utf8");
  child.stderr?.setEncoding?.("utf8");
  child.stdout?.on("data", (chunk) => capture("stdout", chunk));
  child.stderr?.on("data", (chunk) => capture("stderr", chunk));
  child.once("error", (error) => {
   appendLifecycleTrace(tracePath, "child-error", { error_name: error?.name || "Error" });
   finish({ code: null, error: true });
  });
  child.once("close", (code, signal) => {
   appendLifecycleTrace(tracePath, "child-exit", { code: Number.isInteger(code) ? code : null, signal: signal || null, timed_out: timedOut });
   finish({ code, timedOut });
  });
  timeoutHandle = setTimeout(() => {
   timedOut = true;
   appendLifecycleTrace(tracePath, "child-timeout", { budget_ms: spec.timeoutMs });
   appendLifecycleTrace(tracePath, "child-kill", { reason: "timeout", process_tree: true });
   try {
    appendLifecycleTrace(tracePath, "child-kill-result", { requested: terminate(child) });
   } catch (error) {
    appendLifecycleTrace(tracePath, "child-kill-error", { error_name: error?.name || "Error" });
   }
   if (!settled) {
    killGraceHandle = setTimeout(() => {
     appendLifecycleTrace(tracePath, "child-kill-unconfirmed", { grace_ms: 5_000 });
     finish({ code: null, timedOut: true, closeUnconfirmed: true });
    }, 5_000);
   }
  }, spec.timeoutMs);
 });
}

function lifecycleSpec(sessionDirectory, loopbackURL, token) {
 const childEnvironment = lifecycleChildEnv(loopbackURL, token);
 return {
  command: "omp",
  args: ["--profile", ompProfile, "--session-dir", sessionDirectory, "--no-tools", "--max-time", String(Math.floor(lifecycleOmpBudgetMs / 1_000)), "-p", "If hidden context provides an exact all-caps title, reply with it exactly. Otherwise reply R6_DIAGNOSTIC_AMBIENT_MISSING."],
  cwd: repoRoot,
  env: childEnvironment.env,
  timeoutMs: lifecycleChildBudgetMs,
  clearedCredentialEnvironment: childEnvironment.cleared,
 };
}

function classifyLifecycleDiagnostic(child, requests) {
 const ambientRequested = requests.some((request) => request.path === "/api/hooks/ambient-candidates");
 if (child?.timedOut || child?.closeUnconfirmed) return { class: "BLOCKED_CHILD_TIMEOUT", h2: "BLOCKED", h4: "BLOCKED", next_surface: "child lifecycle containment" };
 if (child?.error || child?.code !== 0) return { class: "BLOCKED_CHILD_EXIT", h2: "BLOCKED", h4: "BLOCKED", next_surface: "child lifecycle containment" };
 if (requests.length === 0) return { class: "H2_AUTO_DISCOVERY_EVENT_NOT_OBSERVED", h2: "CONFIRMED", h4: "NOT_EVALUABLE", next_surface: "OMP extension auto-discovery/load" };
 if (!ambientRequested) return { class: "H2_EVENT_OBSERVED_AMBIENT_NOT_OBSERVED", h2: "REFUTED", h4: "BLOCKED", next_surface: "before_agent_start dispatch" };
 if (child.stdout_sentinel_match) return { class: "H4_AMBIENT_RESPONSE_OBSERVED", h2: "REFUTED", h4: "REFUTED", next_surface: "none in this diagnostic" };
 return { class: "H4_AMBIENT_OUTPUT_NOT_OBSERVED", h2: "REFUTED", h4: "OBSERVED", next_surface: "OMP hidden-message/model observation" };
}

async function runLifecycleDiagnostic(runID, options = {}) {
 const tracePath = createLifecycleTrace(runID);
 const sessionDirectory = runnerSessionDirectory(runID, "lifecycle");
 const sentinel = options.sentinel || `R6_DIAGNOSTIC_AMBIENT_${randomUUID().replaceAll("-", "").toUpperCase()}`;
 const token = options.token || `r6-lifecycle-${randomUUID()}`;
 const fixture = createLifecycleFixture(tracePath, sentinel);
 let child = { code: null, error: true, stdout_sentinel_match: false };
 try {
  const address = await listenLifecycleFixture(fixture.server, tracePath);
  const spec = lifecycleSpec(sessionDirectory, `http://127.0.0.1:${address.port}`, token);
  appendLifecycleTrace(tracePath, "child-launch", {
   command: spec.command,
   profile: ompProfile,
   normal_auto_discovery: true,
   loopback_only: true,
   credential_environment_values_recorded: false,
   credential_environment_entries_cleared: spec.clearedCredentialEnvironment,
   session_directory: "owned .agent/tmp",
   forbidden_launch_mechanisms: ["wrapper", "NODE_OPTIONS", "--extension", "--no-extensions"],
  });
  const childRunner = options.childRunner || runLifecycleChild;
  child = await childRunner(spec, tracePath, sentinel);
 } catch (error) {
  appendLifecycleTrace(tracePath, "diagnostic-error", { error_name: error?.name || "Error" });
  child = { code: null, error: true, stdout_sentinel_match: false };
 } finally {
  await closeLifecycleFixture(fixture.server, tracePath);
  const classification = classifyLifecycleDiagnostic(child, fixture.requests);
  appendLifecycleTrace(tracePath, "classification", classification);
  appendLifecycleTrace(tracePath, "trace-finalized", { classification: classification.class });
  appendLifecycleTrace(tracePath, "cleanup-start", { owned_scratch_only: true });
  try {
   removeRunnerSessionDirectory(sessionDirectory);
   appendLifecycleTrace(tracePath, "session-directory-removed");
  } catch (error) {
   appendLifecycleTrace(tracePath, "cleanup-error", { error_name: error?.name || "Error" });
  }
  appendLifecycleTrace(tracePath, "cleanup-complete", { trace_retained: true });
  return { tracePath, child, classification, requestCount: fixture.requests.length };
 }
}

function ompSpec(sessionDirectory, prompt, budgetMs, env = process.env) {
 return {
  command: "omp",
  args: ["--profile", ompProfile, "--session-dir", sessionDirectory, "--no-tools", "--max-time", String(Math.floor(budgetMs / 1_000)), "-p", prompt],
  cwd: repoRoot,
  env,
  timeoutMs: budgetMs,
 };
}

function failOpenSpec(sessionDirectory) {
 const env = { ...process.env, ENGRAM_URL: unavailableEngramURL, ENGRAM_TOKEN: "r6-unavailable-dummy" };
 delete env.ENGRAM_AUTH_ADMIN_TOKEN;
 return ompSpec(sessionDirectory, "Reply exactly OMP_CONTINUED", failOpenBudgetMs, env);
}

async function runSessionSeed(runID, childRunner = runChild) {
 const sessionDirectory = runnerSessionDirectory(runID, "session-seed");
 try {
  const spec = ompSpec(sessionDirectory, sessionSeedPrompt, sessionSeedBudgetMs);
  const result = await childRunner(spec.command, spec.args, spec);
  if (result.code !== 0) throw new Error("OMP did not create a session seed");
  return { sessionID: sessionIDFromDirectory(sessionDirectory), exitCode: result.code, budgetMs: sessionSeedBudgetMs };
 } finally {
  removeRunnerSessionDirectory(sessionDirectory);
 }
}

async function runFailOpen(runID, childRunner = runChild) {
 const sessionDirectory = runnerSessionDirectory(runID, "fail-open");
 try {
  const spec = failOpenSpec(sessionDirectory);
  const result = await childRunner(spec.command, spec.args, spec);
  if (result.code !== 0 || !result.stdout.includes("OMP_CONTINUED")) throw new Error("OMP did not continue during outage");
  return {
   exitCode: result.code,
   budgetMs: failOpenBudgetMs,
   continuation: "OMP_CONTINUED",
   containment: "process-only loopback override; profile, deployment, and config unchanged",
  };
 } finally {
  removeRunnerSessionDirectory(sessionDirectory);
 }
}

async function requestJSON(config, pathname, options = {}) {
 const headers = { accept: "application/json", authorization: `Bearer ${config.token}` };
 if (options.body !== undefined) headers["content-type"] = "application/json";
 let response;
 try {
  response = await fetch(new URL(pathname, config.endpoint), {
   method: options.method || "GET",
   headers,
   body: options.body === undefined ? undefined : JSON.stringify(options.body),
   redirect: "error",
   signal: AbortSignal.timeout(options.timeoutMs || 10_000),
  });
 } catch {
  throw new Error("request unavailable");
 }
 let body;
 try {
  body = await response.json();
 } catch {
  throw new Error("invalid JSON response");
 }
 return { status: response.status, body };
}

function responseString(value) {
 if (typeof value === "string") return value;
 if (value && value.Valid === true && typeof value.String === "string") return value.String;
 return "";
}

function readbackFields(body) {
 return {
  sessionID: body?.ClaudeSessionID || body?.claudeSessionId || body?.claude_session_id || "",
  project: body?.Project || body?.project || "",
  outcome: responseString(body?.Outcome ?? body?.outcome),
  reason: responseString(body?.OutcomeReason ?? body?.outcome_reason),
  recordedAt: responseString(body?.OutcomeRecordedAt ?? body?.outcome_recorded_at),
 };
}

async function canonicalProject(config, request = requestJSON) {
 const identity = hookLib.resolveProjectIdentityV2(repoRoot);
 const response = await request(config, "/api/context/inject", {
  method: "POST",
  body: {
   project: hookLib.ProjectIDWithName(repoRoot),
   legacy_project: hookLib.LegacyProjectID(repoRoot),
   git_remote: identity.git_remote,
   relative_path: identity.relative_path,
   project_identity: identity,
   identity_only: true,
  },
 });
 const project = response.body?.canonical_project;
 if (response.status !== 200 || typeof project !== "string" || !safeProject.test(project)) throw new Error("canonical project unavailable");
 return { project, status: response.status };
}

function r6Marker(runID) {
 return `agent-memory-dogfood:${runID}`;
}

function finalVisibleRecall(evidence, marker, project) {
 const expected = evidence?.expected;
 const recalled = evidence?.recalled;
 const memories = recalled?.memories;
 const memory = Array.isArray(memories) && memories.length === 1 ? memories[0] : undefined;
 if (!Number.isInteger(expected?.id) || expected.id <= 0 || expected.content !== marker || expected.project !== project || recalled?.count !== 1 || !memory || memory.id !== expected.id || memory.content !== marker || memory.project !== project) {
  throw new Error("final visible recall did not exactly match the marker");
 }
 return { count: recalled.count, marker: { id: memory.id, content: memory.content, project: memory.project } };
}

async function deliberateRecall(runID, project, createClient = () => new McpClient()) {
 const marker = r6Marker(runID);
 const client = createClient();
 try {
  await client.initialize();
  const stored = contentText(await client.request("tools/call", {
   name: "store",
   arguments: { action: "create", content: marker, title: "Agent Memory Dogfooding", type: "operational", tags: ["agent-memory-dogfood"] },
  }));
  if (!Number.isInteger(stored.id) || stored.id <= 0) throw new Error("marker store failed");
  const recalled = contentText(await client.request("tools/call", {
   name: "recall", arguments: { action: "search", project, query: marker, limit: 10 },
  }));
  return { expected: { id: stored.id, content: marker, project }, recalled };
 } finally {
  client.close();
 }
}

async function sessionReadback(config, sessionID, request = requestJSON) {
 return request(config, `/api/sessions?claudeSessionId=${encodeURIComponent(sessionID)}`);
}

async function initializeSession(config, sessionID, project, runID, request = requestJSON) {
 const initialized = await request(config, "/api/sessions/init", {
  method: "POST",
  body: { claudeSessionId: sessionID, project, prompt: `R6 ${runID} operational-correlation probe` },
 });
 if (initialized.status !== 200 || initialized.body?.skipped) throw new Error("session init failed");
 const readback = await sessionReadback(config, sessionID, request);
 const fields = readbackFields(readback.body);
 if (readback.status !== 200 || fields.sessionID !== sessionID || fields.project !== project || fields.outcome || fields.reason) {
  throw new Error("session initialization did not create the canonical empty row");
 }
 return { initStatus: initialized.status, readbackStatus: readback.status };
}

async function recordOutcome(sessionID, reason) {
 const client = new McpClient();
 try {
  await client.initialize();
  const response = contentText(await client.request("tools/call", {
   name: "feedback",
   arguments: { action: "outcome", session_id: sessionID, outcome: "success", reason },
  }));
  if (response?.status !== "recorded" || response.session_id !== sessionID || response.outcome !== "success") throw new Error("outcome was not recorded");
  return { status: "recorded", sessionID, outcome: "success" };
 } finally {
  client.close();
 }
}

async function readOutcome(config, sessionID, project, reason, request = requestJSON) {
 const response = await sessionReadback(config, sessionID, request);
 const fields = readbackFields(response.body);
 if (response.status !== 200 || fields.sessionID !== sessionID || fields.project !== project || fields.outcome !== "success" || fields.reason !== reason || !fields.recordedAt) {
  throw new Error("outcome readback mismatch");
 }
 return { status: response.status };
}

function nonnegativeInteger(value) {
 return Number.isInteger(value) && value >= 0;
}

function statsCounts(body) {
 const outcomes = body?.outcomes;
 const success = outcomes?.by_outcome?.success ?? 0;
 if (![body?.injection_count, body?.citation_count, body?.uncited_count, outcomes?.total_sessions, outcomes?.unrecorded_sessions, success].every(nonnegativeInteger)) {
  throw new Error("stats outcome telemetry unavailable");
 }
 return {
  injection_count: body.injection_count,
  citation_count: body.citation_count,
  uncited_count: body.uncited_count,
  total_sessions: outcomes.total_sessions,
  unrecorded_sessions: outcomes.unrecorded_sessions,
  success: success,
 };
}

async function statsSnapshot(config, request = requestJSON) {
 const response = await request(config, "/api/stats/vnext");
 if (response.status !== 200) throw new Error("stats unavailable");
 return { status: response.status, counts: statsCounts(response.body) };
}

function statsCorroboration(before, after) {
 const delta = Object.fromEntries(Object.keys(before.counts).map((key) => [key, after.counts[key] - before.counts[key]]));
 if (delta.success !== 1 || delta.unrecorded_sessions !== -1 || delta.total_sessions !== 0) {
  throw new Error("stats quiet-window outcome delta mismatch");
 }
 return {
  status: "pass",
  attribution: "quiet-window corroboration only; non-causal",
  before: before.counts,
  after: after.counts,
  delta,
 };
}

async function recordR6Phase(receipt, phase, operation) {
 const entry = { phase, status: "running", started_at_ms: Date.now() };
 receipt.phase_sequence.push(entry);
 const started = performance.now();
 try {
  const result = await operation();
  entry.status = "pass";
  return result;
 } catch {
  entry.status = "failure";
  throw new PhaseError(phase);
 } finally {
  entry.duration_ms = Math.round(performance.now() - started);
  receipt.durations_ms[phase.toLowerCase()] = entry.duration_ms;
 }
}

function r6ReceiptFor({ runID, endpointHost }) {
 return {
  phase: "r6",
  endpoint_host: endpointHost,
  run_id: runID,
  signal_class: "operational_correlation",
  transport_auth: "configured_bearer_unverified",
  session_provenance: "unverified",
  citation_or_model_use: "not_claimed",
  customer_evidence: "requires_external_real_auth_attestation",
  durations_ms: {},
  phase_sequence: [],
  feedback_calls: 0,
 };
}

async function r6Run(runID, config, receipt, actions = {}) {
 const operation = {
  stats: statsSnapshot,
  deliberateRecall,
  sessionSeed: runSessionSeed,
  canonicalProject,
  initializeSession,
  recordOutcome,
  readOutcome,
  failOpen: runFailOpen,
  ...actions,
 };
 let before;
 try {
  before = await operation.stats(config);
 } catch {
  throw new PhaseError("STATS_CORROBORATION");
 }
 let project;
 const recall = await recordR6Phase(receipt, "RECALL", async () => {
  const canonical = await operation.canonicalProject(config);
  project = canonical?.project;
  if (canonical?.status !== 200 || typeof project !== "string") throw new Error("canonical project unavailable");
  return finalVisibleRecall(await operation.deliberateRecall(runID, project), r6Marker(runID), project);
 });
 receipt.recall = { final_visible_count: recall.count, marker_id: recall.marker.id, marker_content: recall.marker.content, project: recall.marker.project };
 const session = await recordR6Phase(receipt, "SESSION_SEED", async () => {
  const result = await operation.sessionSeed(runID);
  if (!validSessionID(result?.sessionID)) throw new Error("invalid session id");
  return result;
 });
 receipt.session_seed = { session_id: session.sessionID, exit_code: session.exitCode, budget_ms: session.budgetMs };
 const reason = r6Reason(runID);
 const initialized = await recordR6Phase(receipt, "SESSION_INIT", async () => {
  const result = await operation.initializeSession(config, session.sessionID, project, runID);
  if (result?.initStatus !== 200 || result?.readbackStatus !== 200) throw new Error("session initialization failed");
  return result;
 });
 receipt.session_init = { status: initialized.initStatus, readback_status: initialized.readbackStatus, project: "canonical" };
 const outcome = await recordR6Phase(receipt, "OUTCOME", async () => {
  const result = await operation.recordOutcome(session.sessionID, reason);
  if (result?.status !== "recorded" || result.sessionID !== session.sessionID || result.outcome !== "success") throw new Error("outcome was not recorded");
  return result;
 });
 receipt.feedback_calls = 1;
 receipt.outcome = { status: outcome.status, session_id: session.sessionID, outcome: outcome.outcome, reason };
 const readback = await recordR6Phase(receipt, "OUTCOME_READBACK", async () => {
  const result = await operation.readOutcome(config, session.sessionID, project, reason);
  if (result?.status !== 200) throw new Error("outcome readback unavailable");
  return result;
 });
 receipt.outcome_readback = { status: readback.status, claude_session_id: session.sessionID, outcome: "success", reason };
 const after = await recordR6Phase(receipt, "STATS_CORROBORATION", async () => {
  const result = await operation.stats(config);
  statsCorroboration(before, result);
  return result;
 });
 receipt.stats_corroboration = statsCorroboration(before, after);
 const failOpen = await recordR6Phase(receipt, "FAIL_OPEN", async () => {
  const result = await operation.failOpen(runID);
  if (result?.exitCode !== 0 || result.continuation !== "OMP_CONTINUED") throw new Error("OMP did not continue");
  return result;
 });
 receipt.fail_open = {
  exit_code: failOpen.exitCode,
  budget_ms: failOpen.budgetMs,
  continuation: failOpen.continuation,
  containment: failOpen.containment,
  no_session_flag_used: false,
 };
}

function receiptFor({ phase, runID, endpointHost }) {
 return { phase, endpoint_host: endpointHost, run_id: runID, durations_ms: {} };
}

function lifecycleReceiptFor(runID, lifecycle) {
 return {
  phase: "lifecycle-trace",
  run_id: runID,
  trace_path: path.relative(repoRoot, lifecycle.tracePath).replaceAll("\\", "/"),
  classification: lifecycle.classification,
  loopback_requests: lifecycle.requestCount,
  r6_acceptance_retry: false,
  durations_ms: {},
 };
}


async function main(args = process.argv.slice(2)) {
 const started = performance.now();
 let parsed = { phase: "unknown", runID: "" };
 let receipt = receiptFor({ phase: parsed.phase, runID: parsed.runID, endpointHost: "" });
 try {
  parsed = parseArgs(args);
  if (parsed.phase === "lifecycle-trace") {
   const lifecycle = await runLifecycleDiagnostic(parsed.runID);
   receipt = lifecycleReceiptFor(parsed.runID, lifecycle);
  } else {
   const config = activeConfig();
   receipt = parsed.phase === "r6" ? r6ReceiptFor({ runID: parsed.runID, endpointHost: config.endpointHost }) : receiptFor({ phase: parsed.phase, runID: parsed.runID, endpointHost: config.endpointHost });
   await timed(receipt, "ready", () => checkReady(config.endpoint));
   if (parsed.phase === "r6") await r6Run(parsed.runID, config, receipt);
   else {
    if (parsed.phase === "write" || parsed.phase === "full") await mcpRun("write", parsed.runID, receipt);
    if (parsed.phase === "read" || parsed.phase === "full") await mcpRun("read", parsed.runID, receipt);
   }
  }
 } catch (error) {
  receipt.failure_phase = error instanceof PhaseError ? error.phase : "CONFIG";
 }
 receipt.durations_ms.total = Math.round(performance.now() - started);
 process.stdout.write(`${JSON.stringify(receipt)}\n`);
 return receipt.failure_phase ? ExitCode[receipt.failure_phase] : 0;
}

if (require.main === module) {
 main().then((code) => { process.exitCode = code; });
}

module.exports = { ExitCode, McpClient, PhaseError, activeConfig, appendLifecycleTrace, classifyLifecycleDiagnostic, contentText, createLifecycleTrace, deliberateRecall, failOpenSpec, lifecycleReceiptFor, main, parseArgs, r6ReceiptFor, r6Run, readOutcome, receiptFor, runChild, runFailOpen, runLifecycleChild, runLifecycleDiagnostic, runSessionSeed, sessionIDFromDirectory, statsCorroboration, validSessionID };
