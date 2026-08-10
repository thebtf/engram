#!/usr/bin/env bash
# Black-box checks for the immutable deployment wrapper.
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
        # shellcheck disable=SC2016 # Deliberately write command substitution as inert fixture text.
        printf 'UNRELATED_VALUE=$(touch %s.injected)\n' "${path}"
        printf 'ENGRAM_SERVER_IMAGE=%s\n' "${server}"
        printf 'ENGRAM_OPERATOR_IMAGE=%s\n' "${operator}"
        printf 'ENGRAM_POSTGRES_IMAGE=%s\n' "${postgres}"
        printf 'POSTGRES_PASSWORD=database-sentinel\n'
        printf 'ENGRAM_AUTH_ADMIN_TOKEN=admin-sentinel\n'
        printf 'ENGRAM_AUTH_DISABLED=false\n'
        printf 'ENGRAM_VAULT_KEY=vault-sentinel\n'
        printf 'ENGRAM_EMBEDDING_API_KEY=provider-sentinel\n'
    } >"${path}"
}
make_publication_result() {
    local path="$1" commit="0123456789abcdef0123456789abcdef01234567" version="v6.47.0"
    cat >"${path}" <<EOF
{"schema_version":1,"release_version":"${version}","source_commit":"${commit}","single_writer_model":"repository-workflow-release-publish","external_package_admin_trust_boundary":true,"remote_inspection":"complete","acceptance_manifest_sha256":"sha256:$(printf 'd%.0s' {1..64})","destinations":[
{"image":"server","reference":"ghcr.io/thebtf/engram:${version}","config_digest":"sha256:$(printf 'e%.0s' {1..64})","manifest_digest":"sha256:$(printf 'a%.0s' {1..64})","action":"pushed"},
{"image":"server","reference":"ghcr.io/thebtf/engram:sha-${commit}","config_digest":"sha256:$(printf 'e%.0s' {1..64})","manifest_digest":"sha256:$(printf 'a%.0s' {1..64})","action":"verified-noop"},
{"image":"operator_console","reference":"ghcr.io/thebtf/engram-operator-console:${version}","config_digest":"sha256:$(printf 'f%.0s' {1..64})","manifest_digest":"sha256:$(printf 'b%.0s' {1..64})","action":"pushed"},
{"image":"operator_console","reference":"ghcr.io/thebtf/engram-operator-console:sha-${commit}","config_digest":"sha256:$(printf 'f%.0s' {1..64})","manifest_digest":"sha256:$(printf 'b%.0s' {1..64})","action":"verified-noop"},
{"image":"postgres","reference":"ghcr.io/thebtf/engram-postgres:${version}","config_digest":"sha256:$(printf '0%.0s' {1..64})","manifest_digest":"sha256:$(printf 'c%.0s' {1..64})","action":"pushed"},
{"image":"postgres","reference":"ghcr.io/thebtf/engram-postgres:sha-${commit}","config_digest":"sha256:$(printf '0%.0s' {1..64})","manifest_digest":"sha256:$(printf 'c%.0s' {1..64})","action":"verified-noop"}]}
EOF
}

run_wrapper() {
    local source="$1" marker="$2" after_config="$3" after_pull="$4"
    shift 4
    local mock_bin="${marker}.bin"
    mkdir "${mock_bin}"
    cat >"${mock_bin}/docker" <<EOF
#!/usr/bin/env bash
snapshot="" action="" quiet=0 present=""
for ((i = 1; i <= \$#; i++)); do
    case "\${!i}" in
        --env-file) next=\$((i + 1)); snapshot="\${!next}" ;;
        config|pull|up) action="\${!i}" ;;
        --quiet) quiet=1 ;;
    esac
done
server=""
while IFS= read -r line || [[ -n "\${line}" ]]; do
    case "\${line}" in ENGRAM_SERVER_IMAGE=*) server="\${line#*=}";; esac
done < "\${snapshot}"
for key in ENGRAM_SERVER_IMAGE ENGRAM_OPERATOR_IMAGE ENGRAM_POSTGRES_IMAGE POSTGRES_PASSWORD ENGRAM_AUTH_ADMIN_TOKEN ENGRAM_AUTH_DISABLED ENGRAM_VAULT_KEY ENGRAM_EMBEDDING_API_KEY; do
    [[ -z "\${!key+x}" ]] || present+="\${key},"
done
mode="\$(stat -c '%a' "\${snapshot}")"
printf '%s|%s|%s|%s|%s|%s\n' "\${action}" "\${snapshot}" "\${mode}" "\${server}" "\${quiet}" "\${present}" >> "${marker}"
case "\${action}" in
    config) [[ "${after_config}" == "${source}" ]] || cp "${after_config}" "${source}" ;;
    pull) [[ "${after_pull}" == "${source}" ]] || cp "${after_pull}" "${source}" ;;
esac
EOF
    chmod +x "${mock_bin}/docker"
    set +e
    PATH="${mock_bin}:${PATH}" env -u COMPOSE_FILE -u COMPOSE_PATH_SEPARATOR -u COMPOSE_PROJECT_NAME -u COMPOSE_PROFILES -u COMPOSE_ENV_FILES -u COMPOSE_DISABLE_ENV_FILE -u COMPOSE_PROJECT_DIRECTORY "$@" bash "${PREFLIGHT}" --publication-result "${PUBLICATION_RESULT}" --env-file "${source}" >"${WRAPPER_OUTPUT}" 2>&1
    local rc=$?
    set -e
    rm -rf "${mock_bin}"
    return "${rc}"
}
make_blocking_docker() {
    local mock_bin="$1" marker="$2" pull_started="$3" release="$4"
    mkdir "${mock_bin}"
    cat >"${mock_bin}/docker" <<EOF
#!/usr/bin/env bash
action=""
for ((i = 1; i <= \$#; i++)); do
    case "\${!i}" in config|pull|up) action="\${!i}" ;; esac
done
printf '%s\n' "\${action}" >> "${marker}"
if [[ "\${action}" == pull ]]; then
    : > "${pull_started}"
    while [[ ! -e "${release}" ]]; do sleep 0.05; done
fi
EOF
    chmod +x "${mock_bin}/docker"
}

start_wrapper() {
    local source="$1" output="$2" mock_bin="$3"
    shift 3
    (
        PATH="${mock_bin}:${PATH}" env -u COMPOSE_FILE -u COMPOSE_PATH_SEPARATOR -u COMPOSE_PROJECT_NAME -u COMPOSE_PROFILES -u COMPOSE_ENV_FILES -u COMPOSE_DISABLE_ENV_FILE -u COMPOSE_PROJECT_DIRECTORY "${@}" bash "${PREFLIGHT}" --publication-result "${PUBLICATION_RESULT}" --env-file "${source}"
    ) >"${output}" 2>&1 &
    STARTED_WRAPPER_PID=$!
}

wait_for_file() {
    local path="$1"
    for _ in {1..100}; do
        [[ -e "${path}" ]] && return 0
        sleep 0.05
    done
    return 1
}

assert_no_values_output() {
    local output="$1" value
    shift
    for value in "$@"; do
        [[ "$(<"${output}")" != *"${value}"* ]] || return 1
    done
}

assert_lock_sequence() {
    local marker="$1" action configs=0 pulls=0 ups=0 invalid=0
    while IFS= read -r action || [[ -n "${action}" ]]; do
        case "${action}" in config) configs=$((configs + 1)) ;; pull) pulls=$((pulls + 1)) ;; up) ups=$((ups + 1)) ;; *) invalid=1 ;; esac
    done < "${marker}"
    [[ ${configs} -eq 2 && ${pulls} -eq 2 && ${ups} -eq 2 && ${invalid} -eq 0 ]]
}

assert_rejected_before_compose() {
    local name="$1" source="$2" marker="${2}.compose" rc=0
    shift 2
    WRAPPER_OUTPUT="${source}.output"
    run_wrapper "${source}" "${marker}" "${source}" "${source}" "$@" || rc=$?
    if [[ ${rc} -ne 0 && ! -e ${marker} ]] && assert_no_secret_output "${WRAPPER_OUTPUT}"; then pass "${name}"; else fail "${name}"; fi
    rm -f "${marker}"
}

assert_frozen_compose_input() {
    local marker="$1" expected="$2" source="$3" config=0 pull=0 up=0 invalid=0 snapshot_path="" line action snapshot mode actual quiet present
    while IFS= read -r line || [[ -n "${line}" ]]; do
        IFS='|' read -r action snapshot mode actual quiet present <<<"${line}"
        [[ "${snapshot}" != "${source}" && "${mode}" == 600 && ( -z "${snapshot_path}" || "${snapshot}" == "${snapshot_path}" ) && "${actual}" == "${expected}" && -z "${present}" ]] || { invalid=1; continue; }
        snapshot_path="${snapshot}"
        case "${action}" in config) [[ ${quiet} -eq 1 ]] || invalid=1; config=1 ;; pull) pull=1 ;; up) up=1 ;; *) invalid=1 ;; esac
    done < "${marker}"
    [[ ${config} -eq 1 && ${pull} -eq 1 && ${up} -eq 1 && ${invalid} -eq 0 ]]
}

assert_no_secret_output() {
    local output="$1" secret
    for secret in database-sentinel admin-sentinel vault-sentinel provider-sentinel; do
        [[ "$(<"${output}")" != *"${secret}"* ]] || return 1
    done
}

main() {
    local tmp=".image-preflight-test.$$"
    mkdir "${tmp}"
    # shellcheck disable=SC2064 # Capture this invocation's unique temporary path now.
    trap "rm -rf '${tmp}'" EXIT
    local server operator postgres
    server="ghcr.io/thebtf/engram@sha256:$(printf 'a%.0s' {1..64})"
    operator="ghcr.io/thebtf/engram-operator-console@sha256:$(printf 'b%.0s' {1..64})"
    postgres="ghcr.io/thebtf/engram-postgres@sha256:$(printf 'c%.0s' {1..64})"
    local marker="${tmp}/valid.compose" rc=0
    PUBLICATION_RESULT="${tmp}/publication-result.json"
    make_publication_result "${PUBLICATION_RESULT}"
    make_env "${tmp}/valid.env" "${server}" "${operator}" "${postgres}"
    WRAPPER_OUTPUT="${tmp}/valid.output"
    run_wrapper "${tmp}/valid.env" "${marker}" "${tmp}/valid.env" "${tmp}/valid.env" env ENGRAM_SERVER_IMAGE="${server}" ENGRAM_OPERATOR_IMAGE="${operator}" ENGRAM_POSTGRES_IMAGE="${postgres}" POSTGRES_PASSWORD=database-sentinel ENGRAM_AUTH_ADMIN_TOKEN=admin-sentinel ENGRAM_AUTH_DISABLED=false ENGRAM_VAULT_KEY=vault-sentinel ENGRAM_EMBEDDING_API_KEY=provider-sentinel || rc=$?
    if [[ ${rc} -eq 0 && ! -e ${tmp}/valid.env.injected ]] && assert_frozen_compose_input "${marker}" "${server}" "${tmp}/valid.env" && assert_no_secret_output "${WRAPPER_OUTPUT}"; then
        pass 'config quietly validates and all mutations use only the frozen snapshot'
    else
        fail 'config quietly validates and all mutations use only the frozen snapshot'
    fi
    rm -f "${marker}"

    local lock_marker="${tmp}/lock.compose" lock_mock="${tmp}/lock.bin" pull_started="${tmp}/pull.started" release="${tmp}/pull.release" first_pid second_pid third_pid first_rc second_rc third_rc
    make_env "${tmp}/lock.env" "${server}" "${operator}" "${postgres}"
    make_blocking_docker "${lock_mock}" "${lock_marker}" "${pull_started}" "${release}"
    start_wrapper "${tmp}/lock.env" "${tmp}/lock-first.output" "${lock_mock}" env -u ENGRAM_SERVER_IMAGE -u ENGRAM_OPERATOR_IMAGE -u ENGRAM_POSTGRES_IMAGE
    first_pid=${STARTED_WRAPPER_PID}
    if wait_for_file "${pull_started}"; then
        start_wrapper "${tmp}/lock.env" "${tmp}/lock-second.output" "${lock_mock}" env -u ENGRAM_SERVER_IMAGE -u ENGRAM_OPERATOR_IMAGE -u ENGRAM_POSTGRES_IMAGE
        second_pid=${STARTED_WRAPPER_PID}
        second_rc=0; wait "${second_pid}" || second_rc=$?
        : >"${release}"
        first_rc=0; wait "${first_pid}" || first_rc=$?
        start_wrapper "${tmp}/lock.env" "${tmp}/lock-third.output" "${lock_mock}" env -u ENGRAM_SERVER_IMAGE -u ENGRAM_OPERATOR_IMAGE -u ENGRAM_POSTGRES_IMAGE
        third_pid=${STARTED_WRAPPER_PID}
        third_rc=0; wait "${third_pid}" || third_rc=$?
        if [[ ${first_rc} -eq 0 && ${second_rc} -ne 0 && ${third_rc} -eq 0 && ! -e "${ROOT}/deploy/docker-compose.runtime.yml.deploy.lock" ]] && assert_lock_sequence "${lock_marker}" && assert_no_values_output "${tmp}/lock-first.output" database-sentinel admin-sentinel vault-sentinel provider-sentinel "${server}" "${operator}" "${postgres}" && assert_no_values_output "${tmp}/lock-second.output" database-sentinel admin-sentinel vault-sentinel provider-sentinel "${server}" "${operator}" "${postgres}" && assert_no_values_output "${tmp}/lock-third.output" database-sentinel admin-sentinel vault-sentinel provider-sentinel "${server}" "${operator}" "${postgres}"; then
            pass 'concurrent deployments reject contenders and release the Compose lock'
        else
            fail 'concurrent deployments reject contenders and release the Compose lock'
        fi
    else
        fail 'concurrent deployments reject contenders and release the Compose lock'
    fi
    rm -rf "${lock_mock}"

    make_env "${tmp}/invalid.env" "${server}" "${operator}" "${postgres}"
    printf 'ENGRAM_SERVER_IMAGE=%s\n' "${server}" >>"${tmp}/invalid.env"
    assert_rejected_before_compose 'duplicate bare assignment rejected' "${tmp}/invalid.env" env -u ENGRAM_SERVER_IMAGE -u ENGRAM_OPERATOR_IMAGE -u ENGRAM_POSTGRES_IMAGE

    for syntax in "export ENGRAM_SERVER_IMAGE=${server}" "ENGRAM_SERVER_IMAGE=\"${server}\"" "ENGRAM_SERVER_IMAGE=${server} # pinned" "ENGRAM_SERVER_IMAGE=\${OTHER_IMAGE}"; do
        make_env "${tmp}/syntax.env" "${server}" "${operator}" "${postgres}"
        printf '%s\n' "${syntax}" >>"${tmp}/syntax.env"
        assert_rejected_before_compose "managed nonliteral syntax rejected: ${syntax%%=*}" "${tmp}/syntax.env" env -u ENGRAM_SERVER_IMAGE -u ENGRAM_OPERATOR_IMAGE -u ENGRAM_POSTGRES_IMAGE
    done
    make_env "${tmp}/comment.env" "${server}" "${operator}" "${postgres}"
    printf '# ENGRAM_SERVER_IMAGE=untrusted\n  # ENGRAM_OPERATOR_IMAGE=untrusted\n# ENGRAM_POSTGRES_IMAGE=untrusted\n' >>"${tmp}/comment.env"
    marker="${tmp}/comment.compose"; rc=0; WRAPPER_OUTPUT="${tmp}/comment.output"
    run_wrapper "${tmp}/comment.env" "${marker}" "${tmp}/comment.env" "${tmp}/comment.env" env -u ENGRAM_SERVER_IMAGE -u ENGRAM_OPERATOR_IMAGE -u ENGRAM_POSTGRES_IMAGE || rc=$?
    if [[ ${rc} -eq 0 ]] && assert_frozen_compose_input "${marker}" "${server}" "${tmp}/comment.env" && assert_no_secret_output "${WRAPPER_OUTPUT}"; then
        pass 'comment-only managed image key mentions are ignored'
    else
        fail 'comment-only managed image key mentions are ignored'
    fi
    rm -f "${marker}"

    make_env "${tmp}/malformed.env" 'ghcr.io/example/server:latest' "${operator}" "${postgres}"
    assert_rejected_before_compose 'mutable tag rejected before compose' "${tmp}/malformed.env" env -u ENGRAM_SERVER_IMAGE -u ENGRAM_OPERATOR_IMAGE -u ENGRAM_POSTGRES_IMAGE
    make_env "${tmp}/wrong-registry-server.env" "registry.example/thebtf/engram@sha256:$(printf 'a%.0s' {1..64})" "${operator}" "${postgres}"
    assert_rejected_before_compose 'server wrong registry rejected before compose' "${tmp}/wrong-registry-server.env" env -u ENGRAM_SERVER_IMAGE -u ENGRAM_OPERATOR_IMAGE -u ENGRAM_POSTGRES_IMAGE
    make_env "${tmp}/near-miss-registry-server.env" "ghcrXio/thebtf/engram@sha256:$(printf 'a%.0s' {1..64})" "${operator}" "${postgres}"
    assert_rejected_before_compose 'server near-miss registry rejected before compose' "${tmp}/near-miss-registry-server.env" env -u ENGRAM_SERVER_IMAGE -u ENGRAM_OPERATOR_IMAGE -u ENGRAM_POSTGRES_IMAGE
    make_env "${tmp}/wrong-registry-operator.env" "${server}" "registry.example/thebtf/engram-operator-console@sha256:$(printf 'b%.0s' {1..64})" "${postgres}"
    assert_rejected_before_compose 'operator wrong registry rejected before compose' "${tmp}/wrong-registry-operator.env" env -u ENGRAM_SERVER_IMAGE -u ENGRAM_OPERATOR_IMAGE -u ENGRAM_POSTGRES_IMAGE
    make_env "${tmp}/wrong-registry-postgres.env" "${server}" "${operator}" "registry.example/thebtf/engram-postgres@sha256:$(printf 'c%.0s' {1..64})"
    assert_rejected_before_compose 'postgres wrong registry rejected before compose' "${tmp}/wrong-registry-postgres.env" env -u ENGRAM_SERVER_IMAGE -u ENGRAM_OPERATOR_IMAGE -u ENGRAM_POSTGRES_IMAGE

    make_env "${tmp}/wrong-repository-server.env" "ghcr.io/thebtf/engram-other@sha256:$(printf 'a%.0s' {1..64})" "${operator}" "${postgres}"
    assert_rejected_before_compose 'server wrong repository rejected before compose' "${tmp}/wrong-repository-server.env" env -u ENGRAM_SERVER_IMAGE -u ENGRAM_OPERATOR_IMAGE -u ENGRAM_POSTGRES_IMAGE
    make_env "${tmp}/wrong-repository-operator.env" "${server}" "ghcr.io/thebtf/engram-operator-console-other@sha256:$(printf 'b%.0s' {1..64})" "${postgres}"
    assert_rejected_before_compose 'operator wrong repository rejected before compose' "${tmp}/wrong-repository-operator.env" env -u ENGRAM_SERVER_IMAGE -u ENGRAM_OPERATOR_IMAGE -u ENGRAM_POSTGRES_IMAGE
    make_env "${tmp}/wrong-repository-postgres.env" "${server}" "${operator}" "ghcr.io/thebtf/engram-postgres-other@sha256:$(printf 'c%.0s' {1..64})"
    assert_rejected_before_compose 'postgres wrong repository rejected before compose' "${tmp}/wrong-repository-postgres.env" env -u ENGRAM_SERVER_IMAGE -u ENGRAM_OPERATOR_IMAGE -u ENGRAM_POSTGRES_IMAGE

    make_env "${tmp}/override.env" "${server}" "${operator}" "${postgres}"
    assert_rejected_before_compose 'different inherited security value is rejected before compose' "${tmp}/override.env" env ENGRAM_AUTH_ADMIN_TOKEN=conflicting-admin-token

    while IFS= read -r line || [[ -n "${line}" ]]; do
        [[ "${line}" == ENGRAM_AUTH_DISABLED=* ]] || printf '%s\n' "${line}"
    done <"${tmp}/valid.env" >"${tmp}/missing.env"
    marker="${tmp}/missing.compose"; rc=0; WRAPPER_OUTPUT="${tmp}/missing.output"
    run_wrapper "${tmp}/missing.env" "${marker}" "${tmp}/missing.env" "${tmp}/missing.env" env ENGRAM_AUTH_DISABLED=true || rc=$?
    if [[ ${rc} -eq 0 ]] && assert_frozen_compose_input "${marker}" "${server}" "${tmp}/missing.env" && assert_no_secret_output "${WRAPPER_OUTPUT}"; then
        pass 'absent snapshot keys cannot be injected from the parent environment'
    else
        fail 'absent snapshot keys cannot be injected from the parent environment'
    fi

    for key in COMPOSE_FILE COMPOSE_PATH_SEPARATOR COMPOSE_PROJECT_NAME COMPOSE_PROFILES COMPOSE_ENV_FILES COMPOSE_DISABLE_ENV_FILE COMPOSE_PROJECT_DIRECTORY COMPOSE_CONVERT_WINDOWS_PATHS COMPOSE_REMOVE_ORPHANS COMPOSE_IGNORE_ORPHANS; do
        make_env "${tmp}/compose.env" "${server}" "${operator}" "${postgres}"
        assert_rejected_before_compose "${key} in process environment is rejected before compose" "${tmp}/compose.env" env "${key}=untrusted"
        make_env "${tmp}/compose-snapshot.env" "${server}" "${operator}" "${postgres}"
        printf '%s=untrusted\n' "${key}" >>"${tmp}/compose-snapshot.env"
        assert_rejected_before_compose "${key} in frozen env file is rejected before compose" "${tmp}/compose-snapshot.env" env -u ENGRAM_SERVER_IMAGE -u ENGRAM_OPERATOR_IMAGE -u ENGRAM_POSTGRES_IMAGE
    done
    make_env "${tmp}/compose-whitespace.env" "${server}" "${operator}" "${postgres}"
    printf '  COMPOSE_PROJECT_NAME = untrusted\n' >>"${tmp}/compose-whitespace.env"
    assert_rejected_before_compose 'whitespace-form COMPOSE_PROJECT_NAME is rejected before compose' "${tmp}/compose-whitespace.env" env -u ENGRAM_SERVER_IMAGE -u ENGRAM_OPERATOR_IMAGE -u ENGRAM_POSTGRES_IMAGE
    make_env "${tmp}/compose-export.env" "${server}" "${operator}" "${postgres}"
    printf 'export COMPOSE_REMOVE_ORPHANS=untrusted\n' >>"${tmp}/compose-export.env"
    assert_rejected_before_compose 'export-form COMPOSE_REMOVE_ORPHANS is rejected before compose' "${tmp}/compose-export.env" env -u ENGRAM_SERVER_IMAGE -u ENGRAM_OPERATOR_IMAGE -u ENGRAM_POSTGRES_IMAGE
    cp "${tmp}/publication-result.json" "${tmp}/invalid-publication.json"
    sed -i 's/"remote_inspection":"complete"/"remote_inspection":"deferred"/' "${tmp}/invalid-publication.json"
    PUBLICATION_RESULT="${tmp}/invalid-publication.json"
    make_env "${tmp}/invalid-publication.env" "${server}" "${operator}" "${postgres}"
    assert_rejected_before_compose 'publication provenance fields are required before compose' "${tmp}/invalid-publication.env" env -u ENGRAM_SERVER_IMAGE -u ENGRAM_OPERATOR_IMAGE -u ENGRAM_POSTGRES_IMAGE

    cp "${tmp}/publication-result.json" "${tmp}/boolean-schema-publication.json"
    sed -i 's/"schema_version":1/"schema_version":true/' "${tmp}/boolean-schema-publication.json"
    PUBLICATION_RESULT="${tmp}/boolean-schema-publication.json"
    make_env "${tmp}/boolean-schema.env" "${server}" "${operator}" "${postgres}"
    assert_rejected_before_compose 'boolean schema version is rejected before compose' "${tmp}/boolean-schema.env" env -u ENGRAM_SERVER_IMAGE -u ENGRAM_OPERATOR_IMAGE -u ENGRAM_POSTGRES_IMAGE

    make_publication_result "${tmp}/publication-result.json"
    PUBLICATION_RESULT="${tmp}/publication-result.json"
    make_env "${tmp}/manifest-mismatch.env" "ghcr.io/thebtf/engram@sha256:$(printf '9%.0s' {1..64})" "${operator}" "${postgres}"
    assert_rejected_before_compose 'image digest must match publication manifest before compose' "${tmp}/manifest-mismatch.env" env -u ENGRAM_SERVER_IMAGE -u ENGRAM_OPERATOR_IMAGE -u ENGRAM_POSTGRES_IMAGE


    printf '\n%d tests, %d failures\n' "${TESTS_RUN}" "${TESTS_FAILED}"
    [[ ${TESTS_FAILED} -eq 0 ]]
}

main "$@"
