package uci_test

import (
	"testing"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/uci"
)

func TestCanonicalAuthBrowserSubjectSatisfiesReleaseCallerContract(t *testing.T) {
	caller := uci.BrowserReleaseCaller{
		Subject:         auth.BrowserSubjectForUser(73),
		SessionID:       "browser-session-73",
		DocumentBinding: "50000000-0000-4000-8000-000000000073",
	}
	if !caller.Subject.Valid() {
		t.Fatal("canonical auth browser subject did not satisfy UCI release caller contract")
	}
}
