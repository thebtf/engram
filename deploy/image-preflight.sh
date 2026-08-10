#!/usr/bin/env bash
# Validate and consume immutable deployment image identities without evaluating dotenv input.
set -euo pipefail

if [[ $# -gt 1 ]]; then
    printf 'usage: %s [env-file]\n' "$0" >&2
    exit 2
fi

die() {
    printf 'image preflight: %s\n' "$1" >&2
    exit 1
}

ENV_FILE="${1:-.env}"
[[ -r "${ENV_FILE}" ]] || die "cannot read env file ${ENV_FILE}"

umask 077
SNAPSHOT="$(mktemp "${TMPDIR:-/tmp}/engram-image-env.XXXXXX")"
trap 'rm -f "${SNAPSHOT}"' EXIT
chmod 600 "${SNAPSHOT}"
cat -- "${ENV_FILE}" > "${SNAPSHOT}"

keys=(ENGRAM_SERVER_IMAGE ENGRAM_OPERATOR_IMAGE ENGRAM_POSTGRES_IMAGE)
declare -A canonical_repositories=(
    [ENGRAM_SERVER_IMAGE]='ghcr.io/thebtf/engram'
    [ENGRAM_OPERATOR_IMAGE]='ghcr.io/thebtf/engram-operator-console'
    [ENGRAM_POSTGRES_IMAGE]='ghcr.io/thebtf/engram-postgres'
)
compose_overrides=(COMPOSE_FILE COMPOSE_PATH_SEPARATOR COMPOSE_PROJECT_NAME COMPOSE_PROFILES COMPOSE_ENV_FILES COMPOSE_DISABLE_ENV_FILE COMPOSE_PROJECT_DIRECTORY COMPOSE_CONVERT_WINDOWS_PATHS COMPOSE_REMOVE_ORPHANS COMPOSE_IGNORE_ORPHANS)
declare -A values=()

RUNTIME_COMPOSE_FILE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/docker-compose.runtime.yml"
declare -A interpolation_keys=()
while IFS= read -r line || [[ -n "${line}" ]]; do
    remainder="${line}"
    while [[ "${remainder}" =~ \$\{([[:alpha:]_][[:alnum:]_]*) ]]; do
        interpolation_keys["${BASH_REMATCH[1]}"]=1
        remainder="${remainder#*"${BASH_REMATCH[0]}"}"
    done
done < "${RUNTIME_COMPOSE_FILE}"

declare -A snapshot_values=()
while IFS= read -r line || [[ -n "${line}" ]]; do
    line="${line%$'\r'}"
    for key in "${compose_overrides[@]}"; do
        [[ "${line}" =~ ^[[:space:]]*(export[[:space:]]+)?${key}[[:space:]]*([=:]|$) ]] && die "env file sets ${key}"
    done
    for key in "${keys[@]}"; do
        if [[ "${line}" =~ (^|[^[:alnum:]_])${key}([^[:alnum:]_]|$) ]]; then
            [[ "${line}" =~ ^${key}=([[:alnum:]][[:alnum:]./_:-]*@sha256:[[:xdigit:]]{64})$ ]] || die "${key} must use one bare literal digest assignment"
            [[ -z "${values[${key}]+x}" ]] || die "duplicate ${key} definition"
            values["${key}"]="${BASH_REMATCH[1]}"
        fi
    done
    if [[ "${line}" =~ ^([[:alpha:]_][[:alnum:]_]*)=(.*)$ ]] && [[ -n "${interpolation_keys[${BASH_REMATCH[1]}]+x}" ]]; then
        key="${BASH_REMATCH[1]}"
        value="${BASH_REMATCH[2]}"
        [[ -z "${snapshot_values[${key}]+x}" ]] || die "duplicate ${key} definition"
        [[ "${value}" != *'$'* ]] || die "${key} must use a literal dotenv value"
        snapshot_values["${key}"]="${value}"
    fi
done < "${SNAPSHOT}"

for key in "${keys[@]}"; do
    value="${values[${key}]-}"
    [[ -n "${value}" ]] || die "missing ${key}"
    digest="${value#"${canonical_repositories[${key}]}@sha256:"}"
    [[ "${digest}" =~ ^[0-9a-f]{64}$ && "${value}" == "${canonical_repositories[${key}]}@sha256:${digest}" ]] || die "${key} must use the canonical repository with a lowercase SHA-256 digest"
done

for key in "${compose_overrides[@]}"; do
    [[ -z "${!key+x}" ]] || die "process environment sets ${key}"
done

for key in "${!interpolation_keys[@]}"; do
    if [[ ${!key+x} && -n ${snapshot_values[${key}]+x} && ${!key-} != "${snapshot_values[${key}]}" ]]; then
        die "process environment overrides ${key} with a different frozen value"
    fi
    unset "${key}"
done

printf 'image preflight: immutable image identities accepted from %s\n' "${ENV_FILE}"
docker compose --env-file "${SNAPSHOT}" -f "${RUNTIME_COMPOSE_FILE}" config --quiet
docker compose --env-file "${SNAPSHOT}" -f "${RUNTIME_COMPOSE_FILE}" pull
docker compose --env-file "${SNAPSHOT}" -f "${RUNTIME_COMPOSE_FILE}" up -d
