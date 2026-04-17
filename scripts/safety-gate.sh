#!/usr/bin/env bash
# safety-gate.sh — Invariant checker for engram v5 cleanup (T006 / Phase 2).
#
# Verifies that vault credentials and static entity counts have not drifted
# before or after each cleanup PR.  Correctness is load-bearing: 13 production
# vault credentials must survive every PR in the 14-PR v5.0.0 sequence.
#
# Usage:
#   ENGRAM_API_TOKEN=<token> bash scripts/safety-gate.sh [--phase=pre-us3|post-us3] \
#       [--baseline <path>] [--skip-pg] [--help]
#
# Exit codes:
#   0  all invariants satisfied
#   1  one or more invariants violated
#   2  configuration / prerequisite error

set -euo pipefail

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEFAULT_BASELINE="${SCRIPT_DIR}/safety-gate-baseline.json"

usage() {
    cat >&2 <<EOF
Usage: ENGRAM_API_TOKEN=<token> bash scripts/safety-gate.sh [OPTIONS]

Options:
  --baseline <path>          Path to baseline JSON file.
                             Default: scripts/safety-gate-baseline.json
  --phase=pre-us3|post-us3   Which credentials invariant to apply.
                             pre-us3  : check vault.credential_count (default before US-3)
                             post-us3 : check entities_post_us3.credentials.exact
                             auto     : detect via presence of credentials table (default)
  --skip-pg                  Skip all Postgres entity checks.
  --help                     Show this message.

Environment:
  ENGRAM_API_TOKEN           Bearer token for the engram server (required).
  SAFETY_GATE_SKIP_PG        Set to any non-empty value to skip Postgres checks.
EOF
}

die_config() {
    echo "SAFETY-GATE CONFIG ERROR: $*" >&2
    exit 2
}

die_prereq() {
    echo "SAFETY-GATE PREREQ ERROR: $*" >&2
    exit 2
}

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------

BASELINE_FILE="${DEFAULT_BASELINE}"
PHASE="auto"
SKIP_PG="${SAFETY_GATE_SKIP_PG:-}"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --help|-h)
            usage
            exit 0
            ;;
        --baseline)
            shift
            [[ $# -eq 0 ]] && die_config "--baseline requires a path argument"
            BASELINE_FILE="$1"
            ;;
        --phase=*)
            PHASE="${1#--phase=}"
            case "${PHASE}" in
                pre-us3|post-us3|auto) ;;
                *)
                    die_config "Invalid --phase value: ${PHASE} (allowed: pre-us3|post-us3|auto)"
                    ;;
            esac
            ;;
        --skip-pg)
            SKIP_PG="1"
            ;;
        *)
            die_config "Unknown flag: $1"
            ;;
    esac
    shift
done

# ---------------------------------------------------------------------------
# Prerequisites
# ---------------------------------------------------------------------------

command -v jq  >/dev/null 2>&1 || die_prereq "jq is required but not found in PATH"
command -v curl >/dev/null 2>&1 || die_prereq "curl is required but not found in PATH"

if [[ -z "${SKIP_PG}" ]]; then
    command -v docker >/dev/null 2>&1 || die_prereq "docker is required for Postgres checks (use --skip-pg to bypass)"
fi

[[ -f "${BASELINE_FILE}" ]] || die_config "Baseline file not found: ${BASELINE_FILE}"

if [[ -z "${ENGRAM_API_TOKEN:-}" ]]; then
    echo "SAFETY-GATE CONFIG ERROR: ENGRAM_API_TOKEN environment variable is required" >&2
    exit 2
fi

# ---------------------------------------------------------------------------
# Load baseline
# ---------------------------------------------------------------------------

# Validate JSON structure BEFORE extracting fields — corrupt/invalid JSON
# causes jq to return exit code 4, which under `set -euo pipefail` aborts
# the script uncontrollably (bypasses our exit-code contract of 0/1/2).
if ! jq -e . "${BASELINE_FILE}" >/dev/null 2>&1; then
    die_config "Baseline file is not valid JSON: ${BASELINE_FILE}"
fi

SERVER_URL="$(jq -r '.server_url // empty' "${BASELINE_FILE}")"
VAULT_FP="$(jq -r '.vault.fingerprint // empty' "${BASELINE_FILE}")"
VAULT_CRED_COUNT="$(jq -r '.vault.credential_count // empty' "${BASELINE_FILE}")"
VAULT_MISMATCH_MAX="$(jq -r '.vault.mismatch_count_max // 0' "${BASELINE_FILE}")"
PG_CONTAINER="$(jq -r '.postgres.container // empty' "${BASELINE_FILE}")"
PG_USER="$(jq -r '.postgres.user // empty' "${BASELINE_FILE}")"
PG_DB="$(jq -r '.postgres.database // empty' "${BASELINE_FILE}")"
POST_US3_CRED_COUNT="$(jq -r '.entities_post_us3.credentials.exact // empty' "${BASELINE_FILE}")"

# Validate required fields are present (after fallback to empty). Postgres
# fields only required when --skip-pg is NOT set.
[[ -n "${SERVER_URL}" ]]        || die_config "Missing required baseline field: .server_url"
[[ -n "${VAULT_FP}" ]]          || die_config "Missing required baseline field: .vault.fingerprint"
[[ -n "${VAULT_CRED_COUNT}" ]]  || die_config "Missing required baseline field: .vault.credential_count"
if [[ -z "${SKIP_PG}" ]]; then
    [[ -n "${PG_CONTAINER}" ]] || die_config "Missing required baseline field: .postgres.container"
    [[ -n "${PG_USER}" ]]      || die_config "Missing required baseline field: .postgres.user"
    [[ -n "${PG_DB}" ]]        || die_config "Missing required baseline field: .postgres.database"
fi

# ---------------------------------------------------------------------------
# Phase detection
# ---------------------------------------------------------------------------

resolve_phase() {
    if [[ "${PHASE}" != "auto" ]]; then
        echo "${PHASE}"
        return
    fi
    if [[ -n "${SKIP_PG}" ]]; then
        # Cannot query DB; fall back to pre-us3 assumption.
        echo "pre-us3"
        return
    fi
    local result
    result="$(docker exec "${PG_CONTAINER}" \
        psql -U "${PG_USER}" -d "${PG_DB}" -tAc \
        "SELECT to_regclass('public.credentials')" 2>/dev/null || true)"
    if [[ -n "${result}" ]]; then
        echo "post-us3"
    else
        echo "pre-us3"
    fi
}

RESOLVED_PHASE="$(resolve_phase)"

# ---------------------------------------------------------------------------
# Tracking failures
# ---------------------------------------------------------------------------

FAILURES=()
SUMMARY_PARTS=()

record_fail() {
    FAILURES+=("$1")
    echo "  FAIL: $1" >&2
}

# ---------------------------------------------------------------------------
# Gate 1: Vault status
# ---------------------------------------------------------------------------

echo "Checking vault status at ${SERVER_URL}/api/vault/status ..."

VAULT_RESPONSE="$(curl -sf \
    -H "Authorization: Bearer ${ENGRAM_API_TOKEN}" \
    "${SERVER_URL}/api/vault/status" 2>/dev/null)" || {
    echo "SAFETY-GATE ERROR: Failed to reach ${SERVER_URL}/api/vault/status" >&2
    echo "  Verify the server is running and ENGRAM_API_TOKEN is valid." >&2
    exit 2
}

GOT_FP="$(echo "${VAULT_RESPONSE}" | jq -r '.fingerprint // empty' 2>/dev/null || true)"
GOT_CRED_COUNT="$(echo "${VAULT_RESPONSE}" | jq -r '.credential_count // empty' 2>/dev/null || true)"
# Vault API (handlers_vault.go) emits `mismatch_count` ONLY when > 0.
# Treat absent field as 0 (healthy state).
# Use || true so a non-JSON response (proxy error page, empty body) leaves
# GOT_MISMATCH empty rather than aborting under set -e with jq's exit 4.
GOT_MISMATCH="$(echo "${VAULT_RESPONSE}" | jq -r '.mismatch_count // 0' 2>/dev/null || true)"

if [[ -z "${GOT_FP}" || -z "${GOT_CRED_COUNT}" ]]; then
    echo "SAFETY-GATE ERROR: Unexpected vault response — missing required fields (fingerprint or credential_count)" >&2
    echo "  Response: ${VAULT_RESPONSE}" >&2
    exit 2
fi

# fingerprint
if [[ "${GOT_FP}" != "${VAULT_FP}" ]]; then
    record_fail "fingerprint: expected ${VAULT_FP}, got ${GOT_FP}"
else
    SUMMARY_PARTS+=("fingerprint=${GOT_FP}")
fi

# credential_count
if [[ "${RESOLVED_PHASE}" == "post-us3" ]]; then
    EXPECTED_CRED="${POST_US3_CRED_COUNT}"
else
    EXPECTED_CRED="${VAULT_CRED_COUNT}"
fi

if [[ "${GOT_CRED_COUNT}" != "${EXPECTED_CRED}" ]]; then
    record_fail "credentials: expected ${EXPECTED_CRED}, got ${GOT_CRED_COUNT} (phase=${RESOLVED_PHASE})"
else
    SUMMARY_PARTS+=("credentials=${GOT_CRED_COUNT}")
fi

# mismatch_count
if [[ "${GOT_MISMATCH}" -gt "${VAULT_MISMATCH_MAX}" ]]; then
    record_fail "mismatch_count: expected <=${VAULT_MISMATCH_MAX}, got ${GOT_MISMATCH}"
else
    SUMMARY_PARTS+=("mismatches=${GOT_MISMATCH}")
fi

# ---------------------------------------------------------------------------
# Gate 2: Postgres entity counts
# ---------------------------------------------------------------------------

if [[ -n "${SKIP_PG}" ]]; then
    echo "Skipping Postgres entity checks (SAFETY_GATE_SKIP_PG set)."
else
    echo "Checking Postgres entity counts (phase=${RESOLVED_PHASE}) ..."

    # Entity table mapping: parallel indexed arrays for Bash 3.x compatibility.
    # (declare -A requires Bash 4+; macOS ships with Bash 3.2.)
    # ENTITY_KEYS[i] and ENTITY_TABLES[i] are co-indexed: key → SQL table name.
    # Keys match .entities.<key> in baseline JSON.
    ENTITY_KEYS=(issues api_tokens versioned_documents documents content)
    ENTITY_TABLES=(issues api_tokens versioned_documents documents content)

    # post-US3 adds credentials table
    if [[ "${RESOLVED_PHASE}" == "post-us3" ]]; then
        ENTITY_KEYS+=(credentials)
        ENTITY_TABLES+=(credentials)
    fi

    check_entity() {
        local key="$1"
        local table="$2"

        # Determine bound type and value from baseline JSON
        local exact min bound_type expected
        exact="$(jq -r --arg k "${key}" '.entities[$k].exact // empty' "${BASELINE_FILE}")"
        min="$(jq -r --arg k "${key}" '.entities[$k].min // empty' "${BASELINE_FILE}")"

        # post-US3 credentials come from a different section
        if [[ "${key}" == "credentials" && "${RESOLVED_PHASE}" == "post-us3" ]]; then
            exact="$(jq -r '.entities_post_us3.credentials.exact // empty' "${BASELINE_FILE}")"
            min=""
        fi

        if [[ -n "${exact}" ]]; then
            bound_type="exact"
            expected="${exact}"
        elif [[ -n "${min}" ]]; then
            bound_type="min"
            expected="${min}"
        else
            echo "  WARN: No bound defined for entity '${key}' — skipping" >&2
            return
        fi

        local got
        got="$(docker exec "${PG_CONTAINER}" \
            psql -U "${PG_USER}" -d "${PG_DB}" -tAc \
            "SELECT COUNT(*) FROM ${table}" 2>/dev/null | tr -d '[:space:]')" || {
            record_fail "${key}: psql query failed (table=${table})"
            return
        }

        if [[ "${bound_type}" == "exact" ]]; then
            if [[ "${got}" != "${expected}" ]]; then
                record_fail "${key}: expected ${expected}, got ${got}"
            else
                SUMMARY_PARTS+=("${key}=${got}")
            fi
        else
            # min bound
            if [[ "${got}" -lt "${expected}" ]]; then
                record_fail "${key}: expected >=${expected}, got ${got}"
            else
                SUMMARY_PARTS+=("${key}>=${got}")
            fi
        fi
    }

    for i in "${!ENTITY_KEYS[@]}"; do
        check_entity "${ENTITY_KEYS[i]}" "${ENTITY_TABLES[i]}"
    done
fi

# ---------------------------------------------------------------------------
# Report
# ---------------------------------------------------------------------------

if [[ ${#FAILURES[@]} -gt 0 ]]; then
    echo "" >&2
    echo "SAFETY-GATE: FAIL — ${#FAILURES[@]} invariant(s) violated:" >&2
    for f in "${FAILURES[@]}"; do
        echo "  - ${f}" >&2
    done
    exit 1
fi

# Use printf to join with ", " — IFS-based join only uses the FIRST character
# of IFS, so `IFS=', '` would produce "a,b,c" not "a, b, c".
SUMMARY=$(printf "%s, " "${SUMMARY_PARTS[@]}")
SUMMARY="${SUMMARY%, }"
echo "SAFETY-GATE: OK (${SUMMARY})"
exit 0
