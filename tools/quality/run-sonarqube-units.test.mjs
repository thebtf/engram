import { spawn } from "node:child_process";
import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";

import {
  Deadline,
  boundedCoverpkg,
  classifyPackageUnit,
  collectCoverage,
  consumeGoEvents,
  coverageProfiles,
  fingerprintProfile,
  finishGoEvents,
  normalizeCoverage,
  planPackageUnits,
  reusableProfile,
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
