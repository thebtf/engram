package engramcore

import (
	"strings"
	"testing"
)

func TestProjectReadDigestSemantics(t *testing.T) {
	a, ok := projectReadDigest([]byte(`{"query":"fixture","params":{"x":1,"y":[true,123456789012345678901234567890.0]}}`))
	if !ok {
		t.Fatal("valid arguments rejected")
	}
	b, ok := projectReadDigest([]byte(`{"params":{"y":[true,12345678901234567890123456789e1],"x":1.00},"query":"fixture"}`))
	if !ok || a != b {
		t.Fatal("equal JSON values differ")
	}
	c, ok := projectReadDigest([]byte(`{"query":"fixture","params":{"x":1,"y":[true,123456789012345678901234567891]}}`))
	if !ok || a == c {
		t.Fatal("changed large number conflated")
	}
	for _, raw := range []string{`{"query":"a","query":"b"}`, `{"nested":{"x":1,"x":2}}`, `{"project":"x"}`, `{"query":"a"} {}`} {
		if _, ok := projectReadDigest([]byte(raw)); ok {
			t.Fatalf("ambiguous input accepted: %s", raw)
		}
	}
}

func TestProjectReadDigestExponentBound(t *testing.T) {
	for _, raw := range []string{`{"value":1e999999999}`, `{"value":1e-999999999}`} {
		if _, ok := projectReadDigest([]byte(raw)); !ok {
			t.Fatalf("bounded exponent rejected: %s", raw)
		}
	}
	for _, exponent := range []string{"1000000000", "-1000000000", strings.Repeat("9", 100000)} {
		if _, ok := projectReadDigest([]byte(`{"value":1e` + exponent + `}`)); ok {
			t.Fatal("oversized exponent accepted for scoped binding")
		}
	}
}
