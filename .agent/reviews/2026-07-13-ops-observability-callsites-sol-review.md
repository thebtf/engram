# OPS observability call-sites — root post-review

Date: 2026-07-13
Candidate: `work/prc-sol-synthesis-r1`
Scope: real gRPC/auth/version/database/index call sites and daemon exporter lifecycle

## Outcome

Root changed-code review: **PASS**.
Independent blind judge: **PENDING (review infrastructure unavailable)**.

The patch closes the previously documented PR-7 call-site blocker without
restoring any v5-demolished path. It uses the supported `otelgrpc` v0.68.0
StatsHandler integration that exactly matches the repository's OTel v1.43.0 and
gRPC v1.80.0 dependency line. Runtime event attributes are fixed program values;
no token, DSN, user/project/root/run ID, or error text enters metric attributes.

## Review findings resolved

1. **Shim telemetry impersonated the daemon.** The first implementation would
   initialize `service.name=engram-daemon` in every short-lived client/shim
   process. A new RED proved the issue; initialization is now restricted to the
   actual `--muxcore-daemon` process. Count-5 and race count-3 are green.
2. **Resource assertion was formatting-sensitive.** Under `-race`, protojson
   inserted spaces and the string assertion failed despite the correct payload.
   Tests now inspect OTLP protobuf resource attributes structurally.
3. **Graceful-restart hard deadline could be exceeded.** A fresh 5-second
   telemetry shutdown context could extend the documented 60-second restart
   budget. The graceful path now derives the flush timeout from the existing
   restart context; ordinary shutdown retains its own bounded 5-second context.

## Behavioral-edge review

- Server transport: a real bufconn RPC emits `rpc.server.call.duration`.
- Auth: missing credentials produce `Unauthenticated` and the bounded
  `auth/missing_credentials` event; credential material is absent.
- Version: an authenticated cross-major request returns `compatible=false` and
  emits `client_version/incompatible`.
- Both daemon gRPC connection classes execute their production dialers and emit
  `rpc.client.call.duration`.
- Database failure is observed at the `gorm.NewStore` production seam without
  changing error propagation or exposing the DSN/driver error.
- Background indexing retains its existing terminal state and logs while adding
  only `index/run_error` or `index/panic` metrics.
- The server remains `engram-server`; only the persistent local daemon is
  `engram-daemon`. Client/shim mode owns no exporter.
- Telemetry shuts down only after the bridge/modules/service have stopped, so
  StatsHandlers do not record through a closed provider on the normal paths.

## Evidence

- TDD and production mutation proof:
  `.agent/specs/ops-observability-call-sites/evidence/OBS-CALLSITES-R1.tdd.json`
- Focused tests: PASS count=5.
- Focused race: PASS count=3 across all changed packages.
- Full changed packages: PASS.
- `go test -p=1 ./... -count=1`: PASS.
- `go vet ./...`: PASS.
- `go build ./...`: PASS.
- OTLP smoke: PASS, no missing required tests.
- Critical suite first run: truthful FAIL 196/197 because `DATABASE_DSN` was
  unset and the isolation test failed closed before execution.
- Dedicated `engram_test` database was verified idle; focused isolation test
  passed; critical rerun `sol-synthesis-20260713-observability-r2`: PASS 197/197,
  0 failed, 0 skipped, parser exit 0, test exit 0.

## Independent review status

AIMux health reported Loom unavailable because its SQLite session store was not
initialized. The review task returned non-retryable `CapabilityMismatch`.
Its solo `peer_review` route produced keyword-only objections unrelated to the
code and is explicitly rejected as blind-judge evidence. This safe-point may be
committed and pushed for PR-based independent review, but it is not the final
blind-judge acceptance record.
