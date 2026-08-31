package redaction

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

func TestCompiledRulesFingerprintExactOrderedEncoding(t *testing.T) {
	rules := []Rule{
		{ID: "credential", Pattern: `token=[^[:space:]]+`, Replacement: "[redacted]"},
		{ID: "address", Pattern: `mail=[^[:space:]]+`, Replacement: "[hidden]"},
	}
	compiled, err := CompileRules(rules)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := CompiledRulesFingerprint(compiled), compiledRulesFingerprintReference(rules); got != want {
		t.Fatalf("CompiledRulesFingerprint() = %q, want %q", got, want)
	}
}

func TestCompiledRulesFingerprintEmptyStable(t *testing.T) {
	if got, want := CompiledRulesFingerprint(nil), compiledRulesFingerprintReference(nil); got != want {
		t.Fatalf("CompiledRulesFingerprint(nil) = %q, want %q", got, want)
	}
}

func TestCompiledRulesFingerprintDetectsOrderAndContentDrift(t *testing.T) {
	original := []Rule{
		{ID: "first", Pattern: "first", Replacement: "x"},
		{ID: "second", Pattern: "second", Replacement: "y"},
	}
	reordered := []Rule{original[1], original[0]}
	changed := append([]Rule(nil), original...)
	changed[1].Replacement = "z"

	originalCompiled, err := CompileRules(original)
	if err != nil {
		t.Fatal(err)
	}
	reorderedCompiled, err := CompileRules(reordered)
	if err != nil {
		t.Fatal(err)
	}
	changedCompiled, err := CompileRules(changed)
	if err != nil {
		t.Fatal(err)
	}
	originalFingerprint := CompiledRulesFingerprint(originalCompiled)
	if originalFingerprint == CompiledRulesFingerprint(reorderedCompiled) {
		t.Fatal("rule order did not change the execution fingerprint")
	}
	if originalFingerprint == CompiledRulesFingerprint(changedCompiled) {
		t.Fatal("rule content did not change the execution fingerprint")
	}
}

func compiledRulesFingerprintReference(rules []Rule) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("engram.redaction-rules/v1"))
	var count [4]byte
	binary.BigEndian.PutUint32(count[:], uint32(len(rules)))
	_, _ = digest.Write(count[:])
	for _, rule := range rules {
		writeFingerprintReferenceText(digest, rule.ID)
		writeFingerprintReferenceText(digest, rule.Pattern)
		writeFingerprintReferenceText(digest, rule.Replacement)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func writeFingerprintReferenceText(digest interface{ Write([]byte) (int, error) }, value string) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(value))
}
