package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/thebtf/engram/internal/config"
)

const (
	existingVaultTestHexKey    = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	testVaultDerivationContext = "engram.crypto.vault-derive/v1\x00"
)

func TestOpenExistingVaultConfiguredHexHasPriorityAndDerivesKeys(t *testing.T) {
	vault, err := OpenExistingVault(&config.Config{
		EncryptionKey:     existingVaultTestHexKey,
		EncryptionKeyFile: filepath.Join(t.TempDir(), "missing-vault.key"),
	})
	if err != nil {
		t.Fatalf("OpenExistingVault configured hex: %v", err)
	}
	if got, want := vault.KeySource(), "env"; got != want {
		t.Errorf("key source: got %q, want %q", got, want)
	}

	assertVaultCommitmentAndDomainKeys(t, vault)
}

func TestOpenExistingVaultConfiguredFile(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "configured-vault.key")
	if err := os.WriteFile(keyFile, []byte(existingVaultTestHexKey), 0o600); err != nil {
		t.Fatalf("write configured vault key: %v", err)
	}
	before, err := os.Stat(keyFile)
	if err != nil {
		t.Fatalf("stat configured vault key before open: %v", err)
	}

	vault, err := OpenExistingVault(&config.Config{EncryptionKeyFile: keyFile})
	if err != nil {
		t.Fatalf("OpenExistingVault configured file: %v", err)
	}
	if got, want := vault.KeySource(), "file"; got != want {
		t.Errorf("key source: got %q, want %q", got, want)
	}

	after, err := os.Stat(keyFile)
	if err != nil {
		t.Fatalf("stat configured vault key after open: %v", err)
	}
	if got, want := after.Mode().Perm(), before.Mode().Perm(); got != want {
		t.Errorf("configured vault key mode changed while opening")
	}
}

func TestOpenExistingVaultDefaultExisting(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "vault.key")
	if err := os.WriteFile(keyFile, []byte(existingVaultTestHexKey), 0o600); err != nil {
		t.Fatalf("write default vault key: %v", err)
	}
	before, err := os.Stat(keyFile)
	if err != nil {
		t.Fatalf("stat default vault key before open: %v", err)
	}

	vault, err := openExistingVault(&config.Config{}, func() string { return keyFile }, loadKeyFromFile)
	if err != nil {
		t.Fatalf("open default existing vault: %v", err)
	}
	if got, want := vault.KeySource(), "auto_generated"; got != want {
		t.Errorf("key source: got %q, want %q", got, want)
	}

	after, err := os.Stat(keyFile)
	if err != nil {
		t.Fatalf("stat default vault key after open: %v", err)
	}
	if got, want := after.Mode().Perm(), before.Mode().Perm(); got != want {
		t.Errorf("default vault key mode changed while opening")
	}
}

func TestOpenExistingVaultMissingDefaultLeavesFilesystemUntouched(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "missing", "vault.key")

	vault, err := openExistingVault(&config.Config{}, func() string { return keyFile }, loadKeyFromFile)
	if vault != nil {
		t.Error("missing default vault returned a value")
	}
	assertVaultKeyUnavailable(t, err)

	if _, statErr := os.Stat(filepath.Dir(keyFile)); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("missing vault directory was created or cannot be classified as absent")
	}
}

func TestOpenExistingVaultDefaultDisappearanceLeavesFilesystemUntouched(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "vault.key")
	if err := os.WriteFile(keyFile, []byte(existingVaultTestHexKey), 0o600); err != nil {
		t.Fatalf("write default vault key: %v", err)
	}

	vault, err := openExistingVault(&config.Config{}, func() string { return keyFile }, func(path string) ([]byte, error) {
		if path != keyFile {
			t.Error("default vault read used an unexpected path")
			return nil, fs.ErrNotExist
		}
		if removeErr := os.Remove(path); removeErr != nil {
			t.Fatalf("simulate vault key disappearance: %v", removeErr)
		}
		return loadKeyFromFile(path)
	})
	if vault != nil {
		t.Error("disappeared default vault returned a value")
	}
	assertVaultKeyUnavailable(t, err)

	if _, statErr := os.Stat(keyFile); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("disappeared vault key was recreated or cannot be classified as absent")
	}
	entries, readErr := os.ReadDir(filepath.Dir(keyFile))
	if readErr != nil {
		t.Fatalf("read default vault directory after disappearance: %v", readErr)
	}
	if len(entries) != 0 {
		t.Error("opening a disappeared vault key created filesystem entries")
	}
}

func assertVaultCommitmentAndDomainKeys(t *testing.T, vault *Vault) {
	t.Helper()

	master, err := hex.DecodeString(existingVaultTestHexKey)
	if err != nil {
		t.Fatalf("decode test vault key: %v", err)
	}
	wantCommitment := sha256.Sum256(master)
	if got := vault.KeyCommitment(); got != wantCommitment {
		t.Error("full vault key commitment changed")
	}
	if got, want := vault.Fingerprint(), hex.EncodeToString(wantCommitment[:])[:16]; got != want {
		t.Errorf("vault fingerprint changed: got %q, want %q", got, want)
	}

	domains := []string{
		"engram.hap03/channel/v1",
		"engram.hap03/occurrence/v1",
		"engram.hap03/content/v1",
		"engram.hap03/receipt/v1",
	}
	seen := make(map[[32]byte]string, len(domains))
	for _, domain := range domains {
		got := vault.DeriveKey(domain)
		if repeat := vault.DeriveKey(domain); repeat != got {
			t.Error("domain key derivation is not stable")
		}
		if other, exists := seen[got]; exists {
			t.Errorf("distinct domains derived the same key: %q and %q", other, domain)
		}
		seen[got] = domain

		mac := hmac.New(sha256.New, master)
		_, _ = mac.Write([]byte(testVaultDerivationContext))
		_, _ = mac.Write([]byte(domain))
		var want [32]byte
		copy(want[:], mac.Sum(nil))
		if got != want {
			t.Errorf("domain key derivation did not use the fixed HMAC context for %q", domain)
		}
	}
}

func assertVaultKeyUnavailable(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected unavailable vault key error")
	}

	var unavailable *VaultKeyUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("missing vault key returned %T, want VaultKeyUnavailableError", err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Error("unavailable vault key did not preserve not-found classification")
	}
}
