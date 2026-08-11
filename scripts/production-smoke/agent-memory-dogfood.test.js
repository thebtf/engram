const fs = require("node:fs");
const assert = require("node:assert/strict");
const { EventEmitter } = require("node:events");
const test = require("node:test");

const { ExitCode, contentText, createLifecycleTrace, deliberateRecall, failOpenSpec, lifecycleReceiptFor, main, parseArgs, r6ReceiptFor, r6Run, readOutcome, receiptFor, runFailOpen, runLifecycleChild, runLifecycleDiagnostic, runSessionSeed, statsCorroboration, validSessionID } = require("./agent-memory-dogfood.js");

test("parses only supported phases and safe run IDs", () => {
 assert.deepEqual(parseArgs(["--phase", "read", "--run-id", "run_42"]), { phase: "read", runID: "run_42" });
 assert.throws(() => parseArgs(["--phase", "delete"]), /CONFIG/);
 assert.throws(() => parseArgs(["--run-id", "marker with spaces"]), /CONFIG/);
 assert.throws(() => parseArgs(["--phase"]), /CONFIG/);
 assert.throws(() => parseArgs(["--run-id"]), /CONFIG/);

 const codes = Object.values(ExitCode);
 assert.equal(new Set(codes).size, codes.length);
 assert.ok(codes.every((code) => Number.isInteger(code) && code > 0));
});

test("receipt has an allowlisted secret-safe shape", () => {
 const receipt = receiptFor({ phase: "write", runID: "run_42", endpointHost: "engram.example.test:37777" });
 receipt.marker_id = 42;
 receipt.durations_ms.total = 1;
 const json = JSON.stringify(receipt);
 assert.deepEqual(Object.keys(receipt).sort(), ["durations_ms", "endpoint_host", "marker_id", "phase", "run_id"]);
 assert.doesNotMatch(json, /engram_[A-Za-z0-9]+|Authorization|token/i);
});

test("lifecycle receipt remains writable after a finalized trace", () => {
 const receipt = lifecycleReceiptFor("lifecycle-receipt", {
  tracePath: "D:\\Dev\\engram\\.agent\\runs\\agent-memory-dogfooding\\r6-lifecycle-trace.jsonl",
  classification: { class: "H2_AUTO_DISCOVERY_EVENT_NOT_OBSERVED" },
  requestCount: 0,
 });
 receipt.durations_ms.total = 1;
 assert.deepEqual(receipt, {
  phase: "lifecycle-trace",
  run_id: "lifecycle-receipt",
  trace_path: ".agent/runs/agent-memory-dogfooding/r6-lifecycle-trace.jsonl",
  classification: { class: "H2_AUTO_DISCOVERY_EVENT_NOT_OBSERVED" },
  loopback_requests: 0,
  r6_acceptance_retry: false,
  durations_ms: { total: 1 },
 });
});

test("receipt records safe matching marker correlation", () => {
 const marker = "agent-memory-dogfood:run_42";
 const receipt = receiptFor({ phase: "full", runID: "run_42", endpointHost: "engram.example.test:37777" });
 Object.assign(receipt, { marker_id: 42, marker_content: marker, recalled_marker_id: 42, recalled_marker_content: marker });
 assert.equal(receipt.marker_id, receipt.recalled_marker_id);
 assert.equal(receipt.marker_content, receipt.recalled_marker_content);
 assert.doesNotMatch(JSON.stringify(receipt), /engram_[A-Za-z0-9]+|Authorization|token/i);
});

test("unwraps the mounted MCP content envelope", () => {
 const health = { overall_status: "healthy", health_score: 100 };
 const directResult = { content: [{ type: "text", text: JSON.stringify(health) }] };
 const mountedResult = { content: [{ type: "text", text: JSON.stringify(directResult) }] };
 assert.deepEqual(contentText(directResult), health);
 assert.deepEqual(contentText(mountedResult), health);
});

test("invalid input produces a typed CONFIG receipt and exit code", async () => {
 let output = "";
 const write = process.stdout.write;
 process.stdout.write = (chunk) => { output += chunk; return true; };
 try {
  assert.equal(await main(["--phase", "invalid"]), ExitCode.CONFIG);
 } finally {
  process.stdout.write = write;
 }
 assert.equal(JSON.parse(output).failure_phase, "CONFIG");
});

test("R6 rejects numeric and malformed session IDs", () => {
 assert.equal(validSessionID("019fefed-3b53-7000-89fd-a0169a83cbcc"), true);
 assert.equal(validSessionID("3236"), false);
 assert.equal(validSessionID("not-a-session"), false);
 assert.equal(validSessionID("019fefed-3b53-6000-89fd-a0169a83cbcc"), false);
});

function exactRecallEvidence(runID, project, patch = {}) {
 const marker = `agent-memory-dogfood:${runID}`;
 const expected = { id: 42, content: marker, project, ...patch.expected };
 const memory = { id: 42, content: marker, project, ...patch.memory };
 return { expected, recalled: { count: 1, memories: [memory], ...patch.recalled } };
}

test("R6 deliberate recall wires store and project-scoped recall through MCP", async () => {
 const runID = "r6-direct-recall";
 const project = "p2n_canonical";
 const marker = `agent-memory-dogfood:${runID}`;
 const calls = [];
 const response = (body) => ({ content: [{ type: "text", text: JSON.stringify(body) }] });
 const client = {
  initialize: async () => { calls.push(["initialize"]); },
  request: async (method, params) => {
   calls.push([method, params]);
   return params.arguments.action === "create"
    ? response({ id: 42 })
    : response({ count: 1, memories: [{ id: 42, content: marker, project }] });
  },
  close: () => { calls.push(["close"]); },
 };
 const evidence = await deliberateRecall(runID, project, () => client);
 assert.deepEqual(evidence, { expected: { id: 42, content: marker, project }, recalled: { count: 1, memories: [{ id: 42, content: marker, project }] } });
 assert.deepEqual(calls, [
  ["initialize"],
  ["tools/call", { name: "store", arguments: { action: "create", content: marker, title: "Agent Memory Dogfooding", type: "operational", tags: ["agent-memory-dogfood"] } }],
  ["tools/call", { name: "recall", arguments: { action: "search", project, query: marker, limit: 10 } }],
  ["close"],
 ]);
});

test("R6 outcome readback rejects every mismatched outcome field", async (t) => {
 const sessionID = "019fefed-3b53-7000-89fd-a0169a83cbcc";
 const project = "p2n_canonical";
 const reason = "R6 r6-readback runner operational-correlation event completed";
 const expectedPath = `/api/sessions?claudeSessionId=${encodeURIComponent(sessionID)}`;
 const valid = { ClaudeSessionID: sessionID, Project: project, Outcome: { Valid: true, String: "success" }, OutcomeReason: { Valid: true, String: reason }, OutcomeRecordedAt: { Valid: true, String: "2026-08-12T00:00:00Z" } };
 const absentTimestamp = { ...valid };
 delete absentTimestamp.OutcomeRecordedAt;
 await readOutcome({}, sessionID, project, reason, async (_config, pathname) => {
  assert.equal(pathname, expectedPath);
  return { status: 200, body: valid };
 });
 const cases = [
  ["non-200 HTTP status", { status: 500, body: valid }],
  ["mismatched session ID", { status: 200, body: { ...valid, ClaudeSessionID: "019fefed-3b53-7000-89fd-a0169a83cbcd" } }],
  ["mismatched project", { status: 200, body: { ...valid, Project: "p2n_other" } }],
  ["mismatched outcome", { status: 200, body: { ...valid, Outcome: { Valid: true, String: "failure" } } }],
  ["mismatched reason", { status: 200, body: { ...valid, OutcomeReason: { Valid: true, String: "other reason" } } }],
  ["absent recorded timestamp", { status: 200, body: absentTimestamp }],
  ["empty recorded timestamp", { status: 200, body: { ...valid, OutcomeRecordedAt: { Valid: true, String: "" } } }],
 ];
 for (const [name, response] of cases) await t.test(name, async () => {
  await assert.rejects(readOutcome({}, sessionID, project, reason, async () => response), /outcome readback mismatch/);
 });
});

test("R6 records an exact deliberate recall before session feedback with a runner-event reason", async () => {
 const sessionID = "019fefed-3b53-7000-89fd-a0169a83cbcc";
 const runID = "r6-safe";
 const reason = "R6 r6-safe runner operational-correlation event completed";
 const project = "p2n_canonical";
 const before = { status: 200, counts: { injection_count: 4, citation_count: 2, uncited_count: 1, total_sessions: 10, unrecorded_sessions: 7, success: 2 } };
 const after = { status: 200, counts: { injection_count: 4, citation_count: 2, uncited_count: 1, total_sessions: 10, unrecorded_sessions: 6, success: 3 } };
 const calls = [];
 let statsCalls = 0;
 const receipt = r6ReceiptFor({ runID, endpointHost: "engram.example.test:37777" });
 await r6Run(runID, { token: "engram_redacted_secret" }, receipt, {
  stats: async () => { calls.push(`stats-${statsCalls + 1}`); return statsCalls++ === 0 ? before : after; },
  canonicalProject: async () => { calls.push("canonical-project"); return { project, status: 200 }; },
  deliberateRecall: async (actualRunID, actualProject) => { calls.push("deliberate-recall"); assert.equal(actualRunID, runID); assert.equal(actualProject, project); return exactRecallEvidence(runID, project); },
  sessionSeed: async () => { calls.push("session-seed"); return { sessionID, exitCode: 0, budgetMs: 95_000 }; },
  initializeSession: async (_config, actualSessionID, actualProject, actualRunID) => {
   calls.push("session-init");
   assert.equal(actualSessionID, sessionID);
   assert.equal(actualProject, project);
   assert.equal(actualRunID, runID);
   return { initStatus: 200, readbackStatus: 200 };
  },
  recordOutcome: async (actualSessionID, actualReason) => {
   calls.push("feedback");
   assert.equal(actualSessionID, sessionID);
   assert.equal(actualReason, reason);
   assert.doesNotMatch(actualReason, /\b(?:session|agent|model|used)\b/i);
   return { status: "recorded", sessionID, outcome: "success" };
  },
  readOutcome: async (_config, actualSessionID, actualProject, actualReason) => {
   calls.push("outcome-readback");
   assert.equal(actualSessionID, sessionID);
   assert.equal(actualProject, project);
   assert.equal(actualReason, reason);
   return { status: 200 };
  },
  failOpen: async () => { calls.push("fail-open"); return { exitCode: 0, budgetMs: 65_000, continuation: "OMP_CONTINUED", containment: "process-only loopback override; profile, deployment, and config unchanged" }; },
 });
 assert.deepEqual(calls, ["stats-1", "canonical-project", "deliberate-recall", "session-seed", "session-init", "feedback", "outcome-readback", "stats-2", "fail-open"]);
 assert.deepEqual(receipt.phase_sequence.map((entry) => [entry.phase, entry.status]), [
  ["RECALL", "pass"], ["SESSION_SEED", "pass"], ["SESSION_INIT", "pass"], ["OUTCOME", "pass"], ["OUTCOME_READBACK", "pass"], ["STATS_CORROBORATION", "pass"], ["FAIL_OPEN", "pass"],
 ]);
 assert.deepEqual(receipt.recall, { final_visible_count: 1, marker_id: 42, marker_content: "agent-memory-dogfood:r6-safe", project });
 assert.deepEqual(receipt.session_seed, { session_id: sessionID, exit_code: 0, budget_ms: 95_000 });
 assert.equal(receipt.feedback_calls, 1);
 assert.deepEqual(receipt.outcome_readback, { status: 200, claude_session_id: sessionID, outcome: "success", reason });
 assert.equal(receipt.stats_corroboration.attribution, "quiet-window corroboration only; non-causal");
 assert.doesNotMatch(JSON.stringify(receipt), /engram_redacted|Authorization|token/i);
});

test("R6 rejects non-exact final recall before any feedback", async (t) => {
 const runID = "r6-invalid-recall";
 const project = "p2n_canonical";
 const cases = [
  ["zero count", { recalled: { count: 0 } }],
  ["wrong count", { recalled: { count: 2, memories: [{ id: 42, content: `agent-memory-dogfood:${runID}`, project }, { id: 43, content: `agent-memory-dogfood:${runID}`, project }] } }],
  ["wrong ID", { memory: { id: 43 } }],
  ["wrong content", { memory: { content: "agent-memory-dogfood:other-run" } }],
  ["wrong project", { memory: { project: "p2n_other" } }],
 ];
 for (const [name, patch] of cases) await t.test(name, async () => {
  const receipt = r6ReceiptFor({ runID, endpointHost: "engram.example.test" });
  const calls = [];
  await assert.rejects(
   r6Run(runID, {}, receipt, {
    stats: async () => ({ status: 200, counts: { injection_count: 0, citation_count: 0, uncited_count: 0, total_sessions: 1, unrecorded_sessions: 1, success: 0 } }),
    canonicalProject: async () => { calls.push("canonical-project"); return { project, status: 200 }; },
    deliberateRecall: async () => { calls.push("deliberate-recall"); return exactRecallEvidence(runID, project, patch); },
    sessionSeed: async () => { calls.push("session-seed"); throw new Error("must not run"); },
    recordOutcome: async () => { calls.push("feedback"); throw new Error("must not run"); },
   }),
   (error) => error?.phase === "RECALL",
  );
  assert.deepEqual(calls, ["canonical-project", "deliberate-recall"]);
  assert.equal(receipt.feedback_calls, 0);
  assert.deepEqual(receipt.phase_sequence.map((entry) => [entry.phase, entry.status]), [["RECALL", "failure"]]);
 });
});

test("R6 receipt labels operational correlation without prohibited claims", () => {
 const receipt = r6ReceiptFor({ runID: "r6-labels", endpointHost: "engram.example.test" });
 assert.deepEqual({ signal_class: receipt.signal_class, transport_auth: receipt.transport_auth, session_provenance: receipt.session_provenance, citation_or_model_use: receipt.citation_or_model_use, customer_evidence: receipt.customer_evidence }, {
  signal_class: "operational_correlation",
  transport_auth: "configured_bearer_unverified",
  session_provenance: "unverified",
  citation_or_model_use: "not_claimed",
  customer_evidence: "requires_external_real_auth_attestation",
 });
 assert.equal("auth_context" in receipt, false);
 assert.equal("evidence_scope" in receipt, false);
 assert.equal("automatic_memory" in receipt, false);
});

test("R6 keeps a bad session ID as a typed session-seed failure", async () => {
 const runID = "r6-failure";
 const project = "p2n_canonical";
 const receipt = r6ReceiptFor({ runID, endpointHost: "engram.example.test" });
 await assert.rejects(
  r6Run(runID, {}, receipt, {
   stats: async () => ({ status: 200, counts: { injection_count: 0, citation_count: 0, uncited_count: 0, total_sessions: 1, unrecorded_sessions: 1, success: 0 } }),
   canonicalProject: async () => ({ project, status: 200 }),
   deliberateRecall: async () => exactRecallEvidence(runID, project),
   sessionSeed: async () => ({ sessionID: "3236", exitCode: 0, budgetMs: 95_000 }),
  }),
  (error) => error?.phase === "SESSION_SEED",
 );
 assert.deepEqual(receipt.phase_sequence.map((entry) => [entry.phase, entry.status]), [["RECALL", "pass"], ["SESSION_SEED", "failure"]]);
});

test("R6 stats remain aggregate-only quiet-window corroboration", () => {
 const before = { status: 200, counts: { injection_count: 1, citation_count: 0, uncited_count: 0, total_sessions: 7, unrecorded_sessions: 5, success: 1 } };
 const after = { status: 200, counts: { injection_count: 1, citation_count: 0, uncited_count: 0, total_sessions: 7, unrecorded_sessions: 4, success: 2 } };
 const corroboration = statsCorroboration(before, after);
 assert.deepEqual(Object.keys(corroboration).sort(), ["after", "attribution", "before", "delta", "status"]);
 assert.equal(corroboration.attribution, "quiet-window corroboration only; non-causal");
 assert.deepEqual(corroboration.delta, { injection_count: 0, citation_count: 0, uncited_count: 0, total_sessions: 0, unrecorded_sessions: -1, success: 1 });
 assert.throws(() => statsCorroboration(before, before), /quiet-window outcome delta mismatch/);
});

test("R6 fail-open child overrides only its process environment", () => {
 const spec = failOpenSpec("C:\\agent-memory-dogfood-r6-fail-open");
 assert.equal(spec.command, "omp");
 assert.equal(spec.cwd.endsWith("engram"), true);
 assert.equal(spec.env.ENGRAM_URL, "http://127.0.0.1:9");
 assert.equal(spec.env.ENGRAM_TOKEN, "r6-unavailable-dummy");
 assert.equal(spec.env.ENGRAM_AUTH_ADMIN_TOKEN, undefined);
 assert.ok(!spec.args.includes("--no-session"));
 assert.equal(spec.args[spec.args.indexOf("--session-dir") + 1], "C:\\agent-memory-dogfood-r6-fail-open");
});

test("R6 creates a session seed without inspecting automatic-memory output", async () => {
 const sessionID = "019fefed-3b53-7000-89fd-a0169a83cbcc";
 const sessionCalls = [];
 const session = await runSessionSeed("r6-child-contract", async (command, args, options) => {
  sessionCalls.push({ command, args, options });
  const sessionDirectory = args[args.indexOf("--session-dir") + 1];
  fs.writeFileSync(`${sessionDirectory}/session.jsonl`, `${JSON.stringify({ id: sessionID })}\n`);
  return { code: 0, stdout: "unrelated automatic-memory output" };
 });
 assert.equal(session.sessionID, sessionID);
 assert.deepEqual(sessionCalls.map((call) => call.command), ["omp"]);
 assert.ok(!sessionCalls[0].args.includes("--no-session"));
 assert.equal(sessionCalls[0].options.timeoutMs, session.budgetMs);

 const failOpenCalls = [];
 const failOpen = await runFailOpen("r6-child-contract", async (command, args, options) => {
  failOpenCalls.push({ command, args, options });
  return { code: 0, stdout: "OMP_CONTINUED" };
 });
 assert.equal(failOpen.continuation, "OMP_CONTINUED");
 assert.deepEqual(failOpenCalls.map((call) => call.command), ["omp"]);
 assert.equal(failOpenCalls[0].options.env.ENGRAM_URL, "http://127.0.0.1:9");
 assert.equal(failOpenCalls[0].options.env.ENGRAM_TOKEN, "r6-unavailable-dummy");
 assert.ok(!failOpenCalls[0].args.includes("--no-session"));
});

function traceEvents(tracePath) {
 return fs.readFileSync(tracePath, "utf8").trim().split("\n").map((line) => JSON.parse(line));
}

function fakeChild(pid = 1) {
 const child = new EventEmitter();
 child.pid = pid;
 child.stdout = new EventEmitter();
 child.stderr = new EventEmitter();
 child.kill = () => true;
 return child;
}

test("lifecycle trace flushes header, fixture, and launch metadata before awaits", async () => {
 let checked = false;
 const result = await runLifecycleDiagnostic("lifecycle-flush", {
  childRunner: async (_spec, tracePath) => {
   const events = traceEvents(tracePath).map((entry) => entry.event);
   assert.deepEqual(events, ["trace-header", "fixture-listening-await", "fixture-listening", "child-launch"]);
   checked = true;
   return { code: 0, stdout_sentinel_match: false };
  },
 });
 try {
  assert.equal(checked, true);
  assert.equal(fs.existsSync(result.tracePath), true);
 } finally {
  fs.rmSync(result.tracePath, { force: true });
 }
});

test("lifecycle trace redacts credentials, query values, and model input", async () => {
 const token = "r6-loopback-dummy-token-should-not-appear";
 const query = "r6-private-query-should-not-appear";
 const prompt = "r6-private-prompt-should-not-appear";
 const result = await runLifecycleDiagnostic("lifecycle-redaction", {
  token,
  childRunner: async (spec) => {
   const response = await fetch(`${spec.env.ENGRAM_URL}/api/hooks/ambient-candidates?query=${query}`, {
    method: "POST",
    headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
    body: JSON.stringify({ prompt_text: prompt, session_id: "safe-session", limit: 3 }),
   });
   assert.equal(response.status, 200);
   return { code: 0, stdout_sentinel_match: false };
  },
 });
 try {
  const trace = fs.readFileSync(result.tracePath, "utf8");
  assert.equal(result.requestCount, 1);
  assert.doesNotMatch(trace, new RegExp(`${token}|${query}|${prompt}|Bearer`, "i"));
  assert.match(trace, /"path":"\/api\/hooks\/ambient-candidates"/);
  assert.match(trace, /"authorization_present":true/);
  assert.match(trace, /"body_keys":\["prompt_text","session_id","limit"\]/);
  assert.match(trace, /"field_lengths":\{"prompt_text":35,"session_id":12,"limit":null\}/);
 } finally {
  fs.rmSync(result.tracePath, { force: true });
 }
});

test("lifecycle trace preserves exit and timeout kill events", async () => {
 const tracePath = createLifecycleTrace("lifecycle-child-events");
 try {
  const exitChild = fakeChild(101);
  const exited = await runLifecycleChild({ command: "fake", args: [], cwd: process.cwd(), env: process.env, timeoutMs: 100 }, tracePath, "SENTINEL", () => {
   queueMicrotask(() => {
    exitChild.stdout.emit("data", "SENTINEL");
    exitChild.stderr.emit("data", "bounded stderr");
    exitChild.emit("close", 0, null);
   });
   return exitChild;
  });
  assert.equal(exited.code, 0);
  assert.equal(exited.stdout_sentinel_match, true);

  const timeoutChild = fakeChild(102);
  let treeKillRequested = false;
  const timedOut = await runLifecycleChild({ command: "fake", args: [], cwd: process.cwd(), env: process.env, timeoutMs: 5 }, tracePath, "SENTINEL", () => timeoutChild, (child) => {
   treeKillRequested = true;
   queueMicrotask(() => child.emit("close", null, "SIGKILL"));
   return true;
  });
  assert.equal(timedOut.timedOut, true);
  assert.equal(treeKillRequested, true);
  const events = traceEvents(tracePath).map((entry) => entry.event);
  assert.ok(events.includes("child-exit"));
  assert.ok(events.includes("child-timeout"));
  assert.ok(events.includes("child-kill"));
 } finally {
  fs.rmSync(tracePath, { force: true });
 }
});

test("lifecycle cleanup follows durable trace finalization and retains the trace", async () => {
 const result = await runLifecycleDiagnostic("lifecycle-cleanup", {
  childRunner: async () => ({ code: 0, stdout_sentinel_match: false }),
 });
 try {
  const events = traceEvents(result.tracePath).map((entry) => entry.event);
  const finalized = events.indexOf("trace-finalized");
  const cleanup = events.indexOf("cleanup-start");
  const removed = events.indexOf("session-directory-removed");
  const complete = events.indexOf("cleanup-complete");
  assert.ok(finalized >= 0 && finalized < cleanup && cleanup < removed && removed < complete);
  assert.equal(fs.existsSync(result.tracePath), true);
 } finally {
  fs.rmSync(result.tracePath, { force: true });
 }
});
