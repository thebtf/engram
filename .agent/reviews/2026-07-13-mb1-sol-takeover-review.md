# MB1 data-integrity macro-batch — SOL takeover review

## Verdict

`APPROVE_FOR_SYNTHESIS`, not a project-wide release approval.

- Base: `fef455bcf640f849c2d40c9bc26a459b5593e10a`
- Reviewed product head: `05382508a495faa3f350ea6a503cc68ff55cd70f`
- Tree: `7c7fee364ae390d00e54129bb3eb077913d953cf`
- Changed paths: 65
- Canonical path-list SHA-256: `899ba7be4ad24b10a9fd71fb2d11b1eeb0601b98428e5062414610cf4baf9b62`
- Canonical name/status SHA-256: `1780c149438c6e3819db98775d4708f1f0cf566f14f0f5ee838a13ee28a0b564`

## Product result reviewed

The batch closes the six declared data-integrity classes: arbiter claim release and
migration ordering, literal project/path selectors, cross-test governance isolation,
historical-only ingest snapshots, lossless mutation decoding, and fail-closed dream
crystallization with exact project/session provenance. It also adds a checksum-bound
v4.5.0 upgrade replay and retains the accepted candidate-review rollback contract.

## Review findings closed

1. The broad PostgreSQL run exposed session-start tests that assumed an empty global
   rule namespace. The tests now assert inclusion/exclusion by identity and derive the
   router budget outcome from actual `deferred_budget` suppression. The dirty-database
   scenario passed five consecutive runs.
2. `target_memory_id` was strictly parsed but Phase2 re-read the raw value through the
   legacy coercer. Phase2 now consumes the authoritative normalized `int64`; a value
   above 2^53 is covered by a permanent repeated regression.

## Fresh evidence

- Six-group hostile PostgreSQL matrix, three repetitions per group: PASS.
- All touched packages plus tagged recovery/isolation critical packages: PASS except
  the two separately tracked gRPC identity tests described below.
- Full ordinary repository gate: `go test -p=1 ./... -count=1` PASS.
- `go vet ./...` PASS.
- `go build ./...` PASS.
- Focused race gate for dream retry/provenance and strict MCP mutations, three
  repetitions: PASS.
- Pre-v5 fixture provenance and happy/interrupted upgrade behavior: PASS.

## Non-MB1 release blocker preserved

`TestCritical_AuthTwoTier/gRPC_accepts_operator_key` and
`gRPC_accepts_dashboard-issued_keycard` fail with `project identity metadata is
invalid`. The identical focused failures reproduce on exact base `fef455bc`; MB1 does
not regress them and this review does not waive them. They remain mandatory before a
production-ready verdict.
