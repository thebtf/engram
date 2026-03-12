# Technical Debt

## Privacy / Secret Detection (2026-03-12)

### ~~P1: privacy/secrets.go functions never called from production code~~ — RESOLVED 2026-03-12
`ContainsSecrets()`, `RedactSecrets()`, and `SanitizeObservation()` in `internal/privacy/secrets.go`
were fully implemented and tested but unreachable from any production path.

**Resolution:**
- `internal/mcp/tools_memory.go` `handleStoreMemory`: changed from hard-reject to warn-and-redact.
  Content containing secrets is now logged at WARN level and redacted via `RedactSecrets()` before storage.
- `internal/worker/handlers_ingest.go` `handleIngestEvent`: secret detection added on `toolInputStr` and
  `toolResultStr` immediately after stringification, before any pipeline processing. Secrets are redacted
  in-memory; the raw event stored in `raw_events` may still contain original data (source-of-truth store).


## Credential Storage (2026-03-12)

### ~~S2: vault_status missing key_source field~~ — RESOLVED 2026-03-12
Added `source` field to `Vault` struct, `KeySource()` method, exposed in `vault_status` as `key_source`.

### ~~S3: GetCredential searches by title OR narrative unnecessarily~~ — RESOLVED 2026-03-12
Simplified to `WHERE title = ?` in both `GetCredential` and `DeleteCredential`.

### ~~S7: Migration 031 rollback silently swallows errors~~ — RESOLVED 2026-03-12
Replaced `_ = tx.Exec(s).Error` with `log.Warn().Err(err)` logging.

### ~~S8: expandTagHierarchy duplicated~~ — RESOLVED 2026-03-12
`tools_memory.go` now calls shared `expandTagHierarchy` from `tools_credential.go` (same package).
Note: tags on credentials remain stored but undiscoverable (filtered from search). Low impact, no functional harm.

### S9: EncryptionKey held as plain string in Config
**What:** `cfg.EncryptionKey` holds the raw hex key as an immutable Go string that cannot be zeroed after use.
**Why deferred:** Go language limitation — strings are immutable. The `Vault.key` `[]byte` field has the same concern. Clearing config after decode is partial mitigation only.
**Impact:** Key material persists in heap until GC. Low practical risk for a server process.
**Files:** `internal/config/config.go`, `internal/crypto/vault.go`

### C2: Move vault state from package-level to Server struct
**What:** `sync.Once` + `sharedVault` + `vaultInitErr` are package-level globals, preventing test isolation.
**Why deferred:** Permanent init failure is correct behavior (requires human intervention). Comment documents the constraint. Moving to Server struct is moderate effort for test-isolation benefit only.
**Impact:** Tests that trigger vault init failure will poison all subsequent tests in the same binary.
**Files:** `internal/mcp/tools_credential.go`
