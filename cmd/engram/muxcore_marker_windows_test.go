//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

func TestMuxcoreDaemonMarkerReadDuringAtomicReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.marker.json")
	exe := filepath.ToSlash(filepath.Join(t.TempDir(), "engram.exe"))
	raw := []byte(`{"schema_version":2,"product_version":"v7.0.6","daemon_compat_epoch":1,"pid":42,"daemon_generation":"generation","exe":"` + exe + `"}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Run("read during replacement", func(t *testing.T) {
		name, err := windows.UTF16PtrFromString(path)
		if err != nil {
			t.Fatal(err)
		}
		// Atomic replacement holds DELETE access; concurrent readers must share it.
		handle, err := windows.CreateFile(name, windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer windows.CloseHandle(handle)
		marker, err := readMuxcoreDaemonVersionMarker(path)
		if err != nil || marker.PID != 42 || marker.DaemonGeneration != "generation" {
			t.Fatalf("read marker while atomic replacement holds DELETE: marker=%+v error=%v", marker, err)
		}
	})
	replacement := []byte(strings.ReplaceAll(string(raw), `"generation"`, `"successor"`))
	if err := writeMuxcoreMarkerAtomically(path, replacement); err != nil {
		t.Fatalf("atomically replace marker after releasing DELETE handle: %v", err)
	}
	marker, err := readMuxcoreDaemonVersionMarker(path)
	if err != nil || marker.DaemonGeneration != "successor" {
		t.Fatalf("read atomically replaced marker: marker=%+v error=%v", marker, err)
	}
}

func TestMuxcoreMarkerReadMatchesReadFileLongUnicodePath(t *testing.T) {
	version := windows.RtlGetVersion()
	t.Logf("native Windows=%d.%d.%d; Go enables CanUseLongPaths from build15063, not build14393", version.MajorVersion, version.MinorVersion, version.BuildNumber)
	segment := strings.Repeat("segment-", 20)
	root := filepath.Join(t.TempDir(), segment, segment, "память-🧠")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "daemon.marker.json")
	units := len(utf16.Encode([]rune(path)))
	if units <= 260 {
		t.Fatalf("fixture path has %d UTF16 units, want >260", units)
	}
	exe := filepath.ToSlash(filepath.Join(t.TempDir(), "engram.exe"))
	raw := []byte(`{"schema_version":2,"product_version":"v7.0.6","daemon_compat_epoch":1,"pid":42,"daemon_generation":"generation","exe":"` + exe + `"}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	prior, priorErr := os.ReadFile(path)
	extended := path
	if !strings.HasPrefix(extended, `\\?\`) {
		if strings.HasPrefix(extended, `\\`) {
			extended = `\\?\UNC\` + extended[2:]
		} else {
			extended = `\\?\` + extended
		}
	}
	control, controlErr := readMuxcoreDaemonVersionMarker(extended)
	t.Logf("explicit extended-path control: marker=%+v error=%v", control, controlErr)
	if controlErr != nil || control.PID != 42 || control.DaemonGeneration != "generation" {
		t.Fatalf("extended-path control failed before ordinary-path comparison: marker=%+v error=%v", control, controlErr)
	}
	marker, markerErr := readMuxcoreDaemonVersionMarker(path)
	t.Logf("marker path=%s UTF16_units=%d os.ReadFile_error=%v marker_reader_error=%v", path, units, priorErr, markerErr)
	if priorErr != nil || string(prior) != string(raw) || markerErr != nil || marker.PID != 42 || marker.DaemonGeneration != "generation" {
		t.Fatalf("long Unicode marker differs from os.ReadFile: marker=%+v old_error=%v new_error=%v", marker, priorErr, markerErr)
	}
	status := muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "generation"}
	client := daemonConvergenceIdentity{ProductVersion: "v7.0.6", DaemonCompatEpoch: 1}
	if action, err := readLiveMuxcoreDaemonActionAt(path, "", status, client); err != nil || action != daemonConvergenceJoin {
		t.Fatalf("long-path consumer action=%v error=%v; want join, nil", action, err)
	}
}

func TestMuxcoreMarkerReadPreservesNativeOpenErrors(t *testing.T) {
	for _, path := range []string{
		"", "nul\x00path", filepath.Join(t.TempDir(), "missing.marker.json"),
		":" + strings.Repeat("x", 300),
		`C:\` + strings.Repeat("x", 300),
		`C:\` + strings.Repeat(`segment\`, 4200),
	} {
		nativeErr := markerNativeOpenError(t, path)
		legacyPath := muxcoreMarkerOpenPath(path, false)
		legacyErr := markerNativeOpenError(t, legacyPath)
		wantErr := nativeErr
		if !muxcoreMarkerCanUseLongPaths {
			wantErr = legacyErr
		}
		_, fullErr := windows.FullPath(path)
		if fullErr != nil && (legacyPath != path || legacyErr != nativeErr) {
			t.Errorf("path_bytes=%d: failed conversion changed the native open error", len(path))
		}
		_, priorErr := os.ReadFile(path)
		_, gotErr := readMuxcoreMarkerFile(path)
		t.Logf("path_bytes=%d FullPath=%v os.ReadFile=%v raw_native=%v legacy_native=%v reader=%v", len(path), fullErr, unwrappedMarkerPathError(priorErr), nativeErr, legacyErr, unwrappedMarkerPathError(gotErr))
		var pathErr *os.PathError
		if !errors.As(gotErr, &pathErr) || pathErr.Op != "open" || pathErr.Path != path || pathErr.Err != wantErr {
			t.Errorf("path_bytes=%d: reader error does not preserve native open error", len(path))
		}
	}
}

func markerNativeOpenError(t *testing.T, path string) error {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err == nil {
		windows.CloseHandle(handle)
		t.Fatal("invalid fixture unexpectedly opened")
	}
	return err
}

func unwrappedMarkerPathError(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}

func TestMuxcoreMarkerOpenPathLegacy(t *testing.T) {
	t.Chdir(t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	drive := filepath.VolumeName(cwd)
	long := strings.Repeat("x", 300)
	abs := drive + `\` + long
	short := drive + `\` + strings.Repeat("x", 247-len(drive)-1)
	relative := strings.Repeat("x", 247-len(cwd)-1)
	for _, test := range []struct{ name, path, want string }{
		{"short absolute", short, short},
		{"absolute byte boundary", short + "x", `\\?\` + short + "x"},
		{"Unicode byte boundary", drive + `\` + strings.Repeat("я", 125), `\\?\` + drive + `\` + strings.Repeat("я", 125)},
		{"absolute", abs + `\marker.json`, `\\?\` + abs + `\marker.json`},
		{"forward slashes", drive + "/" + long + "/marker.json", `\\?\` + abs + `\marker.json`},
		{"mixed separators", drive + `\\` + long + `///marker.json`, `\\?\` + abs + `\marker.json`},
		{"dot", abs + `\.\marker.json`, `\\?\` + abs + `\marker.json`},
		{"dotdot collapses below threshold", abs + `\..\marker.json`, `\\?\` + drive + `\marker.json`},
		{"dotdot above drive root", abs + `\..\..\marker.json`, `\\?\` + drive + `\marker.json`},
		{"short relative", "marker.json", "marker.json"},
		{"relative below boundary", relative, relative},
		{"relative boundary", relative + "x", `\\?\` + cwd + `\` + relative + "x"},
		{"relative", long + `\marker.json`, `\\?\` + cwd + `\` + long + `\marker.json`},
		{"relative dotdot", long + `\..\marker.json`, `\\?\` + cwd + `\marker.json`},
		{"drive relative", drive + long + `\marker.json`, `\\?\` + cwd + `\` + long + `\marker.json`},
		{"root relative", `\` + long + `\marker.json`, `\\?\` + abs + `\marker.json`},
		{"UNC", `\\server\share\` + long, `\\?\UNC\server\share\` + long},
		{"UNC mixed separators", `/\server/share\` + long, `\\?\UNC\server\share\` + long},
		{"UNC dotdot preserves share", `\\server\share\` + long + `\..\..\marker.json`, `\\?\UNC\server\share\marker.json`},
		{"UNC trailing separator", `\\server\share\` + long + `\`, `\\?\UNC\server\share\` + long + `\`},
		{"device", `\\.\` + abs + `\marker.json`, `\\.\` + abs + `\marker.json`},
		{"device dotdot", `//./` + drive + "/" + long + `/../marker.json`, `\\.\` + drive + `\marker.json`},
		{"already extended", `\\?\` + abs + `/marker.json`, `\\?\` + abs + `/marker.json`},
		{"mixed extended prefix", `/\?/` + abs + `/marker.json`, `/\?/` + abs + `/marker.json`},
		{"NT extended", `\??\` + abs + `\marker.json`, `\??\` + abs + `\marker.json`},
		{"empty", "", ""},
		{"NUL", long + "\x00marker.json", long + "\x00marker.json"},
		{"native length rejection", drive + `\` + strings.Repeat(`segment\`, 4200), drive + `\` + strings.Repeat(`segment\`, 4200)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := muxcoreMarkerOpenPath(test.path, false); got != test.want {
				t.Errorf("legacy conversion: got %q; want %q", got, test.want)
			}
			if got := muxcoreMarkerOpenPath(test.path, true); got != test.path {
				t.Error("modern path changed")
			}
		})
	}
}

func TestMuxcoreMarkerOpenPathShortRelativeInLongDirectory(t *testing.T) {
	segment := strings.Repeat("segment-", 20)
	dir := filepath.Join(t.TempDir(), segment, segment)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if got, want := muxcoreMarkerOpenPath("marker.json", false), `\\?\`+dir+`\marker.json`; got != want {
		t.Fatalf("short relative path in long cwd: got %q; want %q", got, want)
	}
}
