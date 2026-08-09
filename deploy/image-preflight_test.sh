#!/usr/bin/env bash
# Black-box checks for deploy/image-preflight.sh.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PREFLIGHT="${ROOT}/deploy/image-preflight.sh"
TESTS_RUN=0
TESTS_FAILED=0

pass() { TESTS_RUN=$((TESTS_RUN + 1)); printf 'PASS: %s\n' "$1"; }
fail() { TESTS_RUN=$((TESTS_RUN + 1)); TESTS_FAILED=$((TESTS_FAILED + 1)); printf 'FAIL: %s\n' "$1" >&2; }

make_env() {
    local path="$1" server="$2" operator="$3" postgres="$4"
    {
        printf 'UNRELATED_VALUE=$(touch %s.injected)\n' "${path}"
        printf 'ENGRAM_SERVER_IMAGE=%s\n' "${server}"
        printf 'ENGRAM_OPERATOR_IMAGE=%s\n' "${operator}"
        printf 'ENGRAM_POSTGRES_IMAGE=%s\n' "${postgres}"
    } >"${path}"
}
make_env_without_server() {
    local path="$1" operator="$2" postgres="$3"
    {
        printf 'ENGRAM_OPERATOR_IMAGE=%s\n' "${operator}"
        printf 'ENGRAM_POSTGRES_IMAGE=%s\n' "${postgres}"
    } >"${path}"
}

run_documented_path() {
    local env_file="$1" marker="$2"
    shift 2
    local mock_bin="${marker}.bin"
    mkdir "${mock_bin}"
    cat >"${mock_bin}/docker" <<EOF
#!/usr/bin/env bash
printf '%s\\n' "\$*" >> "${marker}"
EOF
    chmod +x "${mock_bin}/docker"
    set +e
    PATH="${mock_bin}:${PATH}" "$@" bash -c '
        bash "$1" "$2" && docker compose --env-file "$2" -f "$3" config
    ' -- "${PREFLIGHT}" "${env_file}" "${ROOT}/deploy/docker-compose.runtime.yml"
    local rc=$?
    set -e
    rm -rf "${mock_bin}"
    return "${rc}"
}

assert_rejected_before_compose() {
    local name="$1" env_file="$2"
    local marker="${env_file}.compose"
    local rc=0
    run_documented_path "${env_file}" "${marker}" env -u ENGRAM_SERVER_IMAGE -u ENGRAM_OPERATOR_IMAGE -u ENGRAM_POSTGRES_IMAGE || rc=$?
    if [[ ${rc} -ne 0 && ! -e ${marker} ]]; then pass "${name}"; else fail "${name}"; fi
    rm -f "${marker}"
}

main() {
    local tmp
    tmp=".image-preflight-test.$$"
    mkdir "${tmp}"
    trap "rm -rf '${tmp}'" EXIT
    local server="ghcr.io/example/server@sha256:$(printf 'a%.0s' {1..64})"
    local operator="ghcr.io/example/operator@sha256:$(printf 'b%.0s' {1..64})"
    local postgres="ghcr.io/example/postgres@sha256:$(printf 'c%.0s' {1..64})"
    make_env "${tmp}/valid.env" "${server}" "${operator}" "${postgres}"
    local marker="${tmp}/valid.compose" rc=0
    run_documented_path "${tmp}/valid.env" "${marker}" env -u ENGRAM_SERVER_IMAGE -u ENGRAM_OPERATOR_IMAGE -u ENGRAM_POSTGRES_IMAGE || rc=$?
    if [[ ${rc} -eq 0 && -s ${marker} && ! -e ${tmp}/valid.env.injected ]]; then
        pass 'valid three digests reach compose without evaluating dotenv'
    else
        fail 'valid three digests reach compose without evaluating dotenv'
    fi
    rm -f "${marker}"

    make_env_without_server "${tmp}/missing.env" "${operator}" "${postgres}"
    assert_rejected_before_compose 'missing image rejected before compose' "${tmp}/missing.env"

    make_env "${tmp}/empty.env" "" "${operator}" "${postgres}"
    assert_rejected_before_compose 'empty image rejected before compose' "${tmp}/empty.env"

    make_env "${tmp}/latest.env" 'ghcr.io/example/server:latest' "${operator}" "${postgres}"
    assert_rejected_before_compose 'latest tag rejected before compose' "${tmp}/latest.env"

    make_env "${tmp}/branch.env" 'ghcr.io/example/server:main' "${operator}" "${postgres}"
    assert_rejected_before_compose 'branch tag rejected before compose' "${tmp}/branch.env"

    make_env "${tmp}/semver.env" 'ghcr.io/example/server:v1.2.3' "${operator}" "${postgres}"
    assert_rejected_before_compose 'semver tag rejected before compose' "${tmp}/semver.env"

    make_env "${tmp}/malformed.env" 'ghcr.io/example/server@sha256:abc' "${operator}" "${postgres}"
    assert_rejected_before_compose 'malformed digest rejected before compose' "${tmp}/malformed.env"

    make_env "${tmp}/duplicate.env" "${server}" "${operator}" "${postgres}"
    printf 'ENGRAM_SERVER_IMAGE=%s\n' "${server}" >>"${tmp}/duplicate.env"
    assert_rejected_before_compose 'duplicate image key rejected before compose' "${tmp}/duplicate.env"

    make_env "${tmp}/override.env" "${server}" "${operator}" "${postgres}"
    marker="${tmp}/override.compose"; rc=0
    run_documented_path "${tmp}/override.env" "${marker}" env ENGRAM_SERVER_IMAGE="${operator}" || rc=$?
    if [[ ${rc} -ne 0 && ! -e ${marker} ]]; then pass 'environment override rejected before compose'; else fail 'environment override rejected before compose'; fi
    rm -f "${marker}"

    printf '\n%d tests, %d failures\n' "${TESTS_RUN}" "${TESTS_FAILED}"
    [[ ${TESTS_FAILED} -eq 0 ]]
}

main "$@"
