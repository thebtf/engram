"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");

const {
  CAPABILITY,
  DISPOSITIONS,
  EXIT_CODES,
  GUARANTEES,
  PROOF_CLASSES,
  SCENARIO_IDS,
  SCHEMA,
  STATES,
  QualificationRecordError,
  boundaryFailure,
  buildCapabilityRecord,
  buildQualificationRecord,
  canonicalDigest,
  canonicalSerialize,
  deriveDisposition,
  deriveGaps,
  deriveReasons,
  exitFor,
  parseCapabilityRecord,
  parseQualificationRecord,
  sha256,
  verifyDigest,
} = require("./omp-capability-record.js");

const digest = (character) => character.repeat(64);
const gitObject = (character) => character.repeat(40);
const runtimeProof = () => ({ id: "runtime-a", class: "INSTALLED_RUNTIME", sha256: digest("a") });
const secondRuntimeProof = () => ({ id: "runtime-b", class: "INSTALLED_RUNTIME", sha256: digest("c") });
const sourceProof = () => ({ id: "source-a", class: "SOURCE_DIAGNOSTIC", sha256: digest("d") });

function proofRefs() {
  return [runtimeProof()];
}

function evidenceRefs() {
  return [
    { id: "artifact-a", class: "BUILT_ARTIFACT", sha256: digest("b") },
    runtimeProof(),
    secondRuntimeProof(),
    sourceProof(),
  ];
}

function counts(overrides = {}) {
  return {
    callbacks_expected: 0,
    callbacks_observed: 0,
    route_attempts_expected: 0,
    route_attempts_observed: 0,
    deliveries_expected: 0,
    deliveries_observed: 0,
    direct_fallback_attempts: 0,
    rediscoveries: 0,
    server_dispatches: 0,
    telemetry_attempts: 0,
    cleanup_residue: 0,
    ...overrides,
  };
}

function custody(overrides = {}) {
  return {
    extension_url_present: false,
    extension_token_present: false,
    extension_config_path_present: false,
    authorization_seen: true,
    child_hap_config_present: true,
    ...overrides,
  };
}

function invalidationSubcases() {
  return [
    "adapter_mismatch",
    "expiry",
    "process_exit",
    "project_keycard_rotation",
  ].map((id) => ({
    id,
    passed: true,
    route_attempts: 1,
    server_dispatches: 0,
    proof_refs: proofRefs(),
    reason_codes: [],
  }));
}

function scenarioFacts(id) {
  switch (id) {
    case "new_new_happy":
      return {
        counts: counts({
          callbacks_expected: 2, callbacks_observed: 2, route_attempts_expected: 4, route_attempts_observed: 4,
          deliveries_expected: 2, deliveries_observed: 2, server_dispatches: 4, telemetry_attempts: 2
        }),
        route_sequence: [
          "session_start:IDENTITY_REGISTRATION:OK",
          "session_start:SESSION_START_CONTEXT:OK",
          "before_agent_start:IDENTITY_REGISTRATION:OK",
          "before_agent_start:AMBIENT_CANDIDATES:OK",
        ],
        custody: custody(),
      };
    case "new_plugin_old_daemon":
      return { counts: counts({ callbacks_expected: 2, callbacks_observed: 2 }), route_sequence: [], custody: custody() };
    case "old_plugin_new_daemon":
      return {
        counts: counts({ callbacks_expected: 2, callbacks_observed: 2, deliveries_expected: 2, deliveries_observed: 2 }),
        route_sequence: [],
        custody: custody({
          extension_url_present: true, extension_token_present: true, extension_config_path_present: true,
          child_hap_config_present: false
        }),
      };
    case "relay_outage":
      return {
        counts: counts({ callbacks_expected: 2, callbacks_observed: 2, route_attempts_expected: 2, route_attempts_observed: 2 }),
        route_sequence: [
          "session_start:IDENTITY_REGISTRATION:NO_DELIVERY",
          "before_agent_start:IDENTITY_REGISTRATION:NO_DELIVERY",
        ],
        custody: custody(),
      };
    case "stale_generation":
      return {
        counts: counts({
          callbacks_expected: 2, callbacks_observed: 2, route_attempts_expected: 5, route_attempts_observed: 5,
          deliveries_expected: 2, deliveries_observed: 2, rediscoveries: 1, server_dispatches: 4, telemetry_attempts: 2
        }),
        route_sequence: [
          "session_start:IDENTITY_REGISTRATION:NO_DELIVERY",
          "session_start:IDENTITY_REGISTRATION:OK",
          "session_start:SESSION_START_CONTEXT:OK",
          "before_agent_start:IDENTITY_REGISTRATION:OK",
          "before_agent_start:AMBIENT_CANDIDATES:OK",
        ],
        custody: custody(),
      };
    case "capability_invalidation":
      return { counts: counts(), route_sequence: [], custody: custody() };
    case "rollback_future_turn":
      return {
        counts: counts({
          callbacks_expected: 4, callbacks_observed: 4, route_attempts_expected: 6, route_attempts_observed: 6,
          deliveries_expected: 2, deliveries_observed: 2, server_dispatches: 4, telemetry_attempts: 2
        }),
        route_sequence: [
          "session_start:IDENTITY_REGISTRATION:NO_DELIVERY",
          "before_agent_start:IDENTITY_REGISTRATION:NO_DELIVERY",
          "session_start:IDENTITY_REGISTRATION:OK",
          "session_start:SESSION_START_CONTEXT:OK",
          "before_agent_start:IDENTITY_REGISTRATION:OK",
          "before_agent_start:AMBIENT_CANDIDATES:OK",
        ],
        custody: custody(),
      };
    default:
      throw new Error(`unknown fixture scenario ${id}`);
  }
}

function scenario(id, overrides = {}) {
  const facts = scenarioFacts(id);
  return {
    id,
    state: "OBSERVED",
    passed: true,
    counts: facts.counts,
    route_sequence: facts.route_sequence,
    shared_deadline: true,
    custody: facts.custody,
    subcases: id === "capability_invalidation" ? invalidationSubcases() : [],
    proof_refs: proofRefs(),
    reason_codes: [],
    ...overrides,
  };
}

function scenarios(overrides = {}) {
  return SCENARIO_IDS.map((id) => overrides[id] || scenario(id));
}

function guarantee(overrides = {}) {
  return {
    state: "OBSERVED",
    proof_refs: proofRefs(),
    reason_codes: [],
    ...overrides,
  };
}

function guarantees(overrides = {}) {
  return Object.fromEntries(GUARANTEES.map((id) => [id, overrides[id] || guarantee()]));
}

function qualificationInput(overrides = {}) {
  return {
    run_id: "hap-01c-fixture-run",
    generated_at: "2026-08-30T12:34:56.000Z",
    candidate: {
      source_commit: gitObject("1"),
      source_tree: gitObject("2"),
      design_sha256: digest("3"),
      adapter_revision: "omp-hap-01b/1",
      adapter_sha256: digest("4"),
    },
    host: {
      platform: "win32",
      arch: "x64",
      profile: "hap-01c-safe",
      scratch: true,
    },
    artifact: {
      version: "6.49.0-rc.1+relay",
      package_sha256: digest("5"),
      extension_entry_sha256: digest("6"),
      relay_helper_sha256: digest("7"),
      adapter_sha256: digest("4"),
      daemon_object_sha256: digest("8"),
      client_object_sha256: digest("9"),
      fixture_object_sha256: digest("0"),
      omp_command_sha256: digest("1"),
      postgres_image_sha256: digest("2"),
      bootstrap_targets_sha256: digest("a"),
      install_tree_sha256: digest("b"),
      baseline_source_commit: gitObject("3"),
      baseline_source_tree: gitObject("4"),
      baseline_plugin_install_tree_sha256: digest("3"),
      baseline_client_object_sha256: digest("4"),
      archives: [
        { name: "engram_6.49.0_darwin_arm64.tar.gz", platform: "darwin", arch: "arm64", size: 2048, sha256: digest("c") },
        { name: "engram_6.49.0_linux_amd64.tar.gz", platform: "linux", arch: "amd64", size: 4096, sha256: digest("d") },
        { name: "engram_6.49.0_windows_amd64.zip", platform: "win32", arch: "amd64", size: 8192, sha256: digest("e") },
      ],
    },
    scenarios: scenarios(),
    guarantees: guarantees(),
    effects: {
      active_profile_before_sha256: digest("f"),
      active_profile_after_sha256: digest("f"),
      production_mutation: false,
      real_credentials_used: false,
      cleanup_residue: 0,
    },
    evidence_refs: evidenceRefs(),
    ...overrides,
  };
}

function findScenario(value, id) {
  const found = value.scenarios.find((item) => item.id === id);
  assert.ok(found, `missing fixture scenario ${id}`);
  return found;
}

function resign(record) {
  record.record_sha256 = canonicalDigest(record);
  return record;
}

function assertBuildRejects(mutator) {
  const input = qualificationInput();
  mutator(input);
  assert.throws(() => buildQualificationRecord(input), QualificationRecordError);
}

function assertParseRejects(mutator, source = buildQualificationRecord(qualificationInput())) {
  const record = structuredClone(source);
  mutator(record);
  assert.throws(() => parseQualificationRecord(record), QualificationRecordError);
}

test("HAP-01C exposes the exact schema constants and builds an immutable qualified record", () => {
  const record = buildQualificationRecord(qualificationInput());

  assert.equal(record.schema, "hap-01c-qualification-1");
  assert.equal(record.schema, SCHEMA);
  assert.equal(record.capability, "installed-hap-01c-relay");
  assert.equal(record.capability, CAPABILITY);
  assert.deepEqual(SCENARIO_IDS, [
    "new_new_happy",
    "new_plugin_old_daemon",
    "old_plugin_new_daemon",
    "relay_outage",
    "stale_generation",
    "capability_invalidation",
    "rollback_future_turn",
  ]);
  assert.deepEqual(GUARANTEES, [
    "relay_reachable",
    "adapter_attestation_bound",
    "credential_custody",
    "route_parity",
    "callback_order",
    "callback_deadline",
    "capability_lifecycle",
    "partial_rollout",
    "outage_no_fallback",
    "attempt_telemetry",
    "rollback_future_turn",
    "cleanup",
  ]);
  assert.deepEqual(STATES, ["OBSERVED", "MISSING", "CONTRADICTED", "UNAVAILABLE", "INFERRED", "PROHIBITED"]);
  assert.deepEqual(PROOF_CLASSES, ["BUILT_ARTIFACT", "SCRATCH_INSTALL", "INSTALLED_RUNTIME", "SOURCE_DIAGNOSTIC", "INFERENCE"]);
  assert.deepEqual(DISPOSITIONS, ["QUALIFIED", "UNQUALIFIED", "INCONCLUSIVE"]);
  assert.deepEqual(EXIT_CODES, { QUALIFIED: 0, UNQUALIFIED: 20, INCONCLUSIVE: 21, BOUNDARY_FAILURE: 10 });
  assert.deepEqual(Object.keys(record), [
    "schema",
    "capability",
    "run_id",
    "generated_at",
    "candidate",
    "host",
    "artifact",
    "scenarios",
    "guarantees",
    "gaps",
    "disposition",
    "effects",
    "evidence_refs",
    "record_sha256",
  ]);
  assert.deepEqual(record.scenarios.map((item) => item.id), SCENARIO_IDS);
  assert.deepEqual(Object.keys(record.guarantees), GUARANTEES);
  assert.equal(record.disposition, "QUALIFIED");
  assert.deepEqual(record.gaps, []);
  assert.equal(exitFor(record), EXIT_CODES.QUALIFIED);
  assert.equal(Object.isFrozen(record), true);
  assert.equal(Object.isFrozen(record.scenarios[0].counts), true);
  assert.equal(Object.isFrozen(record.evidence_refs), true);
});

test("safe ambient omission keeps partial rollout, stale recovery, and rollback qualified", () => {
  const input = qualificationInput();
  for (const id of ["old_plugin_new_daemon", "stale_generation", "rollback_future_turn"]) {
    findScenario(input, id).counts.deliveries_observed = 1;
  }
  const record = buildQualificationRecord(input);
  assert.equal(record.disposition, "QUALIFIED");
  assert.equal(record.scenarios.filter((item) => item.counts.deliveries_observed === 1).length, 3);

  assertBuildRejects((invalid) => {
    findScenario(invalid, "rollback_future_turn").counts.deliveries_observed = 0;
  });
});

test("qualification derives disposition, gaps, and reasons from the complete record", () => {
  const record = buildQualificationRecord(qualificationInput());
  assert.equal(deriveDisposition(record), "QUALIFIED");
  assert.equal(deriveDisposition(qualificationInput()), "QUALIFIED");
  assert.deepEqual(deriveGaps(record), []);
  assert.deepEqual(deriveReasons(record), ["ALL_QUALIFICATION_CRITERIA_OBSERVED"]);

  const failedInput = qualificationInput();
  const failedScenario = findScenario(failedInput, "relay_outage");
  failedScenario.passed = false;
  const failed = buildQualificationRecord(failedInput);
  assert.equal(failed.disposition, "UNQUALIFIED");
  assert.deepEqual(deriveGaps(failed), failed.gaps);
  assert.ok(deriveReasons(failed).includes("SCENARIO_RELAY_OUTAGE_FAILED"));
});

test("canonical serialization is deterministic, excludes record_sha256, and verifies the digest", () => {
  const record = buildQualificationRecord(qualificationInput());
  const reordered = {
    record_sha256: record.record_sha256,
    evidence_refs: record.evidence_refs,
    effects: record.effects,
    disposition: record.disposition,
    gaps: record.gaps,
    guarantees: record.guarantees,
    scenarios: record.scenarios,
    artifact: record.artifact,
    host: record.host,
    candidate: record.candidate,
    generated_at: record.generated_at,
    run_id: record.run_id,
    capability: record.capability,
    schema: record.schema,
  };

  assert.equal(canonicalSerialize(record), canonicalSerialize(reordered));
  assert.equal(canonicalDigest(record), record.record_sha256);
  assert.equal(canonicalDigest({ ...record, record_sha256: digest("0") }), record.record_sha256);
  assert.equal(verifyDigest(record), true);
  assert.equal(verifyDigest({ ...record, record_sha256: digest("0") }), false);
  assertParseRejects((tampered) => {
    tampered.record_sha256 = digest("0");
  }, record);
});

test("direct fallback, disallowed effects, and an observed scenario failure are unqualified", () => {
  const directFallback = qualificationInput();
  const directFallbackScenario = findScenario(directFallback, "new_plugin_old_daemon");
  directFallbackScenario.passed = false;
  directFallbackScenario.counts.direct_fallback_attempts = 1;

  const effectFailure = qualificationInput();
  effectFailure.effects.production_mutation = true;

  const scenarioFailure = qualificationInput();
  findScenario(scenarioFailure, "relay_outage").passed = false;

  const records = [
    buildQualificationRecord(directFallback),
    buildQualificationRecord(effectFailure),
    buildQualificationRecord(scenarioFailure),
  ];
  assert.deepEqual(records.map((record) => record.disposition), ["UNQUALIFIED", "UNQUALIFIED", "UNQUALIFIED"]);
  assert.deepEqual(records.map(exitFor), [EXIT_CODES.UNQUALIFIED, EXIT_CODES.UNQUALIFIED, EXIT_CODES.UNQUALIFIED]);
  assert.ok(deriveReasons(records[0]).includes("SCENARIO_NEW_PLUGIN_OLD_DAEMON_DIRECT_FALLBACK"));
  assert.ok(deriveReasons(records[1]).includes("EFFECT_PRODUCTION_MUTATION"));
});

test("unavailable observation and missing installed-runtime proof are inconclusive", () => {
  const unavailable = qualificationInput();
  const unavailableScenario = findScenario(unavailable, "relay_outage");
  unavailableScenario.state = "UNAVAILABLE";
  unavailableScenario.passed = false;

  const sourceOnly = qualificationInput();
  sourceOnly.guarantees.relay_reachable.proof_refs = [sourceProof()];

  const records = [buildQualificationRecord(unavailable), buildQualificationRecord(sourceOnly)];
  assert.deepEqual(records.map((record) => record.disposition), ["INCONCLUSIVE", "INCONCLUSIVE"]);
  assert.deepEqual(records.map(exitFor), [EXIT_CODES.INCONCLUSIVE, EXIT_CODES.INCONCLUSIVE]);
  assert.ok(deriveReasons(records[1]).includes("MISSING_INSTALLED_RUNTIME_GUARANTEE_RELAY_REACHABLE"));
});

test("boundary failure exposes only the redacted safe object and maps to the boundary exit", () => {
  const secretPath = "C:/operators/private/token=not-for-records";
  const failure = boundaryFailure("RELAY_BOOTSTRAP_UNAVAILABLE", secretPath);

  assert.deepEqual(Object.keys(failure), ["error_class", "message_sha256"]);
  assert.equal(failure.error_class, "RELAY_BOOTSTRAP_UNAVAILABLE");
  assert.equal(failure.message_sha256, sha256(secretPath));
  assert.equal(exitFor(failure), EXIT_CODES.BOUNDARY_FAILURE);
  assert.equal(Object.isFrozen(failure), true);
  assert.doesNotMatch(JSON.stringify(failure), /token=|C:[\\/]|private/i);
  assert.throws(() => {
    failure.error_class = "LEAKED";
  }, TypeError);
});

test("every record and nested object rejects unknown and missing fields", () => {
  const qualified = buildQualificationRecord(qualificationInput());
  const withGapInput = qualificationInput();
  withGapInput.effects.real_credentials_used = true;
  const withGap = buildQualificationRecord(withGapInput);
  const cases = [
    ["record", () => qualified, (record) => record, "run_id"],
    ["candidate", () => qualified, (record) => record.candidate, "source_commit"],
    ["host", () => qualified, (record) => record.host, "profile"],
    ["artifact", () => qualified, (record) => record.artifact, "version"],
    ["archive", () => qualified, (record) => record.artifact.archives[0], "name"],
    ["scenario", () => qualified, (record) => record.scenarios[0], "state"],
    ["counts", () => qualified, (record) => record.scenarios[0].counts, "callbacks_expected"],
    ["custody", () => qualified, (record) => record.scenarios[0].custody, "extension_url_present"],
    ["subcase", () => qualified, (record) => findScenario(record, "capability_invalidation").subcases[0], "passed"],
    ["guarantee", () => qualified, (record) => record.guarantees.relay_reachable, "state"],
    ["proof reference", () => qualified, (record) => record.guarantees.relay_reachable.proof_refs[0], "id"],
    ["effects", () => qualified, (record) => record.effects, "production_mutation"],
    ["evidence reference", () => qualified, (record) => record.evidence_refs[0], "id"],
    ["gap", () => withGap, (record) => record.gaps[0], "scope"],
  ];

  for (const [label, factory, select, required] of cases) {
    const withUnknown = structuredClone(factory());
    select(withUnknown).unexpected = "never admitted";
    assert.throws(() => parseQualificationRecord(withUnknown), QualificationRecordError, `${label} rejects unknown fields`);

    const withMissing = structuredClone(factory());
    delete select(withMissing)[required];
    assert.throws(() => parseQualificationRecord(withMissing), QualificationRecordError, `${label} rejects missing fields`);
  }
});

test("the legacy schema and aliases do not accept omp-advisor-1 records", () => {
  const record = buildQualificationRecord(qualificationInput());
  const oldSchema = structuredClone(record);
  oldSchema.schema = "omp-advisor-1";
  resign(oldSchema);

  assert.throws(() => parseQualificationRecord(oldSchema), QualificationRecordError);
  assert.throws(() => parseCapabilityRecord(oldSchema), QualificationRecordError);
  const aliasRecord = buildCapabilityRecord(qualificationInput());
  assert.equal(aliasRecord.schema, SCHEMA);
  assert.equal(aliasRecord.capability, CAPABILITY);
});

test("scenario, guarantee, and invalidation-subcase membership is complete, closed, and duplicate-free", () => {
  assertBuildRejects((input) => {
    input.scenarios.pop();
  });
  assertBuildRejects((input) => {
    input.scenarios[SCENARIO_IDS.length - 1] = structuredClone(input.scenarios[0]);
  });
  assertBuildRejects((input) => {
    input.scenarios[0].id = "unrecognized_scenario";
  });
  assertBuildRejects((input) => {
    delete input.guarantees.cleanup;
  });
  assertBuildRejects((input) => {
    input.guarantees.unrecognized_guarantee = guarantee();
  });
  assertBuildRejects((input) => {
    input.guarantees = [guarantee(), guarantee()];
  });
  assertBuildRejects((input) => {
    findScenario(input, "capability_invalidation").subcases.pop();
  });
  assertBuildRejects((input) => {
    const subcases = findScenario(input, "capability_invalidation").subcases;
    subcases[subcases.length - 1] = structuredClone(subcases[0]);
  });
  assertBuildRejects((input) => {
    findScenario(input, "capability_invalidation").subcases[0].id = "unrecognized_subcase";
  });
  assertBuildRejects((input) => {
    findScenario(input, "relay_outage").subcases = [structuredClone(findScenario(input, "capability_invalidation").subcases[0])];
  });
});

test("scenario state, pass state, observed denominators, invalidation, and happy-path semantics are constrained", () => {
  assertBuildRejects((input) => {
    const target = findScenario(input, "relay_outage");
    target.state = "UNAVAILABLE";
    target.passed = true;
  });
  assertBuildRejects((input) => {
    findScenario(input, "relay_outage").counts.callbacks_observed = 1;
  });
  assertBuildRejects((input) => {
    findScenario(input, "new_new_happy").route_sequence[3] = "before_agent_start:SESSION_START_CONTEXT:OK";
  });
  assertBuildRejects((input) => {
    const target = findScenario(input, "capability_invalidation");
    target.subcases[0].passed = false;
  });
  assertBuildRejects((input) => {
    const target = findScenario(input, "new_new_happy");
    target.counts.direct_fallback_attempts = 1;
  });
});

test("qualified disposition requires exact scenario facts and archive matrix", () => {
  const cases = [
    (input) => { findScenario(input, "new_new_happy").shared_deadline = false; },
    (input) => { findScenario(input, "new_new_happy").custody.extension_token_present = true; },
    (input) => {
      const target = findScenario(input, "new_new_happy");
      target.counts.deliveries_expected = 1;
      target.counts.deliveries_observed = 1;
    },
    (input) => { findScenario(input, "stale_generation").counts.rediscoveries = 0; },
    (input) => { findScenario(input, "capability_invalidation").subcases[0].server_dispatches = 1; },
    (input) => { input.artifact.archives.pop(); },
  ];
  for (const mutate of cases) {
    const input = qualificationInput();
    mutate(input);
    const record = buildQualificationRecord(input);
    assert.equal(record.disposition, "UNQUALIFIED");
    assert.ok(deriveReasons(record).some((reason) => /CONTRACT_MISMATCH|SUBCASE_|ARCHIVE_MATRIX/.test(reason)));
  }
});

test("malformed route tokens, counts, custody, archives, effects, candidate, host, timestamps, and semver are rejected", () => {
  assertBuildRejects((input) => {
    findScenario(input, "new_new_happy").route_sequence[0] = "session_start:DIRECT:OK";
  });
  assertBuildRejects((input) => {
    findScenario(input, "relay_outage").counts.telemetry_attempts = -1;
  });
  assertBuildRejects((input) => {
    findScenario(input, "relay_outage").counts.telemetry_attempts = 1.5;
  });
  assertBuildRejects((input) => {
    findScenario(input, "relay_outage").counts.telemetry_attempts = Number.MAX_SAFE_INTEGER;
  });
  assertBuildRejects((input) => {
    findScenario(input, "relay_outage").custody.authorization_seen = "yes";
  });
  assertBuildRejects((input) => {
    input.artifact.archives[0].name = "../private-release.tar.gz";
  });
  assertBuildRejects((input) => {
    input.artifact.archives[0].size = 0;
  });
  assertBuildRejects((input) => {
    input.artifact.archives[0].platform = "android";
  });
  assertBuildRejects((input) => {
    input.artifact.archives[0].sha256 = "not-a-sha";
  });
  assertBuildRejects((input) => {
    input.effects.cleanup_residue = 1.5;
  });
  assertBuildRejects((input) => {
    input.effects.production_mutation = "false";
  });
  assertBuildRejects((input) => {
    input.candidate.source_commit = "not-a-git-object";
  });
  assertBuildRejects((input) => {
    input.candidate.adapter_revision = "omp-hap-01c/1";
  });
  assertBuildRejects((input) => {
    input.host.platform = "android";
  });
  assertBuildRejects((input) => {
    input.host.profile = "unsafe profile";
  });
  assertBuildRejects((input) => {
    input.host.scratch = false;
  });
  assertBuildRejects((input) => {
    input.generated_at = "2026-02-30T12:34:56Z";
  });
  assertBuildRejects((input) => {
    input.artifact.version = "v6.49.0";
  });
});

test("proof references must resolve exactly to unique top-level evidence", () => {
  assertBuildRejects((input) => {
    findScenario(input, "relay_outage").proof_refs = [{ id: "missing-evidence", class: "INSTALLED_RUNTIME", sha256: digest("a") }];
  });
  assertBuildRejects((input) => {
    input.guarantees.relay_reachable.proof_refs = [{ id: "runtime-a", class: "INSTALLED_RUNTIME", sha256: digest("f") }];
  });
  assertBuildRejects((input) => {
    input.evidence_refs.push(runtimeProof());
  });
  assertBuildRejects((input) => {
    input.evidence_refs[0].id = "runtime-a";
  });
});

test("build normalizes unordered inputs while parsing requires every canonical array order", () => {
  const source = qualificationInput();
  source.scenarios.reverse();
  source.artifact.archives.reverse();
  source.evidence_refs.reverse();
  findScenario(source, "capability_invalidation").subcases.reverse();
  source.guarantees.relay_reachable.proof_refs = [secondRuntimeProof(), runtimeProof()];
  source.guarantees.relay_reachable.reason_codes = ["Z_REASON", "A_REASON"];

  const record = buildQualificationRecord(source);
  assert.deepEqual(record.scenarios.map((item) => item.id), SCENARIO_IDS);
  assert.deepEqual(record.artifact.archives.map((item) => item.name), [
    "engram_6.49.0_darwin_arm64.tar.gz",
    "engram_6.49.0_linux_amd64.tar.gz",
    "engram_6.49.0_windows_amd64.zip",
  ]);
  assert.deepEqual(record.evidence_refs.map((item) => item.id), ["artifact-a", "runtime-a", "runtime-b", "source-a"]);
  assert.deepEqual(findScenario(record, "capability_invalidation").subcases.map((item) => item.id), [
    "adapter_mismatch",
    "expiry",
    "process_exit",
    "project_keycard_rotation",
  ]);
  assert.deepEqual(record.guarantees.relay_reachable.proof_refs.map((item) => item.id), ["runtime-a", "runtime-b"]);
  assert.deepEqual(record.guarantees.relay_reachable.reason_codes, ["A_REASON", "Z_REASON"]);

  const noncanonicalMutations = [
    (candidate) => candidate.scenarios.reverse(),
    (candidate) => candidate.artifact.archives.reverse(),
    (candidate) => candidate.evidence_refs.reverse(),
    (candidate) => findScenario(candidate, "capability_invalidation").subcases.reverse(),
    (candidate) => candidate.guarantees.relay_reachable.proof_refs.reverse(),
    (candidate) => candidate.guarantees.relay_reachable.reason_codes.reverse(),
  ];
  for (const mutate of noncanonicalMutations) {
    const noncanonical = structuredClone(record);
    mutate(noncanonical);
    resign(noncanonical);
    assert.throws(() => parseQualificationRecord(noncanonical), QualificationRecordError);
  }
});

test("candidate and artifact adapter digests must agree", () => {
  assertBuildRejects((input) => {
    input.candidate.adapter_sha256 = digest("f");
  });

  const record = structuredClone(buildQualificationRecord(qualificationInput()));
  record.artifact.adapter_sha256 = digest("f");
  resign(record);
  assert.throws(() => parseQualificationRecord(record), QualificationRecordError);
});

test("building copy-owns caller data and emits a deeply immutable record", () => {
  const input = qualificationInput();
  const before = structuredClone(input);
  const record = buildQualificationRecord(input);

  assert.deepEqual(input, before);
  assert.throws(() => {
    record.scenarios[0].counts.callbacks_expected = 99;
  }, TypeError);
  assert.throws(() => {
    record.evidence_refs.push(runtimeProof());
  }, TypeError);
  assert.equal(record.scenarios[0].counts.callbacks_expected, 2);
  assert.equal(record.evidence_refs.length, 4);
});
