package privacy

import (
	"strings"
	"testing"
)

func TestContainsSecrets(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "empty string",
			input:    "",
			expected: false,
		},
		{
			name:     "normal text",
			input:    "This is just some regular text about a bug fix",
			expected: false,
		},
		{
			name:     "API key pattern",
			input:    "api_key=abc123def456ghi789jkl012mno345pqr678",
			expected: true,
		},
		{
			name:     "api-key with dash",
			input:    `api-key: "abc123def456ghi789jkl012mno"`,
			expected: true,
		},
		{
			name:     "password in config",
			input:    `password="super_secret_password_123"`,
			expected: true,
		},
		{
			name:     "OpenAI key format",
			input:    "sk-abc123def456ghi789jkl012mno345pqr678",
			expected: true,
		},
		{
			name:     "Anthropic key format",
			input:    "sk-ant-api03-abc123def456ghi789jkl012mno345",
			expected: true,
		},
		{
			name:     "GitHub PAT",
			input:    "ghp_1234567890abcdefghijklmnopqrstuvwxyz",
			expected: true,
		},
		{
			name:     "GitHub PAT new format",
			input:    "github_pat_12ABCDEFGHIJ3456789abc_defghijklmno",
			expected: true,
		},
		{
			name:     "AWS access key",
			input:    "AKIAIOSFODNN7EXAMPLE",
			expected: true,
		},
		{
			name:     "Private key header",
			input:    "-----BEGIN RSA PRIVATE KEY-----",
			expected: true,
		},
		{
			name:     "JWT token",
			input:    "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
			expected: true,
		},
		{
			name:     "bearer token",
			input:    "Bearer abc123def456ghi789jkl012mno345",
			expected: true,
		},
		{
			name:     "secret_key in code",
			input:    `secret_key = "my_super_secret_token_here"`,
			expected: true,
		},
		{
			name:     "short password is not detected",
			input:    `password="short"`,
			expected: false, // Too short to trigger
		},
		{
			name:     "word password in sentence",
			input:    "The password field should be validated",
			expected: false,
		},
		{
			name:     "word api in code",
			input:    "The API returns JSON data",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ContainsSecrets(tt.input)
			if result != tt.expected {
				t.Errorf("ContainsSecrets(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestRedactSecrets(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "no secrets",
			input:    "This is safe text",
			expected: "This is safe text",
		},
		{
			name:     "API key gets redacted with hash",
			input:    "api_key=abc123def456ghi789jkl012mno345pqr678",
			expected: "api_key=[REDACTED:586f23e7]",
		},
		{
			name:     "OpenAI key gets redacted with hash",
			input:    "The key is sk-abc123def456ghi789jkl012mno345pqr678",
			expected: "The key is sk-a...[REDACTED:c99e03e4]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RedactSecrets(tt.input)
			if result != tt.expected {
				t.Errorf("RedactSecrets(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}


func TestRedactSecretsHashMatchesExtract(t *testing.T) {
	inputs := []string{
		"api_key=abc123def456ghi789jkl012mno345pqr678",
		"sk-abc123def456ghi789jkl012mno345pqr678",
		"ghp_1234567890abcdefghijklmnopqrstuvwxyz",
		`password="super_secret_password_123"`,
		`secret_key="my_super_secret_token_here_now"`,
	}

	for _, input := range inputs {
		name := input
		if len(name) > 20 {
			name = name[:20]
		}
		t.Run(name, func(t *testing.T) {
			extracted := ExtractSecrets(input)
			if len(extracted) == 0 {
				t.Fatalf("ExtractSecrets(%q) returned no secrets", input)
			}
			redacted := RedactSecrets(input)

			// The hash in [REDACTED:{hash}] must match the auto:{hash} name from ExtractSecrets
			for _, secret := range extracted {
				hashFromName := strings.TrimPrefix(secret.Name, "auto:")
				marker := "[REDACTED:" + hashFromName + "]"
				if !strings.Contains(redacted, marker) {
					t.Errorf("redacted output %q does not contain expected marker %q (from ExtractSecrets name %q)",
						redacted, marker, secret.Name)
				}
			}
		})
	}
}

func TestExtractSecrets(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		expectedCount int
		expectedNames []string // subset of names to verify
	}{
		{
			name:          "empty string",
			input:         "",
			expectedCount: 0,
		},
		{
			name:          "no secrets",
			input:         "This is just regular text",
			expectedCount: 0,
		},
		{
			name:          "single OpenAI key",
			input:         "key is sk-abc123def456ghi789jkl012mno345pqr678",
			expectedCount: 1,
		},
		{
			name:          "duplicate same secret",
			input:         "sk-abc123def456ghi789jkl012mno345pqr678 and again sk-abc123def456ghi789jkl012mno345pqr678",
			expectedCount: 1, // deduped by hash
		},
		{
			name:          "two different secrets",
			input:         "sk-abc123def456ghi789jkl012mno345pqr678 and ghp_1234567890abcdefghijklmnopqrstuvwxyz",
			expectedCount: 2,
		},
		{
			name:          "API key with prefix",
			input:         "api_key=" + strings.Repeat("a", 36), // generated to avoid secret scanner false positives
			expectedCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := ExtractSecrets(tt.input)
			if len(results) != tt.expectedCount {
				t.Errorf("ExtractSecrets() returned %d secrets, want %d", len(results), tt.expectedCount)
				for _, s := range results {
					t.Logf("  name=%s value=%s", s.Name, s.Value)
				}
			}
			// Verify all names start with "auto:"
			for _, s := range results {
				if len(s.Name) < 5 || s.Name[:5] != "auto:" {
					t.Errorf("secret name %q does not start with 'auto:'", s.Name)
				}
				if s.Value == "" {
					t.Error("secret value is empty")
				}
			}
			// Verify deterministic: same input = same names
			if tt.expectedCount > 0 {
				results2 := ExtractSecrets(tt.input)
				if len(results) != len(results2) {
					t.Fatalf("non-deterministic: got %d then %d", len(results), len(results2))
				}
				for i := range results {
					if results[i].Name != results2[i].Name {
						t.Errorf("non-deterministic names: %q vs %q", results[i].Name, results2[i].Name)
					}
				}
			}
		})
	}
}

func BenchmarkContainsSecrets(b *testing.B) {
	text := "This is a normal piece of text that does not contain any secrets or sensitive information"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ContainsSecrets(text)
	}
}

func BenchmarkContainsSecretsWithSecret(b *testing.B) {
	text := "api_key=abc123def456ghi789jkl012mno345pqr678"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ContainsSecrets(text)
	}
}
