#!/usr/bin/env bash
# bench-inject.sh — Measure p50/p95/p99 latency of POST /api/context/inject over 100 warm calls.
# Usage: ENGRAM_URL=http://host:37777 ENGRAM_API_TOKEN=<token> ./scripts/bench-inject.sh
# Output: .agent/reports/f1-latency-delta.json
#
# Baseline p99 is not yet captured (T005 deferred). First run establishes the baseline.
# Delta assertion (>50ms fail) is skipped until baseline is available.

set -euo pipefail

ENGRAM_URL="${ENGRAM_URL:-http://localhost:37777}"
ENGRAM_API_TOKEN="${ENGRAM_API_TOKEN:-}"
ENDPOINT="${ENGRAM_URL}/api/context/inject"
N=100
REPORT_DIR=".agent/reports"
REPORT_FILE="${REPORT_DIR}/f1-latency-delta.json"

BODY='{"project":"bench-inject","session_id":"bench-session-001","cwd":"/tmp"}'

mkdir -p "${REPORT_DIR}"

echo "Running ${N} warm calls to ${ENDPOINT}..."

declare -a TIMINGS

for i in $(seq 1 "${N}"); do
    HTTP_RESPONSE=$(curl -s -o /dev/null -w "%{http_code} %{time_total}" \
        -X POST "${ENDPOINT}" \
        -H "Content-Type: application/json" \
        ${ENGRAM_API_TOKEN:+-H "Authorization: Bearer ${ENGRAM_API_TOKEN}"} \
        -d "${BODY}") || true

    HTTP_CODE=$(echo "${HTTP_RESPONSE}" | awk '{print $1}')
    TIME_TOTAL=$(echo "${HTTP_RESPONSE}" | awk '{print $2}')

    if [[ -z "${HTTP_CODE}" || "${HTTP_CODE}" == "000" ]]; then
        echo "" >&2
        echo "ERROR: curl failed to get HTTP code — server unreachable at ${ENGRAM_URL}" >&2
        exit 2
    fi

    if [[ "${HTTP_CODE}" -lt 200 || "${HTTP_CODE}" -ge 300 ]]; then
        echo "" >&2
        echo "ERROR: HTTP ${HTTP_CODE} on call ${i}" >&2
        exit 1
    fi

    TIMINGS+=("${TIME_TOTAL}")
    printf "."
done
echo ""

# Sort timings numerically and compute percentiles.
# curl reports time_total in seconds with 6 decimal places; convert to ms.
SORTED=$(printf '%s\n' "${TIMINGS[@]}" | sort -n)
COUNT=$(echo "${SORTED}" | wc -l | tr -d ' ')

get_percentile() {
    local pct=$1
    local idx=$(( (COUNT * pct + 99) / 100 ))
    [[ ${idx} -lt 1 ]] && idx=1
    [[ ${idx} -gt ${COUNT} ]] && idx=${COUNT}
    echo "${SORTED}" | sed -n "${idx}p"
}

to_ms() {
    awk "BEGIN { printf \"%.2f\", $1 * 1000 }"
}

P50_MS=$(to_ms "$(get_percentile 50)")
P95_MS=$(to_ms "$(get_percentile 95)")
P99_MS=$(to_ms "$(get_percentile 99)")

echo "Results: p50=${P50_MS}ms  p95=${P95_MS}ms  p99=${P99_MS}ms"

CAPTURED_AT=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

cat > "${REPORT_FILE}" <<EOF
{
  "captured_at": "${CAPTURED_AT}",
  "p50": ${P50_MS},
  "p95": ${P95_MS},
  "p99": ${P99_MS},
  "baseline_p99": null,
  "delta_ms": null,
  "note": "baseline p99 not captured yet — first run establishes baseline"
}
EOF

echo "Report written to ${REPORT_FILE}"
