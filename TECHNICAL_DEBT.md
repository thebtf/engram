# Technical Debt

## Credential Storage (2026-03-12)

### S2: vault_status missing key_source field
**What:** `vault_status` MCP tool does not expose whether the key was auto-generated, loaded from file, or from env var.
**Why deferred:** Additive feature, not a bug. Requires `Vault` struct changes (`source` field) and handler update.
**Impact:** Operators in container environments may not realize they need to back up `vault.key`. The auto-generate path already logs a warning.
**Files:** `internal/crypto/vault.go`, `internal/mcp/tools_credential.go` (handleVaultStatus)

### S3: GetCredential searches by title OR narrative unnecessarily
**What:** `GetCredential` WHERE clause uses `(title = ? OR narrative = ?)` but store_credential always sets both to the same value.
**Why deferred:** Low risk, minor query simplification. The OR widens query surface but cannot match non-credential observations (type filter prevents it).
**Impact:** Negligible performance difference. Slightly wider query surface than needed.
**Files:** `internal/db/gorm/observation_store.go` (GetCredential, DeleteCredential)

### S7: Migration 031 rollback silently swallows errors
**What:** Rollback uses `_ = tx.Exec(s).Error` — failures are silently ignored.
**Why deferred:** Best-effort rollback is a codebase pattern. Logging would improve observability but the migration is simple (DROP COLUMN IF EXISTS).
**Impact:** If rollback step fails, schema may be left in inconsistent state. Low probability given the simplicity of the rollback.
**Files:** `internal/db/gorm/migrations.go` (migration 031 rollback)

### S8: expandTagHierarchy duplicated + tags on credentials are dead data
**What:** `expandTagHierarchy` in `tools_credential.go` duplicates logic from `tools_memory.go`. Tags stored on credentials are excluded from search, making them undiscoverable.
**Why deferred:** No functional impact — tags don't cause errors, they're just unused. Consolidating requires shared utility extraction.
**Impact:** Code duplication. Wasted storage for concept tags on credential observations.
**Files:** `internal/mcp/tools_credential.go`, `internal/mcp/tools_memory.go`

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
