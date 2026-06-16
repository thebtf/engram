package embedding

import "testing"

// TestDeclaredTypeRe pins the parser that turns a format_type() rendering of a
// pgvector column into its integer dimension. The DB-backed assert path is
// exercised in CI (DATABASE_DSN); this covers the pure parsing contract so a
// regex regression is caught without a database.
func TestDeclaredTypeRe(t *testing.T) {
	cases := []struct {
		in      string
		wantN   string
		wantHit bool
	}{
		{"vector(1536)", "1536", true},
		{"vector(4096)", "4096", true},
		{"halfvec(1536)", "1536", true},
		{"vector", "", false},       // unparameterized — must not match
		{"vector(1536) ", "", false}, // trailing space — anchored, must not match
		{"text", "", false},
		{"numeric(10,2)", "", false},
	}
	for _, c := range cases {
		m := declaredTypeRe.FindStringSubmatch(c.in)
		if c.wantHit {
			if m == nil {
				t.Errorf("%q: expected match, got none", c.in)
				continue
			}
			if m[1] != c.wantN {
				t.Errorf("%q: dimension = %q, want %q", c.in, m[1], c.wantN)
			}
		} else if m != nil {
			t.Errorf("%q: expected no match, got %v", c.in, m)
		}
	}
}

// TestEmbeddingDimValue pins the SSOT value so an accidental edit is caught and
// forces a conscious migration-paired change.
func TestEmbeddingDimValue(t *testing.T) {
	if EmbeddingDim != 1536 {
		t.Fatalf("EmbeddingDim = %d, want 1536 (changing it requires a paired schema migration + re-embed)", EmbeddingDim)
	}
}
