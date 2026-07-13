# Release Integration R1 External Review Follow-up

Reviewed head: `cce0832274abf5c2de4807145204791fd8385687`

## Decision

Valid findings were fixed as a single security/runtime batch. Suggestions that would weaken evidence or contradict official release data were rejected with direct proof. Exact-commit image/dev-stand gates remain mandatory before integration.

## Finding classification

| Source | Finding | Classification | Evidence / disposition |
|---|---|---|---|
| Gemini | `[Text.UTF8Encoding]` is not valid | FALSE POSITIVE | PowerShell 7 resolved both `[Text.UTF8Encoding]` and `[System.Text.UTF8Encoding]` to `System.Text.UTF8Encoding`; the call was still made fully qualified for clarity. |
| Gemini | `$script:imageSecretFiles` may be null in cleanup | FALSE POSITIVE | BuildAndScan initializes it to an empty ordered map before entering the `try`; alternate modes exit before cleanup and never call `Remove-PrefixedResources`. |
| Gemini | `Write-DevStandSecretFiles` may recreate an insecure Unix file | SUPERSEDED BY VALID CLASS FIX | The write path now reapplies shared access enforcement after every write. |
| Codex | 0600 file-backed secrets are unreadable by non-root service UIDs | VALID, FIXED | Official Compose docs confirm file sources are bind mounts and uid/gid/mode are ignored. Host roots remain 0700; files are 0644 for container UID 70/65532; Windows uses protected owner + LocalSystem ACLs. Direct UID 65532 bind-read probe PASS. |
| Codex | Runtime credential proof accepts legacy plaintext alongside `_FILE` | VALID, FIXED | Exact inspect proof rejects PostgreSQL, DSN, admin, and both vault plaintext names. Root follow-up additionally requires exactly one value per expected/redacted name, rejecting conflicting duplicates. Hostile self-tests moved RED to GREEN. |
| CodeRabbit | Windows secret paths inherit broad ACLs | VALID, FIXED | Shared helper removes inheritance and grants only current owner + LocalSystem FullControl, then reads the ACL back and asserts it. |
| CodeRabbit | Docker residue should be optional when Docker is absent | REJECTED | This release gate emits `container_residue_checked=true`; returning an empty snapshot without measurement would recreate false evidence. Missing Docker remains a fail-closed dependency error. |
| CodeRabbit | Fixed 300 ms OTLP delay can false-fire | VALID, FIXED | Replaced by bounded 5-second polling with final measured process/container residue. Live smoke PASS. |
| CodeRabbit | Docker Scout v1.23.1 does not exist | FALSE POSITIVE | Official GitHub release API and checksum asset identify v1.23.1 and the pinned archive SHA-256; the earlier independent review also reproduced the checksum. |
| CodeRabbit | Nuxt UI guard misses kebab-case components | VALID, FIXED | Scanner and hostile fixtures now cover PascalCase and kebab-case UForm/UAuthForm. |
| CodeRabbit | Duplicate Scout install blocks should be a composite action | NON-BLOCKING MAINTAINABILITY | Both copies are bound to one exact version/hash by the critical contract. This is not changed inside the security follow-up; secret access logic, where semantic drift mattered, was centralized. |

## Verification

- RED: critical contract rejected the absent access helper.
- RED: `run-db-suite.ps1 -SelfTest` accepted mixed `DATABASE_DSN_FILE` + `DATABASE_DSN` before the fix.
- RED: exact environment proof accepted conflicting duplicate `_FILE` entries before the root follow-up.
- GREEN: final targeted critical runtime contract PASS in 55.841s.
- GREEN: `run-db-suite.ps1 -SelfTest` and `run-dev-stand.ps1 -SelfTest` PASS.
- GREEN: live OTLP smoke PASS for 13/13 required tests with one poll, no timeout, and zero residue.
- GREEN: PowerShell AST parse PASS for all six changed/added scripts.
- GREEN: Windows owner/LocalSystem ACL readback and non-root UID 65532 Docker bind-read probe PASS.
- GREEN: gitleaks 8.30.0 scanned 149,061 cumulative UTF-8 bytes from the batch base, including untracked and ignored review artifacts; no leaks.

External source: <https://docs.docker.com/reference/compose-file/services/#secrets>.
