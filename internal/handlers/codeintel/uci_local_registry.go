package codeintel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

const (
	uciLocalRegistrySchemaVersion = 2
	uciLocalSQLiteBusyTimeoutMS   = 5000

	uciLocalMaxOpaqueIDBytes      = 512
	uciLocalMaxFingerprintBytes   = 512
	uciLocalMaxRootPathBytes      = 4096
	uciLocalMaxRelativePathBytes  = 4096
	uciLocalMaxDirtyPaths         = int64(4096)
	uciLocalMaxRecoveryDirtyPaths = int64(4096)
)

// UCILocalRescanCause describes why incremental reconciliation is no longer
// sufficient for the local operational record.
type UCILocalRescanCause string

const (
	UCILocalRescanSequenceGap                UCILocalRescanCause = "sequence_gap"
	UCILocalRescanStaleSequence              UCILocalRescanCause = "stale_sequence"
	UCILocalRescanOfflineRecoveryBound       UCILocalRescanCause = "offline_recovery_bound"
	UCILocalRescanOverflow                   UCILocalRescanCause = "overflow"
	UCILocalRescanRestart                    UCILocalRescanCause = "restart"
	UCILocalRescanMove                       UCILocalRescanCause = "move"
	UCILocalRescanGitTransition              UCILocalRescanCause = "git_transition"
	UCILocalRescanWatcherRegistrationFailure UCILocalRescanCause = "watcher_registration_failure"
)

// UCILocalApprovedRoot is local evidence that one root may be tracked. Its
// opaque identifiers are correlation values only; they do not authorize access.
type UCILocalApprovedRoot struct {
	RootID                  string
	SourceID                string
	CommonGitDirFingerprint string
	RootPath                string
}

// UCILocalCheckoutRegistration binds opaque server identifiers to local
// workstation evidence without making either a source of authority. The
// server supplies IncarnationID; this registry never creates one.
type UCILocalCheckoutRegistration struct {
	RootID                   string
	SourceID                 string
	CheckoutID               string
	IncarnationID            string
	CommonGitDirFingerprint  string
	PrivateGitDirFingerprint string
	WorkstationID            string
	ClientInstanceID         string
}

// UCILocalDirtyChange is one coalescible path change observed by a watcher.
type UCILocalDirtyChange struct {
	CheckoutID   string
	RelativePath string
	Sequence     int64
}

// UCILocalOfflineRecovery is the bounded non-content recovery watermark for
// one checkout. It intentionally contains counts, never queued source bodies.
type UCILocalOfflineRecovery struct {
	CheckoutID         string
	FromSequence       int64
	ThroughSequence    int64
	MaxDirtyPaths      int64
	ObservedDirtyPaths int64
}

// UCILocalCheckoutRecord is an immutable snapshot of local operational state.
// DirtyPaths is always a caller-owned map.
type UCILocalCheckoutRecord struct {
	RootID                   string
	SourceID                 string
	CheckoutID               string
	CommonGitDirFingerprint  string
	PrivateGitDirFingerprint string
	WorkstationID            string
	ClientInstanceID         string
	IncarnationID            string
	DirtySequence            int64
	DirtyPaths               map[string]int64
	RescanRequired           bool
	RescanCauses             []UCILocalRescanCause
	LastReconciledSequence   int64
	OfflineRecovery          UCILocalOfflineRecovery
}

// UCILocalRegistry owns only daemon-local SQLite operational evidence. The
// caller owns db lifecycle.
type UCILocalRegistry struct {
	db *sql.DB
}

// NewUCILocalRegistry opens an idempotent, version-checked local registry.
func NewUCILocalRegistry(db *sql.DB) (*UCILocalRegistry, error) {
	if db == nil {
		return nil, errors.New("uci local registry: database is required")
	}

	// A registry is a small, transactional dirty-set. One connection also keeps
	// SQLite :memory: callers on the schema initialized below.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	registry := &UCILocalRegistry{db: db}
	if err := registry.initialize(context.Background()); err != nil {
		return nil, err
	}
	return registry, nil
}

// RecordApprovedRoot records or refreshes local root evidence. It does not
// create a checkout or imply any remote authorization.
func (registry *UCILocalRegistry) RecordApprovedRoot(ctx context.Context, root UCILocalApprovedRoot) error {
	if err := validateUCILocalApprovedRoot(root); err != nil {
		return err
	}

	return registry.withImmediate(ctx, func(conn *sql.Conn) error {
		existing, found, err := loadUCILocalApprovedRoot(ctx, conn, root.RootID)
		if err != nil {
			return err
		}
		if found {
			if existing.SourceID != root.SourceID || existing.CommonGitDirFingerprint != root.CommonGitDirFingerprint {
				return errors.New("uci local registry: root ID is already bound to different evidence")
			}
			if existing.RootPath == root.RootPath {
				return nil
			}
			_, err = conn.ExecContext(ctx, `
				UPDATE uci_local_approved_roots
				SET local_root_path = ?
				WHERE root_id = ?`, root.RootPath, root.RootID)
			return uciLocalRegistryOperationError("update approved root path", err)
		}
		_, err = conn.ExecContext(ctx, `
			INSERT INTO uci_local_approved_roots (
				root_id, server_source_id, common_git_dir_fingerprint, local_root_path
			) VALUES (?, ?, ?, ?)`,
			root.RootID,
			root.SourceID,
			root.CommonGitDirFingerprint,
			root.RootPath,
		)
		return uciLocalRegistryOperationError("record approved root", err)
	})
}

// ApprovedRoot returns local root evidence by its opaque root identifier.
func (registry *UCILocalRegistry) ApprovedRoot(ctx context.Context, rootID string) (UCILocalApprovedRoot, bool, error) {
	if err := validateUCILocalValue("root_id", rootID, uciLocalMaxOpaqueIDBytes); err != nil {
		return UCILocalApprovedRoot{}, false, err
	}

	var root UCILocalApprovedRoot
	var found bool
	err := registry.withRead(ctx, func(conn *sql.Conn) error {
		var err error
		root, found, err = loadUCILocalApprovedRoot(ctx, conn, rootID)
		return err
	})
	if err != nil {
		return UCILocalApprovedRoot{}, false, err
	}
	return root, found, nil
}

// RegisterCheckout persists workstation evidence for one opaque checkout. An
// exact retry keeps state. A changed server incarnation resets dirty and
// recovery state; changed local evidence with the same incarnation is refused.
func (registry *UCILocalRegistry) RegisterCheckout(ctx context.Context, registration UCILocalCheckoutRegistration) (UCILocalCheckoutRecord, error) {
	if err := validateUCILocalCheckoutRegistration(registration); err != nil {
		return UCILocalCheckoutRecord{}, err
	}

	var result UCILocalCheckoutRecord
	err := registry.withImmediate(ctx, func(conn *sql.Conn) error {
		root, found, err := loadUCILocalApprovedRoot(ctx, conn, registration.RootID)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("uci local registry: approved root is required")
		}
		if root.SourceID != registration.SourceID || root.CommonGitDirFingerprint != registration.CommonGitDirFingerprint {
			return errors.New("uci local registry: approved root does not match local evidence")
		}

		existing, found, err := loadUCILocalCheckout(ctx, conn, registration.CheckoutID)
		if err != nil {
			return err
		}
		if !found {
			if err := insertUCILocalCheckout(ctx, conn, registration, true); err != nil {
				return err
			}
			result, _, err = loadUCILocalCheckout(ctx, conn, registration.CheckoutID)
			return err
		}

		if sameUCILocalRegistration(existing, registration) {
			result = existing
			return nil
		}
		if existing.IncarnationID == registration.IncarnationID {
			return errors.New("uci local registry: checkout evidence changed without a new server incarnation")
		}
		if err := replaceUCILocalCheckout(ctx, conn, registration); err != nil {
			return err
		}
		if err := resetUCILocalCheckoutOperationalState(ctx, conn, registration.CheckoutID); err != nil {
			return err
		}

		result, _, err = loadUCILocalCheckout(ctx, conn, registration.CheckoutID)
		return err
	})
	if err != nil {
		return UCILocalCheckoutRecord{}, err
	}
	return result, nil
}

// RecordDirty atomically coalesces one path into the dirty-set. An exact replay
// is a no-op. A different path or higher per-path sequence is retained even
// when its checkout-level sequence arrives out of order.
func (registry *UCILocalRegistry) RecordDirty(ctx context.Context, change UCILocalDirtyChange) (UCILocalCheckoutRecord, error) {
	relativePath, err := validateUCILocalDirtyChange(change)
	if err != nil {
		return UCILocalCheckoutRecord{}, err
	}

	var result UCILocalCheckoutRecord
	err = registry.withImmediate(ctx, func(conn *sql.Conn) error {
		current, found, err := loadUCILocalCheckout(ctx, conn, change.CheckoutID)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("uci local registry: checkout is not registered")
		}

		if knownSequence, found := current.DirtyPaths[relativePath]; found && knownSequence == change.Sequence {
			result = current
			return nil
		}

		stale := change.Sequence <= current.DirtySequence
		if stale {
			if err := setUCILocalRescanCause(ctx, conn, change.CheckoutID, UCILocalRescanStaleSequence); err != nil {
				return err
			}
		} else if change.Sequence != current.DirtySequence+1 {
			if err := setUCILocalRescanCause(ctx, conn, change.CheckoutID, UCILocalRescanSequenceGap); err != nil {
				return err
			}
		}

		if _, exists := current.DirtyPaths[relativePath]; !exists && int64(len(current.DirtyPaths)) >= uciLocalMaxDirtyPaths {
			if err := setUCILocalRescanCause(ctx, conn, change.CheckoutID, UCILocalRescanOfflineRecoveryBound); err != nil {
				return err
			}
		} else {
			_, err := conn.ExecContext(ctx, `
				INSERT INTO uci_local_dirty_paths (server_checkout_id, relative_path, sequence)
				VALUES (?, ?, ?)
				ON CONFLICT(server_checkout_id, relative_path) DO UPDATE SET
					sequence = excluded.sequence
				WHERE excluded.sequence > uci_local_dirty_paths.sequence`,
				change.CheckoutID,
				relativePath,
				change.Sequence,
			)
			if err != nil {
				return uciLocalRegistryOperationError("record dirty path", err)
			}
		}

		if !stale {
			_, err = conn.ExecContext(ctx, `
				UPDATE uci_local_checkouts
				SET dirty_sequence = ?
				WHERE server_checkout_id = ?`,
				change.Sequence,
				change.CheckoutID,
			)
			if err != nil {
				return uciLocalRegistryOperationError("advance dirty sequence", err)
			}
		}

		result, _, err = loadUCILocalCheckout(ctx, conn, change.CheckoutID)
		return err
	})
	if err != nil {
		return UCILocalCheckoutRecord{}, err
	}
	return result, nil
}

// RequireRescan records a coalesced operational reason to rebuild from current
// local bytes. It neither selects a context nor performs reconciliation.
func (registry *UCILocalRegistry) RequireRescan(ctx context.Context, checkoutID string, cause UCILocalRescanCause) (UCILocalCheckoutRecord, error) {
	if err := validateUCILocalValue("checkout_id", checkoutID, uciLocalMaxOpaqueIDBytes); err != nil {
		return UCILocalCheckoutRecord{}, err
	}
	if !validUCILocalRescanCause(cause) {
		return UCILocalCheckoutRecord{}, errors.New("uci local registry: invalid rescan cause")
	}

	var result UCILocalCheckoutRecord
	err := registry.withImmediate(ctx, func(conn *sql.Conn) error {
		if _, found, err := loadUCILocalCheckout(ctx, conn, checkoutID); err != nil {
			return err
		} else if !found {
			return errors.New("uci local registry: checkout is not registered")
		}
		if err := setUCILocalRescanCause(ctx, conn, checkoutID, cause); err != nil {
			return err
		}
		var err error
		result, _, err = loadUCILocalCheckout(ctx, conn, checkoutID)
		return err
	})
	if err != nil {
		return UCILocalCheckoutRecord{}, err
	}
	return result, nil
}

// MarkReconciled records the accepted durable sequence and clears only dirty
// entries at or before it. A full acknowledgement at the current high-water
// clears prior rescan causes; later watcher events remain dirty.
func (registry *UCILocalRegistry) MarkReconciled(ctx context.Context, checkoutID string, sequence int64) (UCILocalCheckoutRecord, error) {
	if err := validateUCILocalValue("checkout_id", checkoutID, uciLocalMaxOpaqueIDBytes); err != nil {
		return UCILocalCheckoutRecord{}, err
	}
	if sequence < 0 {
		return UCILocalCheckoutRecord{}, errors.New("uci local registry: reconciled sequence must not be negative")
	}

	var result UCILocalCheckoutRecord
	err := registry.withImmediate(ctx, func(conn *sql.Conn) error {
		current, found, err := loadUCILocalCheckout(ctx, conn, checkoutID)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("uci local registry: checkout is not registered")
		}
		if sequence > current.DirtySequence {
			if err := setUCILocalRescanCause(ctx, conn, checkoutID, UCILocalRescanSequenceGap); err != nil {
				return err
			}
			result, _, err = loadUCILocalCheckout(ctx, conn, checkoutID)
			return err
		}
		if sequence < current.LastReconciledSequence {
			result = current
			return nil
		}

		if sequence > current.LastReconciledSequence {
			_, err = conn.ExecContext(ctx, `
				UPDATE uci_local_checkouts
				SET last_reconciled_sequence = ?
				WHERE server_checkout_id = ?`,
				sequence,
				checkoutID,
			)
			if err != nil {
				return uciLocalRegistryOperationError("advance reconciled sequence", err)
			}
		}
		_, err = conn.ExecContext(ctx, `
			DELETE FROM uci_local_dirty_paths
			WHERE server_checkout_id = ? AND sequence <= ?`,
			checkoutID,
			sequence,
		)
		if err != nil {
			return uciLocalRegistryOperationError("clear reconciled dirty paths", err)
		}
		if sequence == current.DirtySequence {
			if err := clearUCILocalRescanState(ctx, conn, checkoutID); err != nil {
				return err
			}
		}

		result, _, err = loadUCILocalCheckout(ctx, conn, checkoutID)
		return err
	})
	if err != nil {
		return UCILocalCheckoutRecord{}, err
	}
	return result, nil
}

// RecordOfflineRecovery retains one bounded recovery watermark per checkout.
// When that watermark exceeds known local state or its configured capacity, the
// registry retains the metadata and requires a current-byte rescan.
func (registry *UCILocalRegistry) RecordOfflineRecovery(ctx context.Context, recovery UCILocalOfflineRecovery) (UCILocalCheckoutRecord, error) {
	if err := validateUCILocalOfflineRecovery(recovery); err != nil {
		return UCILocalCheckoutRecord{}, err
	}
	bounded, overflow := boundUCILocalOfflineRecovery(recovery)

	var result UCILocalCheckoutRecord
	err := registry.withImmediate(ctx, func(conn *sql.Conn) error {
		current, found, err := loadUCILocalCheckout(ctx, conn, bounded.CheckoutID)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("uci local registry: checkout is not registered")
		}

		prior := current.OfflineRecovery
		if prior.CheckoutID != "" {
			if bounded.ThroughSequence < prior.ThroughSequence {
				if err := setUCILocalRescanCause(ctx, conn, bounded.CheckoutID, UCILocalRescanStaleSequence); err != nil {
					return err
				}
				result, _, err = loadUCILocalCheckout(ctx, conn, bounded.CheckoutID)
				return err
			}
			if bounded.ThroughSequence == prior.ThroughSequence && !sameUCILocalOfflineRecovery(bounded, prior) {
				if err := setUCILocalRescanCause(ctx, conn, bounded.CheckoutID, UCILocalRescanStaleSequence); err != nil {
					return err
				}
				result, _, err = loadUCILocalCheckout(ctx, conn, bounded.CheckoutID)
				return err
			}
		}

		if bounded.FromSequence > current.DirtySequence+1 {
			if err := setUCILocalRescanCause(ctx, conn, bounded.CheckoutID, UCILocalRescanSequenceGap); err != nil {
				return err
			}
		}
		if overflow {
			if err := setUCILocalRescanCause(ctx, conn, bounded.CheckoutID, UCILocalRescanOfflineRecoveryBound); err != nil {
				return err
			}
		}

		_, err = conn.ExecContext(ctx, `
			INSERT INTO uci_local_offline_recovery (
				server_checkout_id, from_sequence, through_sequence,
				max_dirty_paths, observed_dirty_paths
			) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(server_checkout_id) DO UPDATE SET
				from_sequence = excluded.from_sequence,
				through_sequence = excluded.through_sequence,
				max_dirty_paths = excluded.max_dirty_paths,
				observed_dirty_paths = excluded.observed_dirty_paths`,
			bounded.CheckoutID,
			bounded.FromSequence,
			bounded.ThroughSequence,
			bounded.MaxDirtyPaths,
			bounded.ObservedDirtyPaths,
		)
		if err != nil {
			return uciLocalRegistryOperationError("record offline recovery", err)
		}

		result, _, err = loadUCILocalCheckout(ctx, conn, bounded.CheckoutID)
		return err
	})
	if err != nil {
		return UCILocalCheckoutRecord{}, err
	}
	return result, nil
}

// Snapshot returns a caller-owned snapshot of one local checkout record.
func (registry *UCILocalRegistry) Snapshot(ctx context.Context, checkoutID string) (UCILocalCheckoutRecord, bool, error) {
	if err := validateUCILocalValue("checkout_id", checkoutID, uciLocalMaxOpaqueIDBytes); err != nil {
		return UCILocalCheckoutRecord{}, false, err
	}

	var record UCILocalCheckoutRecord
	var found bool
	err := registry.withRead(ctx, func(conn *sql.Conn) error {
		var err error
		record, found, err = loadUCILocalCheckout(ctx, conn, checkoutID)
		return err
	})
	if err != nil {
		return UCILocalCheckoutRecord{}, false, err
	}
	return record, found, nil
}

func (registry *UCILocalRegistry) initialize(ctx context.Context) error {
	conn, err := registry.db.Conn(ctx)
	if err != nil {
		return uciLocalRegistryOperationError("open database connection", err)
	}
	defer conn.Close()

	if err := configureUCILocalSQLiteConnection(ctx, conn, true); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return uciLocalRegistryOperationError("begin schema transaction", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackUCILocalSQLite(conn)
		}
	}()

	for _, statement := range uciLocalRegistrySchema {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return uciLocalRegistryOperationError("apply schema", err)
		}
	}

	var version int
	err = conn.QueryRowContext(ctx, `
		SELECT version FROM uci_local_registry_schema WHERE singleton = 1`).Scan(&version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO uci_local_registry_schema (singleton, version)
			VALUES (1, ?)`, uciLocalRegistrySchemaVersion); err != nil {
			return uciLocalRegistryOperationError("record schema version", err)
		}
		version = uciLocalRegistrySchemaVersion
	case err != nil:
		return uciLocalRegistryOperationError("read schema version", err)
	}

	for version != uciLocalRegistrySchemaVersion {
		switch version {
		case 1:
			if err := migrateUCILocalRegistryV1ToV2(ctx, conn); err != nil {
				return err
			}
			version = 2
		default:
			return fmt.Errorf("uci local registry: unsupported schema version %d", version)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return uciLocalRegistryOperationError("commit schema transaction", err)
	}
	committed = true
	return nil
}

func (registry *UCILocalRegistry) withImmediate(ctx context.Context, operation func(*sql.Conn) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	conn, err := registry.db.Conn(ctx)
	if err != nil {
		return uciLocalRegistryOperationError("open database connection", err)
	}
	defer conn.Close()
	if err := configureUCILocalSQLiteConnection(ctx, conn, false); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return uciLocalRegistryOperationError("begin write transaction", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackUCILocalSQLite(conn)
		}
	}()

	if err := operation(conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return uciLocalRegistryOperationError("commit write transaction", err)
	}
	committed = true
	return nil
}

func (registry *UCILocalRegistry) withRead(ctx context.Context, operation func(*sql.Conn) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	conn, err := registry.db.Conn(ctx)
	if err != nil {
		return uciLocalRegistryOperationError("open database connection", err)
	}
	defer conn.Close()
	if err := configureUCILocalSQLiteConnection(ctx, conn, false); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return uciLocalRegistryOperationError("begin read transaction", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackUCILocalSQLite(conn)
		}
	}()

	if err := operation(conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return uciLocalRegistryOperationError("commit read transaction", err)
	}
	committed = true
	return nil
}

func configureUCILocalSQLiteConnection(ctx context.Context, conn *sql.Conn, initialize bool) error {
	pragmas := []string{
		"PRAGMA foreign_keys=ON",
		fmt.Sprintf("PRAGMA busy_timeout=%d", uciLocalSQLiteBusyTimeoutMS),
		"PRAGMA synchronous=NORMAL",
	}
	if initialize {
		pragmas = append([]string{"PRAGMA journal_mode=WAL"}, pragmas...)
	}
	for _, pragma := range pragmas {
		if _, err := conn.ExecContext(ctx, pragma); err != nil {
			return uciLocalRegistryOperationError("configure SQLite", err)
		}
	}
	return nil
}

func rollbackUCILocalSQLite(conn *sql.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _ = conn.ExecContext(ctx, "ROLLBACK")
}

var uciLocalRegistrySchema = []string{
	`CREATE TABLE IF NOT EXISTS uci_local_registry_schema (
		singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
		version INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS uci_local_approved_roots (
		root_id TEXT PRIMARY KEY,
		server_source_id TEXT NOT NULL,
		common_git_dir_fingerprint TEXT NOT NULL,
		local_root_path TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS uci_local_checkouts (
		server_checkout_id TEXT PRIMARY KEY,
		root_id TEXT NOT NULL,
		server_source_id TEXT NOT NULL,
		common_git_dir_fingerprint TEXT NOT NULL,
		private_git_dir_fingerprint TEXT NOT NULL,
		workstation_id TEXT NOT NULL,
		client_instance_id TEXT NOT NULL,
		incarnation_id TEXT NOT NULL,
		dirty_sequence INTEGER NOT NULL DEFAULT 0 CHECK (dirty_sequence >= 0),
		last_reconciled_sequence INTEGER NOT NULL DEFAULT 0 CHECK (last_reconciled_sequence >= 0),
		rescan_required INTEGER NOT NULL DEFAULT 0 CHECK (rescan_required IN (0, 1)),
		FOREIGN KEY (root_id) REFERENCES uci_local_approved_roots(root_id)
	)`,
	`CREATE TABLE IF NOT EXISTS uci_local_dirty_paths (
		server_checkout_id TEXT NOT NULL,
		relative_path TEXT NOT NULL,
		sequence INTEGER NOT NULL CHECK (sequence > 0),
		PRIMARY KEY (server_checkout_id, relative_path),
		FOREIGN KEY (server_checkout_id) REFERENCES uci_local_checkouts(server_checkout_id) ON DELETE CASCADE
	)`,
	`CREATE TABLE IF NOT EXISTS uci_local_rescan_causes (
		server_checkout_id TEXT NOT NULL,
		cause TEXT NOT NULL,
		PRIMARY KEY (server_checkout_id, cause),
		FOREIGN KEY (server_checkout_id) REFERENCES uci_local_checkouts(server_checkout_id) ON DELETE CASCADE
	)`,
	`CREATE TABLE IF NOT EXISTS uci_local_offline_recovery (
		server_checkout_id TEXT PRIMARY KEY,
		from_sequence INTEGER NOT NULL CHECK (from_sequence >= 0),
		through_sequence INTEGER NOT NULL CHECK (through_sequence >= from_sequence),
		max_dirty_paths INTEGER NOT NULL CHECK (max_dirty_paths > 0),
		observed_dirty_paths INTEGER NOT NULL CHECK (observed_dirty_paths >= 0),
		FOREIGN KEY (server_checkout_id) REFERENCES uci_local_checkouts(server_checkout_id) ON DELETE CASCADE
	)`,
}

func migrateUCILocalRegistryV1ToV2(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `
		ALTER TABLE uci_local_approved_roots
		ADD COLUMN local_root_path TEXT NOT NULL DEFAULT ''`); err != nil {
		return uciLocalRegistryOperationError("migrate schema v1 to v2", err)
	}
	_, err := conn.ExecContext(ctx, `
		UPDATE uci_local_registry_schema
		SET version = ?
		WHERE singleton = 1`, uciLocalRegistrySchemaVersion)
	return uciLocalRegistryOperationError("record schema migration", err)
}

func loadUCILocalApprovedRoot(ctx context.Context, conn *sql.Conn, rootID string) (UCILocalApprovedRoot, bool, error) {
	var root UCILocalApprovedRoot
	err := conn.QueryRowContext(ctx, `
		SELECT root_id, server_source_id, common_git_dir_fingerprint, local_root_path
		FROM uci_local_approved_roots
		WHERE root_id = ?`, rootID,
	).Scan(&root.RootID, &root.SourceID, &root.CommonGitDirFingerprint, &root.RootPath)
	if errors.Is(err, sql.ErrNoRows) {
		return UCILocalApprovedRoot{}, false, nil
	}
	if err != nil {
		return UCILocalApprovedRoot{}, false, uciLocalRegistryOperationError("load approved root", err)
	}
	return root, true, nil
}

func loadUCILocalCheckout(ctx context.Context, conn *sql.Conn, checkoutID string) (UCILocalCheckoutRecord, bool, error) {
	record := UCILocalCheckoutRecord{
		DirtyPaths:   make(map[string]int64),
		RescanCauses: make([]UCILocalRescanCause, 0),
	}
	var rescanRequired int
	err := conn.QueryRowContext(ctx, `
		SELECT
			root_id,
			server_source_id,
			server_checkout_id,
			common_git_dir_fingerprint,
			private_git_dir_fingerprint,
			workstation_id,
			client_instance_id,
			incarnation_id,
			dirty_sequence,
			last_reconciled_sequence,
			rescan_required
		FROM uci_local_checkouts
		WHERE server_checkout_id = ?`, checkoutID,
	).Scan(
		&record.RootID,
		&record.SourceID,
		&record.CheckoutID,
		&record.CommonGitDirFingerprint,
		&record.PrivateGitDirFingerprint,
		&record.WorkstationID,
		&record.ClientInstanceID,
		&record.IncarnationID,
		&record.DirtySequence,
		&record.LastReconciledSequence,
		&rescanRequired,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return UCILocalCheckoutRecord{}, false, nil
	}
	if err != nil {
		return UCILocalCheckoutRecord{}, false, uciLocalRegistryOperationError("load checkout", err)
	}
	record.RescanRequired = rescanRequired != 0

	dirtyRows, err := conn.QueryContext(ctx, `
		SELECT relative_path, sequence
		FROM uci_local_dirty_paths
		WHERE server_checkout_id = ?
		ORDER BY relative_path`, checkoutID)
	if err != nil {
		return UCILocalCheckoutRecord{}, false, uciLocalRegistryOperationError("load dirty paths", err)
	}
	defer dirtyRows.Close()
	for dirtyRows.Next() {
		var relativePath string
		var sequence int64
		if err := dirtyRows.Scan(&relativePath, &sequence); err != nil {
			return UCILocalCheckoutRecord{}, false, uciLocalRegistryOperationError("scan dirty path", err)
		}
		record.DirtyPaths[relativePath] = sequence
	}
	if err := dirtyRows.Err(); err != nil {
		return UCILocalCheckoutRecord{}, false, uciLocalRegistryOperationError("iterate dirty paths", err)
	}

	causeRows, err := conn.QueryContext(ctx, `
		SELECT cause
		FROM uci_local_rescan_causes
		WHERE server_checkout_id = ?
		ORDER BY CASE cause
			WHEN 'sequence_gap' THEN 1
			WHEN 'stale_sequence' THEN 2
			WHEN 'offline_recovery_bound' THEN 3
			ELSE 4
		END`, checkoutID)
	if err != nil {
		return UCILocalCheckoutRecord{}, false, uciLocalRegistryOperationError("load rescan causes", err)
	}
	defer causeRows.Close()
	for causeRows.Next() {
		var rawCause string
		if err := causeRows.Scan(&rawCause); err != nil {
			return UCILocalCheckoutRecord{}, false, uciLocalRegistryOperationError("scan rescan cause", err)
		}
		cause := UCILocalRescanCause(rawCause)
		if !validUCILocalRescanCause(cause) {
			return UCILocalCheckoutRecord{}, false, errors.New("uci local registry: unrecognized rescan cause")
		}
		record.RescanCauses = append(record.RescanCauses, cause)
	}
	if err := causeRows.Err(); err != nil {
		return UCILocalCheckoutRecord{}, false, uciLocalRegistryOperationError("iterate rescan causes", err)
	}

	err = conn.QueryRowContext(ctx, `
		SELECT server_checkout_id, from_sequence, through_sequence, max_dirty_paths, observed_dirty_paths
		FROM uci_local_offline_recovery
		WHERE server_checkout_id = ?`, checkoutID,
	).Scan(
		&record.OfflineRecovery.CheckoutID,
		&record.OfflineRecovery.FromSequence,
		&record.OfflineRecovery.ThroughSequence,
		&record.OfflineRecovery.MaxDirtyPaths,
		&record.OfflineRecovery.ObservedDirtyPaths,
	)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return UCILocalCheckoutRecord{}, false, uciLocalRegistryOperationError("load offline recovery", err)
	}

	return record, true, nil
}

func insertUCILocalCheckout(ctx context.Context, conn *sql.Conn, registration UCILocalCheckoutRegistration, rescanRequired bool) error {
	rescan := 0
	if rescanRequired {
		rescan = 1
	}
	_, err := conn.ExecContext(ctx, `
		INSERT INTO uci_local_checkouts (
			server_checkout_id,
			root_id,
			server_source_id,
			common_git_dir_fingerprint,
			private_git_dir_fingerprint,
			workstation_id,
			client_instance_id,
			incarnation_id,
			dirty_sequence,
			last_reconciled_sequence,
			rescan_required
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?)`,
		registration.CheckoutID,
		registration.RootID,
		registration.SourceID,
		registration.CommonGitDirFingerprint,
		registration.PrivateGitDirFingerprint,
		registration.WorkstationID,
		registration.ClientInstanceID,
		registration.IncarnationID,
		rescan,
	)
	return uciLocalRegistryOperationError("insert checkout", err)
}

func replaceUCILocalCheckout(ctx context.Context, conn *sql.Conn, registration UCILocalCheckoutRegistration) error {
	_, err := conn.ExecContext(ctx, `
		UPDATE uci_local_checkouts
		SET
			root_id = ?,
			server_source_id = ?,
			common_git_dir_fingerprint = ?,
			private_git_dir_fingerprint = ?,
			workstation_id = ?,
			client_instance_id = ?,
			incarnation_id = ?,
			dirty_sequence = 0,
			last_reconciled_sequence = 0,
			rescan_required = 1
		WHERE server_checkout_id = ?`,
		registration.RootID,
		registration.SourceID,
		registration.CommonGitDirFingerprint,
		registration.PrivateGitDirFingerprint,
		registration.WorkstationID,
		registration.ClientInstanceID,
		registration.IncarnationID,
		registration.CheckoutID,
	)
	return uciLocalRegistryOperationError("replace checkout incarnation", err)
}

func resetUCILocalCheckoutOperationalState(ctx context.Context, conn *sql.Conn, checkoutID string) error {
	for _, statement := range []string{
		`DELETE FROM uci_local_dirty_paths WHERE server_checkout_id = ?`,
		`DELETE FROM uci_local_rescan_causes WHERE server_checkout_id = ?`,
		`DELETE FROM uci_local_offline_recovery WHERE server_checkout_id = ?`,
	} {
		if _, err := conn.ExecContext(ctx, statement, checkoutID); err != nil {
			return uciLocalRegistryOperationError("reset checkout operational state", err)
		}
	}
	return nil
}

func setUCILocalRescanCause(ctx context.Context, conn *sql.Conn, checkoutID string, cause UCILocalRescanCause) error {
	if !validUCILocalRescanCause(cause) {
		return errors.New("uci local registry: invalid rescan cause")
	}
	if _, err := conn.ExecContext(ctx, `
		UPDATE uci_local_checkouts
		SET rescan_required = 1
		WHERE server_checkout_id = ?`, checkoutID); err != nil {
		return uciLocalRegistryOperationError("mark rescan required", err)
	}
	_, err := conn.ExecContext(ctx, `
		INSERT INTO uci_local_rescan_causes (server_checkout_id, cause)
		VALUES (?, ?)
		ON CONFLICT(server_checkout_id, cause) DO NOTHING`, checkoutID, cause)
	return uciLocalRegistryOperationError("record rescan cause", err)
}

func clearUCILocalRescanState(ctx context.Context, conn *sql.Conn, checkoutID string) error {
	if _, err := conn.ExecContext(ctx, `
		DELETE FROM uci_local_rescan_causes WHERE server_checkout_id = ?`, checkoutID); err != nil {
		return uciLocalRegistryOperationError("clear rescan causes", err)
	}
	_, err := conn.ExecContext(ctx, `
		UPDATE uci_local_checkouts
		SET rescan_required = 0
		WHERE server_checkout_id = ?`, checkoutID)
	return uciLocalRegistryOperationError("clear rescan requirement", err)
}

func sameUCILocalRegistration(record UCILocalCheckoutRecord, registration UCILocalCheckoutRegistration) bool {
	return record.RootID == registration.RootID &&
		record.SourceID == registration.SourceID &&
		record.CheckoutID == registration.CheckoutID &&
		record.IncarnationID == registration.IncarnationID &&
		record.CommonGitDirFingerprint == registration.CommonGitDirFingerprint &&
		record.PrivateGitDirFingerprint == registration.PrivateGitDirFingerprint &&
		record.WorkstationID == registration.WorkstationID &&
		record.ClientInstanceID == registration.ClientInstanceID
}

func sameUCILocalOfflineRecovery(left, right UCILocalOfflineRecovery) bool {
	return left.CheckoutID == right.CheckoutID &&
		left.FromSequence == right.FromSequence &&
		left.ThroughSequence == right.ThroughSequence &&
		left.MaxDirtyPaths == right.MaxDirtyPaths &&
		left.ObservedDirtyPaths == right.ObservedDirtyPaths
}

func validUCILocalRescanCause(cause UCILocalRescanCause) bool {
	switch cause {
	case UCILocalRescanSequenceGap,
		UCILocalRescanStaleSequence,
		UCILocalRescanOfflineRecoveryBound,
		UCILocalRescanOverflow,
		UCILocalRescanRestart,
		UCILocalRescanMove,
		UCILocalRescanGitTransition,
		UCILocalRescanWatcherRegistrationFailure:
		return true
	default:
		return false
	}
}

func validateUCILocalApprovedRoot(root UCILocalApprovedRoot) error {
	for _, field := range []struct {
		name  string
		value string
		limit int
	}{
		{"root_id", root.RootID, uciLocalMaxOpaqueIDBytes},
		{"source_id", root.SourceID, uciLocalMaxOpaqueIDBytes},
		{"common_git_dir_fingerprint", root.CommonGitDirFingerprint, uciLocalMaxFingerprintBytes},
	} {
		if err := validateUCILocalValue(field.name, field.value, field.limit); err != nil {
			return err
		}
	}
	return validateUCILocalAbsoluteRootPath(root.RootPath)
}

func validateUCILocalCheckoutRegistration(registration UCILocalCheckoutRegistration) error {
	for _, field := range []struct {
		name  string
		value string
		limit int
	}{
		{"root_id", registration.RootID, uciLocalMaxOpaqueIDBytes},
		{"source_id", registration.SourceID, uciLocalMaxOpaqueIDBytes},
		{"checkout_id", registration.CheckoutID, uciLocalMaxOpaqueIDBytes},
		{"common_git_dir_fingerprint", registration.CommonGitDirFingerprint, uciLocalMaxFingerprintBytes},
		{"private_git_dir_fingerprint", registration.PrivateGitDirFingerprint, uciLocalMaxFingerprintBytes},
		{"workstation_id", registration.WorkstationID, uciLocalMaxOpaqueIDBytes},
		{"client_instance_id", registration.ClientInstanceID, uciLocalMaxOpaqueIDBytes},
	} {
		if err := validateUCILocalValue(field.name, field.value, field.limit); err != nil {
			return err
		}
	}
	return validateUCILocalIncarnationID(registration.IncarnationID)
}

func validateUCILocalAbsoluteRootPath(rootPath string) error {
	if err := validateUCILocalValue("root_path", rootPath, uciLocalMaxRootPathBytes); err != nil {
		return err
	}
	if !filepath.IsAbs(rootPath) {
		return errors.New("uci local registry: root_path must be absolute")
	}
	return nil
}

func validateUCILocalIncarnationID(value string) error {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value {
		return errors.New("uci local registry: incarnation_id must be a canonical non-nil UUID")
	}
	return nil
}

func validateUCILocalDirtyChange(change UCILocalDirtyChange) (string, error) {
	if err := validateUCILocalValue("checkout_id", change.CheckoutID, uciLocalMaxOpaqueIDBytes); err != nil {
		return "", err
	}
	if change.Sequence <= 0 {
		return "", errors.New("uci local registry: dirty sequence must be positive")
	}
	return normalizeUCILocalRelativePath(change.RelativePath)
}

func validateUCILocalOfflineRecovery(recovery UCILocalOfflineRecovery) error {
	if err := validateUCILocalValue("checkout_id", recovery.CheckoutID, uciLocalMaxOpaqueIDBytes); err != nil {
		return err
	}
	if recovery.FromSequence < 0 || recovery.ThroughSequence < recovery.FromSequence {
		return errors.New("uci local registry: invalid offline recovery sequence range")
	}
	if recovery.MaxDirtyPaths <= 0 || recovery.ObservedDirtyPaths < 0 {
		return errors.New("uci local registry: invalid offline recovery dirty-path counts")
	}
	return nil
}

func boundUCILocalOfflineRecovery(recovery UCILocalOfflineRecovery) (UCILocalOfflineRecovery, bool) {
	bounded := recovery
	overflow := recovery.ObservedDirtyPaths > recovery.MaxDirtyPaths || recovery.MaxDirtyPaths > uciLocalMaxRecoveryDirtyPaths
	if bounded.MaxDirtyPaths > uciLocalMaxRecoveryDirtyPaths {
		bounded.MaxDirtyPaths = uciLocalMaxRecoveryDirtyPaths
	}
	if bounded.ObservedDirtyPaths > uciLocalMaxRecoveryDirtyPaths {
		bounded.ObservedDirtyPaths = uciLocalMaxRecoveryDirtyPaths + 1
		overflow = true
	}
	return bounded, overflow
}

func validateUCILocalValue(name, value string, limit int) error {
	if value == "" {
		return fmt.Errorf("uci local registry: %s is required", name)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("uci local registry: %s must be valid UTF-8", name)
	}
	if len(value) > limit {
		return fmt.Errorf("uci local registry: %s exceeds its bound", name)
	}
	if strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("uci local registry: %s contains a NUL byte", name)
	}
	return nil
}

func normalizeUCILocalRelativePath(raw string) (string, error) {
	if err := validateUCILocalValue("relative_path", raw, uciLocalMaxRelativePathBytes); err != nil {
		return "", err
	}
	if filepath.IsAbs(raw) || filepath.VolumeName(raw) != "" || (len(raw) >= 2 && raw[1] == ':') {
		return "", errors.New("uci local registry: relative path must not be absolute")
	}
	normalized := path.Clean(strings.ReplaceAll(raw, "\\", "/"))
	if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, "../") || path.IsAbs(normalized) {
		return "", errors.New("uci local registry: relative path escapes its root")
	}
	if len(normalized) > uciLocalMaxRelativePathBytes {
		return "", errors.New("uci local registry: relative path exceeds its bound")
	}
	return normalized, nil
}

func uciLocalRegistryOperationError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	detail := err.Error()
	const maxDetailBytes = 256
	if len(detail) > maxDetailBytes {
		detail = detail[:maxDetailBytes]
	}
	return fmt.Errorf("uci local registry %s: %s", operation, detail)
}
