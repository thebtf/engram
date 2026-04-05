// Package crypto provides AES-256-GCM encryption for credential storage.
package crypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/config"
)

// Vault provides AES-256-GCM encryption using a single master key.
type Vault struct {
	key         []byte // 32 bytes for AES-256
	fingerprint string // SHA-256(key)[:16] hex
	source      string // how the key was loaded: "env", "file", "auto_generated"
}

// NewVault creates a Vault by loading or generating the encryption key.
// Key loading priority:
// 1. cfg.EncryptionKey (hex-decoded from ENGRAM_ENCRYPTION_KEY env var)
// 2. cfg.EncryptionKeyFile (read file from ENGRAM_ENCRYPTION_KEY_FILE env var)
// 3. Auto-generate: save to DataDir()/vault.key, log warning
func NewVault(cfg *config.Config) (*Vault, error) {
	var key []byte
	var err error

	switch {
	case cfg.EncryptionKey != "":
		key, err = hex.DecodeString(cfg.EncryptionKey)
		if err != nil {
			return nil, fmt.Errorf("decode ENGRAM_ENCRYPTION_KEY: %w", err)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("ENGRAM_ENCRYPTION_KEY must be 32 bytes (64 hex chars), got %d bytes", len(key))
		}
		log.Info().Msg("vault: loaded key from ENGRAM_ENCRYPTION_KEY env var")
		return &Vault{key: key, fingerprint: computeFingerprint(key), source: "env"}, nil

	case cfg.EncryptionKeyFile != "":
		key, err = loadKeyFromFile(cfg.EncryptionKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load key from ENGRAM_ENCRYPTION_KEY_FILE %q: %w", cfg.EncryptionKeyFile, err)
		}
		log.Info().Str("file", cfg.EncryptionKeyFile).Msg("vault: loaded key from file")
		return &Vault{key: key, fingerprint: computeFingerprint(key), source: "file"}, nil

	default:
		// Prefer /data/vault.key (Docker persistent volume) over ~/.engram/vault.key.
		// This prevents key loss when containers are recreated.
		keyFile := filepath.Join(config.DataDir(), "vault.key")
		if altDir := "/data"; isDir(altDir) {
			keyFile = filepath.Join(altDir, "vault.key")
		}
		if _, statErr := os.Stat(keyFile); statErr == nil {
			key, err = loadKeyFromFile(keyFile)
			if err != nil {
				return nil, fmt.Errorf("load auto-generated key from %q: %w", keyFile, err)
			}
			log.Info().Str("file", keyFile).Msg("vault: loaded auto-generated key")
			return &Vault{key: key, fingerprint: computeFingerprint(key), source: "auto_generated"}, nil
		} else if !os.IsNotExist(statErr) {
			// Stat failed for a reason other than the file not existing (e.g.,
			// permission denied, I/O error). Returning here prevents accidentally
			// generating a new key and overwriting an existing vault.key.
			return nil, fmt.Errorf("stat vault key %q: %w", keyFile, statErr)
		} else {
			key = make([]byte, 32)
			if _, err = io.ReadFull(rand.Reader, key); err != nil {
				return nil, fmt.Errorf("generate encryption key: %w", err)
			}
			if err = os.MkdirAll(filepath.Dir(keyFile), 0700); err != nil {
				return nil, fmt.Errorf("create key directory %q: %w", filepath.Dir(keyFile), err)
			}
			if err = saveKeyToFile(keyFile, key); err != nil {
				return nil, fmt.Errorf("save generated key to %q: %w", keyFile, err)
			}
			log.Warn().
				Str("file", keyFile).
				Msg("vault: auto-generated new encryption key — BACK UP THIS FILE to avoid losing access to stored credentials")
			return &Vault{key: key, fingerprint: computeFingerprint(key), source: "auto_generated"}, nil
		}
	}
}

// Encrypt encrypts plaintext using AES-256-GCM.
// Returns nonce (12B) || ciphertext || GCM tag as a single byte slice.
func (v *Vault) Encrypt(plaintext string) ([]byte, error) {
	block, err := aes.NewCipher(v.key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize()) // 12 bytes
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	// gcm.Seal(dst, nonce, plaintext, additionalData) appends ciphertext+tag to dst.
	// Using nonce as dst prepends the nonce to the output: nonce || ciphertext || tag.
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return ciphertext, nil
}

// Decrypt decrypts ciphertext produced by Encrypt.
// Expected format: nonce (12B) || ciphertext || GCM tag (16B).
func (v *Vault) Decrypt(ciphertext []byte) (string, error) {
	block, err := aes.NewCipher(v.key)
	if err != nil {
		return "", fmt.Errorf("create AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	minLen := nonceSize + gcm.Overhead()
	if len(ciphertext) < minLen {
		return "", fmt.Errorf("ciphertext too short: got %d bytes, need at least %d", len(ciphertext), minLen)
	}

	nonce, data := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}

	return string(plaintext), nil
}

// Fingerprint returns the first 16 hex chars of SHA-256(key).
func (v *Vault) Fingerprint() string {
	return v.fingerprint
}

// KeySource returns how the encryption key was loaded: "env", "file", or "auto_generated".
func (v *Vault) KeySource() string {
	return v.source
}

// MatchesFingerprint reports whether fp matches this vault's key fingerprint.
// Uses constant-time comparison to avoid timing side-channels on key-derived material.
func (v *Vault) MatchesFingerprint(fp string) bool {
	return subtle.ConstantTimeCompare([]byte(v.fingerprint), []byte(fp)) == 1
}

// VaultExists reports whether a vault key is already configured (env var set or
// key file exists on disk). It performs a read-only passive check and does NOT
// create files or initialize the vault.
func VaultExists(cfg *config.Config) bool {
	if cfg.EncryptionKey != "" {
		return true
	}
	if cfg.EncryptionKeyFile != "" {
		_, err := os.Stat(cfg.EncryptionKeyFile)
		return err == nil
	}
	keyFile := filepath.Join(config.DataDir(), "vault.key")
	_, err := os.Stat(keyFile)
	return err == nil
}

// computeFingerprint returns the first 16 hex chars of SHA-256(key).
func computeFingerprint(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:])[:16]
}

// loadKeyFromFile reads a hex-encoded 32-byte key from a file.
// Supports both 64-char hex strings and raw 32-byte files.
func loadKeyFromFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	hexStr := strings.TrimSpace(string(data))
	// Try hex-encoded first (preferred format).
	if decoded, err := hex.DecodeString(hexStr); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	// Fall back to raw bytes (trimmed — file may have trailing newline that would
	// cause a 32-byte key to appear as 33 bytes and fail the length check).
	raw := bytes.TrimRight(data, "\r\n \t")
	if len(raw) == 32 {
		return raw, nil
	}
	return nil, fmt.Errorf("key file must contain 32 raw bytes or 64 hex chars, got %d bytes", len(data))
}

// isDir returns true if path exists and is a directory.
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// saveKeyToFile saves the key as hex to path with 0600 permissions.
func saveKeyToFile(path string, key []byte) error {
	hexKey := hex.EncodeToString(key)
	return os.WriteFile(path, []byte(hexKey), 0600)
}
