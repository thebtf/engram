import { spawn } from "node:child_process";
import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";

import {
  Deadline,
  atomicReplace,
  assertUnitDedicatedOwnership,
  boundedCoverpkg,
  canonicalJson,
  classifyPackageUnit,
  collectCoverage,
  consumeGoEvents,
  coverageProfiles,
  fingerprintProfile,
  finishGoEvents,
  mergeCoverProfiles,
  normalizeCoverage,
  planPackageUnits,
  reusableProfile,
  runCoverageProfile,
  runOwnedCommand,
  schedulePackageUnits,
  sha256,
  sourceInventory,
  summarizeGoEvents,
  terminalPackageSummary,
} from "./run-sonarqube.mjs";

function temporaryDirectory() {
  return mkdtempSync(join(tmpdir(), "engram-sonar-package-units-"));
}

function write(path, contents) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, contents);
}

function delay(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

function replacementError(code) {
  return Object.assign(new Error(`${code} replacement failure`), { code });
}

test("Windows atomic replacement retries bounded transient sharing errors", () => {
  const waits = [];
  const failures = [replacementError("EPERM"), replacementError("EACCES"), replacementError("EBUSY")];
  let attempts = 0;
  atomicReplace("temporary", "destination", {
    isWindows: true,
    rename() {
      attempts += 1;
      const failure = failures.shift();
      if (failure) throw failure;
    },
    wait(milliseconds) { waits.push(milliseconds); },
  });
  assert.equal(attempts, 4);
  assert.deepEqual(waits, [25, 50, 100]);
});

test("Windows atomic replacement preserves the original error after bounded exhaustion", () => {
  const waits = [];
  const original = replacementError("EPERM");
  let attempts = 0;
  assert.throws(() => atomicReplace("temporary", "destination", {
    isWindows: true,
    rename() {
      attempts += 1;
      throw attempts === 1 ? original : replacementError("EPERM");
    },
    wait(milliseconds) { waits.push(milliseconds); },
  }), (error) => error === original);
  assert.equal(attempts, 4);
  assert.deepEqual(waits, [25, 50, 100]);
});

test("Windows atomic replacement does not retry non-transient failures", () => {
  const original = replacementError("EINVAL");
  let attempts = 0;
  assert.throws(() => atomicReplace("temporary", "destination", {
    isWindows: true,
    rename() {
      attempts += 1;
      throw original;
    },
    wait() { throw new Error("non-transient rename must not wait"); },
  }), (error) => error === original);
  assert.equal(attempts, 1);
});

function goEventState() {
  return {
    buffer: "",
    currentTest: null,
    currentPackage: null,
    passedTests: new Set(),
    skippedTests: new Map(),
    skipReasons: new Map(),
    failedTests: new Set(),
    startedTests: new Set(),
    passedPackages: new Set(),
    failedPackages: new Set(),
    lifecycle: new Set(),
  };
}

function campaign(namespace, number, currentCandidate) {
  const runId = `${String(number).padStart(8, "0")}-0000-4000-8000-${String(number).padStart(12, "0")}`;
  const runDir = join(namespace, "runs", runId);
  mkdirSync(runDir, { recursive: true });
  writeFileSync(join(runDir, "events.ndjson"), "");
  return {
    namespace,
    runDir,
    manifest: {
      schema_version: 2,
      run_id: runId,
      candidate: currentCandidate,
      profiles: [],
      merged: { status: "pending" },
      analysis: { state: "not_submitted" },
      publication: { requested: false, state: "not_requested" },
      resources: { cleanup: { errors: [] } },
      result: { coverage: "pending", technical_gate: "pending", effect: "not_requested", disposition: "pending" },
    },
  };
}

function candidate(repositoryPath) {
  const inventory = sourceInventory(repositoryPath);
  return {
    repository_path: repositoryPath,
    repository_id: "repository",
    worktree_id: "worktree",
    head: "a".repeat(40),
    tree: "b".repeat(40),
    inputs_sha256: inventory.sha256,
    inventory: inventory.entries,
  };
}

function deadline() {
  return new Deadline({ overallTimeout: 1000, coverageTimeout: 60, profileTimeout: 30, scannerTimeout: 60, qualityGateTimeout: 60 });
}

function progress() {
  let work = null;
  return {
    completed: 0,
    reused: 0,
    configuredWork: null,
    completedWork: [],
    configureWork(nextWork) {
      assert.deepEqual(Object.keys(nextWork).sort(), ["coverage", "dedicated", "overall", "race"]);
      for (const phase of Object.values(nextWork)) assert.deepEqual(Object.keys(phase).sort(), ["completed", "required", "reused"]);
      work = structuredClone(nextWork);
      this.configuredWork = structuredClone(nextWork);
    },
    complete(workPhase, { reused = false } = {}) {
      const phase = work?.[workPhase];
      assert.ok(phase, `unknown work phase ${workPhase}`);
      assert.ok(phase.completed < phase.required, `${workPhase} exceeds its declared denominator`);
      assert.ok(work.overall.completed < work.overall.required, "overall work exceeds its declared denominator");
      phase.completed += 1;
      work.overall.completed += 1;
      if (reused) {
        phase.reused += 1;
        work.overall.reused += 1;
      }
      this.completedWork.push({ workPhase, reused });
    },
    activate() { },
    meaningful() { },
    deactivate() { },
    location() { },
    semantic() { },
    output() { },
  };
}

function packages(alphaImports = []) {
  return [
    { importPath: "example/internal/alpha", directory: "internal/alpha", imports: alphaImports, testGoFiles: ["alpha_test.go"], xTestGoFiles: [] },
    { importPath: "example/internal/beta", directory: "internal/beta", imports: [], testGoFiles: ["beta_test.go"], xTestGoFiles: [] },
  ];
}

function coverageEnvironment() {
  return {
    sha256: "test-environment",
    values: { image_id: `sha256:${"f".repeat(64)}` },
    testEnvironment: {},
  };
}

async function collectBaseUnits(currentCampaign, currentCandidate, units, calls) {
  await collectCoverage(
    currentCampaign,
    currentCandidate,
    coverageEnvironment(),
    { baseOnly: true, fresh: false, jobs: 2 },
    { signal: new AbortController().signal, children: new Map() },
    deadline(),
    progress(),
    {
      goCommand: "fake-go",
      dockerCommand: "fake-docker",
      profiles: [coverageProfiles.find((profile) => profile.name === "base")],
      packageUnits: units,
      unitRuntime: {
        expectedTests: async (_command, profile) => [{ package: profile.target, test: "TestPackage" }],
        runProcess: async (_command, args, context) => {
          calls.push(args);
          const coverageArgument = args.find((argument) => argument.startsWith("-coverprofile="));
          if (coverageArgument) {
            write(join(context.cwd, coverageArgument.slice("-coverprofile=".length)), "mode: atomic\ninternal/unit.go:1.1,1.2 1 1\n");
          }
          const packageName = args.find((argument) => argument.startsWith("example/"));
          context.onStdout(`{"Action":"run","Package":"${packageName}","Test":"TestPackage"}\n{"Action":"pass","Package":"${packageName}","Test":"TestPackage"}\n{"Action":"pass","Package":"${packageName}"}\n`);
          return { code: 0, stdout: "", stderr: "" };
        },
      },
    },
  );
}

function profileReuseRecord(root, currentCandidate, expectedTests, events, entry = {}) {
  const profile = coverageProfiles.find((candidateProfile) => candidateProfile.name === "base");
  const environment = { sha256: "test-environment" };
  const coverage = "mode: atomic\ninternal/unit.go:1.1,1.2 1 1\n";
  write(join(root, "artifacts", "coverage.out"), coverage);
  write(join(root, "artifacts", "events.ndjson"), events);
  return {
    profile,
    environment,
    record: {
      runDir: root,
      manifest: {
        schema_version: 2,
        run_id: "11111111-1111-4111-8111-111111111111",
        candidate: currentCandidate,
        profiles: [{
          name: "base",
          status: "passed",
          fingerprint: fingerprintProfile(profile, currentCandidate, environment),
          coverage: { path: "artifacts/coverage.out", sha256: sha256(coverage), bytes: Buffer.byteLength(coverage) },
          test_events: { path: "artifacts/events.ndjson", sha256: sha256(events), bytes: Buffer.byteLength(events) },
          expected_tests: expectedTests,
          expected_test_inventory_sha256: sha256(JSON.stringify(expectedTests)),
          package_counts: { passed: 1, failed: 0, tests_passed: 0, tests_skipped: 1 },
          ...entry,
        }],
      },
    },
  };
}

test("interleaved same-name Go tests retain package-qualified terminal events", () => {
  const events = [
    '{"Action":"run","Package":"example/a","Test":"TestSame"}',
    '{"Action":"run","Package":"example/b","Test":"TestSame"}',
    '{"Action":"pass","Package":"example/b","Test":"TestSame"}',
    '{"Action":"pass","Package":"example/a","Test":"TestSame"}',
    '{"Action":"pass","Package":"example/a"}',
    '{"Action":"pass","Package":"example/b"}',
  ].join("\n");
  const state = goEventState();
  const retained = [];
  consumeGoEvents(events.slice(0, events.indexOf("example/b")), state, (line) => retained.push(line));
  consumeGoEvents(events.slice(events.indexOf("example/b")), state, (line) => retained.push(line));
  finishGoEvents(state, (line) => retained.push(line));

  assert.deepEqual([...state.passedTests].sort(), ["example/a/TestSame", "example/b/TestSame"]);
  assert.deepEqual([...state.passedPackages].sort(), ["example/a", "example/b"]);
  assert.equal(retained.length, 6);
  assert.deepEqual(
    summarizeGoEvents(events),
    { passed_tests: ["example/b/TestSame", "example/a/TestSame"], passed_packages: ["example/a", "example/b"], failed_tests: [], skipped_tests: [] },
  );
});

test("package plans bound coverage to direct first-party imports and classify external-state units", () => {
  const planned = planPackageUnits([
    { importPath: "example/internal/alpha", directory: "internal/alpha", imports: ["example/internal/beta", "example/internal/missing", "net/http", "example/internal/alpha"], testGoFiles: ["alpha_test.go"], xTestGoFiles: [] },
    { importPath: "example/internal/beta", directory: "internal/beta", imports: [], testGoFiles: [], xTestGoFiles: ["beta_external_test.go"] },
    { importPath: "example/internal/db/gorm", directory: "internal/db/gorm", imports: [], testGoFiles: ["store_test.go"], xTestGoFiles: [] },
    { importPath: "example/internal/no-tests", directory: "internal/no-tests", imports: [], testGoFiles: [], xTestGoFiles: [] },
  ]);
  const alphaCoverage = planned.find((unit) => unit.phase === "coverage" && unit.importPath === "example/internal/alpha");
  const alphaRace = planned.find((unit) => unit.phase === "race" && unit.importPath === "example/internal/alpha");

  assert.deepEqual(alphaCoverage.coverpkg, ["example/internal/alpha", "example/internal/beta"]);
  assert.deepEqual(alphaRace.coverpkg, []);
  assert.equal(planned.some((unit) => unit.importPath === "example/internal/no-tests"), false);
  assert.equal(planned.find((unit) => unit.importPath === "example/internal/db/gorm").classification, "serial");
  assert.equal(classifyPackageUnit({ importPath: "example/pkg/fast", serial: true }), "serial");
  assert.equal(classifyPackageUnit("example/pkg/fast"), "ordinary");
  assert.deepEqual(
    boundedCoverpkg("example/internal/alpha", ["example/internal/beta", "example/internal/alpha", "net/http", "example/internal/beta"], ["example/internal/alpha", "example/internal/beta", "example/internal/unrelated"]),
    ["example/internal/alpha", "example/internal/beta"],
  );
});

test("scheduler finishes all race units before coverage, limits ordinary work to two, and serializes external state", async () => {
  const units = [
    { id: "race-serial-a", phase: "race", classification: "serial" },
    { id: "race-ordinary-a", phase: "race", classification: "ordinary" },
    { id: "race-ordinary-b", phase: "race", classification: "ordinary" },
    { id: "race-ordinary-c", phase: "race", classification: "ordinary" },
    { id: "coverage-serial-a", phase: "coverage", classification: "serial" },
    { id: "coverage-ordinary-a", phase: "coverage", classification: "ordinary" },
    { id: "coverage-ordinary-b", phase: "coverage", classification: "ordinary" },
  ];
  const trace = [];
  let ordinaryActive = 0;
  let serialActive = 0;
  let maximumOrdinary = 0;
  let maximumSerial = 0;
  const outcomes = await schedulePackageUnits(units, 2, async (unit) => {
    trace.push(`${unit.phase}:start:${unit.id}`);
    if (unit.classification === "ordinary") {
      ordinaryActive += 1;
      maximumOrdinary = Math.max(maximumOrdinary, ordinaryActive);
    } else {
      serialActive += 1;
      maximumSerial = Math.max(maximumSerial, serialActive);
    }
    await delay(unit.classification === "ordinary" ? 15 : 5);
    if (unit.classification === "ordinary") ordinaryActive -= 1;
    else serialActive -= 1;
    trace.push(`${unit.phase}:end:${unit.id}`);
    return { status: "passed" };
  });

  const firstCoverageStart = trace.findIndex((item) => item.startsWith("coverage:start:"));
  const finalRaceEnd = Math.max(...trace.map((item, index) => item.startsWith("race:end:") ? index : -1));
  assert.ok(firstCoverageStart > finalRaceEnd, trace.join(", "));
  assert.equal(maximumOrdinary, 2);
  assert.equal(maximumSerial, 1);
  assert.deepEqual([...outcomes.values()].map((outcome) => outcome.status), Array(units.length).fill("passed"));
});

test("base package units retain race terminal evidence, coverage artifacts, reuse only exact inputs, and never claim complete coverage", async () => {
  const root = temporaryDirectory();
  try {
    const repository = join(root, "repository");
    const namespace = join(root, "namespace");
    write(join(repository, "go.mod"), "module example\n");
    write(join(repository, "internal/alpha/alpha.go"), "package alpha\n");
    write(join(repository, "internal/alpha/alpha_test.go"), "package alpha\n");
    write(join(repository, "internal/beta/beta.go"), "package beta\n");
    write(join(repository, "internal/beta/beta_test.go"), "package beta\n");
    const units = planPackageUnits(packages());
    const initialCandidate = candidate(repository);
    const first = campaign(namespace, 1, initialCandidate);
    first.manifest.fingerprints = { coverage_environment: coverageEnvironment().sha256 };
    const firstCalls = [];
    await collectBaseUnits(first, initialCandidate, units, firstCalls);

    const base = first.manifest.profiles.find((entry) => entry.name === "base");
    const raceEntries = base.units.filter((entry) => entry.unit.phase === "race");
    const coverageEntries = base.units.filter((entry) => entry.unit.phase === "coverage");
    assert.equal(firstCalls.length, units.length);
    assert.equal(base.status, "passed");
    assert.ok(raceEntries.every((entry) => entry.status === "passed" && entry.coverage.status === "not_applicable"));
    assert.ok(coverageEntries.every((entry) => entry.status === "passed" && entry.coverage.bytes > 0 && /^mode: atomic\r?\n/.test(readFileSync(join(first.runDir, entry.coverage.path), "utf8"))));
    assert.deepEqual(first.manifest.merged, { status: "incomplete", reason: "base_only", selected_profiles: 1, required_profiles: coverageProfiles.length, selected_units: units.length });
    assert.equal(first.manifest.result.coverage, "incomplete");

    const exact = campaign(namespace, 2, initialCandidate);
    const exactCalls = [];
    await collectBaseUnits(exact, initialCandidate, units, exactCalls);
    assert.equal(exactCalls.length, 0);
    assert.ok(exact.manifest.profiles[0].units.every((entry) => entry.source_run_id === first.manifest.run_id));
    assert.doesNotThrow(() => assertUnitDedicatedOwnership(exact.manifest.profiles[0].units, { runDir: exact.runDir, manifest: exact.manifest }, initialCandidate, coverageEnvironment()));
    exact.manifest.profiles[0].units[0].source_run_id = "33333333-3333-4333-8333-333333333333";
    assert.throws(() => assertUnitDedicatedOwnership(exact.manifest.profiles[0].units, { runDir: exact.runDir, manifest: exact.manifest }, initialCandidate, coverageEnvironment()), /admissible test evidence/);

    for (const [kind, path] of [["code", "internal/alpha/alpha.go"], ["test", "internal/alpha/alpha_test.go"], ["dependency", "go.mod"]]) {
      write(join(repository, path), `changed ${kind}\n`);
      const changedCandidate = candidate(repository);
      const changed = campaign(namespace, 3 + firstCalls.length, changedCandidate);
      const changedCalls = [];
      await collectBaseUnits(changed, changedCandidate, units, changedCalls);
      assert.equal(changedCalls.length, units.length, kind);
    }

    const descriptorCandidate = candidate(repository);
    const descriptorFirst = campaign(namespace, 10, descriptorCandidate);
    const descriptorFirstCalls = [];
    await collectBaseUnits(descriptorFirst, descriptorCandidate, units, descriptorFirstCalls);
    const descriptorChangedUnits = planPackageUnits(packages(["example/internal/beta"]));
    const descriptorSecond = campaign(namespace, 11, descriptorCandidate);
    const descriptorSecondCalls = [];
    await collectBaseUnits(descriptorSecond, descriptorCandidate, descriptorChangedUnits, descriptorSecondCalls);
    assert.equal(descriptorSecondCalls.length, 1);
    assert.match(descriptorSecondCalls[0].join(" "), /-coverpkg=example\/internal\/alpha,example\/internal\/beta/);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("mandatory skipped tests remain obligations and conditional skips bind exact package, test, source, and reason", () => {
  const root = temporaryDirectory();
  try {
    const requiredRepository = join(root, "required");
    write(join(requiredRepository, "go.mod"), "module example\n");
    write(join(requiredRepository, "internal/db/gorm/store_test.go"), 'func TestUCIRequired() { t.Skip("conditional setup") }');
    const requiredCandidate = candidate(requiredRepository);
    const requiredEvents = '{"Action":"output","Package":"example/internal/db/gorm","Test":"TestUCIRequired","Output":"conditional setup"}\n{"Action":"skip","Package":"example/internal/db/gorm","Test":"TestUCIRequired"}\n{"Action":"pass","Package":"example/internal/db/gorm"}\n';
    const required = profileReuseRecord(join(root, "required-evidence"), requiredCandidate, [{ package: "example/internal/db/gorm", test: "TestUCIRequired" }], requiredEvents);
    assert.equal(reusableProfile(required.record, required.profile, requiredCandidate, required.environment), null);

    const conditionalCases = [
      {
        name: "valid",
        sourcePath: "internal/books/pipeline_test.go",
        source: 'func TestTarget() { t.Skip("service unavailable") }',
        packageName: "example/internal/books",
        reason: "service unavailable",
        reusable: true,
      },
      {
        name: "wrong package source",
        sourcePath: "internal/other/pipeline_test.go",
        source: 'func TestTarget() { t.Skip("service unavailable") }',
        packageName: "example/internal/books",
        reason: "service unavailable",
        reusable: false,
      },
      {
        name: "wrong test source",
        sourcePath: "internal/books/pipeline_test.go",
        source: 'func TestSibling() { t.Skip("service unavailable") }',
        packageName: "example/internal/books",
        reason: "service unavailable",
        reusable: false,
      },
      {
        name: "wrong source reason",
        sourcePath: "internal/books/pipeline_test.go",
        source: 'func TestTarget() { t.Skip("service unavailable") }',
        packageName: "example/internal/books",
        reason: "different reason",
        reusable: false,
      },
    ];
    for (const testCase of conditionalCases) {
      const repository = join(root, testCase.name.replaceAll(" ", "-"));
      write(join(repository, "go.mod"), "module example\n");
      write(join(repository, testCase.sourcePath), testCase.source);
      const currentCandidate = candidate(repository);
      const events = `{"Action":"output","Package":"${testCase.packageName}","Test":"TestTarget","Output":"${testCase.reason}"}\n{"Action":"skip","Package":"${testCase.packageName}","Test":"TestTarget"}\n{"Action":"pass","Package":"${testCase.packageName}"}\n`;
      const fixture = profileReuseRecord(join(root, `${testCase.name}-evidence`), currentCandidate, [{ package: testCase.packageName, test: "TestTarget" }], events);
      assert.equal(Boolean(reusableProfile(fixture.record, fixture.profile, currentCandidate, fixture.environment)), testCase.reusable, testCase.name);
    }
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("cancellation and timeout preserve completed unit results while killing only their owned process trees", async () => {
  const sentinel = spawn(process.execPath, ["-e", "setInterval(() => {}, 1000)"], { stdio: "ignore" });
  const units = [
    { id: "completed", phase: "race", classification: "ordinary" },
    { id: "timed-out", phase: "race", classification: "ordinary" },
  ];
  let rootPid = null;
  let descendantPid = null;
  try {
    const outcomes = await schedulePackageUnits(units, 1, async (unit) => {
      if (unit.id === "completed") return { status: "passed" };
      let timeoutError;
      await assert.rejects(
        runOwnedCommand(process.execPath, ["-e", "const { spawn } = require('node:child_process'); const child = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { stdio: ['ignore', 'pipe', 'ignore'] }); console.log(child.pid); setInterval(() => {}, 1000);"], {
          timeoutMs: 100,
          onStart: (record) => { rootPid = record.child.pid; },
          onStdout: (chunk) => { descendantPid ||= Number(String(chunk).trim()); },
        }),
        (error) => {
          timeoutError = error;
          return /exceeded its budget/.test(error.message);
        },
      );
      assert.equal(timeoutError.constructor.name, "BudgetError");
      return { status: "timed_out" };
    });
    await delay(40);
    assert.deepEqual([...outcomes.values()].map((outcome) => outcome.status), ["passed", "timed_out"]);
    assert.deepEqual(terminalPackageSummary([
      { unit: units[0], status: "passed" },
      { unit: units[1], status: "timed_out" },
    ]), { total: 2, race: 2, coverage: 0, passed: 1, failed: 0, timed_out: 1, cancelled: 0, pending: 0 });
    assert.ok(rootPid);
    assert.ok(descendantPid);
    assert.throws(() => process.kill(rootPid, 0));
    assert.throws(() => process.kill(descendantPid, 0));
    assert.doesNotThrow(() => process.kill(sentinel.pid, 0));

    const controller = new AbortController();
    let cancelledRoot = null;
    await assert.rejects(
      runOwnedCommand(process.execPath, ["-e", "setInterval(() => {}, 1000)"], {
        signal: controller.signal,
        onStart: (record) => {
          cancelledRoot = record.child.pid;
          setTimeout(() => controller.abort(new Error("test cancellation")), 25);
        },
      }),
      /test cancellation/,
    );
    await delay(40);
    assert.throws(() => process.kill(cancelledRoot, 0));
    assert.doesNotThrow(() => process.kill(sentinel.pid, 0));
  } finally {
    sentinel.kill();
  }
});

test("coverage normalization canonicalizes Windows and Unix blocks and keeps each maximum hit count", () => {
  const normalized = normalizeCoverage([
    { source: "unix", contents: "mode: atomic\nexample/internal/alpha/file.go:1.1,2.2 1 1\n" },
    { source: "windows", contents: "mode: atomic\nC:\\work\\engram\\internal\\beta\\file.go:3.1,4.2 2 4\nexample/internal/alpha/file.go:1.1,2.2 1 9\n" },
  ]);
  assert.match(normalized, /^mode: atomic\n/);
  assert.match(normalized, /example\/internal\/alpha\/file\.go:1\.1,2\.2 1 9/);
  assert.match(normalized, /C:\\work\\engram\\internal\\beta\\file\.go:3\.1,4\.2 2 4/);
  assert.equal(normalized.match(/example\/internal\/alpha\/file\.go:1\.1,2\.2/g).length, 1);
});

test("coverage normalization preserves Go zero-statement point blocks but rejects reversed ranges", () => {
  const generated = "github.com/thebtf/engram/cmd/engram/main.go:580.18,580.18 0 1";
  const coverable = "github.com/thebtf/engram/cmd/engram/main.go:581.1,581.2 1 1";
  assert.equal(normalizeCoverage([
    { source: "generated", contents: `mode: atomic\n${generated}\n` },
    { source: "coverable", contents: `mode: atomic\n${coverable}\n` },
  ]), `mode: atomic\n${generated}\n${coverable}\n`);
  assert.throws(() => normalizeCoverage([
    { source: "reversed", contents: "mode: atomic\ngithub.com/thebtf/engram/cmd/engram/main.go:580.18,580.17 0 1\n" },
    { source: "coverable", contents: `mode: atomic\n${coverable}\n` },
  ]), /Invalid coverprofile block/);
  assert.throws(() => normalizeCoverage([{ source: "zero", contents: `mode: atomic\n${generated}\n` }]), /No Go coverage blocks/);
});

test("coverage normalization fails closed on conflicting statement counts", () => {
  assert.throws(
    () => normalizeCoverage([
      { source: "first", contents: "mode: atomic\nexample/internal/alpha/file.go:1.1,2.2 1 1\n" },
      { source: "conflict", contents: "mode: atomic\nexample/internal/alpha/file.go:1.1,2.2 2 1\n" },
    ]),
    /Conflicting coverprofile statement count/i,
  );
});

test("coverage normalization fails closed on corrupt or missing headers", () => {
  assert.throws(() => normalizeCoverage([{ source: "missing", contents: "example/file.go:1.1,2.2 1 1\n" }]), /Malformed coverprofile header/);
  assert.throws(() => normalizeCoverage([{ source: "corrupt", contents: "mode: atomic\nnot a coverage block\n" }]), /Malformed coverprofile block/);
  assert.throws(() => normalizeCoverage([{ source: "wrong-mode", contents: "mode: set\nexample/file.go:1.1,2.2 1 1\n" }]), /Coverage mode must be atomic/);
});

const r15SavedRun = "D:/Dev/engram/.agent/e/sonarqube/worktrees/634b6b14e6f9b058e723e998f9411b0357a430a06ba267336a6d6b712b27b8f9/runs/543a1e2a-2a95-4530-8c88-782824a71b3e";

function r15Event(action, packageName, testName, output = null) {
  return `${JSON.stringify({ Action: action, Package: packageName, ...(testName ? { Test: testName } : {}), ...(output === null ? {} : { Output: output }) })}\n`;
}

function r15Candidate(root, sources) {
  for (const [path, source] of sources) write(join(root, path), source);
  return {
    ...candidate(root),
    repository_path: root,
    inventory: sources.map(([path, source]) => ({ path, type: "file", sha256: sha256(source) })),
  };
}

function r15UnitProfile(unit, name = unit.id) {
  return {
    name,
    target: unit.importPath,
    race: true,
    packageConcurrency: 1,
    baseUnit: true,
    unitPhase: "race",
    workPhase: "race",
    unitDirectory: unit.directory,
  };
}

function r15Progress() {
  return { activate() { }, meaningful() { }, deactivate() { }, location() { }, semantic() { }, output() { }, complete() { } };
}

async function r15Execute(root, profile, currentCandidate, expected, events, options = {}) {
  const entry = { id: profile.name, name: profile.name, attempt: 1 };
  const currentCampaign = { runDir: join(root, "execution", profile.name), manifest: { profiles: [entry] } };
  mkdirSync(currentCampaign.runDir, { recursive: true });
  const execution = options.execution || { signal: new AbortController().signal };
  try {
    await runCoverageProfile("fake-go", profile, currentCampaign, entry, root, {}, null, execution, deadline(), r15Progress(), [], options.profileDeadline || Date.now() + 30_000, currentCandidate, {
      expectedTests: async () => expected,
      runProcess: async (_command, _args, context) => {
        if (options.failure) throw new Error(options.failure);
        const coverageArgument = _args.find((argument) => argument.startsWith("-coverprofile="));
        if (options.coverage !== undefined && coverageArgument) write(join(context.cwd, coverageArgument.slice("-coverprofile=".length)), options.coverage);
        context.onStdout(events);
        return { stdout: "", stderr: "" };
      },
    });
    return { passed: true, entry };
  } catch (error) {
    return { passed: false, entry, error };
  }
}

function r15OwnerEvidence(root, currentCandidate, environment, ownerConfig) {
  const config = typeof ownerConfig === "string" ? { state: ownerConfig } : ownerConfig;
  const owner = coverageProfiles.find((profile) => profile.name === "uci-installed");
  const packageName = config.packageName || "example/cmd/engram";
  const test = config.test || "TestUCIInstalledStandardClientsKeepDirtyViewsIsolated";
  const events = config.state === "passed"
    ? `${r15Event("pass", packageName, test)}${r15Event("pass", packageName, null)}`
    : `${r15Event("skip", packageName, test)}${r15Event("pass", packageName, null)}`;
  const coverage = "mode: atomic\nowner.go:1.1,1.2 1 1\n";
  write(join(root, "owner-events.ndjson"), events);
  write(join(root, "owner-coverage.out"), coverage);
  return {
    name: owner.name,
    status: config.state === "pending" ? "pending" : "passed",
    fingerprint: fingerprintProfile(owner, currentCandidate, environment),
    coverage: { path: "owner-coverage.out", sha256: sha256(coverage), bytes: Buffer.byteLength(coverage) },
    test_events: { path: "owner-events.ndjson", sha256: sha256(events), bytes: Buffer.byteLength(events) },
    expected_tests: [{ package: packageName, test }],
    expected_test_inventory_sha256: sha256(canonicalJson([{ package: packageName, test }])),
    package_counts: { passed: 1, failed: 0, tests_passed: config.state === "passed" ? 1 : 0, tests_skipped: config.state === "passed" ? 0 : 1 },
  };
}

function r15Retained(root, currentCandidate, expected, events, counts, owner = null, status = "passed", coverageContent = "mode: atomic\nfixture.go:1.1,1.2 1 1\n") {
  const environment = { sha256: "r15-environment" };
  const profile = coverageProfiles.find((item) => item.name === "base");
  const sourceRunId = "11111111-1111-4111-8111-111111111111";
  const reloadRunId = "22222222-2222-4222-8222-222222222222";
  const sourceDir = join(root, "retained", "runs", sourceRunId);
  const reloadDir = join(root, "retained", "runs", reloadRunId);
  const coverage = coverageContent;
  mkdirSync(sourceDir, { recursive: true });
  mkdirSync(reloadDir, { recursive: true });
  write(join(sourceDir, "events.ndjson"), events);
  write(join(sourceDir, "coverage.out"), coverage);
  const entry = {
    name: profile.name,
    status,
    fingerprint: fingerprintProfile(profile, currentCandidate, environment),
    coverage: { path: "coverage.out", sha256: sha256(coverage), bytes: Buffer.byteLength(coverage) },
    test_events: { path: "events.ndjson", sha256: sha256(events), bytes: Buffer.byteLength(events) },
    expected_tests: expected,
    expected_test_inventory_sha256: sha256(canonicalJson(expected)),
    package_counts: counts,
  };
  const owners = owner ? [r15OwnerEvidence(sourceDir, currentCandidate, environment, owner)] : [];
  const source = {
    schema_version: 2,
    run_id: sourceRunId,
    candidate: currentCandidate,
    fingerprints: { coverage_environment: environment.sha256 },
    profiles: [entry, ...owners],
  };
  write(join(sourceDir, "manifest.json"), JSON.stringify(source));
  const reloaded = {
    schema_version: 2,
    run_id: reloadRunId,
    candidate: currentCandidate,
    fingerprints: { coverage_environment: environment.sha256 },
    profiles: [{ ...entry, source_run_id: sourceRunId }],
  };
  return {
    retained: { runDir: sourceDir, manifest: source },
    reloaded: { runDir: reloadDir, manifest: reloaded },
    profile,
    environment,
  };
}

function r15Counts(events) {
  const summary = summarizeGoEvents(events);
  return {
    passed: summary.passed_packages.length,
    failed: 0,
    tests_passed: summary.passed_tests.length,
    tests_skipped: summary.skipped_tests.length,
  };
}

function r15Obligations(entry) {
  return (entry.allowed_skip_obligations || []).map(({ kind, required_profile: requiredProfile, test }) => ({
    ...(kind === undefined ? {} : { kind }),
    ...(requiredProfile === undefined ? {} : { required_profile: requiredProfile }),
    test,
  }));
}

test("R15 compact skip cases have an exact execution, retained, and reload/reuse matrix", async () => {
  const root = temporaryDirectory();
  try {
    const packageName = "example/cmd/engram";
    const unit = { id: "base-race-r15", importPath: packageName, directory: "cmd/engram" };
    const make = (name, testName, sources, events, owner = null) => ({ name, testName, sources, events, owner });
    const cases = [
      make("package and test pass", "TestPass", [["cmd/engram/pass_test.go", "func TestPass(t *testing.T) {}"]], `${r15Event("pass", packageName, "TestPass")}${r15Event("pass", packageName, null)}`),
      make("known conditional skip", "TestConditional", [["cmd/engram/conditional_test.go", 'func TestConditional(t *testing.T) { t.Skip("explicit conditional") }']], `${r15Event("output", packageName, "TestConditional", "explicit conditional")}${r15Event("skip", packageName, "TestConditional")}${r15Event("pass", packageName, null)}`),
      make("nested owner skip", "TestUCIInstalledStandardClientsKeepDirtyViewsIsolated/nested", [["cmd/engram/nested_test.go", 'func TestUCIInstalledStandardClientsKeepDirtyViewsIsolated(t *testing.T) { t.Run("nested", func(t *testing.T) { t.Skip("requires installed database") }) }']], `${r15Event("output", packageName, "TestUCIInstalledStandardClientsKeepDirtyViewsIsolated/nested", "requires installed database")}${r15Event("skip", packageName, "TestUCIInstalledStandardClientsKeepDirtyViewsIsolated/nested")}${r15Event("pass", packageName, null)}`, "passed"),
      make("neighboring helper", "TestHelper", [["cmd/engram/helper_test.go", "func TestHelper(t *testing.T) { skipFromNeighbor(t) }"], ["cmd/engram/skip_helper_test.go", 'func skipFromNeighbor(t *testing.T) { t.Skip("helper controlled") }']], `${r15Event("output", packageName, "TestHelper", "helper controlled")}${r15Event("skip", packageName, "TestHelper")}${r15Event("pass", packageName, null)}`),
      make("pending owner", "TestUCIInstalledStandardClientsKeepDirtyViewsIsolated", [["cmd/engram/pending_test.go", 'func TestUCIInstalledStandardClientsKeepDirtyViewsIsolated(t *testing.T) { t.Skip("requires installed database") }']], `${r15Event("output", packageName, "TestUCIInstalledStandardClientsKeepDirtyViewsIsolated", "requires installed database")}${r15Event("skip", packageName, "TestUCIInstalledStandardClientsKeepDirtyViewsIsolated")}${r15Event("pass", packageName, null)}`, "pending"),
      make("owner real pass", "TestUCIInstalledStandardClientsKeepDirtyViewsIsolated", [["cmd/engram/owner_test.go", 'func TestUCIInstalledStandardClientsKeepDirtyViewsIsolated(t *testing.T) { t.Skip("requires installed database") }']], `${r15Event("output", packageName, "TestUCIInstalledStandardClientsKeepDirtyViewsIsolated", "requires installed database")}${r15Event("skip", packageName, "TestUCIInstalledStandardClientsKeepDirtyViewsIsolated")}${r15Event("pass", packageName, null)}`, "passed"),
      make("owner skipped", "TestUCIInstalledStandardClientsKeepDirtyViewsIsolated", [["cmd/engram/owner_skipped_test.go", 'func TestUCIInstalledStandardClientsKeepDirtyViewsIsolated(t *testing.T) { t.Skip("requires installed database") }']], `${r15Event("output", packageName, "TestUCIInstalledStandardClientsKeepDirtyViewsIsolated", "requires installed database")}${r15Event("skip", packageName, "TestUCIInstalledStandardClientsKeepDirtyViewsIsolated")}${r15Event("pass", packageName, null)}`, "skipped"),
      make("unknown reason", "TestUnknown", [["cmd/engram/unknown_test.go", 'func TestUnknown(t *testing.T) { t.Skip("known reason") }']], `${r15Event("output", packageName, "TestUnknown", "forged reason")}${r15Event("skip", packageName, "TestUnknown")}${r15Event("pass", packageName, null)}`),
      make("test failure", "TestFailed", [["cmd/engram/failure_test.go", "func TestFailed(t *testing.T) {}"]], `${r15Event("fail", packageName, "TestFailed")}${r15Event("fail", packageName, null)}`),
      make("package failure", "TestPackageFailed", [["cmd/engram/package_failure_test.go", "func TestPackageFailed(t *testing.T) {}"]], `${r15Event("pass", packageName, "TestPackageFailed")}${r15Event("fail", packageName, null)}`),
      make("missing terminal package", "TestNoPackage", [["cmd/engram/no_package_test.go", "func TestNoPackage(t *testing.T) {}"]], r15Event("pass", packageName, "TestNoPackage")),
    ];
    const actual = [];
    for (const item of cases) {
      const caseRoot = join(root, item.name.replaceAll(" ", "-"));
      const currentCandidate = r15Candidate(caseRoot, item.sources);
      const expected = [{ package: packageName, test: item.testName }];
      const execution = await r15Execute(caseRoot, r15UnitProfile(unit), currentCandidate, expected, item.events);
      const retained = r15Retained(caseRoot, currentCandidate, expected, item.events, r15Counts(item.events), item.owner);
      actual.push({
        name: item.name,
        execution: execution.passed,
        retained: Boolean(reusableProfile(retained.retained, retained.profile, currentCandidate, retained.environment)),
        reload_reuse: Boolean(reusableProfile(retained.reloaded, retained.profile, currentCandidate, retained.environment)),
        obligations: r15Obligations(execution.entry),
      });
    }
    assert.deepEqual(actual, [
      { name: "package and test pass", execution: true, retained: true, reload_reuse: true, obligations: [] },
      { name: "known conditional skip", execution: true, retained: true, reload_reuse: true, obligations: [{ kind: "conditional", test: "TestConditional" }] },
      { name: "nested owner skip", execution: true, retained: true, reload_reuse: true, obligations: [{ required_profile: "uci-installed", test: "TestUCIInstalledStandardClientsKeepDirtyViewsIsolated/nested" }] },
      { name: "neighboring helper", execution: true, retained: true, reload_reuse: true, obligations: [{ kind: "conditional", test: "TestHelper" }] },
      { name: "pending owner", execution: true, retained: false, reload_reuse: false, obligations: [{ required_profile: "uci-installed", test: "TestUCIInstalledStandardClientsKeepDirtyViewsIsolated" }] },
      { name: "owner real pass", execution: true, retained: true, reload_reuse: true, obligations: [{ required_profile: "uci-installed", test: "TestUCIInstalledStandardClientsKeepDirtyViewsIsolated" }] },
      { name: "owner skipped", execution: true, retained: false, reload_reuse: false, obligations: [{ required_profile: "uci-installed", test: "TestUCIInstalledStandardClientsKeepDirtyViewsIsolated" }] },
      { name: "unknown reason", execution: false, retained: false, reload_reuse: false, obligations: [] },
      { name: "test failure", execution: false, retained: false, reload_reuse: false, obligations: [] },
      { name: "package failure", execution: false, retained: false, reload_reuse: false, obligations: [] },
      { name: "missing terminal package", execution: false, retained: false, reload_reuse: false, obligations: [] },
    ]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("R15 rejects timeout, cancellation, invalid hashes, and incomplete retained logs", async () => {
  const root = temporaryDirectory();
  try {
    const packageName = "example/internal/books";
    const currentCandidate = r15Candidate(root, [["internal/books/books_test.go", "func TestBook(t *testing.T) {}"]]);
    const expected = [{ package: packageName, test: "TestBook" }];
    const events = `${r15Event("pass", packageName, "TestBook")}${r15Event("pass", packageName, null)}`;
    const profile = r15UnitProfile({ id: "r15-timeout", importPath: packageName, directory: "internal/books" });
    const timedOut = await r15Execute(root, profile, currentCandidate, expected, events, { profileDeadline: Date.now() - 1 });
    const controller = new AbortController();
    controller.abort(new Error("r15 cancelled"));
    const cancelled = await r15Execute(root, { ...profile, name: "r15-cancelled" }, currentCandidate, expected, events, { execution: { signal: controller.signal }, failure: "cancelled command" });
    assert.equal(timedOut.entry.status, "timed_out");
    assert.equal(cancelled.entry.status, "cancelled");
    const retained = r15Retained(root, currentCandidate, expected, events, r15Counts(events));
    assert.ok(reusableProfile(retained.retained, retained.profile, currentCandidate, retained.environment));
    retained.retained.manifest.profiles[0].test_events.sha256 = "0".repeat(64);
    assert.equal(reusableProfile(retained.retained, retained.profile, currentCandidate, retained.environment), null);
    const incomplete = r15Retained(join(root, "incomplete"), currentCandidate, expected, r15Event("pass", packageName, "TestBook"), { passed: 0, failed: 0, tests_passed: 1, tests_skipped: 0 });
    assert.equal(reusableProfile(incomplete.retained, incomplete.profile, currentCandidate, incomplete.environment), null);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("R15 deferred owner does not block coverage, but genuine race failure does", async () => {
  const units = [
    { id: "race-deferred-owner", phase: "race", classification: "ordinary" },
    { id: "coverage-after-deferral", phase: "coverage", classification: "ordinary" },
  ];
  const launched = [];
  await schedulePackageUnits(units, 1, async (unit) => {
    launched.push(unit.id);
    return { status: "passed", deferred_owner: unit.id === "race-deferred-owner" ? "uci-installed" : null };
  });
  assert.deepEqual(launched, ["race-deferred-owner", "coverage-after-deferral"]);
  const blocked = [];
  const outcomes = await schedulePackageUnits(units, 1, async (unit) => {
    blocked.push(unit.id);
    if (unit.id === "race-deferred-owner") throw new Error("race failure");
    return { status: "passed" };
  });
  assert.equal(outcomes.get("race-deferred-owner").status, "failed");
  assert.deepEqual(blocked, ["race-deferred-owner"]);
});

test("R15 unit rename preserves the full test identity and skip policy", async () => {
  const root = temporaryDirectory();
  try {
    const packageName = "example/internal/books";
    const testName = "TestRenamed/nested";
    const currentCandidate = r15Candidate(root, [["internal/books/books_test.go", 'func TestRenamed(t *testing.T) { t.Run("nested", func(t *testing.T) { t.Skip("stable policy") }) }']]);
    const expected = [{ package: packageName, test: testName }];
    const events = `${r15Event("output", packageName, testName, "stable policy")}${r15Event("skip", packageName, testName)}${r15Event("pass", packageName, null)}`;
    const unit = { id: "base-race-original", importPath: packageName, directory: "internal/books" };
    const original = await r15Execute(root, r15UnitProfile(unit), currentCandidate, expected, events);
    const renamed = await r15Execute(root, r15UnitProfile({ ...unit, id: "base-race-renamed" }), currentCandidate, expected, events);
    assert.equal(original.passed, true);
    assert.equal(renamed.passed, true);
    assert.deepEqual(r15Obligations(renamed.entry), r15Obligations(original.entry));
    assert.deepEqual(r15Obligations(renamed.entry), [{ kind: "conditional", test: testName }]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("R15 replays every saved failed unit log through execution and retained reload/reuse", async () => {
  const root = temporaryDirectory();
  try {
    const saved = JSON.parse(readFileSync(join(r15SavedRun, "manifest.json"), "utf8"));
    const failedUnits = saved.profiles.find((profile) => profile.name === "base").units.filter((unit) => unit.status === "failed");
    assert.equal(failedUnits.length, 12);
    const actual = [];
    for (const unitEntry of failedUnits) {
      const events = readFileSync(join(r15SavedRun, unitEntry.test_events.path.replaceAll("\\", "/")), "utf8");
      const replayRoot = join(root, unitEntry.id);
      const replay = await r15Execute(replayRoot, r15UnitProfile(unitEntry.unit, `replay-${unitEntry.id}`), saved.candidate, unitEntry.expected_tests, events);
      const owner = unitEntry.unit.importPath === "github.com/thebtf/engram/cmd/engram"
        ? { state: "passed", packageName: unitEntry.unit.importPath, test: "TestUCIInstalledStandardClientsKeepDirtyViewsIsolated" }
        : null;
      const retained = r15Retained(replayRoot, saved.candidate, unitEntry.expected_tests, events, unitEntry.package_counts, owner);
      actual.push({
        id: unitEntry.id,
        execution: replay.passed,
        retained: Boolean(reusableProfile(retained.retained, retained.profile, saved.candidate, retained.environment)),
        reload_reuse: Boolean(reusableProfile(retained.reloaded, retained.profile, saved.candidate, retained.environment)),
      });
    }
    assert.deepEqual(actual, failedUnits.map((unit) => ({
      id: unit.id,
      execution: true,
      retained: unit.id !== "base-race-cdfda1f895eb3580" && unit.id !== "base-coverage-cdfda1f895eb3580",
      reload_reuse: unit.id !== "base-race-cdfda1f895eb3580" && unit.id !== "base-coverage-cdfda1f895eb3580",
    })));
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

const r15ZeroSavedRun = "D:/Dev/engram/.agent/e/sonarqube/worktrees/634b6b14e6f9b058e723e998f9411b0357a430a06ba267336a6d6b712b27b8f9/runs/89bfb55e-0bdb-467d-a5d8-08db3e6929b0";

function r15CoverageProfile(unit) {
  return {
    ...r15UnitProfile(unit),
    race: false,
    coverpkg: unit.coverpkg.join(","),
    unitPhase: "coverage",
    workPhase: "coverage",
  };
}

function r15ZeroEvents(packageName, test = "TestZero", action = "pass", marker = "coverage: [no statements]\n") {
  return `${r15Event("pass", packageName, test)}${r15Event("output", packageName, null, marker)}${r15Event(action, packageName, null)}`;
}

test("R15 replays saved header-only internal/version coverage as a completed zero-statement unit", async () => {
  const root = temporaryDirectory();
  try {
    const saved = JSON.parse(readFileSync(join(r15ZeroSavedRun, "manifest.json"), "utf8"));
    const unit = saved.profiles.find((profile) => profile.name === "base").units.find((entry) => entry.id === "base-coverage-c65178eeb44dc297");
    const header = readFileSync(join(r15ZeroSavedRun, unit.coverage.path.replaceAll("\\", "/")), "utf8");
    const events = readFileSync(join(r15ZeroSavedRun, unit.test_events.path.replaceAll("\\", "/")), "utf8");
    const execution = await r15Execute(root, r15CoverageProfile(unit.unit), saved.candidate, unit.expected_tests, events, { coverage: header });
    const retained = r15Retained(root, saved.candidate, unit.expected_tests, events, unit.package_counts, null, "passed", header);
    const ordinary = join(root, "ordinary.out");
    const merged = join(root, "merged.out");
    write(ordinary, "mode: atomic\ninternal/other/other.go:1.1,1.2 1 1\n");
    mergeCoverProfiles([join(retained.retained.runDir, "coverage.out"), ordinary], merged);
    assert.throws(() => mergeCoverProfiles([join(retained.retained.runDir, "coverage.out")], join(root, "all-zero.out")), /No Go coverage blocks/);
    assert.deepEqual({
      execution: execution.passed,
      retained: Boolean(reusableProfile(retained.retained, retained.profile, saved.candidate, retained.environment)),
      reload_reuse: Boolean(reusableProfile(retained.reloaded, retained.profile, saved.candidate, retained.environment)),
      merged: readFileSync(merged, "utf8"),
    }, {
      execution: true,
      retained: true,
      reload_reuse: true,
      merged: "mode: atomic\ninternal/other/other.go:1.1,1.2 1 1\n",
    });
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("R15 rejects unproven or malformed header-only coverage evidence", async () => {
  const root = temporaryDirectory();
  try {
    const packageName = "example/internal/version";
    const expected = [{ package: packageName, test: "TestZero" }];
    const unit = { id: "r15-zero-coverage", importPath: packageName, directory: "internal/version", coverpkg: [packageName] };
    const header = "mode: atomic\n";
    const cases = [
      { name: "missing no-statements marker", events: r15ZeroEvents(packageName, "TestZero", "pass", "coverage: [statements missing]\n"), coverage: header, execution: false },
      { name: "failed package", events: r15ZeroEvents(packageName, "TestZero", "fail"), coverage: header, execution: false },
      { name: "incomplete package", events: r15ZeroEvents(packageName).replace(r15Event("pass", packageName, null), ""), coverage: header, execution: false },
      { name: "malformed extra bytes", events: r15ZeroEvents(packageName), coverage: "mode: atomic\nmalformed\n", execution: false },
      { name: "wrong coverage mode", events: r15ZeroEvents(packageName), coverage: "mode: set\n", execution: false },
      { name: "wrong coverage hash", events: r15ZeroEvents(packageName), coverage: header, execution: true, wrongHash: true },
    ];
    const actual = [];
    for (const item of cases) {
      const caseRoot = join(root, item.name.replaceAll(" ", "-"));
      const currentCandidate = r15Candidate(caseRoot, [["internal/version/version_test.go", "func TestZero(t *testing.T) {}"]]);
      const execution = await r15Execute(caseRoot, r15CoverageProfile(unit), currentCandidate, expected, item.events, { coverage: item.coverage });
      const retained = r15Retained(caseRoot, currentCandidate, expected, item.events, { passed: 1, failed: 0, tests_passed: 1, tests_skipped: 0 }, null, "passed", item.coverage);
      if (item.wrongHash) {
        retained.retained.manifest.profiles[0].coverage.sha256 = "0".repeat(64);
        write(join(retained.retained.runDir, "manifest.json"), JSON.stringify(retained.retained.manifest));
      }
      actual.push({
        name: item.name,
        execution: execution.passed,
        retained: Boolean(reusableProfile(retained.retained, retained.profile, currentCandidate, retained.environment)),
        reload_reuse: Boolean(reusableProfile(retained.reloaded, retained.profile, currentCandidate, retained.environment)),
      });
    }
    assert.deepEqual(actual, cases.map((item) => ({ name: item.name, execution: item.execution, retained: false, reload_reuse: false })));
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
