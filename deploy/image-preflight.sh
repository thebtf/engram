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
declare -A values=()

while IFS= read -r line || [[ -n "${line}" ]]; do
    line="${line%$'\r'}"
    for key in "${keys[@]}"; do
        if [[ "${line}" =~ (^|[^[:alnum:]_])${key}([^[:alnum:]_]|$) ]]; then
            [[ "${line}" =~ ^${key}=([[:alnum:]][[:alnum:]./_:-]*@sha256:[[:xdigit:]]{64})$ ]] || die "${key} must use one bare literal digest assignment"
            [[ -z "${values[${key}]+x}" ]] || die "duplicate ${key} definition"
            values["${key}"]="${BASH_REMATCH[1]}"
        fi
    done
done < "${SNAPSHOT}"

for key in "${keys[@]}"; do
    value="${values[${key}]-}"
    [[ -n "${value}" ]] || die "missing ${key}"
    if [[ ${!key+x} && ${!key} != "${value}" ]]; then
        die "process environment overrides ${key} with a different value"
    fi
done

unset "${keys[@]}"
COMPOSE_FILE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/docker-compose.runtime.yml"
printf 'image preflight: immutable image identities accepted from %s\n' "${ENV_FILE}"
docker compose --env-file "${SNAPSHOT}" -f "${COMPOSE_FILE}" config
docker compose --env-file "${SNAPSHOT}" -f "${COMPOSE_FILE}" pull
docker compose --env-file "${SNAPSHOT}" -f "${COMPOSE_FILE}" up -d
