"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");

const {
  EXIT_CODES,
  GUARANTEES,
  PROOF_CLASSES,
  STATES,
  boundaryFailure,
  buildCapabilityRecord,
  canonicalDigest,
  canonicalSerialize,
  exitFor,
  parseCapabilityRecord,
  verifyDigest,
} = require("./omp-capability-record.js");

const digest = (character) => character.repeat(64);

function proof(id, proofClass = "INSTALLED_RUNTIME", character = "a") {
  return { id, class: proofClass, sha256: digest(character) };
}

function guarantee(state = "OBSERVED", proofClass = "INSTALLED_RUNTIME", character = "a") {
  return { state, proof_refs: [proof(`evidence-${character}`, proofClass, character)], reason_codes: [] };
}

function guarantees(overrides = {}) {
  const chars = ["a", "b", "c", "d", "e", "f", "1"];
  return Object.fromEntries(GUARANTEES.map((key, index) => [
    key,
    overrides[key] || guarantee("OBSERVED", key === "installed_artifact" ? "INSTALLED_ARTIFACT" : "INSTALLED_RUNTIME", chars[index]),
  ]));
}

function input(overrides = {}) {
  return {
    host: { platform: "win32", arch: "x64" },
    artifact: {
      package_version: "6.48.0",
      package_manifest_sha256: digest("a"),
      omp_manifest_sha256: digest("b"),
      extension_sha256: digest("c"),
      bootstrap_policy_sha256: digest("d"),
      daemon_object_sha256: digest("e"),
      daemon_object_size: 19789312,
      relative_entrypoint: "scripts/run-engram.js",
      daemon_asset: "engram-windows-amd64.exe",
    },
    probe: { receipt_id: "hap-01-receipt", run_id: "fixture-run", evidence_digest: digest("f") },
    guarantees: guarantees(),
    effects: {
      profile_registry_unchanged: true,
      configuration_unchanged: true,
      probe_writes_confined: true,
      scratch_cleaned: true,
    },
    ...overrides,
  };
}

test("capability record fixes every guarantee key", () => {
  const record = buildCapabilityRecord(input());
  assert.deepEqual(Object.keys(record.guarantees), GUARANTEES);
  assert.deepEqual(record.gaps, []);
  assert.equal(record.disposition, "QUALIFIED");
  assert.equal(exitFor(record), EXIT_CODES.QUALIFIED);
});

test("capability record admits each closed state and proof class", () => {
  for (const state of STATES) {
    for (const proofClass of PROOF_CLASSES) {
      const record = buildCapabilityRecord(input({
        guarantees: guarantees({ callback_budget: guarantee(state, proofClass, "9") }),
      }));
      assert.equal(record.guarantees.callback_budget.state, state, `${state}/${proofClass}`);
      assert.equal(record.guarantees.callback_budget.proof_refs[0].class, proofClass, `${state}/${proofClass}`);
    }
  }
});

test("canonical serialization is deterministic and excludes its own digest", () => {
  const first = buildCapabilityRecord(input());
  const reordered = {
    effects: first.effects,
    reasons: first.reasons,
    guarantees: first.guarantees,
    artifact: first.artifact,
    probe: first.probe,
    capability: first.capability,
    canonical_sha256: first.canonical_sha256,
    host: first.host,
    schema: first.schema,
    disposition: first.disposition,
    gaps: first.gaps,
  };
  const second = buildCapabilityRecord(input());
  assert.equal(canonicalSerialize(first), canonicalSerialize(second));
  assert.equal(canonicalSerialize(first), canonicalSerialize(reordered));
  assert.equal(canonicalDigest(first), first.canonical_sha256);
  assert.equal(canonicalDigest({ ...first, canonical_sha256: digest("9") }), first.canonical_sha256);
  assert.equal(verifyDigest(first), true);
  assert.equal(verifyDigest({ ...first, canonical_sha256: digest("9") }), false);
});

test("direct installed-runtime REST authorization dominates every other gap", () => {
  const record = buildCapabilityRecord(input({
    guarantees: guarantees({
      direct_credential_disabled: guarantee("CONTRADICTED", "INSTALLED_RUNTIME", "9"),
      daemon_authenticated_initialize: guarantee("PROHIBITED", "INFERENCE", "8"),
    }),
  }));
  assert.equal(record.disposition, "UNQUALIFIED");
  assert.deepEqual(record.reasons, ["DIRECT_INSTALLED_RUNTIME_REST_AUTHORIZATION"]);
  assert.equal(exitFor(record), EXIT_CODES.UNQUALIFIED);
});

test("source-only and inferred evidence can never qualify", () => {
  const sourceOnly = buildCapabilityRecord(input({
    guarantees: Object.fromEntries(GUARANTEES.map((key, index) => [key, guarantee("OBSERVED", "SOURCE_DIAGNOSTIC", String(index + 1))])),
  }));
  const inferred = buildCapabilityRecord(input({
    guarantees: guarantees({ callback_budget: guarantee("INFERRED", "INFERENCE", "9") }),
  }));
  assert.equal(sourceOnly.disposition, "INCONCLUSIVE");
  assert.equal(inferred.disposition, "INCONCLUSIVE");
  assert.equal(exitFor(sourceOnly), EXIT_CODES.INCONCLUSIVE);
  assert.equal(exitFor(inferred), EXIT_CODES.INCONCLUSIVE);
});
test("unavailable installed-runtime evidence remains inconclusive", () => {
  const unavailable = buildCapabilityRecord(input({
    guarantees: guarantees({ daemon_authenticated_initialize: guarantee("UNAVAILABLE", "INSTALLED_RUNTIME", "8") }),
  }));
  assert.equal(unavailable.disposition, "INCONCLUSIVE");
  assert.equal(exitFor(unavailable), EXIT_CODES.INCONCLUSIVE);
});

test("disposition and exit mapping distinguish proven failures from prohibited observation", () => {
  const qualified = buildCapabilityRecord(input());
  const unqualified = buildCapabilityRecord(input({
    guarantees: guarantees({ before_agent_start_anchor: guarantee("MISSING", "INSTALLED_RUNTIME", "9") }),
  }));
  const inconclusive = buildCapabilityRecord(input({
    guarantees: guarantees({ daemon_authenticated_initialize: guarantee("PROHIBITED", "INFERENCE", "9") }),
  }));
  assert.deepEqual([qualified.disposition, unqualified.disposition, inconclusive.disposition], ["QUALIFIED", "UNQUALIFIED", "INCONCLUSIVE"]);
  assert.deepEqual([exitFor(qualified), exitFor(unqualified), exitFor(inconclusive)], [0, 20, 21]);
});

test("boundary failures retain only an error class and message digest", () => {
  const failure = boundaryFailure("PROFILE_REGISTRY_UNAVAILABLE", "C:/user/private/token-value");
  assert.deepEqual(Object.keys(failure).sort(), ["error_class", "exit_code", "message_digest"]);
  assert.equal(failure.exit_code, EXIT_CODES.BOUNDARY_FAILURE);
  assert.match(failure.message_digest, /^[a-f0-9]{64}$/);
  assert.doesNotMatch(JSON.stringify(failure), /token-value|C:\//i);
});

test("record shape rejects secret, raw callback, and absolute path fields", () => {
  const record = buildCapabilityRecord(input());
  const withToken = structuredClone(record);
  withToken.guarantees.callback_budget.proof_refs[0].token = "secret";
  const withPath = structuredClone(record);
  withPath.artifact.path = "C:/private/installed/plugin";
  const withCallback = structuredClone(record);
  withCallback.callback = { prompt: "never retain" };
  const malformedEntrypoint = structuredClone(record);
  malformedEntrypoint.artifact.relative_entrypoint = "../scripts/run-engram.js";
  assert.throws(() => parseCapabilityRecord(withToken));
  assert.throws(() => parseCapabilityRecord(withPath));
  assert.throws(() => parseCapabilityRecord(withCallback));
  assert.throws(() => parseCapabilityRecord(malformedEntrypoint));
  const serialized = JSON.stringify(record);
  assert.doesNotMatch(serialized, /"(?:token|secret|authorization|prompt|callback|path|pid|daemon_generation)"\s*:/i);
  assert.doesNotMatch(serialized, /[A-Za-z]:[\\/]/);
});
