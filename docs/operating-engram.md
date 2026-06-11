# Operating Engram

Operational guide for engram server administrators. Covers environment configuration,
redaction rules, startup verification, and restart procedures.

---

## Redaction Rules (EC-F5, EC-F9)

### Overview

Engram supports server-side PII and secret scrubbing before durable memory storage.
Redaction is configured via a server-side rule file and runs before the write-lint
phase. The original content is NOT preserved — redaction is destructive.

### Configuration

Set `ENGRAM_REDACTION_RULES_PATH` to the absolute path of the rule file:

```bash
export ENGRAM_REDACTION_RULES_PATH=/etc/engram/redaction-rules.json
```

If `ENGRAM_REDACTION_RULES_PATH` is **unset**, redaction is a **no-op** and the
system behaves exactly as v6.2.x. No configuration change is required to disable
redaction.

### Rule File Format

Rules are JSON objects with `id` (unique rule identifier), `pattern` (regex), and `replacement` fields:

```json
[
  {
    "id": "aws-access-key",
    "pattern": "AKIA[0-9A-Z]{16}",
    "replacement": "[REDACTED:aws-access-key]"
  },
  {
    "id": "github-pat",
    "pattern": "gh[pousr]_[A-Za-z0-9_]{36,}",
    "replacement": "[REDACTED:github-pat]"
  }
]
```

Rules are applied in order. Each match is replaced; unmatched content is preserved.

### Startup Verification

At startup, engram logs the loaded rule file path and its SHA-256 checksum:

```
INFO  redaction: loaded rule file path=/etc/engram/redaction-rules.json sha256=a3f9... rules=2
```

Operators can use this log line to verify the active rule set matches the expected
configuration. If the expected checksum differs from the logged value, a restart with
the correct rule file is required.

If `ENGRAM_REDACTION_RULES_PATH` is set but the file is **absent at startup**,
the server logs a warning and runs with redaction **disabled** (no-op fallback):

```
WARN  redaction: rule file not found — running with redaction disabled path=/etc/engram/redaction-rules.json
```

### Restart Required for Rule Changes (EC-F9)

**Engram does NOT support hot-reload of redaction rules.**

Modifying the rule file on disk while the server is running has **no effect** on the
active rule set. This is intentional: hot-reload without an explicit signal creates
mid-write rule mismatch ambiguity (a write in flight when the rule set changes may
be partially redacted under one rule set and stored under another).

To apply updated rules:

1. Edit the rule file.
2. Restart the server (or send `SIGHUP` if the SIGHUP handler is enabled in your build).
3. Verify the startup log shows the new SHA-256 checksum.

```bash
# Restart example (systemd)
sudo systemctl restart engram-server

# Verify in logs
journalctl -u engram-server | grep "redaction: loaded"
```

> **Note:** SIGHUP-based reload is optional and must be explicitly enabled at build time.
> Consult the build configuration for your deployment. When in doubt, use a full restart.

### Full Redaction Rejection (EC-F5)

If a redaction rule matches and the resulting content is **empty** (the rule matches
the entire content), the write is **rejected** rather than storing an empty memory.
The error code is `content_fully_redacted`.

Example MCP tool response when full redaction occurs:

```json
{
  "error": "content_fully_redacted",
  "rule_id": "aws-access-key",
  "note": "The memory content was fully redacted by the configured rule. Revise the content or update the redaction rules, then retry."
}
```

The redaction attempt is logged to `audit_log` with:
- `action = 'redacted'`
- `rule_id` of the matched rule

The memory row is **not written** to the database.

### Audit Log

All redaction events (successful scrub or full-content rejection) are written to
`audit_log`:

| `action`    | Meaning                                                                                      |
|-------------|----------------------------------------------------------------------------------------------|
| `redacted`  | Content matched a rule. Write succeeds with scrubbed content (partial match), or is rejected with MCP error code `content_fully_redacted` when the entire content was stripped. |

---

## Snapshot Retention (T049)

Bulk operation snapshots (`bulk_op_snapshots` table) are auto-pruned during the
sleep cycle. Default retention: **30 days**.

Override via environment variable:

```bash
export ENGRAM_SNAPSHOT_RETENTION_DAYS=7   # 7-day retention
```

**Pinned snapshots** (rows with `pinned=true`) are exempt from pruning regardless
of age. Pin a snapshot via the `pin_snapshot` MCP tool or the admin dashboard.

---

## Rollback Conflicts (EC-F3)

If a memory affected by a bulk operation has been modified after the snapshot was
captured (`updated_at > snapshot.created_at`), a rollback attempt will be **refused**
atomically. No rows are restored on conflict.

The conflict is reported as:

```json
{
  "error": "rollback_conflict",
  "conflict_ids": [42, 99],
  "snapshot_id": "snap-abc123"
}
```

`conflict_ids` lists the memory IDs that have been modified. To resolve:

1. Review the conflicting memories.
2. If safe to proceed, the operator must manually reset the modified memories or
   create a new snapshot capturing the current state.
3. Retry the rollback with the new snapshot.

The audit log records `action='rollback_attempted_with_conflict'` for all refused
rollback attempts.
