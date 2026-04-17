#!/usr/bin/env bash
# safety-gate_test.sh — Bash unit tests for safety-gate.sh (T006 / Phase 2)
#
# Each test spins up a private temp directory with mock binaries for curl and
# docker on PATH so the real server/Postgres are never contacted.
#
# Usage:
#   bash scripts/safety-gate_test.sh
#
# Exit codes:
#   0  all tests passed
#   1  one or more tests failed

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GATE="${SCRIPT_DIR}/safety-gate.sh"
BASELINE="${SCRIPT_DIR}/safety-gate-baseline.json"

# ---------------------------------------------------------------------------
# Test framework (minimal)
# ---------------------------------------------------------------------------

TESTS_RUN=0
TESTS_PASSED=0
TESTS_FAILED=0

pass() {
    TESTS_RUN=$(( TESTS_RUN + 1 ))
    TESTS_PASSED=$(( TESTS_PASSED + 1 ))
    echo "  PASS: $1"
}

fail() {
    TESTS_RUN=$(( TESTS_RUN + 1 ))
    TESTS_FAILED=$(( TESTS_FAILED + 1 ))
    echo "  FAIL: $1"
    echo "        $2" >&2
}

assert_exit() {
    local label="$1"
    local expected="$2"
    local actual="$3"
    if [[ "${actual}" -eq "${expected}" ]]; then
        pass "${label}: exit code ${expected}"
    else
        fail "${label}: exit code" "expected ${expected}, got ${actual}"
    fi
}

assert_stdout_contains() {
    local label="$1"
    local needle="$2"
    local haystack="$3"
    if echo "${haystack}" | grep -qF "${needle}"; then
        pass "${label}: stdout contains '${needle}'"
    else
        fail "${label}: stdout" "expected '${needle}' in: ${haystack}"
    fi
}

assert_stderr_contains() {
    local label="$1"
    local needle="$2"
    local haystack="$3"
    if echo "${haystack}" | grep -qF "${needle}"; then
        pass "${label}: stderr contains '${needle}'"
    else
        fail "${label}: stderr" "expected '${needle}' in: ${haystack}"
    fi
}

# ---------------------------------------------------------------------------
# Shared mock builder
# ---------------------------------------------------------------------------
# make_mock_dir creates a temporary directory with mock curl and docker
# binaries.  Callers write the script body for each mock.
# Returns the directory path.

make_mock_dir() {
    local dir
    dir="$(mktemp -d /tmp/safety-gate-test.XXXXXX)"

    # Stubs will be overwritten per-test; provide no-op defaults.
    cat > "${dir}/curl" <<'EOF'
#!/usr/bin/env bash
echo "{}" && exit 0
EOF
    cat > "${dir}/docker" <<'EOF'
#!/usr/bin/env bash
echo "0" && exit 0
EOF
    chmod +x "${dir}/curl" "${dir}/docker"
    echo "${dir}"
}

cleanup_mock_dir() {
    local dir="$1"
    rm -rf "${dir}"
}

# run_gate <mock_dir> <extra_args...>
# Runs safety-gate.sh with PATH prepended by mock_dir.
# Captures stdout/stderr; returns exit code via $?.
# Caller reads GATE_STDOUT and GATE_STDERR after call.
GATE_STDOUT=""
GATE_STDERR=""
run_gate() {
    local mock_dir="$1"
    shift
    local stdout_file stderr_file rc
    stdout_file="$(mktemp)"
    stderr_file="$(mktemp)"
    set +e
    PATH="${mock_dir}:${PATH}" \
        ENGRAM_API_TOKEN="test-token-redacted" \
        bash "${GATE}" --baseline "${BASELINE}" "$@" \
        >"${stdout_file}" 2>"${stderr_file}"
    rc=$?
    set -e
    GATE_STDOUT="$(cat "${stdout_file}")"
    GATE_STDERR="$(cat "${stderr_file}")"
    rm -f "${stdout_file}" "${stderr_file}"
    return "${rc}"
}

# ---------------------------------------------------------------------------
# Test 1 — All invariants match: expect exit 0 and "OK" on stdout
# ---------------------------------------------------------------------------

run_test_1_all_match() {
    echo ""
    echo "Test 1: all invariants match"
    local mock_dir
    mock_dir="$(make_mock_dir)"

    # Mock curl: return a vault/status response matching the baseline exactly.
    cat > "${mock_dir}/curl" <<'EOF'
#!/usr/bin/env bash
# Simulate: curl -sf -H "Authorization: ..." <server_url>/api/vault/status
printf '{"fingerprint":"aa78e55cf896508c","credential_count":13,"mismatch_count":0}'
exit 0
EOF
    chmod +x "${mock_dir}/curl"

    # Mock docker: return table counts that satisfy baseline bounds.
    # Called as: docker exec <container> psql ... -tAc "SELECT ..."
    # We inspect the SQL to return the right count.
    cat > "${mock_dir}/docker" <<'EOF'
#!/usr/bin/env bash
# All args are passed; last arg is the SQL string.
sql="${*: -1}"
case "${sql}" in
    *"to_regclass"*)
        # Phase detection: no credentials table yet (pre-us3)
        printf ''
        ;;
    *"issues"*)         printf '78' ;;
    *"api_tokens"*)     printf '3'  ;;
    *"versioned_documents"*) printf '5' ;;
    *"documents"*)      printf '10' ;;
    *"content"*)        printf '20' ;;
    *)                  printf '0'  ;;
esac
exit 0
EOF
    chmod +x "${mock_dir}/docker"

    local rc=0
    run_gate "${mock_dir}" || rc=$?

    assert_exit "t1" 0 "${rc}"
    assert_stdout_contains "t1" "SAFETY-GATE: OK" "${GATE_STDOUT}"
    assert_stdout_contains "t1" "fingerprint=aa78e55cf896508c" "${GATE_STDOUT}"
    assert_stdout_contains "t1" "credentials=13" "${GATE_STDOUT}"

    cleanup_mock_dir "${mock_dir}"
}

# ---------------------------------------------------------------------------
# Test 2 — Fingerprint mismatch: expect exit 1 and diff on stderr
# ---------------------------------------------------------------------------

run_test_2_fingerprint_mismatch() {
    echo ""
    echo "Test 2: fingerprint mismatch"
    local mock_dir
    mock_dir="$(make_mock_dir)"

    cat > "${mock_dir}/curl" <<'EOF'
#!/usr/bin/env bash
# Return a different fingerprint.
printf '{"fingerprint":"deadbeefcafebabe","credential_count":13,"mismatch_count":0}'
exit 0
EOF
    chmod +x "${mock_dir}/curl"

    # docker returns phase-detect empty (pre-us3) + valid counts
    cat > "${mock_dir}/docker" <<'EOF'
#!/usr/bin/env bash
sql="${*: -1}"
case "${sql}" in
    *"to_regclass"*)    printf ''   ;;
    *"issues"*)         printf '78' ;;
    *"api_tokens"*)     printf '3'  ;;
    *"versioned_documents"*) printf '5' ;;
    *"documents"*)      printf '10' ;;
    *"content"*)        printf '20' ;;
    *)                  printf '0'  ;;
esac
exit 0
EOF
    chmod +x "${mock_dir}/docker"

    local rc=0
    run_gate "${mock_dir}" || rc=$?

    assert_exit "t2" 1 "${rc}"
    assert_stderr_contains "t2" "fingerprint: expected aa78e55cf896508c, got deadbeefcafebabe" "${GATE_STDERR}"

    cleanup_mock_dir "${mock_dir}"
}

# ---------------------------------------------------------------------------
# Test 3 — Entity count drift (issues=77 instead of 78): expect exit 1
# ---------------------------------------------------------------------------

run_test_3_entity_drift() {
    echo ""
    echo "Test 3: entity count drift (issues=77, expected 78)"
    local mock_dir
    mock_dir="$(make_mock_dir)"

    # Vault matches
    cat > "${mock_dir}/curl" <<'EOF'
#!/usr/bin/env bash
printf '{"fingerprint":"aa78e55cf896508c","credential_count":13,"mismatch_count":0}'
exit 0
EOF
    chmod +x "${mock_dir}/curl"

    # issues returns 77 — one short
    cat > "${mock_dir}/docker" <<'EOF'
#!/usr/bin/env bash
sql="${*: -1}"
case "${sql}" in
    *"to_regclass"*)    printf ''   ;;
    *"issues"*)         printf '77' ;;
    *"api_tokens"*)     printf '3'  ;;
    *"versioned_documents"*) printf '5' ;;
    *"documents"*)      printf '10' ;;
    *"content"*)        printf '20' ;;
    *)                  printf '0'  ;;
esac
exit 0
EOF
    chmod +x "${mock_dir}/docker"

    local rc=0
    run_gate "${mock_dir}" || rc=$?

    assert_exit "t3" 1 "${rc}"
    assert_stderr_contains "t3" "issues: expected 78, got 77" "${GATE_STDERR}"

    cleanup_mock_dir "${mock_dir}"
}

# ---------------------------------------------------------------------------
# Test 4 (bonus) — --skip-pg: vault matches, no docker calls, exit 0
# ---------------------------------------------------------------------------

run_test_4_skip_pg() {
    echo ""
    echo "Test 4 (bonus): --skip-pg skips Postgres, exits 0"
    local mock_dir
    mock_dir="$(make_mock_dir)"

    cat > "${mock_dir}/curl" <<'EOF'
#!/usr/bin/env bash
printf '{"fingerprint":"aa78e55cf896508c","credential_count":13,"mismatch_count":0}'
exit 0
EOF
    chmod +x "${mock_dir}/curl"

    # docker records a call if invoked — any call is a test failure
    local docker_call_file="${mock_dir}/docker_called"
    cat > "${mock_dir}/docker" <<EOF
#!/usr/bin/env bash
touch "${docker_call_file}"
exit 0
EOF
    chmod +x "${mock_dir}/docker"

    local rc=0
    run_gate "${mock_dir}" --skip-pg || rc=$?

    assert_exit "t4" 0 "${rc}"
    assert_stdout_contains "t4" "SAFETY-GATE: OK" "${GATE_STDOUT}"

    if [[ -f "${docker_call_file}" ]]; then
        fail "t4: docker should not be called with --skip-pg" "docker was invoked"
    else
        pass "t4: docker was not called"
    fi

    cleanup_mock_dir "${mock_dir}"
}

# ---------------------------------------------------------------------------
# Test 5 (bonus) — missing ENGRAM_API_TOKEN: expect exit 2
# ---------------------------------------------------------------------------

run_test_5_missing_token() {
    echo ""
    echo "Test 5 (bonus): missing ENGRAM_API_TOKEN exits with code 2"
    local mock_dir
    mock_dir="$(make_mock_dir)"

    local stdout_file stderr_file rc
    stdout_file="$(mktemp)"
    stderr_file="$(mktemp)"
    set +e
    # Deliberately unset ENGRAM_API_TOKEN
    PATH="${mock_dir}:${PATH}" \
        bash "${GATE}" --baseline "${BASELINE}" --skip-pg \
        >"${stdout_file}" 2>"${stderr_file}"
    rc=$?
    set -e

    local t5_stderr
    t5_stderr="$(cat "${stderr_file}")"
    rm -f "${stdout_file}" "${stderr_file}"

    assert_exit "t5" 2 "${rc}"
    assert_stderr_contains "t5" "ENGRAM_API_TOKEN" "${t5_stderr}"

    cleanup_mock_dir "${mock_dir}"
}

# ---------------------------------------------------------------------------
# Run all tests
# ---------------------------------------------------------------------------

echo "============================================================"
echo "safety-gate_test.sh"
echo "============================================================"

run_test_1_all_match
run_test_2_fingerprint_mismatch
run_test_3_entity_drift
run_test_4_skip_pg
run_test_5_missing_token

echo ""
echo "============================================================"
echo "Results: ${TESTS_PASSED}/${TESTS_RUN} passed, ${TESTS_FAILED} failed"
echo "============================================================"

[[ "${TESTS_FAILED}" -eq 0 ]]
