package crypto_test

import (
	"os"
	"testing"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/crypto"
)

const testHexKey = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

func TestVault_EncryptDecryptRoundTrip(t *testing.T) {
	cfg := &config.Config{
		EncryptionKey: testHexKey,
	}

	v, err := crypto.NewVault(cfg)
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}

	plaintext := "super-secret-api-key-12345"
	ciphertext, err := v.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if len(ciphertext) == 0 {
		t.Fatal("Encrypt returned empty ciphertext")
	}

	decrypted, err := v.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if decrypted != plaintext {
		t.Errorf("round-trip mismatch: got %q, want %q", decrypted, plaintext)
	}
}

func TestVault_EmptyPlaintext(t *testing.T) {
	cfg := &config.Config{EncryptionKey: testHexKey}
	v, err := crypto.NewVault(cfg)
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}

	ct, err := v.Encrypt("")
	if err != nil {
		t.Fatalf("Encrypt empty string: %v", err)
	}
	pt, err := v.Decrypt(ct)
	if err != nil {
		t.Fatalf("Decrypt empty string: %v", err)
	}
	if pt != "" {
		t.Errorf("expected empty plaintext, got %q", pt)
	}
}

func TestVault_DifferentNonces(t *testing.T) {
	cfg := &config.Config{EncryptionKey: testHexKey}
	v, err := crypto.NewVault(cfg)
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}

	ct1, err := v.Encrypt("same-plaintext")
	if err != nil {
		t.Fatalf("Encrypt 1: %v", err)
	}
	ct2, err := v.Encrypt("same-plaintext")
	if err != nil {
		t.Fatalf("Encrypt 2: %v", err)
	}
	if string(ct1) == string(ct2) {
		t.Error("two encryptions of same plaintext produced identical ciphertext (nonce not random)")
	}
}

func TestVault_Fingerprint(t *testing.T) {
	cfg := &config.Config{EncryptionKey: testHexKey}
	v, err := crypto.NewVault(cfg)
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}

	fp := v.Fingerprint()
	if len(fp) != 16 {
		t.Errorf("fingerprint length: got %d, want 16", len(fp))
	}
	if !v.MatchesFingerprint(fp) {
		t.Error("MatchesFingerprint returned false for own fingerprint")
	}
	if v.MatchesFingerprint("0000000000000000") {
		t.Error("MatchesFingerprint returned true for wrong fingerprint")
	}
}

func TestVault_DecryptTamperedCiphertext(t *testing.T) {
	cfg := &config.Config{EncryptionKey: testHexKey}
	v, err := crypto.NewVault(cfg)
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}

	ct, _ := v.Encrypt("secret")
	// Flip a byte in the ciphertext portion.
	ct[len(ct)-1] ^= 0xFF

	if _, err := v.Decrypt(ct); err == nil {
		t.Error("expected decryption of tampered ciphertext to fail")
	}
}

func TestVault_DecryptTooShort(t *testing.T) {
	cfg := &config.Config{EncryptionKey: testHexKey}
	v, err := crypto.NewVault(cfg)
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}

	if _, err := v.Decrypt([]byte("short")); err == nil {
		t.Error("expected error for too-short ciphertext")
	}
}

func TestVault_AutoGenerate(t *testing.T) {
	tmpDir := t.TempDir()
	// os.UserHomeDir() checks USERPROFILE (Windows) then HOME (Unix)
	t.Setenv("HOME", tmpDir)
	t.Setenv("USERPROFILE", tmpDir)

	cfg := &config.Config{} // no key configured
	v, err := crypto.NewVault(cfg)
	if err != nil {
		t.Fatalf("NewVault with auto-generate: %v", err)
	}

	fp := v.Fingerprint()
	if len(fp) != 16 {
		t.Errorf("auto-generated vault fingerprint length: got %d, want 16", len(fp))
	}

	ct, err := v.Encrypt("test-value")
	if err != nil {
		t.Fatalf("Encrypt after auto-generate: %v", err)
	}
	pt, err := v.Decrypt(ct)
	if err != nil {
		t.Fatalf("Decrypt after auto-generate: %v", err)
	}
	if pt != "test-value" {
		t.Errorf("round-trip: got %q, want %q", pt, "test-value")
	}
}

func TestVault_KeyFromFile(t *testing.T) {
	tmpFile, err := os.CreateTemp(t.TempDir(), "vault*.key")
	if err != nil {
		t.Fatal(err)
	}
	defer tmpFile.Close()
	// Write 64 hex chars (32 bytes).
	if _, err := tmpFile.WriteString(testHexKey); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{EncryptionKeyFile: tmpFile.Name()}
	v, err := crypto.NewVault(cfg)
	if err != nil {
		t.Fatalf("NewVault from file: %v", err)
	}

	ct, err := v.Encrypt("hello")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	pt, err := v.Decrypt(ct)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if pt != "hello" {
		t.Errorf("got %q, want hello", pt)
	}
}

func TestVault_InvalidHexKey(t *testing.T) {
	cfg := &config.Config{EncryptionKey: "not-valid-hex"}
	if _, err := crypto.NewVault(cfg); err == nil {
		t.Error("expected error for invalid hex key")
	}
}

func TestVault_WrongKeyLength(t *testing.T) {
	// 32 hex chars = 16 bytes (too short for AES-256)
	cfg := &config.Config{EncryptionKey: "0102030405060708090a0b0c0d0e0f10"}
	if _, err := crypto.NewVault(cfg); err == nil {
		t.Error("expected error for wrong key length")
	}
}
