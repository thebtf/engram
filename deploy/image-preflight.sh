#!/usr/bin/env bash
# Validate and consume immutable deployment image identities without evaluating dotenv input.
set -euo pipefail

die() {
    printf 'image preflight: %s\n' "$1" >&2
    exit 1
}

if [[ $# -lt 2 || $# -gt 4 ]]; then
    printf 'usage: %s --publication-result PATH [--env-file PATH]\n' "$0" >&2
    exit 2
fi

PUBLICATION_RESULT=""
ENV_FILE=".env"
while [[ $# -gt 0 ]]; do
    case "$1" in
        --publication-result) PUBLICATION_RESULT="${2:?--publication-result requires a path}"; shift 2 ;;
        --env-file) ENV_FILE="${2:?--env-file requires a path}"; shift 2 ;;
        *) printf 'usage: %s --publication-result PATH [--env-file PATH]\n' "$0" >&2; exit 2 ;;
    esac
done
[[ -n "${PUBLICATION_RESULT}" && -r "${PUBLICATION_RESULT}" ]] || die "cannot read publication result"


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
python3 - "${PUBLICATION_RESULT}" "${values[ENGRAM_SERVER_IMAGE]}" "${values[ENGRAM_OPERATOR_IMAGE]}" "${values[ENGRAM_POSTGRES_IMAGE]}" <<'PY'
import json
import re
import sys

record_path, server, operator, postgres = sys.argv[1:]
try:
    with open(record_path, encoding='utf-8') as source:
        record = json.load(source)
except (OSError, json.JSONDecodeError) as error:
    raise SystemExit(f'invalid publication result: {error}')

required = {'schema_version', 'release_version', 'source_commit', 'single_writer_model',
            'external_package_admin_trust_boundary', 'remote_inspection',
            'acceptance_manifest_sha256', 'destinations'}
allowed = required | {'completed_at'}
if not isinstance(record, dict) or set(record) - allowed or required - set(record):
    raise SystemExit('publication result has an unsupported schema')
if type(record['schema_version']) is not int or record['schema_version'] != 1 or not re.fullmatch(r'v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?', record['release_version']):
    raise SystemExit('publication result has an invalid schema or release version')
if not re.fullmatch(r'[0-9a-f]{40}', record['source_commit']) or record['single_writer_model'] != 'repository-workflow-release-publish' or record['external_package_admin_trust_boundary'] is not True or record['remote_inspection'] != 'complete' or not re.fullmatch(r'sha256:[0-9a-f]{64}', record['acceptance_manifest_sha256']):
    raise SystemExit('publication result lacks accepted provenance fields')
repositories = {
    'server': 'ghcr.io/thebtf/engram',
    'operator_console': 'ghcr.io/thebtf/engram-operator-console',
    'postgres': 'ghcr.io/thebtf/engram-postgres',
}
selected = {'server': server, 'operator_console': operator, 'postgres': postgres}
destinations = record['destinations']
if not isinstance(destinations, list) or len(destinations) != 6:
    raise SystemExit('publication result must have exactly six destinations')
seen = set()
for destination in destinations:
    if not isinstance(destination, dict) or set(destination) - {'image', 'reference', 'config_digest', 'manifest_digest', 'action', 'local_id'} or not {'image', 'reference', 'config_digest', 'manifest_digest', 'action'} <= set(destination):
        raise SystemExit('publication result has an invalid destination record')
    image, reference = destination['image'], destination['reference']
    if image not in repositories or reference not in {f'{repositories[image]}:{record["release_version"]}', f'{repositories[image]}:sha-{record["source_commit"]}'}:
        raise SystemExit('publication result has an unexpected destination')
    if (image, reference) in seen or destination['action'] not in {'pushed', 'verified-noop'} or not re.fullmatch(r'sha256:[0-9a-f]{64}', destination['config_digest']) or not re.fullmatch(r'sha256:[0-9a-f]{64}', destination['manifest_digest']):
        raise SystemExit('publication result has duplicate or invalid destination data')
    seen.add((image, reference))
expected = {(image, f'{repository}:{tag}') for image, repository in repositories.items() for tag in (record['release_version'], f'sha-{record["source_commit"]}')}
if seen != expected:
    raise SystemExit('publication result does not cover canonical destinations')
for image, repository in repositories.items():
    rows = [entry for entry in destinations if entry['image'] == image]
    if rows[0]['config_digest'] != rows[1]['config_digest'] or rows[0]['manifest_digest'] != rows[1]['manifest_digest']:
        raise SystemExit('publication result has mismatched digest pairs')
    if selected[image] != f'{repository}@{rows[0]["manifest_digest"]}':
        raise SystemExit(f'selected {image} image is not bound to publication result')
PY

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
