// Package redaction implements server-side PII/secret scrubbing before durable storage.
//
// Status: stub package for Milestone F TG6 T051.
// Full implementation lives in TG5 (write-lint, PR #250, NOT on this branch).
//
// EC-F5: When a redaction rule matches and produces empty content, the write is
// rejected with error='content_fully_redacted' rather than storing an empty memory.
// The redaction attempt is still logged.
//
// EC-F9: Redaction rules are loaded from ENGRAM_REDACTION_RULES_PATH at server
// startup only. Hot-reload without SIGHUP is NOT supported in Milestone F.
// See docs/operating-engram.md for the restart-required operator guide.
//
// ADR-F-004: If ENGRAM_REDACTION_RULES_PATH is absent, redaction is a no-op.
// If the env var is set but the file is absent at startup, the server logs a
// warning and runs with redaction disabled (no-op fallback).
package redaction
