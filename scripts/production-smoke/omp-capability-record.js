#!/usr/bin/env node
"use strict";

const crypto = require("node:crypto");

const SCHEMA = "hap-01c-qualification-1";
const CAPABILITY = "installed-hap-01c-relay";
const GUARANTEES = Object.freeze([
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
const SCENARIO_IDS = Object.freeze([
  "new_new_happy",
  "new_plugin_old_daemon",
  "old_plugin_new_daemon",
  "relay_outage",
  "stale_generation",
  "capability_invalidation",
  "rollback_future_turn",
]);
const STATES = Object.freeze(["OBSERVED", "MISSING", "CONTRADICTED", "UNAVAILABLE", "INFERRED", "PROHIBITED"]);
const PROOF_CLASSES = Object.freeze(["BUILT_ARTIFACT", "SCRATCH_INSTALL", "INSTALLED_RUNTIME", "SOURCE_DIAGNOSTIC", "INFERENCE"]);
const DISPOSITIONS = Object.freeze(["QUALIFIED", "UNQUALIFIED", "INCONCLUSIVE"]);
const EXIT_CODES = Object.freeze({ QUALIFIED: 0, UNQUALIFIED: 20, INCONCLUSIVE: 21, BOUNDARY_FAILURE: 10 });

const RELAY_ROUTES = Object.freeze(["IDENTITY_REGISTRATION", "SESSION_START_CONTEXT", "AMBIENT_CANDIDATES"]);
const CALLBACKS = Object.freeze(["session_start", "before_agent_start"]);
const ROUTE_OUTCOMES = Object.freeze(["OK", "NO_DELIVERY", "REJECTED"]);
const INVALIDATION_SUBCASE_IDS = Object.freeze(["adapter_mismatch", "expiry", "process_exit", "project_keycard_rotation"]);
const GAP_SCOPES = Object.freeze(["GUARANTEE", "SCENARIO", "SUBCASE", "EFFECT", "EVIDENCE"]);

const RECORD_KEYS = Object.freeze([
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
const BUILD_INPUT_KEYS = Object.freeze([
  "run_id",
  "generated_at",
  "candidate",
  "host",
  "artifact",
  "scenarios",
  "guarantees",
  "effects",
  "evidence_refs",
]);
const ASSESSMENT_KEYS = Object.freeze(["scenarios", "guarantees", "effects"]);
const COUNT_KEYS = Object.freeze([
  "callbacks_expected",
  "callbacks_observed",
  "route_attempts_expected",
  "route_attempts_observed",
  "deliveries_expected",
  "deliveries_observed",
  "direct_fallback_attempts",
  "rediscoveries",
  "server_dispatches",
  "telemetry_attempts",
  "cleanup_residue",
]);
const CUSTODY_KEYS = Object.freeze([
  "extension_url_present",
  "extension_token_present",
  "extension_config_path_present",
  "authorization_seen",
  "child_hap_config_present",
]);

const SHA256 = /^[a-f0-9]{64}$/;
const GIT_OBJECT_ID = /^(?:[a-f0-9]{40}|[a-f0-9]{64})$/;
const SAFE_IDENTIFIER = /^[a-z][a-z0-9_-]{0,95}$/;
const SAFE_REASON = /^[A-Z][A-Z0-9_]{0,127}$/;
const SAFE_BASENAME = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const SEMVER = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-(?:0|[1-9]\d*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9]\d*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*))*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;
const RFC3339_UTC = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d{1,9})?Z$/;

const MAX_COUNT = 1_000_000;
const MAX_ARCHIVES = 32;
const MAX_EVIDENCE_REFS = 128;
const MAX_PROOF_REFS = 32;
const MAX_REASON_CODES = 32;
const MAX_ROUTE_SEQUENCE = 32;
const MAX_GAPS = 128;

const PLATFORM_SET = new Set(["win32", "linux", "darwin"]);
const STATE_SET = new Set(STATES);
const PROOF_CLASS_SET = new Set(PROOF_CLASSES);
const DISPOSITION_SET = new Set(DISPOSITIONS);
const CALLBACK_SET = new Set(CALLBACKS);
const RELAY_ROUTE_SET = new Set(RELAY_ROUTES);
const ROUTE_OUTCOME_SET = new Set(ROUTE_OUTCOMES);
const GAP_SCOPE_SET = new Set(GAP_SCOPES);
const INSTALLED_RUNTIME = "INSTALLED_RUNTIME";
const CANONICAL_INPUT = Object.freeze({ requireCanonicalArrays: false });
const CANONICAL_RECORD = Object.freeze({ requireCanonicalArrays: true });

const HAPPY_ROUTE_SEQUENCE = Object.freeze([
  "session_start:IDENTITY_REGISTRATION:OK",
  "session_start:SESSION_START_CONTEXT:OK",
  "before_agent_start:IDENTITY_REGISTRATION:OK",
  "before_agent_start:AMBIENT_CANDIDATES:OK",
]);

class QualificationRecordError extends Error {
  constructor(message) {
    super(message);
    this.name = "QualificationRecordError";
  }
}

function fail(message) {
  throw new QualificationRecordError(message);
}

function isPlainObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value) && Object.getPrototypeOf(value) === Object.prototype;
}

function exactKeys(value, keys, label) {
  if (!isPlainObject(value)) fail(`${label} must be an object`);
  const actual = Object.keys(value).sort();
  const expected = [...keys].sort();
  if (actual.length !== expected.length || actual.some((key, index) => key !== expected[index])) {
    fail(`${label} has unknown or missing fields`);
  }
}

function string(value, label, expression = null, maxLength = 4096) {
  if (typeof value !== "string" || value.length === 0 || value.length > maxLength || (expression && !expression.test(value))) {
    fail(`${label} is invalid`);
  }
  return value;
}

function safeIdentifier(value, label) {
  return string(value, label, SAFE_IDENTIFIER, 96);
}

function reasonCode(value, label) {
  return string(value, label, SAFE_REASON, 128);
}

function digest(value, label) {
  return string(value, label, SHA256, 64);
}

function gitObjectID(value, label) {
  return string(value, label, GIT_OBJECT_ID, 64);
}

function boolean(value, label) {
  if (typeof value !== "boolean") fail(`${label} must be boolean`);
  return value;
}

function count(value, label) {
  if (!Number.isSafeInteger(value) || value < 0 || value > MAX_COUNT) fail(`${label} is invalid`);
  return value;
}

function boundedArray(value, label, maxLength, minLength = 0) {
  if (!Array.isArray(value) || value.length < minLength || value.length > maxLength) fail(`${label} is invalid`);
  return value;
}

function timestamp(value, label) {
  const text = string(value, label, RFC3339_UTC, 64);
  const match = RFC3339_UTC.exec(text);
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  const hour = Number(match[4]);
  const minute = Number(match[5]);
  const second = Number(match[6]);
  const instant = new Date(Date.UTC(year, month - 1, day, hour, minute, second));
  if (
    instant.getUTCFullYear() !== year ||
    instant.getUTCMonth() !== month - 1 ||
    instant.getUTCDate() !== day ||
    instant.getUTCHours() !== hour ||
    instant.getUTCMinutes() !== minute ||
    instant.getUTCSeconds() !== second
  ) {
    fail(`${label} is invalid`);
  }
  return text;
}

function sha256(value) {
  return crypto.createHash("sha256").update(typeof value === "string" || Buffer.isBuffer(value) ? value : String(value)).digest("hex");
}

function canonicalize(value) {
  if (value === null || typeof value === "boolean" || typeof value === "string") return JSON.stringify(value);
  if (typeof value === "number") {
    if (!Number.isFinite(value)) fail("canonical data contains a non-finite number");
    return JSON.stringify(value);
  }
  if (Array.isArray(value)) return `[${value.map(canonicalize).join(",")}]`;
  if (!isPlainObject(value)) fail("canonical data contains an unsupported value");
  return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonicalize(value[key])}`).join(",")}}`;
}

function deepFreeze(value) {
  if (Array.isArray(value)) {
    for (const item of value) deepFreeze(item);
  } else if (isPlainObject(value)) {
    for (const item of Object.values(value)) deepFreeze(item);
  }
  return Object.freeze(value);
}

function orderOrReject(items, compare, label, mode) {
  const sorted = [...items].sort(compare);
  if (mode.requireCanonicalArrays) {
    for (let index = 0; index < items.length; index += 1) {
      if (compare(items[index], sorted[index]) !== 0) fail(`${label} must be sorted canonically`);
    }
    return items;
  }
  return sorted;
}

function ensureUnique(items, keyOf, label) {
  const seen = new Set();
  for (const item of items) {
    const key = keyOf(item);
    if (seen.has(key)) fail(`${label} contains duplicates`);
    seen.add(key);
  }
}

function enumValue(value, allowed, label) {
  const text = string(value, label, null, 128);
  if (!allowed.has(text)) fail(`${label} is not admitted`);
  return text;
}

function parseEvidenceReference(value, label) {
  exactKeys(value, ["id", "class", "sha256"], label);
  return {
    id: safeIdentifier(value.id, `${label}.id`),
    class: enumValue(value.class, PROOF_CLASS_SET, `${label}.class`),
    sha256: digest(value.sha256, `${label}.sha256`),
  };
}

function compareEvidenceReference(left, right) {
  return left.id.localeCompare(right.id);
}

function parseEvidenceReferences(value, label, mode) {
  const refs = boundedArray(value, label, MAX_EVIDENCE_REFS, 1).map((item, index) => parseEvidenceReference(item, `${label}[${index}]`));
  ensureUnique(refs, (ref) => ref.id, label);
  return orderOrReject(refs, compareEvidenceReference, label, mode);
}

function parseProofReferences(value, label, mode) {
  const refs = boundedArray(value, label, MAX_PROOF_REFS, 1).map((item, index) => parseEvidenceReference(item, `${label}[${index}]`));
  ensureUnique(refs, (ref) => ref.id, label);
  return orderOrReject(refs, compareEvidenceReference, label, mode);
}

function parseReasonCodes(value, label, mode) {
  const reasons = boundedArray(value, label, MAX_REASON_CODES).map((item, index) => reasonCode(item, `${label}[${index}]`));
  ensureUnique(reasons, (reason) => reason, label);
  return orderOrReject(reasons, (left, right) => left.localeCompare(right), label, mode);
}

function parseCandidate(value) {
  exactKeys(value, ["source_commit", "source_tree", "design_sha256", "adapter_revision", "adapter_sha256"], "candidate");
  if (value.adapter_revision !== "omp-hap-01b/1") fail("candidate.adapter_revision is unsupported");
  return {
    source_commit: gitObjectID(value.source_commit, "candidate.source_commit"),
    source_tree: gitObjectID(value.source_tree, "candidate.source_tree"),
    design_sha256: digest(value.design_sha256, "candidate.design_sha256"),
    adapter_revision: "omp-hap-01b/1",
    adapter_sha256: digest(value.adapter_sha256, "candidate.adapter_sha256"),
  };
}

function parseHost(value) {
  exactKeys(value, ["platform", "arch", "profile", "scratch"], "host");
  const platform = enumValue(value.platform, PLATFORM_SET, "host.platform");
  return {
    platform,
    arch: safeIdentifier(value.arch, "host.arch"),
    profile: safeIdentifier(value.profile, "host.profile"),
    scratch: boolean(value.scratch, "host.scratch"),
  };
}

function parseArchive(value, label) {
  exactKeys(value, ["name", "platform", "arch", "size", "sha256"], label);
  const name = string(value.name, `${label}.name`, SAFE_BASENAME, 128);
  if (name === "." || name === ".." || name.includes("/") || name.includes("\\")) fail(`${label}.name is invalid`);
  return {
    name,
    platform: enumValue(value.platform, PLATFORM_SET, `${label}.platform`),
    arch: safeIdentifier(value.arch, `${label}.arch`),
    size: (() => {
      if (!Number.isSafeInteger(value.size) || value.size <= 0) fail(`${label}.size is invalid`);
      return value.size;
    })(),
    sha256: digest(value.sha256, `${label}.sha256`),
  };
}

function parseArchives(value, mode) {
  const archives = boundedArray(value, "artifact.archives", MAX_ARCHIVES).map((item, index) => parseArchive(item, `artifact.archives[${index}]`));
  ensureUnique(archives, (archive) => archive.name, "artifact.archives");
  return orderOrReject(archives, (left, right) => left.name.localeCompare(right.name), "artifact.archives", mode);
}

function parseArtifact(value, mode) {
  exactKeys(value, [
    "version",
    "package_sha256",
    "extension_entry_sha256",
    "relay_helper_sha256",
    "adapter_sha256",
    "daemon_object_sha256",
    "client_object_sha256",
    "fixture_object_sha256",
    "omp_command_sha256",
    "postgres_image_sha256",
    "bootstrap_targets_sha256",
    "install_tree_sha256",
    "baseline_source_commit",
    "baseline_source_tree",
    "baseline_plugin_install_tree_sha256",
    "baseline_client_object_sha256",
    "archives",
  ], "artifact");
  return {
    version: string(value.version, "artifact.version", SEMVER, 128),
    package_sha256: digest(value.package_sha256, "artifact.package_sha256"),
    extension_entry_sha256: digest(value.extension_entry_sha256, "artifact.extension_entry_sha256"),
    relay_helper_sha256: digest(value.relay_helper_sha256, "artifact.relay_helper_sha256"),
    adapter_sha256: digest(value.adapter_sha256, "artifact.adapter_sha256"),
    daemon_object_sha256: digest(value.daemon_object_sha256, "artifact.daemon_object_sha256"),
    client_object_sha256: digest(value.client_object_sha256, "artifact.client_object_sha256"),
    fixture_object_sha256: digest(value.fixture_object_sha256, "artifact.fixture_object_sha256"),
    omp_command_sha256: digest(value.omp_command_sha256, "artifact.omp_command_sha256"),
    postgres_image_sha256: digest(value.postgres_image_sha256, "artifact.postgres_image_sha256"),
    bootstrap_targets_sha256: digest(value.bootstrap_targets_sha256, "artifact.bootstrap_targets_sha256"),
    install_tree_sha256: digest(value.install_tree_sha256, "artifact.install_tree_sha256"),
    baseline_source_commit: gitObjectID(value.baseline_source_commit, "artifact.baseline_source_commit"),
    baseline_source_tree: gitObjectID(value.baseline_source_tree, "artifact.baseline_source_tree"),
    baseline_plugin_install_tree_sha256: digest(value.baseline_plugin_install_tree_sha256, "artifact.baseline_plugin_install_tree_sha256"),
    baseline_client_object_sha256: digest(value.baseline_client_object_sha256, "artifact.baseline_client_object_sha256"),
    archives: parseArchives(value.archives, mode),
  };
}

function parseCounts(value, label) {
  exactKeys(value, COUNT_KEYS, label);
  return Object.fromEntries(COUNT_KEYS.map((key) => [key, count(value[key], `${label}.${key}`)]));
}

function parseRouteToken(value, label) {
  const token = string(value, label, null, 128);
  const parts = token.split(":");
  if (
    parts.length !== 3 ||
    !CALLBACK_SET.has(parts[0]) ||
    !RELAY_ROUTE_SET.has(parts[1]) ||
    !ROUTE_OUTCOME_SET.has(parts[2]) ||
    parts.join(":") !== token
  ) {
    fail(`${label} is invalid`);
  }
  return token;
}

function parseRouteSequence(value, label) {
  return boundedArray(value, label, MAX_ROUTE_SEQUENCE).map((token, index) => parseRouteToken(token, `${label}[${index}]`));
}

function parseCustody(value, label) {
  exactKeys(value, CUSTODY_KEYS, label);
  return Object.fromEntries(CUSTODY_KEYS.map((key) => [key, boolean(value[key], `${label}.${key}`)]));
}

function parseSubcase(value, label, mode) {
  exactKeys(value, ["id", "passed", "route_attempts", "server_dispatches", "proof_refs", "reason_codes"], label);
  return {
    id: safeIdentifier(value.id, `${label}.id`),
    passed: boolean(value.passed, `${label}.passed`),
    route_attempts: count(value.route_attempts, `${label}.route_attempts`),
    server_dispatches: count(value.server_dispatches, `${label}.server_dispatches`),
    proof_refs: parseProofReferences(value.proof_refs, `${label}.proof_refs`, mode),
    reason_codes: parseReasonCodes(value.reason_codes, `${label}.reason_codes`, mode),
  };
}

function subcaseRank(id) {
  const rank = INVALIDATION_SUBCASE_IDS.indexOf(id);
  if (rank < 0) fail("scenario.subcases contains an unknown id");
  return rank;
}

function parseSubcases(value, scenarioID, label, mode) {
  const source = boundedArray(value, label, INVALIDATION_SUBCASE_IDS.length);
  if (scenarioID !== "capability_invalidation") {
    if (source.length !== 0) fail(`${label} is only admitted for capability_invalidation`);
    return [];
  }
  if (source.length !== INVALIDATION_SUBCASE_IDS.length) fail(`${label} must contain every required invalidation subcase`);
  const subcases = source.map((item, index) => parseSubcase(item, `${label}[${index}]`, mode));
  ensureUnique(subcases, (subcase) => subcase.id, label);
  for (const subcase of subcases) subcaseRank(subcase.id);
  const ordered = orderOrReject(subcases, (left, right) => subcaseRank(left.id) - subcaseRank(right.id), label, mode);
  for (let index = 0; index < INVALIDATION_SUBCASE_IDS.length; index += 1) {
    if (ordered[index].id !== INVALIDATION_SUBCASE_IDS[index]) fail(`${label} must contain every required invalidation subcase`);
  }
  return ordered;
}

function observedDenominatorsMatch(counts) {
  return (
    counts.callbacks_expected === counts.callbacks_observed &&
    counts.route_attempts_expected === counts.route_attempts_observed &&
    counts.deliveries_expected === counts.deliveries_observed
  );
}

function hasHappyPath(scenario) {
  const { counts, route_sequence: routeSequence } = scenario;
  return (
    counts.callbacks_expected === 2 &&
    counts.callbacks_observed === 2 &&
    counts.route_attempts_expected === 4 &&
    counts.route_attempts_observed === 4 &&
    routeSequence.length === HAPPY_ROUTE_SEQUENCE.length &&
    routeSequence.every((token, index) => token === HAPPY_ROUTE_SEQUENCE[index])
  );
}

function countsMatch(counts, expected) {
  return Object.entries(expected).every(([key, value]) => counts[key] === value);
}

function routeSequenceMatches(actual, expected) {
  return actual.length === expected.length && actual.every((token, index) => token === expected[index]);
}

function newArtifactCustodyIsClean(custody) {
  return !custody.extension_url_present && !custody.extension_token_present &&
    !custody.extension_config_path_present && custody.child_hap_config_present;
}

function scenarioMeetsQualificationContract(scenario) {
  const counts = scenario.counts;
  if (!scenario.shared_deadline || counts.direct_fallback_attempts !== 0 || counts.cleanup_residue !== 0) return false;
  switch (scenario.id) {
    case "new_new_happy":
      return hasHappyPath(scenario) && newArtifactCustodyIsClean(scenario.custody) && countsMatch(counts, {
        deliveries_expected: 2,
        deliveries_observed: 2,
        rediscoveries: 0,
        server_dispatches: 4,
        telemetry_attempts: 2,
      });
    case "new_plugin_old_daemon":
      return newArtifactCustodyIsClean(scenario.custody) && routeSequenceMatches(scenario.route_sequence, []) && countsMatch(counts, {
        callbacks_expected: 2,
        callbacks_observed: 2,
        route_attempts_expected: 0,
        route_attempts_observed: 0,
        deliveries_expected: 0,
        deliveries_observed: 0,
        rediscoveries: 0,
        server_dispatches: 0,
        telemetry_attempts: 0,
      });
    case "old_plugin_new_daemon":
      return scenario.custody.extension_url_present && scenario.custody.extension_token_present &&
        scenario.custody.authorization_seen && !scenario.custody.child_hap_config_present &&
        routeSequenceMatches(scenario.route_sequence, []) && countsMatch(counts, {
          callbacks_expected: 2,
          callbacks_observed: 2,
          route_attempts_expected: 0,
          route_attempts_observed: 0,
          deliveries_expected: 2,
          deliveries_observed: 2,
        });
    case "relay_outage":
      return newArtifactCustodyIsClean(scenario.custody) && routeSequenceMatches(scenario.route_sequence, [
        "session_start:IDENTITY_REGISTRATION:NO_DELIVERY",
        "before_agent_start:IDENTITY_REGISTRATION:NO_DELIVERY",
      ]) && countsMatch(counts, {
        callbacks_expected: 2,
        callbacks_observed: 2,
        route_attempts_expected: 2,
        route_attempts_observed: 2,
        deliveries_expected: 0,
        deliveries_observed: 0,
        rediscoveries: 0,
        server_dispatches: 0,
        telemetry_attempts: 0,
      });
    case "stale_generation":
      return newArtifactCustodyIsClean(scenario.custody) && routeSequenceMatches(scenario.route_sequence, [
        "session_start:IDENTITY_REGISTRATION:NO_DELIVERY",
        "session_start:IDENTITY_REGISTRATION:OK",
        "session_start:SESSION_START_CONTEXT:OK",
        "before_agent_start:IDENTITY_REGISTRATION:OK",
        "before_agent_start:AMBIENT_CANDIDATES:OK",
      ]) && countsMatch(counts, {
        callbacks_expected: 2,
        callbacks_observed: 2,
        route_attempts_expected: 5,
        route_attempts_observed: 5,
        deliveries_expected: 2,
        deliveries_observed: 2,
        rediscoveries: 1,
        server_dispatches: 4,
        telemetry_attempts: 2,
      });
    case "capability_invalidation":
      return newArtifactCustodyIsClean(scenario.custody) && countsMatch(counts, {
        callbacks_expected: 0,
        callbacks_observed: 0,
        deliveries_expected: 0,
        deliveries_observed: 0,
      }) && scenario.subcases.every((subcase) => subcase.passed && subcase.route_attempts >= 1 && subcase.server_dispatches === 0);
    case "rollback_future_turn":
      return newArtifactCustodyIsClean(scenario.custody) && routeSequenceMatches(scenario.route_sequence, [
        "session_start:IDENTITY_REGISTRATION:NO_DELIVERY",
        "before_agent_start:IDENTITY_REGISTRATION:NO_DELIVERY",
        ...HAPPY_ROUTE_SEQUENCE,
      ]) && countsMatch(counts, {
        callbacks_expected: 4,
        callbacks_observed: 4,
        route_attempts_expected: 6,
        route_attempts_observed: 6,
        deliveries_expected: 2,
        deliveries_observed: 2,
        rediscoveries: 0,
        server_dispatches: 4,
        telemetry_attempts: 2,
      });
    default:
      return false;
  }
}

function artifactMatrixIsComplete(artifact) {
  return artifact.archives.length === 3 && new Set(artifact.archives.map((archive) => archive.platform)).size === 3 &&
    ["darwin", "linux", "win32"].every((platform) => artifact.archives.some((archive) => archive.platform === platform));
}

function parseScenario(value, label, mode) {
  exactKeys(value, ["id", "state", "passed", "counts", "route_sequence", "shared_deadline", "custody", "subcases", "proof_refs", "reason_codes"], label);
  const id = enumValue(value.id, new Set(SCENARIO_IDS), `${label}.id`);
  const state = enumValue(value.state, STATE_SET, `${label}.state`);
  const passed = boolean(value.passed, `${label}.passed`);
  const counts = parseCounts(value.counts, `${label}.counts`);
  const routeSequence = parseRouteSequence(value.route_sequence, `${label}.route_sequence`);
  const sharedDeadline = boolean(value.shared_deadline, `${label}.shared_deadline`);
  const custody = parseCustody(value.custody, `${label}.custody`);
  const subcases = parseSubcases(value.subcases, id, `${label}.subcases`, mode);
  const proofRefs = parseProofReferences(value.proof_refs, `${label}.proof_refs`, mode);
  const reasonCodes = parseReasonCodes(value.reason_codes, `${label}.reason_codes`, mode);

  if (state !== "OBSERVED" && passed) fail(`${label}.passed requires OBSERVED state`);
  if (state === "OBSERVED" && !observedDenominatorsMatch(counts)) fail(`${label}.counts must reconcile observed denominators`);
  if (routeSequence.length !== counts.route_attempts_observed) fail(`${label}.route_sequence must match route_attempts_observed`);
  if (passed && counts.direct_fallback_attempts !== 0) fail(`${label}.passed is incompatible with direct fallback`);
  if (passed && counts.cleanup_residue !== 0) fail(`${label}.passed is incompatible with cleanup residue`);
  if (passed && id === "capability_invalidation" && subcases.some((subcase) => !subcase.passed)) {
    fail(`${label}.passed requires every invalidation subcase to pass`);
  }

  const scenario = {
    id,
    state,
    passed,
    counts,
    route_sequence: routeSequence,
    shared_deadline: sharedDeadline,
    custody,
    subcases,
    proof_refs: proofRefs,
    reason_codes: reasonCodes,
  };
  if (scenario.id === "new_new_happy" && scenario.passed && !hasHappyPath(scenario)) {
    fail(`${label} does not contain the required happy-path relay sequence`);
  }
  return scenario;
}

function scenarioRank(id) {
  const rank = SCENARIO_IDS.indexOf(id);
  if (rank < 0) fail("scenarios contains an unknown id");
  return rank;
}

function parseScenarios(value, mode) {
  const source = boundedArray(value, "scenarios", SCENARIO_IDS.length, SCENARIO_IDS.length);
  const scenarios = source.map((item, index) => parseScenario(item, `scenarios[${index}]`, mode));
  ensureUnique(scenarios, (scenario) => scenario.id, "scenarios");
  const ordered = orderOrReject(scenarios, (left, right) => scenarioRank(left.id) - scenarioRank(right.id), "scenarios", mode);
  for (let index = 0; index < SCENARIO_IDS.length; index += 1) {
    if (ordered[index].id !== SCENARIO_IDS[index]) fail("scenarios must contain every required id");
  }
  return ordered;
}

function parseGuarantee(value, key, mode) {
  exactKeys(value, ["state", "proof_refs", "reason_codes"], `guarantees.${key}`);
  return {
    state: enumValue(value.state, STATE_SET, `guarantees.${key}.state`),
    proof_refs: parseProofReferences(value.proof_refs, `guarantees.${key}.proof_refs`, mode),
    reason_codes: parseReasonCodes(value.reason_codes, `guarantees.${key}.reason_codes`, mode),
  };
}

function parseGuarantees(value, mode) {
  exactKeys(value, GUARANTEES, "guarantees");
  return Object.fromEntries(GUARANTEES.map((key) => [key, parseGuarantee(value[key], key, mode)]));
}

function parseEffects(value) {
  exactKeys(value, ["active_profile_before_sha256", "active_profile_after_sha256", "production_mutation", "real_credentials_used", "cleanup_residue"], "effects");
  return {
    active_profile_before_sha256: digest(value.active_profile_before_sha256, "effects.active_profile_before_sha256"),
    active_profile_after_sha256: digest(value.active_profile_after_sha256, "effects.active_profile_after_sha256"),
    production_mutation: boolean(value.production_mutation, "effects.production_mutation"),
    real_credentials_used: boolean(value.real_credentials_used, "effects.real_credentials_used"),
    cleanup_residue: count(value.cleanup_residue, "effects.cleanup_residue"),
  };
}

function assertProofReferencesResolve(fields) {
  const evidenceByID = new Map(fields.evidence_refs.map((reference) => [reference.id, reference]));
  const assertEntry = (entry, label) => {
    for (const reference of entry.proof_refs) {
      const evidence = evidenceByID.get(reference.id);
      if (!evidence || evidence.class !== reference.class || evidence.sha256 !== reference.sha256) {
        fail(`${label}.proof_refs contains an unresolved reference`);
      }
    }
  };
  for (const key of GUARANTEES) assertEntry(fields.guarantees[key], `guarantees.${key}`);
  for (const scenario of fields.scenarios) {
    assertEntry(scenario, `scenarios.${scenario.id}`);
    for (const subcase of scenario.subcases) assertEntry(subcase, `scenarios.${scenario.id}.subcases.${subcase.id}`);
  }
}

function parseQualificationFields(value, mode) {
  const fields = {
    run_id: safeIdentifier(value.run_id, "run_id"),
    generated_at: timestamp(value.generated_at, "generated_at"),
    candidate: parseCandidate(value.candidate),
    host: parseHost(value.host),
    artifact: parseArtifact(value.artifact, mode),
    scenarios: parseScenarios(value.scenarios, mode),
    guarantees: parseGuarantees(value.guarantees, mode),
    effects: parseEffects(value.effects),
    evidence_refs: parseEvidenceReferences(value.evidence_refs, "evidence_refs", mode),
  };
  if (!fields.host.scratch) fail("host.scratch must be true for qualification");
  if (fields.candidate.adapter_sha256 !== fields.artifact.adapter_sha256) {
    fail("candidate.adapter_sha256 must match artifact.adapter_sha256");
  }
  assertProofReferencesResolve(fields);
  return fields;
}

function proofHasInstalledRuntime(entry) {
  return entry.proof_refs.some((reference) => reference.class === INSTALLED_RUNTIME);
}

function qualificationEntries(fields) {
  const entries = [];
  for (const key of GUARANTEES) entries.push({ kind: "guarantee", id: key, entry: fields.guarantees[key] });
  for (const scenario of fields.scenarios) {
    entries.push({ kind: "scenario", id: scenario.id, entry: scenario });
    for (const subcase of scenario.subcases) {
      entries.push({ kind: "subcase", id: `capability_invalidation_${subcase.id}`, entry: subcase });
    }
  }
  return entries;
}

function compareGap(left, right) {
  const scopeRank = GAP_SCOPES.indexOf(left.scope) - GAP_SCOPES.indexOf(right.scope);
  return scopeRank || left.id.localeCompare(right.id) || left.state.localeCompare(right.state);
}

function deriveGapsFromFields(fields) {
  const gaps = [];
  for (const key of GUARANTEES) {
    const guarantee = fields.guarantees[key];
    if (guarantee.state !== "OBSERVED") gaps.push({ scope: "GUARANTEE", id: key, state: guarantee.state });
  }
  for (const scenario of fields.scenarios) {
    if (scenario.state !== "OBSERVED" || !scenario.passed) {
      gaps.push({ scope: "SCENARIO", id: scenario.id, state: scenario.state });
    } else if (!scenarioMeetsQualificationContract(scenario)) {
      gaps.push({ scope: "SCENARIO", id: scenario.id, state: "CONTRADICTED" });
    }
    for (const subcase of scenario.subcases) {
      if (!subcase.passed || subcase.route_attempts < 1 || subcase.server_dispatches !== 0) {
        gaps.push({ scope: "SUBCASE", id: `capability_invalidation_${subcase.id}`, state: "CONTRADICTED" });
      }
    }
  }
  if (fields.effects.active_profile_before_sha256 !== fields.effects.active_profile_after_sha256) {
    gaps.push({ scope: "EFFECT", id: "active_profile_hash", state: "CONTRADICTED" });
  }
  if (fields.effects.production_mutation) gaps.push({ scope: "EFFECT", id: "production_mutation", state: "CONTRADICTED" });
  if (fields.effects.real_credentials_used) gaps.push({ scope: "EFFECT", id: "real_credentials_used", state: "CONTRADICTED" });
  if (fields.effects.cleanup_residue !== 0) gaps.push({ scope: "EFFECT", id: "cleanup_residue", state: "CONTRADICTED" });
  for (const scenario of fields.scenarios) {
    if (scenario.counts.direct_fallback_attempts !== 0) {
      gaps.push({ scope: "EFFECT", id: `direct_fallback_${scenario.id}`, state: "CONTRADICTED" });
    }
    if (scenario.counts.cleanup_residue !== 0) {
      gaps.push({ scope: "EFFECT", id: `scenario_cleanup_${scenario.id}`, state: "CONTRADICTED" });
    }
  }
  if (!artifactMatrixIsComplete(fields.artifact)) {
    gaps.push({ scope: "EVIDENCE", id: "artifact_archive_matrix", state: "CONTRADICTED" });
  }
  for (const item of qualificationEntries(fields)) {
    if (!proofHasInstalledRuntime(item.entry)) {
      gaps.push({ scope: "EVIDENCE", id: `${item.kind}_${item.id}`, state: "INFERRED" });
    }
  }
  return gaps.sort(compareGap);
}

function deriveReasonsFromFields(fields) {
  const reasons = new Set();
  for (const key of GUARANTEES) {
    const guarantee = fields.guarantees[key];
    if (guarantee.state !== "OBSERVED") reasons.add(`GUARANTEE_${key.toUpperCase()}_${guarantee.state}`);
    if (!proofHasInstalledRuntime(guarantee)) reasons.add(`MISSING_INSTALLED_RUNTIME_GUARANTEE_${key.toUpperCase()}`);
  }
  for (const scenario of fields.scenarios) {
    const scenarioName = scenario.id.toUpperCase();
    if (scenario.state !== "OBSERVED") reasons.add(`SCENARIO_${scenarioName}_${scenario.state}`);
    else if (!scenario.passed) reasons.add(`SCENARIO_${scenarioName}_FAILED`);
    else if (!scenarioMeetsQualificationContract(scenario)) reasons.add(`SCENARIO_${scenarioName}_CONTRACT_MISMATCH`);
    if (scenario.counts.direct_fallback_attempts !== 0) reasons.add(`SCENARIO_${scenarioName}_DIRECT_FALLBACK`);
    if (scenario.counts.cleanup_residue !== 0) reasons.add(`SCENARIO_${scenarioName}_CLEANUP_RESIDUE`);
    if (!proofHasInstalledRuntime(scenario)) reasons.add(`MISSING_INSTALLED_RUNTIME_SCENARIO_${scenarioName}`);
    for (const subcase of scenario.subcases) {
      const subcaseName = subcase.id.toUpperCase();
      if (!subcase.passed || subcase.route_attempts < 1 || subcase.server_dispatches !== 0) reasons.add(`SUBCASE_CAPABILITY_INVALIDATION_${subcaseName}_FAILED`);
      if (!proofHasInstalledRuntime(subcase)) reasons.add(`MISSING_INSTALLED_RUNTIME_SUBCASE_CAPABILITY_INVALIDATION_${subcaseName}`);
    }
  }
  if (fields.effects.active_profile_before_sha256 !== fields.effects.active_profile_after_sha256) reasons.add("EFFECT_ACTIVE_PROFILE_HASH_CHANGED");
  if (fields.effects.production_mutation) reasons.add("EFFECT_PRODUCTION_MUTATION");
  if (fields.effects.real_credentials_used) reasons.add("EFFECT_REAL_CREDENTIALS_USED");
  if (fields.effects.cleanup_residue !== 0) reasons.add("EFFECT_CLEANUP_RESIDUE");
  if (!artifactMatrixIsComplete(fields.artifact)) reasons.add("ARTIFACT_ARCHIVE_MATRIX_INCOMPLETE");
  if (reasons.size === 0) reasons.add("ALL_QUALIFICATION_CRITERIA_OBSERVED");
  return [...reasons].sort();
}

function hasHardFailure(fields) {
  if (GUARANTEES.some((key) => ["MISSING", "CONTRADICTED"].includes(fields.guarantees[key].state))) return true;
  if (!artifactMatrixIsComplete(fields.artifact)) return true;
  for (const scenario of fields.scenarios) {
    if (["MISSING", "CONTRADICTED"].includes(scenario.state)) return true;
    if (scenario.state === "OBSERVED" && (!scenario.passed || !scenarioMeetsQualificationContract(scenario))) return true;
    if (scenario.counts.direct_fallback_attempts !== 0 || scenario.counts.cleanup_residue !== 0) return true;
  }
  return (
    fields.effects.active_profile_before_sha256 !== fields.effects.active_profile_after_sha256 ||
    fields.effects.production_mutation ||
    fields.effects.real_credentials_used ||
    fields.effects.cleanup_residue !== 0
  );
}

function hasInconclusiveEvidence(fields) {
  if (GUARANTEES.some((key) => ["UNAVAILABLE", "INFERRED", "PROHIBITED"].includes(fields.guarantees[key].state))) return true;
  if (fields.scenarios.some((scenario) => ["UNAVAILABLE", "INFERRED", "PROHIBITED"].includes(scenario.state))) return true;
  return qualificationEntries(fields).some((item) => !proofHasInstalledRuntime(item.entry));
}

function qualifies(fields) {
  return (
    artifactMatrixIsComplete(fields.artifact) &&
    GUARANTEES.every((key) => fields.guarantees[key].state === "OBSERVED" && proofHasInstalledRuntime(fields.guarantees[key])) &&
    fields.scenarios.every((scenario) => scenario.state === "OBSERVED" && scenario.passed &&
      scenarioMeetsQualificationContract(scenario) && proofHasInstalledRuntime(scenario)) &&
    fields.scenarios.every((scenario) => scenario.subcases.every((subcase) => subcase.passed &&
      subcase.route_attempts >= 1 && subcase.server_dispatches === 0 && proofHasInstalledRuntime(subcase))) &&
    fields.effects.active_profile_before_sha256 === fields.effects.active_profile_after_sha256 &&
    !fields.effects.production_mutation &&
    !fields.effects.real_credentials_used &&
    fields.effects.cleanup_residue === 0
  );
}

function classifyFields(fields) {
  if (hasHardFailure(fields)) return "UNQUALIFIED";
  if (hasInconclusiveEvidence(fields)) return "INCONCLUSIVE";
  if (qualifies(fields)) return "QUALIFIED";
  return "UNQUALIFIED";
}

function parseGap(value, label) {
  exactKeys(value, ["scope", "id", "state"], label);
  return {
    scope: enumValue(value.scope, GAP_SCOPE_SET, `${label}.scope`),
    id: safeIdentifier(value.id, `${label}.id`),
    state: enumValue(value.state, STATE_SET, `${label}.state`),
  };
}

function parseGaps(value, fields) {
  const gaps = boundedArray(value, "gaps", MAX_GAPS).map((item, index) => parseGap(item, `gaps[${index}]`));
  ensureUnique(gaps, (gap) => `${gap.scope}:${gap.id}:${gap.state}`, "gaps");
  const expected = deriveGapsFromFields(fields);
  if (canonicalize(gaps) !== canonicalize(expected)) fail("gaps do not match the qualification facts");
  return expected;
}

function parseBuildInput(value) {
  exactKeys(value, BUILD_INPUT_KEYS, "qualification input");
  return parseQualificationFields(value, CANONICAL_INPUT);
}

function parseRecordShape(value) {
  exactKeys(value, RECORD_KEYS, "qualification record");
  if (value.schema !== SCHEMA) fail("record.schema is unsupported");
  if (value.capability !== CAPABILITY) fail("record.capability is unsupported");
  const fields = parseQualificationFields(value, CANONICAL_RECORD);
  const gaps = parseGaps(value.gaps, fields);
  const disposition = enumValue(value.disposition, DISPOSITION_SET, "record.disposition");
  const expectedDisposition = classifyFields(fields);
  if (disposition !== expectedDisposition) fail("record.disposition does not match the qualification facts");
  return {
    schema: SCHEMA,
    capability: CAPABILITY,
    run_id: fields.run_id,
    generated_at: fields.generated_at,
    candidate: fields.candidate,
    host: fields.host,
    artifact: fields.artifact,
    scenarios: fields.scenarios,
    guarantees: fields.guarantees,
    gaps,
    disposition,
    effects: fields.effects,
    evidence_refs: fields.evidence_refs,
    record_sha256: digest(value.record_sha256, "record.record_sha256"),
  };
}

function canonicalSerialize(record) {
  if (!isPlainObject(record)) fail("record must be an object");
  const payload = {};
  for (const key of Object.keys(record)) {
    if (key !== "record_sha256") payload[key] = record[key];
  }
  return canonicalize(payload);
}

function canonicalDigest(record) {
  return sha256(canonicalSerialize(record));
}

function verifyDigest(record) {
  try {
    const parsed = parseRecordShape(record);
    return crypto.timingSafeEqual(Buffer.from(parsed.record_sha256, "hex"), Buffer.from(canonicalDigest(parsed), "hex"));
  } catch {
    return false;
  }
}

function parseQualificationRecord(value) {
  const parsed = parseRecordShape(value);
  if (!crypto.timingSafeEqual(Buffer.from(parsed.record_sha256, "hex"), Buffer.from(canonicalDigest(parsed), "hex"))) {
    fail("record.record_sha256 does not match canonical payload");
  }
  return deepFreeze(parsed);
}

function buildQualificationRecord(input) {
  const fields = parseBuildInput(input);
  const record = {
    schema: SCHEMA,
    capability: CAPABILITY,
    run_id: fields.run_id,
    generated_at: fields.generated_at,
    candidate: fields.candidate,
    host: fields.host,
    artifact: fields.artifact,
    scenarios: fields.scenarios,
    guarantees: fields.guarantees,
    gaps: deriveGapsFromFields(fields),
    disposition: classifyFields(fields),
    effects: fields.effects,
    evidence_refs: fields.evidence_refs,
    record_sha256: "0".repeat(64),
  };
  record.record_sha256 = canonicalDigest(record);
  return parseQualificationRecord(record);
}

function parseDerivationSubject(value) {
  if (!isPlainObject(value)) fail("qualification assessment must be an object");
  if (Object.prototype.hasOwnProperty.call(value, "schema")) return parseRecordShape(value);
  if (Object.prototype.hasOwnProperty.call(value, "run_id")) return parseBuildInput(value);
  exactKeys(value, ASSESSMENT_KEYS, "qualification assessment");
  return {
    scenarios: parseScenarios(value.scenarios, CANONICAL_INPUT),
    guarantees: parseGuarantees(value.guarantees, CANONICAL_INPUT),
    effects: parseEffects(value.effects),
  };
}

function deriveDisposition(value) {
  return classifyFields(parseDerivationSubject(value));
}

function deriveGaps(value) {
  return deepFreeze(deriveGapsFromFields(parseDerivationSubject(value)));
}

function deriveReasons(value) {
  return deepFreeze(deriveReasonsFromFields(parseDerivationSubject(value)));
}

function parseBoundaryFailure(value) {
  exactKeys(value, ["error_class", "message_sha256"], "boundary failure");
  return {
    error_class: reasonCode(value.error_class, "boundary failure.error_class"),
    message_sha256: digest(value.message_sha256, "boundary failure.message_sha256"),
  };
}

function boundaryFailure(errorClass, message) {
  return deepFreeze({
    error_class: reasonCode(errorClass, "error_class"),
    message_sha256: sha256(typeof message === "string" ? message : String(message)),
  });
}

function exitFor(value) {
  if (isPlainObject(value) && Object.keys(value).length === 2 && Object.prototype.hasOwnProperty.call(value, "error_class") && Object.prototype.hasOwnProperty.call(value, "message_sha256")) {
    parseBoundaryFailure(value);
    return EXIT_CODES.BOUNDARY_FAILURE;
  }
  return EXIT_CODES[parseQualificationRecord(value).disposition];
}

const buildCapabilityRecord = buildQualificationRecord;
const parseCapabilityRecord = parseQualificationRecord;

module.exports = {
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
  canonicalize,
  deriveDisposition,
  deriveGaps,
  deriveReasons,
  exitFor,
  parseCapabilityRecord,
  parseQualificationRecord,
  sha256,
  verifyDigest,
};
