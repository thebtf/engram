// Package main is the engram daemon entry point for v4.3.0. It wires the
// modular framework (internal/module + internal/module/registry +
// internal/module/lifecycle + internal/module/dispatcher) to the muxcore
// engine and runs until the process receives SIGINT / SIGTERM.
//
// v4.2.0 was a monolithic engramHandler implementing muxcore.SessionHandler
// and muxcore.ProjectLifecycle inline. In v4.3.0 all that logic lives in
// internal/handlers/engramcore wrapped as an EngramModule +
// ProxyToolProvider + ProjectLifecycle tenant, registered here via
// wiring.go.
//
// Design reference: design.md §4.1 (startup/shutdown sequence), plan.md
// Phase 5 (US2 engramcore first tenant), tasks T040/T041.
package main

import (
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/control"
	"github.com/thebtf/engram/internal/handlers/serverevents"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/module/dispatcher"
	"github.com/thebtf/engram/internal/module/lifecycle"
	"github.com/thebtf/engram/internal/module/registry"
	"github.com/thebtf/engram/internal/version"
	muxcontrol "github.com/thebtf/mcp-mux/muxcore/control"
	"github.com/thebtf/mcp-mux/muxcore/engine"
	"github.com/thebtf/mcp-mux/muxcore/ipc"
	muxregistry "github.com/thebtf/mcp-mux/muxcore/registry"
	"github.com/thebtf/mcp-mux/muxcore/serverid"
	"github.com/thebtf/mcp-mux/muxcore/upgrade"
	"golang.org/x/mod/semver"
)

// daemonVersion is the string reported to gRPC Initialize and used in
// structured logs. Tracks Constitution §15 unified engram + plugin version.
var daemonVersion = version.Daemon

const (
	muxcoreDaemonFlag            = "--muxcore-daemon"
	muxcoreEmbeddedVersion       = "v0.29.1"
	muxcoreNamespace             = "engram"
	muxcoreDaemonCompatEpoch     = 1
	legacyDaemonVersion          = "v6.46.4"
	legacyEngramCommandPath      = "github.com/thebtf/engram/cmd/engram"
	legacyEngramModulePath       = "github.com/thebtf/engram"
	legacyEngramRevision         = "b7b43196b9689988ad6591471b56003c0d63797c"
	legacyMuxcoreModulePath      = "github.com/thebtf/mcp-mux/muxcore"
	legacyMuxcoreEmbeddedVersion = "v0.28.0"
)

type muxcoreDaemonVersionMarker struct {
	SchemaVersion     int    `json:"schema_version"`
	ProductVersion    string `json:"product_version"`
	DaemonCompatEpoch int    `json:"daemon_compat_epoch"`
	PID               int    `json:"pid"`
	DaemonGeneration  string `json:"daemon_generation"`
	Exe               string `json:"exe"`
}

type legacyMuxcoreDaemonVersionMarker struct {
	Version string `json:"version"`
	PID     int    `json:"pid"`
	Exe     string `json:"exe"`
}

type muxcoreDaemonStatusIdentity struct {
	PID              int    `json:"pid"`
	DaemonGeneration string `json:"daemon_generation"`
	ShuttingDown     bool   `json:"shutting_down"`
}

type daemonConvergenceIdentity struct {
	ProductVersion    string
	DaemonCompatEpoch int
}

type daemonConvergenceAction uint8

const (
	daemonConvergenceJoin daemonConvergenceAction = iota
	daemonConvergenceReplace
	daemonConvergenceFail
)

// startupGate enforces FR-4 / Plan ADR-005. When the daemon process starts
// with a configured server URL but no ENGRAM_TOKEN, exit non-zero with a
// single user-actionable diagnostic. When no server URL is configured the
// gate is silent — local-only flows (loom_*) continue to work without a
// token. Returns true on pass; false (after writing stderr + os.Exit) is
// unreachable from the caller's perspective.
func startupGate() {
	serverURL := os.Getenv(config.EnvServerURL)
	if serverURL == "" {
		serverURL = os.Getenv(config.EnvServerURLAlt)
	}
	if serverURL == "" {
		// No back-end configured — local-only flows are allowed without token.
		return
	}
	if os.Getenv(config.EnvWorkstationToken) != "" {
		return
	}
	fmt.Fprintf(os.Stderr,
		"[engram] FATAL: %s is empty. Generate a keycard at %s/tokens and run /engram:setup.\n",
		config.EnvWorkstationToken, serverURL,
	)
	os.Exit(1)
}

func isMuxcoreDaemonMode() bool {
	for _, arg := range os.Args[1:] {
		if arg == muxcoreDaemonFlag {
			return true
		}
	}
	return false
}

func isMuxcoreProxyMode() bool {
	return os.Getenv("MCP_MUX_SESSION_ID") != ""
}

func muxcoreDaemonMarkerPath() string {
	return serverid.DaemonControlPath("", muxcoreNamespace) + ".marker.json"
}

func muxcoreLegacyDaemonVersionPathForMarker(markerPath string) string {
	return strings.TrimSuffix(markerPath, ".marker.json") + ".version"
}

func muxcoreLegacyDaemonVersionPath() string {
	return muxcoreLegacyDaemonVersionPathForMarker(muxcoreDaemonMarkerPath())
}

func normalizedExecutablePath(path string) (string, bool) {
	path = filepath.Clean(strings.TrimSpace(path))
	return path, filepath.IsAbs(path)
}

func validDaemonIdentity(identity daemonConvergenceIdentity) bool {
	return identity.DaemonCompatEpoch >= 0 && semver.IsValid(identity.ProductVersion) && semver.Canonical(identity.ProductVersion) == identity.ProductVersion
}

// classifyDaemonConvergence is pure: a compatible daemon is never replaced by
// an equal or newer same-epoch caller, irrespective of its object-store path.
func classifyDaemonConvergence(client, live daemonConvergenceIdentity) (daemonConvergenceAction, error) {
	if !validDaemonIdentity(client) || !validDaemonIdentity(live) {
		return daemonConvergenceFail, errors.New("malformed daemon compatibility identity")
	}
	switch {
	case live.DaemonCompatEpoch < client.DaemonCompatEpoch:
		return daemonConvergenceReplace, nil
	case live.DaemonCompatEpoch > client.DaemonCompatEpoch:
		return daemonConvergenceFail, fmt.Errorf("incompatible newer daemon epoch %d (client epoch %d)", live.DaemonCompatEpoch, client.DaemonCompatEpoch)
	case semver.Compare(live.ProductVersion, client.ProductVersion) < 0:
		return daemonConvergenceReplace, nil
	default:
		return daemonConvergenceJoin, nil
	}
}

func decodeStrictJSON(raw []byte, target any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func readMuxcoreDaemonVersionMarker(path string) (muxcoreDaemonVersionMarker, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return muxcoreDaemonVersionMarker{}, err
	}
	var marker muxcoreDaemonVersionMarker
	if err := decodeStrictJSON(raw, &marker); err != nil {
		return muxcoreDaemonVersionMarker{}, err
	}
	if marker.SchemaVersion != 2 || marker.PID <= 0 || strings.TrimSpace(marker.DaemonGeneration) == "" {
		return muxcoreDaemonVersionMarker{}, errors.New("invalid daemon marker schema or identity")
	}
	if _, ok := normalizedExecutablePath(marker.Exe); !ok || !validDaemonIdentity(daemonConvergenceIdentity{ProductVersion: marker.ProductVersion, DaemonCompatEpoch: marker.DaemonCompatEpoch}) {
		return muxcoreDaemonVersionMarker{}, errors.New("invalid daemon marker compatibility identity")
	}
	return marker, nil
}

func decodeLegacyMuxcoreDaemonVersionMarker(raw []byte) (legacyMuxcoreDaemonVersionMarker, error) {
	var marker legacyMuxcoreDaemonVersionMarker
	if err := decodeStrictJSON(raw, &marker); err != nil {
		return legacyMuxcoreDaemonVersionMarker{}, err
	}
	if marker.Version != legacyDaemonVersion || marker.PID <= 0 {
		return legacyMuxcoreDaemonVersionMarker{}, errors.New("unsupported legacy daemon marker")
	}
	if _, ok := normalizedExecutablePath(marker.Exe); !ok {
		return legacyMuxcoreDaemonVersionMarker{}, errors.New("invalid legacy daemon executable")
	}
	return marker, nil
}

func readLegacyMuxcoreDaemonVersionMarker(path string) (legacyMuxcoreDaemonVersionMarker, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return legacyMuxcoreDaemonVersionMarker{}, err
	}
	return decodeLegacyMuxcoreDaemonVersionMarker(raw)
}

func muxcoreDaemonStatus(ctlPath string) (muxcoreDaemonStatusIdentity, bool) {
	resp, err := muxcontrol.SendWithTimeout(ctlPath, muxcontrol.Request{Cmd: "status"}, 2*time.Second)
	if err != nil || resp == nil || !resp.OK || len(resp.Data) == 0 {
		return muxcoreDaemonStatusIdentity{}, false
	}
	var status muxcoreDaemonStatusIdentity
	if err := json.Unmarshal(resp.Data, &status); err != nil || status.PID <= 0 || strings.TrimSpace(status.DaemonGeneration) == "" {
		return muxcoreDaemonStatusIdentity{}, false
	}
	return status, true
}

func clientDaemonIdentity() (daemonConvergenceIdentity, error) {
	identity := daemonConvergenceIdentity{ProductVersion: daemonVersion, DaemonCompatEpoch: muxcoreDaemonCompatEpoch}
	if identity.DaemonCompatEpoch <= 0 || !validDaemonIdentity(identity) {
		return daemonConvergenceIdentity{}, fmt.Errorf("invalid client daemon compatibility identity %q", daemonVersion)
	}
	return identity, nil
}

// muxcoreDaemonConvergenceAction accepts a schema-2 marker only after a fresh
// muxcore status response correlates both PID and generation.
func muxcoreDaemonConvergenceAction(path string, status muxcoreDaemonStatusIdentity, client daemonConvergenceIdentity) (daemonConvergenceAction, error) {
	if status.PID <= 0 || strings.TrimSpace(status.DaemonGeneration) == "" || status.ShuttingDown {
		return daemonConvergenceFail, errors.New("missing fresh live muxcore daemon status identity")
	}
	marker, err := readMuxcoreDaemonVersionMarker(path)
	if err != nil {
		return daemonConvergenceFail, err
	}
	if marker.PID != status.PID || marker.DaemonGeneration != status.DaemonGeneration {
		return daemonConvergenceFail, errors.New("daemon marker does not correlate to fresh control status")
	}
	return classifyDaemonConvergence(client, daemonConvergenceIdentity{ProductVersion: marker.ProductVersion, DaemonCompatEpoch: marker.DaemonCompatEpoch})
}

// processImageIdentity owns the single opened file that proves both the
// marker's filesystem identity and the Go build metadata of the live image.
// Darwin fills its kernel process binding fields before returning it.
type processImageIdentity struct {
	File           *os.File
	darwinUUID     [16]byte
	darwinUniqueID uint64
}

func (image *processImageIdentity) Close() error {
	if image == nil || image.File == nil {
		return nil
	}
	return image.File.Close()
}

func sameDarwinProcessImageBinding(beforeUUID [16]byte, beforeUniqueID uint64, afterUUID [16]byte, afterUniqueID uint64) bool {
	return beforeUniqueID != 0 &&
		afterUniqueID != 0 &&
		beforeUUID != [16]byte{} &&
		afterUUID != [16]byte{} &&
		beforeUniqueID == afterUniqueID &&
		beforeUUID == afterUUID
}

func sameLegacyProcessExecutable(markerPath string, image *processImageIdentity) error {
	markerPath, ok := normalizedExecutablePath(markerPath)
	if !ok {
		return errors.New("invalid legacy daemon executable")
	}
	if image == nil || image.File == nil {
		return errors.New("invalid live daemon process image")
	}
	markerInfo, err := os.Stat(markerPath)
	if err != nil {
		return fmt.Errorf("stat legacy daemon executable: %w", err)
	}
	imageInfo, err := image.File.Stat()
	if err != nil {
		return fmt.Errorf("stat opened live daemon executable: %w", err)
	}
	if !os.SameFile(markerInfo, imageInfo) {
		return errors.New("legacy daemon executable does not match live process image")
	}
	return nil
}

func effectiveBuildModuleVersion(info *buildinfo.BuildInfo, modulePath string) (string, bool) {
	if info == nil {
		return "", false
	}
	version := ""
	for _, dependency := range info.Deps {
		if dependency == nil || dependency.Path == "" || dependency.Version == "" {
			return "", false
		}
		module := dependency
		seen := map[*debug.Module]struct{}{}
		for {
			if module == nil || module.Path == "" || module.Version == "" {
				return "", false
			}
			if _, duplicate := seen[module]; duplicate {
				return "", false
			}
			seen[module] = struct{}{}
			if module.Replace == nil {
				break
			}
			module = module.Replace
		}
		if module.Path != modulePath {
			continue
		}
		if version != "" || module.Version == "" {
			return "", false
		}
		version = module.Version
	}
	if version == "" {
		return "", false
	}
	return version, true
}

func legacyMuxcoreBuildInfoMatches(info *buildinfo.BuildInfo) bool {
	if info == nil ||
		info.Path != legacyEngramCommandPath ||
		info.Main.Path != legacyEngramModulePath ||
		info.Main.Replace != nil {
		return false
	}
	settings := make(map[string]string, len(info.Settings))
	for _, setting := range info.Settings {
		if setting.Key == "" || setting.Value == "" {
			return false
		}
		if _, duplicate := settings[setting.Key]; duplicate {
			return false
		}
		settings[setting.Key] = setting.Value
	}
	if settings["vcs.revision"] != legacyEngramRevision || settings["vcs.modified"] != "false" {
		return false
	}
	dependencies := make(map[string]struct{}, len(info.Deps))
	for _, dependency := range info.Deps {
		if dependency == nil || dependency.Path == "" || dependency.Version == "" {
			return false
		}
		if _, duplicate := dependencies[dependency.Path]; duplicate {
			return false
		}
		dependencies[dependency.Path] = struct{}{}
	}
	version, ok := effectiveBuildModuleVersion(info, legacyMuxcoreModulePath)
	return ok && version == legacyMuxcoreEmbeddedVersion
}

func listMuxcoreRegistryDescriptorsAt(controlPath string) ([]muxregistry.Record, error) {
	return muxregistry.ListDescriptors(filepath.Dir(controlPath))
}

func legacyMuxcoreRegistryDescriptorMatches(records []muxregistry.Record, controlPath string, status muxcoreDaemonStatusIdentity) error {
	expectedPath, err := muxregistry.DescriptorPath(filepath.Dir(controlPath), muxregistry.Descriptor{
		SchemaVersion:     muxregistry.SchemaVersion,
		EngineName:        muxcoreNamespace,
		DaemonControlPath: controlPath,
	})
	if err != nil {
		return fmt.Errorf("construct expected muxcore registry descriptor path: %w", err)
	}
	var descriptor *muxregistry.Descriptor
	for _, record := range records {
		if record.Err != nil {
			return fmt.Errorf("read muxcore registry descriptor %q: %w", record.Path, record.Err)
		}
		if record.Descriptor.EngineName != muxcoreNamespace {
			continue
		}
		if descriptor != nil || record.Path != expectedPath {
			return errors.New("muxcore registry descriptor is ambiguous")
		}
		descriptorCopy := record.Descriptor
		descriptor = &descriptorCopy
	}
	if descriptor == nil {
		return errors.New("expected muxcore registry descriptor is missing")
	}
	if descriptor.ProductName != muxcoreNamespace ||
		descriptor.DaemonControlPath != controlPath ||
		descriptor.PID != status.PID ||
		descriptor.MuxcoreVersion != legacyMuxcoreEmbeddedVersion {
		return errors.New("muxcore registry descriptor does not match the live legacy daemon")
	}
	capabilities := descriptor.Capabilities
	if !capabilities.ListOwners || capabilities.Stop || capabilities.Restart || capabilities.Update {
		return errors.New("muxcore registry descriptor has unsupported capabilities")
	}
	return nil
}

func readCorrelatedLegacyMuxcoreDaemonMarker(path string, status muxcoreDaemonStatusIdentity) (legacyMuxcoreDaemonVersionMarker, error) {
	legacy, err := readLegacyMuxcoreDaemonVersionMarker(path)
	if err != nil {
		return legacyMuxcoreDaemonVersionMarker{}, err
	}
	if legacy.PID != status.PID {
		return legacyMuxcoreDaemonVersionMarker{}, errors.New("legacy daemon marker PID does not match fresh control status")
	}
	image, err := readLiveProcessImage(status.PID)
	if err != nil {
		return legacyMuxcoreDaemonVersionMarker{}, fmt.Errorf("read live legacy daemon process image: %w", err)
	}
	if image == nil {
		return legacyMuxcoreDaemonVersionMarker{}, errors.New("read live legacy daemon process image: empty handle")
	}
	defer image.Close()
	if err := sameLegacyProcessExecutable(legacy.Exe, image); err != nil {
		return legacyMuxcoreDaemonVersionMarker{}, err
	}
	info, err := readProcessBuildInfo(image.File)
	if err != nil {
		return legacyMuxcoreDaemonVersionMarker{}, fmt.Errorf("read live legacy daemon Go build info: %w", err)
	}
	if err := verifyLiveProcessImageBindingForLegacyProof(status.PID, image); err != nil {
		return legacyMuxcoreDaemonVersionMarker{}, fmt.Errorf("verify live legacy daemon process image: %w", err)
	}
	if !legacyMuxcoreBuildInfoMatches(info) {
		return legacyMuxcoreDaemonVersionMarker{}, errors.New("live legacy daemon does not match the official v6.46.4 build identity")
	}
	controlPath := serverid.DaemonControlPath("", muxcoreNamespace)
	records, err := listMuxcoreRegistryDescriptors(controlPath)
	if err != nil {
		return legacyMuxcoreDaemonVersionMarker{}, fmt.Errorf("list muxcore registry descriptors: %w", err)
	}
	if err := legacyMuxcoreRegistryDescriptorMatches(records, controlPath, status); err != nil {
		return legacyMuxcoreDaemonVersionMarker{}, err
	}
	return legacy, nil
}

func muxcoreLegacyDaemonConvergenceAction(path string, status muxcoreDaemonStatusIdentity, client daemonConvergenceIdentity) (daemonConvergenceAction, error) {
	if status.PID <= 0 || strings.TrimSpace(status.DaemonGeneration) == "" || status.ShuttingDown {
		return daemonConvergenceFail, errors.New("missing fresh live muxcore daemon status identity")
	}
	_, err := readCorrelatedLegacyMuxcoreDaemonMarker(path, status)
	if err != nil {
		return daemonConvergenceFail, fmt.Errorf("correlate legacy daemon marker: %w", err)
	}
	if client.DaemonCompatEpoch == 1 && validDaemonIdentity(client) {
		return daemonConvergenceReplace, nil
	}
	return daemonConvergenceFail, errors.New("legacy daemon compatibility identity is unsupported")
}

func readLiveMuxcoreDaemonActionFromDisk(status muxcoreDaemonStatusIdentity, client daemonConvergenceIdentity) (daemonConvergenceAction, error) {
	return readLiveMuxcoreDaemonActionAt(muxcoreDaemonMarkerPath(), muxcoreLegacyDaemonVersionPath(), status, client)
}

func readLiveMuxcoreDaemonActionAt(markerPath, legacyPath string, status muxcoreDaemonStatusIdentity, client daemonConvergenceIdentity) (daemonConvergenceAction, error) {
	if status.PID <= 0 || strings.TrimSpace(status.DaemonGeneration) == "" || status.ShuttingDown {
		return daemonConvergenceFail, errMuxcoreDaemonMarkerUncorrelated
	}
	marker, markerErr := readMuxcoreDaemonVersionMarker(markerPath)
	if markerErr == nil {
		if marker.PID == status.PID {
			if marker.DaemonGeneration == status.DaemonGeneration {
				return classifyDaemonConvergence(client, daemonConvergenceIdentity{ProductVersion: marker.ProductVersion, DaemonCompatEpoch: marker.DaemonCompatEpoch})
			}
			return daemonConvergenceFail, fmt.Errorf("%w: PID or generation differs from fresh control status", errMuxcoreDaemonMarkerUncorrelated)
		}
		// A v6.46.4 client can leave its correlated legacy marker behind after
		// replacing this schema-2 generation. Only use it when schema-2 proves
		// a different, newer PID than that legacy identity.
		legacy, legacyErr := readCorrelatedLegacyMuxcoreDaemonMarker(legacyPath, status)
		if legacyErr == nil &&
			marker.DaemonCompatEpoch == client.DaemonCompatEpoch &&
			client.DaemonCompatEpoch == 1 && validDaemonIdentity(client) &&
			semver.Compare(marker.ProductVersion, legacy.Version) > 0 {
			return classifyDaemonConvergence(client, daemonConvergenceIdentity{ProductVersion: legacy.Version, DaemonCompatEpoch: 1})
		}
		return daemonConvergenceFail, fmt.Errorf("%w: PID or generation differs from fresh control status", errMuxcoreDaemonMarkerUncorrelated)
	}
	if !errors.Is(markerErr, os.ErrNotExist) {
		return daemonConvergenceFail, markerErr
	}
	legacyAction, legacyErr := muxcoreLegacyDaemonConvergenceAction(legacyPath, status, client)
	if legacyErr == nil {
		return legacyAction, nil
	}
	return daemonConvergenceFail, markerErr
}

var (
	readMuxcoreDaemonStatusIdentity                                       = muxcoreDaemonStatus
	readLiveMuxcoreDaemonAction                                           = readLiveMuxcoreDaemonActionFromDisk
	currentExecutable                                                     = os.Executable
	readLiveProcessImage                                                  = liveProcessImageIdentity
	readProcessBuildInfo                                                  = buildinfo.Read
	listMuxcoreRegistryDescriptors                                        = listMuxcoreRegistryDescriptorsAt
	verifyLiveProcessImageBindingForLegacyProof                           = verifyLiveProcessImageBinding
	dialMuxcoreControl                                                    = ipc.DialTimeout
	readMuxcoreControlPeerPID                                             = muxcoreControlPeerPID
	sendMuxcoreControlRequest                                             = sendMuxcoreControlRequestAndReadResponse
	restartMuxcoreDaemon                                                  = restartMuxcoreDaemonBound
	waitForCurrentMuxcoreDaemonReady                                      = waitForMuxcoreDaemonVersion
	acquireRestartLock                          func() (io.Closer, error) = func() (io.Closer, error) { return acquireMuxcoreDaemonLock() }
	muxcoreMarkerPublicationTimeout                                       = muxcoreDaemonMarkerPublicationTimeout
)

var (
	errMuxcoreDaemonStillLower         = errors.New("muxcore daemon remains lower than client")
	errMuxcoreDaemonMarkerUncorrelated = errors.New("muxcore daemon marker is not correlated")
	errMuxcoreDaemonLockAvailable      = errors.New("muxcore daemon restart lock became available")
	writeMuxcoreMarker                 = writeMuxcoreMarkerAtomically
	isRestartLockContended             = isMuxcoreDaemonLockContended
)

const (
	muxcoreDaemonMarkerPublicationTimeout = 5 * time.Second
	muxcoreDaemonLockRetryInterval        = 10 * time.Millisecond
)

// acquireRestartLockWithContext retries only the expected nonblocking-lock
// contention. All other acquisition failures and the caller's deadline remain
// visible to the elected daemon.
func acquireRestartLockWithContext(ctx context.Context) (io.Closer, error) {
	var retry *time.Timer
	defer func() {
		if retry != nil {
			retry.Stop()
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lock, err := acquireRestartLock()
		if err == nil {
			return lock, nil
		}
		if !isRestartLockContended(err) {
			return nil, err
		}
		if retry == nil {
			retry = time.NewTimer(muxcoreDaemonLockRetryInterval)
		} else {
			retry.Reset(muxcoreDaemonLockRetryInterval)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-retry.C:
		}
	}
}

func waitForMuxcoreDaemonVersion(ctx context.Context) error {
	const pollInterval = 100 * time.Millisecond
	client, err := clientDaemonIdentity()
	if err != nil {
		return err
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		status, ok := readMuxcoreDaemonStatusIdentity(serverid.DaemonControlPath("", muxcoreNamespace))
		if ok {
			action, actionErr := readLiveMuxcoreDaemonAction(status, client)
			switch {
			case actionErr == nil && action == daemonConvergenceJoin:
				return nil
			case actionErr == nil && action == daemonConvergenceReplace:
				return errMuxcoreDaemonStillLower
			case errors.Is(actionErr, os.ErrNotExist), errors.Is(actionErr, errMuxcoreDaemonMarkerUncorrelated):
			case action == daemonConvergenceFail && actionErr != nil:
				return actionErr
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func waitForMuxcoreDaemonMarker(ctx context.Context) error {
	return waitForCurrentMuxcoreDaemonReady(ctx)
}

func verifyCurrentMuxcoreDaemonIdentity(expected muxcoreDaemonStatusIdentity) error {
	client, err := clientDaemonIdentity()
	if err != nil {
		return err
	}
	status, ok := readMuxcoreDaemonStatusIdentity(serverid.DaemonControlPath("", muxcoreNamespace))
	if !ok {
		return errors.New("read muxcore daemon status after marker publication")
	}
	if status.PID != expected.PID || status.DaemonGeneration != expected.DaemonGeneration || status.ShuttingDown {
		return errors.New("muxcore daemon status no longer identifies this live, non-shutting-down daemon")
	}
	action, err := readLiveMuxcoreDaemonAction(status, client)
	if err != nil || action != daemonConvergenceJoin {
		if err == nil {
			err = errors.New("daemon identity requires replacement")
		}
		return err
	}
	return nil
}

func sameMuxcoreDaemon(left, right muxcoreDaemonStatusIdentity) bool {
	return left.PID == right.PID && left.DaemonGeneration == right.DaemonGeneration
}

func waitForMuxcoreDaemonChangeOrConvergence(ctx context.Context, previous muxcoreDaemonStatusIdentity, client daemonConvergenceIdentity) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, ok := readMuxcoreDaemonStatusIdentity(serverid.DaemonControlPath("", muxcoreNamespace))
		if !ok || !sameMuxcoreDaemon(status, previous) {
			return errMuxcoreDaemonStillLower
		}
		action, err := readLiveMuxcoreDaemonAction(status, client)
		if err == nil && action == daemonConvergenceJoin {
			return nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, errMuxcoreDaemonMarkerUncorrelated) {
			return err
		}
		lock, lockErr := acquireRestartLock()
		if lockErr == nil {
			if closeErr := lock.Close(); closeErr != nil {
				return fmt.Errorf("probe muxcore daemon restart lock: %w", closeErr)
			}
			return errMuxcoreDaemonLockAvailable
		}
		if !isRestartLockContended(lockErr) {
			return fmt.Errorf("probe muxcore daemon restart lock: %w", lockErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func daemonTerminationStatus(ctx context.Context, runErr <-chan error) (normal, observed bool) {
	if ctx.Err() != nil {
		return true, true
	}
	select {
	case err := <-runErr:
		return err == nil || isExpectedContextShutdown(ctx, err), true
	default:
		return false, false
	}
}

func isExpectedContextShutdown(ctx context.Context, err error) bool {
	return ctx.Err() != nil && errors.Is(err, context.Canceled)
}

func reconcileMuxcoreDaemonVersion(parent context.Context) error {
	if isMuxcoreDaemonMode() || isMuxcoreProxyMode() {
		return nil
	}
	client, err := clientDaemonIdentity()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()

	for {
		status, ok := readMuxcoreDaemonStatusIdentity(serverid.DaemonControlPath("", muxcoreNamespace))
		if !ok {
			return nil // muxcore elects the cold-start daemon after this shim begins.
		}
		action, err := readLiveMuxcoreDaemonAction(status, client)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, errMuxcoreDaemonMarkerUncorrelated) {
			if waitErr := waitForMuxcoreDaemonMarker(ctx); waitErr == nil {
				return nil
			} else if errors.Is(waitErr, errMuxcoreDaemonStillLower) {
				continue
			} else {
				return fmt.Errorf("authenticate starting muxcore daemon PID %d: %w", status.PID, waitErr)
			}
		}
		if err != nil || action == daemonConvergenceFail {
			if err == nil {
				err = errors.New("incompatible live daemon")
			}
			return fmt.Errorf("authenticate live muxcore daemon PID %d: %w", status.PID, err)
		}
		if action == daemonConvergenceJoin {
			return nil
		}
		lock, err := acquireRestartLock()
		if err != nil {
			if !isRestartLockContended(err) {
				return fmt.Errorf("acquire muxcore daemon restart lock: %w", err)
			}
			waitErr := waitForMuxcoreDaemonChangeOrConvergence(ctx, status, client)
			if waitErr == nil || errors.Is(waitErr, errMuxcoreDaemonStillLower) || errors.Is(waitErr, errMuxcoreDaemonLockAvailable) {
				if waitErr == nil {
					fmt.Fprintf(os.Stderr, "[engram] joined concurrent muxcore daemon replacement for %s\n", daemonVersion)
					return nil
				}
				continue
			}
			return fmt.Errorf("wait for muxcore daemon lock holder: %w", waitErr)
		}

		current, ok := readMuxcoreDaemonStatusIdentity(serverid.DaemonControlPath("", muxcoreNamespace))
		if !ok {
			_ = lock.Close()
			return nil
		}
		if !sameMuxcoreDaemon(status, current) {
			_ = lock.Close()
			continue
		}
		action, err = readLiveMuxcoreDaemonAction(current, client)
		if err != nil || action != daemonConvergenceReplace {
			closeErr := lock.Close()
			if err != nil || action == daemonConvergenceFail {
				if err == nil {
					err = errors.New("incompatible live daemon")
				}
				return fmt.Errorf("authenticate fenced muxcore daemon PID %d: %w", current.PID, err)
			}
			if closeErr != nil {
				return fmt.Errorf("release muxcore daemon restart lock: %w", closeErr)
			}
			return nil
		}

		successorExe, exeErr := currentExecutable()
		if exeErr != nil {
			_ = lock.Close()
			return fmt.Errorf("resolve muxcore successor executable: %w", exeErr)
		}
		successorExe, ok = normalizedExecutablePath(successorExe)
		if !ok {
			_ = lock.Close()
			return fmt.Errorf("resolve muxcore successor executable: path %q is not absolute", successorExe)
		}
		restartErr := restartMuxcoreDaemon(ctx, current, successorExe)
		closeErr := lock.Close()
		if restartErr != nil {
			if closeErr != nil {
				return fmt.Errorf("restart fenced lower muxcore daemon PID %d with %s: %w; additionally failed to release restart lock: %v", current.PID, daemonVersion, restartErr, closeErr)
			}
			return fmt.Errorf("restart fenced lower muxcore daemon PID %d with %s: %w", current.PID, daemonVersion, restartErr)
		}
		if closeErr != nil {
			fmt.Fprintf(os.Stderr, "[engram] WARN: released muxcore daemon restart lock with error: %v\n", closeErr)
		}
		waitErr := waitForMuxcoreDaemonMarker(ctx)
		if waitErr == nil {
			fmt.Fprintf(os.Stderr, "[engram] replaced lower muxcore daemon PID %d with %s\n", current.PID, daemonVersion)
			return nil
		}
		if errors.Is(waitErr, errMuxcoreDaemonStillLower) {
			continue
		}
		return fmt.Errorf("restart fenced lower muxcore daemon PID %d did not converge to %s: %w", current.PID, daemonVersion, waitErr)
	}
}

func writeMuxcoreDaemonVersionMarker(logger *slog.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), muxcoreMarkerPublicationTimeout)
	defer cancel()
	return writeMuxcoreDaemonVersionMarkerWithContext(ctx, logger)
}

func writeMuxcoreDaemonVersionMarkerWithContext(ctx context.Context, logger *slog.Logger) error {
	if !isMuxcoreDaemonMode() {
		return nil
	}
	if err := writeMuxcoreDaemonVersionMarkerAtWithContext(ctx, muxcoreDaemonMarkerPath()); err != nil {
		logger.Error("could not publish muxcore daemon version marker", "error", err)
		return err
	}
	return nil
}

func writeMuxcoreDaemonVersionMarkerAt(markerPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), muxcoreMarkerPublicationTimeout)
	defer cancel()
	return writeMuxcoreDaemonVersionMarkerAtWithContext(ctx, markerPath)
}

func writeMuxcoreDaemonVersionMarkerAtWithContext(ctx context.Context, markerPath string) (err error) {
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o700); err != nil {
		return fmt.Errorf("create muxcore marker directory: %w", err)
	}
	lock, err := acquireRestartLockWithContext(ctx)
	if err != nil {
		return fmt.Errorf("acquire muxcore daemon restart lock before publishing marker: %w", err)
	}
	defer func() {
		if closeErr := lock.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("release muxcore daemon restart lock after publishing marker: %w", closeErr))
		}
	}()

	// The status read is deliberately after taking the shared restart lock:
	// a daemon superseded while waiting for it publishes neither projection.
	status, ok := readMuxcoreDaemonStatusIdentity(serverid.DaemonControlPath("", muxcoreNamespace))
	if !ok || status.PID != os.Getpid() || strings.TrimSpace(status.DaemonGeneration) == "" || status.ShuttingDown {
		return errors.New("correlate this live, non-shutting-down muxcore daemon before publishing marker")
	}
	exePath, err := currentExecutable()
	if err != nil {
		return fmt.Errorf("resolve muxcore marker executable: %w", err)
	}
	exePath, ok = normalizedExecutablePath(exePath)
	if !ok {
		return fmt.Errorf("publish muxcore marker with relative executable %q", exePath)
	}
	client, err := clientDaemonIdentity()
	if err != nil {
		return err
	}
	legacyPath := muxcoreLegacyDaemonVersionPathForMarker(markerPath)
	priorLegacy, priorLegacyErr := os.ReadFile(legacyPath)
	legacyExisted := priorLegacyErr == nil
	if priorLegacyErr != nil && !errors.Is(priorLegacyErr, os.ErrNotExist) {
		return fmt.Errorf("read prior muxcore legacy marker: %w", priorLegacyErr)
	}
	legacyMarker := legacyMuxcoreDaemonVersionMarker{Version: client.ProductVersion, PID: status.PID, Exe: exePath}
	if prior, priorErr := readLegacyMuxcoreDaemonVersionMarker(legacyPath); priorErr == nil {
		// v6.46.4 compares its own version and executable exactly before it joins.
		// Keeping that trusted prior identity with this daemon's PID lets it join
		// the schema-2-authoritative current daemon instead of replacing it.
		legacyMarker.Version = prior.Version
		legacyMarker.Exe = prior.Exe
	}
	legacy, err := json.Marshal(legacyMarker)
	if err != nil {
		return fmt.Errorf("marshal muxcore legacy marker: %w", err)
	}
	v2, err := json.Marshal(muxcoreDaemonVersionMarker{SchemaVersion: 2, ProductVersion: client.ProductVersion, DaemonCompatEpoch: client.DaemonCompatEpoch, PID: status.PID, DaemonGeneration: status.DaemonGeneration, Exe: exePath})
	if err != nil {
		return fmt.Errorf("marshal muxcore schema-2 marker: %w", err)
	}
	legacy = append(legacy, '\n')
	v2 = append(v2, '\n')

	// v6.46.4 joins only after its retained identity names this PID. Publish
	// that projection first, then commit schema-2 authority. The restart lock
	// spans both atomic replacements, so a new client cannot act on the mixed
	// tuple; a legacy client sees only an already-rebound PID. If the second
	// write cannot commit, restore the exact previous legacy projection before
	// releasing the lock; without that compensation, the mixed tuple is unsafe.
	restoreLegacy := func(publishErr error) error {
		var restoreErr error
		if legacyExisted {
			restoreErr = writeMuxcoreMarker(legacyPath, priorLegacy)
		} else if err := os.Remove(legacyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			restoreErr = err
		}
		if restoreErr != nil {
			return errors.Join(publishErr, fmt.Errorf("restore prior muxcore legacy marker: %w", restoreErr))
		}
		return publishErr
	}
	current, ok := readMuxcoreDaemonStatusIdentity(serverid.DaemonControlPath("", muxcoreNamespace))
	if !ok || !sameMuxcoreDaemon(status, current) || current.ShuttingDown {
		return errors.New("muxcore daemon status changed before publishing marker")
	}
	if err := writeMuxcoreMarker(legacyPath, legacy); err != nil {
		return err
	}
	current, ok = readMuxcoreDaemonStatusIdentity(serverid.DaemonControlPath("", muxcoreNamespace))
	if !ok || !sameMuxcoreDaemon(status, current) || current.ShuttingDown {
		return restoreLegacy(errors.New("muxcore daemon status changed before publishing schema-2 marker"))
	}
	if err := writeMuxcoreMarker(markerPath, v2); err != nil {
		return restoreLegacy(err)
	}
	return nil
}

func writeMuxcoreMarkerAtomically(path string, payload []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".engram-daemon-marker-*.tmp")
	if err != nil {
		return fmt.Errorf("create muxcore marker: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set muxcore marker permissions: %w", err)
	}
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write muxcore marker: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync muxcore marker: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close muxcore marker: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("publish muxcore marker: %w", err)
	}
	return nil
}

func muxcoreBaseConfig() engine.Config {
	return engine.Config{
		Name:         "engram",
		Namespace:    muxcoreNamespace,
		DaemonFlag:   muxcoreDaemonFlag,
		SkipSnapshot: true,
		Registry: &muxregistry.Config{
			ProductName:    "engram",
			MuxcoreVersion: muxcoreEmbeddedVersion,
			Capabilities:   muxregistry.Capabilities{ListOwners: true},
		},
	}
}

func muxcoreDaemonConfig(disp *dispatcher.Dispatcher) engine.Config {
	cfg := muxcoreBaseConfig()
	cfg.Persistent = true // daemon owns durable module/background state
	cfg.SessionHandler = disp
	return cfg
}

func muxcoreShimConfig() engine.Config {
	cfg := muxcoreBaseConfig()
	cfg.Persistent = false // host shim owns no durable state
	// IdleSuspendDelay and IdleDormantGrace stay zero: muxcore's supervisor
	// exists, but Engram's plugin launcher does not wire it.
	cfg.Handler = func(context.Context, io.Reader, io.Writer) error {
		return fmt.Errorf("engram muxcore shim cannot serve MCP traffic directly")
	}
	return cfg
}

func sendMuxcoreControlRequestAndReadResponse(ctx context.Context, connection net.Conn, request muxcontrol.Request) (*muxcontrol.Response, error) {
	deadline := time.Now().Add(60 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("set muxcore control deadline: %w", err)
	}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return nil, fmt.Errorf("write muxcore control request: %w", err)
	}
	var response muxcontrol.Response
	if err := json.NewDecoder(connection).Decode(&response); err != nil {
		return nil, fmt.Errorf("read muxcore control response: %w", err)
	}
	return &response, nil
}

func restartMuxcoreDaemonBound(ctx context.Context, expected muxcoreDaemonStatusIdentity, successorExe string) error {
	if expected.PID <= 0 || strings.TrimSpace(expected.DaemonGeneration) == "" || expected.ShuttingDown {
		return errors.New("missing fenced live muxcore daemon status identity")
	}
	connection, err := dialMuxcoreControl(serverid.DaemonControlPath("", muxcoreNamespace), 5*time.Second)
	if err != nil {
		return fmt.Errorf("dial fenced muxcore control connection: %w", err)
	}
	defer connection.Close()
	peerPID, err := readMuxcoreControlPeerPID(connection)
	if err != nil {
		return fmt.Errorf("identify muxcore control peer: %w", err)
	}
	if peerPID != expected.PID {
		return fmt.Errorf("muxcore control peer PID %d does not match fenced status PID %d", peerPID, expected.PID)
	}
	current, ok := readMuxcoreDaemonStatusIdentity(serverid.DaemonControlPath("", muxcoreNamespace))
	if !ok || !sameMuxcoreDaemon(expected, current) || current.ShuttingDown {
		return errors.New("muxcore daemon status changed after control peer verification")
	}
	response, err := sendMuxcoreControlRequest(ctx, connection, muxcontrol.Request{
		Cmd:            "graceful-restart",
		DrainTimeoutMs: int((30 * time.Second).Milliseconds()),
		SuccessorExe:   successorExe,
	})
	if err != nil {
		return fmt.Errorf("send peer-bound muxcore graceful restart: %w", err)
	}
	if response == nil || !response.OK {
		if response == nil {
			return errors.New("peer-bound muxcore graceful restart returned no response")
		}
		return fmt.Errorf("peer-bound muxcore graceful restart rejected: %s", response.Message)
	}
	return nil
}

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--help" || os.Args[1] == "-h" || os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("engram %s — stdio MCP daemon for Claude Code\n", daemonVersion)
		fmt.Println()
		fmt.Println("This binary is invoked automatically by the engram plugin.")
		fmt.Println("It is not intended to be run directly.")
		fmt.Println()
		fmt.Println("Environment:")
		fmt.Printf("  %-28s  Server URL (e.g. http://host:37777)\n", config.EnvServerURL)
		fmt.Printf("  %-28s  Workstation keycard (issued via dashboard /tokens)\n", config.EnvWorkstationToken)
		os.Exit(0)
	}

	// FR-4 / ADR-005: fail-fast on missing workstation credential BEFORE
	// any heavy initialisation. Loud failure beats silent loom_*-only
	// graceful degradation that masked PR #203's regression for days.
	startupGate()

	daemonCtx, daemonCancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer daemonCancel()

	daemonMode := isMuxcoreDaemonMode()
	if !daemonMode {
		if err := reconcileMuxcoreDaemonVersion(daemonCtx); err != nil {
			if daemonCtx.Err() != nil {
				return
			}
			fmt.Fprintf(os.Stderr, "[engram] FATAL: muxcore daemon version reconciliation failed: %v\n", err)
			os.Exit(1)
		}
	}

	// Clean stale binaries from previous upgrades (.old.* files).
	if exePath, err := os.Executable(); err == nil {
		if cleaned := upgrade.CleanStale(exePath); cleaned > 0 {
			fmt.Fprintf(os.Stderr, "[engram] cleaned %d stale binary file(s)\n", cleaned)
		}
	}

	if !daemonMode {
		eng, err := engine.New(muxcoreShimConfig())
		if err != nil {
			fmt.Fprintf(os.Stderr, "[engram] FATAL: muxcore shim setup failed: %v\n", err)
			os.Exit(1)
		}
		if err := eng.Run(daemonCtx); err != nil && !isExpectedContextShutdown(daemonCtx, err) {
			fmt.Fprintf(os.Stderr, "[engram] FATAL: muxcore shim terminated: %v\n", err)
			os.Exit(1)
		}
		return
	}

	dd := dataDir()
	logger := newRootLogger()

	// --- Framework wiring ------------------------------------------------
	reg := registry.New()
	if err := registerModules(reg); err != nil {
		logger.Error("module registration failed", "error", err)
		os.Exit(1)
	}
	reg.Freeze()

	logger.Info("module registry frozen",
		"modules", reg.ListNames(),
		"version", daemonVersion,
	)

	disp := dispatcher.NewWithVersion(reg, logger, daemonVersion)
	pipeline := lifecycle.New(reg, logger)

	// Init context is distinct from daemon context — see design.md §3.2
	// and clarification C3 (Init ctx vs deps.DaemonCtx).
	initCtx, initCancel := context.WithCancel(context.Background())

	if err := pipeline.Start(initCtx, depsProviderFor(logger, daemonCtx)); err != nil {
		initCancel()
		logger.Error("lifecycle Start failed", "error", err)
		os.Exit(1)
	}
	initCancel()

	// --- muxcore engine boot ---------------------------------------------
	// The dispatcher satisfies BOTH muxcore.SessionHandler (HandleRequest)
	// and muxcore.ProjectLifecycle (OnProjectConnect/OnProjectDisconnect).
	// muxcore type-asserts on the SessionHandler to detect the optional
	// lifecycle methods — see muxcore.ProjectLifecycle docs.
	eng, err := engine.New(muxcoreDaemonConfig(disp))
	if err != nil {
		logger.Error("engine.New failed", "error", err)
		_ = pipeline.ShutdownAll(daemonCtx)
		os.Exit(1)
	}

	// Multiple clients may race to spawn the daemon. Only the process that
	// actually binds muxcore's control socket reaches Ready; losers must never
	// overwrite the live daemon's version marker or product control surface.
	runErr := make(chan error, 1)
	go func() {
		runErr <- eng.Run(daemonCtx)
	}()
	select {
	case <-eng.Ready():
		if eng.Mode() != engine.ModeDaemon || eng.Daemon() == nil {
			logger.Error("muxcore engine reported ready without a live daemon", "mode", eng.Mode())
			_ = pipeline.ShutdownAll(daemonCtx)
			os.Exit(1)
		}
	case err := <-runErr:
		_ = pipeline.ShutdownAll(daemonCtx)
		if isExpectedContextShutdown(daemonCtx, err) {
			return
		}
		logger.Error("engine.Run terminated before daemon readiness", "error", err)
		os.Exit(1)
	}
	expected, statusOK := readMuxcoreDaemonStatusIdentity(serverid.DaemonControlPath("", muxcoreNamespace))
	if !statusOK || expected.PID != os.Getpid() || expected.ShuttingDown {
		_ = pipeline.ShutdownAll(daemonCtx)
		logger.Error("muxcore daemon status is not this live elected daemon")
		os.Exit(1)
	}
	markerCtx, cancelMarkerPublication := context.WithTimeout(daemonCtx, muxcoreMarkerPublicationTimeout)
	err = writeMuxcoreDaemonVersionMarkerWithContext(markerCtx, logger)
	cancelMarkerPublication()
	if err != nil {
		_ = pipeline.ShutdownAll(daemonCtx)
		if normal, _ := daemonTerminationStatus(daemonCtx, runErr); normal {
			return
		}
		logger.Error("muxcore daemon marker publication failed", "error", err)
		os.Exit(1)
	}
	if err := verifyCurrentMuxcoreDaemonIdentity(expected); err != nil {
		_ = pipeline.ShutdownAll(daemonCtx)
		if normal, _ := daemonTerminationStatus(daemonCtx, runErr); normal {
			return
		}
		logger.Error("muxcore daemon identity changed after cold-start election", "error", err)
		os.Exit(1)
	}
	if normal, observed := daemonTerminationStatus(daemonCtx, runErr); observed {
		_ = pipeline.ShutdownAll(daemonCtx)
		if normal {
			return
		}
		logger.Error("muxcore engine terminated before product control publication")
		os.Exit(1)
	}

	// Start only after muxcore readiness so losing daemon-spawn candidates do
	// not publish a stale PID or contend for Engram's restart control surface.
	sockPath := control.SocketPath(dd)
	pidPath := control.PIDPath(dd)
	if err := os.MkdirAll(control.SocketDir(dd), 0o700); err != nil {
		logger.Warn("could not create run directory for control socket", "dir", control.SocketDir(dd), "error", err)
	}
	ctrlListener := control.NewListener(sockPath, pidPath,
		func(cmd string) string {
			switch cmd {
			case "graceful-restart":
				go handleGracefulRestart(logger, pipeline, disp, filepath.Join(dd, "modules"))
				return "ACK"
			default:
				return "ERR unknown command"
			}
		},
		logger,
	)
	if err := ctrlListener.Start(); err != nil {
		// Non-fatal: daemon continues without the legacy product control socket.
		logger.Warn("control socket start failed — graceful-restart unavailable",
			"error", err,
		)
	}
	defer ctrlListener.Close()

	// --- Serverevents bridge ---------------------------------------------
	// Start the bridge that consumes engram-server's ProjectEvents gRPC stream
	// and fans out OnProjectRemoved to all ProjectRemovalAware modules. The
	// bridge runs in the background for the daemon's lifetime.
	//
	// The bridge is started after the muxcore engine is ready and stopped before
	// pipeline.ShutdownAll so in-flight fan-outs complete before module teardown.
	//
	// If ENGRAM_SERVER_URL is not set the bridge logs a warning and is a no-op.
	// The dispatcher satisfies serverevents.ProjectTracker via its
	// ConnectedProjectIDs() method, populated by OnProjectConnect /
	// OnProjectDisconnect callbacks. This gives the heartbeat path real
	// visibility into active sessions (Phase 5 CRIT fix from PR #171 review).
	sevBridge := serverevents.NewBridge(logger, reg, disp, nil /* production: dial own conn */)
	sevBridge.Start(daemonCtx)

	logger.Info("engram daemon ready", "version", daemonVersion)

	if err := <-runErr; err != nil && !isExpectedContextShutdown(daemonCtx, err) {
		logger.Error("engine.Run terminated", "error", err)
		sevBridge.Stop()
		_ = pipeline.ShutdownAll(daemonCtx)
		os.Exit(1)
	}

	logger.Info("engram daemon shutting down")
	sevBridge.Stop()
	if err := pipeline.ShutdownAll(daemonCtx); err != nil {
		logger.Error("lifecycle Shutdown error", "error", err)
	}
}

// handleGracefulRestart executes the retained explicit graceful-restart sequence:
//  1. Log INFO
//  2. Drain — stop accepting new tool calls (5 s sleep)
//  3. SnapshotAll — persist module state
//  4. ShutdownAll — clean module shutdown
//  5. Check for an operator-provided <current executable>.new binary
//  6. upgrade.Swap(currentExe, newExe) — atomic rename
//  7. execReplace — exec-in-place (Unix) or spawn+exit (Windows)
//
// Each phase is best-effort: failures are logged but do not abort later
// phases. The whole sequence runs under a 60 s hard deadline so a stuck
// module cannot hold up the restart indefinitely.
//
// Step 5 allows the command to be used even when no .new file exists (e.g.
// admin-triggered restart or test). In that case steps 1–4 execute cleanly and
// the daemon exits, leaving supervisor to restart it on the next CC session.
//
// Design reference: tasks.md T058.
func handleGracefulRestart(
	logger *slog.Logger,
	pipeline *lifecycle.Pipeline,
	disp *dispatcher.Dispatcher,
	storageDir string,
) {
	const hardDeadline = 60 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), hardDeadline)
	defer cancel()

	logger.Info("graceful restart initiated", "budget_s", hardDeadline.Seconds())

	// Phase 1 — Drain: refuse new tool calls, wait for in-flight to finish.
	if err := pipeline.Drain(ctx, disp, 5*time.Second); err != nil {
		logger.Warn("Drain error (continuing)", "error", err)
	}

	// Phase 2 — Snapshot: persist module state.
	if _, err := pipeline.SnapshotAll(ctx, storageDir, daemonVersion); err != nil {
		logger.Warn("SnapshotAll error (continuing)", "error", err)
	}

	// Phase 3 — Shutdown: clean module teardown.
	if err := pipeline.ShutdownAll(ctx); err != nil {
		logger.Warn("ShutdownAll error (continuing)", "error", err)
	}

	// Phase 4 — Find new binary.
	currentExe, err := os.Executable()
	if err != nil {
		logger.Error("os.Executable failed — cannot swap binary", "error", err)
		os.Exit(1) // Signal failure to supervisor.
		return
	}
	newExe := currentExe + ".new"

	if _, statErr := os.Stat(newExe); os.IsNotExist(statErr) {
		logger.Warn("no .new binary found — exiting for supervisor restart",
			"looked_for", newExe,
		)
		os.Exit(0)
		return
	}

	// Phase 5 — Atomic swap.
	oldPath, swapErr := upgrade.Swap(currentExe, newExe)
	if swapErr != nil {
		logger.Error("upgrade.Swap failed — exiting for supervisor restart",
			"current", currentExe,
			"new", newExe,
			"error", swapErr,
		)
		os.Exit(1) // Signal failure to supervisor.
		return
	}
	logger.Info("binary swapped",
		"old_backed_up_as", oldPath,
		"new_active", currentExe,
	)

	// Phase 6 — Exec-replace.
	if err := execReplace(currentExe, logger); err != nil {
		logger.Error("exec-replace failed — supervisor will restart on next session",
			"binary", currentExe,
			"error", err,
		)
		os.Exit(1) // Signal failure to supervisor.
	}
}

// newRootLogger returns a JSON-format slog logger by default, or a text
// logger when ENGRAM_LOG_FORMAT=text is set. Structured by design decision
// D12 and NFR-4 (structured logging).
func newRootLogger() *slog.Logger {
	var handler slog.Handler
	if os.Getenv("ENGRAM_LOG_FORMAT") == "text" {
		handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	} else {
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	}
	return slog.New(handler).With("component", "engram-daemon", "version", daemonVersion)
}

// depsProviderFor returns a closure that builds a ModuleDeps value per
// module. Each module gets its own slog.Logger (with the "module" field
// attached), a private storage directory under $ENGRAM_DATA_DIR/modules/,
// and a shared DaemonCtx that is cancelled on SIGINT/SIGTERM.
//
// Storage dir convention (clarification C5): ${DATA_DIR}/modules/${moduleName}/
// with 0700 permissions. Created lazily on first module that needs it.
func depsProviderFor(root *slog.Logger, daemonCtx context.Context) func(name string) module.ModuleDeps {
	return func(name string) module.ModuleDeps {
		storageDir := filepath.Join(dataDir(), "modules", name)
		if err := os.MkdirAll(storageDir, 0o700); err != nil {
			root.Warn("failed to create module storage dir",
				"module", name,
				"path", storageDir,
				"error", err,
			)
		}
		return module.ModuleDeps{
			Logger:     root.With("module", name),
			DaemonCtx:  daemonCtx,
			StorageDir: storageDir,
			Config:     nil, // module-specific config comes from env in v0.1.0
			Notifier:   nil, // muxcore notifier wiring deferred to Phase 6
			Lookup:     nil, // cross-module lookup not used by engramcore
		}
	}
}

// dataDir returns the engram data directory. Honors ENGRAM_DATA_DIR env var
// with a sensible fallback under the user's home directory.
func dataDir() string {
	if dir := os.Getenv("ENGRAM_DATA_DIR"); dir != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".engram")
	}
	return filepath.Join(os.TempDir(), "engram")
}
