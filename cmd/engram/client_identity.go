package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// directClientInstanceID uses the same installation identity file and atomic
// publish protocol as the plugin launcher. No checkout or CWD enters this ID.
func directClientInstanceID() (string, error) {
	if explicit := os.Getenv("ENGRAM_CLIENT_INSTANCE_ID"); explicit != "" {
		if !validDirectClientInstanceID(explicit) {
			return "", errors.New("explicit client instance ID is invalid")
		}
		return explicit, nil
	}
	root := os.Getenv("ENGRAM_DATA_DIR")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil || !filepath.IsAbs(home) {
			return "", errors.New("installation home directory is unavailable")
		}
		root = filepath.Join(home, ".engram")
	}
	if !filepath.IsAbs(root) {
		return "", errors.New("installation data directory must be absolute")
	}
	root = filepath.Clean(root)
	current := filepath.VolumeName(root) + string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(root, current), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		stat, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err = os.Mkdir(current, 0o700); errors.Is(err, os.ErrExist) {
				stat, err = os.Lstat(current)
			} else if err == nil {
				stat, err = os.Lstat(current)
			}
		}
		if err != nil {
			return "", fmt.Errorf("inspect installation directory: %w", err)
		}
		if !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("unsafe installation directory component")
		}
	}
	destination := filepath.Join(root, "client-instance-id")
	readIdentity := func() (string, error) {
		stat, err := os.Lstat(destination)
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		if !stat.Mode().IsRegular() || stat.Size() != 40 {
			return "", errors.New("installed client identity is invalid")
		}
		file, err := os.Open(destination)
		if err != nil {
			return "", err
		}
		defer file.Close()
		opened, err := file.Stat()
		if err != nil {
			return "", err
		}
		if !os.SameFile(stat, opened) {
			return "", errors.New("installed client identity changed during read")
		}
		bytes := make([]byte, 40)
		if _, err := file.ReadAt(bytes, 0); err != nil {
			return "", err
		}
		if string(bytes[:7]) != "engram-" || bytes[39] != '\n' {
			return "", errors.New("installed client identity is invalid")
		}
		if _, err := hex.DecodeString(string(bytes[7:39])); err != nil {
			return "", errors.New("installed client identity is invalid")
		}
		return string(bytes[:39]), nil
	}
	existing, err := readIdentity()
	if err != nil || existing != "" {
		return existing, err
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	staged := filepath.Join(root, "client-instance-id."+hex.EncodeToString(random[:16])+".tmp")
	file, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	defer os.Remove(staged)
	if _, err = file.WriteString("engram-" + hex.EncodeToString(random[16:]) + "\n"); err != nil {
		file.Close()
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	if err = os.Link(staged, destination); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	return readIdentity()
}

func validDirectClientInstanceID(value string) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 256 {
		return false
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("/\\@", r) {
			return false
		}
	}
	if len(value) > 1 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) {
		for i := 1; i < len(value); i++ {
			c := value[i]
			if c == ':' {
				return false
			}
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '+' || c == '.' || c == '-') {
				break
			}
		}
	}
	return true
}
