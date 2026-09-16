package worker

import (
	"context"
	"fmt"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/crypto"
	"github.com/thebtf/engram/internal/intervention"
)

const (
	interventionChannelKeyDomain    = "engram.hap03/channel/v1"
	interventionOccurrenceKeyDomain = "engram.hap03/occurrence/v1"
	interventionContentKeyDomain    = "engram.hap03/content/v1"
	interventionReceiptKeyDomain    = "engram.hap03/receipt/v1"
)

// InterventionReceiptEpochReader reads the distinct, retained receipt epoch
// commitments without granting the worker any receipt mutation capability.
type InterventionReceiptEpochReader interface {
	DistinctInterventionReceiptEpochs(context.Context) ([][32]byte, error)
}

// openInterventionKeyProvider opens only existing Vault material, derives the
// HAP-03 keys for the current epoch, and refuses historical epoch rotation.
func openInterventionKeyProvider(ctx context.Context, cfg *config.Config, reader InterventionReceiptEpochReader) (intervention.KeyProvider, error) {
	if cfg == nil {
		return nil, fmt.Errorf("open intervention key provider: missing configuration")
	}
	if reader == nil {
		return nil, fmt.Errorf("open intervention key provider: missing receipt epoch reader")
	}

	vault, err := crypto.OpenExistingVault(cfg)
	if err != nil {
		return nil, fmt.Errorf("open existing intervention vault: %w", err)
	}

	currentEpoch := vault.KeyCommitment()
	retainedEpochs, err := reader.DistinctInterventionReceiptEpochs(ctx)
	if err != nil {
		return nil, fmt.Errorf("read retained intervention receipt epochs: %w", err)
	}
	if err := validateRetainedInterventionReceiptEpochs(currentEpoch, retainedEpochs); err != nil {
		return nil, fmt.Errorf("validate retained intervention receipt epochs: %w", err)
	}

	keyEpoch, err := intervention.NewKeyEpoch(
		currentEpoch,
		vault.DeriveKey(interventionChannelKeyDomain),
		vault.DeriveKey(interventionOccurrenceKeyDomain),
		vault.DeriveKey(interventionContentKeyDomain),
		vault.DeriveKey(interventionReceiptKeyDomain),
	)
	if err != nil {
		return nil, fmt.Errorf("construct intervention key epoch: %w", err)
	}

	return intervention.NewStaticKeyProvider(keyEpoch), nil
}

func validateRetainedInterventionReceiptEpochs(current [32]byte, retained [][32]byte) error {
	var zero [32]byte
	for index, epoch := range retained {
		if epoch == zero {
			return fmt.Errorf("retained intervention receipt epoch %d is malformed", index)
		}
		if epoch != current {
			return fmt.Errorf("retained intervention receipt epoch %d does not match the current vault", index)
		}
		if index > 0 {
			return fmt.Errorf("retained intervention receipt epochs are not distinct")
		}
	}
	return nil
}
