package intervention

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"hash"
)

const (
	channelDerivationDomain    = "engram.host-channel/v1"
	occurrenceDerivationDomain = "engram.host-occurrence/v1"
	contentDerivationDomain    = "engram.host-content/v1"
)

// KeyEpoch contains exactly one current Vault epoch commitment and its four
// domain-separated derived keys. It never exposes the Vault master key.
type KeyEpoch struct {
	epoch      [32]byte
	channel    [32]byte
	occurrence [32]byte
	content    [32]byte
	receipt    [32]byte
}

// NewKeyEpoch validates one full-width epoch commitment and all four distinct
// non-zero derived key values.
func NewKeyEpoch(epoch, channel, occurrence, content, receipt [32]byte) (KeyEpoch, error) {
	if epoch == ([32]byte{}) || channel == ([32]byte{}) || occurrence == ([32]byte{}) || content == ([32]byte{}) || receipt == ([32]byte{}) ||
		channel == occurrence || channel == content || channel == receipt || occurrence == content || occurrence == receipt || content == receipt {
		return KeyEpoch{}, ErrInvalidInput
	}
	return KeyEpoch{
		epoch:      epoch,
		channel:    channel,
		occurrence: occurrence,
		content:    content,
		receipt:    receipt,
	}, nil
}

// EpochCommitment returns the full-width commitment of the current Vault epoch.
func (e KeyEpoch) EpochCommitment() [32]byte {
	return e.epoch
}

// ChannelKey returns the HAP-03 channel derivation key.
func (e KeyEpoch) ChannelKey() [32]byte {
	return e.channel
}

// OccurrenceKey returns the HAP-03 occurrence derivation key.
func (e KeyEpoch) OccurrenceKey() [32]byte {
	return e.occurrence
}

// ContentKey returns the HAP-03 content derivation key.
func (e KeyEpoch) ContentKey() [32]byte {
	return e.content
}

// ReceiptKey returns the HAP-03 receipt integrity key.
func (e KeyEpoch) ReceiptKey() [32]byte {
	return e.receipt
}

func (e KeyEpoch) valid() bool {
	return e.epoch != ([32]byte{}) && e.channel != ([32]byte{}) && e.occurrence != ([32]byte{}) && e.content != ([32]byte{}) && e.receipt != ([32]byte{})
}

// KeyProvider exposes only the one current HAP-03 epoch. Historical rotation is
// deliberately not modeled by this T01 boundary.
type KeyProvider interface {
	Current() (KeyEpoch, bool)
}

type staticKeyProvider struct {
	epoch KeyEpoch
	valid bool
}

// NewStaticKeyProvider adapts one validated current epoch to the narrow runtime port.
func NewStaticKeyProvider(epoch KeyEpoch) KeyProvider {
	return staticKeyProvider{epoch: epoch, valid: epoch.valid()}
}

func (p staticKeyProvider) Current() (KeyEpoch, bool) {
	if !p.valid {
		return KeyEpoch{}, false
	}
	return p.epoch, true
}

// DeriveChannel commits only the stable authenticated host channel identity.
func (e KeyEpoch) DeriveChannel(binding BindingFacts) (Digest, error) {
	if !e.valid() || !binding.valid() {
		return Digest{}, ErrInvalidInput
	}
	encoder := newHMACEncoder(e.channel)
	encoder.text(channelDerivationDomain)
	encoder.digest(binding.subject)
	encoder.uint32(uint32(binding.hostFamily))
	encoder.digest(binding.channelCommitment)
	return encoder.sum(), nil
}

// DeriveOccurrence commits the binding-independent authorized occurrence axis.
func (e KeyEpoch) DeriveOccurrence(identity OccurrenceIdentity) (Digest, error) {
	if !e.valid() || !identity.valid() {
		return Digest{}, ErrInvalidInput
	}
	encoder := newHMACEncoder(e.occurrence)
	encoder.text(occurrenceDerivationDomain)
	encoder.digest(identity.subject)
	encoder.text(identity.canonicalProject)
	encoder.text(identity.actorPrincipal)
	encoder.text(identity.actorKind)
	encoder.text(identity.workstation)
	encoder.text(identity.occurrence.sessionRef)
	encoder.uint32(uint32(SemanticBeforeAgentStart))
	encoder.text(identity.occurrence.phaseAnchorRef)
	return encoder.sum(), nil
}

// DeriveContent commits only the canonical task/query facts and the explicit
// H03 predecessor-absent marker. It intentionally excludes binding, authority,
// session, anchor, and policy material.
func (e KeyEpoch) DeriveContent(occurrence BeforeAgentStartOccurrence) (Digest, error) {
	if !e.valid() || !occurrence.valid() {
		return Digest{}, ErrInvalidInput
	}
	encoder := newHMACEncoder(e.content)
	encoder.text(contentDerivationDomain)
	encoder.text(occurrence.facts.task.Query())
	encoder.uint32(uint32(len(occurrence.facts.facts)))
	for _, fact := range occurrence.facts.facts {
		encoder.uint32(uint32(fact.kind))
		encoder.text(fact.value)
	}
	encoder.boolean(false) // H03 Advise predecessors are structurally absent.
	return encoder.sum(), nil
}

type hmacEncoder struct {
	mac hash.Hash
}

func newHMACEncoder(key [32]byte) hmacEncoder {
	return hmacEncoder{mac: hmac.New(sha256.New, key[:])}
}

func (e hmacEncoder) sum() Digest {
	var result Digest
	sum := e.mac.Sum(result[:0])
	copy(result[:], sum)
	return result
}

func (e hmacEncoder) bytes(value []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = e.mac.Write(length[:])
	_, _ = e.mac.Write(value)
}

func (e hmacEncoder) text(value string) {
	e.bytes([]byte(value))
}

func (e hmacEncoder) digest(value Digest) {
	e.bytes(value[:])
}

func (e hmacEncoder) uint32(value uint32) {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	_, _ = e.mac.Write(encoded[:])
}

func (e hmacEncoder) boolean(value bool) {
	if value {
		e.uint32(1)
		return
	}
	e.uint32(0)
}
