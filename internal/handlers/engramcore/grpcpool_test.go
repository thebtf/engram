package engramcore

import (
	"strings"
	"testing"
)

func TestSafeRemoteURL_RemovesUserinfoAndDropsMalformedRemote(t *testing.T) {
	for _, tt := range []struct {
		raw  string
		want string
	}{
		{raw: "https://fixture-user:fixture-credential@example.invalid/acme/identity.git", want: "https://example.invalid/acme/identity.git"},
		{raw: "//fixture-user:fixture-credential@example.invalid/%zz", want: "[invalid remote URL]"},
	} {
		if got := safeRemoteURL(tt.raw); got != tt.want || strings.Contains(got, "fixture-credential") {
			t.Fatal("credential-bearing remote was not sanitized")
		}
	}
}
