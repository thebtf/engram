package crypto

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/thebtf/engram/internal/config"
)

func TestLoadKeyFromFileBytes(t *testing.T) {
	binaryKey := make([]byte, 32)
	for i := range binaryKey {
		binaryKey[i] = byte(i + 2)
	}

	tests := []struct {
		name    string
		data    []byte
		want    []byte
		wantErr bool
	}{
		{name: "raw_binary", data: bytes.Clone(binaryKey), want: binaryKey},
		{name: "raw_with_LF", data: append(bytes.Clone(binaryKey), '\n'), want: binaryKey},
		{name: "raw_with_CRLF", data: append(bytes.Clone(binaryKey), '\r', '\n'), want: binaryKey},
		{name: "hex", data: []byte(hex.EncodeToString(binaryKey)), want: binaryKey},
		{name: "hex_with_LF", data: []byte(hex.EncodeToString(binaryKey) + "\n"), want: binaryKey},
		{name: "hex_with_CRLF", data: []byte(hex.EncodeToString(binaryKey) + "\r\n"), want: binaryKey},
		{name: "empty", data: []byte{}, wantErr: true},
		{name: "short_raw", data: binaryKey[:31], wantErr: true},
		{name: "long_raw", data: append(bytes.Clone(binaryKey), 0xff), wantErr: true},
		{name: "invalid_hex_content", data: bytes.Repeat([]byte("z"), 64), wantErr: true},
	}
	for _, ending := range []struct {
		name string
		last byte
	}{
		{name: "CR", last: '\r'},
		{name: "LF", last: '\n'},
		{name: "space", last: ' '},
		{name: "tab", last: '\t'},
	} {
		key := bytes.Clone(binaryKey)
		key[len(key)-1] = ending.last
		tests = append(tests, struct {
			name    string
			data    []byte
			want    []byte
			wantErr bool
		}{name: "raw_ending_" + ending.name, data: key, want: key})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "synthetic.key")
			if err := os.WriteFile(path, tt.data, 0o600); err != nil {
				t.Fatalf("write synthetic key file: %v", err)
			}
			got, err := loadKeyFromFile(path)
			if tt.wantErr {
				if err == nil {
					t.Errorf("loadKeyFromFile() = %x, nil; want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("loadKeyFromFile(): %v", err)
			}
			if !bytes.Equal(got, tt.want) {
				t.Errorf("loadKeyFromFile() = %x; want %x", got, tt.want)
			}
		})
	}
}

func TestOpenExistingVaultRawKeyFileBytes(t *testing.T) {
	for _, ending := range []struct {
		name string
		last byte
	}{
		{name: "CR", last: '\r'},
		{name: "LF", last: '\n'},
		{name: "space", last: ' '},
		{name: "tab", last: '\t'},
	} {
		t.Run(ending.name, func(t *testing.T) {
			key := append(bytes.Repeat([]byte{0x42}, 31), ending.last)
			path := filepath.Join(t.TempDir(), "synthetic.key")
			if err := os.WriteFile(path, key, 0o600); err != nil {
				t.Fatalf("write synthetic key file: %v", err)
			}
			fromFile, err := OpenExistingVault(&config.Config{EncryptionKeyFile: path})
			if err != nil {
				t.Fatalf("OpenExistingVault from file: %v", err)
			}
			if fromFile.KeySource() != "file" {
				t.Errorf("key source = %q; want file", fromFile.KeySource())
			}
			fromHex, err := OpenExistingVault(&config.Config{EncryptionKey: hex.EncodeToString(key)})
			if err != nil {
				t.Fatalf("OpenExistingVault from hex: %v", err)
			}
			ciphertext, err := fromFile.Encrypt("synthetic plaintext")
			if err != nil {
				t.Fatalf("encrypt: %v", err)
			}
			plaintext, err := fromHex.Decrypt(ciphertext)
			if err != nil {
				t.Fatalf("decrypt with same synthetic key: %v", err)
			}
			if plaintext != "synthetic plaintext" {
				t.Errorf("plaintext = %q; want synthetic plaintext", plaintext)
			}
		})
	}
}
