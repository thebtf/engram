package legacyrelay

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	locatorFileName = "engram-hap-01b.locator.json"
	maxLocatorBytes = 4096
)

// Locator is the non-secret, generation-bound discovery record for the dark
// relay endpoint. Possession is not authority; every request still passes peer,
// adapter, capability, route, and deadline admission.
type Locator struct {
	Protocol   string
	Generation DaemonGeneration
	Endpoint   string
}

type locatorWire struct {
	Protocol         string `json:"protocol"`
	DaemonGeneration string `json:"daemon_generation"`
	Endpoint         string `json:"endpoint"`
}

// RuntimeLocatorPath resolves the locator from the caller-supplied runtime
// directory, or the current process runtime temp directory when empty.
func RuntimeLocatorPath(baseDir string) string {
	if strings.TrimSpace(baseDir) == "" {
		baseDir = os.TempDir()
	}
	return filepath.Join(baseDir, locatorFileName)
}

// RuntimeEndpoint derives a stable logical endpoint for one daemon generation.
// The generation is hashed only to keep Unix socket paths bounded; the digest
// is not an authenticator.
func RuntimeEndpoint(baseDir string, generation DaemonGeneration) (string, error) {
	endpoint, _, err := runtimeEndpointForListener(baseDir, generation)
	return endpoint, err
}

func runtimeEndpointForListener(baseDir string, generation DaemonGeneration) (string, string, error) {
	if !generation.valid() {
		return "", "", ErrStaleGeneration
	}
	if strings.TrimSpace(baseDir) == "" {
		baseDir = os.TempDir()
	}
	return runtimeEndpointPath(baseDir, runtimeEndpointFileName(generation))
}

func runtimeEndpointFileName(generation DaemonGeneration) string {
	digest := sha256.Sum256([]byte(generation.Value()))
	return "engram-hap-01b-" + hex.EncodeToString(digest[:8]) + ".sock"
}

// PublishLocator atomically writes one strict mode-0600 locator. It never
// writes a credential, project, capability, process identity, or body.
func PublishLocator(path string, locator Locator) error {
	if err := validateLocator(locator); err != nil {
		return err
	}
	if strings.TrimSpace(path) == "" {
		return errors.New("legacy relay locator path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create legacy relay locator directory: %w", err)
	}
	payload, err := json.Marshal(locatorWire{
		Protocol:         locator.Protocol,
		DaemonGeneration: locator.Generation.Value(),
		Endpoint:         locator.Endpoint,
	})
	if err != nil {
		return fmt.Errorf("marshal legacy relay locator: %w", err)
	}
	payload = append(payload, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), ".engram-hap-01b-locator-*.tmp")
	if err != nil {
		return fmt.Errorf("create legacy relay locator temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set legacy relay locator permissions: %w", err)
	}
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write legacy relay locator: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync legacy relay locator: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close legacy relay locator: %w", err)
	}
	if err := replaceFile(tmpName, path); err != nil {
		return fmt.Errorf("publish legacy relay locator: %w", err)
	}
	cleanup = false
	return nil
}

// ReadLocator parses one exact bounded locator record. Unknown fields, trailing
// values, unsupported protocol, invalid generation, and unsafe endpoints fail
// closed before any dial attempt.
func ReadLocator(path string) (Locator, error) {
	file, err := os.Open(path)
	if err != nil {
		return Locator{}, fmt.Errorf("open legacy relay locator: %w", err)
	}
	defer file.Close()

	limited := io.LimitReader(file, maxLocatorBytes+1)
	payload, err := io.ReadAll(limited)
	if err != nil {
		return Locator{}, fmt.Errorf("read legacy relay locator: %w", err)
	}
	if len(payload) == 0 || len(payload) > maxLocatorBytes {
		return Locator{}, errors.New("legacy relay locator size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var wire locatorWire
	if err := decoder.Decode(&wire); err != nil {
		return Locator{}, fmt.Errorf("decode legacy relay locator: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Locator{}, err
	}
	generation, err := NewDaemonGeneration(wire.DaemonGeneration)
	if err != nil {
		return Locator{}, err
	}
	locator := Locator{Protocol: wire.Protocol, Generation: generation, Endpoint: wire.Endpoint}
	if err := validateLocator(locator); err != nil {
		return Locator{}, err
	}
	return locator, nil
}

func validateLocator(locator Locator) error {
	if locator.Protocol != Protocol {
		return ErrProtocol
	}
	if !locator.Generation.valid() {
		return ErrStaleGeneration
	}
	if strings.TrimSpace(locator.Endpoint) != locator.Endpoint || locator.Endpoint == "" || len(locator.Endpoint) > 4096 {
		return errors.New("legacy relay endpoint is invalid")
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("legacy relay locator contains trailing JSON")
		}
		return fmt.Errorf("decode legacy relay locator trailing bytes: %w", err)
	}
	return nil
}
