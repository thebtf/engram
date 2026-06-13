package privacy

import (
	"strings"
	"testing"
)

// =============================================================================
// CONTRACT: ContainsSecrets
//
// Returns true when the input text matches at least one of the compiled
// secret patterns; false otherwise.  An empty string always returns false.
// =============================================================================

func TestContainsSecrets_EmptyString(t *testing.T) {
	if ContainsSecrets("") {
		t.Error("empty string should not be detected as containing secrets")
	}
}

func TestContainsSecrets_PlainText(t *testing.T) {
	if ContainsSecrets("This is just some regular text about a bug fix") {
		t.Error("plain prose should not be detected as containing secrets")
	}
}

func TestContainsSecrets_APIKeyPattern(t *testing.T) {
	// Construct programmatically to avoid triggering secret scanners on literal values.
	if !ContainsSecrets("api_key=" + strings.Repeat("a", 36)) {
		t.Error("api_key= assignment with long value should be detected")
	}
}

func TestContainsSecrets_APIKeyWithDash(t *testing.T) {
	// Construct programmatically to avoid triggering secret scanners on literal values.
	if !ContainsSecrets(`api-key: "` + strings.Repeat("b", 27) + `"`) {
		t.Error("api-key: assignment with long value should be detected")
	}
}

func TestContainsSecrets_PasswordInConfig(t *testing.T) {
	if !ContainsSecrets(`password="super_secret_password_123"`) {
		t.Error("password= with a long quoted value should be detected")
	}
}

func TestContainsSecrets_OpenAIKey(t *testing.T) {
	if !ContainsSecrets("sk-abc123def456ghi789jkl012mno345pqr678") {
		t.Error("sk- prefixed token should be detected as an OpenAI key")
	}
}

func TestContainsSecrets_AnthropicKey(t *testing.T) {
	if !ContainsSecrets("sk-ant-api03-abc123def456ghi789jkl012mno345") {
		t.Error("sk-ant- prefixed token should be detected as an Anthropic key")
	}
}

func TestContainsSecrets_GitHubPAT(t *testing.T) {
	if !ContainsSecrets("ghp_1234567890abcdefghijklmnopqrstuvwxyz") {
		t.Error("ghp_ prefixed token should be detected as a GitHub PAT")
	}
}

func TestContainsSecrets_GitHubPATNewFormat(t *testing.T) {
	if !ContainsSecrets("github_pat_12ABCDEFGHIJ3456789abc_defghijklmno") {
		t.Error("github_pat_ prefixed token should be detected")
	}
}

func TestContainsSecrets_AWSAccessKey(t *testing.T) {
	if !ContainsSecrets("AKIAIOSFODNN7EXAMPLE") {
		t.Error("AKIA-prefixed string should be detected as an AWS access key")
	}
}

func TestContainsSecrets_PrivateKeyHeader(t *testing.T) {
	// The header-only pattern fires on the BEGIN line alone.
	// NOTE: see issue #263 — the full PEM body is not redacted when a complete
	// BEGIN...END block is present; only the header line is reliably matched.
	if !ContainsSecrets("-----BEGIN RSA PRIVATE KEY-----") {
		t.Error("-----BEGIN ... PRIVATE KEY----- header should be detected")
	}
}

func TestContainsSecrets_JWTToken(t *testing.T) {
	// Construct a synthetic three-segment dot-separated base64url value that triggers
	// the JWT pattern without embedding a realistic-looking token literal.
	jwt := strings.Repeat("eyJ", 1) + strings.Repeat("a", 33) + "." +
		strings.Repeat("eyJ", 1) + strings.Repeat("b", 25) + "." +
		strings.Repeat("c", 43)
	if !ContainsSecrets(jwt) {
		t.Error("base64-structured JWT token should be detected")
	}
}

func TestContainsSecrets_BearerToken(t *testing.T) {
	if !ContainsSecrets("Bearer abc123def456ghi789jkl012mno345") {
		t.Error("Bearer header with long token should be detected")
	}
}

func TestContainsSecrets_SecretKey(t *testing.T) {
	if !ContainsSecrets(`secret_key = "my_super_secret_token_here"`) {
		t.Error("secret_key assignment should be detected")
	}
}

// --- Negative cases: values that should not trigger detection ---

func TestContainsSecrets_ShortPassword(t *testing.T) {
	// password= with a short quoted value falls below the minimum length threshold.
	if ContainsSecrets(`password="short"`) {
		t.Error("short password value should not be detected")
	}
}

func TestContainsSecrets_PasswordWordInSentence(t *testing.T) {
	if ContainsSecrets("The password field should be validated") {
		t.Error("the word 'password' in prose without an assignment should not be detected")
	}
}

func TestContainsSecrets_APIWordInSentence(t *testing.T) {
	if ContainsSecrets("The API returns JSON data") {
		t.Error("the word 'API' in prose without a key assignment should not be detected")
	}
}

// =============================================================================
// CONTRACT: RedactSecrets
//
// Replaces each matched secret with [REDACTED:<8-hex-hash>].  When the match
// includes a key= or key: prefix, the prefix is preserved and only the value
// is replaced.  For standalone token patterns (sk-, ghp_, etc.) the first 4
// characters are kept as a prefix hint followed by "...[REDACTED:hash]".
// An empty string is returned unchanged.
// =============================================================================

func TestRedactSecrets_EmptyString(t *testing.T) {
	if RedactSecrets("") != "" {
		t.Error("RedactSecrets on empty string must return empty string")
	}
}

func TestRedactSecrets_NoSecrets(t *testing.T) {
	input := "This is safe text"
	if got := RedactSecrets(input); got != input {
		t.Errorf("RedactSecrets(%q) = %q, want unchanged %q", input, got, input)
	}
}

func TestRedactSecrets_APIKeyRedactedWithHash(t *testing.T) {
	input := "api_key=abc123def456ghi789jkl012mno345pqr678"
	got := RedactSecrets(input)
	want := "api_key=[REDACTED:586f23e7]"
	if got != want {
		t.Errorf("RedactSecrets(%q) = %q, want %q", input, got, want)
	}
}

func TestRedactSecrets_OpenAIKeyRedactedWithHash(t *testing.T) {
	input := "The key is sk-abc123def456ghi789jkl012mno345pqr678"
	got := RedactSecrets(input)
	want := "The key is sk-a...[REDACTED:c99e03e4]"
	if got != want {
		t.Errorf("RedactSecrets(%q) = %q, want %q", input, got, want)
	}
}

// TestRedactSecrets_PrivateKeyHeaderRedacted verifies that the BEGIN-line-only
// pattern redacts the header when the full PEM block is present.
// NOTE: see issue #263 — when a full BEGIN...END block is present, only the
// header line is reliably redacted by the header-only pattern; the full block
// redaction may not behave as intended.  This test asserts only what the
// current implementation actually does: the output does not contain the raw
// BEGIN marker.
func TestRedactSecrets_PrivateKeyHeaderRedacted(t *testing.T) {
	input := strings.Join([]string{
		"-----BEGIN RSA PRIVATE KEY-----",
		"MIIBOgIBAAJBALfakefakefakefakefakefakefake",
		"-----END RSA PRIVATE KEY-----",
	}, "\n")
	got := RedactSecrets(input)
	if strings.Contains(got, "-----BEGIN RSA PRIVATE KEY-----") {
		t.Errorf("RedactSecrets should redact PEM header; got %q", got)
	}
	if !strings.Contains(got, "[REDACTED:") {
		t.Errorf("RedactSecrets should insert a REDACTED marker; got %q", got)
	}
}

// =============================================================================
// CONTRACT: RedactSecrets ↔ ExtractSecrets hash consistency
//
// For any input that ExtractSecrets reports as containing secrets, each
// returned secret's hash suffix (stripped from "auto:<hash>") must appear
// as a [REDACTED:<hash>] marker in the RedactSecrets output.
// =============================================================================

func TestRedactSecrets_HashMatchesExtractSecrets(t *testing.T) {
	inputs := []string{
		"api_key=abc123def456ghi789jkl012mno345pqr678",
		"sk-abc123def456ghi789jkl012mno345pqr678",
		"ghp_1234567890abcdefghijklmnopqrstuvwxyz",
		`password="super_secret_password_123"`,
		`secret_key="my_super_secret_token_here_now"`,
	}

	for _, input := range inputs {
		label := input
		if len(label) > 20 {
			label = label[:20]
		}
		t.Run(label, func(t *testing.T) {
			extracted := ExtractSecrets(input)
			if len(extracted) == 0 {
				t.Fatalf("ExtractSecrets(%q) returned no secrets", input)
			}
			redacted := RedactSecrets(input)
			for _, secret := range extracted {
				hash := strings.TrimPrefix(secret.Name, "auto:")
				marker := "[REDACTED:" + hash + "]"
				if !strings.Contains(redacted, marker) {
					t.Errorf("redacted output %q does not contain expected marker %q (from name %q)",
						redacted, marker, secret.Name)
				}
			}
		})
	}
}

// =============================================================================
// CONTRACT: ExtractSecrets
//
// Scans text for all secret patterns and returns unique DetectedSecret values.
// Each secret has:
//   - Name in the form "auto:<sha256hex-prefix>" (always starts with "auto:")
//   - Value set to the raw matched secret (non-empty)
//
// Deduplication: the same raw secret matched by multiple patterns or at
// multiple positions produces only one entry.
// Determinism: two calls on the same input return identical slices.
// =============================================================================

func TestExtractSecrets_EmptyString(t *testing.T) {
	if got := ExtractSecrets(""); got != nil {
		t.Errorf("ExtractSecrets(\"\") = %v, want nil", got)
	}
}

func TestExtractSecrets_NoSecrets(t *testing.T) {
	if got := ExtractSecrets("This is just regular text"); len(got) != 0 {
		t.Errorf("ExtractSecrets(plain text) returned %d secrets, want 0", len(got))
	}
}

func TestExtractSecrets_SingleOpenAIKey(t *testing.T) {
	got := ExtractSecrets("key is sk-abc123def456ghi789jkl012mno345pqr678")
	if len(got) != 1 {
		t.Errorf("want 1 secret, got %d", len(got))
	}
}

func TestExtractSecrets_DuplicateSecretDeduplicated(t *testing.T) {
	// Construct programmatically to avoid triggering secret scanners on literal values.
	key := "sk-" + strings.Repeat("a", 36)
	input := key + " and again " + key
	got := ExtractSecrets(input)
	if len(got) != 1 {
		t.Errorf("same secret repeated should deduplicate to 1 entry, got %d", len(got))
	}
}

func TestExtractSecrets_TwoDifferentSecrets(t *testing.T) {
	input := "sk-abc123def456ghi789jkl012mno345pqr678 and ghp_1234567890abcdefghijklmnopqrstuvwxyz"
	got := ExtractSecrets(input)
	if len(got) != 2 {
		t.Errorf("want 2 distinct secrets, got %d", len(got))
	}
}

func TestExtractSecrets_APIKeyWithPrefix(t *testing.T) {
	// Construct the input programmatically to avoid triggering secret scanners.
	input := "api_key=" + strings.Repeat("a", 36)
	got := ExtractSecrets(input)
	if len(got) != 1 {
		t.Errorf("api_key with long value should produce 1 secret, got %d", len(got))
	}
}

// TestExtractSecrets_NamesStartWithAuto verifies the naming contract for every
// detected secret: name must be "auto:<suffix>" and value must be non-empty.
func TestExtractSecrets_NamesStartWithAuto(t *testing.T) {
	inputs := []string{
		"sk-abc123def456ghi789jkl012mno345pqr678",
		"ghp_1234567890abcdefghijklmnopqrstuvwxyz",
		"AKIAIOSFODNN7EXAMPLE",
		"api_key=" + strings.Repeat("z", 30),
	}
	for _, input := range inputs {
		t.Run(input[:min(20, len(input))], func(t *testing.T) {
			results := ExtractSecrets(input)
			for _, s := range results {
				if !strings.HasPrefix(s.Name, "auto:") {
					t.Errorf("secret name %q does not start with 'auto:'", s.Name)
				}
				if s.Value == "" {
					t.Error("secret Value must not be empty")
				}
			}
		})
	}
}

// TestExtractSecrets_Deterministic ensures two successive calls on the same input
// produce identical results (same count, same names in the same order).
func TestExtractSecrets_Deterministic(t *testing.T) {
	input := "sk-abc123def456ghi789jkl012mno345pqr678 and ghp_1234567890abcdefghijklmnopqrstuvwxyz"
	r1 := ExtractSecrets(input)
	r2 := ExtractSecrets(input)
	if len(r1) != len(r2) {
		t.Fatalf("non-deterministic: call 1 returned %d, call 2 returned %d", len(r1), len(r2))
	}
	for i := range r1 {
		if r1[i].Name != r2[i].Name {
			t.Errorf("result[%d] name differs between calls: %q vs %q", i, r1[i].Name, r2[i].Name)
		}
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkContainsSecrets_NoSecret(b *testing.B) {
	text := "This is a normal piece of text that does not contain any secrets or sensitive information"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ContainsSecrets(text)
	}
}

func BenchmarkContainsSecrets_WithSecret(b *testing.B) {
	text := "api_key=" + strings.Repeat("k", 36)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ContainsSecrets(text)
	}
}

