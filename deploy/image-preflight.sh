#!/usr/bin/env bash
# Validate the three production image identities without evaluating dotenv input.
set -euo pipefail

if [[ $# -gt 1 ]]; then
    printf 'usage: %s [env-file]\n' "$0" >&2
    exit 2
fi

ENV_FILE="${1:-.env}"
if [[ ! -r "${ENV_FILE}" ]]; then
    printf 'image preflight: cannot read env file %s\n' "${ENV_FILE}" >&2
    exit 1
fi

die() {
    printf 'image preflight: %s\n' "$1" >&2
    exit 1
}

keys=(ENGRAM_SERVER_IMAGE ENGRAM_OPERATOR_IMAGE ENGRAM_POSTGRES_IMAGE)
declare -A values=()

while IFS= read -r line || [[ -n "${line}" ]]; do
    line="${line%$'\r'}"
    [[ "${line}" =~ ^[[:space:]]*# ]] && continue
    if [[ "${line}" =~ ^[[:space:]]*(ENGRAM_SERVER_IMAGE|ENGRAM_OPERATOR_IMAGE|ENGRAM_POSTGRES_IMAGE)[[:space:]]*=(.*)$ ]]; then
        key="${BASH_REMATCH[1]}"
        value="${BASH_REMATCH[2]}"
        [[ -z "${values[${key}]+x}" ]] || die "duplicate ${key} definition"
        values["${key}"]="${value}"
    fi
done < "${ENV_FILE}"

for key in "${keys[@]}"; do
    value="${values[${key}]-}"
    [[ -n "${value}" ]] || die "missing or empty ${key}"
    [[ "${value}" =~ ^[^[:space:]@]+@sha256:[[:xdigit:]]{64}$ ]] || die "${key} must be an image reference pinned by @sha256:<64-hex>"
    if [[ ${!key+x} && ${!key} != "${value}" ]]; then
        die "process environment overrides ${key} with a different value"
    fi
done

printf 'image preflight: immutable image identities accepted from %s\n' "${ENV_FILE}"
