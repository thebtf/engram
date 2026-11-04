//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unsafe"

	muxcontrol "github.com/thebtf/mcp-mux/muxcore/control"
	"github.com/thebtf/mcp-mux/muxcore/serverid"
	"golang.org/x/mod/semver"
	"golang.org/x/sys/windows"
)

const markerlessRecoveryAvailable = true

// The v7.0.19 tuple is from the tagged bootstrap-targets.json and release asset:
// https://github.com/thebtf/engram/releases/tag/v7.0.19
// Remove the v7.0.19 entry when that daemon is no longer a supported installed
// predecessor. This data authorizes identification for replacement, never launch.
//
//go:embed markerless-attestation.json
var markerlessAttestationData []byte

type markerlessTarget struct {
	Version           string `json:"version"`
	DaemonCompatEpoch int    `json:"daemon_compat_epoch"`
	Size              int64  `json:"size"`
	SHA256            string `json:"sha256"`
}

type markerlessPolicy struct {
	SchemaVersion int                `json:"schema_version"`
	RevokedSHA256 []string           `json:"revoked_sha256"`
	WindowsAMD64  []markerlessTarget `json:"windows_amd64"`
}

func uniqueJSONValue(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("duplicate or invalid policy field")
			}
			seen[name] = true
			if err := uniqueJSONValue(dec); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := uniqueJSONValue(dec); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected policy delimiter")
	}
	_, err = dec.Token()
	return err
}

func parseMarkerlessPolicy(raw []byte) (markerlessPolicy, error) {
	var policy markerlessPolicy
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if err := uniqueJSONValue(dec); err != nil {
		return policy, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return policy, errors.New("extra policy JSON")
	}
	if err := decodeStrictJSON(raw, &policy); err != nil {
		return policy, err
	}
	if policy.SchemaVersion != 1 || policy.RevokedSHA256 == nil || policy.WindowsAMD64 == nil {
		return policy, errors.New("invalid markerless policy")
	}
	validHash := func(s string) bool {
		if len(s) != 64 || s != strings.ToLower(s) {
			return false
		}
		_, err := hex.DecodeString(s)
		return err == nil
	}
	seen := map[string]bool{}
	for _, hash := range policy.RevokedSHA256 {
		if !validHash(hash) || seen[hash] {
			return policy, errors.New("invalid markerless revocation")
		}
		seen[hash] = true
	}
	seen = map[string]bool{}
	for _, target := range policy.WindowsAMD64 {
		if !semver.IsValid(target.Version) || semver.Canonical(target.Version) != target.Version || target.DaemonCompatEpoch != 1 || target.Size <= 0 || target.Size > 128*1024*1024 || !validHash(target.SHA256) || seen[target.SHA256] {
			return policy, errors.New("invalid markerless target")
		}
		seen[target.SHA256] = true
	}
	return policy, nil
}

func (p markerlessPolicy) classify(hash string, size int64, sameCurrent bool, client daemonConvergenceIdentity) (daemonConvergenceAction, error) {
	for _, revoked := range p.RevokedSHA256 {
		if hash == revoked {
			return daemonConvergenceFail, errors.New("markerless image is revoked")
		}
	}
	if sameCurrent {
		return daemonConvergenceJoin, nil
	}
	for _, target := range p.WindowsAMD64 {
		if target.SHA256 == hash && target.Size == size {
			action, err := classifyDaemonConvergence(client, daemonConvergenceIdentity{ProductVersion: target.Version, DaemonCompatEpoch: target.DaemonCompatEpoch})
			if err != nil || action != daemonConvergenceReplace {
				return daemonConvergenceFail, errors.New("markerless predecessor is not older")
			}
			return action, nil
		}
	}
	return daemonConvergenceFail, errors.New("markerless image is not attested")
}

var ntQueryImageMapping = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryInformationProcess")

var queryMarkerlessImageMapping = func(process windows.Handle, file windows.Handle) (uint32, error) {
	if err := ntQueryImageMapping.Find(); err != nil {
		return 0, err
	}
	value := uintptr(file)
	status, _, _ := ntQueryImageMapping.Call(uintptr(process), 44, uintptr(unsafe.Pointer(&value)), unsafe.Sizeof(value), 0)
	return uint32(status), nil
}

func imageMappingMatches(process windows.Handle, file windows.Handle) error {
	status, err := queryMarkerlessImageMapping(process, file)
	if err != nil {
		return fmt.Errorf("resolve process image mapping API: %w", err)
	}
	if status != 0 {
		return fmt.Errorf("process image mapping rejected: NTSTATUS 0x%08x", status)
	}
	return nil
}

func openMarkerlessImage(process windows.Handle) (*os.File, error) {
	path, err := queryFullProcessImageName(process)
	if err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.FILE_EXECUTE|windows.SYNCHRONIZE, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

func markerlessMarkersAbsent() error {
	return markerlessMarkersAbsentAt(muxcoreDaemonMarkerPath(), muxcoreLegacyDaemonVersionPath())
}

func markerlessMarkersAbsentAt(markerPath, legacyPath string) error {
	for _, path := range []string{markerPath, legacyPath} {
		if info, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				if info.Mode()&os.ModeSymlink != 0 {
					return errors.New("daemon marker is a symbolic link")
				}
				return errMuxcoreDaemonStillLower
			}
			return fmt.Errorf("inspect daemon marker: %w", err)
		}
		if _, err := readMuxcoreMarkerFile(path); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				return errMuxcoreDaemonStillLower
			}
			return fmt.Errorf("read daemon marker: %w", err)
		}
	}
	return nil
}

func markerlessBoundStatus(ctx context.Context, expected muxcoreDaemonStatusIdentity) error {
	path := serverid.DaemonControlPath("", muxcoreNamespace)
	conn, err := dialMuxcoreControl(path, 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	peer, err := readMuxcoreControlPeerPID(conn)
	if err != nil || peer != expected.PID {
		return errors.New("markerless status peer changed")
	}
	response, err := sendMuxcoreControlRequest(ctx, conn, muxcontrol.Request{Cmd: "status"})
	if err != nil || response == nil || !response.OK {
		return errors.New("markerless status unavailable")
	}
	var status muxcoreDaemonStatusIdentity
	if err := json.Unmarshal(response.Data, &status); err != nil || !sameMuxcoreDaemon(expected, status) || status.ShuttingDown || strings.TrimSpace(status.DaemonGeneration) == "" {
		return errors.New("markerless status identity changed")
	}
	return nil
}

func markerlessBoundStatusOrTransition(ctx context.Context, expected muxcoreDaemonStatusIdentity) error {
	err := markerlessBoundStatus(ctx, expected)
	if err == nil {
		return nil
	}
	return markerlessIdentityErrorOrTransition(expected, err)
}

func markerlessIdentityErrorOrTransition(expected muxcoreDaemonStatusIdentity, err error) error {
	current, ok := readMuxcoreDaemonStatusIdentity(serverid.DaemonControlPath("", muxcoreNamespace))
	if !ok || !sameMuxcoreDaemon(expected, current) || current.ShuttingDown {
		return errMuxcoreDaemonOwnerChanged
	}
	return err
}

// The process and image handles remain held until restart ACK and exit/convergence.
func reconcileMarkerlessDaemon(ctx context.Context, expected muxcoreDaemonStatusIdentity, client daemonConvergenceIdentity) (bool, bool, error) {
	return reconcileMarkerlessDaemonMode(ctx, expected, client, true)
}

func admitMarkerlessDaemon(ctx context.Context, expected muxcoreDaemonStatusIdentity, client daemonConvergenceIdentity) error {
	_, _, err := reconcileMarkerlessDaemonMode(ctx, expected, client, false)
	return err
}

func reconcileMarkerlessDaemonMode(ctx context.Context, expected muxcoreDaemonStatusIdentity, client daemonConvergenceIdentity, allowReplace bool) (bool, bool, error) {
	if expected.PID <= 0 || strings.TrimSpace(expected.DaemonGeneration) == "" || expected.ShuttingDown {
		return false, false, errors.New("invalid markerless status")
	}
	lock, err := acquireRestartLockWithContext(ctx)
	if err != nil {
		return false, false, err
	}
	locked := true
	defer func() {
		if locked {
			_ = lock.Close()
		}
	}()
	current, ok := readMuxcoreDaemonStatusIdentity(serverid.DaemonControlPath("", muxcoreNamespace))
	if !ok || !sameMuxcoreDaemon(expected, current) || current.ShuttingDown {
		return false, false, errMuxcoreDaemonOwnerChanged
	}
	if err := markerlessMarkersAbsent(); err != nil {
		return false, false, err
	}
	if err := markerlessBoundStatusOrTransition(ctx, expected); err != nil {
		return false, false, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.SYNCHRONIZE, false, uint32(expected.PID))
	if err != nil {
		return false, false, markerlessIdentityErrorOrTransition(expected, err)
	}
	defer windows.CloseHandle(process)
	image, err := openMarkerlessImage(process)
	if err != nil {
		return false, false, markerlessIdentityErrorOrTransition(expected, err)
	}
	defer image.Close()
	if err := imageMappingMatches(process, windows.Handle(image.Fd())); err != nil {
		return false, false, markerlessIdentityErrorOrTransition(expected, err)
	}
	info, err := image.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 128*1024*1024 {
		return false, false, errors.New("invalid markerless image size")
	}
	hasher := sha256.New()
	count, err := io.Copy(hasher, io.LimitReader(image, info.Size()+1))
	if err != nil {
		return false, false, err
	}
	if count != info.Size() {
		return false, false, errors.New("markerless image size changed during hashing")
	}
	hash := hex.EncodeToString(hasher.Sum(nil))
	policy, err := parseMarkerlessPolicy(markerlessAttestationData)
	if err != nil {
		return false, false, err
	}
	currentImage, err := openMarkerlessImage(windows.CurrentProcess())
	if err != nil {
		return false, false, err
	}
	defer currentImage.Close()
	if err := imageMappingMatches(windows.CurrentProcess(), windows.Handle(currentImage.Fd())); err != nil {
		return false, false, err
	}
	currentInfo, err := currentImage.Stat()
	if err != nil {
		return false, false, err
	}
	currentStatus, err := queryMarkerlessImageMapping(windows.CurrentProcess(), windows.Handle(image.Fd()))
	if err != nil || (currentStatus != 0 && currentStatus != 0xc0000001) {
		return false, false, errors.New("current process image mapping could not be determined")
	}
	sameCurrent := currentStatus == 0
	if sameCurrent != os.SameFile(info, currentInfo) {
		return false, false, errors.New("current image file and mapping disagree")
	}
	action, err := policy.classify(hash, info.Size(), sameCurrent, client)
	if err != nil {
		return false, false, err
	}
	if err := markerlessBoundStatusOrTransition(ctx, expected); err != nil {
		return false, false, err
	}
	if err := markerlessMarkersAbsent(); err != nil {
		return false, false, err
	}
	if err := imageMappingMatches(process, windows.Handle(image.Fd())); err != nil {
		return false, false, markerlessIdentityErrorOrTransition(expected, err)
	}
	if action == daemonConvergenceJoin {
		closeErr := lock.Close()
		locked = false
		return false, false, closeErr
	}
	if !allowReplace {
		return false, false, errors.New("markerless daemon requires replacement, not admission")
	}
	successor, err := currentExecutable()
	if err != nil {
		return false, false, err
	}
	successor, ok = normalizedExecutablePath(successor)
	if !ok {
		return false, false, errors.New("invalid successor executable")
	}
	if err := markerlessBoundStatusOrTransition(ctx, expected); err != nil {
		return false, false, err
	}
	if err := markerlessMarkersAbsent(); err != nil {
		return false, false, err
	}
	if err := imageMappingMatches(process, windows.Handle(image.Fd())); err != nil {
		return false, false, markerlessIdentityErrorOrTransition(expected, err)
	}
	if err := restartMuxcoreDaemon(ctx, expected, successor); err != nil {
		return false, false, err
	}
	closeErr := lock.Close()
	locked = false
	if closeErr != nil {
		return true, false, closeErr
	}
	proof := &windowsProcessExitProof{handle: process}
	waitErr := waitForRestartedMuxcoreDaemon(ctx, proof, expected)
	return true, errors.Is(waitErr, errMuxcoreDaemonLockAvailable), waitErr
}
