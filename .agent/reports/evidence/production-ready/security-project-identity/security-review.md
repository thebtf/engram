# SECURITY-PROJECT-IDENTITY Security Review

Classification: **S3 / High**. Project identity selects a tenant namespace and
therefore sits on the path to private data, even though it is deliberately not
an authentication credential.

## Security invariants

- HTTP bearer/session middleware and the gRPC auth interceptor execute
  independently of project resolution. A known selector, git remote/path, or
  non-git anchor never creates a principal or grants private visibility.
- Full metadata is validated before database access. Unknown versions, mixed
  source forms, raw-vs-normalized selectors, control characters, traversal,
  overlong fields, weak anchors, and absent sharing presence fail with
  `PROJECT_IDENTITY_INVALID`.
- Registration takes deterministic transaction-scoped PostgreSQL advisory
  locks. Repeated/concurrent calls converge; legacy-only ambiguity counts every
  candidate and fails closed rather than selecting `LIMIT 1`.
- The binding key is a 128-bit SHA-256 prefix. Raw non-git anchors are never
  written to PostgreSQL, logs, errors, or response payloads.
- Soft-deleted deterministic bindings cannot receive aliases or be silently
  revived. Registration returns the stable unavailable contract instead.
- Database diagnostics are not serialized. HTTP and gRPC expose only stable
  public messages plus code/reason and `upgrade_action`.
- Every OpenClaw project consumer awaits registration before its first data
  operation: 20 files, 22 resolve calls, 22 awaited barriers. Registration
  failure permits zero downstream project requests.
- Claude continues its historical offline fallback only for positively
  classified transport failures. HTTP failures, malformed reached-server
  responses, and other errors are hard barriers and skip the hook handler.

## STRIDE assessment

| Threat | Control and executable evidence |
| --- | --- |
| Spoofing | Identity is namespace metadata only. gRPC auth-interceptor and invalid-bearer OpenClaw tests prove selector knowledge does not bypass bearer validation. |
| Tampering | Strict cross-language validation, exact three-field anchor documents, cryptographic generation, transaction locks, and soft-delete collision tests. |
| Repudiation | Stable machine-readable codes/actions make conflict outcomes auditable without logging raw anchors or database internals. |
| Information disclosure | Resolver returns only the canonical selector; ambiguity does not enumerate candidates; DB details and secrets are absent from added lines and transport errors. |
| Denial of service | Metadata is bounded, registration is timeout-controlled, lock keys are bounded/deterministic, and duplicate locks are removed. |
| Elevation of privilege | Principal authorization remains a separate middleware/interceptor and visibility-filter concern after identity resolution. |

## Compatibility and rollback

- Protobuf changes are additive; existing tags are unchanged. Regeneration is
  byte-reproducible with the recorded toolchain.
- Old clients retain their outer `project` selector. The separate
  `legacy_project` field remains an alias and cannot replace the outer canonical
  selector on a fresh database.
- New clients to an old server retain the outer selector when additive metadata
  or `canonical_project` is unknown. Old clients to a new server fail closed
  only when a legacy selector is genuinely ambiguous.
- No migration or request-time DDL was introduced. Rollback binaries see
  ordinary `projects` rows and aliases. A rollback also rolls back the new
  ambiguity enforcement; operational compatibility is proven, but the security
  improvement itself naturally requires the new server binary.

## Residual risks and deliberate fail-closed behavior

1. A process crash during first anchor-file write can leave a malformed partial
   file. All clients then fail closed until the operator repairs/removes that
   file; they never regenerate over an existing malformed identity.
2. A soft-deleted deterministic binding collision returns 503 and requires an
   explicit operator lifecycle decision. Silent resurrection was rejected.
3. Identity remains global namespace routing in the existing `projects` table;
   confidentiality still depends on the existing bearer/principal visibility
   system, as designed and tested.
4. Fresh-database testing surfaced two existing non-fatal migration warnings:
   historical migration 040 references the v5-demolished
   `observation_vectors`, and migration 109 skips optional vectorscale in a
   pgvector-only image. Neither warning changes identity correctness or schema.

Verdict: **maker security gate PASS; independent checker required before
integration**.
