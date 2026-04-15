package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SnapshotAll writes a snapshot file for every registered Snapshotter module
// in REVERSE registration order, then writes a MANIFEST.json commit-point.
//
// Ordering rationale (design.md §4.3): reverse order ensures that modules
// depending on earlier-registered modules snapshot their derived state first,
// while the modules they depend on are still live.
//
// Per-module atomicity: bytes are written to snapshot.bin.tmp then renamed to
// snapshot.bin so a crash between modules never leaves a partial file visible
// to the next Restore pass.
//
// MANIFEST.json is written last, also via temp+rename, so its presence signals
// a complete snapshot set. If MANIFEST.json is missing on restore (crash between
// the last module write and the manifest write), Pipeline.Restore falls back to
// os.Stat-based discovery — see restore.go.
//
// Errors from individual modules are logged but do NOT abort the fan-out (FR-5).
// Empty byte returns from a module are silently skipped (no file written, no
// manifest entry).
//
// storageDir is the root directory for snapshot files. Module subdirs are
// created as ${storageDir}/<moduleName>/ with 0700 permissions (clarification
// C5 from spec.md).
//
// daemonVersion is embedded in MANIFEST.json for forensic purposes and is not
// interpreted by the restore path.
func (p *Pipeline) SnapshotAll(ctx context.Context, storageDir string, daemonVersion string) ([]ManifestEntry, error) {
	snapshotters := p.reg.ListSnapshotters()

	var entries []ManifestEntry

	// Iterate in REVERSE registration order.
	for i := len(snapshotters) - 1; i >= 0; i-- {
		se := snapshotters[i]
		name := se.Name

		// Bail early if the context is already done — no point continuing.
		select {
		case <-ctx.Done():
			p.logger.Warn("SnapshotAll aborted by context",
				"phase", "snapshot",
				"remaining_modules", i+1,
				"error", ctx.Err(),
			)
			// Write whatever we have so far and return a partial manifest.
			if err := writeManifest(storageDir, daemonVersion, entries); err != nil {
				p.logger.Error("failed to write partial MANIFEST.json after context cancel",
					"phase", "snapshot",
					"error", err,
				)
			}
			return entries, ctx.Err()
		default:
		}

		start := time.Now()
		p.logger.Info("snapshotting module", "module", name, "phase", "snapshot")

		data, err := recoverSnapshot(name, p.logger, se.Snap.Snapshot)
		if err != nil {
			p.logger.Error("module Snapshot failed — skipping",
				"module", name,
				"phase", "snapshot",
				"error", err,
				"duration_ms", time.Since(start).Milliseconds(),
			)
			continue
		}

		// Empty bytes → module declared nothing to persist; skip.
		if len(data) == 0 {
			p.logger.Debug("module Snapshot returned empty bytes — skipping",
				"module", name,
				"phase", "snapshot",
			)
			continue
		}

		// Ensure module subdir exists with 0700 permissions.
		moduleDir := filepath.Join(storageDir, name)
		if mkErr := os.MkdirAll(moduleDir, 0o700); mkErr != nil {
			p.logger.Error("failed to create module snapshot dir — skipping",
				"module", name,
				"phase", "snapshot",
				"dir", moduleDir,
				"error", mkErr,
			)
			continue
		}

		// Atomic write: write to .tmp then rename to final.
		snapshotPath := filepath.Join(moduleDir, "snapshot.bin")
		tmpPath := snapshotPath + ".tmp"

		if writeErr := writeFileAtomic(tmpPath, snapshotPath, data, 0o600); writeErr != nil {
			p.logger.Error("failed to write module snapshot file — skipping",
				"module", name,
				"phase", "snapshot",
				"path", snapshotPath,
				"error", writeErr,
			)
			continue
		}

		entry := ManifestEntry{
			Name:            name,
			File:            filepath.ToSlash(filepath.Join(name, "snapshot.bin")),
			SizeBytes:       int64(len(data)),
			DeclaredVersion: extractEnvelopeVersion(data),
		}

		entries = append(entries, entry)

		p.logger.Info("module snapshot written",
			"module", name,
			"phase", "snapshot",
			"size_bytes", len(data),
			"path", snapshotPath,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}

	// Write MANIFEST.json last — this is the commit point. If the process
	// crashes before this line, the restore path falls back to file scan.
	if err := writeManifest(storageDir, daemonVersion, entries); err != nil {
		p.logger.Error("failed to write MANIFEST.json",
			"phase", "snapshot",
			"error", err,
		)
		return entries, fmt.Errorf("SnapshotAll: write manifest: %w", err)
	}

	p.logger.Info("SnapshotAll complete",
		"phase", "snapshot",
		"modules_snapshotted", len(entries),
		"storage_dir", storageDir,
	)

	return entries, nil
}

// writeFileAtomic writes data to tmpPath then renames to finalPath.
// The temporary file is created with the given permissions.
// On failure the temporary file is removed if possible.
func writeFileAtomic(tmpPath, finalPath string, data []byte, perm os.FileMode) error {
	if err := os.WriteFile(tmpPath, data, perm); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write tmp file %q: %w", tmpPath, err)
	}
	if renameErr := os.Rename(tmpPath, finalPath); renameErr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename %q → %q: %w", tmpPath, finalPath, renameErr)
	}
	return nil
}

// versionOnlyEnvelope is a minimal struct used by extractEnvelopeVersion to
// peek at the version field without importing the full module.SnapshotEnvelope.
type versionOnlyEnvelope struct {
	Version int `json:"version"`
}

// extractEnvelopeVersion attempts to parse the JSON envelope version field
// from snapshot bytes. Returns the version on success or 0 on failure.
// Used by SnapshotAll to embed the declared version in the manifest for
// forensic display; it does not affect the restore path.
func extractEnvelopeVersion(data []byte) int {
	var v versionOnlyEnvelope
	if err := json.Unmarshal(data, &v); err != nil {
		return 0
	}
	if v.Version <= 0 {
		return 0
	}
	return v.Version
}
