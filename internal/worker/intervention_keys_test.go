package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/crypto"
)

const interventionKeyTestHex = "a10102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

type fakeInterventionReceiptEpochReader struct {
	epochs [][32]byte
	err    error
	calls  int
}

func (r *fakeInterventionReceiptEpochReader) DistinctInterventionReceiptEpochs(context.Context) ([][32]byte, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return r.epochs, nil
}

func TestOpenInterventionKeyProviderAdmitsOnlyCurrentEpoch(t *testing.T) {
	cfg, vaultDirectory, vaultPath, vault := existingInterventionVault(t)
	current := vault.KeyCommitment()

	tests := []struct {
		name    string
		reader  *fakeInterventionReceiptEpochReader
		wantErr bool
	}{
		{
			name:   "zero retained epochs is viable",
			reader: &fakeInterventionReceiptEpochReader{},
		},
		{
			name: "current retained epoch is viable",
			reader: &fakeInterventionReceiptEpochReader{
				epochs: [][32]byte{current},
			},
		},
		{
			name: "mismatched retained epoch is rejected",
			reader: &fakeInterventionReceiptEpochReader{
				epochs: [][32]byte{{0x01}},
			},
			wantErr: true,
		},
		{
			name: "zero retained epoch is malformed",
			reader: &fakeInterventionReceiptEpochReader{
				epochs: [][32]byte{{}},
			},
			wantErr: true,
		},
		{
			name: "duplicate retained epoch is malformed",
			reader: &fakeInterventionReceiptEpochReader{
				epochs: [][32]byte{current, current},
			},
			wantErr: true,
		},
		{
			name: "receipt store error is rejected",
			reader: &fakeInterventionReceiptEpochReader{
				err: errors.New("store unavailable"),
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider, err := openInterventionKeyProvider(context.Background(), cfg, test.reader)
			if test.wantErr {
				if err == nil {
					t.Fatal("openInterventionKeyProvider returned nil error")
				}
				if provider != nil {
					t.Fatal("openInterventionKeyProvider returned a provider with an invalid retained epoch")
				}
			} else {
				if err != nil {
					t.Fatalf("openInterventionKeyProvider: %v", err)
				}
				if provider == nil {
					t.Fatal("openInterventionKeyProvider returned a nil provider")
				}
				keyEpoch, ok := provider.Current()
				if !ok {
					t.Fatal("provider has no current key epoch")
				}
				if keyEpoch.EpochCommitment() != current {
					t.Fatal("provider current epoch does not match the vault commitment")
				}
				if keyEpoch.ChannelKey() != vault.DeriveKey("engram.hap03/channel/v1") {
					t.Fatal("provider channel key does not use the HAP-03 channel domain")
				}
				if keyEpoch.OccurrenceKey() != vault.DeriveKey("engram.hap03/occurrence/v1") {
					t.Fatal("provider occurrence key does not use the HAP-03 occurrence domain")
				}
				if keyEpoch.ContentKey() != vault.DeriveKey("engram.hap03/content/v1") {
					t.Fatal("provider content key does not use the HAP-03 content domain")
				}
				if keyEpoch.ReceiptKey() != vault.DeriveKey("engram.hap03/receipt/v1") {
					t.Fatal("provider receipt key does not use the HAP-03 receipt domain")
				}
			}

			assertOnlyExistingInterventionVault(t, vaultDirectory, vaultPath)
		})
	}
}

func TestOpenInterventionKeyProviderRejectsMissingInputsWithoutGeneratingVault(t *testing.T) {
	t.Run("missing receipt epoch reader", func(t *testing.T) {
		cfg, vaultDirectory, vaultPath, _ := existingInterventionVault(t)

		provider, err := openInterventionKeyProvider(context.Background(), cfg, nil)
		if err == nil {
			t.Fatal("openInterventionKeyProvider returned nil error")
		}
		if provider != nil {
			t.Fatal("openInterventionKeyProvider returned a provider without a reader")
		}
		assertOnlyExistingInterventionVault(t, vaultDirectory, vaultPath)
	})

	t.Run("missing existing vault", func(t *testing.T) {
		vaultDirectory := t.TempDir()
		vaultPath := filepath.Join(vaultDirectory, "missing-vault.key")
		reader := &fakeInterventionReceiptEpochReader{}

		provider, err := openInterventionKeyProvider(context.Background(), &config.Config{
			EncryptionKeyFile: vaultPath,
		}, reader)
		if err == nil {
			t.Fatal("openInterventionKeyProvider returned nil error")
		}
		if provider != nil {
			t.Fatal("openInterventionKeyProvider returned a provider without an existing vault")
		}
		var unavailable *crypto.VaultKeyUnavailableError
		if !errors.As(err, &unavailable) {
			t.Fatalf("missing vault error = %T, want VaultKeyUnavailableError", err)
		}
		if _, err := os.Stat(vaultPath); !os.IsNotExist(err) {
			t.Fatalf("missing vault path after open = %v, want not exist", err)
		}
		assertEmptyDirectory(t, vaultDirectory)
	})
}

func existingInterventionVault(t *testing.T) (*config.Config, string, string, *crypto.Vault) {
	t.Helper()

	vaultDirectory := t.TempDir()
	vaultPath := filepath.Join(vaultDirectory, "existing-vault.key")
	if err := os.WriteFile(vaultPath, []byte(interventionKeyTestHex), 0o600); err != nil {
		t.Fatalf("write existing vault fixture: %v", err)
	}

	cfg := &config.Config{EncryptionKeyFile: vaultPath}
	vault, err := crypto.OpenExistingVault(cfg)
	if err != nil {
		t.Fatalf("open existing vault fixture: %v", err)
	}
	return cfg, vaultDirectory, vaultPath, vault
}

func assertOnlyExistingInterventionVault(t *testing.T, directory, vaultPath string) {
	t.Helper()

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read vault fixture directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(vaultPath) || entries[0].IsDir() {
		t.Fatalf("vault fixture directory contents = %#v, want only %q", entries, filepath.Base(vaultPath))
	}
}

func assertEmptyDirectory(t *testing.T, directory string) {
	t.Helper()

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read vault fixture directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("vault fixture directory contents = %#v, want empty", entries)
	}
}
