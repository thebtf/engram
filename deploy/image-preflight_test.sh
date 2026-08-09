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
        printf 'UNRELATED_VALUE=$(touch %s.injected)\n' "${path}"
        printf 'ENGRAM_SERVER_IMAGE=%s\n' "${server}"
        printf 'ENGRAM_OPERATOR_IMAGE=%s\n' "${operator}"
        printf 'ENGRAM_POSTGRES_IMAGE=%s\n' "${postgres}"
    } >"${path}"
}

run_wrapper() {
    local source="$1" marker="$2" after_config="$3" after_pull="$4"
    shift 4
    local mock_bin="${marker}.bin"
    mkdir "${mock_bin}"
    cat >"${mock_bin}/docker" <<EOF
#!/usr/bin/env bash
snapshot=""
for ((i = 1; i <= \$#; i++)); do
    if [[ "\${!i}" == --env-file ]]; then
        next=\$((i + 1)); snapshot="\${!next}"; break
    fi
done
server=""
while IFS= read -r line || [[ -n "\${line}" ]]; do
    case "\${line}" in ENGRAM_SERVER_IMAGE=*) server="\${line#*=}";; esac
done < "\${snapshot}"
mode="\$(stat -c '%a' "\${snapshot}")"
printf '%s|%s|%s|%s\\n' "\${*: -1}" "\${snapshot}" "\${mode}" "\${server}" >> "${marker}"
case "\${*: -1}" in
    config) cp "${after_config}" "${source}" ;;
    pull) cp "${after_pull}" "${source}" ;;
esac
EOF
    chmod +x "${mock_bin}/docker"
    set +e
    PATH="${mock_bin}:${PATH}" "$@" bash "${PREFLIGHT}" "${source}"
    local rc=$?
    set -e
    rm -rf "${mock_bin}"
    return "${rc}"
}

assert_rejected_before_compose() {
    local name="$1" source="$2" replacement="$3" marker="${2}.compose" rc=0
    run_wrapper "${source}" "${marker}" "${replacement}" "${replacement}" env -u ENGRAM_SERVER_IMAGE -u ENGRAM_OPERATOR_IMAGE -u ENGRAM_POSTGRES_IMAGE || rc=$?
    if [[ ${rc} -ne 0 && ! -e ${marker} ]]; then pass "${name}"; else fail "${name}"; fi
    rm -f "${marker}"
}

assert_frozen_compose_input() {
    local marker="$1" expected="$2" source="$3" config=0 pull=0 up=0 invalid=0 snapshot_path="" line action snapshot mode actual
    while IFS= read -r line || [[ -n "${line}" ]]; do
        IFS='|' read -r action snapshot mode actual <<<"${line}"
        [[ "${snapshot}" != "${source}" && "${mode}" == 600 && ( -z "${snapshot_path}" || "${snapshot}" == "${snapshot_path}" ) && "${actual}" == "${expected}" ]] || { invalid=1; continue; }
        snapshot_path="${snapshot}"
        case "${action}" in config) config=1 ;; pull) pull=1 ;; -d) up=1 ;; *) invalid=1 ;; esac
    done < "${marker}"
    [[ ${config} -eq 1 && ${pull} -eq 1 && ${up} -eq 1 && ${invalid} -eq 0 ]]
}

main() {
    local tmp=".image-preflight-test.$$"
    mkdir "${tmp}"
    trap "rm -rf '${tmp}'" EXIT
    local server="ghcr.io/example/server@sha256:$(printf 'a%.0s' {1..64})"
    local operator="ghcr.io/example/operator@sha256:$(printf 'b%.0s' {1..64})"
    local postgres="ghcr.io/example/postgres@sha256:$(printf 'c%.0s' {1..64})"
    local changed_after_config="ghcr.io/example/server@sha256:$(printf 'd%.0s' {1..64})"
    local changed_after_pull="ghcr.io/example/server@sha256:$(printf 'e%.0s' {1..64})"
    local replacement_after_config="${tmp}/after-config.env" replacement_after_pull="${tmp}/after-pull.env" marker="${tmp}/valid.compose" rc=0
    make_env "${tmp}/valid.env" "${server}" "${operator}" "${postgres}"
    make_env "${replacement_after_config}" "${changed_after_config}" "${operator}" "${postgres}"
    make_env "${replacement_after_pull}" "${changed_after_pull}" "${operator}" "${postgres}"
    run_wrapper "${tmp}/valid.env" "${marker}" "${replacement_after_config}" "${replacement_after_pull}" env -u ENGRAM_SERVER_IMAGE -u ENGRAM_OPERATOR_IMAGE -u ENGRAM_POSTGRES_IMAGE || rc=$?
    if [[ ${rc} -eq 0 && ! -e ${tmp}/valid.env.injected ]] && assert_frozen_compose_input "${marker}" "${server}" "${tmp}/valid.env"; then
        pass 'valid input uses one frozen snapshot through config pull and up without evaluation'
    else
        fail 'valid input uses one frozen snapshot through config pull and up without evaluation'
    fi
    rm -f "${marker}"

    make_env "${tmp}/invalid.env" "${server}" "${operator}" "${postgres}"
    printf 'ENGRAM_SERVER_IMAGE=%s\n' "${server}" >>"${tmp}/invalid.env"
    assert_rejected_before_compose 'duplicate bare assignment rejected' "${tmp}/invalid.env" "${replacement_after_config}"

    for syntax in "export ENGRAM_SERVER_IMAGE=${server}" "ENGRAM_SERVER_IMAGE=\"${server}\"" "ENGRAM_SERVER_IMAGE=${server} # pinned" "ENGRAM_SERVER_IMAGE=\${OTHER_IMAGE}"; do
        make_env "${tmp}/syntax.env" "${server}" "${operator}" "${postgres}"
        printf '%s\n' "${syntax}" >>"${tmp}/syntax.env"
        assert_rejected_before_compose "managed nonliteral syntax rejected: ${syntax%%=*}" "${tmp}/syntax.env" "${replacement_after_config}"
    done

    make_env "${tmp}/malformed.env" 'ghcr.io/example/server:latest' "${operator}" "${postgres}"
    assert_rejected_before_compose 'mutable tag rejected before compose' "${tmp}/malformed.env" "${replacement_after_config}"

    make_env "${tmp}/override.env" "${server}" "${operator}" "${postgres}"
    marker="${tmp}/override.compose"; rc=0
    run_wrapper "${tmp}/override.env" "${marker}" "${replacement_after_config}" "${replacement_after_pull}" env ENGRAM_SERVER_IMAGE="${changed_after_config}" || rc=$?
    if [[ ${rc} -ne 0 && ! -e ${marker} ]]; then pass 'different process override rejected before compose'; else fail 'different process override rejected before compose'; fi

    printf '\n%d tests, %d failures\n' "${TESTS_RUN}" "${TESTS_FAILED}"
    [[ ${TESTS_FAILED} -eq 0 ]]
}

main "$@"
