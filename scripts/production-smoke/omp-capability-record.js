#!/usr/bin/env node
"use strict";

const crypto = require("node:crypto");

const SCHEMA = "omp-advisor-1";
const CAPABILITY = "installed-omp-capability";
const GUARANTEES = Object.freeze([
  "installed_artifact",
  "session_start_prewarm",
  "before_agent_start_anchor",
  "callback_budget",
  "renderer_untrusted_reference",
  "daemon_authenticated_initialize",
  "direct_credential_disabled",
]);
const STATES = Object.freeze(["OBSERVED", "MISSING", "CONTRADICTED", "UNAVAILABLE", "INFERRED", "PROHIBITED"]);
const PROOF_CLASSES = Object.freeze(["INSTALLED_ARTIFACT", "INSTALLED_RUNTIME", "SOURCE_DIAGNOSTIC", "INFERENCE"]);
const DISPOSITIONS = Object.freeze(["QUALIFIED", "UNQUALIFIED", "INCONCLUSIVE"]);
const EXIT_CODES = Object.freeze({ QUALIFIED: 0, UNQUALIFIED: 20, INCONCLUSIVE: 21, BOUNDARY_FAILURE: 10 });

const SHA256 = /^[a-f0-9]{64}$/;
const SAFE_IDENTIFIER = /^[a-z][a-z0-9_-]{0,95}$/;
const SAFE_ASSET = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const SAFE_REASON = /^[A-Z][A-Z0-9_]{0,127}$/;
const SAFE_VERSION = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/;
const INSTALLED_PROOF_CLASSES = new Set(["INSTALLED_ARTIFACT", "INSTALLED_RUNTIME"]);

class CapabilityRecordError extends Error {
  constructor(message) {
    super(message);
    this.name = "CapabilityRecordError";
  }
}

function fail(message) {
  throw new CapabilityRecordError(message);
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

function string(value, label, expression = null) {
  if (typeof value !== "string" || value.length === 0 || (expression && !expression.test(value))) {
    fail(`${label} is invalid`);
  }
  return value;
}

function sha256(value) {
  return crypto.createHash("sha256").update(value).digest("hex");
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

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

function relativeEntrypoint(value) {
  string(value, "artifact.relative_entrypoint");
  if (value.includes("\\") || value.startsWith("/") || value.split("/").includes("..") || value.split("/").includes(".")) {
    fail("artifact.relative_entrypoint must be a normalized relative path");
  }
  return value;
}

function parseEvidenceReference(value, label) {
  exactKeys(value, ["id", "class", "sha256"], label);
  const id = string(value.id, `${label}.id`, SAFE_IDENTIFIER);
  const proofClass = string(value.class, `${label}.class`);
  if (!PROOF_CLASSES.includes(proofClass)) fail(`${label}.class is not admitted`);
  const digest = string(value.sha256, `${label}.sha256`, SHA256);
  return { id, class: proofClass, sha256: digest };
}

function compareEvidenceReference(left, right) {
  return left.id.localeCompare(right.id) || left.class.localeCompare(right.class) || left.sha256.localeCompare(right.sha256);
}

function parseProofReferences(value, label) {
  if (!Array.isArray(value) || value.length === 0) fail(`${label} must be a non-empty array`);
  const refs = value.map((item, index) => parseEvidenceReference(item, `${label}[${index}]`));
  for (let index = 1; index < refs.length; index += 1) {
    if (compareEvidenceReference(refs[index - 1], refs[index]) >= 0) fail(`${label} must be unique and sorted`);
  }
  return refs;
}

function parseReasonCodes(value, label) {
  if (!Array.isArray(value)) fail(`${label} must be an array`);
  const reasons = value.map((reason, index) => string(reason, `${label}[${index}]`, SAFE_REASON));
  for (let index = 1; index < reasons.length; index += 1) {
    if (reasons[index - 1] >= reasons[index]) fail(`${label} must be unique and sorted`);
  }
  return reasons;
}

function parseGuarantee(value, key) {
  exactKeys(value, ["state", "proof_refs", "reason_codes"], `guarantees.${key}`);
  const state = string(value.state, `guarantees.${key}.state`);
  if (!STATES.includes(state)) fail(`guarantees.${key}.state is not admitted`);
  return {
    state,
    proof_refs: parseProofReferences(value.proof_refs, `guarantees.${key}.proof_refs`),
    reason_codes: parseReasonCodes(value.reason_codes, `guarantees.${key}.reason_codes`),
  };
}

function parseGuarantees(value) {
  exactKeys(value, GUARANTEES, "guarantees");
  return Object.fromEntries(GUARANTEES.map((key) => [key, parseGuarantee(value[key], key)]));
}

function parseHost(value) {
  exactKeys(value, ["platform", "arch"], "host");
  return {
    platform: string(value.platform, "host.platform", SAFE_IDENTIFIER),
    arch: string(value.arch, "host.arch", SAFE_IDENTIFIER),
  };
}

function parseArtifact(value) {
  exactKeys(value, [
    "package_version",
    "package_manifest_sha256",
    "omp_manifest_sha256",
    "extension_sha256",
    "bootstrap_policy_sha256",
    "daemon_object_sha256",
    "daemon_object_size",
    "relative_entrypoint",
    "daemon_asset",
  ], "artifact");
  if (!Number.isSafeInteger(value.daemon_object_size) || value.daemon_object_size <= 0) {
    fail("artifact.daemon_object_size is invalid");
  }
  return {
    package_version: string(value.package_version, "artifact.package_version", SAFE_VERSION),
    package_manifest_sha256: string(value.package_manifest_sha256, "artifact.package_manifest_sha256", SHA256),
    omp_manifest_sha256: string(value.omp_manifest_sha256, "artifact.omp_manifest_sha256", SHA256),
    extension_sha256: string(value.extension_sha256, "artifact.extension_sha256", SHA256),
    bootstrap_policy_sha256: string(value.bootstrap_policy_sha256, "artifact.bootstrap_policy_sha256", SHA256),
    daemon_object_sha256: string(value.daemon_object_sha256, "artifact.daemon_object_sha256", SHA256),
    daemon_object_size: value.daemon_object_size,
    relative_entrypoint: relativeEntrypoint(value.relative_entrypoint),
    daemon_asset: string(value.daemon_asset, "artifact.daemon_asset", SAFE_ASSET),
  };
}

function parseProbe(value) {
  exactKeys(value, ["receipt_id", "run_id", "evidence_digest"], "probe");
  return {
    receipt_id: string(value.receipt_id, "probe.receipt_id", SAFE_IDENTIFIER),
    run_id: string(value.run_id, "probe.run_id", SAFE_IDENTIFIER),
    evidence_digest: string(value.evidence_digest, "probe.evidence_digest", SHA256),
  };
}

function parseEffects(value) {
  exactKeys(value, ["profile_registry_unchanged", "configuration_unchanged", "probe_writes_confined", "scratch_cleaned"], "effects");
  for (const [key, assertion] of Object.entries(value)) {
    if (typeof assertion !== "boolean") fail(`effects.${key} must be boolean`);
  }
  return { ...value };
}

function installedProof(entry) {
  return entry.proof_refs.some((reference) => INSTALLED_PROOF_CLASSES.has(reference.class));
}

function installedRuntimeProof(entry) {
  return entry.proof_refs.some((reference) => reference.class === "INSTALLED_RUNTIME");
}

function guaranteeReason(key, suffix) {
  return `GUARANTEE_${key.toUpperCase()}_${suffix}`;
}

function classifyGuarantees(guaranteesInput) {
  const guarantees = parseGuarantees(guaranteesInput);
  const gaps = GUARANTEES
    .filter((key) => guarantees[key].state !== "OBSERVED")
    .map((key) => ({ guarantee: key, state: guarantees[key].state }));
  const direct = guarantees.direct_credential_disabled;

  if (direct.state === "CONTRADICTED" && installedRuntimeProof(direct)) {
    return {
      disposition: "UNQUALIFIED",
      reasons: ["DIRECT_INSTALLED_RUNTIME_REST_AUTHORIZATION"],
      gaps,
    };
  }

  const prohibited = GUARANTEES.filter((key) => guarantees[key].state === "PROHIBITED");
  if (prohibited.length > 0) {
    return {
      disposition: "INCONCLUSIVE",
      reasons: prohibited.map((key) => guaranteeReason(key, "PROHIBITED_EFFECT_BOUNDARY")).sort(),
      gaps,
    };
  }

  const inferred = GUARANTEES.filter((key) => guarantees[key].state === "INFERRED");
  const unavailable = GUARANTEES.filter((key) => guarantees[key].state === "UNAVAILABLE");
  const sourceOnly = GUARANTEES.filter((key) => guarantees[key].state === "OBSERVED" && !installedProof(guarantees[key]));
  const unprovenFailure = GUARANTEES.filter((key) => !["OBSERVED", "UNAVAILABLE"].includes(guarantees[key].state) && !installedProof(guarantees[key]));
  if (inferred.length > 0 || unavailable.length > 0 || sourceOnly.length > 0 || unprovenFailure.length > 0) {
    const reasons = [
      ...inferred.map((key) => guaranteeReason(key, "INFERRED")),
      ...unavailable.map((key) => guaranteeReason(key, "NOT_EXPOSED")),
      ...sourceOnly.map((key) => guaranteeReason(key, "SOURCE_ONLY")),
      ...unprovenFailure.map((key) => guaranteeReason(key, "NOT_EXPOSED")),
    ].sort();
    return { disposition: "INCONCLUSIVE", reasons, gaps };
  }

  const failures = GUARANTEES.filter((key) => guarantees[key].state !== "OBSERVED");
  if (failures.length > 0) {
    return {
      disposition: "UNQUALIFIED",
      reasons: failures.map((key) => guaranteeReason(key, guarantees[key].state)).sort(),
      gaps,
    };
  }

  return { disposition: "QUALIFIED", reasons: ["ALL_GUARANTEES_OBSERVED"], gaps };
}

function deriveDisposition(guarantees) {
  return classifyGuarantees(guarantees).disposition;
}

function deriveGaps(guarantees) {
  return classifyGuarantees(guarantees).gaps;
}

function deriveReasons(guarantees) {
  return classifyGuarantees(guarantees).reasons;
}

function parseGaps(value, guarantees) {
  if (!Array.isArray(value)) fail("gaps must be an array");
  const expected = deriveGaps(guarantees);
  if (canonicalize(value) !== canonicalize(expected)) fail("gaps do not match guarantees");
  return clone(expected);
}

function parseRecordShape(value) {
  exactKeys(value, ["schema", "capability", "host", "artifact", "probe", "guarantees", "gaps", "disposition", "reasons", "effects", "canonical_sha256"], "record");
  if (value.schema !== SCHEMA) fail("record.schema is unsupported");
  if (value.capability !== CAPABILITY) fail("record.capability is unsupported");
  const host = parseHost(value.host);
  const artifact = parseArtifact(value.artifact);
  const probe = parseProbe(value.probe);
  const guarantees = parseGuarantees(value.guarantees);
  const classification = classifyGuarantees(guarantees);
  const gaps = parseGaps(value.gaps, guarantees);
  const disposition = string(value.disposition, "record.disposition");
  if (!DISPOSITIONS.includes(disposition) || disposition !== classification.disposition) {
    fail("record.disposition does not match guarantees");
  }
  const reasons = parseReasonCodes(value.reasons, "record.reasons");
  if (canonicalize(reasons) !== canonicalize([...classification.reasons].sort())) {
    fail("record.reasons do not match guarantees");
  }
  const effects = parseEffects(value.effects);
  const canonicalSha256 = string(value.canonical_sha256, "record.canonical_sha256", SHA256);
  return {
    schema: SCHEMA,
    capability: CAPABILITY,
    host,
    artifact,
    probe,
    guarantees,
    gaps,
    disposition,
    reasons,
    effects,
    canonical_sha256: canonicalSha256,
  };
}

function canonicalSerialize(record) {
  if (!isPlainObject(record)) fail("record must be an object");
  const payload = { ...record };
  delete payload.canonical_sha256;
  return canonicalize(payload);
}

function canonicalDigest(record) {
  return sha256(canonicalSerialize(record));
}

function verifyDigest(record) {
  try {
    const parsed = parseRecordShape(record);
    return crypto.timingSafeEqual(Buffer.from(parsed.canonical_sha256), Buffer.from(canonicalDigest(parsed)));
  } catch {
    return false;
  }
}

function parseCapabilityRecord(value) {
  const parsed = parseRecordShape(value);
  if (!crypto.timingSafeEqual(Buffer.from(parsed.canonical_sha256), Buffer.from(canonicalDigest(parsed)))) {
    fail("record.canonical_sha256 does not match canonical payload");
  }
  return Object.freeze(parsed);
}

function buildCapabilityRecord(input) {
  exactKeys(input, ["host", "artifact", "probe", "guarantees", "effects"], "record input");
  const host = parseHost(input.host);
  const artifact = parseArtifact(input.artifact);
  const probe = parseProbe(input.probe);
  const guarantees = parseGuarantees(input.guarantees);
  const effects = parseEffects(input.effects);
  const classification = classifyGuarantees(guarantees);
  const record = {
    schema: SCHEMA,
    capability: CAPABILITY,
    host,
    artifact,
    probe,
    guarantees,
    gaps: classification.gaps,
    disposition: classification.disposition,
    reasons: [...classification.reasons].sort(),
    effects,
    canonical_sha256: "0".repeat(64),
  };
  record.canonical_sha256 = canonicalDigest(record);
  return parseCapabilityRecord(record);
}

function boundaryFailure(errorClass, message) {
  const normalizedClass = string(errorClass, "error_class", SAFE_REASON);
  return Object.freeze({
    exit_code: EXIT_CODES.BOUNDARY_FAILURE,
    error_class: normalizedClass,
    message_digest: sha256(typeof message === "string" ? message : String(message)),
  });
}

function exitFor(record) {
  const parsed = parseCapabilityRecord(record);
  return EXIT_CODES[parsed.disposition];
}

module.exports = {
  CAPABILITY,
  DISPOSITIONS,
  EXIT_CODES,
  GUARANTEES,
  PROOF_CLASSES,
  SCHEMA,
  STATES,
  CapabilityRecordError,
  boundaryFailure,
  buildCapabilityRecord,
  canonicalDigest,
  canonicalSerialize,
  canonicalize,
  classifyGuarantees,
  deriveDisposition,
  deriveGaps,
  deriveReasons,
  exitFor,
  parseCapabilityRecord,
  sha256,
  verifyDigest,
};
