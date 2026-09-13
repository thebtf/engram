import { spawn } from "node:child_process";
import { createServer } from "node:http";
import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";

import {
  Deadline,
  acquireLock,
  cleanupExecution,
  Progress,
  classifyProfileFailure,
  collectCoverage,
  consumeGoEvents,
  createLogWriter,
  coverageProfiles,
  executeAnalysis,
  fingerprintProfile,
  finishGoEvents,
  materializeCoverage,
  parseOptions,
  parseDockerPort,
  redactChunks,
  reusableProfile,
  profileDescriptor,
  runOwnedCommand,
  profileTimeout,
  runCoverageProfile,
  scheduleProfiles,
  sha256,
  runWithCampaign,
  sourceInventory,
  summarizeGoEvents,
  validAnalysisEvidence,
  validateReport,
  waitForAnalysis,
  writeReceipt,
  testInventoryArguments,
} from "./run-sonarqube.mjs";

function temporaryDirectory() {
  return mkdtempSync(join(tmpdir(), "engram-sonar-runner-"));
}

function write(path, contents) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, contents);
}

function goEventState() {
  return { buffer: "", currentTest: null, currentPackage: null, passedTests: new Set(), skippedTests: new Map(), skipReasons: new Map(), failedTests: new Set(), startedTests: new Set(), passedPackages: new Set(), failedPackages: new Set(), lifecycle: new Set() };
}

function progressHarness(directory, now) {
  const runDir = join(directory, "run");
  mkdirSync(runDir, { recursive: true });
  writeFileSync(join(runDir, "events.ndjson"), "");
  const campaign = { runDir, manifest: { run_id: "11111111-1111-4111-8111-111111111111", candidate: candidate(), analysis: {} } };
  const deadline = { started: 0, remaining: () => 123456 };
  return { campaign, progress: new Progress(campaign, deadline, () => now.value), events: () => readFileSync(join(runDir, "events.ndjson"), "utf8").trim().split("\n").filter(Boolean).map(JSON.parse) };
}

function candidate(inputs = "inputs", worktree = "worktree") {
  return {
    repository_id: "repository",
    worktree_id: worktree,
    head: "a".repeat(40),
    tree: "b".repeat(40),
    inputs_sha256: inputs,
  };
}

function reusableFixture(directory, { entry = {}, input = "inputs", worktree = "worktree", environment = "environment" } = {}) {
  const coveragePath = join(directory, "profiles", "base", "attempt-1", "coverage.out");
  const eventPath = join(directory, "profiles", "base", "attempt-1", "test-events.ndjson");
  const coverage = "mode: atomic\nexample.go:1.1,1.2 1 1\n";
  const events = '{"Action":"pass","Package":"example","Test":"TestExample"}\n{"Action":"pass","Package":"example"}\n';
  write(coveragePath, coverage);
  write(eventPath, events);
  const profile = coverageProfiles[0];
  const currentCandidate = candidate(input, worktree);
  const currentEnvironment = { sha256: environment };
  const profileEntry = {
    name: profile.name,
    status: "passed",
    fingerprint: fingerprintProfile(profile, currentCandidate, currentEnvironment),
    coverage: { path: "profiles/base/attempt-1/coverage.out", sha256: sha256(coverage), bytes: Buffer.byteLength(coverage) },
    test_events: { path: "profiles/base/attempt-1/test-events.ndjson", sha256: sha256(events), bytes: Buffer.byteLength(events) },
    expected_tests: [{ package: "example", test: "TestExample" }],
    expected_test_inventory_sha256: sha256('[{"package":"example","test":"TestExample"}]'),
    package_counts: { passed: 1, failed: 0, tests_passed: 1, tests_skipped: 0 },
    ...entry,
  };
  return {
    profile,
    currentCandidate,
    currentEnvironment,
    record: {
      runDir: directory,
      manifest: {
        schema_version: 2,
        run_id: "11111111-1111-4111-8111-111111111111",
        candidate: currentCandidate,
        profiles: [profileEntry],
      },
    },
  };
}

test("cold profile evidence is reusable only for the exact candidate, profile, and environment", () => {
  const directory = temporaryDirectory();
  try {
    const fixture = reusableFixture(directory);
    const reuse = reusableProfile(fixture.record, fixture.profile, fixture.currentCandidate, fixture.currentEnvironment);
    assert.equal(reuse.sourceRun, fixture.record.manifest.run_id);
    assert.equal(reuse.artifact.sha256, fixture.record.manifest.profiles[0].coverage.sha256);

    const changedInputs = [
      "code", "test", "fixture", "dependency", "generated", "schema-config", "external-tool",
    ];
    for (const changed of changedInputs) {
      assert.equal(reusableProfile(fixture.record, fixture.profile, candidate(changed), fixture.currentEnvironment), null, changed);
    }
    assert.equal(reusableProfile(fixture.record, fixture.profile, fixture.currentCandidate, { sha256: "different-toolchain" }), null);
    assert.equal(reusableProfile(fixture.record, { ...fixture.profile, race: false }, fixture.currentCandidate, fixture.currentEnvironment), null);
    assert.equal(reusableProfile(fixture.record, fixture.profile, candidate("inputs", "different-worktree"), fixture.currentEnvironment), null);
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});

test("failed, skipped, partial, and digest-mismatched attempts remain retained but are rejected", () => {
  const directory = temporaryDirectory();
  try {
    for (const entry of [
      { status: "failed" },
      { unexpected_skip_count: 1 },
      { package_counts: { passed: 0 } },
      { coverage: { path: "profiles/base/attempt-1/coverage.out", sha256: "0".repeat(64), bytes: 1 } },
    ]) {
      const fixture = reusableFixture(directory, { entry });
      assert.equal(reusableProfile(fixture.record, fixture.profile, fixture.currentCandidate, fixture.currentEnvironment), null);
      assert.match(readFileSync(join(directory, "profiles", "base", "attempt-1", "coverage.out"), "utf8"), /^mode: atomic$/m);
    }
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});

test("working-byte inventory invalidates source, test, fixture, dependency, and config changes", () => {
  const directory = temporaryDirectory();
  try {
    const paths = ["app.go", "app_test.go", "testdata/fixture.json", "go.sum", "generated.go", "schema.sql", "tool.conf"];
    for (const path of paths) {
      const destination = join(directory, path);
      write(destination, "one");
    }
    const before = sourceInventory(directory).sha256;
    for (const path of paths) {
      writeFileSync(join(directory, path), `changed-${path}`);
      const after = sourceInventory(directory).sha256;
      assert.notEqual(after, before, path);
      writeFileSync(join(directory, path), "one");
    }
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});

test("dedicated coverage profiles remain serialized and stop after failure", async () => {
  const profiles = [
    { name: "base", resourceGroup: "exclusive" },
    { name: "hap-fixture", resourceGroup: "isolated-fixture" },
    { name: "operator-code-fixture", resourceGroup: "isolated-fixture" },
  ];
  let active = 0;
  let maximum = 0;
  const seen = [];
  const results = await scheduleProfiles(profiles, 2, async (profile) => {
    active += 1;
    maximum = Math.max(maximum, active);
    seen.push(profile.name);
    await new Promise((resolve) => setTimeout(resolve, 20));
    active -= 1;
    if (profile.name === "hap-fixture") throw new Error("fixture failed");
    return { status: "passed" };
  });
  assert.equal(maximum, 1);
  assert.deepEqual(seen, ["base", "hap-fixture"]);
  assert.equal(results.get("hap-fixture").status, "failed");
  assert.equal(results.has("operator-code-fixture"), false);
});

test("unitized base replaces the monolithic Go descriptor", () => {
  const base = coverageProfiles.find((profile) => profile.name === "base");
  const dedicated = coverageProfiles.find((profile) => profile.name === "uci");
  assert.deepEqual(profileDescriptor(base).effective_argv, []);
  assert.deepEqual(testInventoryArguments(base), []);
  assert.equal(profileDescriptor(dedicated).effective_argv[2], "-p=1");
  assert.equal(testInventoryArguments(dedicated)[1], "-p=1");
  assert.equal(base.unitized, true);
  assert.notEqual(fingerprintProfile(base, candidate(), { sha256: "environment" }), fingerprintProfile({ ...base, packageConcurrency: 2 }, candidate(), { sha256: "environment" }));
});

test("profile budget expiry is retained as timed_out with an explicit budget reason", () => {
  const deadline = new Deadline({ overallTimeout: 1000, coverageTimeout: 60, profileTimeout: 30, scannerTimeout: 60, qualityGateTimeout: 60 });
  let expiry;
  assert.throws(() => profileTimeout(deadline, Date.now() - 1, 1000), (error) => {
    expiry = error;
    return /profile budget exhausted/.test(error.message);
  });
  assert.deepEqual(classifyProfileFailure(expiry, { signal: new AbortController().signal }), { status: "timed_out", reason: "profile_budget_exhausted" });
});

test("inventory budget expiry retains a timed-out profile attempt", async () => {
  const directory = temporaryDirectory();
  try {
    const base = coverageProfiles.find((profile) => profile.name === "base");
    const entry = { name: base.name, attempt: 1 };
    const campaign = { runDir: join(directory, "run"), manifest: { profiles: [entry] } };
    mkdirSync(campaign.runDir, { recursive: true });
    const deadline = new Deadline({ overallTimeout: 1000, coverageTimeout: 60, profileTimeout: 30, scannerTimeout: 60, qualityGateTimeout: 60 });
    const progress = { activate() { }, meaningful() { }, deactivate() { }, location() { }, semantic() { }, output() { }, completed: 0 };
    const execution = { signal: new AbortController().signal };
    await assert.rejects(
      runCoverageProfile("fake-go", base, campaign, entry, directory, {}, null, execution, deadline, progress, [], Date.now() + 30000, { repository_path: directory, inventory: [] }, {
        expectedTests: async () => profileTimeout(deadline, Date.now() - 1, 1000),
      }),
      /profile budget exhausted/,
    );
    assert.equal(entry.status, "timed_out");
    assert.equal(entry.failure_reason, "profile_budget_exhausted");
    assert.equal(entry.inventory.state, "failed");
    assert.equal(entry.inventory.failure_reason, "profile_budget_exhausted");
    assert.match(entry.failure, /profile budget exhausted/);
    assert.equal(JSON.parse(readFileSync(join(campaign.runDir, "manifest.json"), "utf8")).profiles[0].status, "timed_out");
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});

test("log writer streams ordinary test events while retaining split-secret redaction", () => {
  const directory = temporaryDirectory();
  try {
    const path = join(directory, "test-events.ndjson");
    const secret = "split-secret-token";
    const writer = createLogWriter(path, [secret]);
    writer.write('{"Action":"run","Package":"example/internal/books","Test":"TestExample"}\n');
    assert.match(readFileSync(path, "utf8"), /"Action":"run"/);
    writer.write(secret.slice(0, 7));
    assert.doesNotMatch(readFileSync(path, "utf8"), /split-secret-token|split-secret|token/);
    writer.write(secret.slice(7));
    writer.finish();
    const contents = readFileSync(path, "utf8");
    assert.doesNotMatch(contents, /split-secret-token|split-secret|token/);
    assert.match(contents, /\[REDACTED\]/);
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});

test("semantic Go lifecycle progress distinguishes transitions from noisy output", () => {
  const directory = temporaryDirectory();
  try {
    const now = { value: 0 };
    const { progress, events } = progressHarness(directory, now);
    const state = goEventState();
    const observe = (eventValue, transition) => {
      progress.location("base", eventValue.Package || null, eventValue.Test || null);
      if (transition) progress.semantic("base", transition);
    };
    progress.activate("base", 900000);
    consumeGoEvents('{"Action":"start","Package":"a/pkg"}\n', state, () => { }, observe);
    now.value = 120001;
    progress.output();
    assert.equal(progress.heartbeat().kind, "stalled");
    now.value = 120002;
    consumeGoEvents('{"Action":"run","Package":"b/pkg","Test":"TestInterleaved"}\n', state, () => { }, observe);
    assert.equal(progress.heartbeat().kind, "heartbeat");
    now.value = 240003;
    progress.output();
    assert.equal(progress.heartbeat().kind, "stalled");
    now.value = 240004;
    consumeGoEvents('{"Action":"pause","Package":"b/pkg","Test":"TestInterleaved"}\n', state, () => { }, observe);
    assert.equal(progress.heartbeat().kind, "heartbeat");
    now.value = 360005;
    consumeGoEvents('{"Action":"cont","Package":"b/pkg","Test":"TestInterleaved"}', state, () => { }, observe);
    finishGoEvents(state, () => { }, observe);
    assert.equal(progress.heartbeat().kind, "heartbeat");
    now.value = 480006;
    consumeGoEvents('{"Action":"pass","Package":"a/pkg"}', state, () => { }, observe);
    finishGoEvents(state, () => { }, observe);
    assert.equal(progress.heartbeat().kind, "heartbeat");
    assert.equal(progress.active.get("base").current_package, "a/pkg");
    assert.equal(progress.active.get("base").current_test, null);
    now.value = 480007;
    consumeGoEvents('{"Action":"skip","Package":"b/pkg","Test":"TestInterleaved"}\n', state, () => { }, observe);
    assert.equal(progress.heartbeat().kind, "heartbeat");
    now.value = 480008;
    consumeGoEvents('{"Action":"fail","Package":"c/pkg","Test":"TestFailure"}\n', state, () => { }, observe);
    assert.equal(progress.heartbeat().kind, "heartbeat");
    now.value = 600009;
    consumeGoEvents('{"Action":"fail","Package":"c/pkg","Test":"TestFailure"}\n', state, () => { }, observe);
    progress.output();
    assert.equal(progress.heartbeat().kind, "stalled");
    const last = events().at(-1);
    assert.equal(last.stall_reason, "no_semantic_transition_observed");
    assert.equal(last.last_output_age_ms, 0);
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});

test("one fixture sibling's lifecycle transition does not hide another profile's semantic idle age", () => {
  const directory = temporaryDirectory();
  try {
    const now = { value: 0 };
    const { progress } = progressHarness(directory, now);
    progress.activate("base", 900000);
    progress.activate("hap-fixture", 900000);
    now.value = 120001;
    progress.semantic("hap-fixture", { action: "run", package: "fixture/pkg", test: "TestFixture" });
    const heartbeat = progress.heartbeat();
    assert.equal(heartbeat.kind, "heartbeat");
    assert.equal(heartbeat.active_profiles.find((profile) => profile.profile === "base").semantic_idle, true);
    assert.equal(heartbeat.active_profiles.find((profile) => profile.profile === "hap-fixture").semantic_idle, false);
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});

test("cold collector reaches actual scheduling without an undefined pending-profile variable", async () => {
  const directory = temporaryDirectory();
  try {
    const campaign = {
      namespace: directory,
      runDir: join(directory, "run"),
      manifest: { run_id: "11111111-1111-4111-8111-111111111111", profiles: [], resources: { cleanup: { errors: [] } }, result: {} },
    };
    mkdirSync(campaign.runDir, { recursive: true });
    const environment = { values: { image_id: `sha256:${"a".repeat(64)}` }, sha256: "environment", testEnvironment: {} };
    const deadline = new Deadline({ overallTimeout: 1000, coverageTimeout: 60, profileTimeout: 30, scannerTimeout: 60, qualityGateTimeout: 60 });
    await assert.rejects(
      collectCoverage(campaign, { ...candidate(), repository_path: directory }, environment, { fresh: true, jobs: 1 }, { signal: new AbortController().signal }, deadline, { meaningful() { } }, { goCommand: "fake-go", dockerCommand: "fake-docker", profiles: [] }),
      /Coverage mode must be atomic, got none/,
    );
    assert.equal(campaign.manifest.result.coverage, "failed");
    assert.equal(JSON.parse(readFileSync(join(campaign.runDir, "manifest.json"), "utf8")).result.coverage, "failed");
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});

test("base-only retains package-unit evidence without projecting full coverage or scanner readiness", async () => {
  const directory = temporaryDirectory();
  try {
    const currentCandidate = { ...candidate(), repository_path: directory };
    const environment = { values: { image_id: `sha256:${"a".repeat(64)}` }, sha256: "environment", testEnvironment: {} };
    const packagePath = "example/core";
    const packageUnits = [
      { id: "base-race-core", phase: "race", importPath: packagePath, directory: "core", classification: "ordinary", core: true, coverpkg: [] },
      { id: "base-coverage-core", phase: "coverage", importPath: packagePath, directory: "core", classification: "ordinary", core: true, coverpkg: [packagePath] },
    ];
    const campaign = { namespace: directory, runId: "22222222-2222-4222-8222-222222222222", runDir: join(directory, "run"), manifest: { run_id: "22222222-2222-4222-8222-222222222222", candidate: currentCandidate, profiles: [], merged: { status: "pending" }, analysis: { state: "not_submitted" }, publication: { requested: false, state: "not_requested" }, resources: { cleanup: { errors: [] } }, result: { coverage: "incomplete", technical_gate: "incomplete", effect: "not_requested", disposition: "incomplete" } } };
    mkdirSync(campaign.runDir, { recursive: true });
    const options = parseOptions(["--mode", "coverage", "--base-only"]);
    const deadline = new Deadline({ overallTimeout: 1000, coverageTimeout: 60, profileTimeout: 30, scannerTimeout: 60, qualityGateTimeout: 60 });
    const progress = { completed: 0, reused: 0, activate() { }, meaningful() { }, deactivate() { }, location() { }, semantic() { }, output() { } };
    const unitRuntime = {
      expectedTests: async () => [{ package: packagePath, test: "TestExample" }],
      runProcess: async (_command, args, context) => {
        const coverage = args.find((argument) => argument.startsWith("-coverprofile="));
        if (coverage) write(join(context.cwd, coverage.slice("-coverprofile=".length)), "mode: atomic\ncore.go:1.1,1.2 1 1\n");
        context.onStdout(`{"Action":"run","Package":"${packagePath}","Test":"TestExample"}\n{"Action":"pass","Package":"${packagePath}","Test":"TestExample"}\n{"Action":"pass","Package":"${packagePath}"}\n`);
        return { stdout: "", stderr: "" };
      },
    };
    await runWithCampaign(campaign, currentCandidate, environment, options, { signal: new AbortController().signal, children: new Map() }, deadline, progress, { coverage: { goCommand: "fake-go", dockerCommand: "fake-docker", packageUnits, unitRuntime } });
    assert.equal(campaign.manifest.profiles.length, 1);
    assert.equal(campaign.manifest.profiles[0].status, "passed");
    assert.equal(campaign.manifest.profiles[0].units.length, 2);
    assert.equal(campaign.manifest.merged.status, "incomplete");
    assert.equal(campaign.manifest.result.coverage, "incomplete");
    assert.equal(campaign.manifest.result.disposition, "base_profile_ready");
    assert.equal(campaign.manifest.analysis.state, "not_submitted");
    assert.equal(campaign.manifest.publication.state, "not_requested");
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});

test("submitted CE tasks resume without invoking scanner submission and reports bind identity", async () => {
  let submissions = 0;
  let waits = 0;
  const action = await executeAnalysis(
    { state: "submitted", ce_task_id: "ce-123" },
    { submit: async () => { submissions += 1; }, wait: async () => { waits += 1; } },
  );
  assert.equal(action, "resumed");
  assert.equal(submissions, 0);
  assert.equal(waits, 1);
  await assert.rejects(
    executeAnalysis({ state: "submission_unknown" }, { submit: async () => { }, wait: async () => { } }),
    /automatic scanner resubmission is unsafe/,
  );

  const report = new Map([
    ["ceTaskId", "ce-123"],
    ["ceTaskUrl", "https://sonar.example/api/ce/task?id=ce-123"],
    ["dashboardUrl", "https://sonar.example/dashboard?id=thebtf_engram"],
    ["projectKey", "thebtf_engram"],
    ["serverUrl", "https://sonar.example"],
  ]);
  assert.equal(validateReport(report, "https://sonar.example").ce_task_id, "ce-123");
  report.set("projectKey", "other-project");
  assert.throws(() => validateReport(report, "https://sonar.example"), /intended Sonar server/);
});

test("owned cancellation terminates a descendant while preserving an unrelated sentinel", async () => {
  let rootPid = null;
  let descendantPid = null;
  let timeoutError = null;
  const sentinel = spawn(process.execPath, ["-e", "setInterval(() => {}, 1000)"], { stdio: "ignore" });
  try {
    await assert.rejects(
      runOwnedCommand(process.execPath, ["-e", "const { spawn } = require('node:child_process'); const child = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { stdio: ['ignore', 'pipe', 'ignore'] }); child.stdout.on('data', () => {}); console.log(child.pid); setInterval(() => {}, 1000);"], {
        timeoutMs: 100,
        onStart: (record) => { rootPid = record.child.pid; },
        onStdout: (chunk) => { descendantPid ||= Number(String(chunk).trim()); },
      }),
      (error) => {
        timeoutError = error;
        return /exceeded its budget/.test(error.message);
      },
    );
    assert.ok(rootPid);
    assert.ok(descendantPid);
    await new Promise((resolve) => setTimeout(resolve, 40));
    assert.deepEqual(classifyProfileFailure(timeoutError, { signal: new AbortController().signal }), { status: "timed_out", reason: "profile_budget_exhausted" });
    assert.throws(() => process.kill(rootPid, 0));
    assert.throws(() => process.kill(descendantPid, 0));
    assert.doesNotThrow(() => process.kill(sentinel.pid, 0));
  } finally {
    sentinel.kill();
  }
});

test("container cleanup receives an independent non-aborted signal", () => {
  const controller = new AbortController();
  controller.abort();
  assert.equal(cleanupExecution({ signal: controller.signal }).signal.aborted, false);
});

test("options preserve the default gate while rejecting unsafe mode combinations", () => {
  assert.equal(parseOptions([]).mode, "gate");
  assert.deepEqual(
    parseOptions(["--mode", "coverage", "--fresh", "--jobs", "1", "--overall-timeout", "4500"]),
    { ...parseOptions([]), mode: "coverage", fresh: true, jobs: 1, overallTimeout: 4500 },
  );
  assert.equal(parseOptions(["--mode", "coverage", "--base-only"]).baseOnly, true);
  assert.throws(() => parseOptions(["--base-only"]), /only valid in coverage mode/);
  assert.throws(() => parseOptions(["--mode", "scan", "--base-only"]), /only valid in coverage mode/);
  assert.throws(() => parseOptions(["--mode", "resume"]), /requires --run/);
  assert.throws(() => parseOptions(["--mode", "coverage", "--publish-status"]), /invalid in coverage/);
  assert.throws(() => parseOptions(["--jobs", "3"]), /must be 1 or 2/);
});

test("Docker port parsing has no option-parser dependency", () => {
  assert.equal(parseDockerPort("127.0.0.1:49153\n"), 49153);
  assert.throws(() => parseDockerPort("127.0.0.1:0\n"), /Docker did not report/);
});

test("resume-only lifecycle never submits a not-submitted campaign", async () => {
  let submissions = 0;
  await assert.rejects(
    executeAnalysis(
      { state: "not_submitted" },
      { resumeOnly: true, submit: async () => { submissions += 1; }, wait: async () => { } },
    ),
    /never submits a new analysis/,
  );
  assert.equal(submissions, 0);
});

test("lowercase Go events and full event evidence are required for reuse", () => {
  const directory = temporaryDirectory();
  try {
    const fixture = reusableFixture(directory);
    assert.ok(reusableProfile(fixture.record, fixture.profile, fixture.currentCandidate, fixture.currentEnvironment));
    write(join(directory, "profiles", "base", "attempt-1", "test-events.ndjson"), '{"Action":"Pass","Package":"example","Test":"TestExample"}\n');
    assert.equal(reusableProfile(fixture.record, fixture.profile, fixture.currentCandidate, fixture.currentEnvironment), null);
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});

test("Go JSON lower-case actions preserve package-qualified terminal evidence", () => {
  assert.deepEqual(
    summarizeGoEvents('{"Action":"run","Package":"example","Test":"TestExample"}\n{"Action":"pass","Package":"example","Test":"TestExample"}\n{"Action":"pass","Package":"example"}\n'),
    { passed_tests: ["example/TestExample"], passed_packages: ["example"], failed_tests: [], skipped_tests: [] },
  );
});

test("package-qualified event identities do not collapse duplicate test names", () => {
  assert.deepEqual(
    summarizeGoEvents('{"Action":"pass","Package":"a/pkg","Test":"TestSame"}\n{"Action":"pass","Package":"b/pkg","Test":"TestSame"}\n'),
    { passed_tests: ["a/pkg/TestSame", "b/pkg/TestSame"], passed_packages: ["a/pkg", "b/pkg"], failed_tests: [], skipped_tests: [] },
  );
});

test("report task identity and stream redaction reject mismatches and split secrets", () => {
  const report = new Map([
    ["ceTaskId", "ce-123"],
    ["ceTaskUrl", "https://sonar.example/api/ce/task?id=ce-999"],
    ["dashboardUrl", "https://sonar.example/dashboard?id=thebtf_engram"],
    ["projectKey", "thebtf_engram"],
    ["serverUrl", "https://sonar.example"],
  ]);
  assert.throws(() => validateReport(report, "https://sonar.example"), /intended Sonar server/);
  const credentialed = new Map([
    ["ceTaskId", "ce-123"],
    ["ceTaskUrl", "https://user:pass@sonar.example/api/ce/task?id=ce-123"],
    ["dashboardUrl", "https://sonar.example/dashboard?id=thebtf_engram"],
    ["projectKey", "thebtf_engram"],
    ["serverUrl", "https://sonar.example"],
  ]);
  assert.throws(() => validateReport(credentialed, "https://sonar.example"), /intended Sonar server/);
  const duplicateID = new Map([
    ["ceTaskId", "ce-123"],
    ["ceTaskUrl", "https://sonar.example/api/ce/task?id=ce-123&extra=1#fragment"],
    ["dashboardUrl", "https://sonar.example/dashboard?id=thebtf_engram"],
    ["projectKey", "thebtf_engram"],
    ["serverUrl", "https://sonar.example/other"],
  ]);
  assert.throws(() => validateReport(duplicateID, "https://sonar.example"), /intended Sonar server/);
  const secret = "split-secret-token";
  const text = redactChunks([`before ${secret.slice(0, 7)}`, `${secret.slice(7)} after`], [secret]);
  assert.doesNotMatch(text, /split-secret-token|split-secret|token/);
  assert.match(text, /\[REDACTED\]/);
});

test("phase deadlines are cumulative rather than refreshed per operation", () => {
  const deadline = new Deadline({ overallTimeout: 1000, coverageTimeout: 60, profileTimeout: 30, scannerTimeout: 60, qualityGateTimeout: 60 });
  deadline.begin("coverage");
  deadline.phaseDeadlines.coverage = Date.now() - 1;
  assert.throws(() => deadline.timeoutFor("profile", deadline.profile), /coverage budget exhausted/);
});

test("retained event evidence recomputes skips and failures instead of trusting counters", () => {
  const directory = temporaryDirectory();
  try {
    const fixture = reusableFixture(directory);
    const events = '{"Action":"pass","Package":"example","Test":"TestExample"}\n{"Action":"pass","Package":"example"}\n{"Action":"skip","Package":"example","Test":"TestUnexpected"}\n';
    const eventPath = join(directory, "profiles", "base", "attempt-1", "test-events.ndjson");
    write(eventPath, events);
    fixture.record.manifest.profiles[0].test_events = { path: "profiles/base/attempt-1/test-events.ndjson", sha256: sha256(events), bytes: Buffer.byteLength(events) };
    fixture.record.manifest.profiles[0].unexpected_skip_count = 0;
    assert.equal(reusableProfile(fixture.record, fixture.profile, fixture.currentCandidate, fixture.currentEnvironment), null);
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});

test("base retained evidence accepts only a source-declared conditional skip", () => {
  const directory = temporaryDirectory();
  try {
    const fixture = reusableFixture(directory);
    const base = coverageProfiles.find((profile) => profile.name === "base");
    const source = "internal/books/pipeline_test.go";
    const sourceText = 'func TestBooksStore_CreateStartsPending() { t.Skip("DATABASE_DSN not set, skipping books integration test") }';
    write(join(directory, source), sourceText);
    fixture.currentCandidate.repository_path = directory;
    fixture.currentCandidate.inventory = [{ path: source, type: "file", sha256: sha256(sourceText) }];
    const baseEntry = fixture.record.manifest.profiles[0];
    baseEntry.fingerprint = fingerprintProfile(base, fixture.currentCandidate, fixture.currentEnvironment);
    const packageName = "example/internal/books";
    const skippedEvents = `{"Action":"output","Package":"${packageName}","Test":"TestBooksStore_CreateStartsPending","Output":"DATABASE_DSN not set, skipping books integration test"}\n{"Action":"skip","Package":"${packageName}","Test":"TestBooksStore_CreateStartsPending"}\n{"Action":"pass","Package":"${packageName}"}\n`;
    write(join(directory, "profiles", "base", "attempt-1", "test-events.ndjson"), skippedEvents);
    baseEntry.expected_tests = [{ package: packageName, test: "TestBooksStore_CreateStartsPending" }];
    baseEntry.expected_test_inventory_sha256 = sha256(JSON.stringify(baseEntry.expected_tests));
    baseEntry.test_events = { path: "profiles/base/attempt-1/test-events.ndjson", sha256: sha256(skippedEvents), bytes: Buffer.byteLength(skippedEvents) };
    baseEntry.package_counts = { passed: 1, failed: 0, tests_passed: 0, tests_skipped: 1 };
    assert.ok(reusableProfile(fixture.record, base, fixture.currentCandidate, fixture.currentEnvironment));
    const reasonMismatch = skippedEvents.replace("DATABASE_DSN not set, skipping books integration test", "different conditional reason");
    write(join(directory, "profiles", "base", "attempt-1", "test-events.ndjson"), reasonMismatch);
    baseEntry.test_events = { path: "profiles/base/attempt-1/test-events.ndjson", sha256: sha256(reasonMismatch), bytes: Buffer.byteLength(reasonMismatch) };
    assert.equal(reusableProfile(fixture.record, base, fixture.currentCandidate, fixture.currentEnvironment), null);
    write(join(directory, "profiles", "base", "attempt-1", "test-events.ndjson"), skippedEvents);
    baseEntry.test_events = { path: "profiles/base/attempt-1/test-events.ndjson", sha256: sha256(skippedEvents), bytes: Buffer.byteLength(skippedEvents) };
    const siblingOnly = 'func TestBooksStore_CreateStartsPending() {} func TestSibling() { t.Skip("DATABASE_DSN not set, skipping books integration test") }';
    write(join(directory, source), siblingOnly);
    fixture.currentCandidate.inventory = [{ path: source, type: "file", sha256: sha256(siblingOnly) }];
    baseEntry.fingerprint = fingerprintProfile(base, fixture.currentCandidate, fixture.currentEnvironment);
    assert.equal(reusableProfile(fixture.record, base, fixture.currentCandidate, fixture.currentEnvironment), null);
    write(join(directory, source), "not a conditional skip");
    assert.equal(reusableProfile(fixture.record, base, fixture.currentCandidate, fixture.currentEnvironment), null);
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});

test("coverage composition retains exactly twenty-five profiles", () => {
  assert.equal(coverageProfiles.length, 25);
});

test("scan materializes retained coverage from its source campaign", () => {
  const repository = temporaryDirectory();
  try {
    const sourceID = "11111111-1111-4111-8111-111111111111";
    const scanID = "22222222-2222-4222-8222-222222222222";
    const sourceRun = join(repository, "runs", sourceID);
    const scanRun = join(repository, "runs", scanID);
    const coverage = "mode: atomic\nexample.go:1.1,1.2 1 1\n";
    const candidate = { repository_id: "repository", worktree_id: "worktree", head: "a".repeat(40), tree: "b".repeat(40), inputs_sha256: "inputs" };
    write(join(sourceRun, "coverage.out"), coverage);
    write(join(sourceRun, "manifest.json"), JSON.stringify({ schema_version: 2, run_id: sourceID, candidate, fingerprints: { coverage_environment: "environment" } }));
    mkdirSync(scanRun, { recursive: true });
    const materialized = materializeCoverage({
      runDir: scanRun,
      manifest: { run_id: scanID, candidate, fingerprints: { coverage_environment: "environment" }, merged: { source_run_id: sourceID, path: "coverage.out", sha256: sha256(coverage), bytes: Buffer.byteLength(coverage) } },
    }, { repository_path: repository });
    assert.equal(readFileSync(materialized, "utf8"), coverage);
    assert.throws(() => materializeCoverage({ runDir: scanRun, manifest: { run_id: scanID, candidate, fingerprints: { coverage_environment: "environment" }, merged: { source_run_id: "../escape", path: "coverage.out", sha256: sha256(coverage), bytes: Buffer.byteLength(coverage) } } }, { repository_path: repository }), /source run is invalid/);
  } finally {
    rmSync(repository, { recursive: true, force: true });
  }
});

test("stored report-task and lock evidence are revalidated atomically", () => {
  const directory = temporaryDirectory();
  try {
    const report = "ceTaskId=ce-123\nceTaskUrl=https://sonar.example/api/ce/task?id=ce-123\ndashboardUrl=https://sonar.example/dashboard?id=thebtf_engram\nprojectKey=thebtf_engram\nserverUrl=https://sonar.example\n";
    write(join(directory, "analysis", "report-task.txt"), report);
    assert.ok(validAnalysisEvidence({ runDir: directory, manifest: { analysis: { host: "https://sonar.example", ce_task_id: "ce-123", ce_task_url: "https://sonar.example/api/ce/task?id=ce-123", dashboard_url: "https://sonar.example/dashboard?id=thebtf_engram", report: { path: "analysis/report-task.txt", sha256: sha256(report), bytes: Buffer.byteLength(report) } } } }));
    const release = acquireLock(join(directory, "locks"), "11111111-1111-4111-8111-111111111111");
    assert.throws(() => acquireLock(join(directory, "locks"), "22222222-2222-4222-8222-222222222222"), /already owns/);
    release();
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});

test("fake CE and Quality Gate responses preserve exact task and analysis identity", async () => {
  const directory = temporaryDirectory();
  const requests = [];
  const server = createServer((request, response) => {
    requests.push(request.url);
    response.setHeader("content-type", "application/json");
    if (request.url === "/api/ce/task?id=ce-123") response.end(JSON.stringify({ task: { id: "ce-123", componentKey: "thebtf_engram", status: "SUCCESS", analysisId: "analysis-123" } }));
    else if (request.url === "/api/qualitygates/project_status?analysisId=analysis-123") response.end(JSON.stringify({ projectStatus: { status: "OK" } }));
    else { response.statusCode = 404; response.end("{}"); }
  });
  try {
    await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
    const host = `http://127.0.0.1:${server.address().port}`;
    const runDir = join(directory, "run");
    mkdirSync(runDir, { recursive: true });
    const campaign = { runDir, manifest: { run_id: "11111111-1111-4111-8111-111111111111", analysis: { state: "submitted", ce_task_id: "ce-123", host, project_key: "thebtf_engram", scm_revision: "a".repeat(40), attempts: [] }, result: {} } };
    const deadline = new Deadline({ overallTimeout: 1000, coverageTimeout: 60, profileTimeout: 30, scannerTimeout: 60, qualityGateTimeout: 60 });
    await waitForAnalysis(campaign, { head: "a".repeat(40) }, { host, token: "token" }, { signal: new AbortController().signal }, deadline, { meaningful() { } });
    assert.equal(campaign.manifest.analysis.analysis_id, "analysis-123");
    assert.equal(campaign.manifest.result.technical_gate, "passed");
    assert.deepEqual(requests, ["/api/ce/task?id=ce-123", "/api/qualitygates/project_status?analysisId=analysis-123"]);
  } finally {
    await new Promise((resolve) => server.close(resolve));
    rmSync(directory, { recursive: true, force: true });
  }
});

test("requested status failure cannot project a legacy PASS receipt", () => {
  const directory = temporaryDirectory();
  try {
    const campaign = { runDir: join(directory, "run"), manifest: { analysis: { state: "passed", analysis_id: "analysis", ce_task_url: "https://sonar.example/api/ce/task?id=ce", dashboard_url: "https://sonar.example/dashboard?id=thebtf_engram" }, merged: { sha256: "coverage" }, fingerprints: { analysis_inputs: "inputs" }, resources: { cleanup: { state: "complete", errors: [] } }, publication: { requested: true }, result: { technical_gate: "passed", effect: "failed" } } };
    mkdirSync(campaign.runDir, { recursive: true });
    writeFileSync(join(campaign.runDir, "manifest.json"), JSON.stringify(campaign.manifest));
    assert.throws(() => writeReceipt(campaign, { coordination_root: directory, head: "a".repeat(40) }, { host: "https://sonar.example" }), /requested status publication failed/);
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});

test("receipt manifest hash binds final manifest bytes", () => {
  const directory = temporaryDirectory();
  try {
    const campaign = { runDir: join(directory, "run"), manifest: { analysis: { state: "passed", ce_task_url: "https://sonar.example/api/ce/task?id=ce-123", dashboard_url: "https://sonar.example/dashboard?id=thebtf_engram", analysis_id: "analysis-123" }, merged: { sha256: "coverage" }, fingerprints: { analysis_inputs: "inputs" }, resources: { cleanup: { state: "complete", errors: [] } }, result: { technical_gate: "passed" } } };
    mkdirSync(campaign.runDir, { recursive: true });
    writeFileSync(join(campaign.runDir, "manifest.json"), JSON.stringify(campaign.manifest));
    const receiptPath = writeReceipt(campaign, { coordination_root: directory, head: "a".repeat(40) }, { host: "https://sonar.example" });
    const receipt = JSON.parse(readFileSync(receiptPath, "utf8"));
    assert.equal(receipt.manifest_sha256, sha256(readFileSync(join(campaign.runDir, "manifest.json"))));
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});

test("receipt refuses incomplete cleanup evidence", () => {
  const directory = temporaryDirectory();
  try {
    const campaign = { runDir: join(directory, "run"), manifest: { analysis: { state: "passed", analysis_id: "analysis", ce_task_url: "https://sonar.example/api/ce/task?id=ce", dashboard_url: "https://sonar.example/dashboard" }, merged: { sha256: "coverage" }, fingerprints: { analysis_inputs: "inputs" }, resources: { cleanup: { state: "failed", errors: ["owned child alive"] } }, result: { technical_gate: "passed" } } };
    mkdirSync(campaign.runDir, { recursive: true });
    writeFileSync(join(campaign.runDir, "manifest.json"), JSON.stringify(campaign.manifest));
    assert.throws(() => writeReceipt(campaign, { coordination_root: directory, head: "a".repeat(40) }, { host: "https://sonar.example" }), /cleanup is unresolved/);
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});
