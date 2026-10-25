//go:build windows

package main

import (
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
	marker, markerErr := readMuxcoreDaemonVersionMarker(path)
	t.Logf("marker path=%s UTF16_units=%d os.ReadFile_error=%v marker_reader_error=%v", path, units, priorErr, markerErr)
	if priorErr != nil || string(prior) != string(raw) || markerErr != nil || marker.PID != 42 || marker.DaemonGeneration != "generation" {
		t.Fatalf("long Unicode marker differs from os.ReadFile: marker=%+v old_error=%v new_error=%v", marker, priorErr, markerErr)
	}
}
